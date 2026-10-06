#!/usr/bin/env python3
"""P30 不及预期时间窗测试(2026-10-03,用户拍板):
持仓方向的反向段时长超原段 1.0 倍→预警,超 2.0 倍→出局(策略单自动平/镜像单只预警)。

1. test_replay_warn: BTC 小时数据回放,10-03 00:00 UTC ratio=1.0 → expect_warn
2. test_replay_dead: 同上,10-03 06:00 UTC ratio=2.0 → expect_dead
3. test_point_in_time: 传全量数据+now 回放,结果与截断数据一致(不偷看未来)
4. test_action_split: 行为分野(mirror_ 镜像单永不自动平)
5. test_long_side: 方向自适应(多单看回调/上涨段)
6. test_no_counter: 最新段仍是持仓方向 → 无信号
"""
import datetime
import os
import sys

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
import cycle_structure  # noqa: E402


def _btc_full():
    import bars_crypto
    return bars_crypto.intraday_hourly("BTC", n=120)


def test_replay_warn():
    hs = _btc_full()
    sub = [h for h in hs if h["time"][:16] < "2026-10-03 00:00"]
    r = cycle_structure.expect_check(sub, "short",
                                     now=datetime.datetime(2026, 10, 3, 0, 0))
    assert r["time_ratio"] is not None, "no ratio"
    assert abs(r["time_ratio"] - 1.0) < 0.06, r["time_ratio"]
    assert r["expect_warn"] is True, r
    assert r["expect_dead"] is False, r
    assert r["strategy_version"] == "v1.8", r["strategy_version"]
    o = r["orig_leg"]
    assert o["start"][:16] == "2026-10-02 12:00" and o["end"][:16] == "2026-10-02 18:00", o
    print("PASS replay_warn ratio=%.2f counter_h=%.1f" % (r["time_ratio"], r["counter_hours"]))


def test_replay_dead():
    hs = _btc_full()
    sub = [h for h in hs if h["time"][:16] < "2026-10-03 06:00"]
    r = cycle_structure.expect_check(sub, "short",
                                     now=datetime.datetime(2026, 10, 3, 6, 0))
    assert abs(r["time_ratio"] - 2.0) < 0.06, r["time_ratio"]
    assert r["expect_warn"] is True, r
    assert r["expect_dead"] is True, r
    print("PASS replay_dead ratio=%.2f counter_h=%.1f" % (r["time_ratio"], r["counter_hours"]))


def test_point_in_time():
    hs = _btc_full()  # 全量(含 06:00 之后的数据)
    now = datetime.datetime(2026, 10, 3, 6, 0)
    r_full = cycle_structure.expect_check(hs, "short", now=now)
    sub = [h for h in hs if h["time"][:16] < "2026-10-03 06:00"]
    r_sub = cycle_structure.expect_check(sub, "short", now=now)
    assert r_full["time_ratio"] == r_sub["time_ratio"], (r_full, r_sub)
    assert r_full["expect_dead"] is True
    print("PASS point_in_time ratio=%.2f (full==truncated)" % r_full["time_ratio"])


def test_action_split():
    sys.path.insert(0, SVC)
    import crypto_short_watch as w
    mirror = {"reason": "mirror_user_real_short@84013.8_10x_isolated"}
    strat = {"reason": "strategy_short_signal"}
    # 镜像单 ratio≥2.0:只预警,不自动平
    assert w.expect_action(mirror, 3.0, 1.0, 2.0) == "alert_only"
    assert w.expect_action(mirror, 1.5, 1.0, 2.0) == "alert_only"
    # 策略单 ratio≥2.0:自动平;1.0~2.0:只预警
    assert w.expect_action(strat, 2.5, 1.0, 2.0) == "close"
    assert w.expect_action(strat, 2.0, 1.0, 2.0) == "close"
    assert w.expect_action(strat, 1.5, 1.0, 2.0) == "alert_only"
    # 未达线 / 无数据:None
    assert w.expect_action(strat, 0.5, 1.0, 2.0) is None
    assert w.expect_action(strat, None, 1.0, 2.0) is None
    assert w.expect_action(mirror, 0.9, 1.0, 2.0) is None
    print("PASS action_split (mirror永不自动平)")


def _synth_long():
    """合成多单场景:上涨 7h(+8点) → 回调。bar:time/open/high/low/close。"""
    bars = []
    px = [100, 101, 102, 103, 104, 105, 106, 108,  # idx0-7 上涨,峰在7
          107, 106, 105, 104.5, 104, 103.5,        # idx8-13 回调,谷在13
          104, 104.2]                              # idx14-15 微反弹
    base = datetime.datetime(2026, 1, 1, 0, 0)
    for i, c in enumerate(px):
        t = (base + datetime.timedelta(hours=i)).strftime("%Y-%m-%d %H:%M")
        o = px[i - 1] if i else c
        bars.append({"time": t, "open": o, "high": max(o, c) + 0.1,
                     "low": min(o, c) - 0.1, "close": c,
                     "volume": 1.0, "amount": c})
    return bars


def test_long_side():
    bars = _synth_long()
    base = datetime.datetime(2026, 1, 1, 0, 0)
    # 回调起点=idx7(07:00);now=13:00 → 反向段 6h / 原段 7h ≈ 0.86 → 无预警
    r = cycle_structure.expect_check(bars, "long", now=base + datetime.timedelta(hours=13))
    assert r["orig_leg"]["dir"] == "up", r["orig_leg"]
    assert abs(r["time_ratio"] - 6 / 7) < 0.06, r["time_ratio"]
    assert r["expect_warn"] is False and r["expect_dead"] is False
    # now=21:00 → 14/7=2.0 → dead
    r2 = cycle_structure.expect_check(bars, "long", now=base + datetime.timedelta(hours=21))
    assert abs(r2["time_ratio"] - 2.0) < 0.06, r2["time_ratio"]
    assert r2["expect_dead"] is True
    print("PASS long_side ratio=%.2f→%.2f" % (r["time_ratio"], r2["time_ratio"]))


def test_no_counter():
    bars = _synth_long()
    base = datetime.datetime(2026, 1, 1, 0, 0)
    # 空单视角:最新主要段是上涨(持仓反向)…换一组:直接测上涨趋势中的空单视角无原段
    up_only = []
    for i in range(12):
        t = (base + datetime.timedelta(hours=i)).strftime("%Y-%m-%d %H:%M")
        c = 100 + i
        up_only.append({"time": t, "open": c - 1, "high": c + 0.1, "low": c - 1.1,
                        "close": c, "volume": 1.0, "amount": c})
    r = cycle_structure.expect_check(up_only, "short", now=base + datetime.timedelta(hours=12))
    assert r["time_ratio"] is None and r["expect_warn"] is False and r["expect_dead"] is False, r
    print("PASS no_counter (纯上涨中空单视角无原段→静默)")


if __name__ == "__main__":
    test_replay_warn()
    test_replay_dead()
    test_point_in_time()
    test_action_split()
    test_long_side()
    test_no_counter()
    print("ALL PASS test_p30_expect")
