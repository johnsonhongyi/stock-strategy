package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easy-stock/backend/internal/linkage"
)

func TestDesktopBridgeTestTerminalJobsAndSessionsAreRemoved(t *testing.T) {
	s, _, exec := aiPeerFixture(t)
	db, err := s.chatDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, status := range []string{"completed", "failed", "expired", "cancelled"} {
		id, sessionID := "bridge-test-"+status, "session-"+status
		exec("INSERT INTO desktop_chat_sessions(id,owner,node_id,workspace_id,model,effort) VALUES(?,?,?,?,?,?)", sessionID, "local-desktop", "desk", "ws", "gpt-6-luna", "low")
		exec("INSERT INTO desktop_chat_jobs(id,session_id,owner,node_id,workspace_id,body,status,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?)", id, sessionID, "local-desktop", "desk", "ws", `{"bridge_test":true}`, status, time.Now().Unix(), time.Now().Add(10*time.Minute).Unix())

		request := httptest.NewRequest("DELETE", "/api/v1/ai/desktop/jobs/"+id, nil)
		request.SetPathValue("id", id)
		response := httptest.NewRecorder()
		s.desktopChatJob(response, request)
		if response.Code != 200 {
			t.Fatalf("cleanup %s: %d %s", status, response.Code, response.Body.String())
		}
		for _, table := range []string{"desktop_chat_jobs", "desktop_chat_sessions"} {
			column, value := "id", id
			if table == "desktop_chat_sessions" {
				value = sessionID
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+column+"=?", value).Scan(&count); err != nil || count != 0 {
				t.Fatalf("cleanup %s %s: count=%d err=%v", status, table, count, err)
			}
		}
	}
}

