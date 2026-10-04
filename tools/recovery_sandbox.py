# -*- coding: utf-8 -*-
"""沙箱恢复率执行器（v2.5 kill_service 端到端）

## 为什么必须写成"一条命令内起→测→停"

沙箱实例（`EnvKit_sandbox.exe`）是**被测程序**。用 `sandbox.py --launch`
在一条命令里起它、下一条命令再连，就会发现实例已经死了——
本项目的工具链跨命令回收子进程。

实测证据：沙箱日志显示它 16:12:44 正常起来、控制台起在 18765、
环境检测全部跑完（16:12:48 最后一条），然后进程消失、端口不再监听。
**不是启动失败，是被回收。**

这个坑项目里早就记过（`ab_run.py` 的文档字符串里写着同样的道理），
但我第一版 `sandbox.py --launch` 还是犯了一次——
**把"能启动"和"启动后还能活着"当成了一件事。**

所以本脚本是唯一的执行入口：自己起沙箱、跑完恢复率、finally 里拆干净。

## 它做什么

1. 拆掉旧沙箱（如果有）
2. 建沙箱：独立 config + 独立项目目录 + 被测端口 45311
3. 复制 exe 进去并启动，探测 UI 端口与token
4. **先让沙箱 backend 真的起来**（走产品真实的 start_service API）
5. 逐个注入 → 问 AI → 六维判分
6. finally：停沙箱实例 → 拆沙箱 → 还原记忆库

第4 步是 kill_service 的前提：被注入器杀的那个进程，
必须是 EnvKit 自己通过 start_service 起的。**不是我们手动起的。**
手动起的进程不在 svcState 里，注入器按设计拒绝杀（它只认 svcState 的 PID）。

用法：
    python tools/recovery_sandbox.py --dry-run
    python tools/recovery_sandbox.py --only kill_service --repeat 3
    python tools/recovery_sandbox.py            # 全部注入
"""
import argparse
import json
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / 'tools'))

import ab_memory as ab          # noqa: E402
import recovery_rate as rr      # noqa: E402
import sandbox as sb            # noqa: E402

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def sandbox_start_backend(ui, tok, timeout=90):
    """让沙箱的 backend 通过**产品真实的 API** 起来。

    走 /api/program/backend-start 而不是手动跑 exe——
    手动起的进程不在 svcState 里，注入器按设计不会杀它
    （它只认"EnvKit 通过 start_service 派生的进程"，
    这是它避免误杀用户服务的唯一凭据）。

    返回 (ok, 详情)。起不来就如实说，不假装成功。
    """
    req = urllib.request.Request(
        f'http://127.0.0.1:{ui}/api/program/backend-start',
        data=json.dumps({}).encode('utf-8'), method='POST')
    req.add_header('Content-Type', 'application/json')
    req.add_header('X-EnvKit-Token', tok)
    try:
        body = OPENER.open(req, timeout=timeout).read().decode('utf-8')
        return True, body[:200]
    except Exception as e:
        return False, str(e)[:300]


def sandbox_state(ui, tok):
    """取沙箱的运行时状态（已拆掉外层信封）。

    复用 rr.fetch_runtime_state 的解析口径—— 端点返回
    `{"ok":true,"state":{...}}`，状态在 `state` 里。
    之前这里自己写了一遍 `json.loads(...)['services']`，
    **同一个解析错误犯了第二次**（注入器里还有第一次）。
    """
    raw = rr.fetch_runtime_state()
    # fetch_runtime_state 用的是 ab.api，而 ab.BASE/TOKEN 已指向沙箱，
    # 所以这里拿到的就是沙箱状态。仍显式校验一次 ui，
    # 防止有人忘了改 ab.BASE 之后静默读到别处。
    if not ab.BASE.endswith(str(ui)):
        raise RuntimeError(
            f'ab.BASE={ab.BASE} 与沙箱 UI 端口 {ui} 不一致 —— '
            f'读到的是别的实例。这一条必须拦住：'
            f'拿错实例的数据会把评测结论建立在错误环境上。')
    return raw


