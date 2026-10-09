package httpapi

import (
	"context"
	"easy-stock/backend/internal/foundation"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestAggregateTradingDaysMatchesATS(t *testing.T) {
	lines := []foundation.KLine{}
	for i, day := range []int{2, 5, 6, 7, 8} {
		lines = append(lines, foundation.KLine{Symbol: "600733.SH", Time: time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC), Open: float64(10 + i), High: float64(12 + i), Low: float64(9 + i), Close: float64(11 + i), Volume: 100, Amount: 1000, PreviousClose: 9, Meta: foundation.SourceMeta{TradeDate: time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), Stale: i == 0, Source: "test"}})
	}
	original := lines[0]
	result := aggregateTradingDays(lines, 2)
	if len(result) != 3 || result[0].Open != 10 || result[0].Close != 12 || result[0].High != 13 || result[0].Low != 9 || result[0].Volume != 200 || result[0].Amount != 2000 || !result[0].Time.Equal(lines[1].Time) || !result[0].Meta.Stale || result[0].Meta.TradeDate != "2026-10-05" {
		t.Fatalf("incorrect two-day bars: %+v", result)
	}
	if result[1].PreviousClose != 12 || result[2].Close != 15 || result[2].Volume != 100 {
		t.Fatalf("partial group/baseline: %+v", result)
	}
	if !reflect.DeepEqual(lines[0], original) {
		t.Fatal("source mutated")
	}
	if len(aggregateTradingDays(lines, 3)) != 2 {
		t.Fatal("3D grouping")
	}
}

// Verify the API requests daily inputs and exposes the derived period to clients.
func TestServerReturnsTwoDayKLines(t *testing.T) {
	provider := &tradingDaysProvider{}
	server := NewServer(Config{KLinePrimary: provider})
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/quotes/kline?symbol=000951.SZ&period=2d&limit=20", nil))
	if rec.Code != http.StatusOK || provider.period != "day" || provider.limit != 40 {
		t.Fatalf("route status=%d period=%s limit=%d", rec.Code, provider.period, provider.limit)
	}
	var payload struct {
		Data []foundation.KLine `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 3 || payload.Data[0].Volume != 200 {
		t.Fatalf("incorrect API aggregation: %+v", payload.Data)
	}
}

type tradingDaysProvider struct {
	period string
	limit  int
}

func (p *tradingDaysProvider) KLine(_ context.Context, symbol, period string, limit int) ([]foundation.KLine, error) {
	p.period, p.limit = period, limit
	lines := make([]foundation.KLine, 5)
	for i := range lines {
		lines[i] = foundation.KLine{Symbol: symbol, Time: time.Date(2026, 9, 1+i, 0, 0, 0, 0, time.UTC), Open: 10, High: 12, Low: 9, Close: 11, Volume: 100}
	}
	return lines, nil
}
