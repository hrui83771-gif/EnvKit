# -*- coding: utf-8 -*-
"""brand_icons_build.py —— 把 tools/brand-icons/*.svg 内联进 web/index.html。

为什么内联而不是独立文件 + 路由：main.go 只 `go:embed web/index.html`
（外加 xterm 三个文件），独立图标文件要么加 embed 要么加静态路由，
为一个下拉框图标犯不上；内联进 JS 对象，一次渲染、零请求、离线可用。

源文件说明（2026-10-23 抓取）：
  deepseek.svg / gemini.svg / kimi.svg  ← cdn.simpleicons.org（官方商标单路径）
  openai.svg                            ← Wikimedia Commons「OpenAI logo 2025 (symbol)」
  grok.svg                              ← Wikimedia Commons「XAI Logo.svg」
    （Grok 现行品牌标即 xAI 标；simple-icons 无 openai/xai（下架/未收录），
     Wikimedia 是官方商标的可靠镜像）
  GLM/智谱：官方矢量四处都没拿到（CDN 404、favicon 反爬、Commons 只有位图），
  前端用品牌色徽章 + Z 字母兜底，不走本脚本——见 index.html 里 picon-z。

用法：
    python tools/brand_icons_build.py
幂等：每次整体替换 /*BRAND-ICONS:BEGIN*/ ... /*BRAND-ICONS:END*/ 之间的内容。
"""
import io
import os
import re
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8")
except Exception:
    pass

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "tools", "brand-icons")
HTML = os.path.join(ROOT, "web", "index.html")

BEGIN = "/*BRAND-ICONS:BEGIN*/"
END = "/*BRAND-ICONS:END*/"

# monochrome=True 的图标用 currentColor（随主题文字色自适应）；
# 品牌色图标保留原色（深色主题下品牌色依然可辨）。
MONO = {"openai", "kimi", "grok"}

# grok（xAI 标）原图 viewBox 是整张画布，mark 只占中间——裁到内容包围盒。
VIEWBOX_OVERRIDE = {"grok": "201.61 56.91 438.67 481.45"}

SAFE_TAG = re.compile(r"<\s*(/?)\s*(svg|path|polygon|polyline|g|rect|circle|title)\b", re.I)


def sanitize(inner: str) -> str:
    """只留白名单标签，剥掉注释/样式/script——内联进产品页面的东西必须过这道。"""
    if "<script" in inner.lower() or "<style" in inner.lower() or "<!--" in inner:
        raise ValueError("SVG 含不允许的元素（script/style/注释）")
    for m in re.finditer(r"<\s*(\w+)", inner):
        if not SAFE_TAG.match(m.group(0)):
            raise ValueError("SVG 含白名单外的标签: %r" % m.group(0))
    return inner


def load(name: str):
    p = os.path.join(SRC, name + ".svg")
    with io.open(p, encoding="utf-8") as f:
        raw = f.read()
    vb = re.search(r'viewBox="([^"]+)"', raw)
    if not vb:
        raise ValueError(name + ".svg 无 viewBox")
    viewbox = VIEWBOX_OVERRIDE.get(name, vb.group(1))
    m = re.search(r"<svg[^>]*>(.*)</svg>", raw, re.S)
    if not m:
        raise ValueError(name + ".svg 解析不出内容")
    inner = sanitize(m.group(1)).strip()
    # **必须折叠空白**：源 SVG 常带换行/缩进（xAI 标的 <g> 里就有 tab+换行），
    # 原样进单引号 JS 字符串 = 换行字面量 = 语法错误。
    # path/polygon 数据是空白分隔的，多空白折叠成单格不改变形状。
    inner = re.sub(r"\s+", " ", inner)
    fill = "currentColor" if name in MONO else ""
    if not fill:
        fm = re.search(r'fill="([^"]+)"', raw)
        fill = fm.group(1) if fm else "currentColor"
    return viewbox, inner, fill


def main():
    icons = {}
    for name in ("deepseek", "openai", "kimi", "grok", "gemini"):
        vb, inner, fill = load(name)
        icons[name] = {"vb": vb, "svg": inner, "fill": fill}
    # 自定义项：一支铅笔（Material 编辑图标，公有领域形态），与品牌图标同风格
    icons["custom"] = {
        "vb": "0 0 24 24",
        "svg": '<path d="M3 17.25V21h3.75L17.81 9.94l-3.75-3.75L3 17.25zM20.71 7.04a1 1 0 0 0 0-1.41l-2.34-2.34a1 1 0 0 0-1.41 0l-1.83 1.83 3.75 3.75 1.83-1.83z"/>',
        "fill": "currentColor",
    }

    parts = []
    for k, v in icons.items():
        svg_lit = v["svg"].replace("\\", "\\\\").replace("'", "\\'")
        parts.append("  %s:{vb:'%s',fill:'%s',svg:'%s'}" % (k, v["vb"], v["fill"], svg_lit))
    block = (
        "// 服务商官方品牌图标（内联 SVG，源文件 tools/brand-icons/*.svg，\n"
        "// 由 tools/brand_icons_build.py 注入——改图标改源文件后重跑脚本，勿手改这里）\n"
        + BEGIN + "\nconst AI_PROV_ICONS = {\n" + ",\n".join(parts) + "\n};\n" + END
    )

    with io.open(HTML, encoding="utf-8") as f:
        html = f.read()
    if BEGIN in html and END in html:
        pre = html[: html.index(BEGIN)]
        post = html[html.index(END) + len(END):]
        html = pre + block + post
        how = "replaced"
    else:
        anchor = "/* ---------- AI 助手 ---------- */"
        if anchor not in html:
            raise SystemExit("index.html 里既无旧标记也无锚点，不知道往哪插")
        html = html.replace(anchor, anchor + "\n" + block, 1)
        how = "inserted"

    with io.open(HTML, "w", encoding="utf-8", newline="") as f:
        f.write(html)
    print("OK %s: %d icons -> %s" % (how, len(icons), HTML))


if __name__ == "__main__":
    main()
