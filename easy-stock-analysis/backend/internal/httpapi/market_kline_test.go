package httpapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLatestCompletedDailyDateWaitsForSharedCloseSnapshot(t *testing.T) {
	calendarPath := filepath.Join(t.TempDir(), "calendar.json")
	if err := os.WriteFile(calendarPath, []byte(`{"schema_version":1,"markets":{"CN":{},"US":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("A_STOCK_TRADING_CALENDAR", calendarPath)
	beforeCutoff := time.Date(2026, 10, 9, 16, 9, 0, 0, shanghaiLocation)
	date, err := latestCompletedDailyDateAt("CN", beforeCutoff)
	if err != nil {
		t.Fatal(err)
	}
	if date != "2026-10-08" {
		t.Fatalf("before cutoff date=%s, want previous session", date)
	}
	afterCutoff := time.Date(2026, 10, 9, 16, 10, 0, 0, shanghaiLocation)
	date, err = latestCompletedDailyDateAt("CN", afterCutoff)
	if err != nil {
		t.Fatal(err)
	}
	if date != "2026-10-09" {
		t.Fatalf("after cutoff date=%s, want current session", date)
	}
}