def wait_backend_listening(ui, tok, want_port, timeout=90):
    """等被测服务真的起来了。**两个条件都要满足**：

      1. `svcState["backend"].pid` 非 0 —— EnvKit 记下了它派生的进程
      2. `want_port` 在 LISTENING —— 进程真的在服务

    ## 第一版只查端口，那是个真bug

    第一版 `return True, be` 只看 `sb.port_busy(want_port)`，
    而 `be` 是**同一次循环里**读到的 svcState。
    go build + go run 要好几秒（实测 go build 4s + go run 5s），
    期间端口当然不通，所以它会一直等——这一半是对的。

    真正的问题在**第二条件缺失**：端口通了也不代表 svcState 写好了。
    实测出现过端口已经在听、但 `/api/runtime/state` 的 services 仍为空
    （那一刻 EnvKit 还没把运行态刷进快照），于是注入器紧接着去读
    `services.backend.pid` 拿到 0，报「没有可安全杀的目标」。

    **端口和 pid 是两个独立的就绪信号，两个都要等。**
    只等一个就宣布就绪，是把"服务能连"当成"产品记录了它"——
    而注入器要的恰恰是后者。
    """
    deadline = time.time() + timeout
    last_pid, last_port = 0, False
    while time.time() < deadline:
        try:
            st = sandbox_state(ui, tok)
        except Exception:
            time.sleep(1.0)
            continue
        be = (st.get('services') or {}).get('backend') or {}
        last_pid = be.get('pid') or 0
        last_port = sb.port_busy(want_port)
        if last_pid and last_port:
            return True, be
        time.sleep(1.0)
    return False, {'pid': last_pid, 'listening': last_port}


