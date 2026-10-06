package marketbars

import (
	"context"
	"database/sql"
	"fmt"
)

func hasCompositeDailyBarKey(ordinals map[int]string) bool {
	if len(ordinals) != 3 {
		return false
	}
	seen := map[string]bool{}
	for _, name := range ordinals {
		seen[name] = true
	}
	return seen["market"] && seen["code"] && seen["date"]
}

func migrateDailyBarsMarketKey(db *sql.DB) error {
	ctx := context.Background()
	connection, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(ctx, "ROLLBACK")
		}
	}()

	rows, err := connection.QueryContext(ctx, "PRAGMA table_info(daily_bars)")
	if err != nil {
		return err
	}
	ordinals := make(map[int]string, 3)
	for rows.Next() {
		var cid, notNull, ordinal int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &ordinal); err != nil {
			rows.Close()
			return err
		}
		if ordinal > 0 {
			ordinals[ordinal] = name
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !hasCompositeDailyBarKey(ordinals) {
		statements := []string{
			`CREATE TABLE daily_bars_market_key_v2 (
				code TEXT NOT NULL, date TEXT NOT NULL, open REAL, high REAL, low REAL,
				close REAL, prev_close REAL, volume REAL, amount REAL, source TEXT,
				market TEXT NOT NULL DEFAULT 'CN', PRIMARY KEY(market,code,date))`,
			`INSERT OR REPLACE INTO daily_bars_market_key_v2
				(code,date,open,high,low,close,prev_close,volume,amount,source,market)
				SELECT UPPER(TRIM(COALESCE(code,''))),COALESCE(date,''),open,high,low,close,
				prev_close,volume,amount,source,UPPER(COALESCE(NULLIF(TRIM(market),''),'CN'))
				FROM daily_bars ORDER BY rowid`,
			"DROP TABLE daily_bars",
			"ALTER TABLE daily_bars_market_key_v2 RENAME TO daily_bars",
		}
		for _, statement := range statements {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("%s: %w", statement[:min(len(statement), 72)], err)
			}
		}
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
