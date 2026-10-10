package marketcache

import (
	"context"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func providerKey(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	switch {
	case host == "eastmoney.com" || strings.HasSuffix(host, ".eastmoney.com"):
		return "eastmoney"
	case host == "10jqka.com.cn" || strings.HasSuffix(host, ".10jqka.com.cn"):
		return "10jqka"
	case host == "duanxianxia.com" || strings.HasSuffix(host, ".duanxianxia.com"):
		return "duanxianxia"
	case host == "xuangubao.cn" || strings.HasSuffix(host, ".xuangubao.cn"):
		return "xuangubao"
	default:
		return ""
	}
}

func (c *Cache) providerBlockedUntil(ctx context.Context, provider, scope string) (time.Time, error) {
	if c == nil || provider == "" {
		return time.Time{}, nil
	}
	if scope == "" {
		scope = provider
	}
	var unix int64
	err := c.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(blocked_until),0) FROM market_provider_rate_limits WHERE provider IN (?,?)", provider, scope).Scan(&unix)
	if err != nil {
		return time.Time{}, err
	}
	if unix <= 0 {
		return time.Time{}, nil
	}
	return time.Unix(unix, 0), nil
}

func (c *Cache) recordProviderLimit(ctx context.Context, provider string, status int, retryAfter string) {
	if c == nil || provider == "" {
		return
	}
	base := 30 * time.Minute
	if status == http.StatusForbidden {
		base = 60 * time.Minute
	}
	wait := base
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 {
		if requested := time.Duration(seconds) * time.Second; requested > wait {
			wait = requested
		}
	} else if at, err := http.ParseTime(retryAfter); err == nil && at.After(time.Now().Add(wait)) {
		wait = at.Sub(time.Now())
	}
	if wait > 24*time.Hour {
		wait = 24 * time.Hour
	}
	now := time.Now().Unix()
	baseSeconds := int64(base.Seconds())
	_, err := c.db.ExecContext(ctx, `INSERT INTO market_provider_rate_limits(provider,failure_count,blocked_until,last_status,updated_at)
		VALUES(?,1,?,?,?) ON CONFLICT(provider) DO UPDATE SET
		failure_count=market_provider_rate_limits.failure_count+1,
		blocked_until=MAX(excluded.blocked_until,market_provider_rate_limits.blocked_until,
			excluded.updated_at+MIN(86400,?*(1 << MIN(market_provider_rate_limits.failure_count,5)))),
		last_status=excluded.last_status,updated_at=excluded.updated_at`,
		provider, now+int64(wait.Seconds()), status, now, baseSeconds)
	if err != nil {
		log.Printf("market provider cooldown write failed provider=%s: %v", provider, err)
	}
}

func (c *Cache) clearProviderLimit(ctx context.Context, provider string) {
	if c == nil || provider == "" {
		return
	}
	_, _ = c.db.ExecContext(ctx, "DELETE FROM market_provider_rate_limits WHERE provider=? AND blocked_until<=?", provider, time.Now().Unix())
}

func (c *Cache) claimProviderScope(ctx context.Context, scope string) (time.Time, bool, error) {
	if c == nil || scope == "" {
		return time.Time{}, false, nil
	}
	now := time.Now().Unix()
	until := now + int64((30 * time.Minute).Seconds())
	result, err := c.db.ExecContext(ctx, `INSERT INTO market_provider_rate_limits(provider,failure_count,blocked_until,last_status,updated_at)
		VALUES(?,0,?,102,?) ON CONFLICT(provider) DO UPDATE SET failure_count=0,
		blocked_until=excluded.blocked_until,last_status=excluded.last_status,updated_at=excluded.updated_at
		WHERE market_provider_rate_limits.blocked_until<=excluded.updated_at`, scope, until, now)
	if err != nil {
		return time.Time{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return time.Time{}, false, err
	}
	if rows > 0 {
		return time.Unix(until, 0), true, nil
	}
	var blockedUntil int64
	if err := c.db.QueryRowContext(ctx, "SELECT blocked_until FROM market_provider_rate_limits WHERE provider=?", scope).Scan(&blockedUntil); err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(blockedUntil, 0), false, nil
}

func (c *Cache) acquireProviderRequest(ctx context.Context, provider, scope string) (time.Time, error) {
	if provider == "" {
		return time.Time{}, nil
	}
	blockedUntil, err := c.providerBlockedUntil(ctx, provider, scope)
	if err != nil || blockedUntil.After(time.Now()) || scope == "" || scope == provider {
		return blockedUntil, err
	}
	blockedUntil, claimed, err := c.claimProviderScope(ctx, scope)
	if err != nil || claimed {
		return time.Time{}, err
	}
	return blockedUntil, nil
}

func (c *Cache) recordProviderSuccess(ctx context.Context, scope string) {
	if c == nil || scope == "" {
		return
	}
	now := time.Now().Unix()
	blockedUntil := now + int64((30 * time.Minute).Seconds())
	_, err := c.db.ExecContext(ctx, `INSERT INTO market_provider_rate_limits(provider,failure_count,blocked_until,last_status,updated_at)
		VALUES(?,0,?,200,?) ON CONFLICT(provider) DO UPDATE SET failure_count=0,
		blocked_until=MAX(market_provider_rate_limits.blocked_until,excluded.blocked_until),
		last_status=excluded.last_status,updated_at=excluded.updated_at`,
		scope, blockedUntil, now)
	if err != nil {
		log.Printf("market provider request interval write failed provider=%s: %v", scope, err)
	}
}

func isHeatOrSectorRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	path := strings.ToLower(req.URL.Path)
	if isHeatOrSectorPath(path) {
		return true
	}
	if strings.HasSuffix(path, "/api/qt/clist/get") {
		fs := strings.ToLower(req.URL.Query().Get("fs"))
		return strings.Contains(fs, "b:") || strings.Contains(fs, "m:90")
	}
	return false
}

func providerRequestScope(req *http.Request, provider string) string {
	if provider == "" || !isHeatOrSectorRequest(req) {
		return provider
	}
	scope := provider + ":heat:" + strings.ToLower(req.URL.Path)
	fs := strings.ToLower(strings.TrimSpace(req.URL.Query().Get("fs")))
	if fs != "" {
		scope += ":fs=" + fs
	}
	return scope
}

func cooldownResponse(req *http.Request, until time.Time) *http.Response {
	seconds := int(time.Until(until).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	header := make(http.Header)
	header.Set("Retry-After", strconv.Itoa(seconds))
	header.Set("X-Stock-Provider-Cooldown", until.UTC().Format(time.RFC3339))
	return &http.Response{
		Status:     "429 Too Many Requests",
		StatusCode: http.StatusTooManyRequests,
		Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: header,
		Body:   io.NopCloser(strings.NewReader("market provider cooling down")),
		Request: req,
	}
}
