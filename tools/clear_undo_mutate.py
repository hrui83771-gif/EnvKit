# -*- coding: utf-8 -*-
"""清空撤销 / 开发者说明 CDP 判据的变异验证：改坏必红、复原必绿。

用法：python tools/clear_undo_mutate.py

必须踩过的坑（都写进实现里）：
  1) 前端 embed 在 exe 里 —— 只跑 web_build.py 不重新 go build，变异会全绿（测旧二进制）。
     每组的顺序固定：web_build.py → go build → CDP。
  2) 每组结束后**立刻**按字节还原源码；异常/中断也要还原（try/finally），
     否则仓库会长时间停在改坏状态。不用 git checkout（会抹掉未提交改动）。
  3) 「红了」≠「断言跑到了」：必须同时满足
     OVERALL FAIL + 输出含「断言失败」+ 目标断言的关键词出现在输出里，
     且不含编译失败/页面异常/实例起不来这类假红标记。
  4) 隔离目录残留会让 CDP 一直测旧 exe —— cdp_ai_clear_undo_test.js 的 seed()
     已改成每次无条件从仓库根复制最新 exe。
  5) 复原后必须再跑一次全绿，且按字节比对确认源码与变异前完全一致。
"""
import io
import os
import subprocess
import sys

try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
GO = r"C:\EnvKit\tools\go\bin\go.exe"
NODE = r"C:\Users\hen'r'y'hu'g'y\.workbuddy\binaries\node\versions\22.22.2-3\node.exe"
PY = sys.executable
CDP = 'tools/cdp_ai_clear_undo_test.js'

GO_ENV = dict(os.environ)
GO_ENV.update({
    'GOROOT': 'C:/EnvKit/tools/go',
    'GOTOOLCHAIN': 'local',
    'GOPROXY': 'off',
    'GOTMPDIR': str(ROOT / '.gotmp'),
    'PYTHONUTF8': '1',
})

# 假红标记：这些说明根本没跑到判据，不能算「变异红了」
FAKE_RED = ['envkit ready: false', 'cdp targets unavailable', 'SyntaxError',
            '页面异常', 'no page target', 'go: ', 'Traceback', '断言失败 异常：']

MUTATIONS = [
    {
        'name': 'M1 撤掉撤销窗口（换成普通 toast）',
        'file': 'web/src/js/60-ai.js',
        'old': "  const el = toastUndo(t('对话已清空'), t('撤销'), ()=>aiClearUndo(snap), 10000);\n  if(el) el._ekUndoSnap = snap;",
        'new': "  toast(t('对话已清空'));",
        'expect': '清空后出现带「撤销」按钮的 toast',
    },
    {
        'name': 'M2 去掉 pointer-events:auto（按钮看得见点不到）',
        'file': 'web/src/app.css',
        'old': '.toast-act{pointer-events:auto;width:auto;',
        'new': '.toast-act{width:auto;',
        'expect': '命中测试：按钮在最上层',
    },
    {
        'name': 'M3 拆掉「新对话时不许覆盖」保护',
        'file': 'web/src/js/60-ai.js',
        'old': "  if(aiMessages.length){ toast(t('已经开始了新对话，无法撤销清空')); return; }",
        'new': "  if(aiMessages.length){ toast(t('已经开始了新对话，无法撤销清空')); }",
        'expect': '新对话边界：撤销不覆盖',
    },
    {
        'name': 'M4 只恢复消息、不搬回整段 DOM',
        'file': 'web/src/js/60-ai.js',
        'old': "  box.innerHTML = ''; box.appendChild(snap.frag);",
        'new': "  box.innerHTML = '';",
        'expect': '撤销搬回**整段 DOM**',
    },
    {
        'name': 'M5 删掉摘要组的反直觉因果说明',
        'file': 'web/src/index.tpl.html',
        'old': '<div class="hint" id="h-sd-summary">长对话会把较早内容压成一条要点备忘。触发阈值调大 → 更少压缩，但每次请求更胖；「摘要后保留量」是尾部预算，调大反而压得更少、更快再次触发；「至少保留条数」保证最近几条原文不被压掉。</div>',
        'new': '<div class="hint" id="h-sd-summary">摘要设置。</div>',
        'expect': '最关键那条写明了',
    },
]


def run(cmd, env=None):
    return subprocess.run(cmd, cwd=str(ROOT), env=env or GO_ENV,
                          capture_output=True, text=True,
                          encoding='utf-8', errors='replace')


def build():
    a = run([PY, 'tools/web_build.py'])
    if a.returncode != 0:
        raise RuntimeError('web_build 失败：' + (a.stdout + a.stderr)[-800:])
    b = run([GO, 'build', '-trimpath', '-ldflags', '-s -w', '-o', 'EnvKit.exe', '.'])
    if b.returncode != 0:
        raise RuntimeError('go build 失败：' + (b.stdout + b.stderr)[-800:])


def cdp():
    p = run([NODE, CDP])
    return p.returncode, (p.stdout or '') + (p.stderr or '')


def write_bytes(rel, data):
    (ROOT / rel).write_bytes(data)


def main():
    originals = {}
    for m in MUTATIONS:
        rel = m['file']
        if rel not in originals:
            originals[rel] = (ROOT / rel).read_bytes()

    # 先校验每条变异的锚点都存在：锚点漂移会让「替换没生效」也假装跑过一轮
    for m in MUTATIONS:
        old, new = m['old'].encode('utf-8'), m['new'].encode('utf-8')
        if old == new or old not in originals[m['file']]:
            raise SystemExit('变异锚点失效：' + m['name'])

    results = []
    try:
        for m in MUTATIONS:
            rel = m['file']
            mutated = originals[rel].replace(m['old'].encode('utf-8'),
                                             m['new'].encode('utf-8'), 1)
            write_bytes(rel, mutated)
            build()
            rc, out = cdp()
            bad = [s for s in FAKE_RED if s in out]
            ok = (rc != 0 and 'OVERALL FAIL' in out and '断言失败' in out
                  and m['expect'] in out and not bad)
            results.append((m['name'], ok))
            print(('  RED-OK  ' if ok else '  RED-FAIL') + '  ' + m['name'] +
                  ('  [目标断言: %s]' % m['expect'] if ok else
                   '  rc=%s 假红标记=%s 片段=%s' % (rc, bad, out[-400:].replace('\n', ' | '))),
                  flush=True)
            # 立刻还原，避免异常中断时源码停在改坏状态
            for r, data in originals.items():
                write_bytes(r, data)
    finally:
        # 无论如何：字节级还原 + 重建干净产物 + 再跑一次全绿
        for r, data in originals.items():
            write_bytes(r, data)
        print('\n=== 复原校验 ===', flush=True)
        same = all((ROOT / r).read_bytes() == data for r, data in originals.items())
        print('  源码字节级还原：' + ('OK' if same else '失败'), flush=True)
        build()
        rc, out = cdp()
        green = (rc == 0 and 'OVERALL PASS' in out)
        print('  复原后 CDP：' + ('OVERALL PASS' if green else '未通过 → ' + out[-600:]),
              flush=True)
        results.append(('复原后全绿', green and same))

    failed = [n for n, good in results if not good]
    print('\n变异验证：%d/%d 通过' % (len(results) - len(failed), len(results)), flush=True)
    if failed:
        print('未通过：' + '、'.join(failed), flush=True)
        return 1
    print('结论：每组变异都红在目标断言上，复原后全绿；判据没有空转。', flush=True)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
