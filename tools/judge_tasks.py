# -*- coding: utf-8 -*-
"""EnvKit 任务成功率 / 首次成功率 判分器执行器（v2.4 最后一项）

这一项解锁八项指标里最后两个 🔴：任务成功率与首次成功率。
v2.1 与 v2.2 两份基线都写"缺 Ground Truth + 判分器"，
v2.4 交付了任务集（`eval/tasks/tasks.json`）与A/B 执行器，
但**判分器一直没有独立跑过全量10 题** —— 冒烟集是"能力是否正确"，
这里是"真实任务成功率是多少"，两者不能互相替代。

## 与冒烟集、与 A/B 的区别（这是最容易混的地方）

| | 冒烟集（`eval_smoke_test.go`） | 本脚本 |
|---|---|---|
| 问什么 | 某项能力对不对 | 真实任务能不能完成 |
| 断言对象 | 内部函数返回值 | AI 的实际行为（工具序列 + 回答） |
| 需要模型 | 不需要 | **需要，真实调用** |
| 产出 | 通过/不通过 | **百分比** |

**本脚本会产生百分比，冒烟集不会。** 这是它存在的唯一理由。
口径 §2.1/§2.2：
    任务成功率   = 完成全部 criteria 的任务数 / 任务总数
    首次成功率 = 第一次尝试即通过的任务数 / 任务总数

## 判分为什么可信

判据全部来自 `tasks.json`，形式只有两类：
    ① AI 有没有调用某个工具（从 SSE 事件流读，客观）
    ② AI 的回答含不含某些词 / 不含某些词（从 SSE 文本读，客观）
**没有一条依赖"回答好不好"的主观判断。**
换个人来跑，判分结果完全一样——这是评测可复现的前提。

## 为什么"首次成功率"要跑多轮

首次成功率问的是"第一次就做对"。如果只跑一次，
它和任务成功率是同一个数。所以每题跑 N 次：
    第1 次通过 → 计入首次成功
    任一次通过 → 计入任务成功
两次之差就是"需要重试的比例"，用于衡量首次判断的准确度。

用法：
    python tools/judge_tasks.py --dry-run      # 只列计划
    python tools/judge_tasks.py --repeat 3     # 真实调用（10 题 × 3 = 30 次）
    python tools/judge_tasks.py --only T01,T02 # 只跑指定题
"""
import argparse
import json
import statistics
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / 'tools'))

# 复用 A/B 的采集与判分层——**不重复实现**。
# 两处各自写一遍 SSE 解析的话，改了一处忘了另一处，
# 判分口径就会悄悄漂移（A/B 说通过、判分器说失败）。
import ab_memory as ab

TASKS_JSON = ROOT / 'eval' / 'tasks' / 'tasks.json'
REPORT = ROOT / 'docs' / 'eval' / 'success-rate-report.json'

# 危险任务：会触发写操作确认卡。判分器不自动确认——
# 自动点确认等于替用户做决定，而"AI 是否需要确认"本身就是要观察的行为。
# 这类题判据只看"AI 是否如实报告 / 是否识别出边界"，不看执行结果。
WRITE_TASKS = {'T10'}


def run_task(task, repeat):
    """跑一道题 repeat 次，返回每次的记录。

    `task['injects']` 声明了要在答题前注入的异常。
    注入必须在**答题之前**、撤销必须在**全部跑完之后**——
    中途撤销会让后两次跑的环境与第一次不同，A/B 就失去可比性了。
    """
    injects = task.get('injects') or []
    rows = []
    for i in range(repeat):
        try:
            text, events, dur = ab.chat([{'role': 'user', 'content': task['ask']}])
        except SystemExit:
            raise
        except Exception as e:
            rows.append({'task': task['id'], 'run': i + 1, 'error': str(e)[:200]})
            print(f'  ERR {task["id"]} run{i+1}  {e}')
            continue
        ok, detail = ab.judge(task, text, events)
        # min_explore：这题要求至少 N 次探索才算是"真答出来了"。
        # 目的不是判成败，而是**标记哪些题真的有区分度**——
        # 探索轮数为 0 的题说明画像已能直接回答，它不产生信息。
        explore = ab.explore_rounds(events)
        row = {
            'task': task['id'], 'task_name': task['name'], 'run': i + 1,
            'success': ok,
            'explore_rounds': explore,
            'total_rounds': ab.total_rounds(events),
            'seconds': round(dur, 1),
            'tools': ab.tool_seq(events),
            'criteria': detail,
            'answer_head': text[:300],
            'error_events': [e.get('text', '')[:200] for e in events
                             if e.get('type') == 'error'],
            'confirm_requested': any(e.get('type') == 'confirm_request' for e in events),
        }
        me = task.get('min_explore')
        if me:
            row['min_explore'] = me
            row['has_discriminating_power'] = explore >= me
        rows.append(row)
        flag = 'PASS' if ok else 'FAIL'
        cr = ' [弹确认卡]' if row['confirm_requested'] else ''
        dp = ''
        if me:
            dp = f' 探索{explore}(需≥{me}){"✓" if row["has_discriminating_power"] else "✗"}'
        else:
            dp = f' 探索{explore}'
        print(f'  {flag} {task["id"]} run{i+1} {dp} {row["seconds"]}s{cr}')
        if row['error_events']:
            print(f'       ⚠ 报错事件：{row["error_events"]}')
    return rows


