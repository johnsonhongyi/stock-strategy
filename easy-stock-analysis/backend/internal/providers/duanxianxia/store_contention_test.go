package duanxianxia

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestTryBeginWaitsForShortSharedWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared stock data.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	blocker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	conn, err := blocker.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	done := make(chan error, 1)
	go func() {
		ok, _, err := store.TryBegin(context.Background(), time.Now(), time.Minute)
		if err == nil && !ok {
			t.Error("refresh was not reserved after the writer released")
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("reservation returned while another writer held the lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err = conn.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("reservation did not resume after lock release")
	}
}
