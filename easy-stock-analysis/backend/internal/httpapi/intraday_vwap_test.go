package httpapi

import (
	"easy-stock/backend/internal/foundation"
	"math"
	"testing"
	"time"
)

func TestIntradayVWAPUnitsAndEstimate(t *testing.T) {
	base := time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC)
	lines := []foundation.KLine{
		{Time: base, Close: 10, Volume: 1, Amount: 1000, Meta: foundation.SourceMeta{Source: "eastmoney"}},
		{Time: base.Add(time.Minute), Close: 20, Volume: 3, Amount: 6000, Meta: foundation.SourceMeta{Source: "eastmoney"}},
		{Time: base.Add(24 * time.Hour), Close: 8, Volume: 100, Meta: foundation.SourceMeta{Source: "sina"}},
		{Time: base.Add(24*time.Hour + time.Minute), Close: 12, Volume: 300, Meta: foundation.SourceMeta{Source: "sina"}},
	}
	got := intradayVWAP(lines)
	if got[1].VWAP != 17.5 || got[1].VWAPEstimated || got[2].VWAP != 8 || got[3].VWAP != 11 || !got[3].VWAPEstimated {
		t.Fatalf("unexpected VWAP: %+v", got)
	}
	if lines[0].VWAP != 0 {
		t.Fatal("source mutated")
	}
}
func TestIntradayVWAPZeroAndInvalidVolume(t *testing.T) {
	lines := []foundation.KLine{{Close: 10}, {Close: 10, Volume: math.NaN()}, {Close: 10, Volume: -1}}
	for _, line := range intradayVWAP(lines) {
		if line.VWAP != 0 {
			t.Fatal("invalid volume must not produce VWAP")
		}
	}
}
