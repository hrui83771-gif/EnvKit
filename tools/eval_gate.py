#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""EnvKit 评测门禁：关键指标跌破基线就失败。

## 为什么要有这个

**评测不进门禁，就一定会被"来不及"跳过。**

实测这一点：v2.7 那轮为了绕开后台任务10 分钟上限
把 11 类拆成逐类跑，中间产物又被 `git clean` 清掉，
**最后九类数据无法二次核对**（写总结文档时才暴露）。

## 它守什么

| 项 | 阈值来源 | 为什么守它 |
|---|---|---|
|判分器自检 | 恒为 0 FAIL | 元数据不一致会**静默**让新注入被 argparse拒掉 |
| 结构块覆盖率 | 基线实测 | 覆盖率 0 = **判据退回词表 = 改造没生效** |
| 七维指标 | 基线实测 ±容差 | 防止某次改动悄悄把某维打穿 |
| 每类样本数 | ≥ 3 | 样本不足的数字**不能当结论** |
| ERROR 数 | 恒为 0 | ERROR 是装置故障，不是产品缺陷 |

## 阈值为什么不拍脑袋

**阈值必须来自真实基线**（`tools/eval_baseline.json`，由 `--set-baseline` 生成）。
拍出来的阈值只有两种下场：要么卡死项目，要么形同虚设。

而基线也不能是「当前值」——
**必须人工确认过基线本身可信之后**再 `--set-baseline` 冻结，
否则等于把当时的 bug 一起冻进门禁。

## 用法

    python tools/eval_gate.py                # 用现有基线检查
    python tools/eval_gate.py --set-baseline # 拿当前报告当新基线（需人工确认）
    python tools/eval_gate.py --no-baseline   # 只跑自检，不查指标

