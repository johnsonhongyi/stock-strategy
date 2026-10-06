package httpapi

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"easy-stock/backend/internal/foundation"
	"easy-stock/backend/internal/marketbars"
	"easy-stock/backend/internal/marketcache"
	"easy-stock/backend/internal/tradingcalendar"
)

type marketFirstKLineProvider struct {
	store    *marketbars.Store
	primary  KLineProvider
	fallback KLineProvider
	logger   *log.Logger
	refreshMu       sync.Mutex
	refreshAttempts map[string]refreshAttempt
}

type refreshAttempt struct {
	session string
	at      time.Time
}

func (p *marketFirstKLineProvider) KLine(ctx context.Context, symbol string, period string, limit int) ([]foundation.KLine, error) {
	market := marketbars.MarketFromContext(ctx)
	if limit <= 0 {
		limit = 120
	}

	switch normalizeKLineAggregationPeriod(period) {
	case "day":
		return p.daily(ctx, symbol, market, limit)
	case "week", "month":
		return p.derivedPeriod(ctx, symbol, market, normalizeKLineAggregationPeriod(period), limit)
	default:
		return p.fetchWithFallback(ctx, symbol, period, limit)
	}
}

func (p *marketFirstKLineProvider) daily(ctx context.Context, symbol string, market string, limit int) ([]foundation.KLine, error) {
	cached, found, readErr := p.store.ReadDaily(ctx, symbol, market, limit)
	if readErr != nil {
		p.logf("market data read failed for %s: %v", symbol, readErr)
		found = false
	}
	forceRefresh := marketcache.RefreshFromContext(ctx)
	if found && !forceRefresh {
		stale, expected, calendarErr := klineCacheBehindLatestSession(cached, market)
		if calendarErr != nil {
			p.logf("trading calendar unavailable for %s: %v", symbol, calendarErr)
			return cached, nil
		}
		if !stale {
			return cached, nil
		}
		if !p.claimSessionRefresh(market, symbol, expected) {
			return markKLinesStale(cached, fmt.Errorf("waiting for the next refresh window for %s session %s", market, expected)), nil
		}
		p.logf("cached daily bars for %s end before completed %s session %s; refreshing", symbol, market, expected)
	}

	lines, err := p.fetchWithFallback(ctx, symbol, "day", limit)
	if err != nil {
		if found {
			return markKLinesStale(cached, err), nil
		}
		return nil, err
	}
	if len(lines) > 0 {
		if err := p.store.UpsertDaily(ctx, symbol, market, lines); err != nil {
			p.logf("market data write failed for %s: %v", symbol, err)
		}
	}
	if stale, expected, calendarErr := klineCacheBehindLatestSession(lines, market); calendarErr == nil && stale {
		return markKLinesStale(lines, fmt.Errorf("upstream data has not reached the completed %s session %s", market, expected)), nil
	}
	return lines, nil
}

func (p *marketFirstKLineProvider) derivedPeriod(ctx context.Context, symbol string, market string, period string, limit int) ([]foundation.KLine, error) {
	dailyLimit := dailyLookbackLimit(period, limit)
	cached, found, readErr := p.store.ReadDaily(ctx, symbol, market, dailyLimit)
	if readErr != nil {
		p.logf("market data read failed for %s: %v", symbol, readErr)
		found = false
	}
	derived := aggregateDailyKLines(cached, period)
	forceRefresh := marketcache.RefreshFromContext(ctx)
	if found && !forceRefresh && len(derived) > 0 {
		stale, expected, calendarErr := klineCacheBehindLatestSession(cached, market)
		if calendarErr != nil {
			p.logf("trading calendar unavailable for %s: %v", symbol, calendarErr)
			return lastKLines(derived, limit), nil
		}
		if !stale {
			return lastKLines(derived, limit), nil
		}
		if !p.claimSessionRefresh(market, symbol, expected) {
			return markKLinesStale(lastKLines(derived, limit), fmt.Errorf("waiting for the next refresh window for %s session %s", market, expected)), nil
		}
		p.logf("cached daily bars for %s end before completed %s session %s; refreshing before aggregation", symbol, market, expected)
	}

	lines, err := p.fetchWithFallback(ctx, symbol, "day", dailyLimit)
	if err != nil {
		if len(derived) > 0 {
			return markKLinesStale(lastKLines(derived, limit), err), nil
		}
		return nil, err
	}
	if err := p.store.UpsertDaily(ctx, symbol, market, lines); err != nil {
		p.logf("market data write failed for %s: %v", symbol, err)
	}

	allDaily := mergeDailyKLines(cached, lines)
	if persisted, persistedFound, persistErr := p.store.ReadDaily(ctx, symbol, market, dailyLimit); persistErr == nil && persistedFound {
		allDaily = persisted
	} else if persistErr != nil {
		p.logf("market data reread failed for %s: %v", symbol, persistErr)
	}
	result := lastKLines(aggregateDailyKLines(allDaily, period), limit)
	if stale, expected, calendarErr := klineCacheBehindLatestSession(allDaily, market); calendarErr == nil && stale {
		return markKLinesStale(result, fmt.Errorf("local daily bars have not reached the completed %s session %s", market, expected)), nil
	}
	return result, nil
}

func klineCacheBehindLatestSession(lines []foundation.KLine, market string) (bool, string, error) {
	expected, err := tradingcalendar.LatestCompletedDate(market, time.Now())
	if err != nil {
		return false, "", err
	}
	latest := ""
	for _, line := range lines {
		if line.Time.IsZero() {
			continue
		}
		date := line.Time.Format("2006-01-02")
		if date > latest {
			latest = date
		}
	}
	return latest == "" || latest < expected, expected, nil
}

