#!/usr/bin/env python3
"""55188 数据异动集:理想论坛 sec.php 页面底下的三个真实异动源。

来源(页面 JS 里扒出来的直连接口):
1. 涨停股票池: data.10jqka.com.cn/dataapi/limit_up/limit_up_pool (同花顺)
2. 人气热榜: eq.10jqka.com.cn/open/api/hot_list (同花顺)
3. 盘中异动: flash-api.xuangubao.cn/api/surge_stock (选股宝)

用法: python3 yidong.py [--dry-run]
输出: logs/yidong_YYYYMMDD.json,结构:
  {"date":..., "stocks":[{"code":"600000","name":"..","sources":["涨停池","人气榜"],
    "high_days":"2板","limit_up_type":"换手板","reason":"..","hot_rank":3,...}]}
用户规则:用这份异动集做候选,不做全市场蛮扫。
"""
import json
import os
import sys
import time
import urllib.request
import market_cache
from datetime import datetime
from zoneinfo import ZoneInfo

BJ = ZoneInfo("Asia/Shanghai")
SVC = os.path.dirname(os.path.abspath(__file__))
UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
      "Referer": "https://www.55188.com/sec.php"}


def get_json(url, timeout=15, retries=3):
    last = None
    for i in range(retries):
        try:
            req = urllib.request.Request(url, headers=UA)
            with market_cache.urlopen(req, timeout=timeout) as r:
                return json.loads(r.read().decode("utf-8", "ignore"))
        except Exception as e:
            last = e
            if i < retries - 1:
                import time
                time.sleep(1.5)
    raise last


def norm(code):
    """各种代码格式 -> 6位纯数字"""
    c = str(code).strip().upper().replace(".SH", "").replace(".SZ", "").replace(".BJ", "")
    c = c.split(".")[0]
    return c if len(c) == 6 and c.isdigit() else None


def fetch_limitup():
    """涨停池:返回 {code: {...}}。
    2026-09-30修:同花顺接口频繁IncompleteRead,曾静默失败导致异动候选断流。
    加3次重试;仍失败用东财涨幅榜(f3>=9.5%)兜底;全失败记日志告警,不再静默。"""
    url = ("https://data.10jqka.com.cn/dataapi/limit_up/limit_up_pool"
           "?page=1&limit=200&field=199112,10,9001,330323,330324,330325,9002,"
           "330329,133971,133970,1968584,3475914,9003,9004"
           "&filter=HS,GEM2STAR&order_field=330324&order_type=0&date=")
    out = {}
    d = None
    for i in range(3):
        try:
            d = get_json(url).get("data", {}).get("info", [])
            break
        except Exception as e:
            print("limitup retry %d: %s" % (i + 1, e))
            time.sleep(2)
    if d:
        for it in d:
            c = norm(it.get("code"))
            if not c:
                continue
            out[c] = {
                "code": c, "name": it.get("name", ""),
                "high_days": it.get("high_days", ""),       # 几天几板
                "limit_up_type": it.get("limit_up_type", ""),  # 涨停类型
                "reason": it.get("reason_type", ""),          # 涨停原因
                "turnover_rate": it.get("turnover_rate"),
                "order_amount": it.get("order_amount"),      # 封单额
                "suc_rate": it.get("limit_up_suc_rate"),     # 封板率
                "change_rate": it.get("change_rate"),
                "latest": it.get("latest"),
            }
        return out
    # fallback:东财涨幅榜(涨幅>=9.5%视为涨停候选)
    try:
        eu = ("https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=200&po=1&np=1"
              "&ut=bd1d9ddb04089700cf9c27f6f742942c&fltt=2&invt=2&fid=f3"
              "&fs=m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23&fields=f12,f14,f3")
        req = urllib.request.Request(eu, headers={"User-Agent": "Mozilla/5.0"})
        diff = json.load(market_cache.urlopen(req, timeout=15)
                         ).get("data", {}).get("diff", [])
        for x in diff:
            if (x.get("f3") or 0) >= 9.5 and x.get("f12"):
                c = norm(x["f12"])
                out[c] = {"code": c, "name": x.get("f14", ""),
                          "high_days": "", "limit_up_type": "东财兜底",
                          "reason": "", "fallback": True}
        print("limitup fallback:东财涨幅榜兜底 %d 只" % len(out))
    except Exception as e:
        print("limitup全失败(同花顺3次+东财兜底): %s" % e)
    return out


