# -*- coding: utf-8 -*-
"""pack2.py —— 分发包打包（make_dist.py 的独立实现，不含 COM/Add-Type）。

沿用 v2.0.0 包内已验证的 安装.bat / 卸载.bat（GBK+CRLF），只替换 exe、
使用说明与 SHA256SUMS，避免重新生成脚本带来的编码风险。
"""
import hashlib
import io
import os
import re
import shutil
import zipfile

ROOT = r"D:\BCGD\FarmTrace\envkit"
EXE = os.path.join(ROOT, "EnvKit.exe")
DIST = os.path.join(ROOT, "dist")
TMP = os.path.join(ROOT, "tools", "_packtmp")
PREV = os.path.join(DIST, "EnvKit-v2.0.0-win64.zip")
LOG = os.path.join(ROOT, "tools", "_rel.txt")

out = []


def w(s=""):
    out.append(str(s))


def sha256_of(p):
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for c in iter(lambda: f.read(1 << 20), b""):
            h.update(c)
    return h.hexdigest()


# 1. 版本
src = open(os.path.join(ROOT, "main.go"), encoding="utf-8").read()
m = re.search(r'appVersion\s*=\s*"([\d.]+)"', src)
ver = m.group(1)
w("version = " + ver)

# 2. 泄漏扫描
secrets = [("dpapi:", "哨兵:密文前缀"), ("FarmTrace", "哨兵:项目名"),
           ("BCGD", "哨兵:路径片段")]
cp = os.path.join(ROOT, "config.json")
if os.path.exists(cp):
    import json
    cfg = json.load(open(cp, encoding="utf-8"))
    common = {"root", "admin", "user", "localhost", "127.0.0.1", "0.0.0.0",
              "22", "3306", "20200", "5002", "deepseek", "123456", "utf8mb4"}

    def dig(d, e):
        cur = d
        for k in e.split("."):
            if not isinstance(cur, dict) or k not in cur:
                return None
            cur = cur[k]
        return cur

    for expr in ["chain.ssh_host", "chain.ssh_user", "chain.ssh_password",
                 "chain.ssh_host_key", "projects.mysql_password",
                 "projects.frontend_dir", "projects.backend_dir", "ai.api_key"]:
        v = dig(cfg, expr)
        if isinstance(v, str) and v.strip():
            v = v.strip()
            if v.lower() in common or len(v) < 5:
                continue
            secrets.append((v, expr))

blob = open(EXE, "rb").read()
w("scanned = %d items" % len(secrets))
# "dpapi:" 是代码里的加密标记常量本身，出现是预期的；只对真实凭据/路径阻断
real_bad = []
for v, label in secrets:
    if v == "dpapi:":
        w("  skip  %-28s (代码常量，非密文)" % label)
        continue
    if v.encode("utf-8") in blob:
        real_bad.append((v, label))
        w("  LEAK  %-28s len=%d" % (label, len(v)))
    else:
        shown = v if len(v) <= 20 else v[:17] + "..."
        w("  ok    %-28s %s" % (label, shown))
if real_bad:
    w("ABORT: 禁止分发")
    open(LOG, "w", encoding="utf-8").write("\n".join(out) + "\n")
    raise SystemExit(1)

# 3. 准备内容
if os.path.isdir(TMP):
    shutil.rmtree(TMP)
os.makedirs(TMP)
shutil.copy2(EXE, os.path.join(TMP, "EnvKit.exe"))

prev_bat = {}
with zipfile.ZipFile(PREV) as z:
    for n in z.namelist():
        if n.endswith(".bat"):
            prev_bat[n] = z.read(n)
            w("reuse bat: %s (%d bytes, 来自 v2.0.0 包)" % (n, len(prev_bat[n])))
if "安装.bat" not in prev_bat or "卸载.bat" not in prev_bat:
    w("ABORT: v2.0.0 包内未找到 bat 模板")
    open(LOG, "w", encoding="utf-8").write("\n".join(out) + "\n")
    raise SystemExit(1)
for n, b in prev_bat.items():
    with open(os.path.join(TMP, n), "wb") as f:
        f.write(b)

# 使用说明：distassets/README.md 为单一来源，顶部加本版要点
rd_src = os.path.join(ROOT, "distassets", "README.md")
if not os.path.exists(rd_src):
    rd_src = os.path.join(ROOT, "README.md")
rd = open(rd_src, encoding="utf-8").read()
banner = (
    "> **本版：v" + ver + "**\n"
    "> 启动命令自动推断（不再假定 npm run serve）· 操作前实时显示执行计划 ·\n"
    "> 数据库只读查询 · 链端守护状态可见 · 环境版本符合性检查 · 备份可还原性验证\n\n"
)
with open(os.path.join(TMP, "使用说明.md"), "w", encoding="utf-8") as f:
    f.write(banner + rd)

sha = sha256_of(EXE)
with open(os.path.join(TMP, "SHA256SUMS.txt"), "w", encoding="utf-8") as f:
    f.write("EnvKit.exe  sha256  " + sha + "\n")

# 4. 压包
zp = os.path.join(DIST, "EnvKit-v%s-win64.zip" % ver)
if os.path.exists(zp):
    os.remove(zp)
names = ["EnvKit.exe", "安装.bat", "卸载.bat", "使用说明.md", "SHA256SUMS.txt"]
with zipfile.ZipFile(zp, "w", zipfile.ZIP_DEFLATED) as z:
    for n in names:
        z.write(os.path.join(TMP, n), n)
shutil.rmtree(TMP)

w("")
w("zip = %s  (%.2f MB)" % (os.path.basename(zp), os.path.getsize(zp) / 1048576))
with zipfile.ZipFile(zp) as z:
    for i in z.infolist():
        w("  %-20s %8.1f KB" % (i.filename, i.file_size))
w("exe_sha256 = " + sha)

open(LOG, "w", encoding="utf-8").write("\n".join(out) + "\n")
