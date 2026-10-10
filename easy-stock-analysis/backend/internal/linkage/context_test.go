package linkage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestContextSnapshotsAndEventsAreDurableAndCursorBased(t *testing.T) {
	service, err := NewService(filepath.Join(t.TempDir(), "linkage.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	payload := []byte(`{"schema_version":1,"snapshot_id":"snapshot-1"}`)
	event := ContextEvent{EventID: "event-1", EventType: "heat.rank_changed", OriginNode: "stock-strategy.go",
		CausationID: "snapshot-1", SourceSnapshotIDs: []string{"snapshot-0", "snapshot-1"},
		TradeDate: "2026-10-10", OccurredAt: "2026-10-10T01:00:00Z", Payload: json.RawMessage(`{"symbol":"600000.SH"}`)}
	if err := service.StoreContextSnapshot(context.Background(), "snapshot-1", event.TradeDate, payload, []ContextEvent{event}); err != nil {
		t.Fatal(err)
	}
	latest, err := service.LatestContextSnapshot(context.Background())
	if err != nil || string(latest) != string(payload) {
		t.Fatalf("latest snapshot = %s, err=%v", latest, err)
	}

	events, err := service.ContextEvents(context.Background(), "", 10)
	if err != nil || len(events) != 1 || events[0].EventID != event.EventID {
		t.Fatalf("events = %+v, err=%v", events, err)
	}
	if len(events[0].SourceSnapshotIDs) != 2 {
		t.Fatalf("source snapshot references lost: %+v", events[0])
	}
	if _, err := service.ContextEvents(context.Background(), event.EventID, 10); err != nil {
		t.Fatal(err)
	}
}

func TestContextEvidenceReplayIsIdempotentAndLatestIsSourceScoped(t *testing.T) {
	service, err := NewService(filepath.Join(t.TempDir(), "linkage.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	first := ContextEvidence{WorkspaceID: "desk", NodeID: "tk-1", SourceID: "tk.auction",
		SnapshotID: "auction-1", TradeDate: "2026-10-10", AsOf: "2026-10-10T01:25:00Z",
		Payload: json.RawMessage(`{"schema_version":1,"source_id":"tk.auction","state":"NEUTRAL"}`)}
	inserted, err := service.StoreContextEvidence(context.Background(), first)
	if err != nil || !inserted {
		t.Fatalf("first evidence inserted=%t err=%v", inserted, err)
	}
	inserted, err = service.StoreContextEvidence(context.Background(), first)
	if err != nil || inserted {
		t.Fatalf("replay inserted=%t err=%v", inserted, err)
	}

	second := first
	second.SnapshotID = "auction-2"
	second.AsOf = "2026-10-10T01:26:00Z"
	second.Payload = json.RawMessage(`{"schema_version":1,"source_id":"tk.auction","state":"POSITIVE"}`)
	if inserted, err = service.StoreContextEvidence(context.Background(), second); err != nil || !inserted {
		t.Fatalf("second evidence inserted=%t err=%v", inserted, err)
	}

	latest, err := service.LatestContextEvidence(context.Background())
	if err != nil || len(latest) != 1 || latest[0].SnapshotID != "auction-2" {
		t.Fatalf("latest evidence = %+v, err=%v", latest, err)
	}
	recent, err := service.RecentContextEvidence(context.Background())
	if err != nil || len(recent) != 2 || recent[0].SnapshotID != "auction-1" || recent[1].SnapshotID != "auction-2" {
		t.Fatalf("recent evidence history = %+v, err=%v", recent, err)
	}
}
