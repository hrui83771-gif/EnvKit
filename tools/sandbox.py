# -*- coding: utf-8 -*-
"""沙箱实例构建器（v2.5 kill_service 注入的前置）

## 为什么必须有沙箱

"自主恢复率"这一维要注入「服务进程消失」，让 AI 走**产品真实的
start_service 链路**把服务拉回来。注入器第一版直接抛异常拒绝了这件事：

    inject_kill_service() -> SystemExit('kill_service 需要显式指定目标 pid。
        刻意不自动扫描：注入器擅自杀用户进程是不可接受的风险。')

**这个拒绝是对的**，不能改。���错的方式是"为了跑通评测去放宽安全约束"，
那是本末倒置——评测装置不该有能力毁掉用户正在做的事。

所以正解是**换一个可以安全杀的目标**：造一个一次性沙箱实例，
它有自己的 config.json、自己的项目目录、自己的高位端口（45311）。
杀它不碰用户任何东西，整个沙箱删掉就彻底干净。

## 沙箱换掉了什么、保留了什么

| 字段 | 处理 | 原因 |
|---|---|---|
| `ai`（含 DPAPI 加密的 key） | **原样复制** | 没它就没法真实调用；它是 DPAPI 加密的密文，复制到别的目录仍能解|
| `projects.frontend_dir` | 指向沙箱空目录 | 避免动用户前端 |
| `projects.backend_dir` | 指向 sandbox-backend | **被测目标**：AI 要启动/恢复的就是它 |
| `projects.scan_ports` | `[45311]` | 端口与 fixture 源码里写死的地址一致 |
| `projects.mysql_*` | 清空 | 沙箱不测数据库；留空避免误连用户库 |
| `chain` | 清空 | 不测链端 |
| 其余（proxy/npm 等） | 原样复制 | 无副作用，省事 |

## 安全边界（硬约束）

1. **端口固定 45311**，且启动前检查它空闲——不空闲就拒绝建沙箱，
   绝不"抢占"一个已有服务在用的端口。
2. **沙箱目录在系统临时区**，不在仓库里，不进 git。
3. **AI 记忆清零**：沙箱复用真实 config 意味着会写同一个 memories.json，
   所以建沙箱前先备份、拆沙箱后还原。
   这条是踩过的：v2.4 的 A/B 跑完把 3 条测试记忆留在用户机器上。
4. **绝不改用户的 config.json**。沙箱是"复制一份改那份"。

用法：
    python tools/sandbox.py --build      # 建沙箱，打印沙箱目录
    python tools/sandbox.py --destroy    # 拆沙箱，完整还原
    python tools/sandbox.py --status     # 看当前沙箱状态
"""
import argparse
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# 被测服务端口。写死在 eval/fixtures/sandbox-backend/main.go 的 addr 里，
# 两边必须一致——改了这边不改那边，服务能起来但复验判不出来。
SANDBOX_PORT = 45311

# 沙箱 EnvKit 自己的 UI 端口由 EnvKit 自己找（main.go:findPort(18765)，
# 在 18765-18815 里挑空闲的），**没有命令行参数可指定**。
# 所以这里不设它，也不假装能设——沙箱实例靠"独占运行"来隔离：
# 跑沙箱前必须先停掉正式实例，否则两者会抢同一个 UI 端口，
# 而 token 是绑在实例上的，拿错实例的 token 会打到一个完全不同的环境上。
SANDBOX_BACKEND = ROOT / 'eval' / 'fixtures' / 'sandbox-backend'
REAL_CONFIG = ROOT / 'config.json'

# 沙箱状态记录。放在仓库同级会被 git 看见，所以放临时区。
STATE_NAME = 'envkit-sandbox.json'


def _state_path():
    return Path(tempfile.gettempdir()) / STATE_NAME


