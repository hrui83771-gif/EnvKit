# -*- coding: utf-8 -*-
"""针对本轮改动（共享 HTTP client / 统一重试 / 拆包 / 序号正则修复）的定点回归。"""
import io, json, os, re, subprocess, time, urllib.request, urllib.error

EK = r"D:\BCGD\FarmTrace\envkit"
BASE = "http://127.0.0.1:18765"
LOG = os.path.join(EK, "envkit-%s.log" % time.strftime("%Y%m%d"))
results = []

def loglines():
    if not os.path.exists(LOG):
        return []
    return io.open(LOG, encoding="utf-8", errors="ignore").read().splitlines()

def report(title, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + title + ("  | " + detail if detail else ""))
    results.append((title, ok))

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
    raise SystemExit("EnvKit 未起来")

def api(path, body=None, timeout=120):
    data = json.dumps(body or {}).encode() if body is not None else None
    r = urllib.request.Request(BASE + path, data=data, headers={"X-EnvKit-Token": T})
    for i in range(3):
        try:
            return json.loads(urllib.request.urlopen(r, timeout=timeout).read().decode())
        except (ConnectionResetError, urllib.error.URLError):
            if i == 2:
                raise
            time.sleep(2)

def chat(messages, timeout=180):
    """返回 (text, events, http_status)；status=0 表示请求本身失败。"""
    r = urllib.request.Request(BASE + "/api/ai/chat", data=json.dumps({"messages": messages}).encode(),
                               headers={"X-EnvKit-Token": T, "Content-Type": "application/json"})
    try:
        resp = urllib.request.urlopen(r, timeout=timeout)
    except urllib.error.HTTPError as e:
        return "", [], e.code
    text, events, buf = "", [], ""
    while True:
        ch = resp.read(1)
        if not ch:
            break
        buf += ch.decode("utf-8", "replace")
        while "\n\n" in buf:
            line, buf = buf.split("\n\n", 1)
            line = line.strip()
            if line.startswith("data: "):
                ev = json.loads(line[6:])
                events.append(ev)
                if ev.get("type") == "delta":
                    text += ev.get("text", "")
    return text, events, 200

kill()
start()
T = token()
time.sleep(6)

# --- 1. 共享 client：模型列表 / 测试连接 ---
cfg = api("/api/ai/config")
report("1a AI 配置已保存", cfg.get("key_set") is True, f"model={cfg.get('model')}")
ml = api("/api/ai/models", body={})
report("1b 模型列表（httpShort 共享 client）", ml.get("ok") is True and len(ml.get("models", [])) > 0,
       f"{len(ml.get('models', []))} 个")

t = api("/api/ai/test", body={}, timeout=90)
report("1c 测试连接（httpAPI 共享 client）", t.get("ok") is True, f"{t.get('latency_ms')}ms {str(t.get('error',''))[:60]}")

# --- 2. 序号输入不再 500（本次修掉的真 bug） ---
before = len(loglines())
msgs = [{"role": "user", "content": "我该做什么"},
        {"role": "assistant", "content": "1. 查看日志\n2. 启动服务\n3. 检查数据库连接"},
        {"role": "user", "content": "2"}]
text, events, code = chat(msgs)
report("2 序号输入返回 200（修复前 regexp panic→500）", code == 200, f"HTTP {code}")
newlog = loglines()[before:]
report("2b 无 panic 记录", not any("panic" in l for l in newlog),
       f"新增 {len(newlog)} 行日志")
report("2c 菜单屏蔽生效（界面无菜单残留）",
       not any(k in text for k in ("需要我", "直接说需求", "请告诉我")), f"文本 {len(text)} 字")

# --- 3. 统一重试链路径：检查数据库连接（runMysqlOut） ---
before = len(loglines())
text, events, code = chat([{"role": "user", "content": "检查数据库连接"}])
tools = [e.get("tool") for e in events if e.get("type") == "tool_result"]
ok = "db_check" in tools and any(k in text for k in ("3306", "8.0", "成功", "已存在", "连接"))
report("3 检查数据库连接", ok, f"tools={tools} 回答={text[:60]!r}")

# --- 4. 流式（httpStream 共享 client + 单轮计时器）：查看日志 ---
text, events, code = chat([{"role": "user", "content": "看一下最近的系统日志有没有警告"}])
tools = [e.get("tool") for e in events if e.get("type") == "tool_result"]
report("4 查看日志", "get_logs" in tools and len(text) > 20, f"tools={tools} 文本 {len(text)} 字")

# --- 5. 解读不应触发预检 ---
before = len(loglines())
r = urllib.request.Request(BASE + "/api/ai/explain", data=b"{}",
                           headers={"X-EnvKit-Token": T, "Content-Type": "application/json"})
resp = urllib.request.urlopen(r, timeout=180)
ex, buf = "", ""
while True:
    ch = resp.read(1)
    if not ch:
        break
    buf += ch.decode("utf-8", "replace")
    while "\n\n" in buf:
        line, buf = buf.split("\n\n", 1)
        line = line.strip()
        if line.startswith("data: "):
            ev = json.loads(line[6:])
            if ev.get("type") == "delta":
                ex += ev.get("text", "")
hits = [l for l in loglines()[before:] if "预检命中" in l]
report("5 AI 解读：预检命中 0 次", len(hits) == 0 and len(ex) > 100, f"{len(ex)} 字，命中 {len(hits)}")

print("\n===== 汇总 =====")
ok = sum(1 for _, o in results if o)
print(f"{ok}/{len(results)} 通过")
for t, o in results:
    print(("PASS " if o else "FAIL ") + t)
kill()
print("完成（测试实例已关闭）")
