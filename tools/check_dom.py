# 用 html.parser 检查 id="dbview" 是否落在注释/脚本等非 DOM 区域，以及重复 id
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import io, subprocess, time, urllib.request
from html.parser import HTMLParser

EXE = r"D:\BCGD\FarmTrace\envkit\EnvKit.exe"
CWD = r"D:\BCGD\FarmTrace\envkit"
BASE = "http://127.0.0.1:18765"
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

proc = subprocess.Popen([EXE], cwd=CWD, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                        creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
try:
    html = None
    for _ in range(60):
        try:
            with opener.open(BASE + "/", timeout=2) as r:
                html = r.read().decode("utf-8", "replace")
            break
        except Exception:
            time.sleep(0.5)

    class P(HTMLParser):
        def __init__(self):
            super().__init__(convert_charrefs=True)
            self.stack = []
            self.in_comment = False
            self.found = []
            self.ids = {}
            self.void = {"br", "hr", "img", "input", "meta", "link", "col", "area", "base", "embed", "source", "track", "wbr"}
        def handle_starttag(self, tag, attrs):
            d = dict(attrs)
            if "id" in d:
                self.ids[d["id"]] = self.ids.get(d["id"], 0) + 1
                if d["id"].startswith(("dbview", "dbv", "btn-dbview")):
                    ctx = " > ".join(self.stack[-4:])
                    self.found.append((d["id"], self.getpos()[0], ctx))
            if tag not in self.void:
                self.stack.append(tag)
        def handle_endtag(self, tag):
            if self.stack and tag in self.stack:
                while self.stack and self.stack.pop() != tag:
                    pass
        def handle_startendtag(self, tag, attrs):
            self.handle_starttag(tag, attrs)
            if tag not in self.void:
                self.handle_endtag(tag)
        def handle_comment(self, data):
            self.found.append(("<COMMENT>", self.getpos()[0], data[:60]))

    p = P()
    p.feed(html)
    print("dbv elements in DOM:", p.found)
    dups = {k: v for k, v in p.ids.items() if v > 1}
    print("duplicate ids:", dups)
    # dbview / btn-dbview 是否在 ids 表里
    print("has dbview:", "dbview" in p.ids, "has btn-dbview:", "btn-dbview" in p.ids)
    # config panel 的栈
    print("panel-config ids near:", [k for k in p.ids if "panel" in k or "config" in k][:10])
finally:
    proc.kill()
    print("stopped")
