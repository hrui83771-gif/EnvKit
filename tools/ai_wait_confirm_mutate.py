# -*- coding: utf-8 -*-
"""等待态原因+计时 / 确认卡双入口同步 CDP 判据的变异验证：改坏必红、复原必绿。

用法：python tools/ai_wait_confirm_mutate.py

沿用 clear_undo_mutate.py 的装置约束：
  1) 前端 embed 在 exe 里 —— 每组顺序固定 web_build.py → go build → CDP，
     只重建前端产物会让变异全绿（在测旧二进制）。
  2) 每组结束立刻按字节还原；异常也还原（try/finally）；不用 git checkout。
  3) 「红了」≠「断言跑到了」：必须 OVERALL FAIL + 输出含「断言失败」+
     目标断言名出现在输出里，且不含假红标记。
  4) 复原后再跑一次全绿，并字节比对源码。

装置局限（写进 cdp_ai_wait_confirm_test.js 注释里，不冒充产品判定）：
  CDP 真实鼠标在 #pet（fixed + right 过渡 + confirm 态持续动画）上命中偏移到
  桌宠主体，因此桌宠按钮的命中测试由 elementFromPoint 独立完成、功能链路用 DOM
  click 驱动；抽屉卡按钮仍走真实鼠标。
"""
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
CDP = 'tools/cdp_ai_wait_confirm_test.js'

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
        'name': 'M1 撤掉 aiDecide 的桌宠状态收口',
        'file': 'web/src/js/62-agent.js',
        'old': "  petConfirmDone(p ? (approved ? 'busy' : 'idle') : 'idle');\n",
        'new': "  /* 变异：确认后不收口桌宠 */\n",
        'expect': '抽屉按钮：迷你卡与桌宠状态一起收口',
    },
    {
        'name': 'M2 petConfirmDone 退回「只藏卡不清状态」',
        'file': 'web/src/js/50-pet.js',
        'old': "  petConfirmDone = (next)=>{\n    minicard.classList.remove('show'); mcCb = null;\n    if(st==='confirm') setSt(next || 'idle');\n  };",
        'new': "  petConfirmDone = (next)=>{\n    minicard.classList.remove('show'); mcCb = null;\n  };",
        'expect': '抽屉按钮：迷你卡与桌宠状态一起收口',
    },
    {
        'name': 'M3 不处理服务端 wait 事件（等待态没原因）',
        'file': 'web/src/js/64-ai-settings.js',
        'old': "        } else if(ev.type === 'wait'){\n          /* 服务端给稳定 reason + 真实上限，文案与计时都在前端按界面语言算。\n             确认阶段的思考气泡已被 dropThink 删掉，计时改挂到确认卡里。 */\n          aiWaitSet(ev.reason, ev.budget_ms,\n            (ev.reason === 'confirm' && aiPending) ? aiPending.card : null);",
        'new': "        } else if(ev.type === 'wait'){\n          /* 变异：吞掉 wait 事件 */",
        'expect': '等待态：原因写的是在压历史',
    },
    {
        'name': 'M4 把「已等 Ns」改成会归零的假倒计时',
        'file': 'web/src/js/64-ai-settings.js',
        'old': "      const s = Math.floor((Date.now() - aiWaitState.at) / 1000);",
        'new': "      const s = Math.max(0, budget - Math.floor((Date.now() - aiWaitState.at) / 1000));",
        'expect': '等待态：超时后计时继续往上走',
    },
    {
        'name': 'M5 清空对话时不收口桌宠',
        'file': 'web/src/js/60-ai.js',
        'old': "  petConfirmDone('idle');   // 抽屉卡清了，桌宠迷你卡与 confirm 态必须一起清\n",
        'new': "  /* 变异：清空时留下一张假确认卡 */\n",
        'expect': '清理路径：清空对话后桌宠不残留 confirm 态',
    },
    {
        'name': 'M6 切状态不清旧气泡文案',
        'file': 'web/src/js/50-pet.js',
        'old': "    bubble.textContent = '';\n",
        'new': "    /* 变异：留着「点我确认！」 */\n",
        'expect': '桌宠按钮：不再喊「点我确认！」',
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

    # 锚点漂移会让「替换没生效」也假装跑过一轮
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
            for r, data in originals.items():
                write_bytes(r, data)
    finally:
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
