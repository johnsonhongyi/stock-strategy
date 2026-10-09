package sbc

import "math"

type channel struct {
	mid, upper, lower, pos, kx                                           []float64
	slope, deg, supp, suppSlope, suppDeg, suppPos, anchorHigh, anchorLow float64
	tc, bc, nod, dir, suppDays                                           int
	upperDays, suppDaysPrices                                            []float64
}
type rawChannel struct {
	mid, up, low []float64
	slope        float64
	tc, bc, nod  int
}

func rawChannelFor(h, l, c []float64, tc, bc int) rawChannel {
	n := len(c)
	nod := absInt(tc - bc)
	if nod < 2 {
		nod = max(tc, bc, 5)
	}
	anchor := min(tc, bc)
	end := n - anchor
	slope, last := regression(c[max(0, end-nod) : end+1])
	mid := make([]float64, n)
	for i := range mid {
		mid[i] = last - slope*float64(n-i-anchor)
	}
	from, to := max(0, n-max(tc, bc)), min(n-1, n-min(tc, bc))
	up, down := 0., 0.
	for i := from; i <= to; i++ {
		up = math.Max(up, h[i]-mid[i])
		down = math.Max(down, mid[i]-l[i])
	}
	upper, lower := make([]float64, n), make([]float64, n)
	for i := range mid {
		upper[i] = mid[i] + up
		lower[i] = mid[i] - down
	}
	return rawChannel{mid, upper, lower, slope, tc, bc, nod}
}
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
func validChannel(r rawChannel) bool {
	n := len(r.up)
	return r.up[n-1] > .01 && r.up[n-1]-r.low[n-1] > .01
}