def apply_injection(kind):
    """注入一种异常，返回 (成功, 消息)。"""
    r = subprocess.run(
        [sys.executable, str(ROOT / 'tools' / 'fault_inject.py'), '--inject', kind, '--go'],
        capture_output=True, text=True)
    return r.returncode == 0, (r.stdout or r.stderr).strip().splitlines()[0] if (r.stdout or r.stderr) else ''


def clear_injections():
    subprocess.run([sys.executable, str(ROOT / 'tools' / 'fault_inject.py'), '--undo'],
                   capture_output=True, text=True)


def summarize(rows, repeat):
    """按口径算三个数：任务成功率、首次成功率、需重试比例。"""
    tasks = sorted({r['task'] for r in rows if 'error' not in r})
    if not tasks:
        return {}

    per_task = {}
    n_task_ok = 0        # 任一次通过 → 计入任务成功
    n_first_ok = 0       # 第一次就通过 → 计入首次成功
    for t in tasks:
        rs = [r for r in rows if r['task'] == t]
        if not rs:
            continue
        first_ok = bool(rs[0].get('success'))
        any_ok = any(r.get('success') for r in rs)
        if any_ok:
            n_task_ok += 1
        if first_ok:
            n_first_ok += 1
        per_task[t] = {
            'name': rs[0].get('task_name', ''),
            'first_run_ok': first_ok,
            'any_run_ok': any_ok,
            'runs': len(rs),
            'success_count': sum(1 for r in rs if r.get('success')),
            'explore_median': ab.median([r['explore_rounds'] for r in rs
                                         if 'explore_rounds' in r]),
            'seconds_median': round(ab.median([r['seconds'] for r in rs
                                               if 'seconds' in r]) or 0, 1),
        }

    n = len(per_task)
    return {
        'n_tasks': n,
        'repeat': repeat,
        'task_success_rate': round(n_task_ok / n, 3),
        'first_try_success_rate': round(n_first_ok / n, 3),
        'retry_needed': round((n_task_ok - n_first_ok) / n, 3),
        'n_task_ok': n_task_ok,
        'n_first_ok': n_first_ok,
        'per_task': per_task,
    }


