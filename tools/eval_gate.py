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
| 影子记录聚合 | 无（收集位，不是门禁） | 影子流是晋升证据（plan §4.4），评测跑完收拢成 `docs/eval/shadow-*.json`。**缺记录不判失败**——沙箱没有链配置，守护循环本就不跑；空报告比没有报告更坏，所以没有新记录就不生成文件 |

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
import time
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
    # **按行数断言，不按"PASS 出现次数"。**
    #
    # 原来是 `txt.count('PASS ')`，而详情文字里也会出现 PASS
    # （`空 target → UNVERIFIED（不是 PASS…）`）——
    # 于是门禁报的条数比自检自己报的**多一条**，
    # 两个数字都"看起来对"，对不上时根本分不清谁错了。
    n_pass = sum(1 for l in txt.splitlines() if l.strip().startswith('PASS '))
    return {'rc': r.returncode, 'pass': n_pass, 'fails': fails,
            'stderr': (r.stderr or b'').decode('utf-8', 'replace')}


# ---------- 1b. R5 拓扑独立性静态检查 ----------

# R5（plan §六）：恢复事务核心文件（事务/裁决/证据/指标）不得出现拓扑名。
# 权威实现在 Go 侧（strategy_map_test.go 的 TestCoreFilesHaveNoTopologyDependency），
# 这里只是把它接进门禁——**不在这里重复一份文件清单**，
# 重复的清单必然漂移，而漂移的方向永远是"检查越来越松"。
R5_TEST = 'TestCoreFilesHaveNoTopologyDependency|TestTopologyFileHasNoAdapterImplementations'


def run_r5_static():
    """跑 R5 拓扑独立性静态检查（委托 Go 测试）。

    为什么门禁要管这件事：R5 被破坏时**没有任何功能测试会红**——
    核心文件里多一个 `if adapter == "ssh_chain"` 分支，行为测试全绿，
    但"新拓扑接不进来"这件事已经发生了。只有静态检查能看见它。
    """
    r = subprocess.run(['go', 'test', './...', '-count=1',
                        '-run', R5_TEST],
                       cwd=str(ROOT), capture_output=True)
    txt = ((r.stdout or b'') + (r.stderr or b'')).decode('utf-8', 'replace')
    fails = [l.strip() for l in txt.splitlines()
             if l.strip().startswith('FAIL') or '出现拓扑符号' in l
             or '抽象层只放词汇' in l]
    return {'rc': r.returncode, 'fails': fails, 'out': txt}


