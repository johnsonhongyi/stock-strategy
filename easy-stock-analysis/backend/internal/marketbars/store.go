package marketbars

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"easy-stock/backend/internal/foundation"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type marketContextKey struct{}

func WithMarket(ctx context.Context, market string) context.Context {
	return context.WithValue(ctx, marketContextKey{}, strings.TrimSpace(market))
}

func MarketFromContext(ctx context.Context) string {
	market, _ := ctx.Value(marketContextKey{}).(string)
	return market
}

func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("market data database path is empty")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create market data directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open market data database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=10000; PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure market data database: %w", err)
	}
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS daily_bars (code TEXT, date TEXT, open REAL, high REAL, low REAL, close REAL, prev_close REAL, volume REAL, amount REAL, source TEXT, market TEXT DEFAULT 'CN', PRIMARY KEY(code, date))",
		"CREATE INDEX IF NOT EXISTS idx_bars_code ON daily_bars(code)",
		"CREATE INDEX IF NOT EXISTS idx_bars_market_code_date ON daily_bars(UPPER(code), UPPER(COALESCE(NULLIF(market,''),'CN')), date DESC)",
		"CREATE TABLE IF NOT EXISTS stock_tracking (market TEXT NOT NULL, code TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, source TEXT NOT NULL DEFAULT 'manual', updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(market,code))",
		"CREATE INDEX IF NOT EXISTS idx_stock_tracking_enabled ON stock_tracking(market,enabled,code)",
		"CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)",
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize market data schema: %w", err)
		}
	}
	columns, err := db.Query("PRAGMA table_info(daily_bars)")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	hasMarket := false
	for columns.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := columns.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			columns.Close()
			_ = db.Close()
			return nil, err
		}
		if strings.EqualFold(name, "market") {
			hasMarket = true
		}
	}
	if err := columns.Err(); err != nil {
		columns.Close()
		_ = db.Close()
		return nil, err
	}
	columns.Close()
	if !hasMarket {
		if _, err := db.Exec("ALTER TABLE daily_bars ADD COLUMN market TEXT DEFAULT 'CN'"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("add market column to daily bars: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) ReadDaily(ctx context.Context, symbol string, requestedMarket string, limit int) ([]foundation.KLine, bool, error) {
	code, market, err := s.resolve(ctx, symbol, requestedMarket)
	if err != nil {
		return nil, false, err
	}
	if limit <= 0 {
		limit = 120
	}
	rows, err := s.db.QueryContext(ctx, "SELECT date,open,high,low,close,prev_close,volume,amount,source FROM daily_bars WHERE UPPER(code)=? AND UPPER(COALESCE(NULLIF(market,''),'CN'))=? ORDER BY date DESC LIMIT ?", code, market, limit)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var reversed []foundation.KLine
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	for rows.Next() {
		var date string
		var line foundation.KLine
		var source sql.NullString
		if err := rows.Scan(&date, &line.Open, &line.High, &line.Low, &line.Close, &line.PreviousClose, &line.Volume, &line.Amount, &source); err != nil {
			return nil, false, err
		}
		parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), location)
		if err != nil {
			parsed, err = time.Parse(time.RFC3339, strings.TrimSpace(date))
			if err != nil {
				return nil, false, fmt.Errorf("parse stored daily bar date %q: %w", date, err)
			}
		}
		line.Symbol = symbol
		line.Time = parsed
		sourceName := "local-bars.db"
		if source.Valid && strings.TrimSpace(source.String) != "" {
			sourceName += ":" + strings.TrimSpace(source.String)
		}
		line.Meta = foundation.SourceMeta{Source: sourceName}
		if line.PreviousClose > 0 {
			line.ChangePercent = (line.Close/line.PreviousClose - 1) * 100
		}
		reversed = append(reversed, line)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(reversed) == 0 {
		return nil, false, nil
	}
	lines := make([]foundation.KLine, len(reversed))
	for i := range reversed {
		lines[len(reversed)-1-i] = reversed[i]
	}
	return lines, true, nil
}

func (s *Store) UpsertDaily(ctx context.Context, symbol string, requestedMarket string, lines []foundation.KLine) error {
	code, market, err := s.resolve(ctx, symbol, requestedMarket)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, line := range lines {
		if line.Time.IsZero() {
			continue
		}
		date := line.Time.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02")
		source := strings.TrimSpace(line.Meta.Source)
		if source == "" {
			source = "upstream"
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO daily_bars(code,date,open,high,low,close,prev_close,volume,amount,source,market) VALUES(?,?,?,?,?,?,?,?,?,?,?)",
			code, date, line.Open, line.High, line.Low, line.Close, line.PreviousClose, line.Volume, line.Amount, source, market); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) resolve(ctx context.Context, symbol string, requestedMarket string) (string, string, error) {
	code, inferred := NormalizeSymbol(symbol, requestedMarket)
	if code == "" {
		return "", "", fmt.Errorf("invalid daily bar symbol %q", symbol)
	}
	if strings.TrimSpace(requestedMarket) != "" {
		return code, inferred, nil
	}
	if inferred != "" && inferred != "AUTO" {
		return code, inferred, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT UPPER(COALESCE(NULLIF(market,''),'CN')) FROM daily_bars WHERE UPPER(code)=? ORDER BY 1", code)
	if err != nil {
		return "", "", err
	}
	var found []string
	for rows.Next() {
		var market string
		if err := rows.Scan(&market); err != nil {
			rows.Close()
			return "", "", err
		}
		found = append(found, market)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", "", err
	}
	rows.Close()
	if len(found) == 1 {
		return code, found[0], nil
	}
	if len(found) > 1 {
		return code, "CN", nil
	}
	if inferred == "AUTO" {
		if isCryptoCode(code) {
			return code, "CRYPTO", nil
		}
		return code, "US", nil
	}
	return code, "CN", nil
}

func NormalizeSymbol(symbol string, requestedMarket string) (string, string) {
	value := strings.ToUpper(strings.TrimSpace(symbol))
	market := strings.ToUpper(strings.TrimSpace(requestedMarket))
	if market != "" {
		switch market {
		case "CN", "A", "A股", "SH", "SZ", "BJ":
			market = "CN"
		case "US", "USA", "NASDAQ", "NYSE":
			market = "US"
		case "CRYPTO", "COIN", "CRYPTOCURRENCY":
			market = "CRYPTO"
		}
	}
	for _, suffix := range []struct{ value, market string }{
		{".SH", "CN"}, {".SZ", "CN"}, {".BJ", "CN"},
		{".US", "US"}, {".NASDAQ", "US"}, {".NYSE", "US"},
		{".CRYPTO", "CRYPTO"},
	} {
		if strings.HasSuffix(value, suffix.value) {
			value = strings.TrimSuffix(value, suffix.value)
			if market == "" {
				market = suffix.market
			}
			break
		}
	}
	for _, suffix := range []string{"-USDT", "-USD", "/USD", "USD", "USDT"} {
		if strings.HasSuffix(value, suffix) && len(value) > len(suffix) {
			value = strings.TrimSuffix(value, suffix)
			if market == "" {
				market = "CRYPTO"
			}
			break
		}
	}
	if len(value) == 6 {
		if _, err := strconv.Atoi(value); err == nil && market == "" {
			market = "CN"
		}
	}
	if market == "" {
		market = "AUTO"
	}
	return value, market
}

func isCryptoCode(code string) bool {
	switch code {
	case "BTC", "ETH", "SOL", "XRP", "ADA", "DOGE", "LTC", "DOT", "AVAX", "LINK", "BCH", "XLM":
		return true
	default:
		return false
	}
}
