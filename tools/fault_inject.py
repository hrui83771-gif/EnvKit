# -*- coding: utf-8 -*-
"""EnvKit v2.4 异常注入器（故障恢复率指标的前置）

为什么需要它
------------
"故障恢复率"的定义是「注入异常后自主恢复成功的次数 / 注入异常的次数」。
v2.1 与 v2.2 两份基线都把它标成 🔴 缺前置，理由写着"需异常注入器"——
**理由本身是错的**：Trace 早就能量观察恢复过程了，缺的只是"制造异常"
这一步。工具链上早就现成的有 `portscan.go` 的 `portKillTask`（杀占用）、
`verify.go` 的假探测器（`withLifecycleFake`），但它们都是给 Go 测试用的
可替换点，**不能用来制造真实故障**。

所以这里补一个独立进程：它真的去占端口、真的杀进程、真的写坏文件，
每种注入都必须**可逆**，且默认只在临时目录与高位端口上动手。

## 三条硬约束

1. **默认 dry-run。** 不加 `--go` 只打印将要做什么。这条最重要——
   注入器会杀进程占端口，跑错了会毁掉用户正在做的事。
2. **只碰自己造的东西。** 记录的每个 pid/端口/文件都由本脚本创建，
   `undo()` 只还原本脚本造出来的，绝不碰系统原有的进程。
3. **端口只在 45000+ 里选。** 避开所有常见服务端口（3306/8080/8888/20200）。

用法：
    python tools/fault_inject.py --list
    python tools/fault_inject.py --inject hold_port --go
    python tools/fault_inject.py --undo
"""
import argparse
import json
import os
import socket
import subprocess
import sys
import time
from pathlib import Path

# 状态文件：记录注入点，undo 靠它。放 exe 同级（与 EnvKit 一致）。
STATE = Path(__file__).resolve().parent.parent / 'fault-inject-state.json'

# 注入目标目录。备份类故障必须落进**真实备份目录**，
# 否则 AI 通过正常流程读不到 —— 注入在它触达不到的地方就等于没注入。
BACKUP_DIR = Path(__file__).resolve().parent.parent / 'backups'

# 端口只从高位段选，避开 3306/8080/8888/20200/5002 等常用服务端口
PORT_RANGE = range(45100, 45200)

KINDS = {
    'hold_port': {
        'desc': '占住一个高位端口，制造"启动服务时端口被占"故障',
        'recoverable_by_ai': False,
        'note': 'AI 应识别出端口被外部进程占用并如实报告，不该反复重试启动',
    },
    'kill_service': {
        'desc': '杀掉已登记的服务进程，制造"服务凭空消失"故障',
        'recoverable_by_ai': True,
        'note': 'AI 应通过复验发现服务已死并尝试重新启动',
    },
    'corrupt_backup': {
        'desc': '在临时目录写一份校验和错误的备份文件',
        'recoverable_by_ai': False,
        'note': 'AI 应通过 verify 报出校验和不一致，而不是声称备份可用',
    },
    'unreadable_backup': {
        'desc': '写一份内容被截断的备份（尾部标记缺失）',
        'recoverable_by_ai': False,
        'note': '验"文件完整"必须能查出内容缺损',
    },
}


# ---------- 端口占用 ----------
def _port_free(p):
    """探测端口是否空闲。

    刻意**不设 SO_REUSEADDR**：设了它之后即使端口已被别的进程监听，
    本进程 bind 仍会成功（Windows 上尤其如此），
    于是"起完服务再自检端口是否被占住"这个校验会永远得出"没占住"。

    自检必须用一个在 Windows 上语义稳定的探测方式：尝试 connect。
    连得上说明有人在听——这正是"占住了"的定义。
    """
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(0.5)
    try:
        s.connect(('127.0.0.1', p))
        return False  # 连得上 → 已被占用
    except OSError:
        return True   # 连不上 → 空闲
    finally:
        s.close()


def pick_port():
    for p in PORT_RANGE:
        if _port_free(p):
            return p
    raise SystemExit('45000+ 段没有空闲端口，无法注入')


