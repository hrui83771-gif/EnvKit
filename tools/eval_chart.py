# -*- coding: utf-8 -*-
"""生成 README 用的评测结果图（SVG，手写以免引入依赖）。

跑法：python tools/eval_chart.py
产出：docs/eval/seven-dim.svg
"""
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / 'docs' / 'eval' / 'seven-dim.svg'

# 本轮真实数据（docs/eval/sandbox-recovery-report.json，12 次真实调用）
# 格式：(英文名, 中文名, 分子, 分母, 越高越好, 备注)
DIMS = [
    ('Investigation',      '调查率',      12, 12, True,  '每类注入都做了只读诊断'),
    ('Autonomous Recovery', '自主恢复率',   3,  3, True,  '仅可自主恢复类注入'),
    ('Safe Handling',      '安全处置率',    9,  9, True,  '仅不可自主恢复类注入'),
    ('False Recovery',     '谎报率',        1,  1, False, '仅 1 次断言·样本不足'),
    ('Human Intervention', '人工介入率',    0, 12, False, '越低越好'),
    ('Verification',       '专项复验率',    8,  8, True,  '另有 4 次单列取证档'),
    ('Objective Evidence', '客观取证率',    6,  9, True,  '另有 3 次不适用'),
]

W = 900
LEFT, BARW, GAP = 232, 470, 24
TOP = 104
BARH = 30

# CHART_BOTTOM 先算出来，**高度再由它推导** ——
# 第一版把 H 写死成 430 而底部说明按坐标算到 524，整块溢出画布。
# 手填的高度常量迟早和内容对不上，让它从内容反推。
CHART_BOTTOM = TOP + len(DIMS) * (BARH + GAP) - GAP
H = CHART_BOTTOM + 128

LIGHT = '#f6f7f9'
INK = '#1b1f27'
MUTED = '#6b7280'
GRID = '#e3e6ea'
OK = '#2f9e6e'
WARN = '#d98b23'
BAR = '#4a7ab0'
BAR2 = '#7a8fa6'

parts = []
parts.append(
    f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" '
    f'viewBox="0 0 {W} {H}" font-family="system-ui, -apple-system, '
    f'\'Segoe UI\', Roboto, \'Helvetica Neue\', Arial, sans-serif">')
parts.append(f'<rect width="{W}" height="{H}" fill="{LIGHT}"/>')
parts.append(
    f'<text x="40" y="44" font-size="21" font-weight="600" fill="{INK}">'
    f'故障恢复能力评测 · 七维（12 次真实调用）</text>')
parts.append(
    f'<text x="40" y="68" font-size="13" fill="{MUTED}">'
    f'四种注入 × 3 轮，deepseek-flash · 沙箱模式 · '
    f'v2.6.0 · 分母各自独立，不相加也不平均</text>')

# 图例
lx = 40
for label, col in (('越高越好', OK), ('越低越好', WARN)):
    parts.append(f'<rect x="{lx}" y="{TOP-22}" width="10" height="10" rx="2" fill="{col}"/>')
    parts.append(f'<text x="{lx+15}" y="{TOP-13}" font-size="12" fill="{MUTED}">{label}</text>')
    lx += 92

# 网格（0 / 50 / 100%）
for pct in (0, 0.5, 1.0):
    x = LEFT + BARW * pct
    parts.append(f'<line x1="{x:.0f}" y1="{TOP-6}" x2="{x:.0f}" '
                 f'y2="{CHART_BOTTOM + 4}" '
                 f'stroke="{GRID}" stroke-width="1"/>')
    parts.append(f'<text x="{x:.0f}" y="{CHART_BOTTOM + 18}" '
                 f'font-size="11" fill="{MUTED}" text-anchor="middle">'
                 f'{int(pct*100)}%</text>')

y = TOP
for en, cn, num, den, higher, note in DIMS:
    rate = num / den if den else 0
    # 越低越好的维度用警示色，且同一刻度方向（低=好）不反转，
    # 否则读者会把 0% 的好成绩误读成最差。
    col = OK if (rate >= 0.999 or (not higher and rate == 0)) else (
        BAR if higher else WARN)
    parts.append(f'<text x="40" y="{y+14}" font-size="13" font-weight="600" '
                 f'fill="{INK}">{cn}</text>')
    parts.append(f'<text x="40" y="{y+30}" font-size="10.5" fill="{MUTED}">'
                 f'{en}</text>')
    # 底槽
    parts.append(f'<rect x="{LEFT}" y="{y}" width="{BARW}" height="{BARH}" '
                 f'rx="4" fill="{GRID}"/>')
    w = max(BARW * rate, 3)
    parts.append(f'<rect x="{LEFT}" y="{y}" width="{w:.1f}" height="{BARH}" '
                 f'rx="4" fill="{col}"/>')
    txt = f'{rate*100:.0f}%  ({num}/{den})'
    parts.append(f'<text x="{LEFT+BARW+12}" y="{y+19}" font-size="12" '
                 f'fill="{INK}" font-weight="600">{txt}</text>')
    note_txt = note
    if not higher and rate == 0:
        # 越低越好的维度为 0 时条只有 3px，不加说明会被看成"没数据"
        note_txt = '全程无需人接管'
    parts.append(f'<text x="{LEFT+BARW+90}" y="{y+19}" font-size="10.5" '
                 f'fill="{MUTED}">{note_txt}</text>')
    y += BARH + GAP

# 底部说明（用 CHART_BOTTOM，**不复用循环里的 y**）
FOOT_Y = CHART_BOTTOM + 46
parts.append(f'<line x1="40" y1="{FOOT_Y-22}" x2="{W-40}" y2="{FOOT_Y-22}" '
             f'stroke="{GRID}" stroke-width="1"/>')
parts.append(
    f'<text x="40" y="{FOOT_Y}" font-size="11.5" fill="{MUTED}">'
    f'「专项复验」= verify_environment 或启动工具内嵌复验（强证据）；'
    f'「客观取证」= list_backups 等只读体检（弱证据）。两者分开统计，'
    f'不合并成同一个分子。</text>')
parts.append(
    f'<text x="40" y="{FOOT_Y+20}" font-size="11.5" fill="{MUTED}">'
    f'无样本报「无样本」而非 0%：谎报率仅 1 次有结论；'
    f'另有 4 次只取证（单列 EVIDENCE）、3 次取证档不适用，均不进分母。</text>')
parts.append('</svg>')

OUT.parent.mkdir(parents=True, exist_ok=True)
OUT.write_text('\n'.join(parts), encoding='utf-8')
print(f'written: {OUT}  ({OUT.stat().st_size} bytes)')