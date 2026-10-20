package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"easy-stock/backend/internal/linkage"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func aiPeerFixture(t *testing.T) (*Server, func(string, map[string]any) *httptest.ResponseRecorder, func(string, ...any)) {
	t.Helper()
	s, db := followFixture(t)
	dir := t.TempDir()
	key := []byte(strings.Repeat("s", 32))
	if err := os.WriteFile(filepath.Join(dir, "key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	registry := `{"targets":[{"node_id":"desk","workspace_id":"ws","base_url":"http://127.0.0.1:26676","key_file":"key","enabled":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "targets.json"), []byte(registry), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := linkage.NewService(filepath.Join(dir, "linkage.db"), filepath.Join(dir, "targets.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.linkage = service
	t.Cleanup(func() { _ = s.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`CREATE TABLE ai_jobs(id TEXT PRIMARY KEY,trade_date TEXT,evidence_hash TEXT,node_id TEXT,workspace_id TEXT,context TEXT,question TEXT,created_at TEXT,expires_at TEXT,status TEXT,lease_token TEXT,lease_until TEXT,result TEXT,result_hash TEXT,finished_at TEXT)`)
	exec(`CREATE TABLE ai_desktop(node_id TEXT,workspace_id TEXT,heartbeat TEXT,status TEXT,detail TEXT,PRIMARY KEY(node_id,workspace_id))`)
	exec(`INSERT INTO ai_jobs(id,node_id,workspace_id,trade_date,evidence_hash,context,question,created_at,expires_at,status) VALUES(?,?,?,?,?,?,?,?,?,?)`, strings.Repeat("a", 32), "desk", "ws", time.Now().Format("2006-01-02"), strings.Repeat("b", 64), `{"stocks":[]}`, "fixture", time.Now().UTC().Format(time.RFC3339Nano), time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339Nano), "queued")
	var lock sync.Mutex
	counter := 0
	call := func(operation string, payload map[string]any) *httptest.ResponseRecorder {
		lock.Lock()
		counter++
		nonce := fmt.Sprintf("nonce000000000000%08d", counter)
		lock.Unlock()
		if payload == nil {
			payload = map[string]any{}
		}
		payload["node_id"] = "desk"
		payload["workspace_id"] = "ws"
		raw, _ := json.Marshal(payload)
		path := "/api/v1/linkage/strategy/" + operation
		request := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
		ts := fmt.Sprint(time.Now().Unix())
		hash := sha256.Sum256(raw)
		mac := hmac.New(sha256.New, key)
		fmt.Fprint(mac, strings.Join([]string{"POST", path, hex.EncodeToString(hash[:]), ts, nonce, "desk", "ws"}, "\n"))
		for k, v := range map[string]string{"X-Linkage-Node-ID": "desk", "X-Linkage-Workspace-ID": "ws", "X-Linkage-Timestamp": ts, "X-Linkage-Nonce": nonce, "X-Linkage-Signature": hex.EncodeToString(mac.Sum(nil))} {
			request.Header.Set(k, v)
		}
		response := httptest.NewRecorder()
		if s.webAuth != nil && s.handleWebAuth(response, request) {
			return response
		}
		if !s.authorizedLinkage(request) {
			response.WriteHeader(401)
			return response
		}
		s.autoFollowAIPeer(response, request)
		return response
	}
	if _, err := s.desktopDB(); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO automatic_research_policy VALUES(1,'{"enabled":true,"interval_seconds":900,"daily_max_runs":3,"daily_token_budget":300000,"revision":0}')`)
	exec(`INSERT INTO desktop_chat_config VALUES(1,'{"provider":"codex_desktop","node_id":"desk","workspace_id":"ws","model":"gpt-6-luna","reasoning_effort":"low"}')`)
	exec(`INSERT INTO ai_desktop VALUES(?,?,?,?,?)`, "desk", "ws", time.Now().UTC().Format(time.RFC3339Nano), "ready", `{"capabilities":["strategy_research"],"models":[{"id":"gpt-6-luna","efforts":["low"]}]}`)
	exec(`UPDATE ai_jobs SET context='{"stocks":[],"ai_runtime":{"model":"gpt-6-luna","reasoning_effort":"low"},"auto_research_revision":0}'`)
	return s, call, exec
}

func claimedToken(t *testing.T, r *httptest.ResponseRecorder) string {
	t.Helper()
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var result struct {
		Job *struct {
			Lease string `json:"lease_token"`
		}
	}
	if json.Unmarshal(r.Body.Bytes(), &result) != nil {
		t.Fatal(r.Body.String())
	}
	if result.Job == nil {
		return ""
	}
	return result.Job.Lease
}

func TestAutoFollowAIPeerExpiredAttemptDoesNotRetryAndResultIsIdempotent(t *testing.T) {
	_, call, exec := aiPeerFixture(t)
	old := claimedToken(t, call("claim", nil))
	if len(old) != 64 {
		t.Fatal("missing lease")
	}
	result := map[string]any{"proposal": map[string]any{"plans": []any{}}, "thread_id": "fixture-thread"}
	payload := map[string]any{"id": strings.Repeat("a", 32), "lease_token": old, "status": "completed", "result": result}
	exec("UPDATE ai_jobs SET lease_until=?", time.Now().Add(-time.Second).Unix())
	if r := call("result", payload); r.Code != 409 {
		t.Fatal("expired lease accepted", r.Code)
	}
	if fresh := claimedToken(t, call("claim", nil)); fresh != "" {
		t.Fatal("expired attempt silently retried")
	}
	// The original owner can deliver only while its existing lease is active.
	exec("UPDATE ai_jobs SET lease_until=?", time.Now().Add(time.Minute).Unix())
	for i := 0; i < 2; i++ {
		if r := call("result", payload); r.Code != 200 {
			t.Fatal("result replay", r.Code, r.Body.String())
		}
	}
	payload["result"] = map[string]any{"proposal": map[string]any{"plans": []any{}}, "thread_id": "changed"}
	if r := call("result", payload); r.Code != 409 {
		t.Fatal("conflicting replay accepted", r.Code)
	}
}

func TestAutoFollowAIPeerConcurrentClaimHasOneOwner(t *testing.T) {
	_, call, _ := aiPeerFixture(t)
	var wg sync.WaitGroup
	results := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := call("claim", nil)
			if r.Code != 200 {
				results <- fmt.Sprint("error:", r.Code)
				return
			}
			var v struct {
				Job *struct {
					Lease string `json:"lease_token"`
				}
			}
			_ = json.Unmarshal(r.Body.Bytes(), &v)
			if v.Job != nil {
				results <- v.Job.Lease
			}
		}()
	}
	wg.Wait()
	close(results)
	owners := 0
	for value := range results {
		if len(value) != 64 {
			t.Fatal(value)
		}
		owners++
	}
	if owners != 1 {
		t.Fatalf("owners=%d", owners)
	}
}

func TestAutoFollowAIPeerRejectsUnsignedAndNullResult(t *testing.T) {
	s, call, _ := aiPeerFixture(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/linkage/strategy/claim", strings.NewReader(`{"node_id":"desk","workspace_id":"ws"}`))
	response := httptest.NewRecorder()
	s.autoFollowAIPeer(response, request)
	if response.Code != 403 {
		t.Fatal(response.Code)
	}
	token := claimedToken(t, call("claim", nil))
	r := call("result", map[string]any{"id": strings.Repeat("a", 32), "lease_token": token, "status": "completed", "result": map[string]any{"proposal": nil, "thread_id": "fixture"}})
	if r.Code != 400 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = call("heartbeat", map[string]any{"status": "ready", "detail": map[string]any{"provider": "fixture"}})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestAIPeerWithWebLoginConsumesNonceOnce(t *testing.T) {
	t.Setenv("A_STOCK_BOOTSTRAP_PASSWORD", "isolated-test-password")
	s, call, _ := aiPeerFixture(t)
	auth, err := openWebAuth(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.webAuth = auth
	t.Cleanup(func() { auth.db.Close() })
	response := call("heartbeat", map[string]any{"status": "ready", "detail": map[string]any{}})
	if response.Code != http.StatusOK {
		t.Fatalf("web login plus HMAC: %d %s", response.Code, response.Body.String())
	}
}

func TestAISettingsCarriesModelAndEffort(t *testing.T) {
	s, _, _ := aiPeerFixture(t)
	body := `{"id":"model-settings-test","action":"ai_settings","ai_settings":{"enabled":true,"mode":"enforce","node_id":"desk","workspace_id":"ws","model":"gpt-6-luna","reasoning_effort":"low"}}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/auto-follow/commands", strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	s.autoFollowCommand(response, request)
	if response.Code != 202 {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	for _, effort := range []string{"invented", "high|ultra", "high|xhigh"} {
		bad := strings.Replace(body, `"low"`, `"`+effort+`"`, 1)
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/auto-follow/commands", strings.NewReader(bad))
		r.RemoteAddr = "127.0.0.1:12345"
		s.autoFollowCommand(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid effort accepted: %d", w.Code)
		}
	}
}
