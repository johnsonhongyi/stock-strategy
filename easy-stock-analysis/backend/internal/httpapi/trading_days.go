package httpapi

import (
	"easy-stock/backend/internal/foundation"
	"sort"
	"strconv"
)

// ATS groups from the oldest trading bar, retaining the final partial group.
func aggregateTradingDays(lines []foundation.KLine, days int) []foundation.KLine {
	sorted := append([]foundation.KLine(nil), lines...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })
	result := make([]foundation.KLine, 0)
	for i, line := range sorted {
		if i%days == 0 {
			line.Meta.Source += ":derived-" + strconv.Itoa(days) + "d"
			result = append(result, line)
			continue
		}
		current := &result[len(result)-1]
		current.High = max(current.High, line.High)
		current.Low = min(current.Low, line.Low)
		current.Close, current.Time = line.Close, line.Time
		current.Volume += line.Volume
		current.Amount += line.Amount
		current.TurnoverRate += line.TurnoverRate
		current.Meta.Stale = current.Meta.Stale || line.Meta.Stale
		current.Meta.TradeDate = line.Meta.TradeDate
		if line.Meta.FetchedAt.After(current.Meta.FetchedAt) {
			current.Meta.FetchedAt = line.Meta.FetchedAt
		}
		if line.Meta.Stale {
			current.Meta.Source += ";" + line.Meta.Source
		}
	}
	for i := range result {
		if i > 0 {
			result[i].PreviousClose = result[i-1].Close
		}
		if result[i].PreviousClose > 0 {
			result[i].ChangePercent = (result[i].Close/result[i].PreviousClose - 1) * 100
		}
	}
	return result
}
