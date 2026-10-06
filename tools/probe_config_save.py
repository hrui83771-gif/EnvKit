# -*- coding: utf-8 -*-
"""验证可疑点：POST /api/config 是否会把 AI 配置（base_url/api_key/model）整段抹掉。
先备份 config.json，测完自动还原。"""
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import io, json, os, re, shutil, subprocess, time, urllib.request

EK = r"D:\BCGD\FarmTrace\envkit"
BASE = "http://127.0.0.1:18765"
CFG = os.path.join(EK, "config.json")
BAK = os.path.join(EK, "_cfg_test_backup.json")

def kill():
    subprocess.run(["taskkill", "/F", "/IM", "EnvKit.exe"], capture_output=True)

def start():
    subprocess.Popen([os.path.join(EK, "EnvKit.exe")], cwd=EK,
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=0x00000008)

def token():
    for _ in range(30):
        try:
            h = urllib.request.urlopen(BASE + "/", timeout=5).read().decode("utf-8", "ignore")
            return re.search(r'__EK_TOKEN__\s*=\s*"([^"]+)"', h).group(1)
        except Exception:
            time.sleep(1)

def api(path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(BASE + path, data=data, headers={"X-EnvKit-Token": T, "Content-Type": "application/json"})
    return json.loads(urllib.request.urlopen(r, timeout=60).read().decode())

def ai_state():
    c = api("/api/config")
    ai = c.get("ai")
    if not ai:
        return "AI 配置: 不存在（被抹掉）"
    return "AI 配置: 存在 model=%s base_url=%s key_len=%d" % (ai.get("model"), (ai.get("base_url") or "")[:32], len(ai.get("api_key") or ""))

kill()
shutil.copy(CFG, BAK)
if os.path.exists(CFG + ".bak"):
    shutil.copy(CFG + ".bak", BAK + ".bak")
start(); T = token(); time.sleep(5)

print("初始：", ai_state())

# 复刻前端 configBody() 的形状（注意：其中没有 ai 字段）
body = {
    "app_name": "EnvKit", "app_title": "统一开发环境助手", "install_dir": "C:\\EnvKit\\tools",
    "proxy": {"http": "", "https": ""},
    "goproxy": "https://goproxy.cn,https://goproxy.io,direct", "gosumdb": "", "go_cgo": "",
    "npm_registry": "https://mirrors.cloud.tencent.com/npm/",
    "mysql": {"password": "", "port": 3306, "service_name": "MySQL"},
    "projects": {"frontend_dir": "", "backend_dir": "", "mysql_host": "127.0.0.1", "mysql_port": 3306,
                 "mysql_user": "root", "mysql_password": "", "db_name": "farm", "sql_file": ""},
    "chain": {}, "components": [],
}
api("/api/config", body)
time.sleep(1)
print("模拟前端保存一次后：", ai_state())
t = api("/api/ai/test", {})
print("保存后 AI 测试连接：", "ok=%s %sms %s" % (t.get("ok"), t.get("latency_ms"), str(t.get("error",""))[:60]))
t = api("/api/ai/test", {})
print("保存后 AI 测试连接：", "ok=%s %sms %s" % (t.get("ok"), t.get("latency_ms"), str(t.get("error",""))[:60]))

kill()
shutil.copy(BAK, CFG)
if os.path.exists(BAK + ".bak"):
    shutil.copy(BAK + ".bak", CFG + ".bak")
os.remove(BAK)
if os.path.exists(BAK + ".bak"):
    os.remove(BAK + ".bak")
start(); T = token(); time.sleep(5)
print("还原后：", ai_state())
kill()
print("完成（配置已还原，测试实例已关闭）")
