package tdx

import (
	"context"
	"easy-stock/backend/internal/foundation"
	"easy-stock/backend/internal/marketcache"
	"easy-stock/backend/internal/tradingcalendar"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

type Client struct {
	cache     *cache
	hosts     []string
	slots     chan struct{}
	mu        sync.Mutex
	retryAt   time.Time
	preferred int
}

func New(path string, hosts []string) (*Client, error) {
	c, err := openCache(path)
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		hosts = []string{"117.34.114.16:7709", "117.34.114.14:7709", "223.112.100.140:7709"}
	}
	if len(hosts) > 8 {
		c.db.Close()
		return nil, fmt.Errorf("at most 8 TDX servers")
	}
	for i, h := range hosts {
		hosts[i] = strings.TrimSpace(h)
		if _, _, err := net.SplitHostPort(strings.TrimSpace(h)); err != nil {
			c.db.Close()
			return nil, fmt.Errorf("invalid TDX server")
		}
	}
	return &Client{cache: c, hosts: hosts, slots: make(chan struct{}, 2)}, nil
}
func (c *Client) Close() error { return c.cache.db.Close() }
func normalize(symbol string) (string, string, int, error) {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	market := -1
	switch {
	case strings.HasSuffix(s, ".SH"):
		market = 1
		s = strings.TrimSuffix(s, ".SH")
	case strings.HasSuffix(s, ".SZ"):
		market = 0
		s = strings.TrimSuffix(s, ".SZ")
	case strings.HasSuffix(s, ".BJ"):
		market = 2
		s = strings.TrimSuffix(s, ".BJ")
	case strings.HasPrefix(s, "SH"):
		market = 1
		s = s[2:]
	case strings.HasPrefix(s, "SZ"):
		market = 0
		s = s[2:]
	case strings.HasPrefix(s, "BJ"):
		market = 2
		s = s[2:]
	}
	if len(s) != 6 {
		return "", "", 0, fmt.Errorf("TDX supports SH/SZ/BJ A shares only")
	}
	for _, v := range s {
		if v < '0' || v > '9' {
			return "", "", 0, fmt.Errorf("invalid TDX symbol")
		}
	}
	if market < 0 {
		if s[0] == '6' {
			market = 1
		} else if s[0] == '0' || s[0] == '3' {
			market = 0
		} else if strings.HasPrefix(s, "92") || strings.HasPrefix(s, "89") ||
			strings.HasPrefix(s, "88") || strings.HasPrefix(s, "87") ||
			strings.HasPrefix(s, "83") || strings.HasPrefix(s, "82") ||
			strings.HasPrefix(s, "43") {
			market = 2
		} else {
			return "", "", 0, fmt.Errorf("unsupported TDX market")
		}
	}
	beijing := strings.HasPrefix(s, "92") || strings.HasPrefix(s, "89") ||
		strings.HasPrefix(s, "88") || strings.HasPrefix(s, "87") ||
		strings.HasPrefix(s, "83") || strings.HasPrefix(s, "82") ||
		strings.HasPrefix(s, "43")
	if (market == 1 && s[0] != '6') || (market == 0 && s[0] != '0' && s[0] != '3') ||
		(market == 2 && !beijing) {
		return "", "", 0, fmt.Errorf("TDX A-share market mismatch")
	}
	suffix := ".SZ"
	if market == 1 {
		suffix = ".SH"
	} else if market == 2 {
		suffix = ".BJ"
	}
	return s, s + suffix, market, nil
}
func category(period string) (int, bool) {
	v, ok := map[string]int{"1": 8, "5": 0, "15": 1, "30": 2, "60": 3, "day": 9, "week": 5, "month": 6}[period]
	return v, ok
}
func (c *Client) KLine(ctx context.Context, symbol, period string, limit int) ([]foundation.KLine, error) {
	code, symbol, market, err := normalize(symbol)
	if err != nil {
		return nil, err
	}
	cat, ok := category(period)
	if !ok {
		return nil, fmt.Errorf("unsupported TDX period")
	}
	if limit < 1 || limit > 8000 {
		return nil, fmt.Errorf("TDX limit must be 1..8000")
	}
	now := time.Now()
	cached, stamp, readErr := c.cache.read(ctx, symbol, period, limit)
	ttl := 30 * time.Second
	if period == "day" || period == "week" || period == "month" {
		ttl = 5 * time.Minute
	}
	if readErr == nil && now.Sub(stamp) < ttl && !marketcache.RefreshFromContext(ctx) {
		if len(cached) == 0 {
			return []foundation.KLine{}, nil
		}
		if len(cached) >= limit {
			return markFreshness(cached, now), nil
		}
	}
	c.mu.Lock()
	retry, preferred := c.retryAt, c.preferred
	c.mu.Unlock()
	if now.Before(retry) {
		if len(cached) > 0 {
			return stale(cached, "TDX reconnect cooldown"), nil
		}
		return nil, fmt.Errorf("TDX reconnect cooldown")
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.Lock()
	retry = c.retryAt
	c.mu.Unlock()
	if time.Now().Before(retry) {
		if len(cached) > 0 {
			return stale(cached, "TDX reconnect cooldown"), nil
		}
		return nil, fmt.Errorf("TDX reconnect cooldown")
	}
	// A second caller may have filled the cache while this caller was waiting.
	if rows, at, e := c.cache.read(ctx, symbol, period, limit); e == nil && len(rows) >= limit && at.After(stamp) && !marketcache.RefreshFromContext(ctx) {
		return markFreshness(rows, time.Now()), nil
	}
	var result []foundation.KLine
	var last error
	for i := 0; i < len(c.hosts) && i < 3; i++ {
		if ctx.Err() != nil {
			break
		}
		idx := (preferred + i) % len(c.hosts)
		result, last = c.fetch(ctx, c.hosts[idx], code, symbol, market, cat, limit)
		if last == nil {
			c.mu.Lock()
			c.preferred = idx
			c.retryAt = time.Time{}
			c.mu.Unlock()
			if len(result) > 0 {
				break
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if last != nil {
		c.mu.Lock()
		c.retryAt = time.Now().Add(time.Minute)
		c.mu.Unlock()
		if len(cached) > 0 {
			return stale(cached, fmt.Sprint(last)), nil
		}
		return nil, fmt.Errorf("TDX unavailable: %v", last)
	}
	if len(result) == 0 {
		if len(cached) > 0 {
			return stale(cached, "TDX server returned no bars"), nil
		}
		if err = c.cache.write(ctx, symbol, period, result, time.Now()); err != nil {
			return nil, fmt.Errorf("persist empty TDX result: %w", err)
		}
		return []foundation.KLine{}, nil
	}
	result = markFreshness(result, time.Now())
	if err = c.cache.write(ctx, symbol, period, result, time.Now()); err != nil {
		return nil, fmt.Errorf("persist TDX bars: %w", err)
	}
	return result, nil
}
func (c *Client) fetch(ctx context.Context, host, code, symbol string, market, cat, limit int) ([]foundation.KLine, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	for _, s := range setup {
		request, e := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
		if e != nil {
			return nil, e
		}
		if _, err = exchange(conn, request); err != nil {
			return nil, err
		}
	}
	byTime := map[int64]foundation.KLine{}
	now := time.Now()
	for start := 0; start < limit; start += 800 {
		count := min(800, limit-start)
		body, e := exchange(conn, barRequest(cat, market, start, count, code))
		if e != nil {
			return nil, e
		}
		page, e := decodeBars(body, cat, symbol, now)
		if e != nil {
			return nil, e
		}
		for _, b := range page {
			byTime[b.Time.Unix()] = b
		}
		if len(page) < count {
			break
		}
	}
	out := make([]foundation.KLine, 0, len(byTime))
	for _, b := range byTime {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	// Fetch actual prior daily closes for minute percentages, never substitute
	// the preceding minute as yesterday's close.
	previous := map[string]float64{}
	minute := cat < 4 || cat == 7 || cat == 8
	if minute {
		body, e := exchange(conn, barRequest(9, market, 0, min(800, limit/240+10), code))
		if e == nil {
			daily, e := decodeBars(body, 9, symbol, now)
			if e == nil {
				for i := 1; i < len(daily); i++ {
					previous[daily[i].Time.Format("2006-01-02")] = daily[i-1].Close
				}
			}
		}
	}
	// Current float shares provide estimated, not historical exact turnover.
	// Attach only when bars do not predate the financial report.
	shares, updated := c.finance(ctx, conn, code, symbol, market, now)
	for i := range out {
		out[i].Meta.SourceURL = "tdx://" + host
		if minute {
			out[i].PreviousClose = previous[out[i].Time.Format("2006-01-02")]
		} else if i > 0 {
			out[i].PreviousClose = out[i-1].Close
		}
		if out[i].PreviousClose > 0 {
			out[i].ChangePercent = (out[i].Close/out[i].PreviousClose - 1) * 100
		}
		if shares > 0 && out[i].Time.In(zone).Format("2006-01-02") >= updated {
			out[i].TurnoverRate = out[i].Volume / shares * 100
			out[i].TurnoverEstimated = true
			out[i].Meta.AvailableFields = append(out[i].Meta.AvailableFields, "turnover_estimated_float_shares_"+updated)
		}
	}
	return out, nil
}
func (c *Client) finance(ctx context.Context, conn net.Conn, code, symbol string, market int, now time.Time) (float64, string) {
	var shares float64
	var updated string
	var fetched int64
	if err := c.cache.db.QueryRowContext(ctx, "SELECT shares,updated,fetched FROM finance WHERE symbol=?", symbol).Scan(&shares, &updated, &fetched); err == nil && now.Sub(time.Unix(fetched, 0)) < 24*time.Hour {
		return shares, updated
	}
	b, _ := hex.DecodeString("0c1f187600010b000b0010000100")
	b = append(b, byte(market))
	b = append(b, []byte(code)...)
	body, err := exchange(conn, b)
	if err != nil || len(body) < 25 || le.Uint16(body) != 1 || int(body[2]) != market || string(body[3:9]) != code {
		return 0, ""
	}
	shares = float64(math.Float32frombits(le.Uint32(body[9:]))) * 10000
	date := le.Uint32(body[17:])
	t, err := time.Parse("20060102", fmt.Sprint(date))
	if err != nil || t.After(now) || !finite(shares) || shares <= 0 {
		return 0, ""
	}
	updated = t.Format("2006-01-02")
	if _, err = c.cache.db.ExecContext(ctx, "INSERT OR REPLACE INTO finance VALUES(?,?,?,?)", symbol, shares, updated, now.Unix()); err != nil {
		return 0, ""
	}
	return shares, updated
}
func stale(lines []foundation.KLine, reason string) []foundation.KLine {
	for i := range lines {
		lines[i].Meta.Stale = true
		lines[i].Meta.FallbackReason = reason
	}
	return lines
}
func markFreshness(lines []foundation.KLine, now time.Time) []foundation.KLine {
	expected, err := tradingcalendar.LatestCompletedDate("CN", now)
	if err != nil {
		return stale(lines, "trading calendar unavailable")
	}
	if len(lines) == 0 {
		return lines
	}
	last := lines[len(lines)-1].Time
	if last.In(zone).Format("2006-01-02") < expected {
		return stale(lines, "TDX bars precede completed trading session")
	}
	active, e := tradingcalendar.IsTradingSession("CN", now)
	if e != nil {
		return stale(lines, "trading calendar unavailable")
	}
	if active && now.Sub(last) > 15*time.Minute {
		return stale(lines, "TDX latest bar delayed during trading session")
	}
	return lines
}
