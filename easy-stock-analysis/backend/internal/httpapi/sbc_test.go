package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"easy-stock/backend/internal/foundation"
	"easy-stock/backend/internal/sbc"
)

func sbcRequestBody(t *testing.T) []byte {
	t.Helper()
	bars := make([]foundation.KLine, 30)
	for i := range bars {
		bars[i] = foundation.KLine{Symbol: "600733.SH", Time: time.Date(2025, 1, i+1, 0, 0, 0, 0, time.UTC), Open: 10, High: 11, Low: 9, Close: 10, Volume: 1000, Meta: foundation.SourceMeta{Source: "isolated-test", Stale: true, TradeDate: "2025-01-30"}}
	}
	body, err := json.Marshal(sbc.Request{Symbol: "600733.SH", Period: "day", Bars: bars})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func TestSBCNativeHTTP(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("SBC_PYTHON", "unavailable-python")
	server := NewServer(nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/quotes/sbc/v1/analyze", bytes.NewReader(sbcRequestBody(t)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data struct {
			Implementation string
			Points         []map[string]any
			MarketMeta     foundation.SourceMeta `json:"market_meta"`
			TradeDate      string                `json:"trade_date"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Implementation != "go-native" || len(response.Data.Points) != 30 || !response.Data.MarketMeta.Stale || response.Data.TradeDate != "2025-01-30" {
		t.Fatalf("invalid response metadata: %+v", response.Data)
	}
}
func TestSBCRejectsMalformedHTTP(t *testing.T) {
	valid := string(sbcRequestBody(t))
	for _, body := range []string{`{`, valid + ` {}`, strings.Replace(valid, `"period":"day"`, `"period":"1"`, 1), strings.Repeat(" ", 2<<20) + valid} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/quotes/sbc/v1/analyze", strings.NewReader(body))
		rec := httptest.NewRecorder()
		NewServer(nil).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d want 400", rec.Code)
		}
	}
}
func TestSBCCanceledHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/quotes/sbc/v1/analyze", bytes.NewReader(sbcRequestBody(t))).WithContext(ctx)
	rec := httptest.NewRecorder()
	NewServer(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d want 503: %s", rec.Code, rec.Body.String())
	}
}
