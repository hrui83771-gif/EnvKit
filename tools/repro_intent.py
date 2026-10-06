# -*- coding: utf-8 -*-
"""repro_intent.py —— 对运行中的 EnvKit 实例复现「启动」消息，打印 SSE 事件流。"""
import json
import re
import sys

# **stdout 显式设 UTF-8。**
#
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 `print('  ⚠ …')` 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
# recovery_sandbox.py 实测踩过：评测一行都没跑就崩在这里。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import urllib.request

# 本机调试：强制直连，绕过系统/环境代理（本机 Clash 等会劫持 127.0.0.1 请求返回 502）
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
urllib.request.install_opener(opener)

BASE = "http://127.0.0.1:18765"

html = opener.open(BASE + "/", timeout=10).read().decode("utf-8")
m = re.search(r'__EK_TOKEN__="([^"]+)"', html)
if not m:
    print("FAIL: token not found")
    sys.exit(1)
token = m.group(1)

msgs = [{"role": "user", "content": "启动"}]
req = urllib.request.Request(
    BASE + "/api/ai/chat",
    data=json.dumps({"messages": msgs}).encode("utf-8"),
    headers={"Content-Type": "application/json", "X-EnvKit-Token": token},
    method="POST",
)
with opener.open(req, timeout=120) as resp:
    for raw in resp:
        line = raw.decode("utf-8", "replace").strip()
        if not line.startswith("data: "):
            if line:
                print("RAW:", line)
            continue
        try:
            ev = json.loads(line[6:])
        except Exception:
            print("RAW:", line[:200])
            continue
        t = ev.get("type")
        if t == "delta":
            print("DELTA:", ev.get("text", "")[:80])
        elif t == "confirm_request":
            print("CONFIRM_REQUEST: tool=%s args=%s reason=%s" % (ev.get("tool"), ev.get("args"), ev.get("reason")))
        elif t == "done":
            print("DONE: reason=%s" % ev.get("reason"))
        elif t == "error":
            print("ERROR:", ev.get("text", "")[:200])
        else:
            print("EV:", t, str(ev)[:120])
