# -*- coding: utf-8 -*-
"""smoke_dist.py —— 冒烟测试 /api/dist/export（运行时分发包导出）。"""
import hashlib
import io
import os
import re
import sys
import time
import urllib.request
import zipfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASE = "http://127.0.0.1:18765"
fails = []


def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + ((" | " + detail) if detail else ""))
    if not ok:
        fails.append(name)


# 1. 首页拿令牌
html = urllib.request.urlopen(BASE + "/", timeout=10).read().decode("utf-8")
m = re.search(r'__EK_TOKEN__="([^"]+)"', html)
check("token_injected", bool(m))
token = m.group(1) if m else ""

# 2. 未带令牌应被拒
try:
    urllib.request.urlopen(BASE + "/api/dist/export", timeout=10)
    check("no_token_rejected", False, "got 200")
except urllib.error.HTTPError as e:
    check("no_token_rejected", e.code in (401, 403), "http %d" % e.code)

# 3. 带令牌下载 zip
time.sleep(0.3)
resp = urllib.request.urlopen(BASE + "/api/dist/export?t=" + urllib.parse.quote(token), timeout=30)
blob = resp.read()
disp = resp.headers.get("Content-Disposition", "")
check("zip_downloaded", len(blob) > 3 * 1024 * 1024, "%.1f MB" % (len(blob) / 1048576))
check("zip_filename", "EnvKit-v1.9.5-win64.zip" in disp, disp)

z = zipfile.ZipFile(io.BytesIO(blob))
names = z.namelist()
check("zip_entries", names == ["EnvKit.exe", "安装.bat", "卸载.bat", "使用说明.md", "SHA256SUMS.txt"], str(names))

exe_in_zip = z.read("EnvKit.exe")
exe_on_disk = open(os.path.join(ROOT, "EnvKit.exe"), "rb").read()
check("exe_identical", hashlib.sha256(exe_in_zip).hexdigest() == hashlib.sha256(exe_on_disk).hexdigest())

sums = z.read("SHA256SUMS.txt").decode()
check("sha256sums", sums.strip().endswith(hashlib.sha256(exe_in_zip).hexdigest()), sums.strip())

bat = z.read("安装.bat")
try:
    txt = bat.decode("gbk")
    ok_gbk = True
except Exception:
    txt, ok_gbk = "", False
check("bat_gbk", ok_gbk and "chcp 936" in txt and "\r\n" in txt)
check("readme_utf8", "SmartScreen" in z.read("使用说明.md").decode("utf-8"))

print("== SMOKE %s ==" % ("PASS" if not fails else "FAIL: " + ", ".join(fails)))
sys.exit(0 if not fails else 1)
