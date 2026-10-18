package httpapi

import (
	"context"
	"easy-stock/backend/internal/marketbars"
	"path/filepath"
	"testing"
	"time"
)

func TestSystemStatusCachesCompletedObservationAndCloses(t *testing.T) {
	store, err := marketbars.Open(filepath.Join(t.TempDir(), "bars.db"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{marketBars: store}
	defer server.Close()
	now := time.Now()
	rows, message, checked, loading := server.cachedMarketDataFreshness(now)
	if len(rows) != 0 || message != "" || checked != nil || !loading {
		t.Fatalf("cold read fabricated success: %v %s %v %v", rows, message, checked, loading)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for loading {
		select {
		case <-deadline.C:
			t.Fatal("status background read did not finish")
		case <-tick.C:
			rows, message, checked, loading = server.cachedMarketDataFreshness(now)
		}
	}
	if len(rows) != 3 || message != "" || checked == nil {
		t.Fatalf("completed observation missing: %v %s %v", rows, message, checked)
	}
	_, _, same, again := server.cachedMarketDataFreshness(now.Add(time.Second))
	if again || !same.Equal(*checked) {
		t.Fatal("fresh cache triggered another disk walk")
	}
	server.closeSystemStatusCache()
	if _, message, _, _ = server.cachedMarketDataFreshness(now); message == "" {
		t.Fatal("closed cache admitted new work")
	}
}
func TestSystemStatusShutdownCancelsAndJoinsRead(t *testing.T) {
	server := &Server{}
	ctx, cancel := context.WithCancel(context.Background())
	server.marketStatusCache.cancel = cancel
	server.marketStatusCache.loading = true
	server.marketStatusCache.wg.Add(1)
	go func() { defer server.marketStatusCache.wg.Done(); <-ctx.Done() }()
	server.closeSystemStatusCache()
	if ctx.Err() == nil {
		t.Fatal("pending read not cancelled")
	}
}

func TestSystemStatusFailureKeepsActualLastSuccessfulCheckTime(t *testing.T) {
	store, err := marketbars.Open(filepath.Join(t.TempDir(), "bars.db"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{marketBars: store}
	defer server.Close()
	prior := time.Now().Add(-time.Minute)
	server.marketStatusCache.rows = []marketDataFreshness{{Market: "CN", Rows: 42}}
	server.marketStatusCache.checkedAt = prior
	server.marketStatusCache.attemptedAt = prior.Add(-time.Minute)
	store.Close()
	_, _, _, loading := server.cachedMarketDataFreshness(time.Now())
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	var rows []marketDataFreshness
	var message string
	var checked *time.Time
	for loading {
		select {
		case <-deadline.C:
			t.Fatal("failed refresh did not finish")
		case <-tick.C:
			rows, message, checked, loading = server.cachedMarketDataFreshness(time.Now())
		}
	}
	if len(rows) != 1 || rows[0].Rows != 42 || message == "" || checked == nil || !checked.Equal(prior) {
		t.Fatalf("old data was misrepresented as checked now: %+v %s %v", rows, message, checked)
	}
}