func TestDesktopChatQueueAndCancellation(t *testing.T) {
	s, peer, exec := aiPeerFixture(t)
	db, err := s.chatDB()
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	exec("INSERT INTO ai_desktop VALUES(?,?,?,?,?)", "desk", "ws", time.Now().UTC().Format(time.RFC3339Nano), "ready", `{"capabilities":["ai_chat_v1"],"models":[{"id":"gpt-6-luna","name":"Luna","efforts":["low"]}]}`)
	runtime := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.desktopChatRuntime(w, httptest.NewRequest("PUT", "/api/v1/ai/runtime", strings.NewReader(body)))
		return w
	}
	cfg := `{"provider":"codex_desktop","node_id":"desk","workspace_id":"ws","model":"gpt-6-luna","reasoning_effort":"low"}`
	if w := runtime(strings.Replace(cfg, "gpt-6-luna", "not-installed", 1)); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}

	exec("UPDATE ai_desktop SET detail=?", `{"models":[{"id":"gpt-6-luna","efforts":["low"]}]}`)
	if w := runtime(cfg); w.Code != 400 {
		t.Fatal("legacy bridge advertised chat", w.Code)
	}
	exec("UPDATE ai_desktop SET detail=?", `{"capabilities":["ai_chat_v1"],"models":[{"id":"gpt-6-luna","efforts":["low"]}]}`)
	if w := runtime(cfg); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	create := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.desktopChatJob(w, httptest.NewRequest("POST", "/api/v1/ai/desktop/jobs", strings.NewReader(body)))
		return w
	}
	w := create(`{"prompt":"test read-only"}`)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		Data struct {
			ID      string `json:"id"`
			Session string `json:"session_id"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	if w := create(`{"prompt":"concurrent","session_id":"` + created.Data.Session + `"}`); w.Code != 409 {
		t.Fatal("same session accepted concurrently", w.Code)
	}
	if w := create(`{"prompt":"unknown","session_id":"not-owned"}`); w.Code != 404 {
		t.Fatal("unknown session accepted", w.Code)
	}
	claimed := peer("chat_claim", nil)
	if claimed.Code != 200 {
		t.Fatal(claimed.Code, claimed.Body.String())
	}
	var claim struct {
		Job struct {
			ID    string `json:"id"`
			Token string `json:"lease_token"`
		} `json:"job"`
	}
	json.Unmarshal(claimed.Body.Bytes(), &claim)
	if claim.Job.ID != created.Data.ID {
		t.Fatal("chat mixed with strategy queue")
	}
	payload := map[string]any{"id": claim.Job.ID, "lease_token": claim.Job.Token, "status": "completed", "result": map[string]any{"content": "done", "thread_id": "01a13f4b-5f5a-7fb0-990c-a22bdbe42f68", "model": "wrong", "reasoning_effort": "low"}}
	if w := peer("chat_result", payload); w.Code != 409 {
		t.Fatal("mismatched model accepted", w.Code)
	}
	payload["result"].(map[string]any)["model"] = "gpt-6-luna"
	if w := peer("chat_result", payload); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	next := create(`{"prompt":"continue","session_id":"` + created.Data.Session + `"}`)
	if next.Code != 202 {
		t.Fatal(next.Code, next.Body.String())
	}
	json.Unmarshal(next.Body.Bytes(), &created)
	secondClaim := peer("chat_claim", nil)
	json.Unmarshal(secondClaim.Body.Bytes(), &claim)
	if claim.Job.ID != created.Data.ID {
		t.Fatal("continuation claim mismatch")
	}

	r := httptest.NewRequest("DELETE", "/api/v1/ai/desktop/jobs/"+created.Data.ID, nil)
	r.SetPathValue("id", created.Data.ID)
	cancel := httptest.NewRecorder()
	s.desktopChatJob(cancel, r)
	if !strings.Contains(cancel.Body.String(), "cancelled") {
		t.Fatal(cancel.Body.String())
	}
	payload["id"] = claim.Job.ID
	payload["lease_token"] = claim.Job.Token
	if w := peer("chat_result", payload); w.Code != 409 {
		t.Fatal("cancelled result accepted", w.Code)
	}
	exec("UPDATE desktop_chat_jobs SET owner='another-user' WHERE id=?", created.Data.ID)
	privateRequest := httptest.NewRequest("GET", "/api/v1/ai/desktop/jobs/"+created.Data.ID, nil)
	privateRequest.SetPathValue("id", created.Data.ID)
	privateResponse := httptest.NewRecorder()
	s.desktopChatJob(privateResponse, privateRequest)
	if privateResponse.Code != 404 {
		t.Fatal("cross-owner job exposed", privateResponse.Code)
	}

	exec("UPDATE ai_desktop SET heartbeat=?", time.Now().Add(-2*time.Minute).UTC().Format(time.RFC3339Nano))
	if w := create(`{"prompt":"offline"}`); w.Code != 503 {
		t.Fatal("offline desktop accepted", w.Code)
	}
}

func TestAntigravityRuntimeAndBridgeTestUseAgvTarget(t *testing.T) {
	s, db := followFixture(t)
	defer s.Close()
	defer db.Close()

	keyPath := filepath.Join(t.TempDir(), "peer.key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("s", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(filepath.Dir(keyPath), "targets.json")
	registryBody := `{"targets":[{"node_id":"agv-main","workspace_id":"ws-agv","base_url":"http://127.0.0.1:26778","key_file":"peer.key","enabled":true},{"node_id":"codex-main","workspace_id":"ws-codex","base_url":"http://127.0.0.1:26676","key_file":"peer.key","enabled":true}]}`
	if err := os.WriteFile(registry, []byte(registryBody), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := linkage.NewService(filepath.Join(filepath.Dir(keyPath), "linkage.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	s.linkage = service

	chatDB, err := s.chatDB()
	if err != nil {
		t.Fatal(err)
	}
	defer chatDB.Close()
	_, err = chatDB.Exec(`CREATE TABLE ai_desktop(node_id TEXT,workspace_id TEXT,heartbeat TEXT,status TEXT,detail TEXT,PRIMARY KEY(node_id,workspace_id))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = chatDB.Exec("INSERT INTO ai_desktop VALUES(?,?,?,?,?)", "agv-main", "ws-agv", time.Now().UTC().Format(time.RFC3339Nano), "ready", `{"provider":"antigravity-cli","capabilities":["ai_chat_v1"],"models":[{"id":"gemini-3.8-flash","name":"Gemini 3.8 Flash","efforts":["low","high"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = chatDB.Exec("INSERT INTO ai_desktop VALUES(?,?,?,?,?)", "codex-main", "ws-codex", time.Now().UTC().Format(time.RFC3339Nano), "ready", `{"provider":"codex-app-server","capabilities":["ai_chat_v1"],"models":[{"id":"gpt-6-luna","name":"GPT-6 Luna","efforts":["low","high"]}]}`)
	if err != nil {
		t.Fatal(err)
	}

	runtime := httptest.NewRecorder()
	s.desktopChatRuntime(runtime, httptest.NewRequest("PUT", "/api/v1/ai/runtime", strings.NewReader(`{"provider":"antigravity_desktop","node_id":"agv-main","workspace_id":"ws-agv","model":"gemini-3.8-flash","reasoning_effort":"low"}`)))
	if runtime.Code != 200 {
		t.Fatalf("save Antigravity runtime: %d %s", runtime.Code, runtime.Body.String())
	}
	mismatched := httptest.NewRecorder()
	s.desktopChatRuntime(mismatched, httptest.NewRequest("PUT", "/api/v1/ai/runtime", strings.NewReader(strings.Replace(`{"provider":"antigravity_desktop","node_id":"agv-main","workspace_id":"ws-agv","model":"gemini-3.8-flash","reasoning_effort":"low"}`, "antigravity_desktop", "codex_desktop", 1))))
	if mismatched.Code != 400 {
		t.Fatalf("mismatched bridge provider accepted: %d %s", mismatched.Code, mismatched.Body.String())
	}

	queueBridgeTest := func(savedProvider, node, workspace, model string) {
		t.Helper()
		configBody, _ := json.Marshal(desktopChatConfig{Provider: savedProvider})
		if _, err := chatDB.Exec("INSERT OR REPLACE INTO desktop_chat_config VALUES(1,?)", string(configBody)); err != nil {
			t.Fatal(err)
		}
		requestBody, _ := json.Marshal(map[string]any{"bridge_test": true, "node_id": node, "workspace_id": workspace, "model": model, "reasoning_effort": "low"})
		request := httptest.NewRequest("POST", "/api/v1/ai/desktop/jobs", strings.NewReader(string(requestBody)))
		response := httptest.NewRecorder()
		s.desktopChatJob(response, request)
		if response.Code != 202 {
			t.Fatalf("queue %s bridge test: %d %s", node, response.Code, response.Body.String())
		}
		var created struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.Data.ID == "" {
			t.Fatalf("decode queued job: id=%q err=%v", created.Data.ID, err)
		}
		var queuedNode, queuedWorkspace, body string
		if err := chatDB.QueryRow("SELECT node_id,workspace_id,body FROM desktop_chat_jobs WHERE id=?", created.Data.ID).Scan(&queuedNode, &queuedWorkspace, &body); err != nil {
			t.Fatal(err)
		}
		var job map[string]any
		if err := json.Unmarshal([]byte(body), &job); err != nil {
			t.Fatal(err)
		}
		if queuedNode != node || queuedWorkspace != workspace || job["bridge_test"] != true {
			t.Fatalf("queued job target = %s/%s, body=%s", queuedNode, queuedWorkspace, body)
		}
	}
	// The explicit node selects the test engine even when the saved runtime points at the other bridge.
	queueBridgeTest("codex_desktop", "agv-main", "ws-agv", "gemini-3.8-flash")
	queueBridgeTest("antigravity_desktop", "codex-main", "ws-codex", "gpt-6-luna")
}
