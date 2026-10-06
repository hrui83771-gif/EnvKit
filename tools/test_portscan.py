# 端口占用诊断接口实测：起实例 → 占端口 → 扫描 → 428 确认流 → 结束进程 → 复扫 → 脚本校验 → 停进程
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import io, json, os, re, socket, subprocess, sys, time, urllib.request, urllib.error

EXE = r"D:\BCGD\FarmTrace\envkit\EnvKit.exe"
CWD = r"D:\BCGD\FarmTrace\envkit"
BASE = "http://127.0.0.1:18765"
TIMEOUT = 30
TEST_PORT = 38317

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
                return r.read().decode("utf-8", "replace")
        except Exception:
            time.sleep(0.5)
    raise SystemExit("service not ready")

def main():
    proc = subprocess.Popen([EXE], cwd=CWD,
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
    holder = None
    try:
        html = wait_ready()
        mtok = re.search(r"token[^'\"\n]{0,24}['\"]([A-Za-z0-9_\-]{16,})['\"]", html, re.I)
        if not mtok:
            print("TOKEN NOT FOUND")
            return
        tok = mtok.group(1)
        hdr = lambda p: p + ("&" if "?" in p else "?") + "t=" + tok

        # 1. 起一个占端口子进程
        holder = subprocess.Popen([sys.executable, "-c",
            f"import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); "
            f"s.bind(('0.0.0.0',{TEST_PORT})); s.listen(1); time.sleep(120)"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        time.sleep(1.2)

        # 2. 扫描：TEST_PORT 应 LISTENING，41999 应 FREE
        st, out = get(hdr(f"/api/ports/scan?ports={TEST_PORT},41999"))
        print("SCAN RAW", st, out[:300])
        j = json.loads(out)
        rows = {r["port"]: r for r in j.get("ports", [])}
        r1, r2 = rows.get(TEST_PORT, {}), rows.get(41999, {})
        print("SCAN", st, "| occupied:", r1.get("state"), r1.get("pid"), r1.get("name"),
              "| free:", r2.get("state"))
        assert r1.get("state") == "LISTENING" and r1.get("pid") == holder.pid, "occupied-port scan mismatch"
        assert r2.get("state") == "FREE", "free-port scan mismatch"

        # 3. 安全护栏：系统进程 / 环境变量进程
        st, out = post(hdr("/api/ports/kill"), {"pid": 4, "port": TEST_PORT, "confirm": True})
        print("GUARD pid4:", st, out[:80])
        assert st == 400

        # 4. 未确认 → 428 needConfirm（reason 里应带进程名）
        st, out = post(hdr("/api/ports/kill"), {"pid": holder.pid, "port": TEST_PORT})
        print("CONFIRM-428:", st, out[:220])
        assert st == 428 and json.loads(out).get("needConfirm")

        # 5. 确认后 → 结束成功，复扫 FREE
        st, out = post(hdr("/api/ports/kill"), {"pid": holder.pid, "port": TEST_PORT, "confirm": True})
        print("KILL:", st, out[:120])
        assert st == 200
        holder.wait(timeout=10)
        st, out = get(hdr(f"/api/ports/scan?ports={TEST_PORT}"))
        j = json.loads(out)
        print("RESCAN:", j["ports"][0]["state"])
        assert j["ports"][0]["state"] == "FREE"

        # 6. 启动脚本名校验（非法名拒绝，不真启动）
        st, out = post(hdr("/api/program/web-start"), {"script": "bad name; rm"})
        print("BADSCRIPT:", st, out[:100])
        assert st == 400

        print("ALL PASS")
    finally:
        if holder and holder.poll() is None:
            holder.kill()
        proc.kill()
        print("stopped")

main()
