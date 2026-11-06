# -*- coding: utf-8 -*-
"""EnvKit v3 全站柔和配色迁移（一次性、可复核）。

原则：**改变量值，不改变量名** —— app.css 里 261 处 var() 引用一行不动。
硬编码色按设计方案分三类：A 绿系语义/装饰必换、B 格式识别色与 danger 红保留、
C 大面积纯白柔化。每条替换都打印命中次数，命中 0 会告警但不改文件。
保留文件原有换行风格（newline=''），UTF-8 无 BOM。
"""
from __future__ import print_function
import io
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

LIGHT_OLD = """  :root{
    --bg:#f5f7fa; --card:#ffffff; --soft:#fbfcfe; --soft2:#f2f6f4; --off:#eef2f6; --hover:#e2e8ef;
    --line:#e3e8ef; --text:#1f2933; --muted:#6b7785;
    --brand:#2f7d4f; --brand2:#3a9d63; --brand-weak:#e9f4ee;
    --ok:#1f9d55; --okbg:#e6f6ec; --warn:#b07800; --warnbg:#fdf3e0; --err:#d64545; --errbg:#fdeaea; --info:#3b6fb0; --infobg:#eaf2fb;
    --dotoff:#c7cede; --focusbg:#ffffff;
  }
  body[data-theme="dark"]{
    --bg:#0f1520; --card:#161e2b; --soft:#1c2534; --soft2:#232e40; --off:#232c3b; --hover:#2b3850;
    --line:#2a3547; --text:#e4eaf2; --muted:#8b96a8;
    --brand:#2e8f58; --brand2:#3fae73; --brand-weak:#1a3025;
    --ok:#37b56a; --okbg:#12301f; --warn:#d99a26; --warnbg:#32260f; --err:#e05a5a; --errbg:#3a1a1a; --info:#5b93d6; --infobg:#15243a;
    --dotoff:#3a4658; --focusbg:#1c2534;
  }"""

LIGHT_NEW = """  :root{
    /* v3 柔和色板：暖米白底 + 雾蓝主色 + 藕杏/淡紫点缀，界面语义色不出现绿。
       只改值不改名 —— 全站 261 处 var() 引用保持原样。
       B 类例外：.ai-doc 的格式识别色（含 XLSX 绿）、danger 确认框红、
       日志/终端深色底与 hljs 配色，均为功能性/第三方识别资产，不在此表内。 */
    --bg:#F7F4EF; --card:#FFFCF8; --soft:#FBF9F5; --soft2:#F2EEE8; --off:#F0EBE3; --hover:#E9E2D8;
    --line:#E7E0D6; --text:#3E3C3A; --muted:#746E66;
    --brand:#526E96; --brand2:#8CA6C6; --brand-weak:#EDF3FA;
    --ok:#526E96; --okbg:#EDF3FA; --warn:#87642B; --warnbg:#F8EFE0; --err:#B05652; --errbg:#F9EBE9; --info:#55708F; --infobg:#F1F5F9;
    --dotoff:#D6CEC2; --focusbg:#FFFCF8;
    --ob-glow:#D9B2A0; --ob-lilac:#B7A9C4; --ob-accent:#D9B2A0; --ob-ring:#8CA6C6;
  }
  body[data-theme="dark"]{
    /* 暗色沿用原有蓝灰骨架（本来就不带绿），只把绿系语义值换掉。
       --brand 保持足够深以承载全局 button{color:#fff} 的白字对比度。 */
    --bg:#0f1520; --card:#161e2b; --soft:#1c2534; --soft2:#232e40; --off:#232c3b; --hover:#2b3850;
    --line:#2a3547; --text:#e4eaf2; --muted:#8b96a8;
    --brand:#526E96; --brand2:#A3BAD8; --brand-weak:#2A3342;
    --ok:#7C9CC4; --okbg:#232F3E; --warn:#D6AC72; --warnbg:#32260f; --err:#D08A85; --errbg:#3a1a1a; --info:#9AAEC6; --infobg:#15243a;
    --dotoff:#3a4658; --focusbg:#1c2534;
    --ob-glow:#E0BCA9; --ob-lilac:#C8BAD5; --ob-accent:#E0BCA9; --ob-ring:#A3BAD8;
  }"""

