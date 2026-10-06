# -*- coding: utf-8 -*-
"""起 EnvKit → 跑评测 → 停 EnvKit（一条命令内完成）

为什么必须写在一条命令里
------------------------
调试实例与测试**必须在同一条命令内起→测→停**。EnvKit 是本仓库里的
被测程序，用 `Start-Process` 另起一个命令跑它，会在命令结束时被回收
（本项目的工具跨命令回收子进程，EnvKit 因此在环境检测中途被杀，
日志断在 '检测 Node.js 已安装' 那一行，进程已不存在）。

所以这个包装器不做别的，只负责生命周期：
    1. 若 18765 已被占用且能拿到 token，直接复用（不重复起）
    2. 否则起 EnvKit_ab.exe，等到 HTML 里能取到 token 为止（最多 60s）
    3. 跑指定的评测脚本（默认 ab_memory）
    4. finally 里停掉自己起的那个实例

它不吞异常：评测脚本报错时这里也报错，只是保证进程一定被停。

用法：
    python tools/ab_run.py                 # 跑 A/B 对照
    python tools/ab_run.py --script judge  # 跑判分器执行器
    python tools/ab_run.py --list          # 列出可用的评测脚本
"""
import os
import re
import subprocess
import sys

# **stdout 显式设 UTF-8。**
#
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 `print('  ⚠ …')` 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
# recovery_sandbox.py 实测踩过：评测一行都没跑就崩在这里。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
EXE = ROOT / 'EnvKit_ab.exe'
BASE = 'http://127.0.0.1:18765'

# 强制直连：走本机代理会被剥掉 POST body（GET 也会被代理绕，影响取 token）
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))

# 可跑的评测脚本。key 是 --script 的取值。
SCRIPTS = {
    'ab': ('ab_memory', '经验增益 A/B 对照'),
    'judge': ('judge_tasks', '任务成功率 / 首次成功率 判分器'),
    'e2e23': ('e2e_v23', 'v2.3 能力端到端验收'),
    'recovery': ('recovery_rate', '故障恢复率（注入异常后自主恢复）'),
}


def token_from(url=BASE, timeout=3):
    try:
        html = OPENER.open(url, timeout=timeout).read().decode('utf-8')
    except Exception:
        return None
    m = re.search(r'window\.__EK_TOKEN__="([0-9a-f]+)"', html)
    return m.group(1) if m else None


def ensure_instance(wait=60):
    """返回 (token, proc)。proc 为 None 表示复用了已有实例。"""
    tok = token_from()
    if tok:
        print('复用已在运行的 EnvKit 实例')
        return tok, None
    if not EXE.exists():
        raise SystemExit(f'找不到 {EXE}，先构建：go build -o EnvKit_ab.exe .')
    print(f'启动 {EXE.name}...')
    proc = subprocess.Popen([str(EXE)], cwd=str(ROOT),
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    t0 = time.time()
    while time.time() - t0 < wait:
        if proc.poll() is not None:
            raise SystemExit(
                f'EnvKit 启动后立刻退出（rc={proc.returncode}）。\n'
                f'看日志：{ROOT / "envkit-20261004.log"} 尾部')
        tok = token_from()
        if tok:
            print(f'就绪，耗时 {time.time() - t0:.1f}s')
            return tok, proc
        time.sleep(0.5)
    proc.kill()
    raise SystemExit(f'{wait}s 内没就绪，已终止')


def main():
    # 参数解析放在这里做，只透传给被调脚本——
    # 让 "--script judge --repeat 3" 这类组合能直接工作。
    argv = sys.argv[1:]
    if '--list' in argv:
        print('可用的评测脚本（--script 选一个）：')
        for k, (mod, desc) in SCRIPTS.items():
            print(f'  {k:<8} {mod}.py  {desc}')
        return
    which = 'ab'
    if '--script' in argv:
        i = argv.index('--script')
        which = argv[i + 1]
        del argv[i:i + 2]
    if which not in SCRIPTS:
        raise SystemExit(f'未知脚本 {which}，可用：{sorted(SCRIPTS)}')
    mod_name = SCRIPTS[which][0]

    sys.path.insert(0, str(ROOT / 'tools'))
    mod = __import__(mod_name)

    tok, proc = ensure_instance()
    try:
        # 把 token 交给脚本，避免它自己再取一次
        if hasattr(mod, 'TOKEN'):
            mod.TOKEN = tok
        elif hasattr(mod, 'ab'):
            mod.ab.TOKEN = tok
        sys.argv = [mod_name] + argv
        mod.main()
    finally:
        if proc is not None:
            print('\n停止 EnvKit 实例...')
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
            print('已停止')


if __name__ == '__main__':
    main()