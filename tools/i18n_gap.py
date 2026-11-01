# -*- coding: utf-8 -*-
"""精确统计「界面文案」的英文缺口 —— 只认真正的用户可见文案，不把代码当文案。

为什么另起一个：`i18n_inventory.py --check` 报「缺失 320 条」，其中大量是
JS 注释与代码片段（MISS 列表里能直接看到 `if(ev.usage){`）——
按那个数字补翻译会白干，而"补了 71 条"和"补了 320 条"是两件不同的事。

本脚本只认三种**用户真的会看到**的中文：

    ① `t('…')` / `t("…")` 调用里的字面量（前端运行时翻译）
    ② index.tpl.html 里 `>文本<` 之间的中文（构建期/运行时替换）
    ③ index.tpl.html 里的 `placeholder=` / `title=` / `aria-label=` 值

用法：
    python tools/i18n_gap.py            # 列出缺口（按出现次数降序）
    python tools/i18n_gap.py --check    # 有缺口就 rc=1（CI/提交前用）
    python tools/i18n_gap.py --json     # 输出 {待翻译中文: 出现次数}，便于补词表
"""
import collections
import io
import json
import os
import re
import sys

try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
JS = os.path.join(ROOT, "web", "src", "app.js")
TPL = os.path.join(ROOT, "web", "src", "index.tpl.html")
TABLE = os.path.join(ROOT, "tools", "i18n_en.json")

CJK = re.compile(r"[\u4e00-\u9fa5]")


def strip_comments(src):
    """去掉 // 行注释与 /* */ 块注释。

    只做这一件事，但必须做：注释里的中文不是文案，而 `t('…')` 的正则
    本身不会跨注释匹配，所以这里是为了让 ①②③ 的命中不掺注释正文。
    字符串里的 `//`（如 http://）会被误伤 —— 所以按字符状态机走，不按行切。
    """
    out = []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if c in "'\"`":
            q = c
            out.append(c)
            i += 1
            while i < n:
                if src[i] == "\\":
                    out.append(src[i:i + 2])
                    i += 2
                    continue
                out.append(src[i])
                if src[i] == q:
                    i += 1
                    break
                i += 1
            continue
        if c == "/" and i + 1 < n and src[i + 1] == "/":
            while i < n and src[i] != "\n":
                i += 1
            continue
        if c == "/" and i + 1 < n and src[i + 1] == "*":
            j = src.find("*/", i + 2)
            i = n if j < 0 else j + 2
            continue
        out.append(c)
        i += 1
    return "".join(out)


def unescape_js(s):
    """把 JS 字面量里的转义还原成**运行时的真实值**。

    不还原就会误报：`t('a\\nb')` 在源码里是反斜杠 + n，字典里存的是真换行，
    两边永远对不上 —— 那个 KEY 明明有译文，却会被算成缺失。
    """
    return re.sub(r"\\(u[0-9a-fA-F]{4}|x[0-9a-fA-F]{2}|.)",
                  lambda m: m.group(1) if m.group(1)[0] in "ux" and len(m.group(1)) > 1
                  else {"n": "\n", "t": "\t", "r": "\r", "0": "\0"}.get(m.group(1), m.group(1)),
                  s)


def js_keys():
    src = strip_comments(io.open(JS, encoding="utf-8").read())
    found = re.findall(r"\bt\(\s*'((?:[^'\\\n]|\\.)*)'", src)
    found += re.findall(r'\bt\(\s*"((?:[^"\\\n]|\\.)*)"', src)
    return [unescape_js(s) for s in found if CJK.search(s)]


def tpl_keys():
    src = io.open(TPL, encoding="utf-8").read()
    # <!-- --> 与 {/* */} 不是文案
    src = re.sub(r"<!--[\s\S]*?-->", " ", src)
    keys = []
    # ② 标签之间的文本
    for m in re.finditer(r">([^<>{}]+)<", src):
        s = m.group(1).strip()
        if s and CJK.search(s):
            keys.append(s)
    # ③ 属性值
    for m in re.finditer(r'\b(?:placeholder|title|aria-label)\s*=\s*"([^"]*)"', src):
        s = m.group(1).strip()
        if s and CJK.search(s):
            keys.append(s)
    return keys


def main():
    table = json.loads(io.open(TABLE, encoding="utf-8").read())
    have = {k.strip() for k in table}

    js = js_keys()
    tp = tpl_keys()
    cnt = collections.Counter([s.strip() for s in js + tp])
    missing = {k: v for k, v in cnt.items() if k not in have}

    if "--json" in sys.argv:
        print(json.dumps(missing, ensure_ascii=False, indent=1))
        return 0

    print("界面文案（去重后）: %d 条 —— 其中 JS t() %d 处 / 模板 %d 处"
          % (len(cnt), len(js), len(tp)))
    print("EN 字典 %d 条，缺口 %d 条" % (len(have), len(missing)))
    if "--check" in sys.argv:
        if missing:
            for k, v in sorted(missing.items(), key=lambda x: (-x[1], x[0])):
                print("  MISS %s  %s" % (v, k.replace("\n", "\\n")[:120]))
            return 1
        return 0
    for k, v in sorted(missing.items(), key=lambda x: (-x[1], x[0])):
        print("  %2d  %s" % (v, k.replace("\n", "\\n")[:140]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
