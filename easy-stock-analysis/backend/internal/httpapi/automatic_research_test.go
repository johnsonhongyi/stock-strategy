package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAutomaticResearchDefaultsOffDespiteLegacyEnabled(t *testing.T) {
	s, call, exec := aiPeerFixture(t)
	exec("DELETE FROM automatic_research_policy")
	p, e := loadAutomaticResearchPolicy(mustAutomaticDB(t, s))
	if e != nil || p.Enabled {
		t.Fatal(p, e)
	}
	if token := claimedToken(t, call("claim", nil)); token != "" {
		t.Fatal("default started inference")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/ai/auto-research", nil)
	s.automaticResearch(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) || !strings.Contains(w.Body.String(), `"status":"disabled"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAutomaticResearchUnavailableModelNeverChangesBridge(t *testing.T) {
	for _, change := range []string{"UPDATE ai_desktop SET status='degraded'", "UPDATE ai_desktop SET detail='{}'", "UPDATE desktop_chat_config SET body='{}'"} {
		t.Run(change, func(t *testing.T) {
			s, call, exec := aiPeerFixture(t)
			exec(change)
			if token := claimedToken(t, call("claim", nil)); token != "" {
				t.Fatal("unavailable model admitted")
			}
			db := mustAutomaticDB(t, s)
			if e := s.reconcileStrategyBridge(context.Background(), db); e != nil {
				t.Fatal(e)
			}
			var n int
			if e := db.QueryRow("SELECT count(*) FROM commands WHERE action='ai_settings'").Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
		})
	}
}

func TestAutomaticResearchRejectsFrozenOldModel(t *testing.T) {
	_, call, exec := aiPeerFixture(t)
	exec(`UPDATE ai_jobs SET context='{"ai_runtime":{"model":"expensive-old","reasoning_effort":"high"},"auto_research_revision":0}'`)
	if token := claimedToken(t, call("claim", nil)); token != "" {
		t.Fatal("stale model admitted")
	}
}

func TestAutomaticResearchBudgetsAndUnknownUsage(t *testing.T) {
	s, call, exec := aiPeerFixture(t)
	db := mustAutomaticDB(t, s)
	if len(claimedToken(t, call("claim", nil))) != 64 {
		t.Fatal("initial admission failed")
	}
	// A second queued job cannot bypass outstanding reservations or the interval.
	exec(`INSERT INTO ai_jobs SELECT ?,trade_date,evidence_hash,node_id,workspace_id,context,question,created_at,expires_at,'queued',NULL,NULL,NULL,NULL,NULL FROM ai_jobs LIMIT 1`, strings.Repeat("c", 32))
	exec("UPDATE automatic_research_runs SET started_at=?", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))
	if token := claimedToken(t, call("claim", nil)); token != "" {
		t.Fatal("unreported usage treated as zero")
	}
	result := json.RawMessage(`{"token_usage":{"totalTokens":300000}}`)
	for i := 0; i < 2; i++ {
		if e := settleAutomaticResearch(db, strings.Repeat("a", 32), result); e != nil {
			t.Fatal(e)
		}
	}
	u, e := automaticResearchDaily(db)
	if e != nil || u.Tokens != 300000 || u.Runs != 1 || u.Unknown != 0 {
		t.Fatal(u, e)
	}
	if token := claimedToken(t, call("claim", nil)); token != "" {
		t.Fatal("token budget exceeded")
	}
	exec(`UPDATE automatic_research_policy SET body='{"enabled":true,"interval_seconds":900,"daily_max_runs":1,"daily_token_budget":500000}'`)
	if token := claimedToken(t, call("claim", nil)); token != "" {
		t.Fatal("daily count budget exceeded")
	}
}

func TestAutomaticResearchSettingsPreserveAccountAndRejectCrossOrigin(t *testing.T) {
	s, _, _ := aiPeerFixture(t)
	db := mustAutomaticDB(t, s)
	account, e := followDB(s.autoFollowDBPath)
	if e != nil {
		t.Fatal(e)
	}
	defer account.Close()
	var before, after string
	_ = account.QueryRow("SELECT body FROM state WHERE id=1").Scan(&before)
	runtime := loadDesktopChatConfig(db)
	body := `{"enabled":false,"interval_seconds":1200,"daily_max_runs":2,"daily_token_budget":40000}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/v1/ai/auto-research", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	s.automaticResearch(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	p, e := loadAutomaticResearchPolicy(db)
	if e != nil || p.Enabled || p.Interval != 1200 || p.Revision != 1 {
		t.Fatal(p, e)
	}
	_ = account.QueryRow("SELECT body FROM state WHERE id=1").Scan(&after)
	if before != after || runtime != loadDesktopChatConfig(db) {
		t.Fatal("trading state or system runtime changed")
	}
	r = httptest.NewRequest("PUT", "http://local/api/v1/ai/auto-research", strings.NewReader(body))
	r.Header.Set("Origin", "https://foreign.invalid")
	w = httptest.NewRecorder()
	s.automaticResearch(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestAutomaticResearchAutoReconciliationAndManualSettle(t *testing.T) {
	s, call, exec := aiPeerFixture(t)
	db := mustAutomaticDB(t, s)
	// Claim one job so it enters automatic_research_runs with usage_known=0
	token := claimedToken(t, call("claim", nil))
	if len(token) != 64 {
		t.Fatal("claim failed")
	}
	// Verify state is usage_unsettled while running
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/ai/auto-research", nil)
	s.automaticResearch(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"usage_unsettled"`) {
		t.Fatalf("expected usage_unsettled, got code %d body %s", w.Code, w.Body.String())
	}
	// Case 1: Manual settle endpoint POST /api/v1/ai/auto-research/settle
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/api/v1/ai/auto-research/settle", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	s.settleAutomaticResearchManual(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"status":"usage_unsettled"`) || !strings.Contains(w.Body.String(), `"unsettled_runs":0`) {
		t.Fatalf("manual settle failed: code %d body %s", w.Code, w.Body.String())
	}
	// Case 2: Auto-reconciliation of terminal jobs (e.g. expired or failed jobs)
	exec("UPDATE automatic_research_runs SET usage_known=0")
	exec("UPDATE ai_jobs SET status='expired'")
	u, e := automaticResearchDaily(db)
	if e != nil || u.Unknown != 0 {
		t.Fatalf("auto-reconcile of expired job failed: %v, unknown: %d", e, u.Unknown)
	}
}

func mustAutomaticDB(t *testing.T, s *Server) *sql.DB {
	t.Helper()
	db, e := s.desktopDB()
	if e != nil {
		t.Fatal(e)
	}
	return db
}