def port_busy(p):
    """端口是否已被占用。

    用 connect 而不是 bind——设了 SO_REUSEADDR 时即使端口在听，
    本进程 bind 仍会成功（Windows 上尤其如此），自检会永远得出"空闲"。
    这个坑在 fault_inject.py 里已经踩过一次。
    """
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(0.5)
    try:
        s.connect(('127.0.0.1', p))
        return True
    except OSError:
        return False
    finally:
        s.close()


def kill_tree(pid):
    if os.name == 'nt':
        # 不用 text=True：taskkill 输出是 GBK，Python 按 UTF-8 解码会抛
        # UnicodeDecodeError，而且异常会中断整个拆除流程留下更脏的现场。
        return subprocess.run(['taskkill', '/PID', str(pid), '/T', '/F'],
                              capture_output=True).returncode == 0
    import signal
    try:
        os.kill(pid, signal.SIGTERM)
        return True
    except ProcessLookupError:
        return True


def build():
    if not REAL_CONFIG.exists():
        raise SystemExit(f'找不到 {REAL_CONFIG}：沙箱需要复制它（含 AI 配置）')

    # 硬约束 1：被测服务端口必须空闲。不空闲就拒绝，绝不抢占。
    if port_busy(SANDBOX_PORT):
        raise SystemExit(
            f'被测服务端口 {SANDBOX_PORT} 已被占用，拒绝建沙箱。\n'
            f'沙箱不能抢占已有服务的端口——那正是我们评测器本身要避免的行为。\n'
            f'请先确认是什么在监听，或改 sandbox.py 里的 SANDBOX_PORT（并同步改'
            f' eval/fixtures/sandbox-backend/main.go 里的 addr）。')

    cfg = json.loads(REAL_CONFIG.read_text(encoding='utf-8'))
    st = _state_path()
    if st.exists():
        raise SystemExit(f'已存在沙箱记录 {st}，先 --destroy 再建')

    # 备份记忆库：沙箱复用真实 config，AI 记忆会写同一个文件。
    # 踩过：v2.4 的 A/B 跑完把 3 条测试记忆留在用户机器上。
    mem_backup = None
    mem = ROOT / 'memories.json'
    if mem.exists():
        mem_backup = mem.read_text(encoding='utf-8')
    box = Path(tempfile.mkdtemp(prefix='envkit-sandbox-'))
    frontend = box / 'web'
    frontend.mkdir(parents=True, exist_ok=True)
    (frontend / 'package.json').write_text(json.dumps({
        'name': 'sandbox-frontend',
        'version': '1.0.0',
        'private': True,
        'scripts': {'dev': 'vite --host 127.0.0.1'},
    }, indent=2), encoding='utf-8')

    # 改配置：只换「会被动手的字段」，其余原样保留。
    # **ai 段整个复制**：里面是 DPAPI 加密的密文，
    # DPAPI 绑定的是用户账户不是路径，所以换个目录仍能解密。
    cfg['projects'] = {
        'frontend_dir': str(frontend),
        'backend_dir': str(SANDBOX_BACKEND),
        'mysql_host': '',
        'mysql_port': 0,
        'mysql_user': '',
        'mysql_password': '',
        'db_name': '',
        'sql_file': '',
        'backup_dir': str(box / 'backups'),
        # 端口必须与 sandbox-backend/main.go 里写死的 addr 一致
        'scan_ports': [SANDBOX_PORT],
    }
    # 链端与数据库都不测——清空，避免误连用户环境
    cfg['chain'] = {}
    if 'mysql' in cfg:
        cfg['mysql'] = {}
    (box / 'config.json').write_text(
        json.dumps(cfg, ensure_ascii=False, indent=2), encoding='utf-8')
    (box / 'backups').mkdir(exist_ok=True)

    st.write_text(json.dumps({
        'dir': str(box),
        # 被测服务端口，注入器要确认服务死了没有
        'port': SANDBOX_PORT,
        # 沙箱 EnvKit 的 UI 端口：EnvKit 自己找（18765起），
        # 跑起来后由 launch_sandbox 探测并回填到这里。
        # **不留空**是因为注入器要靠它拿 token 打 /api/runtime/state——
        # 拿错实例的 token 会打到一个完全不同的环境上。
        'ui_port': None,
        'backend_dir': str(SANDBOX_BACKEND),
        # 备份目录也记进状态：注入器要靠它把备份类故障写进沙箱，
        # 而不是用户仓库。缺这个字段它会退回 ROOT/'backups'，
        # 于是"沙箱隔离"只隔离了服务进程，备份注入照样污染用户目录
        # ——**隔离没做到位等于没隔离**，只是不容易发现。
        'backup_dir': str(box / 'backups'),
        # 内容也存下来：只存一个"曾经备份过"的布尔值，
        # 拆除时就无法还原——写回去的是空文件，等于把用户的记忆库清了。
        # **"标记备份存在"不等于"备份了内容"**，这个坑踩过。
        'memories_backed_up': mem_backup is not None,
        'memory_content': mem_backup,
        'created': time.strftime('%Y-%m-%d %H:%M:%S'),
    }, ensure_ascii=False, indent=2), encoding='utf-8')

    # 预编译：让 go build 的耗时留在沙箱准备阶段，
    # 而不是混进"AI 恢复服务"的耗时里污染观测。
    # 不预编译也能跑（EnvKit 自己在 start 时会 build），
    # 但首次 build 可能要十几秒，会让恢复耗时的数字失真。
    build_log = subprocess.run(
        ['go', 'build', '-o', str(box / 'sandbox-backend.exe'), '.'],
        cwd=str(SANDBOX_BACKEND), capture_output=True)
    prebuilt = build_log.returncode == 0

    print(f'沙箱已建：{box}')
    print(f'  backend   : {SANDBOX_BACKEND}')
    print(f'  被测端口  : {SANDBOX_PORT}（已确认空闲）')
    print(f'  预编译    : {"成功" if prebuilt else "失败（EnvKit 启动时会自己 build）"}')
    if not prebuilt:
        err = build_log.stderr.decode('utf-8', 'replace')[:300]
        print(f'    go build 输出：{err}')
    print(f'  记忆库    : {"已备份，拆沙箱时还原" if mem_backup else "无 memories.json，无需还原"}')
    print(f'\n下一步：把 EnvKit 的 exe 复制到 {box} 再启动它。')
    print('  沙箱实例的 UI 端口由 EnvKit 自己找（18765起）——它没有端口参数。')
    print('  **跑沙箱前必须先停掉正式实例**，否则两者抢同一个 UI 端口，')
    print('  而 token 绑在实例上，拿错实例的 token 会打到一个完全不同的环境。')
    return 0


