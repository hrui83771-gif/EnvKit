# -*- coding: utf-8 -*-
"""验证界面语言传给助手后，助手是否用同一种语言回答（lang=en/zh）。"""
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import codecs, io, json, re, subprocess, time, urllib.request

BASE = "http://127.0.0.1:18765"

def token():
    h = urllib.request.urlopen(BASE + "/", timeout=10).read().decode("utf-8", "ignore")
    return re.search(r'__EK_TOKEN__\s*=\s*"([^"]+)"', h).group(1)

def chat(msgs, lang, timeout=180):
    body = json.dumps({"messages": msgs, "lang": lang}).encode()
    req = urllib.request.Request(BASE + "/api/ai/chat", data=body,
                                headers={"X-EnvKit-Token": TOKEN, "Content-Type": "application/json"})
    resp = urllib.request.urlopen(req, timeout=timeout)
    text, buf = "", ""
    dec = codecs.getincrementaldecoder("utf-8")("replace")   # 逐字节解码会把中文拆成乱码
    while True:
        ch = resp.read(1)
        if not ch:
            break
        buf += dec.decode(ch)
        while "\n\n" in buf:
            line, buf = buf.split("\n\n", 1)
            line = line.strip()
            if line.startswith("data: "):
                ev = json.loads(line[6:])
                if ev.get("type") == "delta":
                    text += ev.get("text", "")
    return text

TOKEN = token()
for lang, label in (("en", "英文"), ("zh", "中文")):
    t = ""
    for _ in range(2):
        try:
            t = chat([{"role": "user", "content": "现在环境状态怎么样？Go 和 Node 的版本是多少？"}], lang)
            break
        except Exception as e:
            print("  重试（%s）：%s" % (lang, e)); time.sleep(2)
    cjk = len(re.findall(r"[\u4e00-\u9fff]", t))
    ok = (cjk < 10) if lang == "en" else (cjk > 50)
    print("%s lang=%s：回答 %d 字，其中中文 %d 字 → %s" % (
        "PASS" if ok else "FAIL", lang, len(t), cjk, t[:90].replace("\n", " ")))
