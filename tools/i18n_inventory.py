# -*- coding: utf-8 -*-
"""清点 index.html 里"面向界面"的中文文案（去重），并可与 EN 字典比对覆盖率。

用法：
    python tools/i18n_inventory.py            # 列出待翻译文案（按出现次数）
    python tools/i18n_inventory.py --check    # 报告 EN 字典缺失/多余项

说明：JS 侧用带状态的扫描器提取字符串字面量（正确处理注释、转义、模板串与 ${} 表达式），
因此不会把代码片段误当成文案。含 ${} 的模板串单独归类，需要人工在调用点用 t() 包裹。
"""
import io
import os
import re
import sys
from collections import Counter

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HTML = os.path.join(ROOT, "web", "index.html")
CJK = re.compile(r"[\u4e00-\u9fff]")


def scan_js(src):
    """返回 (普通字符串字面量, 含 ${} 的模板串)"""
    plain, tpl = [], []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if c == "/" and i + 1 < n and src[i + 1] == "/":
            j = src.find("\n", i)
            i = n if j < 0 else j + 1
            continue
        if c == "/" and i + 1 < n and src[i + 1] == "*":
            j = src.find("*/", i + 2)
            i = n if j < 0 else j + 2
            continue
        if c in "'\"`":
            q, i, buf, dyn = c, i + 1, [], False
            while i < n:
                ch = src[i]
                if ch == "\\":
                    buf.append(src[i:i + 2]); i += 2; continue
                if ch == q:
                    i += 1; break
                if q == "`" and ch == "$" and i + 1 < n and src[i + 1] == "{":
                    depth, i, dyn = 1, i + 2, True
                    while i < n and depth > 0:
                        if src[i] == "{":
                            depth += 1
                        elif src[i] == "}":
                            depth -= 1
                        i += 1
                    buf.append("${…}")
                    continue
                buf.append(ch); i += 1
            s = "".join(buf)
            (tpl if dyn else plain).append(s)
            continue
        i += 1
    return plain, tpl


def extract(html):
    body = re.sub(r"<script>[\s\S]*?</script>", "", html)
    body = re.sub(r"<style>[\s\S]*?</style>", "", body)
    texts = [t.strip() for t in re.findall(r">([^<>]+)<", body)]
    attrs = [m.group(1).strip() for m in
             re.finditer(r'\b(?:placeholder|title|alt)="([^"]*)"', body)]
    scripts = re.findall(r"<script>([\s\S]*?)</script>", html)
    js = re.sub(r"data:image/png;base64,[A-Za-z0-9+/=]+", "", scripts[-1] if scripts else "")
    plain, tpl = scan_js(js)
    return texts, attrs, plain, tpl


def current_en_dict(html):
    m = re.search(r"const I18N_EN = \{([\s\S]*?)\n\};", html)
    if not m:
        return {}
    return {mm.group(1): mm.group(2) for mm in
            re.finditer(r'"((?:[^"\\]|\\.)*)"\s*:\s*"((?:[^"\\]|\\.)*)"', m.group(1))}


def main():
    html = io.open(HTML, encoding="utf-8").read()
    texts, attrs, plain, tpl = extract(html)
    groups = {
        "HTML文本": [t for t in texts if CJK.search(t)],
        "HTML属性": [a for a in attrs if CJK.search(a)],
        "JS字面量": [p for p in plain if CJK.search(p)],
    }
    allc = Counter(sum(groups.values(), []))
    uniq = sorted(allc)
    en = current_en_dict(html)
    missing = [s for s in uniq if s not in en]
    print("唯一文案 %d 条 / 出现 %d 处（HTML文本 %d 唯一、属性 %d、JS字面量 %d）" % (
        len(uniq), sum(allc.values()), len(set(groups["HTML文本"])),
        len(set(groups["HTML属性"])), len(set(groups["JS字面量"]))))
    print("含 ${} 的模板串（需在调用点用 t() 包裹）: %d 条" %
          len([x for x in tpl if CJK.search(x)]))
    print("EN 字典 %d 条，缺失 %d 条" % (len(en), len(missing)))
    if "--check" in sys.argv:
        for s in missing:
            print("  MISS", s)
        for k in sorted(set(en) - set(uniq)):
            print("  STALE", k)
        return
    for s in uniq:
        print("%2d× %s" % (allc[s], s))


if __name__ == "__main__":
    main()
