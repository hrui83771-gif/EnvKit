# -*- coding: utf-8 -*-
"""把 web/src/ 拼回单文件 web/index.html（构建期动作，产物仍需提交）。

## 为什么是"拆开维护、构建时合回"

`go:embed web/index.html` 决定交付物必须是**单文件**（exe 里那份）。
但 68 万字节的单文件没法维护：改一行 CSS 要在 6000 行里找，diff 全是噪声。
所以源码拆成三块，构建时拼回来——**产物不变，维护面变小**。

    web/src/index.tpl.html   HTML 骨架（含 <!--@css--> 与 <!--@appjs--> 两个占位符）
    web/src/app.css          全部样式
    web/src/app.js           全部前端逻辑（含 I18N_EN 字典，由 tools/i18n_build.py 维护）
    web/index.html           ← 本脚本生成，**不要直接改**

用法：
    python tools/web_build.py            # 拼接
    python tools/web_build.py --check    # 只校验产物是否与源一致（CI/提交前用）
"""
import io
import os
import sys

try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "web", "src")
TPL = os.path.join(SRC, "index.tpl.html")
CSS = os.path.join(SRC, "app.css")
JS = os.path.join(SRC, "app.js")
OUT = os.path.join(ROOT, "web", "index.html")

# 手机端页面（v3.6.2）：自包含单文件，没有拼接占位符，只是从 src 拷到 web/。
# 之所以也走这个脚本，是为了让 `--check` 能一并发现"改了 src 忘了同步产物"。
MOBILE = os.path.join(SRC, "mobile.html")
MOBILE_OUT = os.path.join(ROOT, "web", "mobile.html")

CSS_MARK = "<!--@css-->"
JS_MARK = "<!--@appjs-->"


def build():
    tpl = io.open(TPL, encoding="utf-8").read()
    css = io.open(CSS, encoding="utf-8").read()
    js = io.open(JS, encoding="utf-8").read()
    if tpl.count(CSS_MARK) != 1 or tpl.count(JS_MARK) != 1:
        raise SystemExit("模板里的占位符数量不对：css=%d js=%d"
                         % (tpl.count(CSS_MARK), tpl.count(JS_MARK)))
    return tpl.replace(CSS_MARK, css, 1).replace(JS_MARK, js, 1)


def main():
    out = build()
    mobile = io.open(MOBILE, encoding="utf-8").read()
    if "--check" in sys.argv:
        cur = io.open(OUT, encoding="utf-8").read() if os.path.exists(OUT) else ""
        if cur != out:
            print("index.html 与 web/src 不一致——请运行 python tools/web_build.py")
            return 1
        curm = io.open(MOBILE_OUT, encoding="utf-8").read() if os.path.exists(MOBILE_OUT) else ""
        if curm != mobile:
            print("mobile.html 与 web/src 不一致——请运行 python tools/web_build.py")
            return 1
        print("index.html / mobile.html 与 web/src 一致（%d + %d 字符）"
              % (len(out), len(mobile)))
        return 0
    io.open(OUT, "w", encoding="utf-8", newline="\n").write(out)
    io.open(MOBILE_OUT, "w", encoding="utf-8", newline="\n").write(mobile)
    print("已生成 web/index.html：%d 字符（css %d + js %d）、web/mobile.html：%d 字符"
          % (len(out), len(io.open(CSS, encoding="utf-8").read()),
             len(io.open(JS, encoding="utf-8").read()), len(mobile)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
