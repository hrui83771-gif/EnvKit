# -*- coding: utf-8 -*-
"""备份静态检查「源库对象清单」判据的变异验证：改坏必红、复原必绿。

用法：python tools/backup_inventory_mutate.py

为什么必须做：这次修的是「把没找到当丢了」的误报，最容易出现的假修法是
**把校验器整个废掉**（只要 dump 里有表就一律通过）。所以每组变异都盯住一条
独立判据：源库 0 个不算缺失、未知不算 0、真丢了仍要报、sidecar 不留孤儿。

约束沿用项目既有装置：
  1) 每组按字节还原（try/finally），不用 git checkout（会抹掉未提交改动）。
  2) 「红了」≠「断言跑到了」：必须输出含 `--- FAIL: <目标测试名>`，
     且不含编译失败/语法错误之类的假红标记。
  3) 复原后再跑一次全绿，并逐文件做字节级比对。
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
PY = sys.executable

GO_ENV = dict(os.environ)
GO_ENV.update({
    'GOROOT': 'C:/EnvKit/tools/go',
    'GOTOOLCHAIN': 'local',
    'GOPROXY': 'off',
    'GOTMPDIR': str(ROOT / '.gotmp'),
    'PYTHONUTF8': '1',
})

RUN_PATTERN = ('TestVerifyBackupStatic|TestBackupStaticCheck|TestObjectInventory|'
               'TestDBObjectInventory|TestPruneBackups')

# 编译不过 / 提前 panic 都不算「判据红了」
FAKE_RED = ['[build failed]', 'syntax error', 'undefined:', 'cannot use',
            'panic:', '[setup failed]', 'no test files', 'go: ']

MUTATIONS = [
    {
        'name': 'M1 源库本来就没有也照报缺失（复现用户事故）',
        'file': 'restore_drill.go',
        'old': "\t\tif inSource == 0 {\n"
               "\t\t\tfinds = append(finds, fmt.Sprintf(\"源库无%s（非缺失）\", label))\n"
               "\t\t\treturn\n"
               "\t\t}\n",
        'new': "\t\t/* 变异：把「源库本来就没有」也当成缺失 */\n",
        'expect': 'TestVerifyBackupStaticSourceHasNoObjects',
    },
    {
        'name': 'M2 清单未知时判成 restore_fail',
        'file': 'restore_drill.go',
        'old': "\t\treturn opOK(\"verify_backup\", cfg.Projects.DBName,\n"
               "\t\t\t\"备份已生成且文件校验通过；源库对象清单未核实，静态检查不能据此判定缺失\"+\n"
               "\t\t\t\t\"，也未证明能还原（未做真实还原演练）\", ev...).withArtifact(r.Artifact)\n",
        'new': "\t\treturn opFail(\"verify_backup\", cfg.Projects.DBName, errKindRestoreFail,\n"
               "\t\t\t\"变异：把未核实当成缺失\", ev...).withArtifact(r.Artifact)\n",
        'expect': 'TestVerifyBackupStaticUnknownBaselineIsNotFailure',
    },
    {
        'name': 'M3 源库真有、dump 丢了也不报（把校验器废掉）',
        'file': 'restore_drill.go',
        'old': "\t\tgaps = append(gaps, fmt.Sprintf(\"源库有 %d 个%s，但备份里一个都没有：%s\", inSource, label, tip))\n",
        'new': "\t\t/* 变异：真丢了也不报 */\n",
        'expect': 'TestVerifyBackupStaticRealLossStillFails',
    },
    {
        'name': 'M4 未知清单被当成「已核实为 0」',
        'file': 'restore_drill.go',
        'old': "\tunknown = inv == nil || !inv.Verified\n",
        'new': "\tunknown = inv == nil   /* 变异：忽略 Verified 标志 */\n",
        'expect': 'TestVerifyBackupStaticUnknownBaselineIsNotFailure',
    },
    {
        'name': 'M5 清理旧备份时留下清单孤儿',
        'file': 'dbops.go',
        'old': "\t\t_ = os.Remove(filepath.Join(dir, n+\".objects.json\")) // 源库对象清单别留孤儿\n",
        'new': "\t\t/* 变异：留下孤儿的源库清单 */\n",
        'expect': 'TestPruneBackupsRemovesInventorySidecar',
    },
]


def run_test():
    p = subprocess.run([GO, 'test', '-run', RUN_PATTERN, '-count=1', '.'],
                       cwd=str(ROOT), env=GO_ENV, capture_output=True,
                       text=True, encoding='utf-8', errors='replace')
    return p.returncode, (p.stdout or '') + (p.stderr or '')


def write_bytes(rel, data):
    (ROOT / rel).write_bytes(data)


def main():
    originals = {}
    for m in MUTATIONS:
        if m['file'] not in originals:
            originals[m['file']] = (ROOT / m['file']).read_bytes()

    # 锚点漂移会让「替换没生效」假装跑过一轮
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
            rc, out = run_test()
            bad = [s for s in FAKE_RED if s in out]
            ok = (rc != 0 and ('--- FAIL: ' + m['expect']) in out and not bad)
            results.append((m['name'], ok))
            print(('  RED-OK  ' if ok else '  RED-FAIL') + '  ' + m['name'] +
                  ('  [目标断言: %s]' % m['expect'] if ok else
                   '  rc=%s 假红=%s 片段=%s' % (rc, bad, out[-500:].replace('\n', ' | '))),
                  flush=True)
            for r, data in originals.items():
                write_bytes(r, data)
    finally:
        for r, data in originals.items():
            write_bytes(r, data)
        print('\n=== 复原校验 ===', flush=True)
        same = all((ROOT / r).read_bytes() == data for r, data in originals.items())
        print('  源码字节级还原：' + ('OK' if same else '失败'), flush=True)
        rc, out = run_test()
        green = (rc == 0 and 'ok  \tenvkit' in out)
        print('  复原后测试：' + ('全绿' if green else '未通过 → ' + out[-600:]), flush=True)
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
