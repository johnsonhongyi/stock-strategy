package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesktopReceiptUsageIsDurableAndIdempotent(t *testing.T) {
	server, _ := followFixture(t)
	server.tokenUsage = newTokenUsageStore(filepath.Join(filepath.Dir(server.autoFollowDBPath), "settings.json"))
	db, err := server.desktopDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().Unix()
	body := `{"model":"gpt-6-luna","reasoning_effort":"low","purpose":"stock_research"}`
	_, err = db.Exec(`INSERT INTO desktop_chat_jobs(id,session_id,owner,node_id,workspace_id,body,status,created_at,expires_at,lease_token,lease_until) VALUES('job','session','owner','desk','ws',?,'running',?,?,'lease',?)`, body, now, now+300, now+300)
	if err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"content":"done","thread_id":"01a13f4b-5f5a-7fb0-990c-a22bdbe42f68","model":"gpt-6-luna","reasoning_effort":"low","token_usage":{"inputTokens":100,"cachedInputTokens":50,"outputTokens":20,"totalTokens":120}}`)
	for i := 0; i < 2; i++ {
		if _, err := desktopChatPeer(db, "chat_result", "desk", "ws", "job", "lease", "completed", result); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("DELETE FROM desktop_chat_jobs WHERE id='job'"); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.tokenUsageSummary(recorder, httptest.NewRequest("GET", "/api/v1/settings/token-usage", nil))
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"total_tokens":120`) || !strings.Contains(recorder.Body.String(), `"stock-analysis"`) {
		t.Fatalf("%d %s", recorder.Code, recorder.Body.String())
	}
	var count, total int
	if err := db.QueryRow("SELECT count(*),sum(total_tokens) FROM desktop_token_usage").Scan(&count, &total); err != nil || count != 1 || total != 120 {
		t.Fatalf("%d %d %v", count, total, err)
	}
}

func TestDesktopBridgeTestUsageModule(t *testing.T) {
	server, _ := followFixture(t)
	db, err := server.desktopDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, body := range []string{`{"bridge_test":true}`, `{"purpose":"bridge_test"}`} {
		id := body
		_, err := db.Exec(`INSERT INTO desktop_chat_jobs(id,session_id,owner,node_id,workspace_id,body,status,created_at,expires_at,result) VALUES(?, 'session','owner','desk','ws',?,'completed',1,300,'{"token_usage":{"totalTokens":1}}')`, id, body)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := recordDesktopUsage(tx, id, time.Now().Unix()); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var module string
		if err := db.QueryRow(`SELECT module FROM desktop_token_usage WHERE job_id=?`, id).Scan(&module); err != nil || module != "settings-bridge-test" {
			t.Fatalf("module=%q error=%v", module, err)
		}
	}
}
