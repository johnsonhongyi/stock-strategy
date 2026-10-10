package tdx

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestATSProtocolParity(t *testing.T) {
	body, _ := hex.DecodeString("0a006ec73301b28c011e3254a081ad4816d6984d6fc7330154ae0182024ab0a51d4978090c4e70c733015414285e8003bb488b59a64d71c73301140086015ec059274945cb154e74c73301006828724060f648ae0edc4d75c73301000a1e7c40f6da48a37dc24d76c7330100680ad0018052b748ad68a24d77c7330100680072a0f0a448f8b9914d78c733010054285ee0a48b48c294764d7bc733010aa401b8014a001def4874abd44d")
	rows, err := decodeBars(body, 9, "000001.SZ", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var expected []struct {
		Open, Close, High, Low, Vol, Amount float64
		Datetime                            string
	}
	if err = json.Unmarshal([]byte(`[{"open":9.01,"close":9.04,"high":9.06,"low":8.99,"vol":355341.0,"amount":320520896.0,"year":2017,"month":6,"day":6,"hour":15,"minute":0,"datetime":"2017-06-06 15:00"},{"open":9.02,"close":9.13,"high":9.15,"low":9.01,"vol":645723.0,"amount":587357696.0,"year":2017,"month":6,"day":7,"hour":15,"minute":0,"datetime":"2017-06-07 15:00"},{"open":9.11,"close":9.13,"high":9.15,"low":9.08,"vol":383004.0,"amount":348860768.0,"year":2017,"month":6,"day":8,"hour":15,"minute":0,"datetime":"2017-06-08 15:00"},{"open":9.15,"close":9.15,"high":9.22,"low":9.12,"vol":685468.0,"amount":628281664.0,"year":2017,"month":6,"day":9,"hour":15,"minute":0,"datetime":"2017-06-09 15:00"},{"open":9.15,"close":9.11,"high":9.19,"low":9.1,"vol":504578.0,"amount":461493696.0,"year":2017,"month":6,"day":12,"hour":15,"minute":0,"datetime":"2017-06-12 15:00"},{"open":9.11,"close":9.12,"high":9.14,"low":9.05,"vol":448434.0,"amount":407876704.0,"year":2017,"month":6,"day":13,"hour":15,"minute":0,"datetime":"2017-06-13 15:00"},{"open":9.12,"close":9.08,"high":9.13,"low":9.04,"vol":375444.0,"amount":340596128.0,"year":2017,"month":6,"day":14,"hour":15,"minute":0,"datetime":"2017-06-14 15:00"},{"open":9.08,"close":9.04,"high":9.08,"low":9.03,"vol":337797.0,"amount":305610496.0,"year":2017,"month":6,"day":15,"hour":15,"minute":0,"datetime":"2017-06-15 15:00"},{"open":9.04,"close":9.02,"high":9.08,"low":9.01,"vol":285991.0,"amount":258559008.0,"year":2017,"month":6,"day":16,"hour":15,"minute":0,"datetime":"2017-06-16 15:00"},{"open":9.03,"close":9.13,"high":9.15,"low":9.02,"vol":489704.0,"amount":446000768.0,"year":2017,"month":6,"day":19,"hour":15,"minute":0,"datetime":"2017-06-19 15:00"}]`), &expected); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(expected) {
		t.Fatal(len(rows))
	}
	for i, r := range rows {
		e := expected[i]
		for _, pair := range [][2]float64{{r.Open, e.Open}, {r.Close, e.Close}, {r.High, e.High}, {r.Low, e.Low}, {r.Volume, e.Vol * 100}, {r.Amount, e.Amount}} {
			if math.Abs(pair[0]-pair[1]) > 1e-6 {
				t.Fatalf("bar %d: %v", i, pair)
			}
		}
		if r.Time.Format("2006-01-02 15:04") != e.Datetime {
			t.Fatal(r.Time, e.Datetime)
		}
	}
	for n := 0; n < len(body); n++ {
		if _, err := decodeBars(body[:n], 9, "000001.SZ", time.Now()); err == nil {
			t.Fatalf("accepted truncated body %d", n)
		}
	}
	request := barRequest(9, 0, 0, 10, "000001")
	if hex.EncodeToString(request) != "0c01086401011c001c002d0500003030303030310900010000000a0000000000000000000000" {
		t.Fatal(hex.EncodeToString(request))
	}
}
func TestCacheRoundTripAndSymbolValidation(t *testing.T) {
	c, err := New(filepath.Join(t.TempDir(), "tdx.db"), []string{"127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	body, _ := hex.DecodeString("0a006ec73301b28c011e3254a081ad4816d6984d6fc7330154ae0182024ab0a51d4978090c4e70c733015414285e8003bb488b59a64d71c73301140086015ec059274945cb154e74c73301006828724060f648ae0edc4d75c73301000a1e7c40f6da48a37dc24d76c7330100680ad0018052b748ad68a24d77c7330100680072a0f0a448f8b9914d78c733010054285ee0a48b48c294764d7bc733010aa401b8014a001def4874abd44d")
	rows, err := decodeBars(body, 9, "000001.SZ", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	if err = c.cache.write(ctx, "000001.SZ", "day", rows, now); err != nil {
		t.Fatal(err)
	}
	got, _, err := c.cache.read(ctx, "000001.SZ", "day", 100)
	if err != nil || len(got) != len(rows) {
		t.Fatal(len(got), err)
	}
	if _, _, _, err = normalize("600733.SZ"); err == nil {
		t.Fatal("accepted wrong exchange")
	}
	if _, _, _, err = normalize("BTC"); err == nil {
		t.Fatal("accepted crypto")
	}
	if _, err = New("relative.db", nil); err == nil {
		t.Fatal("accepted relative cache")
	}
	c.retryAt = time.Now().Add(time.Minute)
	staleRows, err := c.KLine(ctx, "000001.SZ", "day", 100)
	if err != nil || !staleRows[0].Meta.Stale || staleRows[0].Meta.FallbackReason == "" {
		t.Fatal(err)
	}
}
func TestNormalizeBeijingSymbols(t *testing.T) {
	for _, input := range []string{"920305", "920305.BJ", "BJ920305"} {
		code, symbol, market, err := normalize(input)
		if err != nil || code != "920305" || symbol != "920305.BJ" || market != 2 {
			t.Fatalf("normalize(%q) = %q, %q, %d, %v", input, code, symbol, market, err)
		}
	}
	if _, _, _, err := normalize("920305.SH"); err == nil {
		t.Fatal("accepted a Beijing symbol on Shanghai market")
	}
}

func TestLiveTDX(t *testing.T) {
	if os.Getenv("TDX_SMOKE") != "1" {
		t.Skip("explicit network smoke only")
	}
	c, e := New(filepath.Join(t.TempDir(), "tdx.db"), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, period := range []string{"1", "5", "day"} {
		rows, err := c.KLine(ctx, "600733.SH", period, 1280)
		if err != nil {
			t.Fatal(err)
		}
		last := rows[len(rows)-1]
		if last.Volume > 0 && last.Amount > 0 {
			vwap := last.Amount / last.Volume
			if vwap < last.Low*.95 || vwap > last.High*1.05 {
				t.Fatalf("volume unit mismatch: VWAP %g, low %g high %g", vwap, last.Low, last.High)
			}
		}
		t.Logf("%s count=%d latest=%s close=%g volume_shares=%g amount=%g turnover=%g estimated=%v", period, len(rows), last.Time.Format(time.RFC3339), last.Close, last.Volume, last.Amount, last.TurnoverRate, last.TurnoverEstimated)
	}
}
