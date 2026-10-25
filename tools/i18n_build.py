# -*- coding: utf-8 -*-
"""把 tools/i18n_en.json（中文原文 → 英文）注入 web/index.html 的 I18N_EN 字典。

用法：python tools/i18n_build.py
维护流程：加界面文案 → python tools/i18n_inventory.py 看新词 → 补进 tools/i18n_en.json → 跑本脚本 → 重新构建。
"""
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import io
import json
import os
import re

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HTML = os.path.join(ROOT, "web", "src", "app.js")   # 前端已拆分：字典在源文件里，index.html 由 web_build.py 生成
TABLE = os.path.join(ROOT, "tools", "i18n_en.json")


def main():
    table = json.loads(io.open(TABLE, encoding="utf-8").read())
    html = io.open(HTML, encoding="utf-8").read()
    lines = []
    for k in sorted(table):
        lines.append("  %s: %s," % (json.dumps(k, ensure_ascii=False), json.dumps(table[k], ensure_ascii=False)))
    body = "\n".join(lines)
    new_block = "const I18N_EN = {\n" + body + "\n};"
    # 注意：re.sub 的替换串会把 \n 之类当转义处理，必须用函数形式返回替换内容
    out, n = re.subn(r"const I18N_EN = \{[\s\S]*?\n\};", lambda m: new_block, html, count=1)
    if n != 1:
        raise SystemExit("未找到 I18N_EN 字典块（它应当在 web/src/app.js 里）")
    io.open(HTML, "w", encoding="utf-8", newline="\n").write(out)
    print("已注入 %d 条译文到 web/src/app.js（%d 字节）" % (len(table), len(out.encode())))
    # 前端已拆分：写完源文件必须立刻拼回 index.html，否则页面仍是旧的（这个坑很难查）
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    import web_build
    io.open(web_build.OUT, "w", encoding="utf-8", newline="\n").write(web_build.build())
    print("已同步重建 web/index.html")


if __name__ == "__main__":
    main()
