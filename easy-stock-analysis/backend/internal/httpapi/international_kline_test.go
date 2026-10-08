package httpapi

import (
	"context"
	"easy-stock/backend/internal/foundation"
	"easy-stock/backend/internal/marketbars"
	"easy-stock/backend/internal/marketcache"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type internationalTestTransport func(*http.Request) (*http.Response, error)

func (f internationalTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestInternationalMarketRouting(t *testing.T) {
	for _, market := range []string{"US", "CRYPTO"} {
		t.Run(market, func(t *testing.T) {
			p := &internationalKLineProvider{client: &http.Client{Transport: internationalTestTransport(func(r *http.Request) (*http.Response, error) {
				body := `[[1759276800000,"100","120","90","110","10",1759363199999,"1000"]]`
				if market == "US" {
					if r.URL.Hostname() != "query1.finance.yahoo.com" {
						t.Fatal(r.URL)
					}
					body = `{"chart":{"result":[{"timestamp":[1759325400],"indicators":{"quote":[{"open":[100],"high":[120],"low":[90],"close":[110],"volume":[10]}]}}],"error":null}}`
				} else if r.URL.Hostname() != "data-api.binance.vision" {
					t.Fatal(r.URL)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			ctx := marketbars.WithMarket(context.Background(), market)
			code := "BTC"
			if market == "US" {
				code = "AAPL"
			}
			lines, err := p.KLine(ctx, code, "day", 60)
			if err != nil || len(lines) != 1 || lines[0].Close != 110 || lines[0].Symbol != code {
				t.Fatalf("%+v %v", lines, err)
			}
		})
	}
}
func TestCryptoFallbackPreservesBothErrors(t *testing.T) {
	p := &internationalKLineProvider{client: &http.Client{Transport: internationalTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("upstream unavailable"))}, nil
	})}}
	_, err := p.KLine(marketbars.WithMarket(context.Background(), "CRYPTO"), "BTC", "day", 60)
	if err == nil || !strings.Contains(err.Error(), "Binance") || !strings.Contains(err.Error(), "Kraken") || !strings.Contains(err.Error(), "503") {
		t.Fatalf("%v", err)
	}
}

type refreshTestProvider struct{ lines []foundation.KLine }

func (p refreshTestProvider) KLine(ctx context.Context, symbol, period string, limit int) ([]foundation.KLine, error) {
	if !marketcache.RefreshFromContext(ctx) {
		return nil, context.Canceled
	}
	return p.lines, nil
}
func TestRefreshRejectsStaleWithoutOverwritingLocal(t *testing.T) {
	store, err := marketbars.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	day := time.Now().UTC().AddDate(0, 0, -1)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	line := foundation.KLine{Time: day, Open: 100, High: 120, Low: 90, Close: 110, Volume: 10}
	if err := store.UpsertDaily(context.Background(), "BTC", "CRYPTO", []foundation.KLine{line}); err != nil {
		t.Fatal(err)
	}
	line.Close = 115
	line.Meta.Stale = true
	line.Meta.FallbackReason = "connection refused"
	s := &Server{marketBars: store, kLinePrimary: refreshTestProvider{[]foundation.KLine{line}}}
	req := httptest.NewRequest("POST", "/api/v1/market-data/stocks/CRYPTO/BTC/refresh", nil)
	req.SetPathValue("market", "CRYPTO")
	req.SetPathValue("symbol", "BTC")
	out := httptest.NewRecorder()
	s.marketDataStockRefresh(out, req)
	if out.Code != 502 || !strings.Contains(out.Body.String(), "connection refused") {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
	local, _, err := store.ReadDaily(context.Background(), "BTC", "CRYPTO", 60)
	if err != nil || len(local) != 1 || local[0].Close != 110 {
		t.Fatalf("local changed: %+v %v", local, err)
	}
	line.Meta.Stale = false
	s.kLinePrimary = refreshTestProvider{[]foundation.KLine{line}}
	out = httptest.NewRecorder()
	s.marketDataStockRefresh(out, req)
	if out.Code != 200 {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
	local, _, err = store.ReadDaily(context.Background(), "BTC", "CRYPTO", 60)
	if err != nil || len(local) != 1 || local[0].Close != 115 {
		t.Fatalf("refresh not persisted: %+v %v", local, err)
	}
}

func TestAppendFailureRetainsUpstreamReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.txt")
	now := time.Now()
	text := "[" + now.Format(time.RFC3339) + "] cn append started\nbatch 0 FAIL connection refused\n[" + now.Format(time.RFC3339) + "] cn append failed (exit=1)\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	status := scheduledJobStatus{ID: "cn"}
	if !readMarketAppendLog(&status, path, now) || status.LastStatus != "失败" || !strings.Contains(status.LastError, "connection refused") {
		t.Fatalf("%+v", status)
	}
}