# ---------- 2. 读报告 ----------
def load_reports():
    """读所有 `report-<kind>.json`，算七维比率与结构块覆盖率。

    ## 为什么自己算而不是读报告里的 dimensions

    分次跑时每个文件的 `dimensions` 只含**本次跑的那几类**，
    直接读会得到「局部数字」当成「总账」。
    这里按 run 重算，**分母口径与 aggregate() 一致**。

    ## 混批次拒绝（v3.0 §5.2 #169，G13 落地）

    上面那个「已知局限」现在修掉了：每份报告带 `generated_at` 批次标记
    （recovery_sandbox 写报告时落盘）。目录里的报告来自**多个批次**时，
    门禁**拒绝计算**并直接报问题——混批次的数字无法区分
    「代码变了」还是「数据变了」，任何一个看似合理的比率都是假的。

    旧报告（无 generated_at 字段）沿用下面的 stale 告警路径。
    修法：把旧报告移进 docs/eval/archive-<版本>/ 再跑门禁。
    """
    files = sorted(REPORT_DIR.glob('report-*.json'))
    if not files:
        return None, {}, None
    runs = []
    stale = []
    batches = set()
    for f in files:
        try:
            d = json.loads(f.read_text(encoding='utf-8'))
        except Exception as e:
            log('  !! 读不了 %s: %r' % (f.name, e))
            continue
        ga = d.get('generated_at')
        if ga:
            batches.add((ga, d.get('total_runs')))
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
    # ---- G13：混批次 = 数据不可信，拒绝计算 ----
    if len({b[0] for b in batches}) > 1:
        msg = ('目录里的 report-*.json 来自**多个批次**（generated_at 不一致：'
               '%s）——混批次的数字无法区分「代码变了」还是「数据变了」，'
               '门禁拒绝计算。请把旧报告移进 docs/eval/archive-<版本>/ 再跑。'
               % ', '.join(sorted({b[0] for b in batches})))
        log('  !! %s' % msg)
        return len(files), [], msg
    return len(files), runs, None


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
            # v3.0 T1.7：EVIDENCE / UNVERIFIED 单列档。
            # **不数它们的话，decided + not_triggered + error 会对不上
            # eligible，样本凭空消失**——而那正是本项目一直在防的
            # "把没测到写成 0 分"的变体。
            'evidence_only': sum(1 for v in verdicts
                                 if v.get('status') == 'EVIDENCE'),
            'unverified': sum(1 for v in verdicts
                              if v.get('status') == 'UNVERIFIED'),
            # v3.0：被产品掐断（确认卡结束本轮）单列。
            # 与 recovery_rate.aggregate() 同口径 —— 两处口径不一致，
            # 报告与门禁就会各说一套，而人只会信其中一个。
            'cut': sum(1 for v in verdicts if v.get('status') == 'CUT'),
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
    # v3.0 §5.2：AI 不可用类（honest_failure）的轮次**没有模型输出**——
    # 上游断开时结构块「没机会出现」，性质同被确认卡掐断。
    # 不剔除的话，每测一次 ai_unreachable 覆盖率就被拉低一格
    # （实测踩过：门禁报「模型该输出块却没输出」，而那一轮模型根本不存在）。
    hf = [r for r in runs if r.get('honest_error')]
    judgeable = n - len(cut) - len(err) - len(hf)
    # 分子**只算真正走了结构化路径的**。
    #
    # ⚠️ 第一版写的是 `if r.get('verdict_used') or r in cut`
    # —— 那把「被确认卡掐断」的轮次也算进分子，
    # 而它们已经**从分母里剔掉了** → **比率可以大于 1**。
    # 实测 hang_service 得到 coverage_judgeable = 1.500。
    used_j = sum(1 for r in runs if r.get('verdict_used'))
    used_j = sum(1 for r in runs if r.get('verdict_used'))
    cj = (used_j / judgeable) if judgeable else None
    # ---- v3.0 T1.7：词表兜底命中（判据走了弱路径）----
    #
    # 口径与 recovery_sandbox 一致：verdict_fallback.used 且 claim_hits 非空。
    # 它高说明模型不输出结构块（或输出非法值）——**该改产品侧指令**，
    # 但首先要让这件事在门禁输出里可见，而不是藏在一个
    # 看着正常的分数后面（v2.9「让 AI 自己申报」的镜像：
    # **让判据也申报自己走了哪条路**）。
    fb = [r for r in runs
          if (r.get('verdict_fallback') or {}).get('used')
          and (r.get('verdict_fallback') or {}).get('claim_hits')]
    # ---- 不变式：比率必须落在 [0, 1] ----
    #
    # 分子分母口径不一致时比率会 > 1（实测踩过 1.500：
    # 掐断的轮次被算进分子却已从分母剔除）。
    # **比率 > 1 一眼就能看出口径错了** —— 所以当场断言，
    # 别让它悄悄写进报告，那会让基线失真。
    if cj is not None and not (0.0 <= cj <= 1.0):
        raise AssertionError(
            'coverage_judgeable=%.3f 越界：分子 %d / 分母 %d'
            % (cj, used_j, judgeable))
    return {'runs': n, 'present': present, 'valid': valid, 'used': used,
            'coverage': (used / n) if n else None,
            'cut_by_confirm': len(cut),
            'collect_errors': len(err),
            'honest_failures': len(hf),
            'judgeable': judgeable,
            'coverage_judgeable': cj,
            'fallback_hits': len(fb)}


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


# ---------- 影子记录聚合（v3.0 T2.8） ----------

def _shadow_already_collected():
    """已收拢进 docs/eval/shadow-*.json 的源文件名集合。

    按文件名去重而不是收完删源文件：聚合是收集动作，不是清理动作——
    删掉产品运行期的证据流来"防止重复计数"，是把书烧了免记账。
    """
    names = set()
    for p in REPORT_DIR.glob('shadow-*.json'):
        try:
            d = json.loads(p.read_text(encoding='utf-8'))
        except Exception:
            continue
        names.update(d.get('source_files') or [])
    return names