def inject_hold_port(state):
    port = pick_port()
    # 用系统自带的 python 起一个只监听不发话的服务。
    # 刻意不写业务逻辑：它只需要"占着端口且对任何请求都不响应"。
    code = (
        "import socket,sys,time\n"
        "s=socket.socket(socket.AF_INET,socket.SOCK_STREAM)\n"
        "s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)\n"
        f"s.bind(('127.0.0.1',{port}))\n"
        "s.listen(8)\n"
        "time.sleep(3600)\n"
    )
    # state['dir'] 存的是字符串（要进 JSON），用之前转回 Path。
    # 踩过：直接 str / str 报 TypeError，而 --list 与 dry-run 都不走这条路，
    # 所以只有真注入时才暴露。
    f = Path(state['dir']) / f'hold_{port}.py'
    f.write_text(code, encoding='utf-8')
    proc = subprocess.Popen([sys.executable, str(f)],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    # 轮询等待而不是固定 sleep：Windows 上起一个进程再绑定端口要几十到几百毫秒，
    # 固定 sleep(0.6) 在慢机器上会不够——自检失败但进程其实起来了，
    # 结果是"报了失败却留下一个孤儿进程占着端口"。
    # 踩过：第一次固定 0.6s 失败，事后 netstat 显示端口已被占。
    for _ in range(40):          # 最多等 4 秒
        time.sleep(0.1)
        if not _port_free(port):
            break
    else:
        proc.terminate()
        raise SystemExit(f'注入失败：起了进程但端口 {port} 始终没被占住（已终止该进程）')

    state['items'].append({'kind': 'hold_port', 'port': port, 'pid': proc.pid})
    return f'已占用端口 {port}（pid={proc.pid}）'


def inject_kill_service(state):
    """杀掉登记的服务进程。

    只杀状态文件里登记过的 pid —— 本脚本不主动去扫端口杀进程，
    那是 portKillTask 的职责且需要用户确认。这里只提供"制造故障"的能力，
    故障对象由调用方指定。
    """
    raise SystemExit(
        'kill_service 需要显式指定目标 pid。\n'
        '刻意不自动扫描：注入器擅自杀用户进程是不可接受的风险。\n'
        '用法：先手动启动一个服务，或用 --target-pid 指定。'
    )


def inject_corrupt_backup(state):
    """写一份校验和错误的备份：正文完整但 sha256 对不上。

    **刻意写进真实备份目录 `backups/`**（而不是 fault-tmp/）。
    第一版写在 `envkit/fault-tmp/`，但探索沙箱只放行
    「已配置的前后端目录」（BloodLine 项目），**AI 根本读不到那个文件**——
    于是恢复率评测跑出 6/6 全过，而实际上 AI 压根没接触过这个故障。
    **注入必须落在被测系统能触达的路径上，否则测的是空气。**
    """
    p = BACKUP_DIR / 'corrupt-injected.sql'
    p.parent.mkdir(exist_ok=True)
    p.write_text(
        '-- MySQL dump\nSET NAMES utf8mb4;\n'
        'CREATE TABLE `t1` (`id` int NOT NULL) ENGINE=InnoDB;\n'
        '-- Dump completed on 2026-10-04 00:00:00\n',
        encoding='utf-8')
    state['items'].append({'kind': 'corrupt_backup', 'path': str(p)})
    return f'已写校验和错误的备份：{p}'


def inject_unreadable_backup(state):
    """写一份内容被截断的备份：只有开头，尾部标记缺失。

    同样写进 backups/ —— 理由见 inject_corrupt_backup。
    """
    p = BACKUP_DIR / 'truncated-injected.sql'
    p.parent.mkdir(exist_ok=True)
    p.write_text(
        '-- MySQL dump\nSET NAMES utf8mb4;\n'
        'CREATE TABLE `t1` (`id` int NOT NULL) ENGINE=InnoDB;\n',
        encoding='utf-8')  # 故意不写结尾
    state['items'].append({'kind': 'unreadable_backup', 'path': str(p)})
    return f'已写内容截断的备份：{p}'


INJECTORS = {
    'hold_port': inject_hold_port,
    'kill_service': inject_kill_service,
    'corrupt_backup': inject_corrupt_backup,
    'unreadable_backup': inject_unreadable_backup,
}


# ---------- 撤销 ----------
def _kill_pid(pid):
    """终止一个进程，跨平台。

    Windows 上 `os.kill(pid, signal.SIGTERM)` 抛 WinError 87（参数错误）——
    那里只能走 TerminateProcess。用 `taskkill /F /T`：
      - `/T` 连子进程一起收（占位脚本不会留孤儿）
      - `/F` 强制，Python 的 SIGTERM 语义在 Windows 上本来就不可靠
    踩过：第一次用 os.kill + SIGTERM，Windows 上直接 WinError 87抛出去，
    异常中断了整个 undo —— 结果端口释放了但临时文件与状态文件全部残留，
    **"撤销失败"留下了更脏的现场**。
    """
    if os.name == 'nt':
        # 刻意不用 text=True：taskkill 输出是 GBK，Python 会按 UTF-8 解码并抛
        # UnicodeDecodeError（踩过：报在subprocess 的读取线程里，很难定位）。
        # 只看 returncode，不解析输出。
        r = subprocess.run(['taskkill', '/PID', str(pid), '/T', '/F'],
                           capture_output=True)
        return r.returncode == 0
    import signal
    try:
        os.kill(pid, signal.SIGTERM)
        return True
    except ProcessLookupError:
        return True   # 已经没了，视为成功


def undo():
    if not STATE.exists():
        print('没有注入记录，无需撤销')
        return
    st = json.loads(STATE.read_text(encoding='utf-8'))
    done, failed = 0, 0
    for it in st.get('items', []):
        k = it.get('kind')
        try:
            if k == 'hold_port':
                pid = it.get('pid')
                if _kill_pid(pid):
                    done += 1
                    print(f'  已终止占位进程 pid={pid} port={it.get("port")}')
                else:
                    failed += 1
                    print(f'  ! 无法终止 pid={pid}')
            elif k in ('corrupt_backup', 'unreadable_backup'):
                p = Path(it['path'])
                if p.exists():
                    p.unlink()
                    done += 1
                    print(f'  已删除 {p}')
        except Exception as e:
            # 单项失败不能让整个撤销中断——中断会留下更脏的现场。
            failed += 1
            print(f'  !撤销{k} 失败：{e}')
    # 清掉占位脚本
    d = Path(st.get('dir', ''))
    if d.exists():
        for f in d.glob('hold_*.py'):
            try:
                f.unlink()
            except OSError:
                pass
        try:
            if not any(d.iterdir()):
                d.rmdir()
        except OSError:
            pass
    try:
        STATE.unlink()
    except OSError:
        pass
    print(f'撤销完成：{done} 项成功' + (f'，{failed} 项失败' if failed else ''))


# ---------- 主流程 ----------
def load_state():
    if STATE.exists():
        return json.loads(STATE.read_text(encoding='utf-8'))
    d = STATE.parent / 'fault-tmp'
    d.mkdir(exist_ok=True)
    return {'dir': str(d), 'items': []}


def main():
    ap = argparse.ArgumentParser(description='EnvKit 异常注入器')
    ap.add_argument('--list', action='store_true', help='列出可注入的异常')
    ap.add_argument('--inject', choices=sorted(KINDS), help='注入一种异常')
    ap.add_argument('--go', action='store_true', help='真正执行（不加则只打印）')
    ap.add_argument('--undo', action='store_true', help='撤销所有注入')
    ap.add_argument('--json', action='store_true', help='以 JSON 输出结果')
    a = ap.parse_args()

    if a.list:
        if a.json:
            print(json.dumps(KINDS, ensure_ascii=False, indent=2))
        else:
            print('可注入的异常：\n')
            for k, v in KINDS.items():
                print(f'  {k:<20} {v["desc"]}')
                print(f'  {"":<20} AI 可自主恢复: {v["recoverable_by_ai"]} · {v["note"]}')
        return

    if a.undo:
        undo()
        return

    if not a.inject:
        ap.print_help()
        return

    meta = KINDS[a.inject]
    if not a.go:
        print('dry-run（加 --go 才真正执行）：\n')
        print(f'  异常类型：{a.inject}')
        print(f'  说明：{meta["desc"]}')
        print(f'  AI 可自主恢复：{meta["recoverable_by_ai"]}')
        print(f'  预期行为：{meta["note"]}')
        return

    st = load_state()
    try:
        msg = INJECTORS[a.inject](st)
    except SystemExit:
        raise
    except Exception as e:
        print(f'注入失败：{e}', file=sys.stderr)
        sys.exit(1)
    STATE.write_text(json.dumps(st, ensure_ascii=False, indent=2), encoding='utf-8')
    print(msg)
    print(f'记录已写入 {STATE}')
    print(f'撤销：python {Path(__file__).name} --undo')


if __name__ == '__main__':
    main()