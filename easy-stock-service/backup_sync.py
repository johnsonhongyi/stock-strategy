#!/usr/bin/env python3
"""easy-stock 自动备份同步(每3天由定时任务调用)

流程: 打发布包(不含密钥) -> 本地全量快照(含密钥,仅本机) -> 密钥包刷新(仅本机)
      -> Drive 同步(发布包+三文档+restore.sh,发布包只留最近3版) -> 企业微信通知
密钥永不上传 Drive。
"""
import datetime
import json
import os
import shutil
import subprocess
import sys
import tarfile

HOME = os.path.expanduser("~")
WS = os.path.join(HOME, "workspace")
SVC = os.path.join(WS, "easy-stock-service")
WEB = os.path.join(WS, "easy-stock-web")
ANALYSIS = os.path.join(WS, "easy-stock-analysis")
DOCS = os.path.join(WS, "easy-stock-docs")
BACKUPS = os.path.join(WS, "backups")
DATE = datetime.datetime.now().strftime("%Y%m%d")
KEEP = 3

DRIVE_FOLDER = "1aaY4yiTAbr1xAoR9IFMZSglxrBGvi-ks"
DRIVE_DOC_IDS = {
    "01-系统说明文档.md": "1zs1mHIb_Vi7h5pKg5pJ4X-FmF7vkwb8z",
    "02-部署文档.md": "1HVz7iM5GjXCpQJ3Vl-XJ9-PHOdbWRjfM",
    "03-恢复文档.md": "1n2NtRVCDHhuN2lF6cPtWSSzv8FdOKfGQ",
}
DRIVE_RESTORE_ID = "1kNbqAz4RbPiKXgUmkkjHXljRI2K11j9F"

sys.path.insert(0, SVC)


def log(msg):
    print(f"[{datetime.datetime.now().strftime('%H:%M:%S')}] {msg}", flush=True)


def sh(cmd, **kw):
    r = subprocess.run(cmd, capture_output=True, text=True, **kw)
    return r


def prune(paths, keep=KEEP):
    """同类文件只留最新 keep 个,其余删除"""
    paths = sorted(paths)
    for p in paths[:-keep]:
        try:
            os.remove(p)
            log(f"清理旧文件: {os.path.basename(p)}")
        except OSError as e:
            log(f"清理失败 {p}: {e}")


def build_release(tgz_path):
    """组装发布包(不含 __pycache__/node_modules/.git/.env/push_config.json)"""
    rel = f"easy-stock-release-{DATE}"
    stage = os.path.join(WS, "_stage_" + rel)
    if os.path.exists(stage):
        shutil.rmtree(stage)
    root = os.path.join(stage, rel)
    os.makedirs(root)

    def copytree(src, dst, ignore_names=()):
        shutil.copytree(src, dst, ignore=shutil.ignore_patterns(*ignore_names))

    copytree(SVC, os.path.join(root, "easy-stock-service"),
             ("__pycache__", ".env", "push_config.json"))
    copytree(WEB, os.path.join(root, "easy-stock-web"),
             ("node_modules", "logs"))
    copytree(ANALYSIS, os.path.join(root, "easy-stock-analysis"),
             ("node_modules", ".git"))
    os.makedirs(os.path.join(root, "skills"))
    copytree(os.path.join(WS, "skills", "easy-stock-data"),
             os.path.join(root, "skills", "easy-stock-data"))
    os.makedirs(os.path.join(root, "docs"))
    for f in os.listdir(DOCS):
        if f.endswith(".md"):
            shutil.copy2(os.path.join(DOCS, f), os.path.join(root, "docs", f))
    shutil.copy2(os.path.join(WS, "restore.sh"), root)

    with open(os.path.join(root, "MANIFEST.md"), "w") as f:
        f.write(f"# 发布包清单\n\n- 包名: {rel}.tar.gz\n- 打包日期: {DATE}\n"
                "- 内容: easy-stock-service(不含密钥)/easy-stock-web(不含node_modules)/"
                "easy-stock-analysis(不含node_modules/.git)/skills/docs/restore.sh/MANIFEST\n"
                "- 密钥(.env/push_config.json)不进包,见恢复文档\n")

    with tarfile.open(tgz_path, "w:gz") as tar:
        tar.add(root, arcname=rel)
    shutil.rmtree(stage)

    # 校验:无密钥残留
    with tarfile.open(tgz_path) as tar:
        names = tar.getnames()
    bad = [n for n in names if n.endswith("/.env") or n.endswith("push_config.json")]
    if bad:
        raise RuntimeError(f"发布包混入密钥文件: {bad}")
    return len(names)


