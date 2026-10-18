package linkage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceEventQueryUsesIndexesAndPreservesCursor(t *testing.T) {
	service, err := NewService(filepath.Join(t.TempDir(), "linkage.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx := context.Background()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range []struct{ snapshot, workspace, event string }{{"s1", "own", "e1"}, {"s2", "other", "e2"}, {"s3", "own", "e3"}} {
		payload, _ := json.Marshal(map[string]string{"workspace_id": item.workspace})
		event := ContextEvent{EventID: item.event, EventType: "test", OriginNode: "test", CausationID: item.snapshot, SourceSnapshotIDs: []string{item.snapshot}, TradeDate: "2026-10-16", OccurredAt: stamp, Payload: json.RawMessage(`{}`)}
		if err = service.StoreContextSnapshot(ctx, item.snapshot, event.TradeDate, payload, []ContextEvent{event}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := service.db.Query("EXPLAIN QUERY PLAN SELECT event.event_id FROM linkage_context_events AS event WHERE "+contextEventWorkspaceScope+" ORDER BY event.occurred_at,event.event_id LIMIT 100", "own")
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var a, b, c int
		var detail string
		if err = rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	rows.Close()
	text := strings.Join(plan, " ")
	if !strings.Contains(text, "idx_linkage_context_events_causation_time") || !strings.Contains(text, "idx_linkage_context_workspace_created") || strings.Contains(text, "CORRELATED") {
		t.Fatalf("unbounded workspace query: %s", text)
	}
	events, err := service.ContextEventsForWorkspace(ctx, "own", "e1", 10)
	if err != nil || len(events) != 1 || events[0].EventID != "e3" {
		t.Fatalf("cursor: %+v %v", events, err)
	}
	latest, err := service.LatestContextEventsForWorkspace(ctx, "own", 1)
	if err != nil || len(latest) != 1 || latest[0].EventID != "e3" {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	foreign, err := service.ContextEventsForWorkspace(ctx, "missing", "", 10)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("workspace leak: %+v %v", foreign, err)
	}
}
