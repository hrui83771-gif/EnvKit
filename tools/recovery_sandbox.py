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

# **stdout 必须显式设 UTF-8。**
#
# 踩过：Windows 上 PowerShell 给的是 GBK，于是
#   print('  ⚠ 这会让 AI 真的执行写操作…')
# 抛 UnicodeEncodeError —— 'gbk' codec can't encode character '\u26a0'。
#
# 后果不是「少打一个字」，而是**整个评测一行都没跑就崩了**：
# 崩在 `[5/5] 跑注入` 之后的第一行，
# 看起来像「装置有问题」，而它只是控制台编码。
#
# `recovery_selftest.py` 早就设了，这里漏了。
# 凡是有非 ASCII 输出的评测脚本都要设 —— 换终端/重定向就会踩。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:      # 旧版 Python 或已被包装过
    pass

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
    # `--only` 是否指定了类 —— 决定报告写「每类一个文件」还是「写总表」。
    # 留着这个信息，别在过滤 injections 时把它丢掉（那样就分不清是哪种模式）。
    only_kinds = bool(a.only)
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

    report_and_print(results, a.min_samples, only_kinds=only_kinds)
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
            #
            # ## 这里曾经写着 `if inj['expect_autonomous']` —— 已去掉
            #
            # 条件挂在「是否期待自主恢复」上是错的：
            # **凡是要动手的注入（hold_port / crash / stale_log / fake_listen）
            # 都需要一个活着的服务**，崩了就得复位。
            #
            # 实测踩过：v2.7 把 crash_on_next 从 autonomous 改成 safe_only
            # （因为它**谁都救不了**），结果它崩掉之后没人复位，
            # run2/run3 连着报「沙箱服务没在 45311 上响应」——
            # **一个判据改动，悄悄让 2/3 的样本消失了。**
            #
            # 与 expect_autonomous 无关，只看「服务在不在」。
            if not sb.port_busy(sb.SANDBOX_PORT):
                print('  （上一轮之后服务未恢复，先复位再注这一轮）')
                # **复位之前必须先清标记文件**，否则复位起来的服务
                # 一起来就崩 —— 于是「复位失败」，run2/run3 连着ERROR。
                #
                # 实测踩过：crash_on_next run1 把服务搞崩后，
                # run2/run3 都报「复位失败：crash_after_start」——
                # 而真正的原因是**崩溃开关还武装着**，
                # 拉起来的新服务只是又崩一次。
                #
                # 「复位」的前提是「回到干净状态」，
                # 不清开关那不叫复位，叫「再崩一次」。
                _cleared = rr.clear_marker_files()
                if _cleared:
                    print(f'    已清除残留开关标记：{_cleared}')
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
                _row = rr.error_row(
                    inj, r + 1, f'采集失败：{str(e)[:300]}')
                # ---- 区分「被确认卡掐断」与「真的采集故障」 ----
                #
                # `ab.chat` 抛错时会把 events 挂在异常上（见 ab_memory）。
                # **实测踩过**：一条采集失败的报告里事件类型是
                # `['reasoning×934', ..., 'confirm_request×1', 'done×1']`
                # —— 它是被确认卡掐断的（模型正要发写工具，SSE 结束），
                # 而不是我原先以为的「上游故障」。
                #
                # 分不清这两者，就会「产品掐断」被记成「模型/上游问题」，
                # 然后所有人去改错的地方。
                _ev = getattr(e, 'events', None) or []
                _confirm = any(x.get('type') == 'confirm_request'
                               for x in _ev)
                _row['confirm_requested'] = _confirm
                _row['cut_by_confirm'] = _confirm
                if _confirm:
                    _row['collect_error'] = (
                        '被确认卡掐断（模型正要发写工具，本轮 SSE 结束）'
                        + str(e)[:240])
                results.append(_row)
                continue
            finally:
                # **撤销没清干净要说出来**。
                # 静默返回 None 的话，「上一个注入还占着端口」这件事
                # 会在下一轮变成「注入失败」——
                # 而报告里那两行看起来像被测对象的问题。
                _left = rr.clear_injections()
                if _left:
                    print(f'    ⚠撤销有残留：{_left}'
                          f'（下一轮注入可能撞上上一轮的东西）')

            # 恢复类故障要等一会儿：AI 调 start_service 后服务要时间起来。
            # 这里不能用 rr.objective_health 的判据——它假设沙箱里有
            # 数据库和链端。沙箱的客观判据只有一个：**被测端口是否 LISTENING**。
            if inj['expect_autonomous']:
                wait_backend_listening(ui, tok, sb.SANDBOX_PORT, timeout=30)
                time.sleep(0.5)
            after = sandbox_health()
            tinfo = rr.human_intervention(since)
            _jd = rr.judge(inj, text, events, tinfo, after,
                                health_fn=sandbox_healthy)
            verdicts = _jd['dims']
            _verdict_meta = _jd
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
                # **结构块存在结果顶层，不塞进 verdicts** ——
                # verdicts 的每个值都是「一个维度」，有形状约定，
                # 混进元信息会破坏它（第一版塞进去导致遍历时 KeyError 崩掉）。
                'verdict': _verdict_meta.get('verdict'),
                'verdict_used': _verdict_meta.get('verdict_used'),
                'health_before': before, 'health_after': after,
                'confirm_requested': confirm,
                'answer_head': text[:300],
                # **末尾也要存** —— 与 answer_head 对称，缺一不可。
                #
                # 实测踩过：全量跑完覆盖率 26/33，门禁说
                # 「有 5 次模型该输出块却没输出」。
                # 但那5 次的 verdict 全是 None，
                # 而**报告只有 `answer_head`（前 300 字）**——
                # 结构块在回答末尾，看不到末尾就无法区分
                # 「模型没输出」与「输出了但格式不对」。
                #
                # 我为此查了三轮、写了两个探针、全是猜 ——
                # 根因不是判据不对，是**要看的东西没被记下来**。
                #
                # 存末尾不是为了好看，是为了让「覆盖率」这个数字
                # **能被独立核对**。没有它，谁都只能猜。
                'answer_tail': text[-600:] if len(text) > 600 else text,
                'answer_len': len(text),
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


