package sbc

import (
	"fmt"
	"math"
)

func indicators(o, h, l, c, v []float64) []map[string]any {
	n := len(c)
	ch := calculateChannel(h, l, c)
	fibH := fallback(rolling(h, 8, "max", true), h)
	fibL := fallback(rolling(l, 8, "min", true), l)
	a0 := make([]float64, n)
	for i := range c {
		a0[i] = (l[i] + h[i] + c[i]*2) / 4
	}
	aa := fallback(ema(a0, 14), a0)
	a1 := make([]float64, n)
	for i := 1; i < n; i++ {
		if math.Abs(aa[i-1]) > 1e-8 {
			a1[i] = (aa[i] - aa[i-1]) / aa[i-1] * 100
		}
	}
	crossZero := cross(a1, filled(n, 0))
	e12, e26 := fallback(ema(c, 12), c), fallback(ema(c, 26), c)
	diff := make([]float64, n)
	for i := range c {
		diff[i] = e12[i] - e26[i]
	}
	dea := fallback(ema(diff, 9), diff)
	sk13, sk73 := fallback(ema(a0, 13), a0), fallback(ema(a0, 73), a0)
	sk := make([]float64, n)
	for i := range c {
		sk[i] = sk13[i] - sk73[i]
	}
	sd := fallback(ema(sk, 2), sk)
	crossSK := cross(sk, sd)
	vm := fallback(rolling(v, 5, "mean", true), v)
	rsi6 := rsi(c, 6)
	escape := cross(filled(n, 84), rsi6)
	ll27, hh27 := fallback(rolling(l, 27, "min", true), l), fallback(rolling(h, 27, "max", true), h)
	stoch := make([]float64, n)
	for i := range c {
		stoch[i] = (c[i] - ll27[i]) / math.Max(hh27[i]-ll27[i], 1e-8) * 100
	}
	sma1 := ema(stoch, 5)
	sma2 := ema(sma1, 3)
	sma1 = fallback(sma1, stoch)
	sma2 = fallback(sma2, sma1)
	var52 := make([]float64, n)
	for i := range c {
		var52[i] = 3*sma1[i] - 2*sma2[i]
	}
	startSignal := cross(var52, filled(n, 3))
	e5, e13, e21 := fallback(ema(c, 5), c), fallback(ema(c, 13), c), fallback(ema(c, 21), c)
	ma3 := fallback(rolling(c, 3, "mean", true), c)
	ma5, ma10, ma20 := rolling(c, 5, "mean", true), rolling(c, 10, "mean", true), rolling(c, 20, "mean", true)
	points := make([]map[string]any, n)
	lastCross, buy, sell := -1, 0, 0
	for i := range c {
		if crossZero[i] {
			lastCross = i
		}
		anchor := max(0, lastCross)
		fh, fl := maximum(h[:i+1]), minimum(l[:i+1])
		if i >= 3 {
			fh, fl = fibH[i-3], fibL[i-3]
		}
		fibRange := math.Max(fh-fl, 1e-6)
		pct, vr := 0., 1.
		if i > 0 {
			pct = (c[i] - c[i-1]) / c[i-1]
		}
		if vm[i] > 0 {
			vr = v[i] / vm[i]
		}
		launch := crossSK[i] && ((sk[i] < -.04 && pct >= .03) || sk[i] <= -.14 || (sk[i] <= .05 && (vr > 2 || pct > .035)))
		if i >= 4 {
			if c[i] < c[i-4] {
				buy++
				if buy > 9 {
					buy = 1
				}
				sell = 0
			} else if c[i] > c[i-4] {
				sell++
				if sell > 9 {
					sell = 1
				}
				buy = 0
			} else {
				buy, sell = 0, 0
			}
		}
		midFor := ch.mid[i]
		if math.IsNaN(midFor) && finite(ch.upper[i]) && finite(ch.lower[i]) {
			midFor = (ch.upper[i] + ch.lower[i]) / 2
		}
		safeMid := 1.
		if math.Abs(midFor) > 1e-8 {
			safeMid = midFor
		}
		width := ch.upper[i] - ch.lower[i]
		resSlope := (ch.anchorHigh - c[n-1]) / float64(max(1, ch.tc))
		pattern := -1
		if ch.tc < ch.bc || ch.slope > 1e-6 {
			pattern = 1
		}
		trend := (e5[i] + e13[i] + e21[i]) / 3
		p := map[string]any{
			"open": o[i], "high": h[i], "low": l[i], "close": c[i],
			"ma5": ma5[i], "ma10": ma10[i], "ma20": ma20[i],
			"ch_anchor_high_price": ch.anchorHigh, "ch_anchor_low_price": ch.anchorLow,
			"ch_tc2": ch.tc, "ch_bc2": ch.bc, "ch_nod": ch.nod, "ch_pattern": pattern,
			"fib_high": fh, "fib_low": fl, "fib_19": fh - fibRange*.191, "fib_38": fh - fibRange*.382, "fib_50": fh - fibRange*.5, "fib_61": fh - fibRange*.618, "fib_80": fh - fibRange*.809,
			"sig_bottom": flag(diff[i] < -.1 && diff[i] > dea[i]), "sig_top": flag(h[i] >= a0[anchor]*1.3), "sig_launch": flag(launch), "sig_escape": flag(escape[i]), "sig_start": flag(startSignal[i]),
			"ch_upper": ch.upper[i], "ch_mid": ch.mid[i], "ch_lower": ch.lower[i], "chan_kx": ch.kx[i],
			"ch_slope": rounded(ch.slope, 6), "ch_slope_deg": rounded(ch.deg, 2), "ch_width": rounded(width, 3), "ch_height": rounded(width, 3),
			"ch_height_pct": rounded(width/safeMid*100, 2), "ch_width_pct": rounded(width/c[i]*100, 2), "upper_height": ch.upper[i] - midFor, "lower_height": midFor - ch.lower[i], "ch_pos": rounded(ch.pos[i], 2), "ch_dir": ch.dir,
			"ch_supp_price": rounded(ch.supp, 3), "ch_supp_slope": rounded(ch.suppSlope, 4), "ch_supp_slope_deg": rounded(ch.suppDeg, 2), "ch_supp_pos": rounded(ch.suppPos, 2), "ch_supp_days": ch.suppDays,
			"ch_res_price": rounded(ch.anchorHigh, 3), "ch_res_slope": rounded(resSlope, 4), "ch_res_slope_deg": rounded(angle(resSlope/c[n-1]*100), 2),
			"ch_supp": rounded(ch.supp, 3), "ch_supplast": rounded(ch.supp, 3), "reversal_line": rounded(math.Min(ma3[i], trend), 3),
			"td_buy_count": buy, "td_sell_count": sell, "td_buy_signal": buy == 9, "td_sell_signal": sell == 9,
		}
		for d := 1; d <= 9; d++ {
			p[fmt.Sprintf("ch_upper%d", d)] = ch.upperDays[d-1]
			p[fmt.Sprintf("ch_supp%d", d)] = ch.suppDaysPrices[d-1]
			p[fmt.Sprintf("ch_supp_price%d", d)] = ch.suppDaysPrices[d-1]
		}
		for key, value := range p {
			if number, ok := value.(float64); ok && !finite(number) {
				p[key] = nil
			}
		}
		points[i] = p
	}
	return points
}