func (p *marketFirstKLineProvider) claimSessionRefresh(market string, symbol string, session string) bool {
	const retryWindow = 15 * time.Minute
	key := strings.ToUpper(strings.TrimSpace(market)) + ":" + strings.ToUpper(strings.TrimSpace(symbol))
	now := time.Now()
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	if p.refreshAttempts == nil {
		p.refreshAttempts = make(map[string]refreshAttempt)
	}
	if previous, ok := p.refreshAttempts[key]; ok && previous.session == session && now.Sub(previous.at) < retryWindow {
		return false
	}
	p.refreshAttempts[key] = refreshAttempt{session: session, at: now}
	return true
}

func (p *marketFirstKLineProvider) fetchWithFallback(ctx context.Context, symbol string, period string, limit int) ([]foundation.KLine, error) {
	lines, primaryErr := p.fetch(ctx, p.primary, symbol, period, limit)
	if primaryErr == nil {
		return lines, nil
	}
	lines, fallbackErr := p.fetch(ctx, p.fallback, symbol, period, limit)
	if fallbackErr == nil {
		return lines, nil
	}
	return nil, fmt.Errorf("primary and fallback kline providers failed: %v; %w", primaryErr, fallbackErr)
}

func (p *marketFirstKLineProvider) fetch(ctx context.Context, provider KLineProvider, symbol string, period string, limit int) ([]foundation.KLine, error) {
	if provider == nil {
		return nil, fmt.Errorf("kline provider is unavailable")
	}
	lines, err := provider.KLine(ctx, symbol, period, limit)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("provider returned no kline rows")
	}
	return normalizeKLinePeriod(lines, period), nil
}

func (p *marketFirstKLineProvider) logf(format string, args ...any) {
	if p.logger != nil {
		p.logger.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func normalizeKLineAggregationPeriod(period string) string {
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "day", "daily", "1d", "101":
		return "day"
	case "week", "weekly", "1w", "102":
		return "week"
	case "month", "monthly", "103":
		return "month"
	default:
		return ""
	}
}

func dailyLookbackLimit(period string, limit int) int {
	factor := 23
	if period == "week" {
		factor = 6
	}
	const maxDailyBars = 5000
	if limit <= 0 || limit > maxDailyBars/factor {
		return maxDailyBars
	}
	return min(limit*factor+factor, maxDailyBars)
}

func aggregateDailyKLines(lines []foundation.KLine, period string) []foundation.KLine {
	if len(lines) == 0 {
		return nil
	}
	const shanghaiOffset = 8 * 60 * 60
	location := time.FixedZone("Asia/Shanghai", shanghaiOffset)
	sorted := append([]foundation.KLine(nil), lines...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })

	result := make([]foundation.KLine, 0, len(sorted))
	keys := make([]string, 0, len(sorted))
	for _, line := range sorted {
		if line.Time.IsZero() {
			continue
		}
		localTime := line.Time.In(location)
		key := localTime.Format("2006-01")
		if period == "week" {
			year, week := localTime.ISOWeek()
			key = fmt.Sprintf("%04d-W%02d", year, week)
		}
		if len(result) == 0 || keys[len(keys)-1] != key {
			line.Time = localTime
			line.Meta.Source = "local-bars.db:derived-" + period
			result = append(result, line)
			keys = append(keys, key)
			continue
		}

		current := &result[len(result)-1]
		if line.High > current.High {
			current.High = line.High
		}
		if line.Low < current.Low || current.Low == 0 {
			current.Low = line.Low
		}
		current.Close = line.Close
		current.Time = localTime
		current.Volume += line.Volume
		current.Amount += line.Amount
		current.TurnoverRate += line.TurnoverRate
		current.Meta.Stale = current.Meta.Stale || line.Meta.Stale
		if line.Meta.FetchedAt.After(current.Meta.FetchedAt) {
			current.Meta.FetchedAt = line.Meta.FetchedAt
		}
		if current.PreviousClose <= 0 && line.PreviousClose > 0 {
			current.PreviousClose = line.PreviousClose
		}
	}

	for i := range result {
		if result[i].PreviousClose <= 0 && i > 0 {
			result[i].PreviousClose = result[i-1].Close
		}
		if result[i].PreviousClose > 0 {
			result[i].ChangePercent = (result[i].Close/result[i].PreviousClose - 1) * 100
		}
	}
	return result
}

func mergeDailyKLines(cached []foundation.KLine, fresh []foundation.KLine) []foundation.KLine {
	const shanghaiOffset = 8 * 60 * 60
	location := time.FixedZone("Asia/Shanghai", shanghaiOffset)
	byDate := make(map[string]foundation.KLine, len(cached)+len(fresh))
	for _, line := range cached {
		if !line.Time.IsZero() {
			byDate[line.Time.In(location).Format("2006-01-02")] = line
		}
	}
	for _, line := range fresh {
		if !line.Time.IsZero() {
			byDate[line.Time.In(location).Format("2006-01-02")] = line
		}
	}
	merged := make([]foundation.KLine, 0, len(byDate))
	for _, line := range byDate {
		merged = append(merged, line)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Time.Before(merged[j].Time) })
	return merged
}

func lastKLines(lines []foundation.KLine, limit int) []foundation.KLine {
	if limit <= 0 || len(lines) <= limit {
		return lines
	}
	return append([]foundation.KLine(nil), lines[len(lines)-limit:]...)
}

func markKLinesStale(lines []foundation.KLine, err error) []foundation.KLine {
	result := append([]foundation.KLine(nil), lines...)
	for i := range result {
		result[i].Meta.Stale = true
		if err != nil {
			result[i].Meta.FallbackReason = err.Error()
		}
	}
	return result
}