def main():
    ap = argparse.ArgumentParser(description='任务成功率 / 首次成功率 判分器')
    ap.add_argument('--repeat', type=int, default=3, help='每题跑几次（默认 3）')
    ap.add_argument('--only', help='只跑指定题，逗号分隔，如 T01,T02')
    ap.add_argument('--dry-run', action='store_true', help='只列计划，不调模型')
    a = ap.parse_args()

    spec = json.loads(TASKS_JSON.read_text(encoding='utf-8'))
    tasks = spec['tasks']
    if a.only:
        ids = {x.strip() for x in a.only.split(',')}
        tasks = [t for t in tasks if t['id'] in ids]

    cfg = ab.api('/api/ai/config')
    print(f"模型：{cfg.get('model')}")
    print(f'题集：{len(tasks)} 题 × {a.repeat} 次 = {len(tasks) * a.repeat} 次真实调用\n')
    for t in tasks:
        mark = ''
        if t['id'] in WRITE_TASKS:
            mark = ' [会触发写操作确认]'
        if t.get('injects'):
            mark += f' [注入异常: {",".join(t["injects"])}]'
        if t.get('min_explore'):
            mark += f' [需探索≥{t["min_explore"]}]'
        print(f"  {t['id']} {t['name']}{mark}")
        print(f"      问：{t['ask']}")
        print(f"      验：{t['why']}")
        for c in t['criteria']:
            print(f"      · {c['kind']} {c.get('values', [])}")

    if a.dry_run:
        print('\ndry-run：未调用模型。去掉 --dry-run 真正执行。')
        return
    if not cfg.get('key_set'):
        raise SystemExit('AI 未配置 key，做不了真实调用评测')

    rows = []
    # 有注入的题先统一注入，**跑完全部再撤销**——
    # 中途撤销会让后几次跑的环境与第一次不同，成功率就不可比了。
    injected = set()
    for t in tasks:
        for k in (t.get('injects') or []):
            if k not in injected:
                okk, msg = apply_injection(k)
                print(f'注入 {k}：{"成功" if okk else "失败"} {msg}')
                injected.add(k)
    try:
        for t in tasks:
            print(f'\n--- {t["id"]} ---')
            rows.extend(run_task(t, a.repeat))
    finally:
        # 用 try/finally：即使判分中途报错也要撤销，
        # 否则注入的端口会一直占着（它能占一小时）
        if injected:
            print('\n撤销注入...')
            clear_injections()

    summary = summarize(rows, a.repeat)
    print('\n===== 汇总 =====')
    print(f"任务成功率  ：{summary['task_success_rate']:.3f} "
          f"（{summary['n_task_ok']}/{summary['n_tasks']} 题在 {a.repeat} 次内至少通过一次）")
    print(f"首次成功率：{summary['first_try_success_rate']:.3f} "
          f"（{summary['n_first_ok']}/{summary['n_tasks']} 题第一次就通过）")
    print(f"需重试比例：{summary['retry_needed']:.3f}")

    # 需重试为 0 时必须说清它的含义——否则会被读成"模型完美"。
    # 实际上它更可能是**任务区分度不足**：首次就过说明没有需要修正的地方，
    # 不是判据宽容。这与 v2.1 基线「冒烟集不产生百分比」同源：
    # 一个高成功率要先问"是不是题太简单"，而不是先庆祝。
    if summary['retry_needed'] == 0:
        print('\n  需重试为 0：每题第一次就通过，**不说明模型完美**。')
        print('  更可能的解释是任务区分度不足——判据太容易满足。')
        print('  判别方法：看单题的探索轮数；全是 0~1 说明画像已能直接回答，')
        print('  这类题不该留在成功率题集里（它们不产生信息）。')

    print('\n逐题明细：')
    for tid, d in sorted(summary['per_task'].items()):
        flags = []
        if not d['first_run_ok']:
            flags.append('首次未过')
        if not d['any_run_ok']:
            flags.append('全未过')
        print(f"  {tid} {d['name']}：{d['success_count']}/{d['runs']} 通过"
              f" · 探索中位 {d['explore_median']} · {d['seconds_median']}s"
              + (f" · {'/'.join(flags)}" if flags else ''))

    # ===== 区分度统计 =====
    # 这是本轮新增的核心指标：**满分本身不是结果，题目能不能区分才是**。
    # 一道题若探索轮数常年为 0，说明画像里已有答案，AI 不需要探索就能过——
    # 它验的是"底线"（不谎报、不越界），不是"能力上限"。
    # 这样的题留在题集里只会把成功率推向 1.000，让人误以为能力已满分。
    with_me = [r for r in rows if r.get('min_explore')]
    if with_me:
        got = sum(1 for r in with_me if r.get('has_discriminating_power'))
        print(f'\n区分度检查（有 min_explore 的hard 题）：')
        print(f'  {got}/{len(with_me)} 次达到要求的探索轮数')
        for t in sorted({r['task'] for r in with_me}):
            rs = [r for r in with_me if r['task'] == t]
            g = sum(1 for r in rs if r.get('has_discriminating_power'))
            exp = rs[0]['min_explore']
            med = ab.median([r['explore_rounds'] for r in rs])
            mark = '✅' if g == len(rs) else ('🟡' if g else '❌')
            print(f'  {mark} {t}: {g}/{len(rs)} 达标（需≥{exp}，实测中位 {med}）')

    # 全题探索轮数分布：一眼看出哪些题不产生信息
    print('\n探索轮数分布（0 轮的题不产生信息，建议从题集移除）：')
    zero = [tid for tid, d in sorted(summary['per_task'].items())
            if (d['explore_median'] or 0) == 0]
    for tid, d in sorted(summary['per_task'].items()):
        med = d['explore_median'] or 0
        bar = '█' * int(med)
        tag = '  ← 0 轮' if med == 0 else ''
        print(f"  {tid} {med:>4.1f} {bar}{tag}")
    print(f'\n  0 轮题：{len(zero)}/{summary["n_tasks"]} 题'
          + (f'（{",".join(zero)}）' if zero else ''))

    result = {
        'model': cfg.get('model'),
        'repeat': a.repeat,
        'n_tasks': len(tasks),
        'injections_used': sorted(injected),
        'discrimination': {
            # 区分度：本轮新增的核心观测量。
            # 满分不是结果，"题目能不能区分"才是。
            'hard_tasks': sorted({r['task'] for r in rows if r.get('min_explore')}),
            'hard_runs_total': len(with_me),
            'hard_runs_meeting_bar': sum(1 for r in with_me if r.get('has_discriminating_power')),
            # 探索轮数为 0 的题：画像已含答案，AI 不探索就能过，不产生信息
            'zero_explore_tasks': zero,
            'zero_explore_ratio': round(len(zero) / summary['n_tasks'], 3) if summary.get('n_tasks') else None,
        },
        'summary': summary,
        'rows': rows,
    }
    REPORT.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding='utf-8')
    print(f'\n完整数据：{REPORT}')


if __name__ == '__main__':
    main()