# -*- coding: utf-8 -*-
"""v1.9.14 缓存命中率实测：启动实例 -> 抓 token -> 三轮真实对话 -> 解析 usage 事件 -> 停进程。"""
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import io, json, re, sys, time, subprocess, urllib.request

BASE = "http://127.0.0.1:18765"
EXE = r"D:\BCGD\FarmTrace\envkit\EnvKit.exe"
CWD = r"D:\BCGD\FarmTrace\envkit"
# 本机代理会剥 POST body（v1.9.3 教训），必须强制直连：urlopen 没有 proxies 参数，用 Opener
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def open_url(req, timeout):
    return OPENER.open(req, timeout=timeout)


def log(msg):
    print(msg, flush=True)


def wait_up(proc, timeout=30):
    t0 = time.time()
    while time.time() - t0 < timeout:
        if proc.poll() is not None:
            raise SystemExit("EnvKit 进程提前退出，code=%s" % proc.returncode)
        try:
            with open_url(urllib.request.Request(BASE + "/"), 2) as r:
                page = r.read().decode("utf-8", "replace")
                m = re.search(r'__EK_TOKEN__="([0-9a-fA-F]{16,})"', page)
                if m:
                    return m.group(1)
        except Exception:
            pass
        time.sleep(0.4)
    raise SystemExit("服务未在 %ss 内就绪" % timeout)


def chat(token, messages, timeout=120):
    """POST /api/ai/chat，流式读取 SSE，返回 (assistant 全文, usage 或 None, events 摘要)。"""
    body = json.dumps({"messages": messages, "lang": "zh"}).encode("utf-8")
    req = urllib.request.Request(
        BASE + "/api/ai/chat", data=body, method="POST",
        headers={"Content-Type": "application/json", "X-EnvKit-Token": token,
                 "Accept": "text/event-stream"})
    text_parts, usage, events = [], None, []
    with open_url(req, timeout) as r:
        buf = b""
        while True:
            chunk = r.read(1)
            if not chunk:
                break
            buf += chunk
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                line = line.decode("utf-8", "replace").strip()
                if not line.startswith("data:"):
                    continue
                try:
                    ev = json.loads(line[5:].strip())
                except Exception:
                    continue
                t = ev.get("type")
                if t == "delta":
                    text_parts.append(ev.get("text", ""))
                elif t == "tool_call":
                    events.append("tool:" + ev.get("tool", "?"))
                elif t == "usage":
                    usage = ev.get("usage")
                elif t == "error":
                    events.append("error:" + str(ev.get("text"))[:120])
                elif t == "done":
                    events.append("done:" + str(ev.get("reason", "")))
    return "".join(text_parts), usage, events


def pct(u):
    if not u or not u.get("has_cache"):
        return "n/a"
    h, m = u.get("cache_hit", 0), u.get("cache_miss", 0)
    tot = h + m
    return "%.1f%% (%d/%d)" % (100.0 * h / tot if tot else 0, h, tot)


def main():
    proc = subprocess.Popen([EXE], cwd=CWD,
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        token = wait_up(proc)
        log("token ok: %s..." % token[:8])
        msgs = []
        rounds = [
            "用一句话介绍你自己。",
            "帮我查一下现在系统里各组件的安装状态。",
            "把刚才的结果用一句话总结。",
            "我的数据库现在健康吗？",
        ]
        gap = float(sys.argv[1]) if len(sys.argv) > 1 else 0  # 轮间隔秒数（默认 0）
        for i, q in enumerate(rounds, 1):
            if i > 1 and gap > 0:
                log("-- 等待 %ss（等缓存落盘）--" % gap)
                time.sleep(gap)
            msgs = msgs + [{"role": "user", "content": q}]
            t0 = time.time()
            ans, usage, events = chat(token, msgs)
            dt = time.time() - t0
            log("R%d 耗时 %.1fs events=%s" % (i, dt, events))
            log("R%d usage: prompt=%s completion=%s 命中率=%s" % (
                i, usage.get("prompt_tokens") if usage else "?",
                usage.get("completion_tokens") if usage else "?", pct(usage)))
            log("R%d 回答(截断): %s" % (i, ans[:80].replace("\n", " ")))
            msgs = msgs + [{"role": "assistant", "content": ans or "（无文本回复）"}]
        log("DONE")
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()


if __name__ == "__main__":
    main()
