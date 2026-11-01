"""校验 .github/workflows/eval-gate.yml 不会在 GitHub 侧被判 Invalid。

这个文件曾经连续失败 8 次，原因是四个**互相独立**的缺陷叠在一起，
而 Go 单测、本地 YAML 解析都是绿的 —— 所以必须有一个专门的装置来守它。

守的规则：
  1. 能被 YAML 解析，且 job 的 step 数量和名单固定（防止再被块标量吞掉）
  2. run 脚本里**不能出现 `${{ }}`** —— GitHub 会对 run 做模板求值，
     注释里的花括号也照样求值，未定义的变量名直接让整个文件 Invalid
  3. run 脚本里不能出现 `- name:`（说明有 step 被吞进上一个 step 的脚本文本）
  4. setup-go 的版本不低于 go.mod 的 go 行
  5. 需要 bash 语法的 step 必须显式声明 shell（windows runner 默认是 pwsh）

跑法：python tools/ci_workflow_check.py
"""

import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
WF = os.path.join(ROOT, ".github", "workflows", "eval-gate.yml")

# 期望的 step 清单。写死是有意的：
# **少了任何一个都说明它被 YAML 缩进吞掉了**，而不是"我们删了一步"。
EXPECTED_STEPS = [
    "actions/checkout@v4",
    "actions/setup-go@v5",
    "actions/setup-python@v5",
    "判分器自检",
    "go vet",
    "Go 单元测试",
    "全量评测 + 门禁",
    "冻结基线（仅手动勾选时）",
    "门禁判定",
    "清除配置里的 key",
    "上传评测报告",
]

# run 脚本里允许出现的 shell 语法特征 —— 出现这些就必须是 bash
BASH_MARKERS = re.compile(r"\bif\s*\[\s*-[nz]\b|\bdone\b|\bfi\b|<<'[A-Z]+'|\$\(|set\s+-[eux]o")


class Bad(Exception):
    pass


def load():
    try:
        import yaml
    except ImportError:
        raise Bad("缺少 pyyaml，先装：pip install pyyaml")
    with open(WF, encoding="utf-8") as f:
        return yaml.safe_load(f)


def step_label(s):
    # 有 name 就用 name（更可读，报错时能直接对上界面）；否则用 uses
    return s.get("name") or s.get("uses") or "(unnamed)"


def check_steps(doc):
    steps = doc["jobs"]["gate"]["steps"]
    got = [step_label(s) for s in steps]
    if got != EXPECTED_STEPS:
        raise Bad(
            "step 清单不符。\n  期望 %d 项：%s\n  实际 %d 项：%s"
            % (len(EXPECTED_STEPS), EXPECTED_STEPS, len(got), got)
        )
    return steps


def check_run_scripts(steps):
    """run 脚本里不许有 `${{ }}`，也不许有 `- name:`。"""
    for s in steps:
        body = s.get("run")
        if not body:
            continue
        label = step_label(s)

        # 规则 2：模板表达式
        for m in re.finditer(r"\$\{\{(.*?)\}\}", body, re.S):
            raise Bad(
                "step「%s」的 run 脚本里有模板表达式 `${{%s}}`。\n"
                "  GitHub 会对 run 内容做求值，**注释里的也算**，\n"
                "  引用未定义的名字会让整个文件判为 Invalid workflow file。\n"
                "  这类说明请写到 YAML 注释里，不要写进 run 脚本。"
                % (label, m.group(1))
            )

        # 规则 3：被吞掉的 step
        if re.search(r"^\s*-\s+name:", body, re.M):
            raise Bad(
                "step「%s」的 run 脚本里出现了 `- name:` ——\n"
                "  说明后面某个 step 的缩进等于块标量缩进量，被吞成脚本文本了。\n"
                "  那个 step 实际上从来没有执行过。所有 step 必须是 6 空格缩进。"
                % label
            )


def check_bash_shell(steps):
    """用 bash 语法的 step 必须显式 shell: bash（windows runner 默认 pwsh）。"""
    for s in steps:
        body = s.get("run") or ""
        if not BASH_MARKERS.search(body):
            continue
        shell = (s.get("shell") or "").lower()
        if shell != "bash":
            raise Bad(
                "step「%s」用了 bash 语法但没声明 `shell: bash`。\n"
                "  windows-latest 的默认 shell 是 pwsh，`[ -z ... ]` 会直接报\n"
                "  「The term '[' is not recognized」。"
                % step_label(s)
            )


INLINE_PY = re.compile(r"python\s+-(?:c\b|<<)")