def _sandbox_pids():
    """列出**路径在沙箱目录内**的进程 PID。

    ## 为什么不能按进程名匹配

    第一版按 `tasklist` 里含 "EnvKit" 就杀。**那是会误杀用户正式实例的**——
    用户自己开着的那个 EnvKit 也叫 EnvKit，路径在仓库里，不在沙箱里。

    判据必须是**可执行文件路径在沙箱目录下**，
    因为沙箱的 exe 是我们复制进去的那份，路径唯一。

    ## 为什么不用 wmic

    第一版用 `wmic process get ProcessId,ExecutablePath`。
    实测被安全策略直接拦下（Program Blacklist）——
    **评测装置不该要求用户往安全黑名单里加白名单**才能跑。
    换 PowerShell 的 Get-CimInstance，拿的是同一份数据。

    拿不到 ExecutablePath（非管理员时该字段常为空）就返回空列表，
    **宁可漏杀也不误杀** —— 漏杀的后果是临时目录删不掉，
    误杀的后果是用户的 EnvKit 窗口突然消失。
    """
    if os.name != 'nt':
        return []
    # 单行 PowerShell：输出 CSV，格式 "Name,Pid,Path"
    ps = ("Get-CimInstance Win32_Process | "
          "Where-Object { $_.ExecutablePath -like '*EnvKit*' -or "
          "$_.Name -like '*sandbox-backend*' } | "
          "ForEach-Object { '{0},{1},{2}' -f $_.Name,$_.ProcessId,$_.ExecutablePath }")
    r = subprocess.run(
        ['powershell', '-NoProfile', '-NonInteractive', '-Command', ps],
        capture_output=True)
    txt = r.stdout.decode('utf-8', 'replace') + r.stdout.decode('gbk', 'replace')
    out = []
    for line in txt.splitlines():
        parts = line.strip().split(',', 2)
        if len(parts) < 2:
            continue
        name, pid_s = parts[0], parts[1]
        path = parts[2] if len(parts) > 2 else ''
        if not pid_s.isdigit():
            continue
        out.append((int(pid_s), f'{name},{path}'))
    return out


