package sbc

import (
	"fmt"
	"math"
	"strconv"
)

// Python round uses the binary value rather than NumPy's scaled rounding.
func pyRound(v float64, digits int) float64 {
	out, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', digits, 64), 64)
	return out
}

type classification struct {
	kind                           string
	slope, suppSlope, supp, anchor float64
	bullish                        bool
}

func classify(c, l []float64) classification {
	n := len(c)
	out := classification{kind: "horizontal"}
	if n < 15 {
		return out
	}
	global := normalizedSlope(c[max(0, n-60):])
	bcIndex := n - min(36, n) + argmin(l[n-min(36, n):])
	bc := n - bcIndex
	anchor, current := l[bcIndex], c[n-1]
	down := 0.
	if bcIndex >= 8 {
		down = normalizedSlope(c[:bcIndex+1])
	}
	suppAngle, supp := 0., anchor
	if bc > 1 && anchor > .0001 {
		rise := (current - anchor) / anchor * 100
		suppAngle = angle(rise / float64(bc) * 1.2)
		supp = anchor * (1 + rise/float64(bc)*float64(bc-1)/100*.75)
	}
	if n >= 20 {
		out.bullish = avg(c[n-5:]) >= avg(c[n-10:]) && current >= avg(c[n-20:])*.98
	}
	if down <= -3 || global <= -3 {
		out.kind = "descending"
	} else if (global >= 1.5 || suppAngle >= 2) && down > -2.5 {
		out.kind = "ascending"
	}
	effective := math.Max(global, suppAngle)
	if out.kind == "descending" {
		effective = down
	}
	out.slope, out.suppSlope, out.supp, out.anchor = pyRound(effective, 2), pyRound(suppAngle, 2), pyRound(supp, 2), pyRound(anchor, 2)
	return out
}
func defaultStrategy(kind string) map[string]any {
	cn, pattern := "上涨通道顺势", "未匹配上涨通道形态"
	if kind == "descending" {
		cn, pattern = "下降通道反转", "未匹配底部反转形态"
	}
	return map[string]any{"is_matched": false, "channel_type": kind, "channel_type_cn": cn, "pattern_name": pattern, "score": 0., "entry_price": 0., "stop_loss": 0., "target_price_1": 0., "target_price_2": 0., "channel_slope_deg": 0., "lowest_low": 0., "base_high": 0., "volume_shrink_pct": 0., "breakout_bar_idx": -1, "reason": ""}
}

func pioneer(c, h, l, v []float64) (indices []int, low, base float64) {
	n := len(c)
	if n < 15 {
		return
	}
	ma5, ma10, ma20 := rolling(c, 5, "mean", false), rolling(c, 10, "mean", false), rolling(c, 20, "mean", false)
	vm := rolling(v, 5, "mean", false)
	ll5, ll20, hh10 := rolling(l, 5, "min", false), rolling(l, 20, "min", false), rolling(h, 10, "max", false)
	last := -999
	for i := range c {
		nxMax := math.Max(ma5[i], math.Max(ma10[i], ma20[i]))
		nxMin := math.Min(ma5[i], math.Min(ma10[i], ma20[i]))
		prior := max(0, i-1)
		if (nxMax-nxMin)/math.Max(c[i], .0001) <= .05 && ll5[i] >= ll20[i]*.985 && v[i] >= vm[i]*1.3 && c[i] >= hh10[prior]*.985 && c[i] > nxMax && (c[i]-c[prior])/math.Max(c[prior], .0001) >= .028 && i-last > 5 {
			indices = append(indices, i)
			last = i
			low, base = ll20[i], hh10[prior]
		}
	}
	return
}

func evaluateStrategy(o, h, l, c, v []float64) map[string]any {
	n := len(c)
	if n < 15 {
		return map[string]any{"is_matched": false, "channel_type": "unknown", "channel_type_cn": "未知周期", "pattern_name": "数据不足", "score": 0., "reason": "K线数据过短，无法测算"}
	}
	cls := classify(c, l)
	if indices, low, base := pioneer(c, h, l, v); len(indices) > 0 {
		idx := indices[len(indices)-1]
		entry := c[idx]
		height := math.Max(base-low, entry*.05)
		cn := "下降通道反转"
		if cls.kind == "ascending" {
			cn = "上涨通道顺势"
		}
		return map[string]any{"is_matched": true, "channel_type": cls.kind, "channel_type_cn": cn, "pattern_name": "通道底部反转·逆势先锋突破", "score": 99., "entry_price": pyRound(entry, 2), "stop_loss": pyRound(low*.985, 2), "target_price_1": pyRound(entry+height*1.2, 2), "target_price_2": pyRound(entry+height*2, 2), "channel_slope_deg": cls.slope, "lowest_low": pyRound(low, 2), "base_high": pyRound(base, 2), "volume_shrink_pct": 0., "breakout_bar_idx": idx, "pioneer_sig_indices": indices, "reason": fmt.Sprintf("【对齐TDX逆势先锋】在%.2f首发大阳穿线放量起爆，支撑:%.2f，目标:%.2f", entry, low, pyRound(entry+height*1.2, 2))}
	}
	if cls.kind == "ascending" {
		return ascending(o, h, l, c, v, cls)
	}
	if cls.kind == "horizontal" {
		up := ascending(o, h, l, c, v, cls)
		if up["is_matched"].(bool) {
			return up
		}
	}
	return descending(o, h, l, c, v)
}