def collect_shadow_records():
    """把 shadow-*.jsonl（产品侧 shadow.go 写在 exe 同目录）聚合为
    docs/eval/shadow-<stamp>.json。

    **没有新记录就返回 None，不生成文件**：空报告比没有报告更坏——
    它看起来像"评估过，零通过"。缺记录在这里是预期（沙箱没有链配置，
    守护循环不跑），所以这一步是收集位，不是门禁。
    """
    done = _shadow_already_collected()
    files = [p for p in sorted(ROOT.glob('shadow-*.jsonl'))
             if p.name not in done]
    if not files:
        return None
    recs = []
    for p in files:
        try:
            text = p.read_text(encoding='utf-8')
        except Exception:
            continue
        for ln in text.splitlines():
            ln = ln.strip()
            if not ln:
                continue
            try:
                recs.append(json.loads(ln))
            except Exception:
                continue  # 写一半被杀的半行跳过，不猜内容
    if not recs:
        return None
    out = {
        'generated_at': time.strftime('%Y%m%d-%H%M%S'),
        'source_files': [p.name for p in files],
        'total': len(recs),
        'passed': sum(1 for r in recs if r.get('pass')),
        'by_strategy': {},
        'records': recs,
    }
    for r in recs:
        k = r.get('strategy') or '?'
        b = out['by_strategy'].setdefault(k, {'total': 0, 'passed': 0})
        b['total'] += 1
        if r.get('pass'):
            b['passed'] += 1
    p = REPORT_DIR / ('shadow-%s.json' % out['generated_at'])
    p.write_text(json.dumps(out, ensure_ascii=False, indent=2), encoding='utf-8')
    return out


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
    log('[1/5] 判分器自检')
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

    # ---- 1b. R5 拓扑独立性静态检查 ----
    # 放在自检之后、跑数之前：**行为变化之前闸门就得在位**。
    # T2.6 要改守护循环，那正是 R5 最容易被破坏的时刻。
    log('')
    log('[2/5] R5 拓扑独立性静态检查')
    r5 = run_r5_static()
    if r5['rc'] != 0 or r5['fails']:
        problems.append('R5 拓扑独立性被破坏（%d 处）' % len(r5['fails']))
        for l in r5['fails'][:10]:
            log('    %s' % l[:200])
    else:
        log('  核心文件无拓扑依赖；抽象层不含具体实现')

    # ---- 2b. 影子记录聚合 ----
    # 收集位不是门禁：没有记录不判失败（沙箱无链配置，守护循环不跑）。
    log('')
    log('[3/5] 影子记录聚合（T2.8 影子模式）')
    sh = collect_shadow_records()
    if sh is None:
        log('  没有新的影子记录（shadow-*.jsonl）——守护循环未在沙箱里跑过，属预期')
    else:
        log('  聚合 %d 条（通过 %d）→ docs/eval/shadow-%s.json'
            % (sh['total'], sh['passed'], sh['generated_at']))
        for k, v in sorted(sh['by_strategy'].items()):
            log('    %-18s %d 条，通过 %d' % (k, v['total'], v['passed']))
        log('  注意：通过 = 计划可生成且前置检查可过；**影子从不执行，通过不是恢复成功**')

    # ---- 3. 报告 ----
    log('')
    log('[4/5] 评测报告')
    n_files, runs, mixed_msg = load_reports()
    if mixed_msg:
        # G13：混批次直接进 problems —— 门禁必须红，不能装作没看见
        problems.append(mixed_msg)
    elif not runs:
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
        # v3.0 T1.7：兜底命中要像覆盖率一样被看见。
        if cov.get('fallback_hits'):
            log('  ⚠ 有 %d 轮判据走了词表兜底且兜底命中了声称措辞'
                '（判定输入变弱：结构块缺失/非法 → 该改产品侧指令）'
                % cov['fallback_hits'])
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
                and not cov['cut_by_confirm']):
            # 剔除 AI 不可用轮次后再卡：那些轮次没有模型回合，
            # 不能让它们把「模型照不照做」的分母拉大。
            _n_nohf = cov['runs'] - cov.get('honest_failures', 0)
            _cov_nohf = (cov['used'] / _n_nohf) if _n_nohf else None
            if _cov_nohf is not None and _cov_nohf < 1.0:
                # 没有掐断轮次却覆盖率不满 → 确实是模型不照做。
                msg = ('结构块覆盖率 %.3f < 1.0 且无确认卡掐断 —— '
                       '模型确实没照做，该改产品侧指令（%d 次退回词表）'
                       % (_cov_nohf, _n_nohf - cov['used']))
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
            # v3.0 T1.7：单列档印出来——EVIDENCE（只取证未复验）与
            # UNVERIFIED（复验/宣称未证明恢复）都不进分母，
            # 但**必须能被看见**，否则样本凭空消失。
            extra = []
            if d.get('evidence_only'):
                extra.append('EVIDENCE %d' % d['evidence_only'])
            if d.get('unverified'):
                extra.append('UNVERIFIED %d' % d['unverified'])
            if extra:
                log('  %-16s 单列档：%s（不进分母：未知不是失败，也不是成功）'
                    % ('', '、'.join(extra)))
            if d['error']:
                problems.append('%s 有 %d 个 ERROR（装置故障，不是产品缺陷）'
                                % (dim, d['error']))

    # ---- 4. 比基线 ----
    log('')
    log('[5/5] 与基线比较')
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