def check_python_encoding(steps):
    """内联 python 含非 ASCII 输出时，必须开 UTF-8 模式。

    Windows runner 上 `python -c` 的 stdout 是 cp1252，一句中文 print 就抛
    UnicodeEncodeError —— 会让一个辅助步骤把整个门禁判成红，
    而判分其实是过的。这个坑比"测试失败"更难查：红的位置和原因在两处。
    """
    for s in steps:
        body = s.get("run") or ""
        if not INLINE_PY.search(body):
            continue
        try:
            body.encode("ascii")
        except UnicodeEncodeError:
            pass
        else:
            continue  # 全 ASCII，不会踩这个坑

        env = s.get("env") or {}
        utf8 = str(env.get("PYTHONUTF8", "")).strip().lower() in ("1", "true")
        ioe = str(env.get("PYTHONIOENCODING", "")).strip().lower()
        if utf8 or ioe.startswith("utf-8") or ioe == "utf8":
            continue
        raise Bad(
            "step「%s」里有内联 python（`-c` / heredoc）且含非 ASCII 输出，\n"
            "  但没设 `PYTHONUTF8: '1'`。\n"
            "  Windows runner 的 stdout 是 cp1252，中文 print 会抛\n"
            "  UnicodeEncodeError，把整个门禁判红。" % step_label(s)
        )


GATED_STEPS = ("门禁判定", "冻结基线（仅手动勾选时）")


def check_judge_guarded(steps):
    """判定步骤必须挂在上一步的 output 上，不能无条件跑。

    全量评测在未配 secret 时是"优雅跳过"，但如果门禁判定照样执行，
    eval_gate 找不到 report-*.json 会 append「缺少评测报告」→ 红。
    那是一条**什么都没测**的红，比绿更误导人。
    """
    for s in steps:
        label = step_label(s)
        if label not in GATED_STEPS:
            continue
        cond = str(s.get("if") or "")
        if "steps." in cond and "outputs." in cond:
            continue
        raise Bad(
            "step「%s」没有挂 `steps.<id>.outputs.ran` 之类的前置条件。\n"
            "  全量评测被跳过（未配 secret）时它仍会执行，而 eval_gate\n"
            "  找不到 report-*.json 会追加「缺少评测报告」，让门禁红在\n"
            "  一个什么都没测的状态上。" % label
        )


def check_go_version(doc):
    """setup-go 的版本不能低于 go.mod 的 go 行。"""
    want = None
    with open(os.path.join(ROOT, "go.mod"), encoding="utf-8") as f:
        for line in f:
            m = re.match(r"go\s+(\d+)\.(\d+)", line.strip())
            if m:
                want = (int(m.group(1)), int(m.group(2)))
                break
    if want is None:
        raise Bad("go.mod 里找不到 go 指令")

    for s in doc["jobs"]["gate"]["steps"]:
        if s.get("uses", "").startswith("actions/setup-go"):
            v = str(s.get("with", {}).get("go-version", ""))
            m = re.match(r"(\d+)\.(\d+)", v)
            if not m:
                raise Bad("setup-go 的 go-version 认不出来：%r" % v)
            got = (int(m.group(1)), int(m.group(2)))
            if got < want:
                raise Bad(
                    "setup-go 装 %s，但 go.mod 要求 %d.%d —— 编译前就会报\n"
                    "  `requires go >= %d.%d.0`。" % (v, want[0], want[1], want[0], want[1])
                )
            return got
    raise Bad("找不到 actions/setup-go 步骤")


def main():
    if not os.path.exists(WF):
        print("FAIL：找不到 %s" % WF)
        return 1

    try:
        doc = load()
        if doc.get("name") != "eval-gate":
            # GitHub 读不到 name 时会退回文件路径，那是解析失败的表现
            raise Bad("workflow 的 name 不是 eval-gate（实际 %r）" % doc.get("name"))

        steps = check_steps(doc)
        check_run_scripts(steps)
        check_bash_shell(steps)
        check_python_encoding(steps)
        check_judge_guarded(steps)
        gov = check_go_version(doc)

        n_run = sum(1 for s in steps if s.get("run"))
        print("OK  step=%d  run=%d  go=%d.%d  触发=%s"
              % (len(steps), n_run, gov[0], gov[1],
                 ",".join(k for k in ("push", "pull_request", "workflow_dispatch") if k in doc[True] or k in doc.get("on", {}))))
        return 0
    except Bad as e:
        print("FAIL：%s" % e)
        return 1
    except Exception as e:  # noqa: BLE001 - 任何异常都是门禁失败
        print("FAIL：%s: %s" % (type(e).__name__, e))
        return 1


if __name__ == "__main__":
    sys.exit(main())
