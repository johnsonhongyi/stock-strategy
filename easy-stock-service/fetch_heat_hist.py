#!/usr/bin/env python3
"""涨停池历史抓取:2026-08-03~2026-09-29每个交易日 -> logs/heat_hist_<YYYYMMDD>.json
幂等/断点续跑,间隔>=0.5秒,失败重试3次。"""
import market_cache
import json, os, sys, time, urllib.request, http.client
from datetime import datetime
from zoneinfo import ZoneInfo

BJ = ZoneInfo("Asia/Shanghai")
SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)
from trading_calendar import next_trading_day

UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
      "Referer": "https://www.55188.com/sec.php"}

def norm(code):
    c = str(code).strip().upper().replace(".SH","").replace(".SZ","").replace(".BJ","")
    c = c.split(".")[0]
    return c if len(c)==6 and c.isdigit() else None

def board_num(high_days):
    import re
    s = str(high_days or "")
    if "首" in s: return 1
    m = re.search(r"(\d+)\s*板", s)
    return int(m.group(1)) if m else 0

class UpstreamBanned(Exception):
    """上游返回403/被封IP:立即停手,不再重试,避免延长封禁"""
    pass

def get_json(url, timeout=25, retries=8):
    last = None
    for i in range(retries):
        try:
            req = urllib.request.Request(url, headers=UA)
            with market_cache.urlopen(req, timeout=timeout) as r:
                return json.loads(r.read().decode("utf-8","ignore"))
        except http.client.IncompleteRead as e:
            # 10jqka chunked断流常发,试partial
            try:
                return json.loads(e.partial.decode("utf-8","ignore"))
            except Exception:
                last = e
        except urllib.error.HTTPError as e:
            # 2026-09-29事故:51连发后IP被同花顺nginx封(403),重试只会加重
            if e.code == 403:
                raise UpstreamBanned("10jqka 403: IP被封,停止请求 %s" % url)
            last = e
        except Exception as e:
            last = e
        if i < retries-1:
            time.sleep(2*(i+1))
    raise last

def salvage_info(text):
    """从截断JSON里按花括号配对打捞完整对象"""
    objs = []
    p = text.find('"info"')
    if p < 0:
        return objs
    depth = 0; cur = None; instr = False; esc = False
    for i in range(p, len(text)):
        ch = text[i]
        if instr:
            if esc: esc = False
            elif ch == '\\': esc = True
            elif ch == '"': instr = False
            continue
        if ch == '"': instr = True
        elif ch == '{':
            if depth == 0: cur = i
            depth += 1
        elif ch == '}':
            depth -= 1
            if depth == 0 and cur is not None:
                try:
                    o = json.loads(text[cur:i + 1])
                    if o.get("code"): objs.append(o)
                except Exception:
                    pass
                cur = None
    return objs

def fetch_day(yyyymmdd):
    url = ("https://data.10jqka.com.cn/dataapi/limit_up/limit_up_pool"
           "?page=1&limit=200&field=199112,10,9001,330323,330324,330325,9002,"
           "330329,133971,133970,1968584,3475914,9003,9004"
           "&filter=HS,GEM2STAR&order_field=330324&order_type=0&date="+yyyymmdd)
    partial = False
    try:
        info = get_json(url).get("data",{}).get("info",[])
    except Exception as e:
        # 兜底:打捞partial
        try:
            req = urllib.request.Request(url, headers=UA)
            market_cache.urlopen(req, timeout=25).read()
            info = []
        except http.client.IncompleteRead as ie:
            info = salvage_info(ie.partial.decode("utf-8","ignore"))
            partial = True
        except Exception:
            raise e
    stocks = []
    for it in info:
        c = norm(it.get("code"))
        if not c: continue
        stocks.append({
            "code": c, "name": it.get("name",""),
            "high_days": it.get("high_days",""),
            "board": board_num(it.get("high_days","")),
            "limit_up_type": it.get("limit_up_type",""),
            "reason": it.get("reason_type",""),
            "suc_rate": it.get("limit_up_suc_rate"),
            "turnover_rate": it.get("turnover_rate"),
            "latest": it.get("latest"),
        })
    return stocks, partial

def valid_file(p):
    try:
        d = json.load(open(p))
        return isinstance(d.get("stocks"), list) and len(d["stocks"]) > 0
    except Exception:
        return False

def main():
    start = sys.argv[1] if len(sys.argv) > 1 else "2026-08-03"
    end = sys.argv[2] if len(sys.argv) > 2 else "2026-09-29"
    days = []; d = start
    while d <= end:
        days.append(d); d = next_trading_day(d)
    # 同花顺批量节流:一批次后至少30分钟(2026-09-29封IP事故)
    import time as _t, json as _j
    _th = os.path.join(SVC, "logs", "upstream_throttle.json")
    try: _td = _j.load(open(_th))
    except Exception: _td = {}
    _last = (_td.get("ths_bulk") or {}).get("last", 0)
    if _t.time() - _last < 1800:
        print("ths_bulk cooling down, wait %ds" % int(1800 - (_t.time() - _last))); return
    _td["ths_bulk"] = {"last": _t.time(), "note": "heat_hist batch"}
    _j.dump(_td, open(_th, "w"), ensure_ascii=False, indent=1)
    ok, fail, skip = 0, [], 0
    for d in days:
        ymd = d.replace("-","")
        p = os.path.join(SVC,"logs","heat_hist_%s.json"%ymd)
        if os.path.exists(p) and valid_file(p):
            skip += 1; continue
        try:
            stocks, partial = fetch_day(ymd)
            if not stocks:
                raise RuntimeError("empty pool for "+ymd)
            json.dump({"date":d,"count":len(stocks),"partial":partial,"stocks":stocks},
                      open(p,"w"),ensure_ascii=False,indent=1)
            ok += 1
            print(d, "OK", len(stocks), "partial" if partial else "", flush=True)
        except UpstreamBanned as e:
            print(d, "BANNED", str(e)[:100], flush=True)
            print("上游封IP,整批停止,剩余日期下次解封后再跑", flush=True)
            break
        except Exception as e:
            fail.append((d,str(e)[:100]))
            print(d, "FAIL", str(e)[:100], flush=True)
        # 同花顺反爬严:日期间至少隔3秒,禁止密集连发(2026-09-29曾0.6秒间隔被封IP)
        time.sleep(3)
    print("done: ok=%d skip=%d fail=%d"%(ok,skip,len(fail)))
    for f in fail: print("  FAIL",f)

if __name__=="__main__":
    main()
