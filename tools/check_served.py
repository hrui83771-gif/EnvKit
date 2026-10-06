# 抓取 EnvKit 实际吐出的页面，检查 dbview 相关元素与 2376 行内容
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import re, subprocess, time, urllib.request

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
    if html is None:
        raise SystemExit("not ready")
    lines = html.splitlines()
    print("total_lines:", len(lines))
    hits = [(i + 1, l.strip()[:100]) for i, l in enumerate(lines) if 'id="dbview"' in l or 'id="btn-dbview"' in l or 'id="st-dbview"' in l]
    print("dbview hits:", hits)
    # 2376 行前后
    for i in range(2372, min(2380, len(lines))):
        print(i + 1, "|", lines[i][:110])
    # 检查 onclick 绑定行
    bind = [(i + 1, l.strip()[:110]) for i, l in enumerate(lines) if "btn-dbview').onclick" in l]
    print("bind:", bind)
finally:
    proc.kill()
    print("stopped")
