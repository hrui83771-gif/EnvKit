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
import html
import io
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "web", "docs.html")
MAIN_GO = os.path.join(ROOT, "main.go")
INDEX = os.path.join(ROOT, "web", "docs-index.json")
RELEASES = "https://github.com/hrui83771-gif/EnvKit/releases"
SITE_BASE = "https://hrui83771-gif.github.io/EnvKit"

# 分节页的正文结构（表格 / 代码 / 列表）只有文档解析器知道。索引 JSON 是给 AI
# 检索用的纯文本，拿它猜结构会把表格拉成「甲 · 乙 · 丙」——所以这里直接用
# 同一个解析器再跑一次（结构化模式），保证静态页与交互页同一份事实源。
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import docs_index  # noqa: E402


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


def shown_version(version):
    """页头显示的版本串：v<版本> · <构建日期>，与 exe 内置版同格式。

    源码占位是 v__APP_VERSION__——前缀 v 已在模板里，不能再拼一个。
    dispatch 传进来的可能是 v3.8.0 也可能是 main，统一成「一次 v」。
    """
    shown = version if version.startswith("v") else "v" + version
    date = build_date()
    return (shown + " · " + date) if date else shown


def build(version, strict=True):
    raw = io.open(SRC, encoding="utf-8").read()
    if "__APP_VERSION__" not in raw:
        return None, fail("源文件里没有版本占位符，可能已经在别的流程里被替换过")

    out = raw.replace("v__APP_VERSION__", shown_version(version)).replace(
        "__APP_VERSION__", shown_version(version))
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

    # 分节页是从 docs-index.json 生成的：文档新增/改名一节但忘了重跑
    # docs_index.py 时，站点会静默少一页、sitemap 也不会收录。这里直接
    # 拿 D 数组的节 id 顺序做对照，谁先漂就红在谁身上。
    src_ids = re.findall(r"\{id:'([a-z0-9_-]+)',\s*group:z", out)
    idx_ids = []
    try:
        doc = json.loads(io.open(INDEX, encoding="utf-8").read())
        idx_ids = [s.get("id") for s in (doc.get("sections") or [])]
    except (OSError, ValueError):
        idx_ids = []

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
        ("docs-index-in-sync", bool(src_ids) and src_ids == idx_ids),
    ]
    bad = [name for name, ok in checks if not ok]
    for name, ok in checks:
        print(("OK  " if ok else "BAD ") + name)
    if src_ids and src_ids != idx_ids:
        print("  索引与文档不一致：文档 %d 节 / 索引 %d 节；重跑 python tools/docs_index.py"
              % (len(src_ids), len(idx_ids)))
        miss = [x for x in src_ids if x not in idx_ids]
        extra = [x for x in idx_ids if x not in src_ids]
        if miss:
            print("  索引缺：" + ", ".join(miss))
        if extra:
            print("  索引多：" + ", ".join(extra))
    if bad and strict:
        return None, fail("校验未通过：" + ", ".join(bad))
    return out, 0


# 分节页的正文结构来自 docs_index 的结构化块，不再靠「按行首猜代码」。
# 早先那套猜测把表格拉成一串「甲 · 乙 · 丙」、把 FAQ 和列表混成一段散文，
# 静态页读起来像被人抽掉了排版。

