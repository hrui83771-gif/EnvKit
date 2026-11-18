"""把 web/docs.html 变成可静态托管的单页文档站。

桌面版的文档页有两个「只有服务端才有意义」的东西：
  1. 页头版本号占位 __APP_VERSION__，由 main.go 在响应时替换；
  2. 指向本地控制台的绝对链接 / 与 /#tour。
GitHub Pages 上没人做这两件事，直接上传会露出占位符、点了跳到域名根。
本脚本从同一份构建产物生成 _site/index.html，本地和 CI 都能跑，
保证在线版与 exe 内置版同源，不靠手工维护第二份文档。

跑法：
    python tools/docs_site.py --version v3.8.1 --out _site
    python tools/docs_site.py --check        # 只校验产物，不落盘
"""
import argparse
import io
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "web", "docs.html")
MAIN_GO = os.path.join(ROOT, "main.go")
RELEASES = "https://github.com/hrui83771-gif/EnvKit/releases"


def build_date():
    """从 main.go 读构建日期，确保站点版与 exe 内置版页头格式一致。

    exe 内置版由 main.go 替换成「appVersion · buildDate」；站点版先前只有
    tag 名，两处格式不一致。日期以 main.go 为单一事实源，避免两份硬编码漂移。
    读不到就返回空串（只显示版本号），不让发布因解析失败而中断。
    """
    try:
        text = io.open(MAIN_GO, encoding="utf-8").read()
        m = re.search(r'buildDate\s*=\s*"([0-9-]+)"', text)
        return m.group(1) if m else ""
    except OSError:
        return ""

CONSOLE_LINK = '<a class="hbtn primary" id="cta" href="/">返回控制台</a>'
RELEASE_LINK = ('<a class="hbtn primary" id="cta" href="' + RELEASES +
                '" target="_blank" rel="noopener">下载最新版 ↗</a>')
# 页头主按钮的文案由 setLang 每帧用 tv(T.backCta) 重写；把词条本身换掉，
# 切语言后就只会显示一种语言，而不是写死的「Download / 下载」并列。
BACK_SOURCE = "backCta:z('返回控制台','Back to console')"
BACK_SITE = "backCta:z('下载最新版 ↗','Download ↗')"
REPLAY_JS = ("document.getElementById('replay').textContent = "
             "lang === 'en' ? 'Replay the tour →' : '重新观看界面导览 →';")
RELEASE_JS = ("document.getElementById('replay').textContent = "
              "lang === 'en' ? 'Releases ↗' : '下载最新版 ↗';")


def fail(msg):
    print("FAIL:", msg)
    return 1


def build(version, strict=True):
    raw = io.open(SRC, encoding="utf-8").read()
    if "__APP_VERSION__" not in raw:
        return None, fail("源文件里没有版本占位符，可能已经在别的流程里被替换过")

    # 源码里的占位是 v__APP_VERSION__——前缀 v 已经在模板里，不能再来一个。
    # dispatch 传进来的可能是 v3.8.0 也可能是 main，统一成「一次 v」的显示形式。
    shown = version if version.startswith("v") else "v" + version
    # 与 exe 内置版同格式：v<版本> · <构建日期>。日期取 main.go，读不到则只显示版本。
    date = build_date()
    if date:
        shown = shown + " · " + date
    out = raw.replace("v__APP_VERSION__", shown).replace("__APP_VERSION__", shown)
    if CONSOLE_LINK not in out:
        return None, fail("找不到「返回控制台」链接，站点版会指向域名根")
    out = out.replace(CONSOLE_LINK, RELEASE_LINK)
    if BACK_SOURCE not in out:
        return None, fail("找不到主按钮词条 backCta，站点版会一直显示「返回控制台」")
    out = out.replace(BACK_SOURCE, BACK_SITE)
    if REPLAY_JS not in out:
        return None, fail("找不到 replay 文案赋值，切语言后会把链接文字改回导览")
    out = out.replace(REPLAY_JS, RELEASE_JS)
    # 源码里这条链接的 href 是 /#tour；脚本里同一串出现在选择器里，不能整串替换。
    out = out.replace('<a href="/#tour" id="replay">',
                      '<a href="' + RELEASES + '" id="replay" target="_blank" rel="noopener">')

    checks = [
        ("version-replaced", "__APP_VERSION__" not in out),
        ("version-not-doubled", 'id="ver">vv' not in out),
        ("no-console-root", 'href="/"' not in out),
        ("no-tour-link", 'href="/#tour"' not in out),
        ("no-external-asset", not re.findall(
            r'<(?:script|link)\b[^>]*\b(?:src|href)="https?://[^"]+"', out)),
        ("sections>=21", out.count("{id:'") >= 21),
        ("release-link", RELEASES in out),
        ("cta-single-lang", 'Download / 下载' not in out and BACK_SITE in out),
        ("theme-toggle", 'id="theme"' in out and "ek-theme" in out),
    ]
    bad = [name for name, ok in checks if not ok]
    for name, ok in checks:
        print(("OK  " if ok else "BAD ") + name)
    if bad and strict:
        return None, fail("校验未通过：" + ", ".join(bad))
    return out, 0


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--version", default=os.environ.get("GITHUB_REF_NAME", "dev"),
                    help="显示在页头的版本号，CI 里取 tag 名")
    ap.add_argument("--out", default="_site", help="输出目录")
    ap.add_argument("--check", action="store_true", help="只校验，不写文件")
    a = ap.parse_args()

    out, rc = build(a.version, strict=True)
    if rc:
        return rc
    if a.check:
        print("只校验模式：未写文件")
        return 0

    dst = os.path.join(ROOT, a.out)
    if not os.path.isdir(dst):
        os.makedirs(dst)
    io.open(os.path.join(dst, "index.html"), "w", encoding="utf-8", newline="\n").write(out)
    io.open(os.path.join(dst, ".nojekyll"), "w", encoding="utf-8", newline="\n").write("")
    print("已生成 %s/index.html（%d 字符，版本 %s）" % (a.out, len(out), a.version))
    return 0


if __name__ == "__main__":
    sys.exit(main())