def main():
    ap = argparse.ArgumentParser(description='沙箱恢复率（kill_service 端到端）')
    ap.add_argument('--repeat', type=int, default=2)
    ap.add_argument('--only', help='只跑指定注入，逗号分隔')
    ap.add_argument('--dry-run', action='store_true')
    ap.add_argument('--keep', action='store_true', help='跑完不拆沙箱（调试用）')
    ap.add_argument('--min-samples', type=int, default=3)
    ap.add_argument('--allow-writes', action='store_true',
                    help='自动批准确认卡（**仅限 ab_memory.AUTO_APPROVE_WHITELIST '
                         '里的 start_service**）。\n'
                         '不加时恢复类注入会停在确认卡，Autonomous Recovery '
                         '与 False Recovery 两维记为「无样本」——\n'
                         '那是授权闸门在工作，不是 AI 判断错。')
    a = ap.parse_args()

    injections = rr.INJECTIONS
    if a.only:
        ids = {x.strip() for x in a.only.split(',')}
        injections = [i for i in injections if i['kind'] in ids]

    if a.dry_run:
        rr._print_plan(injections, a.repeat, a.min_samples)
        print('\n这条命令会：拆旧沙箱 → 建沙箱 → 复制 exe 启动 → '
              '经API 拉起被测服务 → 注入 → 问 AI → 六维判分 → 拆沙箱')
        print(f'被测服务端口：{sb.SANDBOX_PORT}（与 sandbox-backend/main.go 的 addr 一致）')
        print('\ndry-run：未调用模型。')
        return 0

    if not ab_exe_ok():
        raise SystemExit(ab_exe_hint())

    # ===== 生命周期开始 =====
    results = []
    ui = tok = None
    try:
        print('[1/5] 清理旧沙箱...')
        sb.destroy()

        print('[2/5] 建沙箱...')
        sb.build()

        print('[3/5] 启动沙箱实例...')
        # launch 会自己复制 exe、探测端口、回填 token。
        # 它是子进程调用，所以实例是**这条命令的孙进程**——
        # 生命周期跟着本命令走，这正是我们要的。
        r = subprocess.run(
            [sys.executable, str(ROOT / 'tools' / 'sandbox.py'), '--launch'],
            capture_output=True)
        st = json.loads(sb._state_path().read_text(encoding='utf-8'))
        ui, tok = st.get('ui_port'), st.get('token')
        if not ui or not tok:
            print(r.stdout.decode('utf-8', 'replace'))
            print(r.stderr.decode('utf-8', 'replace'))
            raise SystemExit('沙箱实例没起来')
        print(f'      UI={ui} pid={st.get("pid")}')

        print(f'[4/5] 经产品 API 拉起被测服务（端口 {sb.SANDBOX_PORT}）...')
        # ab.BASE/TOKEN 必须**在第一次读状态之前**设好。
        # rr.fetch_runtime_state 走的是 ab.api，设晚了会读用户真实实例
        # ——那不是"读不到数据"，是**读到了错误环境的数据**，
        # 后者更危险：它看起来完全正常。
        ab.BASE = f'http://127.0.0.1:{ui}'
        ab.TOKEN = tok
        ok, detail = sandbox_start_backend(ui, tok)
        if not ok:
            raise SystemExit(f'拉起被测服务失败：{detail}')
        listening, be = wait_backend_listening(ui, tok, sb.SANDBOX_PORT)
        if not listening:
            # 把两个条件分别报出来。只说"没就绪"的话，
            # 排查时还得自己猜是端口没通还是 pid 没写进快照。
            raise SystemExit(
                f'被测服务没就绪（等 {sb.SANDBOX_PORT} 超时）。\n'
                f'  svcState 记录的 pid : {be.get("pid")}（0 = EnvKit 没记下派生的进程）\n'
                f'  端口是否在监听      : {be.get("listening")}\n'
                f'  **两个条件都必须满足**：端口通 ≠ 产品记录了它，'
                f'而注入器要杀的正是「产品记录的那个」。\n'
                f'  典型原因：go build 失败（看沙箱目录下的 envkit-*.log）、'
                f'端口被占、或 go run 编译报错。\n'
                f'  沙箱日志：{box_hint()}')
        print(f'      已就绪：pid={be.get("pid")} 端口={sb.SANDBOX_PORT} LISTENING')

        print('[5/5] 跑注入...\n')
        if a.allow_writes:
            print('  [允许写操作] 自动批准确认卡，白名单 = '
                  f'{sorted(ab.AUTO_APPROVE_WHITELIST)}')
            print('  ⚠ 这会让 AI 真的执行写操作。沙箱模式下被写的只是一次性服务，'
                  '但请确认沙箱确实在跑。\n')
        results = run_injections(injections, a.repeat, ui, tok,
                                 allow_writes=a.allow_writes)
    finally:
        print('\n[收尾] 拆沙箱...')
        if a.keep:
            print(f'  --keep 已指定，保留沙箱。状态：{sb._state_path()}')
        else:
            sb.destroy()

    if not results:
        print('\n没有产生任何结果：全部注入都失败了。**不产出报告**——'
              '空数据写成 0.000 就是谎报。')
        return 1

    report_and_print(results, a.min_samples)
    return 0


def ab_exe_ok():
    """被测 exe 必须存在，**且比所有 .go 源码新**。

    ## v2.6 修正：原来只看文件是否存在

    实测踩过（而且它**没有报任何错**）：
    我改了 `aibackup.go`（sha256 真复算 + 修大文件误报截断），
    `go build -o EnvKit.exe`，然后跑评测 ——
    **测的是三小时前构建的旧 exe。**

    根因：`recovery_sandbox.py` 读的是 `EnvKit_ab.exe`，
    而我构建时用的是 `EnvKit.exe`（另一个名字，仓库里同时存在两个）。
    `ab_exe_ok()` 只检查 `EnvKit_ab.exe` 存在，于是顺利通过。

    ## 为什么这比"测到旧产品"更严重

    它会让**所有数字都失去意义，而且看不出异常**：
    报告正常生成、维度正常汇总、模型正常调用 ——
    唯一的破绽是 AI 引用了旧措辞（"校验和旁挂：缺失或不一致"）。
    如果我没顺手核对 exe 时间戳，这轮数据会被当成"修复无效"写进报告，
    然后下一步就去改判据 —— **而真相是产品根本没被测到。**

    这与 v2.4 那条「开了开关 ≠ 有东西注入」同源：
    **装置看起来在跑，但它跑的不是你以为的那个东西。**

    ## 判据：mtime 比最新 .go 新

    粗糙但可靠。Go 的构建缓存让增量构建很快，
    而"改了源码没重新构建被测exe"是一个真实且高频的错误。
    宁可误报（让人重构建一次），不可静默测旧版本。
    """
    exe = ROOT / 'EnvKit_ab.exe'
    if not exe.exists():
        return False
    srcs = [p for p in ROOT.glob('*.go') if p.name != '*_test.go']
    if not srcs:
        return True
    newest = max(p.stat().st_mtime for p in srcs)
    if exe.stat().st_mtime < newest:
        return False
    return True


