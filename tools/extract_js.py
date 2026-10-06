# -*- coding: utf-8 -*-
"""抽取 web/index.html 的内联 <script> 块到临时 js 文件，供 node --check 语法校验。"""
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import io, os, re, sys

HTML = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "web", "index.html")
html = io.open(HTML, encoding="utf-8").read()
blocks = re.findall(r"<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)</script>", html)
outdir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "_jscheck")
if os.path.isdir(outdir):
    for f in os.listdir(outdir):
        os.remove(os.path.join(outdir, f))
else:
    os.makedirs(outdir)
for i, b in enumerate(blocks):
    b = b.replace("__TOKEN__", "x").replace("__APP_VERSION__", "v")
    io.open(os.path.join(outdir, "block%d.js" % i), "w", encoding="utf-8").write(b)
print("extracted %d inline script blocks -> %s" % (len(blocks), outdir))
