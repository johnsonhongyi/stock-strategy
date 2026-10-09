package httpapi

import (
	"easy-stock/backend/internal/foundation"
	"math"
	"sort"
	"strings"
	"time"
)

// Compute before viewport slicing, in chronological order. Unknown amount or
// volume units use an explicitly marked close/volume weighted estimate.
func intradayVWAP(input []foundation.KLine) []foundation.KLine {
	return intradayVWAPWindow(input, false)
}

func intradayVWAPWindow(input []foundation.KLine, continuous bool) []foundation.KLine {
	lines := append([]foundation.KLine(nil), input...)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time.Before(lines[j].Time) })
	var amount, volume float64
	var date string
	estimated := false
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	for i := range lines {
		line := &lines[i]
		day := line.Time.In(zone).Format("2006-01-02")
		if date == "" || (!continuous && day != date) {
			date = day
			amount = 0
			volume = 0
			estimated = false
		}
		line.VWAP = 0
		line.VWAPEstimated = false
		if line.Volume < 0 || math.IsNaN(line.Volume) || math.IsInf(line.Volume, 0) || line.Close <= 0 || math.IsNaN(line.Close) || math.IsInf(line.Close, 0) {
			continue
		}
		units := 1.0
		source := strings.ToLower(line.Meta.Source)
		known := strings.HasPrefix(source, "tdx") || strings.HasPrefix(source, "sina") || strings.HasPrefix(source, "eastmoney")
		if strings.HasPrefix(source, "eastmoney") {
			units = 100
		}
		shares := line.Volume * units
		traded := line.Amount
		if shares > 0 && (!known || traded <= 0 || math.IsNaN(traded) || math.IsInf(traded, 0)) {
			traded = line.Close * shares
			estimated = true
		}
		if shares > 0 {
			amount += traded
			volume += shares
		}
		if volume > 0 {
			line.VWAP = amount / volume
			line.VWAPEstimated = estimated
		}
	}
	return lines
}
