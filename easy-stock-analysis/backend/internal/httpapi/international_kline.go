package httpapi

import (
	"context"
	"easy-stock/backend/internal/foundation"
	"easy-stock/backend/internal/marketbars"
	"easy-stock/backend/internal/runtimelog"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// International daily sources share the same market cache and proxy transport.
// Intraday routing is deliberately left to the established provider interfaces.
type internationalKLineProvider struct{ client *http.Client }

func (p *internationalKLineProvider) get(ctx context.Context, target string, result any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s HTTP %d: %s", req.URL.Hostname(), resp.StatusCode, runtimelog.Redact(strings.TrimSpace(string(body))))
	}
	if resp.Header.Get("X-Stock-Cache") == "STALE" {
		return fmt.Errorf("%s 仅返回陈旧缓存", req.URL.Hostname())
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(result); err != nil {
		return fmt.Errorf("%s 响应解析失败: %w", req.URL.Hostname(), err)
	}
	return nil
}
func (p *internationalKLineProvider) KLine(ctx context.Context, symbol, period string, limit int) ([]foundation.KLine, error) {
	if normalizeKLineAggregationPeriod(period) != "day" {
		return nil, fmt.Errorf("该市场网络行情目前仅支持日线")
	}
	code, market := marketbars.NormalizeSymbol(symbol, marketbars.MarketFromContext(ctx))
	var lines []foundation.KLine
	var err error
	if market == "CRYPTO" {
		lines, err = p.crypto(ctx, code, limit)
		if err != nil {
			primaryErr := err
			lines, err = p.kraken(ctx, code)
			if err != nil {
				err = fmt.Errorf("%v; Kraken: %w", primaryErr, err)
			}
		}
	} else if market == "US" {
		lines, err = p.us(ctx, code)
	} else {
		return nil, fmt.Errorf("不支持的海外市场 %s", market)
	}
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("%s 未返回日线", code)
	}
	for i := 1; i < len(lines); i++ {
		lines[i].PreviousClose = lines[i-1].Close
	}
	return lastKLines(lines, limit), nil
}
func (p *internationalKLineProvider) crypto(ctx context.Context, code string, limit int) ([]foundation.KLine, error) {
	var rows [][]json.RawMessage
	target := "https://data-api.binance.vision/api/v3/klines?interval=1d&symbol=" + url.QueryEscape(strings.TrimSuffix(code, "USDT")+"USDT") + "&limit=" + strconv.Itoa(min(max(limit+1, 2), 1000))
	if err := p.get(ctx, target, &rows); err != nil {
		return nil, fmt.Errorf("Binance %s: %w", code, err)
	}
	lines := make([]foundation.KLine, 0, len(rows))
	for _, row := range rows {
		if len(row) < 8 {
			return nil, fmt.Errorf("Binance %s 日线字段缺失", code)
		}
		var stamp int64
		if json.Unmarshal(row[0], &stamp) != nil {
			return nil, fmt.Errorf("Binance 日线时间无效")
		}
		values := make([]float64, 6)
		for i, index := range []int{1, 2, 3, 4, 5, 7} {
			var text string
			if json.Unmarshal(row[index], &text) != nil {
				return nil, fmt.Errorf("Binance 日线数值无效")
			}
			value, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return nil, fmt.Errorf("Binance 日线数值无效")
			}
			values[i] = value
		}
		line := foundation.KLine{Symbol: code, Time: time.UnixMilli(stamp).UTC(), Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4], Amount: values[5], Meta: foundation.SourceMeta{Source: "binance", FetchedAt: time.Now(), SourceURL: target}}
		if !validCompletedDailyKLine(line) {
			return nil, fmt.Errorf("Binance %s OHLC 无效", code)
		}
		lines = append(lines, line)
	}
	return lines, nil
}
func (p *internationalKLineProvider) us(ctx context.Context, code string) ([]foundation.KLine, error) {
	type quote struct{ Open, High, Low, Close, Volume []*float64 }
	var payload struct {
		Chart struct {
			Error  *struct{ Description string }
			Result []struct {
				Timestamp  []int64
				Indicators struct{ Quote []quote }
			}
		}
	}
	target := "https://query1.finance.yahoo.com/v8/finance/chart/" + url.PathEscape(strings.ReplaceAll(code, ".", "-")) + "?range=2y&interval=1d"
	if err := p.get(ctx, target, &payload); err != nil {
		return nil, fmt.Errorf("Yahoo %s: %w", code, err)
	}
	if payload.Chart.Error != nil {
		return nil, fmt.Errorf("Yahoo %s: %s", code, payload.Chart.Error.Description)
	}
	if len(payload.Chart.Result) == 0 || len(payload.Chart.Result[0].Indicators.Quote) == 0 {
		return nil, fmt.Errorf("Yahoo %s 未返回日线", code)
	}
	result := payload.Chart.Result[0]
	q := result.Indicators.Quote[0]
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, err
	}
	lines := make([]foundation.KLine, 0, len(result.Timestamp))
	for i, stamp := range result.Timestamp {
		if i >= len(q.Open) || i >= len(q.High) || i >= len(q.Low) || i >= len(q.Close) || i >= len(q.Volume) || q.Open[i] == nil || q.High[i] == nil || q.Low[i] == nil || q.Close[i] == nil || q.Volume[i] == nil {
			continue
		}
		day := time.Unix(stamp, 0).In(location)
		// Store the exchange date without a timezone-dependent date shift.
		day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
		line := foundation.KLine{Symbol: code, Time: day, Open: *q.Open[i], High: *q.High[i], Low: *q.Low[i], Close: *q.Close[i], Volume: *q.Volume[i], Amount: *q.Close[i] * *q.Volume[i], Meta: foundation.SourceMeta{Source: "yahoo", FetchedAt: time.Now(), SourceURL: target}}
		if !validCompletedDailyKLine(line) {
			continue
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func (p *internationalKLineProvider) kraken(ctx context.Context, code string) ([]foundation.KLine, error) {
	pair := code + "USD"
	if code == "BTC" {
		pair = "XBTUSD"
	}
	if code == "DOGE" {
		pair = "XDGUSD"
	}
	var payload struct {
		Error  []string
		Result map[string]json.RawMessage
	}
	target := "https://api.kraken.com/0/public/OHLC?interval=1440&pair=" + url.QueryEscape(pair)
	if err := p.get(ctx, target, &payload); err != nil {
		return nil, err
	}
	if len(payload.Error) > 0 {
		return nil, fmt.Errorf("%s: %s", code, strings.Join(payload.Error, ", "))
	}
	for key, raw := range payload.Result {
		if key == "last" {
			continue
		}
		var rows [][]json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("Kraken 日线格式无效")
		}
		lines := make([]foundation.KLine, 0, len(rows))
		for _, row := range rows {
			if len(row) < 8 {
				return nil, fmt.Errorf("Kraken 日线字段缺失")
			}
			var stamp int64
			if json.Unmarshal(row[0], &stamp) != nil {
				return nil, fmt.Errorf("Kraken 日线时间无效")
			}
			values := make([]float64, 6)
			for i, index := range []int{1, 2, 3, 4, 5, 6} {
				var text string
				if json.Unmarshal(row[index], &text) != nil {
					return nil, fmt.Errorf("Kraken 日线数值无效")
				}
				value, err := strconv.ParseFloat(text, 64)
				if err != nil {
					return nil, fmt.Errorf("Kraken 日线数值无效")
				}
				values[i] = value
			}
			line := foundation.KLine{Symbol: code, Time: time.Unix(stamp, 0).UTC(), Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[5], Amount: values[4] * values[5], Meta: foundation.SourceMeta{Source: "kraken", FetchedAt: time.Now(), SourceURL: target}}
			if !validCompletedDailyKLine(line) {
				return nil, fmt.Errorf("Kraken %s OHLC 无效", code)
			}
			lines = append(lines, line)
		}
		return lines, nil
	}
	return nil, fmt.Errorf("Kraken %s 未返回日线", code)
}