func ascending(o, h, l, c, v []float64, cls classification) map[string]any {
	res := defaultStrategy("ascending")
	n := len(c)
	fail := func(reason string) map[string]any { res["reason"] = reason; return res }
	if n < 25 {
		return fail("K线数据不足 25 根")
	}
	effective := math.Max(cls.slope, cls.suppSlope)
	res["channel_slope_deg"], res["lowest_low"] = pyRound(effective, 2), pyRound(cls.anchor, 2)
	if effective < 1.8 && !cls.bullish {
		return fail(fmt.Sprintf("通道向上斜率不足 (%.1f° < 1.8°)", effective))
	}
	recent := max(0, n-5)
	history := min(n, 40)
	localHigh := maximum(h[n-history : n-5])
	res["base_high"] = pyRound(localHigh, 2)
	meanVol := avg(v[n-history:])
	ratio := avg(v[recent:]) / math.Max(1e-6, meanVol)
	shrink := math.Max(0, (1-ratio)*100)
	res["volume_shrink_pct"] = pyRound(shrink, 1)
	current, low := c[n-1], minimum(l[recent:])
	priorLow := minimum(l[:n-5])
	if low < priorLow*.995 {
		return fail(fmt.Sprintf("最近低点跌破前期大底支撑 (%.2f < %.2f)", low, priorLow))
	}
	dist := (current - cls.supp) / math.Max(.0001, cls.supp)
	pullback := dist >= -.03 && dist <= .085 && (c[n-1] >= o[n-1] || c[n-1] >= c[n-2] || ratio <= .85)
	breakout := current >= localHigh*.998 || maximum(h[recent:]) >= localHigh*1.002
	if !pullback && !breakout {
		if current >= localHigh*.97 && low >= cls.anchor*1.02 {
			breakout = true
		} else {
			return fail(fmt.Sprintf("未处于回踩支撑位 (偏离:%+.1f%%) 亦未突破局部高点 (%.2f < %.2f)", dist*100, current, localHigh))
		}
	}
	first := n - 1
	if breakout {
		for i := recent; i < n; i++ {
			if c[i] >= localHigh*.99 || h[i] >= localHigh*1.002 {
				first = i
				break
			}
		}
	}
	entry := c[first]
	stop := pyRound(math.Max(cls.supp*.985, low*.988), 2)
	if stop >= entry {
		stop = pyRound(entry*.965, 2)
	}
	amp := math.Max(entry-stop, entry*.05)
	score := 65.
	pattern := "上涨通道中继突破"
	if pullback && !(breakout && v[n-1] > meanVol*1.3) {
		pattern = "上涨通道下轨回踩企稳"
		score += 15
		if ratio <= .85 {
			score += 10
		}
	} else {
		score += 18
		if v[n-1] > meanVol*1.2 {
			score += 10
		}
	}
	if cls.bullish {
		score += 10
	}
	if effective >= 10 {
		score += 6
	}
	res["is_matched"], res["pattern_name"], res["score"] = true, pattern, math.Min(99, math.Max(65, pyRound(score, 1)))
	res["entry_price"], res["stop_loss"], res["target_price_1"], res["target_price_2"], res["breakout_bar_idx"] = pyRound(entry, 2), stop, pyRound(entry+amp*1.2, 2), pyRound(entry+amp*2, 2), first
	res["reason"] = fmt.Sprintf("上涨通道(%+.1f°) 触发【%s】| 介入:%.2f | 支撑:%.2f | 前高:%.2f", effective, pattern, entry, cls.supp, localHigh)
	return res
}

