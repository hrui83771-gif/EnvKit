# -*- coding: utf-8 -*-
"""把 web/src/ 拼回单文件 web/index.html（构建期动作，产物仍需提交）。

## 为什么是"拆开维护、构建时合回"

`go:embed web/index.html` 决定交付物必须是**单文件**（exe 里那份）。
但 50 万字节的单文件没法维护：改一行逻辑要在 8000 行里找，diff 全是噪声。
所以源码拆开维护，构建时拼回来——**产物不变，维护面变小**。

    web/src/index.tpl.html   HTML 骨架（含 <!--@css--> 与 <!--@appjs--> 两个占位符）
    web/src/app.css          全部样式
    web/src/js/*.js          前端逻辑，**按文件名排序后空串拼接**
    web/index.html           ← 本脚本生成，**不要直接改**

## js 分批规则（v3.7.2）

文件名以两位数字开头，**数字前缀就是拼接顺序**（`00-` 在前，`80-` 在后）。
这里刻意用「排序后全量拼接」而不是手写文件清单：
清单要与目录内容保持同步，迟早会漏；排序只有一个来源。

代价是**顺序即语义**：`const` 提到 `TDZ` 之外会直接报错，所以前缀不能随便改。
新增块时取一个落在正确位置的前缀（如 `45-`），不要用 `zz-` 往后扔。

## 为什么不是打包器

目标产物是**一个内联 <script>**，没有模块边界、没有 import。
任何打包器都会引入它的运行时/包装函数，产出与现在不逐字节一致——
而 `--check` 是这次拆分的唯一验收标准：拆完产物必须**完全不变**。

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
JSDIR = os.path.join(SRC, "js")
OUT = os.path.join(ROOT, "web", "index.html")

# 手机端页面（v3.6.2）：自包含单文件，没有拼接占位符，只是从 src 拷到 web/。
# 之所以也走这个脚本，是为了让 `--check` 能一并发现"改了 src 忘了同步产物"。
MOBILE = os.path.join(SRC, "mobile.html")
MOBILE_OUT = os.path.join(ROOT, "web", "mobile.html")

CSS_MARK = "<!--@css-->"
JS_MARK = "<!--@appjs-->"


def js_files():
    """按文件名排序返回 js 片段——数字前缀即拼接顺序。"""
    names = sorted(n for n in os.listdir(JSDIR) if n.endswith(".js"))
    if not names:
        raise SystemExit("web/src/js/ 里没有 .js 片段")
    return [os.path.join(JSDIR, n) for n in names]


def read_js():
    """空串拼接，且**不额外插入换行**：片段本身已带行尾换行，
    多插一个就会让产物与拆分前不再逐字节一致。"""
    return "".join(io.open(p, encoding="utf-8", newline="").read() for p in js_files())


def build():
    tpl = io.open(TPL, encoding="utf-8").read()
    css = io.open(CSS, encoding="utf-8").read()
    js = read_js()
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
        print("index.html / mobile.html 与 web/src 一致（%d + %d 字符，js %d 个片段）"
              % (len(out), len(mobile), len(js_files())))
        return 0
    io.open(OUT, "w", encoding="utf-8", newline="\n").write(out)
    io.open(MOBILE_OUT, "w", encoding="utf-8", newline="\n").write(mobile)
    print("已生成 web/index.html：%d 字符（css %d + js %d，%d 个片段）、web/mobile.html：%d 字符"
          % (len(out), len(io.open(CSS, encoding="utf-8").read()),
             len(read_js()), len(js_files()), len(mobile)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