def ab_exe_hint():
    """ab_exe_ok 为 False 时，把**具体原因**说清楚。

    不给线索等于让每个使用者重新复现一遍——
    而"跑一次才知道"在批量评测里根本做不到（v2.5 教训 5.4）。
    """
    exe = ROOT / 'EnvKit_ab.exe'
    if not exe.exists():
        return ('找不到 EnvKit_ab.exe。\n'
                '  构建：go build -trimpath -ldflags "-s -w" -o EnvKit_ab.exe .\n'
                '  **注意名字必须是 EnvKit_ab.exe** —— 本脚本只认这一个；\n'
                '  构建成 EnvKit.exe 会被当成"exe 不存在"，'
                '而更糟的情况是仓库里已有一个旧的 EnvKit_ab.exe，'
                '于是**静默测旧版本**。')
    srcs = [p for p in ROOT.glob('*.go') if p.name != '*_test.go']
    if srcs:
        newest = max(srcs, key=lambda p: p.stat().st_mtime)
        age = exe.stat().st_mtime
        return (f'EnvKit_ab.exe 比源码旧 —— 被测对象不是当前代码。\n'
                f'  最新源码：{newest.name}\n'
                f'  exe mtime：{age}\n'
                f'  重建：go build -trimpath -ldflags "-s -w" -o EnvKit_ab.exe .\n'
                f'  **继续跑会测一个过期版本，所有数字都不可信，'
                f'而报告里看不出任何异常。**')
    return 'EnvKit_ab.exe 状态未知，请手动重建。'


def box_hint():
    """沙箱日志路径提示。

    排查失败必须能直接看到日志——第一版跑完就把沙箱拆了，
    日志随目录一起没了，害我重跑一次带 --keep 才看到
    「go build成功、服务确实起来了」。**装置自己故障时不给线索，
    等于让每个使用者都要重新复现一遍。**
    """
    try:
        st = json.loads(sb._state_path().read_text(encoding='utf-8'))
        d = Path(st['dir'])
        logs = sorted(d.glob('envkit-*.log'))
        return str(logs[-1]) if logs else f'{d}（暂无日志）'
    except Exception:
        return '（沙箱状态不可读）'