func calculateChannel(h, l, c []float64) channel {
	n := len(c)
	hh, ll := rolling(h, min(n, 36), "max", false), rolling(l, min(n, 36), "min", false)
	tc, bc := 1, 1
	for i := range c {
		if h[i] == hh[i] {
			tc = n - i
		}
		if l[i] == ll[i] {
			bc = n - i
		}
	}
	macro := sign(float64(bc - tc))
	raw := rawChannelFor(h, l, c, tc, bc)
	chosen := false
	if macro == -1 {
		from, to := n-max(tc, bc), n-min(tc, bc)
		hp, lp := h[n-tc], l[n-bc]
		if to > from {
			hp, lp = h[from], l[to]
		}
		drop := (hp - lp) / math.Max(hp, .0001)
		if drop >= .25 && absInt(tc-bc) >= 8 && float64(bc) <= float64(absInt(tc-bc))*2.1 && minimum(raw.mid[from:to+1]) > .05 && c[n-1] < hp*.88 {
			chosen = true
		}
		if !chosen && c[n-1] >= h[from]*.9 && min(tc, bc) > 3 && n > min(tc, bc) && bc < tc {
			candidateTC := max(1, bc-argmax(h[n-bc:]))
			candidate := rawChannelFor(h, l, c, candidateTC, bc)
			if validChannel(candidate) && candidate.slope > 1e-6 {
				raw = candidate
				tc = candidateTC
				chosen = true
			}
		}
	}
	if !chosen && validChannel(raw) {
		chosen = true
	}
	fallbackChannel := !chosen
	slope := raw.slope
	nod := raw.nod
	if fallbackChannel {
		window := min(n, max(15, min(30, n/2)))
		slice := c[n-window:]
		local, last := regression(slice)
		var variance float64
		mean := avg(slice)
		for _, v := range slice {
			variance += (v - mean) * (v - mean)
		}
		std := math.Sqrt(variance / float64(len(slice)))
		band := math.Max(std*2, c[n-1]*.06)
		for i := range c {
			raw.mid[i] = math.Max(c[n-1]*.5, last-local*float64(n-i-1))
			raw.up[i] = raw.mid[i] + band
			raw.low[i] = math.Max(.01, raw.mid[i]-band)
		}
		if !(macro == -1 && slope < 0) {
			slope = local
		}
		nod = absInt(tc - bc)
		if nod < 2 {
			nod = 5
		}
	} else {
		tc, bc = raw.tc, raw.bc
	}
	limitMin := minimum(l[max(0, n-100):]) * .9
	limitMax := maximum(h[max(0, n-100):]) * 1.1
	start := n - max(tc, bc)
	mid, upper, lower := filled(n, math.NaN()), filled(n, math.NaN()), filled(n, math.NaN())
	for i := start; i < n; i++ {
		core := n-i >= min(tc, bc)
		if fallbackChannel || core || (raw.mid[i] >= limitMin && raw.mid[i] <= limitMax) {
			mid[i] = raw.mid[i]
		}
		if fallbackChannel || core || (raw.up[i] >= limitMin && raw.up[i] <= limitMax) {
			upper[i] = raw.up[i]
		}
		if fallbackChannel || core || (raw.low[i] >= limitMin && raw.low[i] <= limitMax) {
			lower[i] = raw.low[i]
		}
	}
	nominal := 0.
	for i := range c {
		nominal = math.Max(nominal, raw.up[i]-raw.low[i])
	}
	if nominal <= .01 {
		nominal = c[n-1] * .08
	}
	pos := filled(n, math.NaN())
	for i := start; i < n; i++ {
		if finite(lower[i]) {
			width := upper[i] - lower[i]
			if !finite(width) || width <= 1e-6 {
				width = nominal
			}
			pos[i] = (c[i] - lower[i]) / width * 100
		}
	}
	dir := 0
	if slope > 1e-6 {
		dir = 1
	} else if slope < -1e-6 {
		dir = -1
	}
	if fallbackChannel && macro == -1 {
		dir = -1
	}
	if !((dir == 1 && slope > 0) || (dir == -1 && slope < 0)) {
		slope = math.Abs(slope)
		if dir == -1 {
			slope = -slope
		}
	}
	out := channel{mid: mid, upper: upper, lower: lower, pos: pos, kx: filled(n, math.NaN()), slope: slope, deg: angle(slope / math.Max(.1, c[n-1]) * 100), tc: tc, bc: bc, nod: nod, dir: dir, anchorHigh: h[n-tc], anchorLow: l[n-bc]}
	// KX DRAWLINE pairs and historical segments, in SBC's overwrite order.
	ll20, hh20, ll4 := rolling(l, 20, "min", false), rolling(h, 20, "max", false), rolling(l, 4, "min", false)
	type pair struct{ a, b int }
	pairs := []pair{}
	a, b := -1, -1
	for i := range c {
		if l[i] <= ll20[i] {
			if a >= 0 && b > a {
				pairs = append(pairs, pair{a, b})
				b = -1
			}
			a = i
		} else if h[i] >= hh20[i] && a >= 0 {
			b = i
		}
	}
	if a >= 0 && b > a {
		pairs = append(pairs, pair{a, b})
	}
	out.suppDays = bc
	out.suppSlope = (c[n-1] - out.anchorLow) / float64(max(1, bc))
	out.supp = out.anchorLow + out.suppSlope*float64(bc)
	if len(pairs) > 0 {
		last := pairs[len(pairs)-1]
		out.suppSlope = (ll4[last.b] - l[last.a]) / float64(last.b-last.a)
		out.supp = l[last.a] + out.suppSlope*float64(n-1-last.a)
		out.suppDays = n - 1 - last.a
		draw := func(p pair, end int) {
			slope := (ll4[p.b] - l[p.a]) / float64(p.b-p.a)
			indices := []int{}
			for i := p.a; i < end; i++ {
				v := l[p.a] + slope*float64(i-p.a)
				if v >= limitMin && v <= limitMax {
					indices = append(indices, i)
				}
			}
			if len(indices) > 1 {
				for _, i := range indices {
					out.kx[i] = l[p.a] + slope*float64(i-p.a)
				}
			}
		}
		draw(last, n)
		for _, p := range pairs[:len(pairs)-1] {
			if p.a >= start {
				draw(p, min(n, p.b+5))
			}
		}
	}
	out.supp = math.Max(.01, out.supp)
	out.suppDeg = angle(out.suppSlope / c[n-1] * 100)
	out.suppPos = (c[n-1] - out.supp) / out.supp * 100
	for day := 1; day <= 9; day++ {
		up, supp := out.anchorHigh, out.supp-out.suppSlope*float64(day-1)
		if n >= day && finite(upper[n-day]) {
			up = upper[n-day]
		}
		if n >= day && finite(out.kx[n-day]) {
			supp = out.kx[n-day]
		}
		out.upperDays = append(out.upperDays, pyRound(up, 3))
		out.suppDaysPrices = append(out.suppDaysPrices, pyRound(math.Max(.01, supp), 3))
	}
	return out
}
