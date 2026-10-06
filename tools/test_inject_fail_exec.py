# -*- coding: utf-8 -*-
"""聚焦验证：注入 5 次额度，看统一重试链是否"注入失败 → 自动重试 → 真实执行成功"。"""
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import os, re, io, time, subprocess, urllib.request

EK = r"D:\BCGD\FarmTrace\envkit"
LOG = os.path.join(EK, "envkit-%s.log" % time.strftime("%Y%m%d"))
BASE = "http://127.0.0.1:18765"

def kill():
    subprocess.run(["taskkill", "/F", "/IM", "EnvKit.exe"], capture_output=True)

def lines_from(n):
    with io.open(LOG, encoding="utf-8", errors="ignore") as f:
        return f.read().splitlines()[n:]

kill()
before = len(io.open(LOG, encoding="utf-8", errors="ignore").read().splitlines()) if os.path.exists(LOG) else 0
env = dict(os.environ); env["ENVKIT_INJECT_FAIL"] = "4"
subprocess.Popen([os.path.join(EK, "EnvKit.exe")], cwd=EK, env=env,
                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=0x00000008)
t = None
for _ in range(30):
    try:
        h = urllib.request.urlopen(BASE + "/", timeout=5).read().decode("utf-8", "ignore")
        t = re.search(r'__EK_TOKEN__\s*=\s*"([^"]+)"', h).group(1); break
    except Exception:
        time.sleep(1)
assert t, "EnvKit 未起来"

def post(path):
    r = urllib.request.Request(BASE + path, data=b"{}", headers={"X-EnvKit-Token": t})
    return urllib.request.urlopen(r, timeout=60).read().decode("utf-8", "ignore")

post("/api/program/db-test"); time.sleep(18)

new = lines_from(before)
print("新日志里与注入/重试有关的行：")
for ln in new:
    if any(k in ln for k in ("注入", "重试", "包装", "连接", "mysql")):
        print("  ", ln)

print("\n带 token 的 db-test 结果：")
print("  /api/state:", post("/api/state")[:50])
kill()
print("完成（测试实例已关闭）")
