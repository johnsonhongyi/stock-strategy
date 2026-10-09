// Package sbc implements the ATS chart calculations in pure Go.
// No Python, CGO, market provider or trading runtime is required.
package sbc

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"easy-stock/backend/internal/foundation"
)

//go:embed manifest.json
var manifest json.RawMessage

var ErrInvalidInput = errors.New("invalid SBC input")

type Request struct {
	Symbol string             `json:"symbol"`
	Period string             `json:"period"`
	Bars   []foundation.KLine `json:"bars"`
}

func Validate(input Request) error {
	switch input.Period {
	case "day", "2d", "3d", "week", "month", "5", "15", "30", "60":
	default:
		return fmt.Errorf("%w: unsupported period", ErrInvalidInput)
	}
	if strings.TrimSpace(input.Symbol) == "" || len(input.Bars) < 10 || len(input.Bars) > 1000 {
		return fmt.Errorf("%w: symbol and 10 to 1000 bars required", ErrInvalidInput)
	}
	for i, b := range input.Bars {
		for _, v := range []float64{b.Open, b.High, b.Low, b.Close, b.Volume, b.Amount, b.PreviousClose, b.TurnoverRate, b.ChangePercent} {
			if !finite(v) {
				return fmt.Errorf("%w: non-finite bar %d", ErrInvalidInput, i)
			}
		}
		if b.Open <= 0 || b.Close <= 0 || b.Low <= 0 || b.High < b.Open || b.High < b.Close || b.High < b.Low || b.Low > b.Open || b.Low > b.Close || b.Volume < 0 || b.Time.IsZero() || (i > 0 && !b.Time.After(input.Bars[i-1].Time)) || (b.Symbol != "" && !sameSymbol(b.Symbol, input.Symbol)) {
			return fmt.Errorf("%w: invalid OHLC, volume, timestamp or symbol at bar %d", ErrInvalidInput, i)
		}
	}
	return nil
}

func sameSymbol(a, b string) bool {
	if strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) {
		return true
	}
	left, leftErr := foundation.NormalizeSymbol(a)
	right, rightErr := foundation.NormalizeSymbol(b)
	return leftErr == nil && rightErr == nil && left.Canonical == right.Canonical
}

// Analyze preserves the v1 JSON contract for existing HTTP/custom providers.
func Analyze(ctx context.Context, input []byte) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var request Request
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if err := Validate(request); err != nil {
		return nil, err
	}
	n := len(request.Bars)
	o, h, l, c, v := make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	for i, b := range request.Bars {
		o[i], h[i], l[i], c[i], v[i] = b.Open, b.High, b.Low, b.Close, b.Volume
	}
	points := indicators(o, h, l, c, v)
	for i, b := range request.Bars {
		points[i]["time"] = b.Time
	}
	var activity any
	if request.Period == "day" {
		days := []any{}
		amps := []float64{}
		for i := max(1, n-5); i < n; i++ {
			amp := pyRound((h[i]-l[i])/c[i-1]*100, 1)
			amps = append(amps, amp)
			days = append(days, []any{request.Bars[i].Time.Format("2006-01-02"), amp})
		}
		mean, peak := pyRound(avg(amps), 1), maximum(amps)
		rating := "低位沉寂"
		if mean >= 7 || peak >= 10 {
			rating = "极高活跃"
		} else if mean >= 4.5 {
			rating = "高度活跃"
		} else if mean >= 2.5 {
			rating = "温和波动"
		}
		activity = map[string]any{"avg_amp": mean, "max_amp": peak, "rating": rating, "days": days}
	}
	levels := []any{}
	low, high := minimum(l), maximum(h)
	for _, ratio := range []float64{.191, .382, .5, .618, .809} {
		levels = append(levels, map[string]float64{"ratio": ratio, "price": low + (high-low)*ratio})
	}
	strategy := evaluateStrategy(o, h, l, c, v)
	last := request.Bars[n-1]
	date := last.Meta.TradeDate
	if date == "" {
		date = last.Time.Format("2006-01-02")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"schema_version": 1, "source": "ats-sbc", "implementation": "go-native", "symbol": request.Symbol, "period": request.Period, "as_of": last.Time, "trade_date": date, "market_meta": last.Meta, "points": points, "activity": activity, "levels": levels, "strategy": strategy, "engine_revision": manifest})
}
