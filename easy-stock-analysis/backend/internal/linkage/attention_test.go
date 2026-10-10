package linkage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestAttentionIntentsAreIdempotentRevisionedAndTombstoned(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "linkage.db")
	service, err := NewService(databasePath, "")
	if err != nil {
		t.Fatal(err)
	}

	add := AttentionIntent{IntentID: "intent-add-1", WorkspaceID: "desk", NodeID: "tk-1",
		Symbol: "600000.SH", Source: "stock-strategy:sbc", Action: "add", BaseRevision: 0}
	result, err := service.ApplyAttentionIntent(context.Background(), add)
	if err != nil || result.Revision != 1 || result.Replayed {
		t.Fatalf("add result = %+v, err=%v", result, err)
	}
	replayed, err := service.ApplyAttentionIntent(context.Background(), add)
	if err != nil || !replayed.Replayed || replayed.Revision != result.Revision {
		t.Fatalf("replay result = %+v, err=%v", replayed, err)
	}
	changedReplay := add
	changedReplay.BaseRevision = 1
	if _, err := service.ApplyAttentionIntent(context.Background(), changedReplay); err == nil || err.Error() != "attention_intent_conflict" {
		t.Fatalf("changed base revision must conflict for a reused intent id, got %v", err)
	}
	stale := add
	stale.IntentID = "intent-add-stale"
	if _, err := service.ApplyAttentionIntent(context.Background(), stale); err == nil || err.Error() != "attention_revision_conflict" {
		t.Fatalf("stale revision error = %v", err)
	}

	remove := AttentionIntent{IntentID: "intent-remove-all", WorkspaceID: "desk", NodeID: "tk-1",
		Symbol: "600000.SH", Source: "manual", Action: "remove_all", BaseRevision: 1}
	removed, err := service.ApplyAttentionIntent(context.Background(), remove)
	if err != nil || removed.Revision != 2 {
		t.Fatalf("remove result = %+v, err=%v", removed, err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	service, err = NewService(databasePath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	snapshot, err := service.GetAttentionSnapshot(context.Background(), "desk", "tk-1")
	if err != nil || snapshot.Revision != 2 || len(snapshot.Sources) != 1 || snapshot.Sources[0].Active || len(snapshot.Tombstones) != 1 || snapshot.Tombstones[0].Revision != 2 {
		t.Fatalf("snapshot = %+v, err=%v", snapshot, err)
	}
	remove.IntentID = "intent-remove-conflict"
	remove.BaseRevision = 0
	if _, err := service.ApplyAttentionIntent(context.Background(), remove); err == nil || err.Error() != "attention_revision_conflict" {
		t.Fatalf("expected stale remove conflict, got %v", err)
	}
}

func TestAttentionStateProjectsAcrossNodesInOneWorkspace(t *testing.T) {
	service, err := NewService(filepath.Join(t.TempDir(), "linkage.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	add := AttentionIntent{IntentID: "shared-add", WorkspaceID: "desk", NodeID: "tk-1",
		Symbol: "600000.SH", Source: "manual", Action: "add", BaseRevision: 0}
	if _, err := service.ApplyAttentionIntent(context.Background(), add); err != nil {
		t.Fatal(err)
	}
	projected, err := service.GetAttentionSnapshot(context.Background(), "desk", "ats-1")
	if err != nil || projected.Revision != 1 || len(projected.Sources) != 1 || !projected.Sources[0].Active {
		t.Fatalf("ATS projection = %+v, err=%v", projected, err)
	}

	remove := AttentionIntent{IntentID: "shared-remove", WorkspaceID: "desk", NodeID: "ats-1",
		Symbol: "600000.SH", Source: "manual", Action: "remove_all", BaseRevision: 1}
	if _, err := service.ApplyAttentionIntent(context.Background(), remove); err != nil {
		t.Fatal(err)
	}
	projected, err = service.GetAttentionSnapshot(context.Background(), "desk", "tk-1")
	if err != nil || projected.Revision != 2 || len(projected.Tombstones) != 1 || projected.Tombstones[0].Revision != 2 {
		t.Fatalf("TK tombstone projection = %+v, err=%v", projected, err)
	}
	otherWorkspace, err := service.GetAttentionSnapshot(context.Background(), "other-desk", "tk-1")
	if err != nil || otherWorkspace.Revision != 0 || len(otherWorkspace.Sources) != 0 {
		t.Fatalf("other workspace was not isolated: %+v, err=%v", otherWorkspace, err)
	}
}

func TestLegacyPerNodeAttentionMigrationDoesNotEraseAnActiveReplica(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "linkage.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE linkage_attention_revisions(workspace_id TEXT,node_id TEXT,revision INTEGER,updated_at TEXT,PRIMARY KEY(workspace_id,node_id));
		CREATE TABLE linkage_attention_sources(workspace_id TEXT,node_id TEXT,symbol TEXT,source TEXT,active INTEGER,revision INTEGER,intent_id TEXT,updated_at TEXT,PRIMARY KEY(workspace_id,node_id,symbol,source));
		CREATE TABLE linkage_attention_tombstones(workspace_id TEXT,node_id TEXT,symbol TEXT,revision INTEGER,intent_id TEXT,updated_at TEXT,PRIMARY KEY(workspace_id,node_id,symbol));
		INSERT INTO linkage_attention_revisions VALUES('desk','tk-1',1,'2026-10-10T00:00:00Z');
		INSERT INTO linkage_attention_revisions VALUES('desk','ats-1',4,'2026-10-10T00:01:00Z');
		INSERT INTO linkage_attention_sources VALUES('desk','tk-1','600000.SH','manual',1,1,'active-tk','2026-10-10T00:00:00Z');
		INSERT INTO linkage_attention_sources VALUES('desk','ats-1','600000.SH','manual',0,4,'stale-ats','2026-10-10T00:01:00Z');
		INSERT INTO linkage_attention_sources VALUES('desk','ats-1','600001.SH','stock-strategy:sbc',0,4,'removed','2026-10-10T00:01:00Z');
		INSERT INTO linkage_attention_tombstones VALUES('desk','ats-1','600001.SH',4,'removed-all','2026-10-10T00:01:00Z');`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	service, err := NewService(databasePath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	snapshot, err := service.GetAttentionSnapshot(context.Background(), "desk", "new-node")
	if err != nil || snapshot.Revision != 5 || len(snapshot.Sources) != 2 || len(snapshot.Tombstones) != 1 {
		t.Fatalf("migrated workspace snapshot = %+v, err=%v", snapshot, err)
	}
	if !snapshot.Sources[0].Active || snapshot.Sources[0].Revision != 5 {
		t.Fatalf("stale inactive replica erased an active favorite: %+v", snapshot.Sources[0])
	}
}