def run_injections(injections, repeat, ui, tok, allow_writes=False):
    """在沙箱实例上跑注入。ab.TOKEN/BASE 已在 main 里指向沙箱。"""
    results = []
    for inj in injections:
        print(f"\n=== {inj['kind']}（{inj['class']}）===")
        for r in range(repeat):
            # 每一轮注入前先确保被测服务在跑。
            #
            # 上一轮 AI 可能没恢复成功（那正是要测的），此时服务仍是死的，
            # 注入器按设计拒绝杀一个没在跑的东西——报「没有可安全杀的目标」。
            # 但那是**上一轮的结果**，不是这一轮的装置故障。
            # 不复位就继续跑，第二轮会变成 ERROR，
            # 于是"AI 恢复率低"这件事被"装置没复位"掩盖掉。
            if inj['expect_autonomous']:
                if not sb.port_busy(sb.SANDBOX_PORT):
                    print('  （上一轮之后服务未恢复，先复位再注这一轮）')
                    ok2, d2 = sandbox_start_backend(ui, tok)
                    if ok2:
                        wait_backend_listening(ui, tok, sb.SANDBOX_PORT, timeout=60)
                    if not sb.port_busy(sb.SANDBOX_PORT):
                        print(f'  run{r+1} 复位失败，跳过：{d2[:150]}')
                        results.append(rr.error_row(
                            inj, r + 1,
                            f'上一轮未恢复且复位失败，本轮未测（不是AI 的问题）'))
                        continue

            inj_ok, inj_why = rr.apply_injection(inj['kind'])
            if not inj_ok:
                # 注入失败必须进结果并标 ERROR，不能静默跳过——
                # "跑了 2 次都没问题"和"一次都没跑成"在报告里长得一样，
                # 这正是 v2.4 把"没测到"写成 0.000 的那种谎报。
                print(f'  run{r+1} 注入失败：{inj_why[:200]}')
                results.append(rr.error_row(
                    inj, r + 1, f'注入失败：{inj_why[:300]}'))
                continue
            since = time.strftime('%Y-%m-%d %H:%M:%S')
            time.sleep(0.5)
            before = sandbox_health()
            try:
                text, events, dur = ab.chat([{'role': 'user', 'content': inj['ask']}],
                                             allow_writes=allow_writes)
            except SystemExit:
                raise
            except Exception as e:
                # 采集出错必须显式标记，不能让它变成"0 工具所以 FAIL"——
                # 那是把管线问题算成产品缺陷。
                print(f'  run{r+1} 采集失败：{str(e)[:200]}')
                results.append(rr.error_row(
                    inj, r + 1, f'采集失败：{str(e)[:300]}'))
                continue
            finally:
                rr.clear_injections()

            # 恢复类故障要等一会儿：AI 调 start_service 后服务要时间起来。
            # 这里不能用 rr.objective_health 的判据——它假设沙箱里有
            # 数据库和链端。沙箱的客观判据只有一个：**被测端口是否 LISTENING**。
            if inj['expect_autonomous']:
                wait_backend_listening(ui, tok, sb.SANDBOX_PORT, timeout=30)
                time.sleep(0.5)
            after = sandbox_health()
            tinfo = rr.human_intervention(since)
            verdicts = rr.judge(inj, text, events, tinfo, after,
                                health_fn=sandbox_healthy)
            used = ab.tool_seq(events)
            # 确认卡必须记录 —— 不记的话，"AI 没调 start_service" 有两种
            # 截然不同的原因，而报告里看起来一模一样：
            #   (a) 它判断不需要重启（诊断错）
            #   (b) 它判断需要但弹卡等确认（**判分器不自动点**）
            # v2.5 修完诊断方向后实测到的正是 (b)：AI 原话是
            # 「确认进程不在了，复验通过过，说明是被外部终止的，我直接重新拉起」——
            # 判断完全正确，却因为 start_service 是写操作而停在确认卡上。
            # 把它记成"没恢复"是**冤枉了 AI**，而且会让人去修错的地方。
            confirm = any(e.get('type') == 'confirm_request' for e in events)
            marks = ' '.join(f"{k[:4]}={v['status']}" for k, v in verdicts.items())
            if confirm:
                marks += ' [弹确认卡]'
            print(f'  run{r+1}  {dur:.0f}s  工具={used}')
            print(f'       {marks}')
            results.append({
                'injection': inj['kind'], 'injection_class': inj['class'],
                'expect_autonomous': inj['expect_autonomous'],
                'run': r + 1, 'tools': used, 'seconds': round(dur, 1),
                'verdicts': verdicts, 'trace_data_missing': tinfo is None,
                'health_before': before, 'health_after': after,
                'confirm_requested': confirm,
                'answer_head': text[:300],
            })
    return results