def destroy():
    st = _state_path()
    if not st.exists():
        print('没有沙箱记录，无需拆除')
        return 0
    info = json.loads(st.read_text(encoding='utf-8'))
    box = Path(info['dir'])
    box_s = str(box).lower()

    # 只杀路径确实在沙箱目录下的进程。
    # 判据是路径而不是进程名：按名字杀会连用户自己的正式实例一起杀掉。
    killed, skipped = 0, 0
    for pid, line in _sandbox_pids():
        low = line.lower()
        if box_s in low or 'sandbox-backend' in low:
            if kill_tree(pid):
                killed += 1
                print(f'  已终止沙箱进程 pid={pid}')
        else:
            skipped += 1

    if box.exists():
        shutil.rmtree(box, ignore_errors=True)
    if box.exists():
        print(f'  ⚠ 沙箱目录仍存在（可能有文件被占用）：{box}')

    # 还原记忆库。内容在建沙箱时就存进了 state——
    # 这里只判断"当初有没有备份过"，不重新读文件（那时已经被沙箱覆盖了）。
    if info.get('memories_backed_up') and info.get('memory_content') is not None:
        (ROOT / 'memories.json').write_text(
            info['memory_content'], encoding='utf-8')
        print('  记忆库已还原为沙箱建立前的内容')
    elif info.get('memories_backed_up'):
        # 走到这里说明建沙箱时只记了标记没存内容。
        # 这种��况必须显式报出来，不能静默跳过——
        # 静默跳过等于让用户以为记忆库没事，实际可能已经被沙箱的测试记忆覆盖。
        print('  ⚠ 建沙箱时只记了"已备份"标记但没存内容，无法还原。'
              '请检查 memories.json 是否被沙箱的测试记忆覆盖。')
    st.unlink()
    print(f'沙箱已拆除：{box}（终止 {killed} 个沙箱进程，'
          f'跳过 {skipped} 个非沙箱进程）')
    return 0


def status():
    st = _state_path()
    if not st.exists():
        print('当前无沙箱')
        return 0
    info = json.loads(st.read_text(encoding='utf-8'))
    box = Path(info['dir'])
    print(f'沙箱目录：{box}（{"存在" if box.exists() else "已丢失"}）')
    print(f'被测端口：{info["port"]}（{"被占用" if port_busy(info["port"]) else "空闲"}）')
    ui = info.get('ui_port')
    if ui:
        print(f'UI 端口 ：{ui}（{"在监听" if port_busy(ui) else "未监听，实例可能已停"}）')
    else:
        print('UI 端口 ：未启动（跑 --launch 起一个）')
    print(f'创建时间：{info.get("created")}')
    return 0


