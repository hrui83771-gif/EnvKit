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

# 仓库根目录（沙箱与注入器共用）
ROOT = Path(__file__).resolve().parent.parent

# 状态文件：记录注入点，undo 靠它。放 exe 同级（与 EnvKit 一致）。
STATE = ROOT / 'fault-inject-state.json'

# 备份类注入落哪个目录。
#
# 必须在沙箱运行时指向**沙箱的**备份目录，否则会往用户仓库里写注入文件。
# 判据：读沙箱状态文件里的 backup_dir（sandbox.py 建沙箱时写进去的）。
# 没有沙箱时才退回仓库 backups/（用户手动用注入器时的老行为）。
#
# 踩过：v2.5 第一版硬编码 ROOT/'backups'，而沙箱 config 指向临时区的 backups/
# —— 于是"沙箱隔离"只隔离了服务进程，备份类注入照样污染用户目录。
# **隔离没做到位等于没隔离**，只是不容易发现。
def _backup_dir():
    import tempfile
    try:
        st = Path(tempfile.gettempdir()) / 'envkit-sandbox.json'
        if st.exists():
            info = json.loads(st.read_text(encoding='utf-8'))
            bd = info.get('backup_dir')
            if bd:
                return Path(bd)
    except Exception:
        pass
    # 没有沙箱：退回 exe 同级 backups/。
    # 备份故障必须落进 AI 真正读得到的目录 ——
    # 写在它触达不到的地方，等于没注入。
    return ROOT / 'backups'


BACKUP_DIR = _backup_dir()


