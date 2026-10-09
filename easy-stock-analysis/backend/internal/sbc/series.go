package sbc

import "math"

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func rounded(v float64, digits int) float64 {
	p := math.Pow10(digits)
	return math.RoundToEven(v*p) / p
}
func avg(a []float64) float64 {
	var s float64
	for _, v := range a {
		s += v
	}
	return s / float64(len(a))
}
func minimum(a []float64) float64 {
	v := a[0]
	for _, x := range a[1:] {
		v = math.Min(v, x)
	}
	return v
}
func maximum(a []float64) float64 {
	v := a[0]
	for _, x := range a[1:] {
		v = math.Max(v, x)
	}
	return v
}
func argmin(a []float64) int {
	k := 0
	for i := 1; i < len(a); i++ {
		if a[i] < a[k] {
			k = i
		}
	}
	return k
}
func argmax(a []float64) int {
	k := 0
	for i := 1; i < len(a); i++ {
		if a[i] > a[k] {
			k = i
		}
	}
	return k
}
func filled(n int, value float64) []float64 {
	a := make([]float64, n)
	for i := range a {
		a[i] = value
	}
	return a
}

// rolling uses min_periods=1 for Pandas-compatible strategy windows.
func rolling(a []float64, period int, op string, full bool) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		start := max(0, i-period+1)
		if full && i+1 < period {
			out[i] = math.NaN()
			continue
		}
		window := a[start : i+1]
		switch op {
		case "min":
			out[i] = minimum(window)
		case "max":
			out[i] = maximum(window)
		default:
			out[i] = avg(window)
		}
	}
	return out
}

// EMA follows TA-Lib's default SMA seed and leading-NaN lookback behavior.
// Fill fallback values only AFTER EMA chaining (SBC's stochastic oscillator).
func ema(a []float64, period int) []float64 {
	out := filled(len(a), math.NaN())
	first := 0
	for first < len(a) && math.IsNaN(a[first]) {
		first++
	}
	seed := first + period - 1
	if seed >= len(a) {
		return out
	}
	out[seed] = avg(a[first : seed+1])
	alpha := 2 / float64(period+1)
	for i := seed + 1; i < len(a); i++ {
		out[i] = (a[i]-out[i-1])*alpha + out[i-1]
	}
	return out
}
func fallback(a, values []float64) []float64 {
	for i := range a {
		if math.IsNaN(a[i]) {
			a[i] = values[i]
		}
	}
	return a
}
func cross(a, b []float64) []bool {
	out := make([]bool, len(a))
	for i := 1; i < len(a); i++ {
		out[i] = a[i-1] < b[i-1] && a[i] >= b[i]
	}
	return out
}
func flag(v bool) int {
	if v {
		return 1
	}
	return 0
}
func sign(v float64) int {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}
func angle(v float64) float64 { return math.Atan(v) * 180 / math.Pi }

func regression(a []float64) (slope, last float64) {
	n := len(a)
	if n < 2 {
		return 0, a[n-1]
	}
	xm := (float64(n) - 1) / 2
	mean := avg(a)
	var num float64
	for i, v := range a {
		num += (float64(i) - xm) * (v - mean)
	}
	slope = num / (float64(n) * float64(n*n-1) / 12)
	return slope, mean + slope*xm
}
func normalizedSlope(a []float64) float64 {
	if len(a) < 2 || avg(a) <= .0001 {
		return 0
	}
	slope, _ := regression(a)
	return angle(slope / avg(a) * 100 * 1.5)
}
func rsi(a []float64, period int) []float64 {
	out := filled(len(a), 50)
	if len(a) <= period {
		return out
	}
	var gain, loss float64
	for i := 1; i <= period; i++ {
		d := a[i] - a[i-1]
		gain += math.Max(d, 0)
		loss += math.Max(-d, 0)
	}
	gain /= float64(period)
	loss /= float64(period)
	for i := period; i < len(a); i++ {
		if i > period {
			d := a[i] - a[i-1]
			gain = (gain*float64(period-1) + math.Max(d, 0)) / float64(period)
			loss = (loss*float64(period-1) + math.Max(-d, 0)) / float64(period)
		}
		if gain+loss <= 1e-14 {
			out[i] = 0
		} else {
			out[i] = 100 * gain / (gain + loss)
		}
	}
	return out
}
