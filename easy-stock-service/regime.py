"""大环境底座:把盘前情绪重组成 人流/资金/涟漪 三维,worker执行前必读,我每周复盘时审判它。
人流=注意力在哪(涨停/连板/人气/热点板块);资金=钱在哪(机构净买入/成交额/赚钱效应);
涟漪=扩散到哪(涨跌家数/板块轮动/炸板)。输出 logs/market_regime_<date>.json。"""
import json
import os
import sys
from datetime import datetime
from zoneinfo import ZoneInfo

SVC = os.path.dirname(os.path.abspath(__file__))
BJ = ZoneInfo("Asia/Shanghai")


def _load(path):
    try:
        return json.load(open(path))
    except Exception:
        return None


def build_regime(date, dry=False):
    pre = _load(os.path.join(SVC, "logs", "sentiment_%s_premarket.json" % date))
    m = (pre or {}).get("market", {})
    yidong = _load(os.path.join(SVC, "logs", "yidong_%s.json" % date)) or {}

    # 人流:注意力在哪
    renliu = {
        "涨停家数": m.get("limit_up"),
        "跌停家数": m.get("limit_down"),
        "连板高度": m.get("max_board"),
        "首板家数": (m.get("risk_appetite") or {}).get("first_board"),
        "热点板块": [{"板块": s.get("name"), "涨停": s.get("limit_up"),
                      "原因": s.get("reason", "")[:40]}
                     for s in (m.get("hot_sectors") or [])[:5]],
        "人气股": [{"代码": s.get("code"), "名称": s.get("name"),
                    "人气排名": s.get("hot_rank")}
                   for s in (yidong.get("hot_list") or [])[:5]],
    }
    # 资金:钱在哪
    zijin = {
        "上涨家数": m.get("up"),
        "下跌家数": m.get("down"),
        "赚钱效应": (m.get("parts") or {}).get("money_effect"),
        "成交额动量": (m.get("parts") or {}).get("amount_mom"),
        "风险偏好": (m.get("risk_appetite") or {}).get("level"),
        "风险偏好趋势": (m.get("risk_appetite") or {}).get("trend"),
        "昨日涨停今日表现": m.get("prev_limitup_ret"),
    }
    # 涟漪:扩散到哪
    rot = m.get("rotation") or {}
    lianyi = {
        "新进热点": rot.get("new_in"),
        "掉出热点": rot.get("dropped_out"),
        "炸板扣分": (m.get("parts") or {}).get("broken"),
        "蓄水池水位": (m.get("water") or {}).get("direction"),
        "水位连续下降天数": (m.get("water") or {}).get("days_declining"),
    }
    # 门禁:综合结论,worker执行层直接读这个
    score = m.get("score", 0)
    dead = bool(m.get("dead_day"))
    down = m.get("down") or 0
    if dead or down > 4000:
        gate, gate_why = "禁止新开仓", "装死日/下跌超4000家(P5)"
    elif score <= -40:
        gate, gate_why = "禁止新开仓", "情绪冰点<=-40"
    elif score <= -15:
        gate, gate_why = "只减仓不加仓", "情绪弱势"
    else:
        gate, gate_why = "正常执行", "情绪中性偏暖"
    out = {
        "date": date,
        "as_of": datetime.now(BJ).strftime("%Y-%m-%d %H:%M:%S"),
        "sentiment_score": score,
        "sentiment_label": m.get("label"),
        "人流": renliu,
        "资金": zijin,
        "涟漪": lianyi,
        "门禁": gate,
        "门禁原因": gate_why,
    }
    if not dry:
        p = os.path.join(SVC, "logs", "market_regime_%s.json" % date)
        json.dump(out, open(p, "w"), ensure_ascii=False, indent=1)
    print("regime %s: %s分[%s] 门禁=%s(%s)" % (
        date, score, m.get("label"), gate, gate_why))
    print("  人流:涨停%s家 连板%s板 首板%s家" % (
        renliu["涨停家数"], renliu["连板高度"], renliu["首板家数"]))
    print("  资金:涨跌 %s/%s 风险偏好%s(%s)" % (
        zijin["上涨家数"], zijin["下跌家数"],
        zijin["风险偏好"], zijin["风险偏好趋势"]))
    print("  涟漪:新进 %s 水位%s" % (lianyi["新进热点"], lianyi["蓄水池水位"]))
    return out


if __name__ == "__main__":
    dry = "--dry-run" in sys.argv
    date = sys.argv[1] if len(sys.argv) > 1 and not sys.argv[1].startswith("--") \
        else datetime.now(BJ).strftime("%Y-%m-%d")
    from trading_calendar import guard_trading_day, today_str
    if date == today_str():
        guard_trading_day("regime")  # 回填历史日期不受限
    build_regime(date, dry=dry)