APP_CSS = [
    (LIGHT_OLD, LIGHT_NEW, 1),
    # A 类：绿系语义 / 品牌脉冲
    ("rgba(48,164,95,.4)", "rgba(82,110,150,.4)", 1),
    ("rgba(48,164,95,0)", "rgba(82,110,150,0)", 1),
    (".log .err{color:#ff8a8a}.log .warn{color:#ffcf6b}.log .ok{color:#7ee2a0}.log .INFO{color:#9fd0ff}",
     ".log .err{color:#E9A6A2}.log .warn{color:#E0C48A}.log .ok{color:#A8C4E2}.log .INFO{color:#A8BED8}", 1),
    (".log.light .err{color:#c0392b}.log.light .warn{color:#9a6b00}.log.light .ok{color:#1f7a44}.log.light .INFO{color:#2b5f9e}",
     ".log.light .err{color:var(--err)}.log.light .warn{color:var(--warn)}.log.light .ok{color:var(--ok)}.log.light .INFO{color:var(--info)}", 1),
    ("background:#37b56a;flex:none", "background:var(--ok);flex:none", 1),
    (".ai-verify.is-ok{border-color:#2e7d4f;background:#0f2018;color:#8fdcb0}",
     ".ai-verify.is-ok{border-color:var(--brand);background:var(--brand-weak);color:var(--brand)}", 1),
    (".ai-verify.is-pending{border-color:#8a6d1f;background:#211b0d;color:#e8c976}",
     ".ai-verify.is-pending{border-color:var(--warn);background:var(--warnbg);color:var(--warn)}", 1),
    (".ai-verify.is-bad{border-color:#a33;background:#24100f;color:#f0a9a4}",
     ".ai-verify.is-bad{border-color:var(--err);background:var(--errbg);color:var(--err)}", 1),
    (".ai-msg.user.ai-md pre{background:#12331f}", ".ai-msg.user.ai-md pre{background:#1B2431}", 1),
    # C 类：桌宠装饰色降饱和（含亮绿徽标）
    (".pet-badge.red{background:#ef4444}.pet-badge.blue{background:#3b82f6}.pet-badge.green{background:#22c55e}.pet-badge.warn{background:#f59e0b}",
     ".pet-badge.red{background:#C8908C}.pet-badge.blue{background:#8CA6C6}.pet-badge.green{background:#B7A9C4}.pet-badge.warn{background:#D6B27C}", 1),
    ("background:#7cc4f8;border-radius", "background:#A8C4E2;border-radius", 1),
    ("linear-gradient(#cfeaff,#5aa8d8)", "linear-gradient(#DCE9F6,#8CA6C6)", 1),
    ("rgba(90,168,216,.55)", "rgba(140,166,198,.55)", 1),
    ("color:#9db4d8;font-weight:700", "color:#A8BED8;font-weight:700", 1),
    # C 类：桌宠气泡纯白 / 纯黑柔化成主题变量
    (".pet-bubble.inner{background:#e9e9f0;color:#7c7c8a;", ".pet-bubble.inner{background:#F0EBE3;color:#746E66;", 1),
    ("border-top-color:#e9e9f0", "border-top-color:#F0EBE3", 1),
    ("background:#fff;color:#333;border-radius:12px", "background:var(--card);color:var(--text);border-radius:12px", 1),
    ("border-top-color:#fff;border-bottom:none", "border-top-color:var(--card);border-bottom:none", 1),
    ("border-bottom:6px solid #fff", "border-bottom:6px solid var(--card)", 1),
    (".pet-bubble.rich .wx1{font-weight:600;color:#2f2f33}", ".pet-bubble.rich .wx1{font-weight:600;color:var(--text)}", 1),
    (".pet-bubble.rich .wx2{color:#6b6b75;", ".pet-bubble.rich .wx2{color:var(--muted);", 1),
    (".pet-bubble.rich .wx3{color:#8a6a2f;", ".pet-bubble.rich .wx3{color:var(--warn);", 1),
]

MOBILE = [
    (".dot.on{background:#5fa87a;box-shadow:0 0 0 3px rgba(95,168,122,.16)}",
     ".dot.on{background:#526E96;box-shadow:0 0 0 3px rgba(82,110,150,.16)}", 1),
    (".dot.wait{background:#d9a441;box-shadow:0 0 0 3px rgba(217,164,65,.16)}",
     ".dot.wait{background:#C9A15C;box-shadow:0 0 0 3px rgba(201,161,92,.16)}", 1),
    (".dot.bad{background:#c96a5f}", ".dot.bad{background:#B05652}", 1),
    (".btn.primary{background:#4a6fa5;border-color:#4a6fa5;color:#fff}",
     ".btn.primary{background:#526E96;border-color:#526E96;color:#fff}", 1),
    (".btn.primary:active{background:#3f5f8e}", ".btn.primary:active{background:#425F86}", 1),
    ("box-shadow:0 0 0 3px rgba(74,111,165,.1)", "box-shadow:0 0 0 3px rgba(82,110,150,.12)", 4),
    (".msg.md a{color:#4a6fa5;", ".msg.md a{color:#526E96;", 1),
    ("accent-color:#4a6fa5;", "accent-color:#526E96;", 1),
]