def drive_cli(*args):
    r = sh(["hatch_gws_cli", "drive"] + list(args))
    if r.returncode != 0:
        raise RuntimeError(f"drive 命令失败: {' '.join(args[:3])} :: {r.stderr.strip()[:200]}")
    try:
        return json.loads(r.stdout)
    except json.JSONDecodeError:
        return {}


def drive_sync(release_tgz):
    """发布包上传 + 文档/restore.sh 原位更新 + 发布包只留最近3版"""
    up = drive_cli("+upload", release_tgz, "--parent", DRIVE_FOLDER)
    log(f"Drive 上传发布包: {up.get('name')}")
    for name, fid in DRIVE_DOC_IDS.items():
        p = os.path.join(DOCS, name)
        if os.path.exists(p):
            drive_cli("files", "update", "--params",
                      json.dumps({"fileId": fid}), "--upload", p)
            log(f"Drive 更新文档: {name}")
    rp = os.path.join(WS, "restore.sh")
    if os.path.exists(rp):
        drive_cli("files", "update", "--params",
                  json.dumps({"fileId": DRIVE_RESTORE_ID}), "--upload", rp)
        log("Drive 更新 restore.sh")
    # 修剪旧发布包
    q = f"'{DRIVE_FOLDER}' in parents and trashed=false"
    lst = drive_cli("files", "list", "--params",
                    json.dumps({"q": q, "pageSize": 50,
                                "fields": "files(id,name)"}))
    rels = sorted([f for f in lst.get("files", [])
                   if f["name"].startswith("easy-stock-release-")
                   and f["name"].endswith(".tar.gz")],
                  key=lambda x: x["name"])
    for f in rels[:-KEEP]:
        drive_cli("files", "update", "--params", json.dumps({"fileId": f["id"]}),
                  "--json", json.dumps({"trashed": True}))
        log(f"Drive 移入回收站: {f['name']}")


def notify(title, content):
    import push
    push.send(title, content, only="wecom")
    log("企业微信通知已发送")


def main():
    t0 = datetime.datetime.now()
    os.makedirs(BACKUPS, exist_ok=True)
    release_tgz = os.path.join(WS, f"easy-stock-release-{DATE}.tar.gz")
    full_tgz = os.path.join(BACKUPS, f"easy-stock-full-{DATE}.tar.gz")
    keys_tgz = os.path.join(BACKUPS, f"easy-stock-keys-{DATE}.tar.gz")

    # 1. 发布包(Drive 用,不含密钥)
    n_files = build_release(release_tgz)
    log(f"发布包就绪: {os.path.basename(release_tgz)} ({n_files} 文件)")
    prune([os.path.join(WS, f) for f in os.listdir(WS)
           if f.startswith("easy-stock-release-") and f.endswith(".tar.gz")])

    # 2. 本地全量快照(含密钥,仅本机)
    r = sh(["tar", "-czf", full_tgz, "-C", WS, "easy-stock-service", "easy-stock-web"])
    if r.returncode != 0:
        raise RuntimeError("本地全量快照打包失败")
    log(f"本地快照就绪: {os.path.basename(full_tgz)}")
    prune([os.path.join(BACKUPS, f) for f in os.listdir(BACKUPS)
           if f.startswith("easy-stock-full-") and f.endswith(".tar.gz")])

    # 3. 密钥包刷新(仅本机)
    r = sh(["tar", "-czf", keys_tgz, "-C", SVC, ".env", "push_config.json"])
    if r.returncode != 0:
        raise RuntimeError("密钥包打包失败")
    os.chmod(keys_tgz, 0o600)
    prune([os.path.join(BACKUPS, f) for f in os.listdir(BACKUPS)
           if f.startswith("easy-stock-keys-") and f.endswith(".tar.gz")])

    # 4. Drive 同步
    drive_sync(release_tgz)

    mins = (datetime.datetime.now() - t0).total_seconds() / 60
    size_m = os.path.getsize(release_tgz) / 1024 / 1024
    notify(f"💾 备份同步完成 {DATE}",
           f"发布包 easy-stock-release-{DATE}.tar.gz({size_m:.0f}M,{n_files}文件)已同步到 Drive「easy-stock-发布包」(只留最近{KEEP}版)\n"
           f"三文档与 restore.sh 已原位更新;本地全量快照与密钥包已刷新(密钥仅本机,不进 Drive)\n"
           f"耗时 {mins:.1f} 分钟")
    log("全部完成")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:  # noqa: BLE001
        log(f"失败: {e}")
        try:
            notify("🚨 备份同步失败", f"{DATE} 自动备份失败: {e}\n请检查后手动处理")
        except Exception:  # noqa: BLE001
            pass
        sys.exit(1)