PAGE_CSS = """
:root{--bg:#faf8f5;--paper:#fff;--ink:#2f3742;--sub:#4a5a6e;--muted:#7d8794;
  --line:#e3e7ee;--brand:#4a6fa5;--code:#f4f6fa}
@media(prefers-color-scheme:dark){
  :root{--bg:#161c26;--paper:#1f2733;--ink:#e4e9f0;--sub:#b6c2d1;--muted:#8d9aab;
    --line:#2f3a49;--brand:#7fa6d8;--code:#182130}
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);
  font:15px/1.75 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
.wrap{max-width:820px;margin:0 auto;padding:28px 22px 64px}
.top{display:flex;flex-wrap:wrap;gap:10px;align-items:center;
  font-size:13px;color:var(--muted);margin-bottom:22px}
.top a{color:var(--brand);text-decoration:none}
.top a:hover{text-decoration:underline}
.top .sp{flex:1}
.top button{font:inherit;font-size:12.5px;color:var(--sub);background:var(--paper);
  border:1px solid var(--line);border-radius:999px;padding:3px 12px;cursor:pointer}
h1{font-size:23px;line-height:1.4;margin:0 0 6px;letter-spacing:-.2px}
.group{font-size:12.5px;color:var(--muted);margin-bottom:20px}
h2{font-size:16px;margin:30px 0 10px;padding-top:4px}
p{margin:0 0 12px;color:var(--sub)}
pre{margin:0 0 14px;padding:12px 14px;background:var(--code);border:1px solid var(--line);
  border-radius:8px;overflow-x:auto}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:13px;color:var(--ink)}
.nav{display:flex;gap:10px;margin-top:40px;padding-top:18px;border-top:1px solid var(--line)}
.nav a{flex:1;padding:10px 14px;border:1px solid var(--line);border-radius:8px;
  background:var(--paper);color:var(--brand);text-decoration:none;font-size:13px}
.nav a:hover{border-color:var(--brand)}
.nav .none{flex:1;border:1px dashed var(--line);border-radius:8px;color:var(--muted);font-size:13px;padding:10px 14px}
.nav .rt{text-align:right}
footer{margin-top:26px;font-size:12px;color:var(--muted)}
/* 表格：窄屏自己横向滚动，不要把整页撑破 */
.tw{margin:0 0 14px;overflow-x:auto;border:1px solid var(--line);border-radius:8px}
table{border-collapse:collapse;width:100%;font-size:13.5px}
/* 窄屏下 width:100% 会把三列压成竖排单字，必须给个底线宽度让 .tw 去滚 */
th,td{text-align:left;padding:8px 11px;border-bottom:1px solid var(--line);vertical-align:top}
thead th{background:var(--code);font-weight:600;white-space:nowrap}
tbody tr:last-child td{border-bottom:0}
td{color:var(--sub)}
/* 提示条：warn / tip / ok 三档，只靠左侧色条区分，不铺大色块 */
.note{margin:0 0 14px;padding:10px 13px;border-left:3px solid var(--brand);
  background:var(--paper);border-radius:0 8px 8px 0;font-size:13.5px;color:var(--sub)}
.note.warn{border-left-color:#c98a3c}
.note.ok{border-left-color:#3f9a6a}
.codelab{font-size:11.5px;color:var(--muted);margin:0 0 -8px 2px;
  text-transform:uppercase;letter-spacing:.4px}
ul,ol{margin:0 0 14px;padding-left:22px;color:var(--sub)}
li{margin:0 0 6px}
ol.steps{counter-reset:st;list-style:none;padding-left:0}
ol.steps li{position:relative;padding-left:30px}
ol.steps li:before{counter-increment:st;content:counter(st);position:absolute;left:0;
  top:2px;width:20px;height:20px;border-radius:50%;background:var(--brand);color:var(--paper);
  font-size:11.5px;line-height:20px;text-align:center}
dl.faq{margin:0 0 14px}
dl.faq dt{font-weight:600;color:var(--ink);margin-top:12px}
dl.faq dd{margin:4px 0 0;color:var(--sub)}
.xlink{margin:18px 0 0;padding-top:12px;border-top:1px dashed var(--line);font-size:13px}
.xlink span{color:var(--muted);margin-right:8px}
.xlink a{color:var(--brand);text-decoration:none;margin-right:12px}
.xlink a:hover{text-decoration:underline}
/* 窄屏：表格给底线宽度、由 .tw 自己横向滚；整页不再跟着被撑破也不会把列压成竖排 */
@media(max-width:620px){
  .wrap{padding:20px 15px 56px}
  h1{font-size:20px}
  h2{font-size:15px}
  table{min-width:520px}
}
[hidden]{display:none!important}
"""

