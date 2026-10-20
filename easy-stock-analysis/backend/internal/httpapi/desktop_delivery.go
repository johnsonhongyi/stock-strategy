package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Notifications are hints; committed SQLite state and lease tokens are authoritative.
type desktopSignals struct {
	mu        sync.Mutex
	listeners map[string]map[chan struct{}]struct{}
	count     int
	closed    bool
}

func (s *desktopSignals) subscribe(key string) (<-chan struct{}, func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.count >= 128 {
		return nil, func() {}, false
	}
	if s.listeners == nil {
		s.listeners = make(map[string]map[chan struct{}]struct{})
	}
	if s.listeners[key] == nil {
		s.listeners[key] = make(map[chan struct{}]struct{})
	}
	ch := make(chan struct{}, 1)
	s.listeners[key][ch] = struct{}{}
	s.count++
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.listeners[key], ch)
			s.count--
			if len(s.listeners[key]) == 0 {
				delete(s.listeners, key)
			}
		})
	}, true
}
func (s *desktopSignals) notify(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.listeners[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func desktopNodeKey(node, workspace string) string { return "node:" + node + ":" + workspace }

func (s *desktopSignals) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, listeners := range s.listeners {
		for ch := range listeners {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}
func (s *desktopSignals) isClosed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }

func (s *Server) desktopDB() (*sql.DB, error) {
	s.desktopDBMu.Lock()
	defer s.desktopDBMu.Unlock()
	if s.desktopSignals.isClosed() {
		return nil, errors.New("desktop delivery is closed")
	}
	if s.desktopChatDB != nil {
		return s.desktopChatDB, nil
	}
	db, err := s.chatDB()
	if err == nil {
		s.desktopChatDB = db
	}
	return db, err
}

type desktopPeerFailure struct {
	Code   string
	Status int
}

func (s *Server) pruneDesktopJobs(ctx context.Context, db *sql.DB) error {
	s.desktopDBMu.Lock()
	due := time.Since(s.desktopPrunedAt) > time.Minute
	if due {
		s.desktopPrunedAt = time.Now()
	}
	s.desktopDBMu.Unlock()
	if !due {
		return nil
	}
	now := time.Now().Unix()
	if _, err := db.ExecContext(ctx, "UPDATE desktop_chat_jobs SET status='expired' WHERE expires_at<=? AND status IN ('queued','running')", now); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM desktop_chat_sessions WHERE owner LIKE 'stock-research-%' AND id IN (SELECT session_id FROM desktop_chat_jobs WHERE expires_at<? AND status NOT IN ('queued','running')) AND NOT EXISTS (SELECT 1 FROM desktop_chat_jobs j WHERE j.session_id=desktop_chat_sessions.id AND j.expires_at>=?)", now-86400, now-86400); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, "DELETE FROM desktop_chat_jobs WHERE id IN (SELECT id FROM desktop_chat_jobs WHERE expires_at<? AND status NOT IN ('queued','running') LIMIT 500)", now-86400)
	return err
}

func (e desktopPeerFailure) Error() string { return e.Code }
func writeDesktopPeerError(w http.ResponseWriter, err error) {
	var failure desktopPeerFailure
	if errors.As(err, &failure) {
		writeJSON(w, failure.Status, map[string]string{"error": failure.Code, "code": failure.Code})
		return
	}
	// Database/transport failures are retryable; protocol validation is explicit below.
	writeJSON(w, 503, map[string]string{"error": "desktop queue temporarily unavailable", "code": "CHAT_QUEUE_UNAVAILABLE"})
}

func (s *Server) desktopNext(w http.ResponseWriter, r *http.Request, node, workspace string, waitMS int, preferChat bool) {
	db, err := s.desktopDB()
	if err != nil {
		writeDesktopPeerError(w, err)
		return
	}
	if err = s.pruneDesktopJobs(r.Context(), db); err != nil {
		writeDesktopPeerError(w, err)
		return
	}
	wake, unsubscribe, ok := s.desktopSignals.subscribe(desktopNodeKey(node, workspace))
	if !ok {
		writeError(w, 429, "too many desktop waiters")
		return
	}
	defer unsubscribe()
	deadline := time.NewTimer(time.Duration(max(0, min(20000, waitMS))) * time.Millisecond)
	defer deadline.Stop()
	// Strategy jobs can also be inserted by the external Python worker.
	fallback := time.NewTicker(time.Second)
	defer fallback.Stop()
	for {
		if s.desktopSignals.isClosed() {
			return
		}
		order := []string{"strategy", "chat"}
		if preferChat {
			order = []string{"chat", "strategy"}
		}
		for _, kind := range order {
			var response map[string]any
			if kind == "chat" {
				response, err = desktopChatPeer(db, "chat_claim", node, workspace, "", "", "", nil)
			} else {
				response, err = s.claimAutomaticResearch(r.Context(), db, node, workspace)
			}
			if err != nil {
				writeDesktopPeerError(w, err)
				return
			}
			if response["job"] != nil {
				s.desktopSignals.notify("chat:" + fmt.Sprint(response["job"].(map[string]any)["id"]))
				response["kind"] = kind
				writeJSON(w, 200, response)
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			writeJSON(w, 200, map[string]any{"job": nil})
			return
		case <-wake:
		case <-fallback.C:
		}
	}
}

func streamDesktopSnapshots(w http.ResponseWriter, r *http.Request, wake <-chan struct{}, read func() (any, bool, error)) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming unavailable")
		return
	}
	data, terminal, err := read()
	if err != nil {
		writeError(w, 404, "task unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	var previous []byte
	send := func(value any) bool {
		raw, err := json.Marshal(map[string]any{"data": value})
		if err != nil {
			return false
		}
		if bytes.Equal(previous, raw) {
			return true
		}
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", raw)
		flusher.Flush()
		if err == nil {
			previous = raw
		}
		return err == nil
	}
	if !send(data) || terminal {
		return
	}
	fallback := time.NewTicker(15 * time.Second)
	defer fallback.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-fallback.C:
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err = fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
		data, terminal, err = read()
		if err != nil {
			return
		}
		if !send(data) || terminal {
			return
		}
	}
}
func (s *Server) desktopChatEvents(w http.ResponseWriter, r *http.Request) {
	db, err := s.desktopDB()
	if err != nil {
		writeDesktopPeerError(w, err)
		return
	}
	id, owner := r.PathValue("id"), s.desktopChatOwner(r)
	wake, unsubscribe, ok := s.desktopSignals.subscribe("chat:" + id)
	if !ok {
		writeError(w, 429, "too many streams")
		return
	}
	defer unsubscribe()
	streamDesktopSnapshots(w, r, wake, func() (any, bool, error) {
		if s.desktopSignals.isClosed() {
			return nil, true, errors.New("server closing")
		}
		var status, raw, session string
		var created, expires int64
		err := db.QueryRowContext(r.Context(), "SELECT status,result,session_id,created_at,expires_at FROM desktop_chat_jobs WHERE id=? AND owner=?", id, owner).Scan(&status, &raw, &session, &created, &expires)
		if err == nil && (status == "queued" || status == "running") && (expires <= time.Now().Unix() || status == "queued" && created+90 <= time.Now().Unix()) {
			_, err = db.ExecContext(r.Context(), "UPDATE desktop_chat_jobs SET status='expired' WHERE id=? AND owner=? AND status IN ('queued','running') AND (expires_at<=? OR (status='queued' AND created_at+90<=?))", id, owner, time.Now().Unix(), time.Now().Unix())
			if err == nil {
				err = db.QueryRowContext(r.Context(), "SELECT status,result FROM desktop_chat_jobs WHERE id=? AND owner=?", id, owner).Scan(&status, &raw)
			}
		}
		return map[string]any{"id": id, "session_id": session, "status": status, "result": json.RawMessage(raw)}, status != "queued" && status != "running", err
	})
}
func (s *Server) stockResearchEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.readResearchJob(w, r); !ok {
		return
	}
	_, release, ok := s.desktopSignals.subscribe("research-sse:" + r.PathValue("id"))
	if !ok {
		writeError(w, 429, "too many streams")
		return
	}
	defer release()
	wake, unsubscribe := s.stockResearch.Subscribe(r.PathValue("id"))
	defer unsubscribe()
	streamDesktopSnapshots(w, r, wake, func() (any, bool, error) {
		job, err := s.stockResearchStore.Get(r.Context(), r.PathValue("id"))
		return job.Public(), job.Status != "queued" && job.Status != "running", err
	})
}
