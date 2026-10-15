package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func followFixture(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec("CREATE TABLE state(id INTEGER PRIMARY KEY,body TEXT)")
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"mode": "paper", "cash": 500000, "positions": []any{}, "events": []any{}, "closed": []any{map[string]any{"pnl": 10}}, "last_heartbeat": time.Now().UTC().Format(time.RFC3339Nano)}
	body, _ := json.Marshal(state)
	if _, err = db.Exec("INSERT INTO state VALUES(1,?)", string(body)); err != nil {
		t.Fatal(err)
	}
	control, err := sql.Open("sqlite", filepath.Join(filepath.Dir(path), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Close() })
	_, err = control.Exec("CREATE TABLE commands(id TEXT PRIMARY KEY,action TEXT,payload TEXT,created_at TEXT,status TEXT DEFAULT 'pending',applied_at TEXT,result TEXT)")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{autoFollowDBPath: path}, control
}

func TestAutoFollowStatusAndMissingAccount(t *testing.T) {
	s, _ := followFixture(t)
	w := httptest.NewRecorder()
	s.autoFollowStatus(w, httptest.NewRequest("GET", "/api/v1/auto-follow", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var r struct {
		Data struct {
			Available, Online bool
			Summary           struct {
				Realized float64 `json:"realized_pnl"`
			}
		}
	}
	if json.Unmarshal(w.Body.Bytes(), &r) != nil || !r.Data.Available || !r.Data.Online || r.Data.Summary.Realized != 10 {
		t.Fatal(w.Body.String())
	}
	s.autoFollowDBPath = filepath.Join(t.TempDir(), "missing.db")
	w = httptest.NewRecorder()
	s.autoFollowStatus(w, httptest.NewRequest("GET", "/api/v1/auto-follow", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAutoFollowCommandIdempotencyAndValidation(t *testing.T) {
	s, db := followFixture(t)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		s.autoFollowCommand(w, httptest.NewRequest("POST", "/api/v1/auto-follow/commands", strings.NewReader(`{"id":"request-123","action":"pause"}`)))
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM commands").Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"id":"request-123","action":"resume"}`, 409},
		{`{"id":"request-456","action":"real_order"}`, 400},
		{`{"id":"request-456","action":"pause"} {}`, 400},
		{`{"id":"request-456","action":"settings","settings":{"limit_up":true}}`, 400},
	} {
		w := httptest.NewRecorder()
		s.autoFollowCommand(w, httptest.NewRequest("POST", "/api/v1/auto-follow/commands", strings.NewReader(tc.body)))
		if w.Code != tc.code {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}

func TestAutoFollowControlRejectsCrossOrigin(t *testing.T) {
	s, _ := followFixture(t)
	r := httptest.NewRequest("POST", "http://stocks.local/api/v1/auto-follow/commands", strings.NewReader(`{"id":"request-123","action":"pause"}`))
	r.Header.Set("Origin", "https://attacker.invalid")
	w := httptest.NewRecorder()
	s.autoFollowCommand(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestAutoFollowLoggedGuestCannotControl(t *testing.T) {
	s, _ := followFixture(t)
	auth, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	_, err = auth.Exec(`CREATE TABLE web_users(id INTEGER,username TEXT,admin INTEGER); CREATE TABLE web_sessions(token TEXT,user_id INTEGER,expires INTEGER); INSERT INTO web_users VALUES(1,'guest',0)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.Exec("INSERT INTO web_sessions VALUES(?,1,?)", sessionHash("test-session"), time.Now().Add(time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	s.webAuth = &webAuthStore{db: auth}
	r := httptest.NewRequest("POST", "/api/v1/auto-follow/commands", strings.NewReader(`{"id":"request-123","action":"pause"}`))
	r.AddCookie(&http.Cookie{Name: webCookie, Value: "test-session"})
	w := httptest.NewRecorder()
	s.autoFollowCommand(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}