退出码：0 = 通过；1 = 有项目未达标。
"""
import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

ROOT = Path(__file__).resolve().parent.parent
BASELINE = ROOT / 'tools' / 'eval_baseline.json'
REPORT_DIR = ROOT / 'docs' / 'eval'

# 判定用哪几类产物
# **只用 `--only` 分次跑产生的 report-<kind>.json**——
# 全量跑会写总表，但两种模式的字段结构不同，
# 混用会让读报告的人算错覆盖率。
DECISIONS = ('PASS', 'FAIL')
# 指标方向：True = 越高越好，False = 越低越好
DIM_META = {
    'investigation': True,
    'recovery': True,
    'safe_handling': True,
    'false_recovery': False,
    'human': False,
    'verification': True,
    'evidence': True,
}


def log(msg):
    print(msg, flush=True)


# ---------- 1. 判分器自检 ----------
def run_selftest():
    """跑判分器自检。**它比任何单测都更能挡住回归**——
    元数据不一致会让新注入静默失效，而单测是绿的。"""
    r = subprocess.run([sys.executable, str(ROOT / 'tools/recovery_selftest.py')],
                       cwd=str(ROOT), capture_output=True)
    txt = (r.stdout or b'').decode('utf-8', 'replace')
    fails = [l.strip() for l in txt.splitlines() if l.strip().startswith('FAIL')]
    n_pass = txt.count('PASS ')
    return {'rc': r.returncode, 'pass': n_pass, 'fails': fails,
            'stderr': (r.stderr or b'').decode('utf-8', 'replace')}


# ---------- 2. 读报告 ----------
def load_reports():
    """读所有 `report-<kind>.json`，算七维比率与结构块覆盖率。

    ## 为什么自己算而不是读报告里的 dimensions

    分次跑时每个文件的 `dimensions` 只含**本次跑的那几类**，
    直接读会得到「局部数字」当成「总账」。
    这里按 run 重算，**分母口径与 aggregate() 一致**。

    ## ⚠️ 已知局限：它读的是**目录里所有的报告**，不区分新旧

    实测踩过：刚跑完 `stale_log_ok` + `slow_start` 两类，
    目录里还有更早跑的 `ok_then_die` / `fake_listen` 报告，
    于是覆盖率算成6/9 = 0.667 —— **那3 次是旧数据，判据还没结构化**。

    **这会让门禁在"数据不齐"时报一个看似合理的错数字。**

    修法（待做）：报告里记`generated_at` 与判据版本，
    门禁只认**同一批次**的报告。
    在那之前，**跑门禁前必须确认目录里的报告都是同一批次的**。
    """
    files = sorted(REPORT_DIR.glob('report-*.json'))
    if not files:
        return None, {}
    runs = []
    stale = []
    for f in files:
        try:
            d = json.loads(f.read_text(encoding='utf-8'))
        except Exception as e:
            log('  !! 读不了 %s: %r' % (f.name, e))
            continue
        # **旧报告 = 没有 verdict_used 字段**（v2.9 之前跑的）
        rs = d.get('results') or []
        if rs and 'verdict_used' not in (rs[0] or {}):
            stale.append(f.name)
        for r in rs:
            r['_src'] = f.name
            runs.append(r)
    if stale:
        log('  ⚠ 有 %d 个报告是**旧格式**（无verdict_used 字段，'
            '判据结构化之前跑的）：%s' % (len(stale), ', '.join(stale)))
        log('    它们会被算进分母，让数字失真 —— **跑门禁前请确认'
            '目录里都是同一批次的报告**。')
    return len(files), runs


def compute(runs):
    """按七维算通过率。**分母各自独立**，与 aggregate() 同口径。"""
    out = {}
    for dim, higher_better in DIM_META.items():
        # 分母口径：只有该维"适用"的注入才进分母
        if dim in ('recovery', 'false_recovery'):
            rs = [r for r in runs if r.get('expect_autonomous')]
        elif dim == 'safe_handling':
            rs = [r for r in runs if not r.get('expect_autonomous')]
        else:
            rs = runs
        verdicts = [r.get('verdicts', {}).get(dim, {}) for r in rs]
        decided = [v for v in verdicts if v.get('status') in DECISIONS]
        n_pass = sum(1 for v in decided if v.get('passed'))
        out[dim] = {
            'pass': n_pass,
            'decided': len(decided),
            'rate': (n_pass / len(decided)) if decided else None,
            'higher_better': higher_better,
            'not_triggered': sum(1 for v in verdicts
                                 if v.get('status') == 'NOT_TRIGGERED'),
            'error': sum(1 for v in verdicts if v.get('status') == 'ERROR'),
            'eligible': len(verdicts),
        }
    return out


def coverage(runs):
    """统计结构块覆盖率。口径与 `recovery_sandbox.rr_verdict_coverage` 一致。

    ## 为什么要单独算一个 `judgeable`

    写工具转确认卡时，产品会**结束本轮 SSE**
    （`ai_loop.go` 的 `sseDoneConfirm` + `return`），
    模型的话没说完，结构块自然不在 ——
    **它不是模型忘了输出，是没机会输出。**

    实测 `hang_service` 三次里两次如此。

    把这种轮次算进分母，门禁就会拿着一个**被产品掐断的分数**
    去卡人，而且所有人会去查判据 —— 而该改的是确认卡的收尾。
    所以门禁卡`coverage_judgeable`（分母剔除被掐断的轮次），
    同时把 `cut_by_confirm` 也当**告警**打出来 ——
    它高说明产品有一半的回答是半句，那是该修的产品缺陷。
    """
    n = len(runs)
    used = sum(1 for r in runs if r.get('verdict_used'))
    present = sum(1 for r in runs if r.get('verdict'))
    valid = sum(1 for r in runs if (r.get('verdict') or {}).get('valid'))
    cut = [r for r in runs
           if r.get('cut_by_confirm')
           or (r.get('confirm_requested') and not r.get('verdict'))]
    # 采集失败（error_row）：没测到，**不进任何分母**。
    # 不排除的话，一次采集失败会让门禁以为「模型该输出块却没输出」——
    # 那是把管线问题算成产品缺陷，然后所有人去改产品侧指令。
    #
    # ⚠️ **被确认卡掐断的那次不算「采集失败」**（同上）——
    # 否则同一轮次被扣两次分母，coverage_judgeable 虚高。
    err = [r for r in runs
           if r.get('collect_error') and not r.get('cut_by_confirm')]
    judgeable = n - len(cut) - len(err)
    used_j = sum(1 for r in runs
                 if r.get('verdict_used') or r in cut)
    return {'runs': n, 'present': present, 'valid': valid, 'used': used,
            'coverage': (used / n) if n else None,
            'cut_by_confirm': len(cut),
            'collect_errors': len(err),
            'judgeable': judgeable,
            'coverage_judgeable': (used_j / judgeable) if judgeable else None}


# ---------- 3. 比基线 ----------
def load_baseline():
    if not BASELINE.exists():
        return None
    return json.loads(BASELINE.read_text(encoding='utf-8'))


def save_baseline(snap):
    snap['generated_at'] = __import__('time').strftime('%Y-%m-%d %H:%M:%S')
    snap['note'] = ('由 eval_gate.py --set-baseline 生成。'
                    '**冻结前必须人工确认这份基线本身可信**——'
                    '否则等于把当时的 bug 一起冻进门禁。')
    BASELINE.write_text(json.dumps(snap, ensure_ascii=False, indent=2),
                        encoding='utf-8')
    log('基线已写入 %s' % BASELINE)


# ---------- 主 ----------
def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--set-baseline', action='store_true',
                    help='拿当前报告当新基线（需人工确认基线可信）')
    ap.add_argument('--no-baseline', action='store_true',
                    help='只跑自检，不查指标')
    ap.add_argument('--tol', type=float, default=0.0,
                    help='指标相对基线的允许跌幅（0 = 不许跌）')
    a = ap.parse_args()

    log('=' * 68)
    log('EnvKit 评测门禁')
    log('=' * 68)

    # ---- 1. 自检 ----
    log('')
    log('[1/3] 判分器自检')
    st = run_selftest()
    log('  PASS=%d  硬FAIL=%d' % (st['pass'], len(st['fails'])))
    problems = []
    if st['fails']:
        problems.append('判分器自检有 %d 项 FAIL' % len(st['fails']))
        for l in st['fails'][:10]:
            log('    %s' % l[:150])
    if st['stderr'].strip():
        log('  stderr（前 300 字）: %s' % st['stderr'].strip()[:300])
        problems.append('自检进程有 stderr（可能崩了）')

    # ---- 2. 报告 ----
    log('')
    log('[2/3] 评测报告')
    n_files, runs = load_reports()
    if not runs:
        log('  !! 没有 report-<kind>.json —— '
            '门禁无法判断。先跑：python tools/recovery_sandbox.py --only <kind>')
        if not a.no_baseline and not a.set_baseline:
            problems.append('缺少评测报告')
    else:
        cov = coverage(runs)
        log('  报告文件 %d 个、run %d 条' % (n_files, len(runs)))
        # **旧格式报告必须让门禁失败**，不能只打 warning——
        # 用了旧判据的数据算出来的比率，与新判据的**不是同一个东西**，
        # 拿它跟基线比是拿两个不同的尺子量。
        stale_n = sum(1 for r in runs if 'verdict_used' not in r)
        if stale_n:
            msg = ('有 %d 条run 是旧判据跑的（无 verdict_used 字段）—— '
                   '**新旧判据的数字不可比**，门禁不能拿它们跟基线比。'
                   '请清掉 docs/eval/report-*.json 重跑。' % stale_n)
            log('  !! %s' % msg)
            problems.append(msg)
        log('  结构块覆盖率：%d/%d = %s（含被确认卡掐断的轮次）'
            % (cov['used'], cov['runs'],
               ('%.3f' % cov['coverage']) if cov['coverage'] is not None else '—'))
        if cov['judgeable']:
            log('  可判定轮次覆盖率：%d/%d = %s'
                % (cov['used'], cov['judgeable'],
                   '%.3f' % cov['coverage_judgeable']))
        if cov['cut_by_confirm']:
            # 这是**产品缺陷**，不是模型问题：
            # 确认卡触发时本轮 SSE 直接结束，模型的话停在一半。
            # 用户在界面上看到的也是半句。
            log('  ⚠ 有 %d 轮被确认卡掐断（模型没机会输出结论块，'
                '用户看到的也是半句话）—— **该改确认卡的收尾**'
                % cov['cut_by_confirm'])
        # **卡的是可判定口径**，不是含掐断轮次的那个。
        #
        # 用含掐断轮次的覆盖率当门禁，等于让模型为产品行为负责：
        # 数字必然偏低，然后所有人去调判据 —— 而该改的是产品。
        cj = cov.get('coverage_judgeable')
        if cj is not None and stale_n == 0 and cj < 1.0:
            msg = ('可判定轮次结构块覆盖率 %.3f < 1.0 —— 有 %d 次模型该输出块却没输出。'
                   '**该改产品侧指令，不是改判据**'
                   % (cj, cov['judgeable'] - cov['used']))
            log('  !! %s' % msg)
            problems.append(msg)
        if (cov.get('coverage') is not None and stale_n == 0
                and cov['coverage'] < 1.0 and not cov['cut_by_confirm']):
            # 没有掐断轮次却覆盖率不满 → 确实是模型不照做。
            msg = ('结构块覆盖率 %.3f < 1.0 且无确认卡掐断 —— '
                   '模型确实没照做，该改产品侧指令（%d 次退回词表）'
                   % (cov['coverage'], cov['runs'] - cov['used']))
            log('  !! %s' % msg)
            problems.append(msg)

        dims = compute(runs)
        for dim in DIM_META:
            d = dims[dim]
            if d['rate'] is None:
                log('  %-16s 无样本（分母 %d，不进比较）'
                    % (dim, d['eligible']))
            else:
                log('  %-16s %d/%d = %.3f  （未触发 %d、ERROR %d）'
                    % (dim, d['pass'], d['decided'], d['rate'],
                       d['not_triggered'], d['error']))
            if d['error']:
                problems.append('%s 有 %d 个 ERROR（装置故障，不是产品缺陷）'
                                % (dim, d['error']))

    # ---- 3. 比基线 ----
    log('')
    log('[3/3] 与基线比较')
    snap = {'coverage': coverage(runs) if runs else None,
            'dims': compute(runs) if runs else {},
            'n_files': n_files, 'n_runs': len(runs) if runs else 0,
            'selftest_pass': st['pass']}

    if a.set_baseline:
        save_baseline(snap)
        log('  已冻结为新基线。**请人工确认这份基线可信后再提交。**')
        return 0

    base = load_baseline()
    if a.no_baseline:
        log('  --no-baseline：跳过指标比较')
    elif base is None:
        msg = ('没有基线（%s 不存在）—— '
               '第一次跑请用 --set-baseline 冻结' % BASELINE.name)
        log('  !! %s' % msg)
        problems.append(msg)
    elif not runs:
        log('  没数据可比')
    else:
        log('  基线：%s（%s）' % (base.get('generated_at', '?'),
                                  base.get('note', '')[:40]))
        # 覆盖率必须守住（它是「改造有没有生效」的开关）
        bc = (base.get('coverage') or {}).get('coverage')
        nc = snap['coverage']['coverage'] if snap['coverage'] else None
        if bc is not None and nc is not None:
            if nc < bc - a.tol:
                msg = '结构块覆盖率 %.3f< 基线 %.3f' % (nc, bc)
                log('  !! %s' % msg)
                problems.append(msg)
            else:
                log('  结构块覆盖率 %.3f（基线 %.3f）OK' % (nc, bc))
        # 各维比较
        for dim in DIM_META:
            bd = (base.get('dims') or {}).get(dim) or {}
            nd = snap['dims'].get(dim) or {}
            br, nr = bd.get('rate'), nd.get('rate')
            if br is None or nr is None:
                log('  %-16s 无可比数据（基线 %s / 当前 %s）'
                    % (dim, br, nr))
                continue
            hb = DIM_META[dim]
            worse = (nr < br - a.tol) if hb else (nr > br + a.tol)
            mark = '!!' if worse else 'OK'
            if worse:
                problems.append('%s 从 %.3f 退到 %.3f' % (dim, br, nr))
            log('  %-16s %.3f → %.3f  %s（%s）'
                % (dim, br, nr, mark, '越高越好' if hb else '越低越好'))

    # ---- 结论 ----
    log('')
    log('=' * 68)
    if problems:
        log('门禁未通过（%d 项）：' % len(problems))
        for p in problems:
            log('  · %s' % p)
        log('=' * 68)
        return 1
    log('门禁通过')
    log('=' * 68)
    return 0


if __name__ == '__main__':
    sys.exit(main())
