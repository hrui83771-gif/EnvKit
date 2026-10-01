# -*- coding: utf-8 -*-
"""repro_proxy.py —— 验证沙箱代理剥 body 问题与回退头兜底。"""
import json
import re
import sys
import urllib.parse
import urllib.request

BASE = "http://127.0.0.1:18765"
PROXY = "http://127.0.0.1:34986"

direct = urllib.request.build_opener(urllib.request.ProxyHandler({}))
viapro = urllib.request.build_opener(urllib.request.ProxyHandler({"http": PROXY, "https": PROXY}))


def chat(opener, tag, empty_body=False, with_fallback=False):
    html = opener.open(BASE + "/", timeout=10).read().decode("utf-8")
    m = re.search(r'__EK_TOKEN__="([^"]+)"', html)
    if not m:
        print(tag, "FAIL: no token")
        return
    payload = {} if empty_body else {"messages": [{"role": "user", "content": "启动"}]}
    headers = {"Content-Type": "application/json", "X-EnvKit-Token": m.group(1)}
    if with_fallback:
        fb = json.dumps({"last": "启动", "confirm": None, "lang": "zh"})
        headers["X-EnvKit-Chat-Fallback"] = urllib.parse.quote(fb)
    req = urllib.request.Request(BASE + "/api/ai/chat", data=json.dumps(payload).encode("utf-8"),
                                 headers=headers, method="POST")
    try:
        with opener.open(req, timeout=60) as resp:
            for raw in resp:
                line = raw.decode("utf-8", "replace").strip()
                if line.startswith("data: "):
                    ev = json.loads(line[6:])
                    t = ev.get("type")
                    if t == "confirm_request":
                        print(tag, "-> CONFIRM_REQUEST tool=%s args=%s (预检生效)" % (ev.get("tool"), ev.get("args")))
                        return
                    if t == "done":
                        print(tag, "-> done reason=%s (未触发预检！)" % ev.get("reason"))
                        return
                    if t == "error":
                        print(tag, "-> ERROR:", ev.get("text", "")[:100])
                        return
    except Exception as e:
        print(tag, "-> EXC:", e)


which = sys.argv[1] if len(sys.argv) > 1 else "all"
if which in ("direct", "all"):
    chat(direct, "[DIRECT]")
if which in ("proxy", "all"):
    chat(viapro, "[VIA-PROXY]")
if which in ("fallback", "all"):
    # 空 body + 回退头：模拟 body 被代理剥掉，服务端应恢复「启动」并触发预检
    chat(direct, "[EMPTY-BODY+FB]", empty_body=True, with_fallback=True)
    # 正常 body + 回退头走代理：无论 body 是否被剥都应触发预检
    chat(viapro, "[PROXY+FB]", with_fallback=True)
