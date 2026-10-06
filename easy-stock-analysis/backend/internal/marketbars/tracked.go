package marketbars

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

type StockDataSummary struct {
	Market        string  `json:"market"`
	Symbol        string  `json:"symbol"`
	Name          string  `json:"name"`
	Tracked       bool    `json:"tracked"`
	TrackingSource string `json:"tracking_source,omitempty"`
	BarCount      int     `json:"bar_count"`
	FirstDate     string  `json:"first_date,omitempty"`
	LatestDate    string  `json:"latest_date,omitempty"`
	LatestClose   float64 `json:"latest_close,omitempty"`
	LatestSource  string  `json:"latest_source,omitempty"`
}

func (s *Store) ListStocks(ctx context.Context, requestedMarket string) ([]StockDataSummary, error) {
	marketFilter := strings.ToUpper(strings.TrimSpace(requestedMarket))
	if marketFilter != "" && marketFilter != "ALL" {
		_, marketFilter = NormalizeSymbol("", marketFilter)
	}
	query := `SELECT bars.market,bars.code,bars.bar_count,bars.first_date,bars.latest_date,
		COALESCE((SELECT latest.close FROM daily_bars latest WHERE UPPER(latest.code)=bars.code AND UPPER(COALESCE(NULLIF(latest.market,''),'CN'))=bars.market AND latest.date=bars.latest_date ORDER BY latest.rowid DESC LIMIT 1),0),
		COALESCE((SELECT latest.source FROM daily_bars latest WHERE UPPER(latest.code)=bars.code AND UPPER(COALESCE(NULLIF(latest.market,''),'CN'))=bars.market AND latest.date=bars.latest_date ORDER BY latest.rowid DESC LIMIT 1),'')
		FROM (SELECT UPPER(COALESCE(NULLIF(market,''),'CN')) AS market,UPPER(code) AS code,COUNT(*) AS bar_count,MIN(date) AS first_date,MAX(date) AS latest_date
		FROM daily_bars WHERE open>0 AND high>=low AND low>0 AND close>0 AND high>=open AND high>=close AND low<=open AND low<=close GROUP BY 1,2) bars`
	args := []any{}
	if marketFilter != "" && marketFilter != "ALL" {
		query += " WHERE bars.market=?"
		args = append(args, marketFilter)
	}
	query += " ORDER BY bars.market,bars.code"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	items := make(map[string]*StockDataSummary)
	for rows.Next() {
		item := &StockDataSummary{}
		if err := rows.Scan(&item.Market, &item.Symbol, &item.BarCount, &item.FirstDate, &item.LatestDate, &item.LatestClose, &item.LatestSource); err != nil {
			rows.Close()
			return nil, err
		}
		item.Name = item.Symbol
		items[item.Market+"\x00"+item.Symbol] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	trackingQuery := "SELECT market,code,name,enabled,source FROM stock_tracking"
	trackingArgs := []any{}
	if marketFilter != "" && marketFilter != "ALL" {
		trackingQuery += " WHERE market=?"
		trackingArgs = append(trackingArgs, marketFilter)
	}
	trackingQuery += " ORDER BY market,code"
	trackedRows, err := s.db.QueryContext(ctx, trackingQuery, trackingArgs...)
	if err != nil {
		return nil, err
	}
	defer trackedRows.Close()
	for trackedRows.Next() {
		var market, code, name, source string
		var enabled int
		if err := trackedRows.Scan(&market, &code, &name, &enabled, &source); err != nil {
			return nil, err
		}
		key := strings.ToUpper(market) + "\x00" + strings.ToUpper(code)
		item := items[key]
		if item == nil {
			if enabled == 0 {
				continue
			}
			item = &StockDataSummary{Market: strings.ToUpper(market), Symbol: strings.ToUpper(code)}
			items[key] = item
		}
		if strings.TrimSpace(name) != "" {
			item.Name = strings.TrimSpace(name)
		}
		item.Tracked = enabled != 0
		item.TrackingSource = source
	}
	if err := trackedRows.Err(); err != nil {
		return nil, err
	}

	result := make([]StockDataSummary, 0, len(items))
	for _, item := range items {
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Market == result[j].Market {
			return result[i].Symbol < result[j].Symbol
		}
		return result[i].Market < result[j].Market
	})
	return result, nil
}

func (s *Store) SetTrackedStock(ctx context.Context, market, symbol, name string, enabled bool, source string) error {
	code, normalizedMarket := NormalizeSymbol(symbol, market)
	if code == "" || normalizedMarket == "" || normalizedMarket == "AUTO" {
		return sql.ErrNoRows
	}
	flag := 0
	if enabled {
		flag = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO stock_tracking(market,code,name,enabled,source,updated_at)
		VALUES(?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(market,code) DO UPDATE SET
		name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE stock_tracking.name END,
		enabled=excluded.enabled,source=excluded.source,updated_at=CURRENT_TIMESTAMP`,
		normalizedMarket, code, strings.TrimSpace(name), flag, strings.TrimSpace(source))
	return err
}

func (s *Store) ClearDaily(ctx context.Context, market, symbol string) (int64, error) {
	code, normalizedMarket := NormalizeSymbol(symbol, market)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM daily_bars WHERE UPPER(code)=? AND UPPER(COALESCE(NULLIF(market,''),'CN'))=?", code, normalizedMarket)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM meta WHERE key IN (?,?)", "seeded_"+code, "useeded_"+code); err != nil {
		return 0, err
	}
	return deleted, tx.Commit()
}
