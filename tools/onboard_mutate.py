# -*- coding: utf-8 -*-
"""首次引导的变异门禁：改坏必红、复原必绿、源码字节级还原。

用法：python tools/onboard_mutate.py

每组变异走完整链路 web_build.py -> go build -> cdp_onboard_test.js。
必须走 go build：前端是 embed 进 exe 的，只跑 web_build.py 会全绿（在测旧二进制）。
判据只认「输出里出现指定 FAIL 项」，不认 exit code —— 提前崩溃也要能区分。
"""
import hashlib
import io
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GO = r'C:/EnvKit/tools/go/bin/go.exe'
ENV = dict(os.environ)
ENV.update({'GOROOT': r'C:/EnvKit/tools/go', 'GOTOOLCHAIN': 'local',
            'GOPROXY': 'off', 'PYTHONUTF8': '1'})

# (编号, 文件, 原文锚点, 改坏后, 必须变红的断言名)
MUTATIONS = [
    ('M1 首次选集换掉',      'web/src/js/90-onboard.js',
     'var FIRST_RUN_SLIDES = [0, 1, 2, 3, 4, 5, 6, 7];',
     'var FIRST_RUN_SLIDES = [0, 1, 2, 5, 7];', 'first-8-slides'),
    ('M2 片头不播',          'web/src/js/90-onboard.js',
     "if(mode === 'first' && !REDUCED) playBrand();",
     'if(false) playBrand();', 'brand-appeared'),
    ('M3 去掉自动化保护',    'web/src/js/40-ops.js',
     'var automated = !!(navigator.webdriver || /HeadlessChrome|PhantomJS/i.test(navigator.userAgent));',
     'var automated = false;', 'automation-no-autoshow'),
    ('M4 演示里偷偷发写请求', 'web/src/js/90-onboard.js',
     "if(id === 'ob-detect-run'){",
     "if(id === 'ob-detect-run'){ fetch('/api/check',{method:'POST'});",
     'demo-zero-write-api'),
    # 02 屏的 hover 联动是这一屏唯一的交互承诺，"只亮节点不亮线"必须被抓到
    ('M5 悬停只亮一半',      'web/src/js/90-onboard.js',
     "      p.classList.toggle('on', k !== null && p.getAttribute('data-wire') === k);",
     "      p.classList.toggle('on', false);",
     'detail-merge-hover'),
    # 04 屏的分组是"结论先行"的骨架，少一组就该红
    ('M6 检测少一组',        'web/src/js/90-onboard.js',
     "var GROUPS = ['运行时', '数据与磁盘', '网络与链端'];",
     "var GROUPS = ['运行时', '数据与磁盘'];",
     'detail-detect-groups'),
    # 记分牌是这个屏的视觉锚点，数字算错不能被放过
    ('M7 记分算错',          'web/src/js/90-onboard.js',
     "countUp(q('#ob-vd-score'), 5, 300);",
     "countUp(q('#ob-vd-score'), 4, 300);",
     'detail-detect-score'),
]

TARGETS = sorted(set(m[1] for m in MUTATIONS))


def run(argv):
    p = subprocess.run(argv, cwd=REPO, env=ENV, stdout=subprocess.PIPE,
                       stderr=subprocess.STDOUT, timeout=180)
    return p.returncode, p.stdout.decode('utf-8', 'replace')


def rebuild():
    rc, out = run([sys.executable, 'tools/web_build.py'])
    if rc != 0:
        raise RuntimeError('web_build.py 失败: ' + out[-500:])
    rc, out = run([GO, 'build', '-o', 'EnvKit.exe', '.'])
    if rc != 0:
        raise RuntimeError('go build 失败: ' + out[-500:])


def cdp():
    rc, out = run(['node', 'tools/cdp_onboard_test.js'])
    return rc, out


def digest(path):
    return hashlib.sha256(open(os.path.join(REPO, path), 'rb').read()).hexdigest()


def main():
    os.chdir(REPO)
    originals = {p: open(os.path.join(REPO, p), 'rb').read() for p in TARGETS}
    before = {p: digest(p) for p in TARGETS}
    report = []
    bad = 0

    try:
        for tag, path, old, new, expect_red in MUTATIONS:
            raw = originals[path]
            text = raw.decode('utf-8')
            n = text.count(old)
            if n != 1:
                report.append('SKIP %s 锚点出现 %d 次' % (tag, n))
                bad += 1
                continue
            with io.open(os.path.join(REPO, path), 'w', encoding='utf-8', newline='') as f:
                f.write(text.replace(old, new, 1))
            try:
                rebuild()
                rc, out = cdp()
                red = ('FAIL ' + expect_red) in out
                summary = [l for l in out.splitlines() if l.startswith('SUMMARY:')]
                crashed = 'TESTERR:' in out
                ok = red and not crashed
                report.append('%s %s %s :: expect=%s red=%s rc=%s %s' %
                              ('PASS' if ok else 'FAIL', tag.split()[0], tag,
                               expect_red, red, rc, (summary[0] if summary else 'no-summary')))
                if not ok:
                    bad += 1
                    report.append('  --- 变异输出尾部 ---')
                    report.extend('  ' + l for l in out.splitlines()[-12:])
            except Exception as e:
                report.append('FAIL %s 链路异常: %s' % (tag.split()[0], e))
                bad += 1
            finally:
                with open(os.path.join(REPO, path), 'wb') as f:
                    f.write(originals[path])

        # 复原：源码必须字节级一致，且重建后 CDP 必须全绿
        after = {p: digest(p) for p in TARGETS}
        restored = (after == before)
        report.append('%s 复原字节级一致 :: %s' % ('PASS' if restored else 'FAIL', restored))
        if not restored:
            bad += 1
        rebuild()
        rc, out = cdp()
        green = (rc == 0 and 'FAIL ' not in out and 'TESTERR:' not in out)
        summary = [l for l in out.splitlines() if l.startswith('SUMMARY:')]
        report.append('%s 复原后全绿 :: rc=%s %s' %
                      ('PASS' if green else 'FAIL', rc, (summary[0] if summary else 'no-summary')))
        if not green:
            bad += 1
            report.extend('  ' + l for l in out.splitlines()[-15:])
    finally:
        # 无论如何都把源码写回原始字节并重建，避免异常退出留坏源码
        for p in TARGETS:
            with open(os.path.join(REPO, p), 'wb') as f:
                f.write(originals[p])
        try:
            rebuild()
        except Exception as e:
            report.append('WARN 收尾重建失败: %s' % e)

    print('\n'.join(report))
    print('RESULT:', 'PASS' if bad == 0 else 'FAIL(%d)' % bad)
    return 1 if bad else 0


if __name__ == '__main__':
    sys.exit(main())