def backup_dir_now():
    """**每次注入时**重新求值备份目录，不要用模块级常量。

    模块级 `BACKUP_DIR` 在 import 时求值，而沙箱往往在这之后才建 ——
    于是沙箱模式下它仍指向仓库 backups/，注入照样污染用户目录。
    这就是"常量在正确时刻算错了"的典型：代码看起来对，跑起来不对。
    """
    return _backup_dir()

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
    """杀掉「沙箱里EnvKit 自己起的那个服务进程」。

    ## 第一版为什么直接抛异常拒绝（这个拒绝是对的，不能改）

        raise SystemExit('kill_service 需要显式指定目标 pid。\n'
                         '刻意不自动扫描：注入器擅自杀用户进程是不可接受的风险。')

    要测"自主恢复"就必须杀掉一个服务进程，但用户机器上跑着的
    真实服务（BloodLine 前后端）绝不能当试验品——杀掉它们等于
    **毁掉用户正在做的事**。

    **为了跑通评测去放宽这条约束是本末倒置**：
    评测装置不该有能力毁掉用户环境。真缺的是"一个可以安全杀的目标"，
    不是"允许杀任何目标"。

    ## 正解：沙箱（tools/sandbox.py）

    沙箱有自己的 config.json、自己的项目目录、自己的高位端口 45311，
    里面跑的是 eval/fixtures/sandbox-backend ——一个只监听端口的
    一次性 Go 服务。杀它不碰用户任何东西。

    ## 为什么必须走 /api/runtime/state 拿PID

    PID 有三条来路，可信度完全不同：
      1. 扫端口 → 拿到的是「占用者」，可能是用户自己的服务。**绝不能杀。**
      2. 本脚本自己记的 pid → 那是 hold_port 造的占位进程，不是"服务"。
      3. **EnvKit 自己记的 svcState["backend"].PID** → 这才是
         「EnvKit 通过 start_service 起的那个服务进程」。

    只有第 3 条是对的。而且它天然满足一个前提：这个进程是 EnvKit
    亲自派生的，EnvKit 也知道怎么把它拉回来——**恢复路径是真的**。

    ## 找不到 PID 就明确失败，不猜

    沙箱服务没起来时 svcState 是空的。此时如果"猜一个进程"，
    杀的就可能是用户的东西。**宁可这一轮标INVALID 也不冒这个险。**
    """
    import json as _json
    import tempfile
    import urllib.request

    # 与 tools/sandbox.py 共用同一个状态文件（临时区，不进 git）。
    # 刻意不硬编码端口：端口变了而这里没变，注入器就会去杀别的进程。
    state_path = Path(tempfile.gettempdir()) / 'envkit-sandbox.json'
    if not state_path.exists():
        raise SystemExit(
            'kill_service 需要先建沙箱。\n'
            '请先跑：python tools/sandbox.py --build\n'
            '（沙箱提供独立 config / 项目目录 / 高位端口，'
            '杀它不碰用户真实服务）')

    base = _json.loads(state_path.read_text(encoding='utf-8'))
    # **两个端口必须严格分开**，第一版就是在这儿错的：
    #   ui_port = 沙箱 EnvKit 自己的 HTTP 端口（EnvKit 自己找，18765起）
    #             → 拿 token、打 /api/runtime/state 都走它
    #   port    = 被测服务端口（45311）
    #             → 那是 sandbox-backend 监听的端口，跟 EnvKit 无关
    # 之前误用 port 去连 EnvKit，拿到的是「连接被拒绝」——
    # 被测服务当然会拒绝，因为它是业务服务不是 EnvKit。
    ui = base.get('ui_port')
    svc_port = base.get('port')
    if not ui:
        raise SystemExit(
            '沙箱实例还没启动（状态里没有 ui_port）。\n'
            '请先跑：python tools/sandbox.py --launch\n'
            '**没有 UI 端口就没有 token，没有 token 就查不到 svcState，'
            '也就不知道该杀哪个 pid** —— 那样就只能靠猜，'
            '而评测器最不能干的就是猜。')
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    # 拿 token（连 EnvKit 自己的 UI 端口）
    try:
        html = opener.open(f'http://127.0.0.1:{ui}/', timeout=5).read().decode('utf-8')
    except Exception as e:
        raise SystemExit(
            f'沙箱实例没在 {ui} 上跑起来（{e}）。\n'
            f'要先用 tools/sandbox.py --launch 起沙箱实例，'
            f'kill_service 才有可杀的目标。\n'
            f'**不猜、不扫端口** —— 扫到的是谁就杀谁，那是评测器最不能做的事。')
    import re
    m = re.search(r'window\.__EK_TOKEN__="([0-9a-f]+)"', html)
    if not m:
        raise SystemExit('取不到沙箱的 X-EnvKit-Token')
    tok = m.group(1)

    req = urllib.request.Request(
        f'http://127.0.0.1:{ui}/api/runtime/state', method='GET')
    req.add_header('X-EnvKit-Token', tok)
    raw = _json.loads(opener.open(req, timeout=10).read().decode('utf-8'))

    # 端点返回 {"ok":true,"state":{...}} —— 状态在 **state 里面**。
    # 第一版直接读 raw['services']，永远是 None，
    # 于是 pid 恒为 0，表现为「没有可安全杀的目标」——
    # 而真相是解析写错了。**这个错误在三个文件里各犯了一次。**
    st = raw.get('state') if isinstance(raw, dict) else None
    if not isinstance(st, dict) or 'services' not in st:
        keys = sorted(raw.keys()) if isinstance(raw, dict) else type(raw).__name__
        raise SystemExit(
            f'/api/runtime/state 的结构与预期不符（顶层键={keys}）。\n'
            f'端点把状态包在 state 字段里，读顶层拿不到 services。\n'
            f'**这是解析写错，不是"没有服务"** —— 别当成环境问题去排查。')

    backend = (st.get('services') or {}).get('backend') or {}
    pid = backend.get('pid')
    if not pid:
        # 明确失败，不猜。这是本函数最重要的一条纪律。
        raise SystemExit(
            '沙箱的 backend 服务当前没在运行（svcState 里没有 pid），'
            '没有可安全杀的目标。\n'
            '请先让沙箱服务跑起来（start_service backend），'
            '再注入 kill_service。\n'
            '**本注入器不会去扫端口杀进程** —— 那样杀到的可能是用户的服务。')

    ok = _kill_pid(int(pid))
    if not ok:
        raise SystemExit(f'终止沙箱服务 pid={pid} 失败')

    # 清掉 EnvKit 侧的运行时记录？不——刻意不清。
    # svcState 里还留着 pid，verifyService 会先判「进程已退出」，
    # 这正是真实故障的样子（EnvKit 还以为服务在）。
    # 恢复路径要求 AI 自己发现并调用 start_service 重新拉起。
    state['items'].append({'kind': 'kill_service', 'pid': int(pid),
                           'port': svc_port, 'target': 'backend'})
    return (f'已杀掉沙箱 backend 服务 pid={pid}'
            f'（被测端口 {svc_port}，沙箱内，用户服务未受影响）')


def inject_corrupt_backup(state):
    """写一份校验和错误的备份：正文完整但 sha256 对不上。

    **刻意写进真实备份目录 `backups/`**（而不是 fault-tmp/）。
    第一版写在 `envkit/fault-tmp/`，但探索沙箱只放行
    「已配置的前后端目录」（BloodLine 项目），**AI 根本读不到那个文件**——
    于是恢复率评测跑出 6/6 全过，而实际上 AI 压根没接触过这个故障。
    **注入必须落在被测系统能触达的路径上，否则测的是空气。**
    """
    p = backup_dir_now() / 'corrupt-injected.sql'
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
    p = backup_dir_now() / 'truncated-injected.sql'
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
            elif k == 'kill_service':
                # **刻意什么都不做。**
                #
                # 被杀的正是那个要靠 AI 恢复的服务——undo 若把它拉起来，
                # 就等于替AI 把故障修好了，那这一轮测的"自主恢复"是假的。
                # 而且它跑在沙箱里，拆沙箱时整个目录连进程一起清掉。
                #
                # 这一点与 hold_port 相反：占位进程是我们造的垃圾，
                # 必须清理；沙箱服务是**被测对象**，必须留着。
                print(f'  kill_service 不做撤销：被测服务保持"已死"状态，'
                      f'（拉起来是 AI 的活，undo 替它做就测不到自主恢复了）'
                      f'  拆沙箱用 python tools/sandbox.py --destroy')
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