def fetch_hotlist():
    """人气热榜:返回 {code: {...}}"""
    url = "https://eq.10jqka.com.cn/open/api/hot_list/v1/hot_stock/a/hour/data.txt"
    out = {}
    try:
        d = get_json(url).get("data", {}).get("stock_list", [])
    except Exception as e:
        print("hotlist fail:", e)
        return out
    for it in d:
        c = norm(it.get("code"))
        if not c:
            continue
        tag = it.get("tag") or {}
        out[c] = {
            "code": c, "name": it.get("name", ""),
            "hot_rank": it.get("order"),
            "hot_rate": it.get("rate"),
            "concept_tags": tag.get("concept_tag") or [],
            "popularity_tag": tag.get("popularity_tag") or "",
        }
    return out


def fetch_surge():
    """选股宝异动:返回 {code: {...}}"""
    url = "https://flash-api.xuangubao.cn/api/surge_stock/stocks?normal=true&uplimit=true"
    out = {}
    try:
        d = get_json(url).get("data", {})
    except Exception as e:
        print("surge fail:", e)
        return out
    fields = d.get("fields", [])
    items = d.get("items", [])
    if not fields or not items:
        return out
    for row in items:
        rec = dict(zip(fields, row))
        c = norm(rec.get("code"))
        if not c:
            continue
        out[c] = {
            "code": c, "name": rec.get("prod_name", ""),
            "surge_change": rec.get("px_change_rate"),
            "surge_desc": (rec.get("description") or "")[:80],
            "surge_plates": rec.get("plates") or "",
            "m_days_n_boards": rec.get("m_days_n_boards") or "",
        }
    return out


def board_num(high_days):
    """'首板'->1,'2板'->2,'3天2板'->2,未知->0"""
    import re
    s = str(high_days or "")
    if "首" in s:
        return 1
    m = re.search(r"(\d+)\s*板", s)
    if m:
        return int(m.group(1))
    return 0


def build():
    lu = fetch_limitup()
    hl = fetch_hotlist()
    sg = fetch_surge()
    merged = {}
    for c, r in lu.items():
        merged.setdefault(c, {"code": c, "name": r["name"], "sources": []})
        merged[c]["sources"].append("涨停池")
        merged[c].update({k: v for k, v in r.items() if k not in ("code", "name")})
    for c, r in hl.items():
        merged.setdefault(c, {"code": c, "name": r["name"], "sources": []})
        merged[c]["sources"].append("人气榜")
        merged[c].update({k: v for k, v in r.items() if k not in ("code", "name")})
    for c, r in sg.items():
        merged.setdefault(c, {"code": c, "name": r["name"], "sources": []})
        merged[c]["sources"].append("异动")
        merged[c].update({k: v for k, v in r.items() if k not in ("code", "name")})

    def prio(m):
        # 涨停池连板 > 涨停池首板 > 多源交叉 > 人气榜 > 异动
        bn = board_num(m.get("high_days"))
        src = len(m.get("sources", []))
        hot = m.get("hot_rank") or 999
        return (bn * 100 + src * 10 - hot * 0.01)

    stocks = sorted(merged.values(), key=prio, reverse=True)
    return {
        "date": datetime.now(BJ).strftime("%Y-%m-%d"),
        "counts": {"涨停池": len(lu), "人气榜": len(hl), "异动": len(sg),
                   "去重合计": len(stocks)},
        "stocks": stocks,
    }


def main():
    dry = "--dry-run" in sys.argv
    data = build()
    print("异动集:", data["counts"])
    for s in data["stocks"][:15]:
        print("  %s %s [%s] %s" % (s["code"], s["name"],
              ",".join(s["sources"]), s.get("high_days") or s.get("popularity_tag") or ""))
    if dry:
        return
    p = os.path.join(SVC, "logs", "yidong_%s.json" % data["date"])
    json.dump(data, open(p, "w"), ensure_ascii=False, indent=1)
    print("saved:", p)


if __name__ == "__main__":
    main()