JS_REPS = {
    "web/src/js/40-ops.js": [
        ("mk(t('占用'), false, '#e5484d')", "mk(t('占用'), false, 'var(--err)')", 1),
        ("mk(t('空闲'), false, '#2fb344')", "mk(t('空闲'), false, 'var(--ok)')", 1),
        ("c.style.color='#e5484d'", "c.style.color='var(--err)'", 1),
    ],
    "web/src/js/30-profile.js": [
        ("cursor:'#9ad1a8', selectionBackground:'#2f7d4f55'",
         "cursor:'#8CA6C6', selectionBackground:'#526E9655'", 1),
    ],
    "web/src/js/50-pet.js": [
        ("color:'#9fd8cb'", "color:'#D9B2A0'", 1),
        ("['#ff5d7e','#e8b23c','#5aa8d8','#9fd8cb','#b78af7']",
         "['#D98C96','#D6B27C','#8CA6C6','#D9B2A0','#B7A9C4']", 1),
        ("['#ef456c','#e8b23c','#5aa8d8']", "['#C8908C','#D6B27C','#8CA6C6']", 1),
        ("color:'#e8b23c'", "color:'#D6B27C'", 2),
    ],
}


def lum(h):
    h = h.lstrip('#')
    ch = [int(h[i:i + 2], 16) / 255.0 for i in (0, 2, 4)]
    f = lambda c: c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4
    return 0.2126 * f(ch[0]) + 0.7152 * f(ch[1]) + 0.0722 * f(ch[2])


def contrast(a, b):
    l1, l2 = sorted([lum(a), lum(b)], reverse=True)
    return (l1 + 0.05) / (l2 + 0.05)


def apply(rel, reps):
    path = os.path.join(ROOT, rel)
    with io.open(path, encoding='utf-8', newline='') as f:
        src = f.read()
    out = src
    problems = []
    for old, new, expect in reps:
        n = out.count(old)
        if n == 0:
            # 幂等：旧值已不存在、新值在位，说明本条上一轮已经迁移过。
            if new and out.count(new) >= expect:
                continue
            problems.append('MISS  ' + repr(old[:64]))
            continue
        if n != expect:
            problems.append('COUNT %d != %d  %s' % (n, expect, repr(old[:64])))
        out = out.replace(old, new)
    if problems:
        print('  !! ' + rel)
        for p in problems:
            print('     ' + p)
    if out != src and not problems:
        with io.open(path, 'w', encoding='utf-8', newline='') as f:
            f.write(out)
        print('  OK  %-34s %d bytes -> %d bytes' % (rel, len(src), len(out)))
    elif out != src:
        print('  HELD %s (有告警，未写入，先修脚本)' % rel)
        return False
    else:
        print('  --  %-34s 已是目标状态' % rel)
    return True


def main():
    print('对比度自检（>= 4.5 才算过）')
    pairs = [
        ('白字 / 主按钮 --brand(浅)', '#ffffff', '#526E96'),
        ('--text / --bg', '#3E3C3A', '#F7F4EF'),
        ('--muted / --bg', '#746E66', '#F7F4EF'),
        ('--err / --card', '#B05652', '#FFFCF8'),
        ('--warn / --card', '#87642B', '#FFFCF8'),
        ('--warn / --warnbg', '#87642B', '#F8EFE0'),
        ('--info / --card', '#55708F', '#FFFCF8'),
        ('--info / --bg', '#55708F', '#F7F4EF'),
        ('--info / --infobg', '#55708F', '#F1F5F9'),
        ('--ok / --okbg', '#526E96', '#EDF3FA'),
        ('--brand / --brand-weak', '#526E96', '#EDF3FA'),
        ('白字 / 主按钮 --brand(暗)', '#ffffff', '#526E96'),
        ('--text / --bg(暗)', '#e4eaf2', '#0f1520'),
    ]
    ok = True
    for name, fg, bg in pairs:
        v = contrast(fg, bg)
        flag = 'OK' if v >= 4.5 else 'LOW'
        if v < 4.5:
            ok = False
        print('  %-30s %.2f:1  %s' % (name, v, flag))
    if not ok:
        print('对比度不达标，请调整色值后再跑。')
        return 1
    print('')
    print('配色替换')
    allok = apply('web/src/app.css', APP_CSS)
    allok = apply('web/src/mobile.html', MOBILE) and allok
    for rel, reps in sorted(JS_REPS.items()):
        allok = apply(rel, reps) and allok
    return 0 if allok else 2


if __name__ == '__main__':
    sys.exit(main())
