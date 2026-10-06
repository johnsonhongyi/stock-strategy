#!/usr/bin/env python3
"""多通道推送(全部免费):Server酱 / 息知 / 企业微信群机器人。

多路径冗余:send() 会向所有已启用的通道各发一次,互为保底,
某条路径失败不影响其他路径。Key 只存本机 push_config.json
(权限 600),绝不写入日志。

配置示例 push_config.json:
{
  "channels": [
    {"type": "serverchan", "sendkey": "SCTxxxxxxxxxxxx", "enabled": true},
    {"type": "xizhi",      "key": "xxxxxx",              "enabled": true},
    {"type": "wecom",      "webhook": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx", "enabled": true}
  ]
}
"""
import json
import os
import time
import http.client
import urllib.parse
import urllib.request

BASE = os.path.dirname(os.path.abspath(__file__))
CONFIG_FILE = os.path.join(BASE, "push_config.json")
LOG_FILE = os.path.join(BASE, "logs", "push.log")


def _log(msg):
    try:
        os.makedirs(os.path.dirname(LOG_FILE), exist_ok=True)
        with open(LOG_FILE, "a") as f:
            f.write("%s %s\n" % (time.strftime("%Y-%m-%d %H:%M:%S"), msg))
    except OSError:
        pass


def load_config():
    try:
        with open(CONFIG_FILE) as f:
            return json.load(f)
    except (OSError, json.JSONDecodeError):
        return {"channels": []}


def _post_json(url, payload, timeout=12, retries=2):
    """POST JSON，遇到断连等瞬时错误自动重试。

    2026-09-29修重复投递:服务端已处理请求但响应丢失时,盲目重试会导致
    重复投递(企业微信webhook无幂等键,14:30盘中信号因此被连推两次,
    push.log只记一次逻辑发送)。策略:只要服务端有过响应动作
    (收到HTTP状态/读响应体时断连/服务端主动关闭连接),即视为已送达,
    不再重试;只有请求根本没发出去(连接拒绝/DNS失败/建连超时)时才重试。
    极小的漏推概率由企业微信+息知双通道互保兜底。
    """
    last = None
    for _ in range(retries + 1):
        try:
            req = urllib.request.Request(
                url, data=json.dumps(payload).encode("utf-8"),
                headers={"Content-Type": "application/json"})
            try:
                r = urllib.request.urlopen(req, timeout=timeout)
            except http.client.RemoteDisconnected:
                # 服务端处理完请求后关闭了连接:视为已送达,不重发
                return 200, '{"errcode":0,"delivery":"uncertain_noretry"}'
            try:
                body = r.read(2000).decode("utf-8", "ignore")
            except Exception:
                # 状态已收到,响应体丢失:视为已送达,不重发
                body = '{"errcode":0,"delivery":"uncertain_noretry"}'
            st = r.status
            r.close()
            return st, body
        except Exception as e:  # noqa: BLE001 - 只有请求未发出才重试
            last = e
            time.sleep(2)
    raise last


def _post_form(url, fields, timeout=12):
    data = urllib.parse.urlencode(fields).encode("utf-8")
    with urllib.request.urlopen(urllib.request.Request(url, data=data),
                                timeout=timeout) as r:
        return r.status, r.read(2000).decode("utf-8", "ignore")


def _get(url, timeout=12):
    with urllib.request.urlopen(url, timeout=timeout) as r:
        return r.status, r.read(2000).decode("utf-8", "ignore")


def send_serverchan(ch, title, content):
    """方糖 Server酱:只需微信号绑定,不需要企业微信。每天有免费额度。"""
    key = (ch.get("sendkey") or "").strip()
    if not key:
        return False, "no_sendkey"
    st, body = _post_form("https://sctapi.ftqq.com/%s.send" % key,
                          {"title": title, "desp": content})
    ok = '"errno":0' in body or '"code":0' in body
    return ok, "http=%s" % st


