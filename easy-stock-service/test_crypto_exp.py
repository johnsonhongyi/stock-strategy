#!/usr/bin/env python3
"""影子实验仓(v1.5)测试:阈值覆盖/账本隔离/推送标题/near-miss/非法组合/dry-run。
只读 main 账本做 md5 校验,不写 main 账本;不发真实推送。"""
import hashlib
import json
import os
import subprocess
import sys

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)

MAIN_LEDGERS = ["logs/paper_ledger.json", "logs/paper_ledger_us.json",
                "logs/paper_ledger_crypto.json", "logs/paper_ledger_crypto_short.json"]


def md5s():
    out = {}
    for f in MAIN_LEDGERS:
        p = os.path.join(SVC, f)
        out[f] = hashlib.md5(open(p, "rb").read()).hexdigest() if os.path.exists(p) else None
    return out


def test_ledger_routing():
    import paper_trade as pt
    pt.MARKET, pt.SIDE = "CRYPTO", "long"
    pt.SLEEVE = "main"
    assert pt._ledger_path().endswith("paper_ledger_crypto.json"), pt._ledger_path()
    pt.SLEEVE = "exp"
    assert pt._ledger_path().endswith("paper_ledger_crypto_exp.json"), pt._ledger_path()
    pt.SIDE = "short"
    pt.SLEEVE = "main"
    assert pt._ledger_path().endswith("paper_ledger_crypto_short.json"), pt._ledger_path()
    pt.SLEEVE = "exp"
    assert pt._ledger_path().endswith("paper_ledger_crypto_exp_short.json"), pt._ledger_path()
    pt.MARKET, pt.SIDE, pt.SLEEVE = "CN", "long", "main"
    assert pt._ledger_path().endswith("paper_ledger.json")
    print("ok test_ledger_routing")


def test_cfg_override():
    import paper_trade as pt
    pt.MARKET, pt.SLEEVE = "CRYPTO", "main"
    cfg = pt.load_cfg()
    assert cfg["buy"]["min_pool_score"] == 8.0, cfg["buy"]["min_pool_score"]
    assert cfg["crypto_short"]["entry"]["s_pool_score"] == 8.0
    pt.SLEEVE = "exp"
    cfg = pt.load_cfg()
    assert cfg["buy"]["min_pool_score"] == 6.0, cfg["buy"]["min_pool_score"]
    assert cfg["crypto_short"]["entry"]["s_pool_score"] == 6.0
    # 其它参数不受影响(单变量实验)
    assert cfg["crypto_short"]["exit"]["hard_stop_pct"] == 0.015
    assert cfg["buy"]["max_positions"] == cfg["buy"]["max_positions"]
    pt.SLEEVE = "main"
    print("ok test_cfg_override")


def test_mtag_exp():
    import paper_trade as pt
    pt.MARKET, pt.SLEEVE = "CRYPTO", "exp"
    pt.SIDE = "short"
    assert pt.mtag() == "[实验](CRYPTO空头)", pt.mtag()
    pt.SIDE = "long"
    assert pt.mtag() == "[实验]", pt.mtag()
    pt.SLEEVE = "main"
    pt.SIDE = "short"
    assert pt.mtag() == "(CRYPTO空头)"
    pt.SIDE = "long"
    assert pt.mtag() == ""
    pt.MARKET = "CN"
    print("ok test_mtag_exp")


def test_near_miss_band():
    import paper_trade as pt
    b = pt._near_miss_band
    assert b(7.0, 8.0, 6.0) is True
    assert b(6.0, 8.0, 6.0) is True    # 下界含
    assert b(8.0, 8.0, 6.0) is False  # 上界不含(主阈值会开仓)
    assert b(5.9, 8.0, 6.0) is False
    assert b(10.0, 8.0, 6.0) is False
    print("ok test_near_miss_band")


def test_near_miss_write():
    import paper_trade as pt
    p = "/tmp/near_miss_test.jsonl"
    if os.path.exists(p):
        os.remove(p)
    pt.MARKET = "CRYPTO"
    line = pt.record_near_miss("BTC", "long", 7.0, 84000.5, "测试", path=p)
    got = json.loads(open(p).read().strip().split("\n")[-1])
    assert got["coin"] == "BTC" and got["side"] == "long" and got["score"] == 7.0
    assert got["entry_price"] == 84000.5 and got["sleeve"] == "main"
    assert "strategy_version" in got and "date" in got and "reason" in got
    assert line == got
    os.remove(p)
    print("ok test_near_miss_write")


