package marketcache

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const RefreshHeader = "X-Stock-Cache-Refresh"

const retentionPeriod = 30 * 24 * time.Hour

type Cache struct {
	db *sql.DB
}

type entry struct {
	statusCode int
	header     http.Header
	body       []byte
	fetchedAt  time.Time
	expiresAt  time.Time
}

type refreshContextKey struct{}

func Open(path string) (*Cache, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("market cache database path is empty")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, fmt.Errorf("create market cache directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open market cache database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`CREATE TABLE IF NOT EXISTS market_provider_http_cache (
			cache_key TEXT PRIMARY KEY,
			status_code INTEGER NOT NULL,
			headers_json TEXT NOT NULL,
			body BLOB NOT NULL,
			fetched_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS market_provider_http_cache_fetched_at
			ON market_provider_http_cache(fetched_at)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize market cache database: %w", err)
		}
	}
	cache := &Cache{db: db}
	if err := cache.migrateLegacyPythonCache(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate legacy Python market cache: %w", err)
	}
	return cache, nil
}

func (c *Cache) migrateLegacyPythonCache(ctx context.Context) error {
	var exists int
	err := c.db.QueryRowContext(ctx, "SELECT 1 FROM sqlite_master WHERE type='table' AND name='http_cache'").Scan(&exists)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := c.db.QueryContext(ctx, "SELECT safe_url,status,content_type,body,fetched_at,expires_at FROM http_cache")
	if err != nil {
		return err
	}
	type legacyEntry struct {
		url         string
		status      int
		contentType string
		body        []byte
		fetchedAt   float64
		expiresAt   float64
	}
	var entries []legacyEntry
	for rows.Next() {
		var value legacyEntry
		if err := rows.Scan(&value.url, &value.status, &value.contentType, &value.body, &value.fetchedAt, &value.expiresAt); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, value := range entries {
		req, err := http.NewRequest(http.MethodGet, value.url, nil)
		if err != nil {
			continue
		}
		key := cacheKey(req)
		if key == "" {
			continue
		}
		header := http.Header{}
		if value.contentType != "" {
			header.Set("Content-Type", value.contentType)
		}
		headerJSON, err := json.Marshal(header)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO market_provider_http_cache (cache_key,status_code,headers_json,body,fetched_at,expires_at) VALUES(?,?,?,?,?,?)",
			key, value.status, string(headerJSON), value.body, int64(value.fetchedAt*float64(time.Second)), int64(value.expiresAt*float64(time.Second)))
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, "DROP TABLE http_cache")
	return err
}

func (c *Cache) Close() error { return c.db.Close() }

func (c *Cache) RoundTripper(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &cacheTransport{cache: c, next: next}
}

func WithRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, refreshContextKey{}, true)
}

func RefreshFromContext(ctx context.Context) bool {
	refresh, _ := ctx.Value(refreshContextKey{}).(bool)
	return refresh
}

func RefreshRequested(header http.Header) bool {
	switch strings.ToLower(strings.TrimSpace(header.Get(RefreshHeader))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func (c *Cache) get(ctx context.Context, key string) (*entry, error) {
	var cached entry
	var headers string
	var fetchedAt, expiresAt int64
	err := c.db.QueryRowContext(ctx, `SELECT status_code, headers_json, body, fetched_at, expires_at
		FROM market_provider_http_cache WHERE cache_key = ?`, key).
		Scan(&cached.statusCode, &headers, &cached.body, &fetchedAt, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(headers), &cached.header); err != nil {
		return nil, err
	}
	cached.fetchedAt = time.Unix(0, fetchedAt)
	cached.expiresAt = time.Unix(0, expiresAt)
	return &cached, nil
}

func (c *Cache) put(ctx context.Context, key string, cached *entry) error {
	headers, err := json.Marshal(cached.header)
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, `INSERT INTO market_provider_http_cache
		(cache_key, status_code, headers_json, body, fetched_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET status_code=excluded.status_code,
		headers_json=excluded.headers_json, body=excluded.body,
		fetched_at=excluded.fetched_at, expires_at=excluded.expires_at`,
		key, cached.statusCode, string(headers), cached.body,
		cached.fetchedAt.UnixNano(), cached.expiresAt.UnixNano())
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, `DELETE FROM market_provider_http_cache WHERE fetched_at < ?`,
		time.Now().Add(-retentionPeriod).UnixNano())
	return err
}
