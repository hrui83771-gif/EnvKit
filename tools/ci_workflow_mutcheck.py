"""把 eval-gate.yml 的四个历史缺陷逐个注回去，确认 ci_workflow_check 每条都能抓到。

抓不住的断言等于没有 —— 自检脚本如果对真缺陷无感，它就只是装饰。

跑法（要用装了 pyyaml 的解释器）：

    pip install pyyaml
    python tools/ci_workflow_mutcheck.py
"""

import io
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WF = os.path.join(ROOT, ".github", "workflows", "eval-gate.yml")
CHECK = os.path.join(ROOT, "tools", "ci_workflow_check.py")


def read():
    with io.open(WF, encoding="utf-8") as f:
        return f.read()


def write(s):
    # newline="\n" 是必须的：Windows 上默认写 CRLF，
    # 而 CRLF 会让 run 块里的 heredoc 在 bash 下带上 \r，报一些跟真因无关的错。
    with io.open(WF, "w", encoding="utf-8", newline="\n") as f:
        f.write(s)


def run_check():
    p = subprocess.run([sys.executable, CHECK], cwd=ROOT,
                       capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    return p.returncode, (p.stdout or "") + (p.stderr or "")


MUTATIONS = [
    # 1. run 脚本里出现模板表达式 —— 就是这次真实失败的原因
    ("run 脚本里出现 ${{ }}",
     "          set -euo pipefail\n",
     "          set -euo pipefail\n          # 不能用 ${{ a && b || c }}\n"),

    # 2. run 脚本里混进 `- name:` —— step 数不变，只有规则 3 能抓到。
    #    （原始缺陷就是这个形态：解析得动，只是"冻结基线/门禁判定"从没执行过）
    ("step 被块标量吞掉",
     "          python tools/recovery_sandbox.py --allow-writes --repeat 3 --min-samples 3\n",
     "          python tools/recovery_sandbox.py --allow-writes --repeat 3 --min-samples 3\n"
     "\n          - name: 门禁判定\n            run: python tools/eval_gate.py\n"),

    # 3. setup-go 版本低于 go.mod
    ("go 版本低于 go.mod",
     "go-version: '1.26'",
     "go-version: '1.25'"),

    # 4. bash 语法但没声明 shell
    ("bash 语法缺 shell: bash",
     "        shell: bash\n",
     ""),

    # 5. 内联 python 有中文输出但没开 UTF-8
    ("python 内联缺 PYTHONUTF8",
     "      - name: 清除配置里的 key\n"
     "        if: always()\n"
     "        env:\n"
     "          PYTHONUTF8: '1'\n"
     "        run: |\n",
     "      - name: 清除配置里的 key\n"
     "        if: always()\n"
     "        run: |\n"),

    # 6. 门禁判定无条件跑 —— 没产出报告时会红在"什么都没测"的状态上
    ("判定步骤失去前置条件",
     "      - name: 门禁判定\n        if: steps.full.outputs.ran == '1'\n",
     "      - name: 门禁判定\n"),
]


def main():
    orig = read()
    ok = True
    caught_n = 0
    try:
        rc, out = run_check()
        first = out.strip().splitlines()[0] if out.strip() else ""
        print("[基线] rc=%d  %s" % (rc, first))
        if rc != 0:
            print("基线都没过，先修基线再谈变异")
            return 1

        for name, old, new in MUTATIONS:
            if old not in orig:
                print("[跳过] %-24s 找不到锚点（工作流改过？）" % name)
                ok = False
                continue
            write(orig.replace(old, new, 1))
            rc, out = run_check()
            caught = rc != 0
            first = next((l for l in out.strip().splitlines() if l.strip()), "")
            print("[%s] %-24s rc=%d  %s"
                  % ("抓到" if caught else "漏了", name, rc, first[:88]))
            if caught:
                caught_n += 1
            else:
                ok = False
    finally:
        write(orig)

    rc, out = run_check()
    print("[复原] rc=%d" % rc)
    if rc != 0:
        ok = False
        print("复原后没过！" + out)

    print("结果：%d/%d 抓到" % (caught_n, len(MUTATIONS)))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
