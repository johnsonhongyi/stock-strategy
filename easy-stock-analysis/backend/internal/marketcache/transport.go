package marketcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const maxCachedBodyBytes = 16 << 20

type cacheTransport struct {
	cache *Cache
	next  http.RoundTripper
}

type replayBody struct {
	io.Reader
	io.Closer
}

func (t *cacheTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !cacheableRequest(req) {
		return t.next.RoundTrip(req)
	}
	key := cacheKey(req)
	if key == "" {
		return t.next.RoundTrip(req)
	}
	forceRefresh := RefreshRequested(req.Header) || RefreshFromContext(req.Context())
	cached, err := t.cache.get(req.Context(), key)
	if err != nil {
		log.Printf("market cache read failed: %v", err)
	}
	now := time.Now()
	if !forceRefresh && cached != nil && now.Before(cached.expiresAt) {
		return cached.response(req, "HIT"), nil
	}

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		if usableStale(req, cached, now) {
			log.Printf("market cache stale fallback host=%s age=%s", req.URL.Hostname(), now.Sub(cached.fetchedAt).Round(time.Second))
			return cached.response(req, "STALE"), nil
		}
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
		if usableStale(req, cached, now) {
			_ = resp.Body.Close()
			log.Printf("market cache stale fallback host=%s status=%d", req.URL.Hostname(), resp.StatusCode)
			return cached.response(req, "STALE"), nil
		}
	}
	if resp.StatusCode != http.StatusOK ||
		strings.Contains(strings.ToLower(resp.Header.Get("Cache-Control")), "no-store") ||
		resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Vary") == "*" {
		resp.Header.Set("X-Stock-Cache", "MISS")
		return resp, nil
	}
	return t.bufferAndStore(req, resp, key, now)
}

func (t *cacheTransport) bufferAndStore(req *http.Request, resp *http.Response, key string, now time.Time) (*http.Response, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCachedBodyBytes+1))
	if err != nil || len(body) > maxCachedBodyBytes {
		resp.Body = replayBody{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), Closer: resp.Body}
		resp.Header.Set("X-Stock-Cache", "MISS")
		return resp, nil
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	entry := &entry{
		statusCode: resp.StatusCode,
		header:     resp.Header.Clone(),
		body:       body,
		fetchedAt:  now,
		expiresAt:  now.Add(cacheTTL(req)),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := t.cache.put(ctx, key, entry); err != nil {
		log.Printf("market cache write failed: %v", err)
	}
	resp.Header.Set("X-Stock-Cache", "MISS")
	return resp, nil
}

func (e *entry) response(req *http.Request, state string) *http.Response {
	header := e.header.Clone()
	header.Set("X-Stock-Cache", state)
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", e.statusCode, http.StatusText(e.statusCode)),
		StatusCode:    e.statusCode,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(e.body)),
		ContentLength: int64(len(e.body)),
		Request:       req,
	}
}

func cacheableRequest(req *http.Request) bool {
	if (req.Method != http.MethodGet && req.Method != http.MethodPost) || req.URL == nil || req.URL.Host == "" {
		return false
	}
	if req.URL.User != nil {
		return false
	}
	if req.Method == http.MethodGet && req.Body != nil && req.Body != http.NoBody {
		return false
	}
	if req.Method == http.MethodPost && req.GetBody == nil {
		return false
	}
	if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" || req.Header.Get("Range") != "" ||
		req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "" {
		return false
	}
	control := strings.ToLower(req.Header.Get("Cache-Control"))
	return !strings.Contains(control, "no-cache") && !strings.Contains(control, "no-store")
}

func cacheKey(req *http.Request) string {
	stableURL := *req.URL
	query := stableURL.Query()
	for key := range query {
		switch strings.ToLower(key) {
		case "_", "_t", "_ts", "timestamp", "nonce", "requestid", "rn":
			query.Del(key)
		case "access_token", "api_key", "token", "x-a-stock-token", "authorization":
			return ""
		}
	}
	stableURL.Scheme = strings.ToLower(stableURL.Scheme)
	stableURL.Host = strings.ToLower(stableURL.Host)
	stableURL.Fragment = ""
	stableURL.RawQuery = query.Encode()
	bodyHash := ""
	if req.Method == http.MethodPost {
		body, err := req.GetBody()
		if err != nil {
			return ""
		}
		payload, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			return ""
		}
		sum := sha256.Sum256(payload)
		bodyHash = hex.EncodeToString(sum[:])
	}
	value := strings.Join([]string{strings.ToUpper(req.Method), stableURL.String(), bodyHash}, "\n")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func cacheTTL(req *http.Request) time.Duration {
	host := strings.ToLower(req.URL.Hostname())
	path := strings.ToLower(req.URL.Path)
	query := req.URL.Query()
	if strings.Contains(host, "sinajs.cn") || strings.Contains(host, "gtimg.cn") ||
		strings.Contains(path, "/api/qt/stock/get") || strings.HasSuffix(path, "/quotes/realtime") || strings.HasSuffix(path, "/ticker") {
		return 15 * time.Second
	}
	if strings.Contains(path, "kline") || req.URL.Query().Get("klt") != "" || req.URL.Query().Get("scale") != "" ||
		strings.HasSuffix(path, "/ohlc") || strings.HasSuffix(path, "/chart") {
		period := query.Get("period")
		if period == "" {
			period = query.Get("interval")
		}
		if period == "" {
			period = query.Get("klt")
		}
		if period == "" {
			period = query.Get("scale")
		}
		switch strings.ToLower(period) {
		case "1", "5", "15", "30", "60", "1m", "2m", "5m", "15m", "30m", "60m", "1h":
			return 45 * time.Second
		}
		return 5 * time.Minute
	}
	if strings.HasSuffix(path, "/stocks/directory") || strings.HasSuffix(path, "/assets") {
		return 24 * time.Hour
	}
	if strings.Contains(path, "limit_up_pool") && query.Get("date") != "" {
		return 30 * 24 * time.Hour
	}
	if strings.Contains(host, "10jqka.com.cn") || strings.Contains(path, "stockrank") {
		return 30 * time.Second
	}
	if strings.Contains(path, "news") || strings.Contains(path, "telegraph") {
		return 2 * time.Minute
	}
	return 5 * time.Minute
}

func usableStale(req *http.Request, cached *entry, now time.Time) bool {
	if cached == nil || now.Before(cached.fetchedAt) {
		return false
	}
	host := strings.ToLower(req.URL.Hostname())
	path := strings.ToLower(req.URL.Path)
	maxAge := 7 * 24 * time.Hour
	if strings.Contains(host, "sinajs.cn") || strings.Contains(host, "gtimg.cn") ||
		strings.Contains(path, "/api/qt/stock/get") || strings.HasSuffix(path, "/quotes/realtime") || strings.HasSuffix(path, "/ticker") {
		maxAge = 2 * time.Hour
	} else if strings.Contains(host, "10jqka.com.cn") || strings.Contains(path, "stockrank") {
		maxAge = 12 * time.Hour
	} else if strings.Contains(path, "kline") || req.URL.Query().Get("klt") != "" || req.URL.Query().Get("scale") != "" ||
		strings.HasSuffix(path, "/ohlc") || strings.HasSuffix(path, "/chart") {
		maxAge = 30 * 24 * time.Hour
	} else if strings.Contains(path, "news") || strings.Contains(path, "telegraph") {
		maxAge = 24 * time.Hour
	}
	return now.Sub(cached.fetchedAt) <= maxAge
}