def sandbox_health():
    """沙箱视角的客观复验。

    ## 为什么不能直接用 rr.objectively_healthy

    通用判据看三样东西：error 级 issue、数据库连通、web/backend 的 phase。
    沙箱里**没有数据库也没有链端**，那三样要么恒为未知、要么恒为 stopped。
    套通用判据的结果是：即使被测服务好好地跑在 45311，
    也会被判成"环境不健康" → Autonomous Recovery Rate 恒为 0，
    而那不是产品缺陷，是**判据用错了对象**。

    沙箱的客观判据只有一个，而且必须唯一：
    **被测服务是否在 45311 上 LISTENING。**
    它是被测对象的健康定义，不是整套环境的健康定义。

    这里不采信 AI 的措辞，也不采信 svcState 的 Running 标志——
    那个只证明进程派生了，不证明端口在听。
    """
    return {
        'ok': True,
        'mode': 'sandbox',
        # 唯一判据：被测端口在不在听
        'sandbox_port': sb.SANDBOX_PORT,
        'listening': sb.port_busy(sb.SANDBOX_PORT),
    }


def sandbox_healthy(h):
    """沙箱版「是否健康」= 被测端口在监听。"""
    if not h.get('ok'):
        return None
    return bool(h.get('listening'))


def report_and_print(results, min_samples):
    dims = rr.aggregate(results)
    by_inj = {}
    for inj in rr.INJECTIONS:
        rs = [r for r in results if r['injection'] == inj['kind']]
        if not rs:
            continue
        by_inj[inj['kind']] = {
            'n': len(rs), 'injection_class': inj['class'],
            'investigated': sum(1 for r in rs
                                if r['verdicts'].get('investigation', {}).get('passed')),
            'safe': sum(1 for r in rs
                        if r['verdicts'].get('safe_handling', {}).get('passed')),
            'recovered': sum(1 for r in rs
                             if r['verdicts'].get('recovery', {}).get('passed')),
            'sample_insufficient': len(rs) < min_samples,
        }

    print('\n' + '=' * 68)
    print('===== 六维汇总（沙箱）=====')
    print('（分母各自独立，不相加也不平均）')
    print('=' * 68)
    for key, en, cn, q, hb in rr.DIMS:
        d = dims[key]
        arrow = '↑ 越高越好' if hb else '↓ 越低越好'
        val = '无样本' if d['no_sample'] else f"{d['rate']:.3f}"
        print(f'\n{cn} {en}  [{arrow}]')
        print(f'  {q}')
        print(f'  = {val}   （{d["pass"]}/{d["n_decided"]} 有结论，'
              f'另有 {d["not_triggered"]} 项未触发、{d["error"]} 项采集错误）')
        if d['no_sample']:
            print('  ⚠ 无样本：这个数字**不代表能力**')

    print('\n分注入类型：')
    for k, v in by_inj.items():
        warn = '  ⚠ 样本不足' if v['sample_insufficient'] else ''
        print(f"  {k} [{v['injection_class']}]：调查 {v['investigated']}/{v['n']}"
              f" · 安全处置 {v['safe']}/{v['n']}"
              + (f" · 自主恢复 {v['recovered']}/{v['n']}"
                 if v['injection_class'] == 'autonomous' else '')
              + warn)

    report = {
        'mode': 'sandbox',
        'sandbox_port': sb.SANDBOX_PORT,
        'min_samples': min_samples,
        'total_runs': len(results),
        'dimensions': dims,
        'by_injection': by_inj,
        'caveats': rr_dims_caveats(),
        'results': results,
    }
    sb.REPORT = sb.ROOT / 'docs' / 'eval' / 'sandbox-recovery-report.json'
    sb.REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2),
                         encoding='utf-8')
    print(f'\n完整数据：{sb.REPORT}')


def rr_dims_caveats():
    return [
        '沙箱模式：被测服务是 eval/fixtures/sandbox-backend，端口 45311。',
        'kill_service 杀的是 EnvKit 通过 start_service 派生的进程'
        '（svcState 里的 PID），不是扫端口杀进程。',
        '六维分母不同，不可相加、不可平均。',
        'NOT_TRIGGERED / ERROR 不进分母——它们不是失败，是没有结论。',
        '无样本报 null，不报 0.000。',
    ]


if __name__ == '__main__':
    sys.exit(main() or 0)
