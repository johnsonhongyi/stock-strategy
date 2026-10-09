package tdx

import (
	"context"
	"database/sql"
	"easy-stock/backend/internal/foundation"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type cache struct{ db *sql.DB }

func openCache(path string) (*cache, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("TDX cache requires an absolute external data path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;
 CREATE TABLE IF NOT EXISTS bars(symbol TEXT NOT NULL,period TEXT NOT NULL,time TEXT NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(symbol,period,time));
 CREATE TABLE IF NOT EXISTS snapshots(symbol TEXT NOT NULL,period TEXT NOT NULL,fetched INTEGER NOT NULL,PRIMARY KEY(symbol,period));
 CREATE TABLE IF NOT EXISTS finance(symbol TEXT PRIMARY KEY,shares REAL NOT NULL,updated TEXT NOT NULL,fetched INTEGER NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &cache{db}, nil
}
func (c *cache) read(ctx context.Context, symbol, period string, limit int) ([]foundation.KLine, time.Time, error) {
	var stamp int64
	if err := c.db.QueryRowContext(ctx, "SELECT fetched FROM snapshots WHERE symbol=? AND period=?", symbol, period).Scan(&stamp); err != nil {
		return nil, time.Time{}, err
	}
	rows, err := c.db.QueryContext(ctx, "SELECT payload FROM bars WHERE symbol=? AND period=? ORDER BY time DESC LIMIT ?", symbol, period, limit)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	var out []foundation.KLine
	for rows.Next() {
		var payload []byte
		var bar foundation.KLine
		if err = rows.Scan(&payload); err != nil {
			return nil, time.Time{}, err
		}
		if err = json.Unmarshal(payload, &bar); err != nil {
			return nil, time.Time{}, err
		}
		out = append(out, bar)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, time.Unix(stamp, 0), rows.Err()
}
func (c *cache) write(ctx context.Context, symbol, period string, bars []foundation.KLine, now time.Time) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, b := range bars {
		payload, e := json.Marshal(b)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO bars VALUES(?,?,?,?)", symbol, period, b.Time.Format(time.RFC3339), payload); err != nil {
			return err
		}
	}
	// Bound each series and expire inactive symbols. All writes remain on the external volume.
	if _, err = tx.ExecContext(ctx, "DELETE FROM bars WHERE symbol=? AND period=? AND time NOT IN (SELECT time FROM bars WHERE symbol=? AND period=? ORDER BY time DESC LIMIT 8000)", symbol, period, symbol, period); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO snapshots VALUES(?,?,?)", symbol, period, now.Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM bars WHERE EXISTS(SELECT 1 FROM snapshots s WHERE s.symbol=bars.symbol AND s.period=bars.period AND s.fetched<?)", now.AddDate(0, 0, -30).Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM snapshots WHERE fetched<?", now.AddDate(0, 0, -30).Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM finance WHERE fetched<?", now.AddDate(0, 0, -30).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
