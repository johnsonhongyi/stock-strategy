package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestStrategyBridgeNeverSilentlyChangesRoutingOrTradingPolicy(t *testing.T) {
	server, _, exec := aiPeerFixture(t)
	account, err := followDB(server.autoFollowDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer account.Close()
	var raw string
	if err = account.QueryRow("SELECT body FROM state WHERE id=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	_ = json.Unmarshal([]byte(raw), &state)
	state["ai_settings"] = map[string]any{"enabled": true, "mode": "enforce", "node_id": "old-offline", "workspace_id": "old-ws", "model": "gpt-6-luna", "reasoning_effort": "max", "interval_seconds": 720}
	body, _ := json.Marshal(state)
	if _, err = account.Exec("UPDATE state SET body=? WHERE id=1", string(body)); err != nil {
		t.Fatal(err)
	}
	db, err := server.desktopDB()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = server.reconcileStrategyBridge(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM commands WHERE action='ai_settings'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unexpected automatic settings changes: %d", count)
	}
	var after string
	if err = account.QueryRow("SELECT body FROM state WHERE id=1").Scan(&after); err != nil || after != string(body) {
		t.Fatal("account or risk policy changed")
	}
	state["ai"] = map[string]any{"desktop_online": false, "desktop_status": "ready", "jobs": []any{"retained"}}
	overlayStrategyBridgeState(context.Background(), db, state)
	ai := state["ai"].(map[string]any)
	if ai["desktop_online"] != true || len(ai["jobs"].([]any)) != 1 {
		t.Fatalf("%+v", ai)
	}
	exec("UPDATE ai_desktop SET heartbeat=?", time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339Nano))
	overlayStrategyBridgeState(context.Background(), db, state)
	if state["ai"].(map[string]any)["desktop_online"] != false || state["ai"].(map[string]any)["desktop_status"] != "offline" {
		t.Fatal("stale snapshot reported online")
	}
}
