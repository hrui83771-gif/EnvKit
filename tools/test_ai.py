# -*- coding: utf-8 -*-
"""EnvKit AI 助手场景实测（curl 子进程版：绕开针对 python.exe 的连接重置）"""
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import json, subprocess, time, sys, glob, os, re, urllib.request, urllib.error

BASE = 'http://127.0.0.1:18765'

def get_token():
    html = subprocess.run(['curl', '-s', BASE + '/'], capture_output=True).stdout.decode('utf-8')
    m = re.search(r'window.__EK_TOKEN__="([0-9a-f]+)"', html)
    return m.group(1)

def _open(req, timeout):
    last = None
    for i in range(3):
        try:
            return urllib.request.urlopen(req, timeout=timeout)
        except ConnectionResetError as e:
            last = e; time.sleep(2)
    raise last

TOKEN = get_token()

def api(path, body=None, method=None, timeout=180):
    data = json.dumps(body or {}).encode('utf-8') if (body is not None or method == 'POST') else None
    req = urllib.request.Request(BASE + path, data=data, method=method or ('POST' if data else 'GET'))
    req.add_header('Content-Type', 'application/json')
    req.add_header('X-EnvKit-Token', TOKEN)
    last = None
    for i in range(3):
        try:
            return json.loads(_open(req, timeout).read().decode('utf-8'))
        except (ConnectionResetError, urllib.error.URLError) as e:
            last = e; time.sleep(2)
    raise last

def sse_chat(messages, confirm=None, timeout=180):
    body = {'messages': messages}
    if confirm:
        body['confirm'] = confirm
    req = urllib.request.Request(BASE + '/api/ai/chat', data=json.dumps(body).encode('utf-8'), method='POST')
    req.add_header('Content-Type', 'application/json')
    req.add_header('X-EnvKit-Token', TOKEN)
    events, text = [], ''
    resp = _open(req, timeout)
    buf = ''
    while True:
        chunk = resp.read(1)
        if not chunk:
            break
        buf += chunk.decode('utf-8', 'replace')
        while '\n\n' in buf:
            line, buf = buf.split('\n\n', 1)
            line = line.strip()
            if not line.startswith('data: '):
                continue
            ev = json.loads(line[6:])
            events.append(ev)
            if ev.get('type') == 'delta':
                text += ev.get('text', '')
    return text, events

def run_turn(messages, auto_confirm=True):
    confirms = []
    text, events = sse_chat(messages)
    cr = next((e for e in events if e.get('type') == 'confirm_request'), None)
    while cr and auto_confirm:
        confirms.append(cr)
        messages = messages + [{'role': 'assistant', 'content': text,
                                'tool_calls': [{'id': cr['tool_call_id'], 'type': 'function',
                                                'function': {'name': cr['tool'], 'arguments': json.dumps(cr.get('args') or {})}}]}]
        text, events = sse_chat(messages, confirm={'tool_call_id': cr['tool_call_id'], 'tool': cr['tool'],
                                                   'args': cr.get('args') or {}, 'approved': True})
        cr = next((e for e in events if e.get('type') == 'confirm_request'), None)
    return text, messages, confirms, events

def report(title, ok, detail=''):
    print(('PASS ' if ok else 'FAIL ') + title + ('  | ' + detail if detail else ''))
    return ('PASS ' if ok else 'FAIL ') + title + ('  | ' + detail if detail else '')

results = []

# ---------- S0 ----------
cfg = api('/api/ai/config')
results.append(report('S0a 配置已保存', cfg.get('key_set') == True, f"provider={cfg.get('provider')} model={cfg.get('model')}"))
t = api('/api/ai/test', timeout=90)
results.append(report('S0b 测试连接', t.get('ok') == True, f"{t.get('latency_ms')}ms {str(t.get('error',''))[:80]}"))

# ---------- S1 读工具 ----------
msgs = [{'role': 'user', 'content': '现在环境状态怎么样？Go 和 Node 的版本分别是多少？'}]
text, msgs, confirms, events = run_turn(msgs)
ok = ('1.23.3' in text or '1.23' in text) and ('22.22' in text or '22.2' in text)
results.append(report('S1 环境状态问答（版本号正确）', ok, f"回答摘取: {text[:100]!r}"))
messages_v = msgs

# ---------- S2 意图执行 ----------
msgs = [{'role': 'user', 'content': '启动前端'}]
text, msgs, confirms, events = run_turn(msgs)
conf = confirms[0] if confirms else None
ok = conf is not None and conf.get('tool') == 'start_service'
results.append(report('S2a "启动前端"触发确认卡片', ok, f"confirm={conf and conf.get('tool')}"))
time.sleep(3)
h = api('/api/health')
web = h.get('services', {}).get('web', {})
results.append(report('S2b 前端真实启动', web.get('running') == True, f"running={web.get('running')} url={web.get('url','')}"))

# ---------- S3 停止 ----------
msgs = msgs + [{'role': 'user', 'content': '把前端停掉'}]
text, msgs, confirms, events = run_turn(msgs)
time.sleep(3)
h = api('/api/health')
web = h.get('services', {}).get('web', {})
results.append(report('S3 "把前端停掉"→真实停止', web.get('running') == False, f"running={web.get('running')}"))

# ---------- S4 备份 ----------
before = set(glob.glob(os.path.join(r'D:\BCGD\FarmTrace\server\dbs', 'farm-*.sql')))
msgs = [{'role': 'user', 'content': '帮我备份数据库'}]
text, msgs, confirms, events = run_turn(msgs)
time.sleep(6)
after = set(glob.glob(os.path.join(r'D:\BCGD\FarmTrace\server\dbs', 'farm-*.sql')))
new = after - before
results.append(report('S4 AI 备份数据库', len(new) == 1, f"新文件: {[os.path.basename(x) for x in new]}"))

# ---------- S5 日志 ----------
msgs = [{'role': 'user', 'content': '看一下最近的系统日志有没有警告'}]
text, msgs, confirms, events = run_turn(msgs)
ok = ('日志' in text or 'WARN' in text or '警告' in text or '权限' in text)
results.append(report('S5 日志查询问答', ok, f"回答摘取: {text[:80]!r}"))

# ---------- S6 解读 ----------
explain_text = ''
req = urllib.request.Request(BASE + '/api/ai/explain', data=b'{}', method='POST')
req.add_header('Content-Type', 'application/json')
req.add_header('X-EnvKit-Token', TOKEN)
resp = _open(req, 180)
buf = ''
while True:
    chunk = resp.read(1)
    if not chunk:
        break
    buf += chunk.decode('utf-8', 'replace')
    while '\n\n' in buf:
        line, buf = buf.split('\n\n', 1)
        line = line.strip()
        if not line.startswith('data: '):
            continue
        ev = json.loads(line[6:])
        if ev.get('type') == 'delta':
            explain_text += ev.get('text', '')
ok = len(explain_text) > 100 and ('建议' in explain_text or '问题' in explain_text or '总结' in explain_text)
results.append(report('S6 诊断报告 AI 解读', ok, f"长度 {len(explain_text)} 摘取: {explain_text[:80]!r}"))

print('\n===== 结果汇总 =====')
passed = sum(1 for r in results if r.startswith('PASS'))
print(f"{passed}/{len(results)} 通过")
for r in results:
    print(r)