PAGE_JS = """
(function(){
  var want = (location.search.match(/[?&]lang=(\\w+)/) || [])[1];
  if(want !== 'en' && want !== 'zh'){ try{ want = localStorage.getItem('ek-lang'); }catch(e){} }
  var lang = want === 'en' ? 'en' : 'zh';
  function paint(){
    document.documentElement.lang = lang === 'en' ? 'en' : 'zh-CN';
    document.getElementById('b-zh').hidden = lang !== 'zh';
    document.getElementById('b-en').hidden = lang !== 'en';
    var b = document.getElementById('lang');
    b.textContent = lang === 'en' ? '中文' : 'EN';
    b.title = lang === 'en' ? 'Switch to Chinese' : 'Switch to English';
  }
  document.getElementById('lang').addEventListener('click', function(){
    lang = lang === 'en' ? 'zh' : 'en';
    try{ localStorage.setItem('ek-lang', lang); }catch(e){}
    paint();
  });
  paint();
})();
"""


def _esc(s):
    return html.escape(s or "", quote=True)


def _pick(node, lang):
    """双语取值：节点是 {"zh":..,"en":..}，缺哪边都退回另一边。"""
    if not isinstance(node, dict):
        return ""
    v = node.get(lang)
    if v is None or v == "":
        v = node.get("zh") or node.get("en") or ""
    return v


def _blocks_html(blocks, lang, sec_id):
    """结构化块 → HTML。表格 / 代码 / 列表 / FAQ 各自还原，不再压成散文。"""
    parts = []
    for b in blocks or []:
        t = b.get("t")
        if t == "p":
            txt = _pick(b, lang).strip()
            if txt:
                parts.append("<p>" + _esc(txt) + "</p>")
        elif t == "note":
            txt = _pick(b, lang).strip()
            if txt:
                parts.append('<div class="note %s">%s</div>' % (_esc(b.get("lv") or "tip"), _esc(txt)))
        elif t == "code":
            code = _pick(b, lang).strip()
            if not code:
                continue
            label = b.get("label") or ""
            if label:
                parts.append('<div class="codelab">' + _esc(label) + "</div>")
            parts.append("<pre><code>" + _esc(code) + "</code></pre>")
        elif t == "table":
            head, rows = b.get("head") or [], b.get("rows") or []
            if not rows:
                continue
            out = ['<div class="tw"><table>']
            if head:
                out.append("<thead><tr>" + "".join(
                    "<th>" + _esc(_pick(c, lang)) + "</th>" for c in head) + "</tr></thead>")
            out.append("<tbody>")
            for r in rows:
                out.append("<tr>" + "".join(
                    "<td>" + _esc(_pick(c, lang)) + "</td>" for c in r) + "</tr>")
            out.append("</tbody></table></div>")
            parts.append("".join(out))
        elif t in ("ul", "ol", "steps", "faq"):
            items = b.get("items") or []
            if not items:
                continue
            if t == "ul":
                parts.append("<ul>" + "".join(
                    "<li>" + _esc(_pick(i, lang)) + "</li>" for i in items) + "</ul>")
            elif t == "faq":
                # FAQ 的原格式是「问题—— 答案」一条字符串。拆得开就用 dl，
                # 拆不开照样当一条列出——不为了排版而丢内容。
                rows = []
                for i in items:
                    txt = _pick(i, lang).strip()
                    if not txt:
                        continue
                    q, sep, a = txt.partition("——")
                    if sep and a.strip():
                        rows.append("<dt>" + _esc(q.strip()) + "</dt><dd>" + _esc(a.strip()) + "</dd>")
                    else:
                        rows.append("<dt>" + _esc(txt) + "</dt>")
                if rows:
                    parts.append('<dl class="faq">' + "".join(rows) + "</dl>")
            else:
                cls = ' class="steps"' if t == "steps" else ""
                parts.append("<ol%s>" % cls + "".join(
                    "<li>" + _esc(_pick(i, lang)) + "</li>" for i in items) + "</ol>")
        elif t == "xlink":
            links = []
            for it in b.get("items") or []:
                sid = it.get("id")
                if not sid or sid == sec_id:
                    continue
                links.append('<a href="../%s/">%s</a>' % (_esc(sid), _esc(_pick(it, lang))))
            if links:
                parts.append('<div class="xlink"><span>%s</span>%s</div>' % (
                    "相关" if lang == "zh" else "See also", "".join(links)))
    return "\n".join(parts)


def _body_html(sec, lang):
    parts = []
    title = _pick(sec.get("title"), lang)
    for ch in sec.get("chunks", []):
        head = _pick(ch.get("heading"), lang)
        # 第一块的 heading 就是节标题，已经在 h1 里了，别重复一遍。
        if head and head != title:
            parts.append("<h2>" + _esc(head) + "</h2>")
        parts.append(_blocks_html(ch.get("blocks"), lang, sec.get("id", "")))
    return "\n".join(x for x in parts if x.strip())