def report_and_print(results, min_samples, only_kinds=False):
    """打印汇总并落盘。

    ## `only_kinds` 为什么必须是参数而不是读全局

    它定义在 `main()` 里。第一版我在 `main` 里赋值、
    却在这个函数里直接用 —— `NameError` 直到跑完整轮评测才暴露
    （`report_and_print` 在最后才被调用）。

    > **跨函数用值就得传参。**
    > 同理：写在 `main` 里、却被别处读取的变量，
    > 编译器不会拦你，只会在最晚的时刻炸。
    """
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
        'verdict_coverage': rr_verdict_coverage(results),
        'results': results,
    }

    # ## 按类分文件输出
    #
    # 原来只有一份 `sandbox-recovery-report.json`，**每次跑都覆盖**。
    # 于是「分次采集」必然丢数据——
    # v2.7 那轮为了绕开后台任务 10 分钟上限改成逐类跑，
    # 结果留档的报告只剩最后一类（`total_runs = 3`），
    # **九类的原始数据无法二次核对**（写总结文档时才暴露）。
    #
    # **逐类规避超时的做法，代价是丢掉了可复现性。**
    # 而「数字能不能信」恰恰是那一轮的主题——
    # 一份不能复现的数字，可信度天然打折。
    #
    # 现在：`--only` 指定了类就写 `report-<kind>.json`（**不覆盖总表**），
    # 全量跑则照旧写总表并额外留一份带时间戳的归档。
    kinds = sorted({r['injection'] for r in results if 'injection' in r})
    out_dir = sb.ROOT / 'docs' / 'eval'
    if only_kinds:
        # 分次采集：每类一个文件，**绝不覆盖别人的**
        for k in kinds:
            p = out_dir / ('report-%s.json' % k)
            sub = [r for r in results if r.get('injection') == k]
            p.write_text(json.dumps({
                'mode': 'sandbox',
                'sandbox_port': sb.SANDBOX_PORT,
                'min_samples': min_samples,
                'total_runs': len(sub),
                'injection': k,
                # **本次采集的范围**。dimensions 是**本次跑的各类**的汇总，
                # 不是九类的总账——写成显式字段，别让人误读成全量。
                'scope': {'kinds': kinds, 'partial': len(kinds) < len(rr.INJECTIONS)},
                'dimensions': dims,
                'by_injection': by_inj,
                'caveats': rr_dims_caveats(),
                'verdict_coverage': rr_verdict_coverage(sub),
                'results': sub,
            }, ensure_ascii=False, indent=2), encoding='utf-8')
            print(f'本类数据：{p}')
        print('（分次采集：总表 sandbox-recovery-report.json 未改动，'
              '避免覆盖别人的数据）')
        print(f'（本次只跑了 {len(kinds)}/{len(rr.INJECTIONS)} 类，'
              f'各文件里的 dimensions 是这 {len(kinds)} 类的汇总，不是全量总账）')

    # 结构块覆盖率：**印出来**，不要只埋在报告里。
    #
    # 判据已经改成「结构块优先」——
    # 覆盖率低就意味着判据实际上在退回词表，
    # **那这次改造就没生效**，而报告里容易被忽略。
    _cov = rr_verdict_coverage(results)
    print(f'\n结构块覆盖率（v2.9）：{_cov["used_by_judge"]}/{_cov["runs"]}'
          f' = {_cov["coverage"]}'
          f'（块出现 {_cov["block_present"]} 次、合法 {_cov["block_valid"]} 次）')
    if _cov['runs'] and _cov['used_by_judge'] == 0:
        print('  ⚠ **判据一次都没走结构化路径** —— 模型没照做。'
              '该改产品侧的指令，不是改判据。')
    else:
        sb.REPORT = out_dir / 'sandbox-recovery-report.json'
        sb.REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2),
                             encoding='utf-8')
        # 带时间戳归档一份：全量跑的结果不该被下一次全量跑覆盖掉
        stamp = time.strftime('%Y%m%d-%H%M%S')
        arch = out_dir / ('sandbox-recovery-%s.json' % stamp)
        arch.write_text(json.dumps(report, ensure_ascii=False, indent=2),
                        encoding='utf-8')
        print(f'\n完整数据：{sb.REPORT}')
        print(f'带时间戳归档：{arch}')


