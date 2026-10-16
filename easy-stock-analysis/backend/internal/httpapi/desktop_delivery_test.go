package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"easy-stock/backend/internal/hermes"
	"easy-stock/backend/internal/stockanalysis"
)

func waitDesktopSubscriber(t *testing.T, s *Server, key string) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		s.desktopSignals.mu.Lock()
		n := len(s.desktopSignals.listeners[key])
		s.desktopSignals.mu.Unlock()
		if n > 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("subscriber did not start")
		case <-tick.C:
		}
	}
}

func TestDesktopNextWakesOnCommit(t *testing.T) {
	s, call, exec := aiPeerFixture(t)
	exec("UPDATE ai_jobs SET status='expired'")
	db, err := s.desktopDB()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call("next", map[string]any{"wait_ms": 5000, "prefer_chat": true}) }()
	waitDesktopSubscriber(t, s, desktopNodeKey("desk", "ws"))
	body := `{"model":"gpt-6-luna","reasoning_effort":"low","deadline_unix_ms":` + jsonNumber(time.Now().Add(time.Minute).UnixMilli()) + `}`
	_, err = db.Exec("INSERT INTO desktop_chat_jobs(id,session_id,owner,node_id,workspace_id,body,status,created_at,expires_at) VALUES('job','session','local-desktop','desk','ws',?,'queued',?,?)", body, time.Now().Unix(), time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	s.desktopSignals.notify(desktopNodeKey("desk", "ws"))
	select {
	case response := <-done:
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"kind":"chat"`) {
			t.Fatal(response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("notification did not wake claim")
	}
}
func jsonNumber(value int64) string { raw, _ := json.Marshal(value); return string(raw) }

func TestDesktopDeliveryLatencySamples(t *testing.T) {
	s, call, exec := aiPeerFixture(t)
	exec("UPDATE ai_jobs SET status='expired'")
	db, err := s.desktopDB()
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]time.Duration, 0, 30)
	for i := 0; i < 30; i++ {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- call("next", map[string]any{"wait_ms": 5000, "prefer_chat": true}) }()
		waitDesktopSubscriber(t, s, desktopNodeKey("desk", "ws"))
		id := jsonNumber(int64(i))
		now := time.Now().Unix()
		_, err = db.Exec("INSERT INTO desktop_chat_jobs(id,session_id,owner,node_id,workspace_id,body,status,created_at,expires_at) VALUES(?,'session','local-desktop','desk','ws',?,'queued',?,?)", id, `{"model":"gpt-6-luna","reasoning_effort":"low"}`, now, now+600)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		s.desktopSignals.notify(desktopNodeKey("desk", "ws"))
		select {
		case response := <-done:
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			samples = append(samples, time.Since(started))
		case <-time.After(time.Second):
			t.Fatal("claim wake stalled")
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("isolated notification -> signed handler claim: samples=%d p50=%s p95=%s", len(samples), samples[14], samples[28])
}

func TestDesktopResearchDeadlineLeavesCancellationTombstone(t *testing.T) {
	s, call, exec := aiPeerFixture(t)
	db, err := s.desktopDB()
	if err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO desktop_chat_config VALUES(1,?)", `{"provider":"codex_desktop","node_id":"desk","workspace_id":"ws","model":"gpt-6-luna","reasoning_effort":"low"}`)
	exec("INSERT INTO ai_desktop VALUES(?,?,?,?,?)", "desk", "ws", time.Now().UTC().Format(time.RFC3339Nano), "ready", `{"capabilities":["ai_chat_v1","stock_research_v1"],"models":[{"id":"gpt-6-luna","efforts":["low"]}]}`)
	p, err := s.newStockResearchCodexPrompter(stockanalysis.ResearchRequest{Symbol: "600519.SH", Model: "gpt-6-luna", ReasoningEffort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	wake, release, ok := s.desktopSignals.subscribe(desktopNodeKey("desk", "ws"))
	if !ok {
		t.Fatal("subscription unavailable")
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.PromptWithOptions(ctx, "isolated", hermes.PromptOptions{DisableTools: true})
		done <- err
	}()
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("job not submitted")
	}
	claimed := call("chat_claim", nil)
	var claim struct {
		Job struct {
			ID        string  `json:"id"`
			Token     string  `json:"lease_token"`
			Remaining float64 `json:"remaining_seconds"`
		}
	}
	if json.Unmarshal(claimed.Body.Bytes(), &claim) != nil || claim.Job.Remaining > 0.3 || claim.Job.ID == "" {
		t.Fatal(claimed.Body.String())
	}
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	session := p.session
	p.Close()
	var status string
	if err = db.QueryRow("SELECT status FROM desktop_chat_jobs WHERE session_id=?", session).Scan(&status); err != nil || status != "cancelled" {
		t.Fatal(status, err)
	}
	response := call("chat_result", map[string]any{"id": claim.Job.ID, "lease_token": claim.Job.Token, "status": "running", "result": map[string]string{"model": "gpt-6-luna", "reasoning_effort": "low"}})
	if response.Code != 409 || !strings.Contains(response.Body.String(), "CHAT_CANCELLED") {
		t.Fatal(response.Code, response.Body.String())
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	first chan struct{}
	once  sync.Once
}

func (w *flushRecorder) Flush() { w.ResponseRecorder.Flush(); w.once.Do(func() { close(w.first) }) }
func TestDesktopSSEStreamsCommittedTerminalAndChecksOwner(t *testing.T) {
	s, call, _ := aiPeerFixture(t)
	db, err := s.desktopDB()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO desktop_chat_jobs(id,session_id,owner,node_id,workspace_id,body,status,created_at,expires_at) VALUES('job','session','local-desktop','desk','ws',?,'queued',?,?)", `{"model":"gpt-6-luna","reasoning_effort":"low"}`, time.Now().Unix(), time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/v1/ai/desktop/jobs/job/events", nil)
	request.SetPathValue("id", "job")
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(ctx)
	recorder := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), first: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); s.desktopChatEvents(recorder, request) }()
	select {
	case <-recorder.first:
	case <-time.After(time.Second):
		t.Fatal("initial SSE snapshot missing")
	}
	claim := call("chat_claim", nil)
	var lease struct {
		Job struct {
			Token string `json:"lease_token"`
		}
	}
	_ = json.Unmarshal(claim.Body.Bytes(), &lease)
	response := call("chat_result", map[string]any{"id": "job", "lease_token": lease.Job.Token, "status": "completed", "result": map[string]string{"model": "gpt-6-luna", "reasoning_effort": "low", "thread_id": "01a13f4b-5f5a-7fb0-990c-a22bdbe42f68", "content": "done"}})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("terminal SSE snapshot missing")
	}
	if strings.Count(recorder.Body.String(), "event: snapshot") < 2 || !strings.Contains(recorder.Body.String(), `"status":"completed"`) {
		t.Fatal(recorder.Body.String())
	}
	_, _ = db.Exec("UPDATE desktop_chat_jobs SET owner='other' WHERE id='job'")
	private := httptest.NewRecorder()
	s.desktopChatEvents(private, request)
	if private.Code != 404 || strings.Contains(private.Body.String(), `"content":"done"`) {
		t.Fatal("cross-owner SSE exposed")
	}
}