def _desc(sec):
    for ch in sec.get("chunks", []):
        t = " ".join(((ch.get("text") or {}).get("zh") or "").split())
        if len(t) >= 40:
            return t[:118] + ("…" if len(t) > 118 else "")
    return "EnvKit 使用文档 · " + (sec.get("title") or {}).get("zh", "")


def section_page(sec, prev, nxt, version):
    title_zh = (sec.get("title") or {}).get("zh", sec["id"])
    title_en = (sec.get("title") or {}).get("en", title_zh)
    group_zh = (sec.get("group") or {}).get("zh", "")
    group_en = (sec.get("group") or {}).get("en", group_zh)
    url = SITE_BASE + "/s/" + sec["id"] + "/"
    desc = _desc(sec)

    def nav():
        if prev:
            p = '<a href="../%s/">← %s</a>' % (prev["id"], _esc((prev.get("title") or {}).get("zh", "")))
        else:
            p = '<span class="none">已是第一节</span>'
        if nxt:
            n = '<a class="rt" href="../%s/">%s →</a>' % (nxt["id"], _esc((nxt.get("title") or {}).get("zh", "")))
        else:
            n = '<span class="none rt">已是最后一节</span>'
        return '<div class="nav">' + p + n + "</div>"

    head = """<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{t} · EnvKit 文档</title>
<meta name="description" content="{d}">
<link rel="canonical" href="{u}">
<meta property="og:type" content="article">
<meta property="og:site_name" content="EnvKit 文档">
<meta property="og:title" content="{t} · EnvKit 文档">
<meta property="og:description" content="{d}">
<meta property="og:url" content="{u}">
<meta name="twitter:card" content="summary">
<style>{css}</style>
</head>
<body>
<div class="wrap">
<div class="top">
  <a href="../../">EnvKit 文档</a>
  <span>/</span>
  <span>{g}</span>
  <span class="sp"></span>
  <button id="lang" type="button" title="Switch to English">EN</button>
</div>
""".format(t=_esc(title_zh), d=_esc(desc), u=_esc(url), css=PAGE_CSS, g=_esc(group_zh))

    zh = ('<div id="b-zh">\n<h1>' + _esc(title_zh) + '</h1>\n<div class="group">'
          + _esc(group_zh) + ' · ' + _esc(version) + "</div>\n"
          + _body_html(sec, "zh") + "\n" + nav() + "\n</div>")
    en = ('<div id="b-en" hidden>\n<h1>' + _esc(title_en) + '</h1>\n<div class="group">'
          + _esc(group_en) + ' · ' + _esc(version) + "</div>\n"
          + _body_html(sec, "en") + "\n" + nav() + "\n</div>")
    tail = ('<footer>本页是「<a href="../../#doc-%s">%s</a>」的静态副本，'
            '交互版（搜索、目录、明暗切换）在 <a href="../../">EnvKit 文档</a>'
            ' · <a href="../">全部分节</a>。</footer>\n'
            '</div>\n<script>%s</script>\n</body>\n</html>\n') % (
        _esc(sec["id"]), _esc(title_zh), PAGE_JS)
    return head + zh + "\n" + en + "\n" + tail


def index_page(sections, version):
    groups, order = {}, []
    for s in sections:
        g = (s.get("group") or {}).get("zh", "")
        if g not in groups:
            groups[g] = []
            order.append(g)
        groups[g].append(s)
    body = ['<h1>EnvKit 使用文档</h1>',
            '<div class="group">' + _esc(version) + ' · 共 %d 节</div>' % len(sections)]
    for g in order:
        body.append("<h2>" + _esc(g) + "</h2><ul>")
        for s in groups[g]:
            body.append('<li><a href="./%s/">%s</a> — %s</li>' % (
                _esc(s["id"]), _esc((s.get("title") or {}).get("zh", "")), _esc(_desc(s)[:78]) + "…"))
        body.append("</ul>")
    body.append('<div class="nav"><a href="../">交互版文档（搜索 / 目录 / 明暗）</a></div>')
    return ("<!doctype html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n"
            "<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n"
            "<title>EnvKit 文档 · 分节索引</title>\n"
            '<meta name="description" content="EnvKit 使用文档的分节索引，每节一个可独立引用的页面。">\n'
            '<link rel="canonical" href="' + SITE_BASE + '/s/">\n'
            "<style>" + PAGE_CSS + "ul{padding-left:20px}li{margin:0 0 8px;color:var(--sub)}"
            "li a{color:var(--brand);text-decoration:none}li a:hover{text-decoration:underline}"
            "</style>\n</head>\n<body>\n<div class=\"wrap\">\n"
            + "\n".join(body) + "\n</div>\n</body>\n</html>\n")


