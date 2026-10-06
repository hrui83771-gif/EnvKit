# 数据浏览接口实测：起实例 → list/tables/rows/query → 只读拦截验证 → 停进程
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import io, json, os, re, subprocess, sys, time, urllib.request

EXE = r"D:\BCGD\FarmTrace\envkit\EnvKit.exe"
CWD = r"D:\BCGD\FarmTrace\envkit"
BASE = "http://127.0.0.1:18765"
TIMEOUT = 30

opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))  # 本机代理会剥 body，必须直连

def get(path):
    with opener.open(BASE + path, timeout=TIMEOUT) as r:
        return r.status, r.read().decode("utf-8", "replace")

def post(path, body):
    data = json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, headers={"Content-Type": "application/json"})
    try:
        with opener.open(req, timeout=TIMEOUT) as r:
            return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")

def wait_ready():
    for _ in range(60):
        try:
            with opener.open(BASE + "/", timeout=2) as r:
                html = r.read().decode("utf-8", "replace")
                m = re.search(r"__EK_TOKEN__|[A-Za-z0-9]{16,}", html)
                return html
        except Exception:
            time.sleep(0.5)
    raise SystemExit("service not ready")

def main():
    proc = subprocess.Popen([EXE], cwd=CWD,
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
    try:
        html = wait_ready()
        if "__TOKEN__" in html:
            print("TOKEN placeholder not replaced?")
            return
        mtok = re.search(r"token[^'\"\n]{0,24}['\"]([A-Za-z0-9_\-]{16,})['\"]", html, re.I)
        if not mtok:
            print("TOKEN NOT FOUND; token lines:", [l[:120] for l in html.splitlines() if "token" in l.lower()][:5])
            return
        tok = mtok.group(1)
        hdr = lambda p: p + ("&" if "?" in p else "?") + "t=" + tok

        st, out = get(hdr("/api/db/list"))
        print("LIST", st, out[:200])

        st, out = get(hdr("/api/db/tables?db=farm"))
        j = json.loads(out)
        print("TABLES", st, "count=", len(j.get("tables", [])), "sample=", j.get("tables", [])[:5])
        print("COUNTS=", dict(list(j.get("counts", {}).items())[:5]))

        if j.get("tables"):
            t0 = j["tables"][0]
            st, out = get(hdr(f"/api/db/rows?db=farm&table={t0}&page=1&size=5"))
            r2 = json.loads(out)
            print("ROWS", st, "total=", r2.get("total"), "cols=", r2.get("cols"))
            for row in (r2.get("rows") or [])[:2]:
                print("  ", [c[:24] for c in row])

        # 自定义查询（合法）
        st, out = post(hdr("/api/db/query"), {"db": "farm", "sql": "SELECT COUNT(*) AS n FROM user_infos"})
        print("QUERY", st, out[:150])
        st, out = post(hdr("/api/db/query"), {"db": "farm", "sql": "SELECT * FROMNoSuchTable"})
        print("BADTAB", st, out[:180])

        # 写操作必须被拦（双层：动词白名单 + READ ONLY）
        st, out = post(hdr("/api/db/query"), {"db": "farm", "sql": "DROP TABLE user"})
        print("DROP-guard", st, out[:150])
        st, out = post(hdr("/api/db/query"), {"db": "farm", "sql": "UPDATE user SET id=1"})
        print("UPDATE-guard", st, out[:150])
        st, out = post(hdr("/api/db/query"), {"db": "farm", "sql": "SELECT 1; DROP TABLE user"})
        print("MULTI-guard", st, out[:150])
    finally:
        proc.kill()
        print("stopped")

main()