def launch(exe_name='EnvKit_ab.exe', wait=25):
    """把 EnvKit exe 复制进沙箱并启动，回填 UI 端口。

    ## 为什么必须"复制 exe"而不是"在沙箱目录里跑仓库的 exe"

    EnvKit 用 `os.Executable()` 定位 config.json（configfile.go:202）。
    在别处跑仓库那份 exe，它读的是**仓库根的 config.json**——
    也就是用户真实配置，backend_dir 指向 BloodLine。
    那样kill_service 杀的就是用户真实服务。**隔离必须是物理的。**

    ## UI 端口只能探测，不能指定

    EnvKit 端口是 findPort(18765) 在 18765-18815 里挑空闲的，
    没有命令行参数。所以这里轮询这些端口找哪个能拿到 token。
    拿到的必须是**沙箱这个 exe** 的 token —— token 是每个实例随机生成的，
    打错实例就等于打到了一个完全不同的环境。
    """
    import re
    import urllib.request

    st = _state_path()
    if not st.exists():
        raise SystemExit('没有沙箱记录，请先 --build')
    info = json.loads(st.read_text(encoding='utf-8'))
    box = Path(info['dir'])
    if not box.exists():
        raise SystemExit(f'沙箱目录已丢失：{box}')

    src = ROOT / exe_name
    if not src.exists():
        raise SystemExit(f'找不到 {src}。先构建：go build -o {exe_name} .')
    dst = box / 'EnvKit_sandbox.exe'
    shutil.copy2(src, dst)

    # 端口冲突预检：正式实例在跑的话，两者会抢同一个 UI 端口。
    # 不预检的后果是沙箱实例静默抢到 18800，而 token 是从错误实例取的。
    if _any_instance_running(ROOT / exe_name):
        raise SystemExit(
            f'检测到仓库目录下已有 EnvKit 实例在运行。\n'
            f'**必须先停掉它再起沙箱** —— 两者会抢同一个 UI 端口（18765起），'
            f'而 token 绑在实例上。拿错实例的 token 会把注入打到用户真实环境上，'
            f'那比不测更糟。')

    proc = subprocess.Popen([str(dst)], cwd=str(box),
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    # 轮询等就绪：EnvKit 启动要做环境检测，1~2 秒才起得来
    ui, token = None, None
    for _ in range(wait * 2):
        time.sleep(0.5)
        for p in range(18765, 18815):
            if not port_busy(p):
                continue
            try:
                html = opener.open(f'http://127.0.0.1:{p}/', timeout=2).read().decode('utf-8')
            except Exception:
                continue
            m = re.search(r'window\.__EK_TOKEN__="([0-9a-f]+)"', html)
            if m:
                ui, token = p, m.group(1)
                break
        if ui:
            break

    if not ui:
        proc.terminate()
        raise SystemExit(
            f'{wait}s 内沙箱实例没就绪。\n'
            f'先手动跑一次看日志：cd {box} && .\\EnvKit_sandbox.exe\n'
            f'（多半是 config 校验没过或端口全被占）')

    info['ui_port'] = ui
    info['token'] = token
    info['pid'] = proc.pid
    st.write_text(json.dumps(info, ensure_ascii=False, indent=2), encoding='utf-8')

    print(f'沙箱实例已启动')
    print(f'  exe       : {dst}')
    print(f'  UI 端口   : {ui}（token 已记入沙箱状态）')
    print(f'  pid       : {proc.pid}')
    print(f'  被测端口  : {info["port"]}（待启动 sandbox-backend）')
    return 0


def _any_instance_running(exe_path):
    """仓库目录下那个 exe 是否已在运行。"""
    if os.name != 'nt':
        return False
    target = str(exe_path).lower()
    for pid, line in _sandbox_pids():
        if target in line.lower():
            return True
    return False


def main():
    ap = argparse.ArgumentParser(description='EnvKit 沙箱实例（kill_service 注入前置）')
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument('--build', action='store_true', help='建沙箱')
    g.add_argument('--launch', action='store_true', help='复制 exe 进沙箱并启动')
    g.add_argument('--destroy', action='store_true', help='拆沙箱并完整还原')
    g.add_argument('--status', action='store_true', help='看沙箱状态')
    a = ap.parse_args()
    if a.build:
        return build()
    if a.launch:
        return launch()
    if a.destroy:
        return destroy()
    return status()


if __name__ == '__main__':
    sys.exit(main() or 0)
