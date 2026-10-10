package httpapi

import (
	"context"
	"easy-stock/backend/internal/foundation"
	"easy-stock/backend/internal/marketbars"
	"easy-stock/backend/internal/providers/tdx"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type tdxFirstKLineProvider struct {
	tdx               *tdx.Client
	fallback          KLineProvider
	preferLocalDaily  bool
}

func (p *tdxFirstKLineProvider) KLine(ctx context.Context, symbol, period string, limit int) ([]foundation.KLine, error) {
	market := strings.ToUpper(marketbars.MarketFromContext(ctx))
	_, inferredMarket := marketbars.NormalizeSymbol(symbol, market)
	if inferredMarket != "" && inferredMarket != "AUTO" { market = inferredMarket }
	if p.preferLocalDaily && market == "CN" && normalizeKLineAggregationPeriod(period) != "" {
		return p.fallback.KLine(ctx, symbol, period, limit)
	}
	if market != "" && market != "CN" {
		return p.fallback.KLine(ctx, symbol, period, limit)
	}
	nativeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	lines, err := p.tdx.KLine(nativeCtx, symbol, period, limit)
	cancel()
	if err == nil && len(lines) > 0 && !lines[len(lines)-1].Meta.Stale {
		return lines, nil
	}
	fallback, e := p.fallback.KLine(ctx, symbol, period, limit)
	if e == nil && len(fallback) > 0 {
		for i := range fallback {
			fallback[i].Meta.FallbackReason = "TDX unavailable or stale; using configured market provider"
		}
		return fallback, nil
	}
	if len(lines) > 0 {
		return lines, nil
	}
	return nil, fmt.Errorf("TDX and market fallback failed: %v; %w", err, e)
}

// Direct native bars retain multiple trading days; the existing chart endpoint
// deliberately reduces period=1 to the latest trading day.
func (s *Server) tdxBars(w http.ResponseWriter, r *http.Request) {
	if s.tdx == nil {
		writeError(w, http.StatusServiceUnavailable, "TDX requires configured external market data storage")
		return
	}
	limit := 1200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil || v < 1 || v > 8000 {
			writeError(w, 400, "limit must be 1..8000")
			return
		}
		limit = v
	}
	days := 0
	if raw := r.URL.Query().Get("days"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil || v < 1 || v > 20 {
			writeError(w, 400, "days must be 1..20")
			return
		}
		days = v
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	lines, err := s.tdx.KLine(ctx, r.URL.Query().Get("symbol"), firstNonEmpty(r.URL.Query().Get("period"), "1"), limit)
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	lines = trailingTDXDays(lines, days)
	writeJSON(w, 200, map[string]any{"vwap_basis": "continuous_requested_window", "data": intradayVWAPIfNeeded(lines, r.URL.Query().Get("period")), "implementation": "go-native-tdx", "volume_unit": "shares", "amount_unit": "CNY", "turnover_basis": "current_float_shares_estimate", "sessions": tdxSessions(lines)})
}
func intradayVWAPIfNeeded(lines []foundation.KLine, period string) []foundation.KLine {
	if period == "" || period == "1" || period == "5" || period == "15" || period == "30" || period == "60" {
		return intradayVWAPWindow(lines, true)
	}
	return lines
}

// Coverage is descriptive: suspensions and newly listed stocks can have fewer
// bars. Do not label a truncated archive as a complete trading session.
func tdxSessions(lines []foundation.KLine) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, b := range lines {
		date := b.Time.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
		entry := out[date]
		if entry == nil {
			entry = map[string]any{"bars": 0, "first": b.Time, "last": b.Time}
			out[date] = entry
		}
		entry["bars"] = entry["bars"].(int) + 1
		entry["last"] = b.Time
	}
	for _, entry := range out {
		entry["full_minute_session_candidate"] = entry["bars"].(int) >= 220 && entry["first"].(time.Time).Hour() == 9 && entry["last"].(time.Time).Hour() == 15
	}
	return out
}

func trailingTDXDays(lines []foundation.KLine, days int) []foundation.KLine {
	if days <= 0 {
		return lines
	}
	seen := map[string]bool{}
	start := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		date := lines[i].Time.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
		if !seen[date] && len(seen) == days {
			break
		}
		seen[date] = true
		start = i
	}
	return lines[start:]
}
