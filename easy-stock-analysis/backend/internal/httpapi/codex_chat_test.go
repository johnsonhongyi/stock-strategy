package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
