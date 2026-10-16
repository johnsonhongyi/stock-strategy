package marketbars

import (
    "context"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestReadDailyPrefersSharedTDXFile(t *testing.T) {
    dir := t.TempDir()
    yesterday := time.Now().In(shanghai).AddDate(0, 0, -1).Format("2006-01-02")
    prior := time.Now().In(shanghai).AddDate(0, 0, -2).Format("2006-01-02")
    content := prior + ",10,11,9,10.5,1000,10500\n" + yesterday + ",10.5,12,10,11.5,1200,13800\n"
    if err := os.WriteFile(filepath.Join(dir, "SH600000.TXT"), []byte(content), 0o600); err != nil { t.Fatal(err) }
    store, err := Open(":memory:")
    if err != nil { t.Fatal(err) }
    defer store.Close()
    store.SetTDXDailyDirectory(dir)
    lines, found, err := store.ReadDaily(context.Background(), "600000", "CN", 10)
    if err != nil { t.Fatal(err) }
    if !found || len(lines) != 2 { t.Fatalf("found=%v lines=%d", found, len(lines)) }
    if lines[1].Meta.Source != "instock-tdx-daily" || lines[1].Meta.TradeDate != yesterday {
        t.Fatalf("unexpected source metadata: %+v", lines[1].Meta)
    }
    if lines[1].PreviousClose != 10.5 || lines[1].Volume != 1200 || lines[1].Amount != 13800 {
        t.Fatalf("unexpected TDX values: %+v", lines[1])
    }
}

func TestReadDailyLoadsBenchmarkIndexesFromSharedTDXFiles(t *testing.T) {
	dir := t.TempDir()
	yesterday := time.Now().In(shanghai).AddDate(0, 0, -1).Format("2006-01-02")
	prior := time.Now().In(shanghai).AddDate(0, 0, -2).Format("2006-01-02")
	content := prior + ",2770,2800,2750,2780,16000000000,410000000000\n" +
		yesterday + ",2780,2820,2760,2805,17000000000,420000000000\n"
	for _, filename := range []string{"SZ399001.TXT", "SZ399006.TXT", "SH000001.TXT"} {
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetTDXDailyDirectory(dir)
	for _, sample := range []struct{ symbol, code string }{
		{"399001.SZ", "399001"},
		{"399006.SZ", "399006"},
		{"000001.SH", "000001"},
	} {
		lines, found, err := store.ReadDaily(context.Background(), sample.symbol, "CN", 10)
		if err != nil {
			t.Fatal(err)
		}
		if !found || len(lines) != 2 {
			t.Fatalf("%s: found=%v lines=%d", sample.symbol, found, len(lines))
		}
		if lines[1].Symbol != sample.code || lines[1].Meta.Source != "instock-tdx-daily" ||
			lines[1].Meta.TradeDate != yesterday || lines[1].PreviousClose != 2780 {
			t.Fatalf("%s: unexpected benchmark bars: %+v", sample.symbol, lines[1])
		}
	}
}
func TestReadDailyDoesNotFallBackToSQLiteWhenTDXIsEnabled(t *testing.T) {
    store, err := Open(":memory:")
    if err != nil { t.Fatal(err) }
    defer store.Close()
    store.SetTDXDailyDirectory(t.TempDir())
    _, err = store.db.ExecContext(context.Background(), `INSERT INTO daily_bars
        (code,date,open,high,low,close,prev_close,volume,amount,source,market)
        VALUES('600000','2026-10-09',10,11,9,10.5,9.5,1000,10500,'legacy','CN')`)
    if err != nil { t.Fatal(err) }
    lines, found, err := store.ReadDaily(context.Background(), "600000", "CN", 10)
    if err != nil { t.Fatal(err) }
    if found || len(lines) != 0 { t.Fatalf("found=%v lines=%d; expected missing TDX file to fail closed", found, len(lines)) }
}

func TestTDXDailyCoverageSeparatesFreshStaleAndInvalid(t *testing.T) {
    if prefix := tdxExchangePrefix("920001"); prefix != "BJ" { t.Fatalf("920001 prefix=%q", prefix) }
    dir := t.TempDir()
    today := time.Now().In(shanghai).AddDate(0, 0, -1).Format("2006-01-02")
    stale := time.Now().In(shanghai).AddDate(0, 0, -2).Format("2006-01-02")
    files := map[string]string{
        "SH600000.TXT": today + ",10,11,9,10.5,1000,10500\n#TDX footer\n78,94744968,554729984.00\n",
        "SZ000001.TXT": stale + ",10,11,9,10.5,1000,10500\n",
        "BJ430001.TXT": "invalid\n",
    }
    for name, content := range files {
        if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil { t.Fatal(err) }
    }
    store, err := Open(":memory:")
    if err != nil { t.Fatal(err) }
    defer store.Close()
    store.SetTDXDailyDirectory(dir)
    coverage, err := store.TDXDailyCoverage(context.Background(), today)
    if err != nil { t.Fatal(err) }
    if coverage.Codes != 3 || coverage.FreshCodes != 1 || coverage.StaleCodes != 1 || coverage.InvalidCodes != 1 {
        t.Fatalf("unexpected coverage: %+v", coverage)
    }
}
