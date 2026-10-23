# -*- coding: utf-8 -*-
"""版本号三处一致性门禁：main.go appVersion ↔ versioninfo.json ↔ resource.syso

背景：发版纪律要求「不发版也要三处同步版本号」，靠人记必漏。
本脚本把这条铁律变成断言，任何一处漏改直接非零退出。

检查项：
  1. main.go 的 appVersion 常量（语义版本 x.y.z）
  2. versioninfo.json：ProductVersion 字符串、FileVersion 字符串前缀、
     FixedFileInfo 三段数字，全部与 appVersion 一致
  3. resource.syso：二进制里能找到 UTF-16LE 编码的 FileVersion 串
     （syso 是 goversioninfo 从 versioninfo.json 生成的，找不到 = 没重新生成）

跑法：python tools/version_check.py   （进构建门禁，失败即红）
"""
import json
import os
import re
import sys

try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

fail = []


def check(name, ok, detail=""):
    print(f"{'OK ' if ok else 'BAD'} {name}{('  -> ' + detail) if detail and not ok else ''}")
    if not ok:
        fail.append(name)


# --- 1. main.go 的 appVersion ---
main_go = open(os.path.join(ROOT, "main.go"), encoding="utf-8").read()
m = re.search(r'appVersion\s*=\s*"(\d+\.\d+\.\d+)"', main_go)
if not m:
    print("BAD main.go 里找不到 appVersion 常量（正则 appVersion = \"x.y.z\"）")
    sys.exit(1)
ver = m.group(1)
print(f"main.go appVersion          = {ver}")

# --- 2. versioninfo.json ---
vi = json.load(open(os.path.join(ROOT, "versioninfo.json"), encoding="utf-8"))
pv_str = vi["StringFileInfo"]["ProductVersion"]
fv_str = vi["StringFileInfo"]["FileVersion"]
ffi = vi["FixedFileInfo"]
check("versioninfo ProductVersion == appVersion", pv_str == ver,
      f"json={pv_str!r} main={ver!r}")
check("versioninfo FileVersion 前缀 == appVersion", fv_str.startswith(ver + "."),
      f"json={fv_str!r} main={ver!r}")
nums_ok = (ffi["ProductVersion"]["Major"], ffi["ProductVersion"]["Minor"], ffi["ProductVersion"]["Patch"]) == \
          (ffi["FileVersion"]["Major"], ffi["FileVersion"]["Minor"], ffi["FileVersion"]["Patch"]) == \
          tuple(int(x) for x in ver.split("."))
check("FixedFileInfo 三段数字 == appVersion", nums_ok,
      f"Product={ffi['ProductVersion']} File={ffi['FileVersion']} main={ver}")

# --- 3. resource.syso（字节级：UTF-16LE 的 FileVersion 串） ---
syso = os.path.join(ROOT, "resource.syso")
if not os.path.exists(syso):
    check("resource.syso 存在", False, "文件缺失（图标/版本资源未生成）")
else:
    blob = open(syso, "rb").read()
    needle = fv_str.encode("utf-16-le")
    check(f"resource.syso 含 FileVersion {fv_str}（UTF-16LE 字节）", needle in blob,
          "syso 里找不到该串——versioninfo.json 改了但没重新生成 syso")

print("\nRESULT:", "PASS" if not fail else "FAIL " + str(fail))
sys.exit(0 if not fail else 1)