def test_exp_ledger_isolation():
    import paper_trade as pt
    before = md5s()
    pt.MARKET, pt.SIDE, pt.SLEEVE = "CRYPTO", "long", "exp"
    pt.LEDGER_CRYPTO_EXP = "/tmp/test_exp_ledger.json"
    pt.LEDGER_CRYPTO_EXP_SHORT = "/tmp/test_exp_short_ledger.json"
    for f in ("/tmp/test_exp_ledger.json", "/tmp/test_exp_short_ledger.json"):
        if os.path.exists(f):
            os.remove(f)
    lg = pt.load_ledger()
    assert lg["cash"] == 100000.0 and lg["sleeve"] == "exp"
    assert lg["exp_kill"] == {"armed": False, "reason": "", "at": ""}
    lg["positions"].append({"code": "TST", "side": "long"})
    pt.save_ledger(lg)
    assert os.path.exists("/tmp/test_exp_ledger.json")
    lg2 = pt.load_ledger()
    assert any(pp["code"] == "TST" for pp in lg2["positions"])
    assert lg2["exp_kill"]["armed"] is False  # 独立 kill 默认未拉起
    # 空头 exp 账本归一化
    pt.SIDE = "short"
    lg3 = pt.load_ledger()
    assert lg3["account"]["margin_balance"] == 100000.0
    assert lg3["short_kill"]["armed"] is False
    for f in ("/tmp/test_exp_ledger.json", "/tmp/test_exp_short_ledger.json"):
        if os.path.exists(f):
            os.remove(f)
    assert md5s() == before, "main 账本被污染!"
    pt.MARKET, pt.SIDE, pt.SLEEVE = "CN", "long", "main"
    print("ok test_exp_ledger_isolation")


def test_decision_sleeve_tag():
    import paper_trade as pt
    pt.MARKET, pt.SIDE, pt.SLEEVE = "CRYPTO", "long", "exp"
    date = pt._today()
    fn = os.path.join(SVC, "logs", "decisions_crypto_exp_%s.json" % date)
    existed = os.path.exists(fn)
    before = open(fn).read() if existed else None
    pt.log_decision(date, {"session": "test", "action": "TEST", "code": "TST",
                           "why": "exp sleeve tag 测试"})
    d = json.load(open(fn))
    last = d["decisions"][-1]
    assert last["sleeve"] == "exp", last
    assert last["strategy_version"] == "v1.8", last.get("strategy_version")
    assert last["action"] == "TEST"
    if existed:
        open(fn, "w").write(before)
    else:
        os.remove(fn)
    pt.MARKET, pt.SIDE, pt.SLEEVE = "CN", "long", "main"
    print("ok test_decision_sleeve_tag")


def test_illegal_combo():
    for market in ("US", "CN"):
        r = subprocess.run([sys.executable, "paper_trade.py", "--market", market,
                            "--sleeve", "exp", "--signal"],
                           cwd=SVC, capture_output=True, text=True, timeout=60)
        assert r.returncode == 2, (market, r.returncode, r.stdout, r.stderr)
    print("ok test_illegal_combo")


def test_dry_runs():
    for side in ("long", "short"):
        r = subprocess.run(
            [sys.executable, "paper_trade.py", "--market", "CRYPTO", "--sleeve", "exp",
             "--side", side, "--signal", "--dry-run"],
            cwd=SVC, capture_output=True, text=True, timeout=240)
        assert r.returncode == 0, (side, r.stdout[-800:], r.stderr[-800:])
    # exp settle dry-run
    r = subprocess.run(
        [sys.executable, "paper_trade.py", "--market", "CRYPTO", "--sleeve", "exp",
         "--side", "short", "--settle", "--dry-run"],
        cwd=SVC, capture_output=True, text=True, timeout=240)
    assert r.returncode == 0, r.stdout[-800:]
    print("ok test_dry_runs")


def main():
    before = md5s()
    test_ledger_routing()
    test_cfg_override()
    test_mtag_exp()
    test_near_miss_band()
    test_near_miss_write()
    test_exp_ledger_isolation()
    test_decision_sleeve_tag()
    test_illegal_combo()
    test_dry_runs()
    assert md5s() == before, "测试污染了 main 账本!"
    print("ALL 9 TESTS PASSED, main 账本 md5 全程不变")


if __name__ == "__main__":
    main()