def emit_static(dst, version):
    """为每一节生成独立静态页，让 `/s/<id>/` 可被单独引用与索引。

    主站是 hash 路由单页：搜索引擎能抓到字，抓不到「哪句属于哪一节」，
    分享出去的预览卡片也永远是首页标题。分节页补的正是这一层结构。
    """
    # 直接从源码解析出「带结构化块」的版本，而不是读 docs-index.json：
    # 那份 JSON 要嵌进 exe，只存纯文本；静态页要还原表格/代码/列表，
    # 两者共用同一个解析器，但不共用同一份产物。
    doc = docs_index.build(with_blocks=True)
    secs = doc.get("sections") or []
    if not secs:
        return fail("文档解析出 0 节，检查 web/src/docs.html 的 D 数组")

    root = os.path.join(dst, "s")
    written = []
    for i, sec in enumerate(secs):
        prev = secs[i - 1] if i > 0 else None
        nxt = secs[i + 1] if i + 1 < len(secs) else None
        d = os.path.join(root, sec["id"])
        if not os.path.isdir(d):
            os.makedirs(d)
        io.open(os.path.join(d, "index.html"), "w", encoding="utf-8", newline="\n").write(
            section_page(sec, prev, nxt, version))
        written.append(sec["id"])
    io.open(os.path.join(root, "index.html"), "w", encoding="utf-8", newline="\n").write(
        index_page(secs, version))

    urls = [SITE_BASE + "/", SITE_BASE + "/s/"] + [SITE_BASE + "/s/" + x + "/" for x in written]
    sm = ['<?xml version="1.0" encoding="UTF-8"?>',
          '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">']
    for u in urls:
        sm.append("  <url><loc>" + u + "</loc></url>")
    sm.append("</urlset>\n")
    io.open(os.path.join(dst, "sitemap.xml"), "w", encoding="utf-8", newline="\n").write("\n".join(sm))
    io.open(os.path.join(dst, "robots.txt"), "w", encoding="utf-8", newline="\n").write(
        "User-agent: *\nAllow: /\nSitemap: " + SITE_BASE + "/sitemap.xml\n")

    # 门禁：节数一致、每页都有标题与描述（否则分享出去仍是空白卡片）。
    bad = []
    for sid in written:
        h = io.open(os.path.join(root, sid, "index.html"), encoding="utf-8").read()
        if "<title>" not in h or 'property="og:description"' not in h or 'content=""' in h:
            bad.append(sid)
    if bad:
        return fail("分节页缺少标题/描述：" + ", ".join(bad))
    if len(written) != len(secs):
        return fail("分节页数 %d 与节数 %d 不一致" % (len(written), len(secs)))
    print("已生成 %d 个分节页 + 分节索引 + sitemap.xml（%s/s/<id>/）" % (len(written), a_out_hint(dst)))
    return 0


def a_out_hint(dst):
    return os.path.relpath(dst, ROOT).replace(os.sep, "/")


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--version", default=os.environ.get("GITHUB_REF_NAME", "dev"),
                    help="显示在页头的版本号，CI 里取 tag 名")
    ap.add_argument("--out", default="_site", help="输出目录")
    ap.add_argument("--check", action="store_true", help="只校验，不写文件")
    ap.add_argument("--no-static", action="store_true",
                    help="只生成 SPA 单页，不生成 /s/<id>/ 分节页")
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
    if not a.no_static:
        return emit_static(dst, shown_version(a.version))
    return 0


if __name__ == "__main__":
    sys.exit(main())