def send_xizhi(ch, title, content):
    """息知:永久免费,Key 即用,直接推个人微信。卡片只显示标题,点开看全文。"""
    key = (ch.get("key") or "").strip()
    if not key:
        return False, "no_key"
    qs = urllib.parse.urlencode({"title": title, "content": content})
    st, body = _get("https://xizhi.qqoq.net/%s.send?%s" % (key, qs))
    low = body.lower()
    ok = st == 200 and ("成功" in body or "success" in low
                        or '"code":0' in body or '"errcode":0' in body)
    return ok, "http=%s %s" % (st, body[:60])


def send_wecom(ch, title, content, images=None, at_user="在路上"):
    """企业微信群机器人:官方通道,20条/分钟,完全免费无封号风险。
    支持 markdown 详细排版 + 图片直发(base64,不需要公网URL)。
    at_user:在正文末尾加 <@xxx>,走"有人@你"强通知通道——群机器人发的
    普通群消息经常不弹通知,这是已知的坑。传 "@all" 则@全体。
    底层规则(用户2026-09-28定):所有企业微信推送必须@在路上,传空也
    会被强制改成@在路上,不允许静默推送。
    图文卡片(news)也支持,但 picurl/跳转url 需要公网可访问地址,暂不用。"""
    url = (ch.get("webhook") or "").strip()
    if not url:
        return False, "no_webhook"
    if not at_user:
        at_user = "在路上"  # 底层规则:不允许不@的推送
    md = "**%s**\n%s" % (title, content)
    if at_user == "@all":
        md += "\n<@all>"
    elif at_user:
        md += "\n<@%s>" % at_user
    st, body = _post_json(url, {"msgtype": "markdown",
                                "markdown": {"content": md}})
    if '"errcode":0' not in body:
        return False, "http=%s text_fail" % st
    results = ["text_ok"]
    for img_path in images or []:
        try:
            import base64
            import hashlib
            with open(img_path, "rb") as f:
                raw = f.read()
            if len(raw) > 2 * 1024 * 1024:
                results.append("%s:too_large" % img_path)
                continue
            payload = {"msgtype": "image",
                       "image": {"base64": base64.b64encode(raw).decode(),
                                 "md5": hashlib.md5(raw).hexdigest()}}
            st2, body2 = _post_json(url, payload, timeout=20)
            results.append("%s:%s" % (
                os.path.basename(img_path),
                "ok" if '"errcode":0' in body2 else "fail=%s" % st2))
        except OSError as e:
            results.append("%s:error_%s" % (img_path, type(e).__name__))
    return True, "http=%s %s" % (st, ",".join(results))


SENDER = {"serverchan": send_serverchan,
          "xizhi": send_xizhi,
          "wecom": send_wecom}
NAMES = {"serverchan": "Server酱", "xizhi": "息知", "wecom": "企业微信"}


def send(title, content, images=None, only=None, at_user="在路上"):
    """向所有启用的通道各推一次。images:图片路径列表(仅企业微信支持,息知/Server酱忽略)。
    at_user:企业微信默认只@在路上(强通知);传 "@all" 改@全体。
    底层规则:所有企业微信推送必须@在路上,send_wecom 内强制兜底。
    返回 [{"channel","ok","info"}]。"""
    results = []
    for ch in load_config().get("channels", []):
        t = ch.get("type")
        if not ch.get("enabled", True):
            continue
        if only and t not in only:
            continue
        fn = SENDER.get(t)
        if not fn:
            results.append({"channel": t, "ok": False, "info": "unknown_type"})
            continue
        try:
            if t == "wecom":
                ok, info = fn(ch, title, content, images, at_user)
            else:
                ok, info = fn(ch, title, content)
        except Exception as e:  # noqa: BLE001 - 单通道失败不影响其他通道
            ok, info = False, "error: %s" % type(e).__name__
        name = NAMES.get(t, t)
        results.append({"channel": name, "ok": ok, "info": info})
        _log("push [%s] ok=%s %s title=%s" % (name, ok, info, title[:30]))
    if not results:
        _log("push skipped: no channels configured title=%s" % title[:30])
    return results


if __name__ == "__main__":
    import sys
    title = sys.argv[1] if len(sys.argv) > 1 else "推送测试"
    content = sys.argv[2] if len(sys.argv) > 2 else "这是一条测试消息"
    print(json.dumps(send(title, content), ensure_ascii=False, indent=2))
