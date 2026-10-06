#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""easy-stock-service 数据持久化后台维护:磁盘/日志/留存/性能巡检。

每天 03:00 由 cron svc-maintenance 调用。只做可逆的清理:
- 运维日志(backend.log/stdout.log/push.log):只保留最近 5000 行
- 日志 JSON/JSONL:90天后 gzip,压缩包 365 天后删除
- K线图(logs/charts, reviews/charts):30天后删除(可随时用 review_charts 重画)
- paper_ledger.json / decisions_*.json / sentiment_intraday_*.jsonl:训练数据,永不删除
  (只做 gzip 归档,不删除——自学习燃料)
- data/*.db:只上报大小,不动内容
- 磁盘 >80% 时推送告警
落盘:logs/maintenance_<date>.log
"""
import gzip
import os
import shutil
import sys
import time

SVC = os.path.dirname(os.path.abspath(__file__))
LOGS = os.path.join(SVC, "logs")
DAY = 86400
NOW = time.time()

REPORT = []


def log(msg):
    REPORT.append(msg)
    print(msg, flush=True)


def disk_check():
    st = shutil.disk_usage(SVC)
    pct = st.used / st.total * 100
    log("disk: %.1f%% used (%s/%s)" % (
        pct, _h(st.used), _h(st.total)))
    if pct > 80:
        try:
            from push import send as push_send
            push_send("🟡磁盘告警", "easy-stock-service 磁盘使用率 %.1f%%,请检查" % pct)
        except Exception as e:
            log("disk alert push fail: %s" % e)
    return pct


def _h(n):
    for u in ("B", "K", "M", "G"):
        if n < 1024:
            return "%.1f%s" % (n, u)
        n /= 1024
    return "%.1fT" % n


def rotate_logs():
    """运维日志截断到最近5000行。"""
    for name in ("backend.log", "stdout.log", "push.log",
                 "breakout_backtest.log"):
        p = os.path.join(LOGS, name)
        if not os.path.exists(p) or os.path.getsize(p) == 0:
            continue
        with open(p, "rb") as f:
            lines = f.read().split(b"\n")
        if len(lines) > 5000:
            with open(p, "wb") as f:
                f.write(b"\n".join(lines[-5000:]))
            log("rotate %s: %d -> 5000 lines" % (name, len(lines)))
        else:
            log("rotate %s: ok (%d lines)" % (name, len(lines)))


def archive_old():
    """JSON/JSONL:90天 gzip;gz 包 365 天删除。训练数据只归档不删除。"""
    n_gz, n_rm, sz = 0, 0, 0
    for root, _, files in os.walk(LOGS):
        if "charts" in root:
            continue
        for fn in files:
            p = os.path.join(root, fn)
            age = NOW - os.path.getmtime(p)
            if fn.endswith(".gz"):
                if age > 365 * DAY and "ledger" not in fn and "decisions" not in fn \
                        and "sentiment_intraday" not in fn:
                    sz += os.path.getsize(p)
                    os.remove(p)
                    n_rm += 1
            elif (fn.endswith(".json") or fn.endswith(".jsonl")) and age > 90 * DAY:
                if "paper_ledger" in fn:
                    continue  # 账本永不归档删除
                with open(p, "rb") as f:
                    raw = f.read()
                with gzip.open(p + ".gz", "wb") as f:
                    f.write(raw)
                os.remove(p)
                n_gz += 1
                sz += len(raw)
    log("archive: gzipped %d, deleted %d old gz, freed %s" % (n_gz, n_rm, _h(sz)))


def clean_charts():
    """K线图 30 天后删除(可重画)。"""
    n, sz = 0, 0
    for base in (os.path.join(LOGS, "charts"),
                 os.path.join(SVC, "reviews", "charts")):
        if not os.path.isdir(base):
            continue
        for day in os.listdir(base):
            dp = os.path.join(base, day)
            if not os.path.isdir(dp):
                continue
            try:
                age = NOW - os.path.getmtime(dp)
            except OSError:
                continue
            if age > 30 * DAY:
                for root, _, files in os.walk(dp):
                    for fn in files:
                        fp = os.path.join(root, fn)
                        try:
                            sz += os.path.getsize(fp)
                            os.remove(fp)
                            n += 1
                        except OSError:
                            pass
                try:
                    os.rmdir(dp)
                except OSError:
                    pass
    log("charts: deleted %d files older than 30d, freed %s" % (n, _h(sz)))


def db_report():
    d = os.path.join(SVC, "data")
    if not os.path.isdir(d):
        return
    for fn in sorted(os.listdir(d)):
        if fn.endswith(".db"):
            p = os.path.join(d, fn)
            log("db %s: %s (只上报,不动)" % (fn, _h(os.path.getsize(p))))


def dup_check():
    a = os.path.join(LOGS, "backend.log")
    b = os.path.join(LOGS, "stdout.log")
    try:
        if os.path.exists(a) and os.path.exists(b):
            import hashlib
            ha = hashlib.md5(open(a, "rb").read()).hexdigest()
            hb = hashlib.md5(open(b, "rb").read()).hexdigest()
            if ha == hb:
                log("note: backend.log 与 stdout.log 内容完全相同(后端双写,保留现状)")
    except Exception as e:
        log("dup check fail: %s" % e)


def main():
    dry = "--dry-run" in sys.argv
    log("=== maintenance %s ===" % time.strftime("%Y-%m-%d %H:%M"))
    disk_check()
    if not dry:
        rotate_logs()
        archive_old()
        clean_charts()
    else:
        log("(dry-run: 跳过实际清理)")
    db_report()
    dup_check()
    rp = os.path.join(LOGS, "maintenance_%s.log" % time.strftime("%Y-%m-%d"))
    if not dry:
        with open(rp, "a") as f:
            f.write("\n".join(REPORT) + "\n")
    log("done")


if __name__ == "__main__":
    main()
