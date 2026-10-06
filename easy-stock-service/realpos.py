"""实盘持仓成本感知:信号文案区分 被套仓减亏 / 旧仓做T / 空仓试错。
数据来自 positions_real.json(用户手动维护),不参与模拟盘。"""
import json
import os

SVC = os.path.dirname(os.path.abspath(__file__))
PATH = os.path.join(SVC, "positions_real.json")

_cache = None


def load():
    global _cache
    if _cache is not None:
        return _cache
    try:
        d = json.load(open(PATH))
        _cache = {p["code"]: p for p in d.get("positions", [])}
    except Exception:
        _cache = {}
    return _cache


def get(code):
    return load().get(code)


def hint(code, price):
    """返回持仓语境文案,空仓返回None。price为现价。"""
    pos = get(code)
    if not pos or not pos.get("cost") or not price:
        return None
    cost = pos["cost"]
    ret = price / cost - 1
    name = pos.get("name", code)
    if ret < -0.005:
        ctx = "被套%.1f%%" % (ret * 100)
        sig = "减亏/做T信号,非新开仓"
    elif ret > 0.005:
        ctx = "浮盈+%.1f%%" % (ret * 100)
        sig = "持仓股,止盈/持有语境"
    else:
        ctx = "基本持平(%+.1f%%)" % (ret * 100)
        sig = "持仓股"
    return "【持仓%s:成本%.2f 现价%.2f %s】%s" % (name, cost, price, ctx, sig)


def is_held(code):
    return code in load()
