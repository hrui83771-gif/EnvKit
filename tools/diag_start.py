# -*- coding: utf-8 -*-
"""诊断：exe 能否在沙箱里启动 + 监听在哪个端口。"""
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import socket, subprocess, time, sys

EXE = r"D:\BCGD\FarmTrace\envkit\EnvKit.exe"
CWD = r"D:\BCGD\FarmTrace\envkit"

proc = subprocess.Popen([EXE], cwd=CWD,
                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
time.sleep(4)
alive = proc.poll() is None
print("alive_after_4s:", alive, "returncode:", proc.returncode, flush=True)

def open_port(port):
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(0.5)
    try:
        s.connect(("127.0.0.1", port))
        s.close()
        return True
    except Exception:
        return False

hits = [p for p in range(18765, 18791) if open_port(p)]
print("listening_ports:", hits, flush=True)
if alive:
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except Exception:
        proc.kill()
    print("terminated", flush=True)