def rr_verdict_coverage(results):
    """统计结构化结论块的覆盖率（v2.9）。

    ## 为什么这个数字必须进报告

    判据改成「结构块优先」之后，
    **模型不输出块 → 判据全退回词表 → 这次改造等于没做**。
    而报告里只有 `answer_head`（**截断**的），
    结构块在回答末尾 —— 于是从报告**根本看不出覆盖率**。

    实测踩过：`slow_start` 三次的 `answer_head` 都解析为 `None`，
    而 `why` 里明明写着「结构块 {...}」。
    **两个都是真的，但只有后者可信。**

    所以判据把解析结果存进结果顶层，
    这里直接统计 —— **覆盖率必须是报告里的一等公民**。

    ## 为什么必须区分「模型没照做」与「产品把生成掐断了」

    写工具会转确认卡，此时 `ai_loop.go` **直接结束本轮 SSE**
    （`sseDoneConfirm` 后 `return`，等用户点头再带 confirm 续接）。
    所以模型的话**真的没说完** —— 结构块当然不在。

    实测：`hang_service` 三次里有 2 次 `confirm_requested=True`，
    `answer_head` 断在「我现在发起（需你点确认）：」。
    **那不是模型忘了输出，是它没机会输出。**

    把这种轮次算进分母，等于**让模型为产品的行为负责**——
    数字会一路偏低，然后所有人去调判据，
    而真正该改的是「确认卡要不要把话说完」。
    所以这里单独报 `cut_by_confirm`，主覆盖率按**可判定的轮次**算。
    """
    n = len(results)
    used = sum(1 for r in results if r.get('verdict_used'))
    present = sum(1 for r in results if r.get('verdict'))
    valid = sum(1 for r in results
                if (r.get('verdict') or {}).get('valid'))

    # 被确认卡掐断的轮次：产品结束了本轮，模型没机会把话说完。
    # **不是采集失败**（文本有内容），也不是模型不照做。
    #
    # `cut_by_confirm=True` 覆盖两种形态：
    #   · 正常路径（有正文，卡在「请点确认执行」）
    #   · 采集失败路径（模型一个字都没输出就被掐断，见 error_row 分支）
    # **后者曾被误记成「上游故障」** —— 事件类型里明明写着
    # `confirm_request×1`，分不清就会去改错的地方。
    cut = [r for r in results
           if r.get('cut_by_confirm')
           or (r.get('confirm_requested') and not r.get('verdict'))]

    # 采集失败（error_row）：**根本没测到**，不能进任何分母。
    # ERROR 的语义是「没有结论」，不是「结论是不合格」。
    #
    # 不排除的话，一次采集失败会让覆盖率看起来像模型不照做——
    # 那是把管线问题算成产品缺陷。
    #
    # ⚠️ **但被确认卡掐断的那次不算「采集失败」** ——
    # 它是被产品掐断的，性质同`cut`。
    # 不分开就会**同一个轮次被算进两个排除桶**，
    # 分母被重复扣掉，coverage_judgeable 反而虚高。
    err = [r for r in results
           if r.get('collect_error') and not r.get('cut_by_confirm')]

    judgeable = n - len(cut) - len(err)
    used_j = sum(1 for r in results
                 if r.get('verdict_used') or r in cut)

    return {
        'runs': n,
        'block_present': present,       # 块在（含非法值）
        'block_valid': valid,           # 块在且字段合法
        'used_by_judge': used,          # 判据实际走了结构化路径
        'coverage': ('%.3f' % (used / n)) if n else None,
        # 下面几个是「把话说清楚」的口径
        'cut_by_confirm': len(cut),         # 被确认卡掐断，块不可能在
        'collect_errors': len(err),         # 采集失败，没测到
        'judgeable_runs': judgeable,        # 真正能要求模型输出块的轮次
        'coverage_judgeable': (
            '%.3f' % (used_j / judgeable)) if judgeable else None,
        'by_injection_cut': sorted({
            '%s run%s' % (r.get('injection'), r.get('run')) for r in cut
        }),
        'by_injection_error': sorted({
            '%s run%s' % (r.get('injection'), r.get('run')) for r in err
        }),
        'note': ('used_by_judge/runs 含「被确认卡掐断」与「采集失败」的轮次，'
                 '所以它天然偏低；**coverage_judgeable 才是'
                 '「模型该输出块时有没有输出」**。'
                 '两者都要低才是真问题——前者低说明模型不照做'
                 '（改产品侧指令），后者低说明模型照做了但没机会'
                 '（改确认卡的收尾，别去动判据）。'
                 '采集失败不计入任何分母：**没测到 ≠ 测了不合格**。'),
    }


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