func descending(o, h, l, c, v []float64) map[string]any {
	res := defaultStrategy("descending")
	n := len(c)
	fail := func(reason string) map[string]any { res["reason"] = reason; return res }
	if n < 30 {
		return fail("K线数据不足 30 根")
	}
	hh, ll := rolling(h, min(36, n), "max", false), rolling(l, min(36, n), "min", false)
	tc, bc := 0, n-1
	highs := []int{}
	for i := range c {
		if h[i] >= hh[i]-.0001 {
			tc = i
			highs = append(highs, i)
		}
		if l[i] <= ll[i]+.0001 {
			bc = i
		}
	}
	if bc >= n-5 {
		return fail("最近 5 根内仍在创波谷新低，未见底部企稳横盘")
	}
	start, end := min(tc, bc), max(tc, bc)
	if tc >= bc {
		start = max(0, bc-15)
		for _, i := range highs {
			if i < bc {
				start = i
			}
		}
	}
	if end-start < 4 {
		start = max(0, bc-15)
	}
	slope := normalizedSlope(c[start : end+1])
	res["channel_slope_deg"] = pyRound(slope, 2)
	if slope > -4 {
		return fail(fmt.Sprintf("通道主跌段下倾角度不足 (%.1f° > -4.0°)", slope))
	}
	hp, lp := h[start], l[end]
	ks := (lp - hp) / float64(max(1, end-start))
	upper, lower := false, false
	for i := start; i <= end; i++ {
		base := hp + ks*float64(i-start)
		upper = upper || h[i] >= base*(1-.035)
		lower = lower || l[i] <= (base-(hp-lp)*.4)*(1+.035)
	}
	if !upper && !lower {
		return fail("走势未充分在下降通道上下轨之间展开")
	}
	lowest := minimum(l[start : bc+1])
	res["lowest_low"] = pyRound(lowest, 2)
	baseEnd := n - 6
	if baseEnd-bc+1 < 3 {
		return fail("底部横盘震荡时间不足 3 根 K 线")
	}
	if minimum(l[bc:baseEnd+1]) < lowest*.998 {
		return fail("底部横盘期间跌破前期绝对波谷低点")
	}
	downVol, baseVol := avg(v[start:bc+1]), avg(v[bc:baseEnd+1])
	ratio := baseVol / math.Max(1e-6, downVol)
	shrink := math.Max(0, (1-ratio)*100)
	res["volume_shrink_pct"] = pyRound(shrink, 1)
	if ratio > .8 {
		return fail(fmt.Sprintf("底部未明显缩量 (量能比: %.2f > 0.80)", ratio))
	}
	baseHigh := maximum(h[bc : baseEnd+1])
	res["base_high"] = pyRound(baseHigh, 2)
	recent := n - 5
	low := minimum(l[recent:])
	if low < lowest*.998 {
		return fail(fmt.Sprintf("最近 5 根 K 线跌破底部最低点 (%.2f < %.2f)", low, lowest))
	}
	if !(maximum(c[recent:]) > baseHigh*.999 || maximum(h[recent:]) > baseHigh*1.002) {
		return fail(fmt.Sprintf("最近 5 根 K 线未有效突破底部整理高点 (%.2f <= %.2f)", maximum(h[recent:]), baseHigh))
	}
	first := n - 1
	for i := recent; i < n; i++ {
		if c[i] >= baseHigh*.99 || h[i] >= baseHigh*1.002 || (c[i] > o[i] && v[i] >= baseVol*1.2 && c[i] >= baseHigh*.98) {
			first = i
			break
		}
	}
	entry := c[first]
	stop := pyRound(lowest*.985, 2)
	height := math.Max(baseHigh-lowest, entry*.03)
	target := pyRound(math.Max(baseHigh+height, entry*1.05), 2)
	score := 65 + math.Min(20, math.Max(0, (1-ratio)*40))
	if v[n-1] > baseVol*1.3 {
		score += 10
	}
	lift := (low - lowest) / math.Max(.0001, lowest) * 100
	score += math.Min(10, math.Max(0, lift*3))
	pattern := "通道底部缩量右侧突破"
	if first < n-1 {
		pattern = "通道底部反转·逆势先锋突破"
		score += 5
	}
	res["is_matched"], res["pattern_name"], res["score"] = true, pattern, math.Min(99, math.Max(60, pyRound(score, 1)))
	res["entry_price"], res["stop_loss"], res["target_price_1"], res["target_price_2"], res["breakout_bar_idx"] = pyRound(entry, 2), stop, target, pyRound(math.Max(target*1.08, entry*1.12), 2), first
	res["reason"] = fmt.Sprintf("下降通道(%+.1f°)探底%.2f后缩量%.1f%%企稳，【逆势先锋】在%.2f首发突破%.2f", slope, lowest, shrink, entry, baseHigh)
	return res
}
