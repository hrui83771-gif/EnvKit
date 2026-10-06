# -*- coding: utf-8 -*-
"""单独验证 /api/ai/explain：不应触发工具预检（带连接重置重试，规避本机对 python 的干扰）。"""
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import io, json, os, re, subprocess, time, urllib.request

EK = r"D:\BCGD\FarmTrace\envkit"
BASE = "http://127.0.0.1:18765"
LOG = os.path.join(EK, "envkit-%s.log" % time.strftime("%Y%m%d"))

def loglines():
    return io.open(LOG, encoding="utf-8", errors="ignore").read().splitlines()

subprocess.run(["taskkill", "/F", "/IM", "EnvKit.exe"], capture_output=True)
subprocess.Popen([os.path.join(EK, "EnvKit.exe")], cwd=EK,
                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=0x00000008)
T = None
for _ in range(30):
    try:
        h = urllib.request.urlopen(BASE + "/", timeout=5).read().decode("utf-8", "ignore")
        T = re.search(r'__EK_TOKEN__\s*=\s*"([^"]+)"', h).group(1); break
    except Exception:
        time.sleep(1)
time.sleep(5)

before = len(loglines())
ex, buf, err = "", "", None
for attempt in range(3):
    try:
        r = urllib.request.Request(BASE + "/api/ai/explain", data=b"{}",
                                   headers={"X-EnvKit-Token": T, "Content-Type": "application/json"})
        resp = urllib.request.urlopen(r, timeout=180)
        ex, buf = "", ""
        while True:
            ch = resp.read(1)
            if not ch:
                break
            buf += ch.decode("utf-8", "replace")
            while "\n\n" in buf:
                line, buf = buf.split("\n\n", 1)
                line = line.strip()
                if line.startswith("data: "):
                    ev = json.loads(line[6:])
                    if ev.get("type") == "delta":
                        ex += ev.get("text", "")
        err = None
        break
    except Exception as e:
        err = e
        time.sleep(3)
        before = len(loglines())

hits = [l for l in loglines()[before:] if "预检命中" in l]
tools = [l for l in loglines()[before:] if "tool_result" in l]
print("请求异常:", err)
print("解读输出长度:", len(ex))
print("预检命中行:", hits)
print("判断:", "PASS 解读未触发预检且输出有效" if (not err and not hits and len(ex) > 100) else "FAIL")
print("摘取:", ex[:120])
subprocess.run(["taskkill", "/F", "/IM", "EnvKit.exe"], capture_output=True)
