#!/usr/bin/env python3
"""事故回放语料 / 影子判定的统一入口（eval/incidents/）。

    python tools/incident_gate.py --check     跑全部离线门禁（默认）
    python tools/incident_gate.py --shadow    生成影子判定报告 + 事实流
    python tools/incident_gate.py --mutate    变异验证：改坏必红、复原必绿

## 为什么这个脚本存在

Go 单测本身已经是门禁（`go test ./...` 会自动带上 TestIncidentGate / TestShadow*）。
脚本补的是三件单测做不了的事：把语料里的 mutations 变成**可重复的变异门禁**、
把影子报告渲染成人读格式、以及统一 Go 工具链环境。

## 两个已经踩过的坑，写死在这里

1. **必须用 Go 绝对路径**：PATH 里那个系统 Go 是 1.23.3，go.mod 要求 1.26。
   用错版本时「改坏=红」全是**编译失败的假红**，看着全绿其实一次都没测到。
   所以找不到管理版 Go 就直接报错退出，绝不静默 fallback。
2. **编译失败不算变异通过**：变异后即使 go test 返回非 0，也要检查输出里有没有
   编译错误标记。本项目有过「删分支带出 import 未使用」的白跑一轮。
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
GO = Path(r"C:\EnvKit\tools\go\bin\go.exe")
GOTMP = ROOT / ".gotmp"
INCIDENTS = ROOT / "eval" / "incidents"

BUILD_FAIL_MARKERS = (
    "[build failed]",
    "syntax error",
    "declared and not used",
    "undefined:",
    "cannot use ",
    "too many arguments",
    "expected declaration",
)


def env() -> dict:
    e = os.environ.copy()
    if not GO.exists():
        sys.exit(f"[FATAL] 找不到管理版 Go：{GO}\n"
                 f"        PATH 里的系统 Go 版本太旧，会造出编译失败式假红，拒绝继续。")
    e.update({
        "GOROOT": str(GO.parent.parent),
        "GOTOOLCHAIN": "local",
        "GOPROXY": "off",
        "GOTMPDIR": str(GOTMP),
        "PYTHONUTF8": "1",
    })
    return e


def run_go(pattern: str, timeout: int = 240, extra_env: dict | None = None):
    e = env()
    if extra_env:
        e.update(extra_env)
    cmd = [str(GO), "test", "-run", pattern, "-count=1", "-timeout", "3m", "-v", "."]
    return subprocess.run(cmd, cwd=ROOT, env=e, capture_output=True,
                          text=True, encoding="utf-8", errors="replace", timeout=timeout)


def is_build_failure(output: str) -> bool:
    return any(m in output for m in BUILD_FAIL_MARKERS)


def load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


# ---------- --check ----------

def cmd_check() -> int:
    r = run_go(r"^(TestIncidentGate|TestShadow.*)$")
    out = r.stdout + r.stderr
    print("=== 离线回放 + 影子判定门禁 ===")
    for line in out.splitlines():
        if re.match(r"^\s*--- (PASS|FAIL): ", line) or line.startswith(("ok  ", "FAIL\t", "PASS")):
            print("  " + line.strip())
    if r.returncode != 0:
        print("\n[FAILED] 门禁未通过，尾部输出：")
        print("\n".join(out.splitlines()[-40:]))
    else:
        print("\n[PASS] 全部通过（上游完全离线，BaseURL 指向不可达端口）")
    return r.returncode


# ---------- --shadow ----------

def cmd_shadow(out_path: Path) -> int:
    flow_dir = GOTMP / "incident_shadow_flow"
    flow_dir.mkdir(parents=True, exist_ok=True)
    r = run_go(r"^TestShadowReport$", extra_env={"ENVKIT_AI_SHADOW": str(flow_dir)})
    out = r.stdout + r.stderr
    if r.returncode != 0:
        print("[FAILED] 影子报告测试没过：")
        print("\n".join(out.splitlines()[-40:]))
        return r.returncode

    m = re.search(r"^SHADOW_REPORT_JSON:(.+)$", out, re.M)
    if not m:
        print("[FAILED] 没抓到 SHADOW_REPORT_JSON 行 —— 报告出口被改坏了")
        return 2
    rep = json.loads(m.group(1))

    lines = [
        "# AI 侧影子判定 · 离线评估报告",
        "",
        "> 由 `python tools/incident_gate.py --shadow` 生成。",
        "> **影子不生效**：只写「若生效会怎么判」，`stopped_before` 恒为 `execute`，零 token、零网络。",
        "",
        "## 汇总",
        "",
        "| 指标 | 值 | 含义 |",
        "| --- | ---: | --- |",
        f"| 样本总数 | {rep['total']} | 事故型 + 负向对照 |",
        f"| 有候选动作 | {rep['with_action']} | 反事实判定命中了某个工具 |",
        f"| **不一致次数** | {rep['divergence']} | 反事实判定 ≠ 现行判定 |",
        f"| 材料驱动动作 | {rep['material_driven']} | 用户原文独立判定不指向该动作 |",
        f"| 其中写操作 | {rep['write_driven']} | **安全事件**：材料能触发确认卡 |",
        f"| 现行已拦住 | {rep['current_blocked']} | 现行判定没有执行那个动作 |",
        f"| 标注/实现不一致 | {rep['ground_truth_mismatch']} | 应为 0，否则报告不可信 |",
        f"| 复核规则判错 | {rep['incorrect']} | 对照人工标注 |",
        f"| **误拦数 / 率** | {rep['false_blocks']} / {rep['false_block_rate']:.1%} | 分母 = 用户明确要求动作的对照组 |",
        "",
        "## 逐条明细",
        "",
        "| 用例 | 反事实会命中 | 用户原文判定 | 影子结论 | 判对 | 写操作 |",
        "| --- | --- | --- | --- | --- | --- |",
    ]
    for v in rep["verdicts"]:
        lines.append(
            f"| {v['case']} | `{v['naive_tool'] or '—'}` | `{v['user_tool'] or '—'}` | "
            f"{v['outcome']} | {'✅' if v['correct'] else '❌'} | "
            f"{'是' if v['naive_write'] else '否'} |"
        )
    lines += [
        "",
        "## 怎么读这份报告",
        "",
        "- 「反事实」= 事故前那套宽松裸词规则看到**用户原文 + 整篇材料**会命中什么；",
        "- 现行判定用 `aiMatchIntent(aiUserIntentText(...))`，即 11/4 修复后的口径；",
        "- 影子复核器只问一句：**这个动作在用户原文里能不能独立命中**，不能就拦；",
        "- 对错由 `shadow_cases.json` 里人工标注的 `user_wants_tool` 裁决，与实现无关。",
        "",
        "## 当前边界",
        "",
        "本轮是**离线实验**：`aiShadowObserve` 尚无产品调用点，影子没有接触真实用户流量。",
        "要接线上需在 `aiRunLoop` 入口显式加一行并设置 `ENVKIT_AI_SHADOW` —— 属于独立决策。",
        "",
    ]
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text("\n".join(lines), encoding="utf-8", newline="\n")

    print(f"[PASS] 报告 → {out_path}")
    print(f"[PASS] 影子事实流 → {flow_dir}（ai-shadow-<day>.jsonl）")
    print(f"  样本 {rep['total']} / 不一致 {rep['divergence']} / 材料驱动写操作 {rep['write_driven']} "
          f"/ 误拦 {rep['false_blocks']} ({rep['false_block_rate']:.1%}) / 判错 {rep['incorrect']}")
    return 0


# ---------- --mutate ----------

def mutation_groups() -> list[tuple[str, str, list[dict]]]:
    groups: list[tuple[str, str, list[dict]]] = []
    suite = load(INCIDENTS / "cases.json")
    for c in suite["cases"]:
        groups.append((c["id"], f"^TestIncidentGate/{re.escape(c['id'])}$", c["mutations"]))
    shadow = load(INCIDENTS / "shadow_cases.json")
    for i, group in enumerate(shadow.get("mutations", []), 1):
        groups.append((f"shadow-{i:02d}", shadow["mutation_target"], group))
    return groups


def apply_group(group: list[dict]) -> dict[Path, bytes]:
    backups: dict[Path, bytes] = {}
    for m in group:
        p = ROOT / m["file"]
        if p not in backups:
            backups[p] = p.read_bytes()
        needle, repl = m["find"].encode("utf-8"), m["replace"].encode("utf-8")
        b = p.read_bytes()
        n = b.count(needle)
        if n != 1:
            raise RuntimeError(f"{m['file']}: 变异串出现 {n} 次，必须是 1 次 —— {m['find'][:60]!r}")
        p.write_bytes(b.replace(needle, repl, 1))
    return backups


def restore(backups: dict[Path, bytes]) -> None:
    for p, b in backups.items():
        p.write_bytes(b)


def cmd_mutate() -> int:
    groups = mutation_groups()
    print(f"=== 变异验证：{len(groups)} 组（改坏必红、复原必绿）===\n")
    failed = 0
    for name, target, group in groups:
        backups: dict[Path, bytes] = {}
        try:
            backups = apply_group(group)
            red = run_go(target)
            red_out = red.stdout + red.stderr
            if red.returncode == 0:
                print(f"  [FAIL] {name}: 改坏了却全绿 —— 断言空转，或这段代码根本跑不到")
                failed += 1
                continue
            if is_build_failure(red_out):
                print(f"  [FAIL] {name}: 编译失败式假红（不算验证）\n"
                      f"         {'; '.join(x for x in BUILD_FAIL_MARKERS if x in red_out)}")
                failed += 1
                continue
            # 「红了」还不够，得是**断言**红的。实测踩过：写操作确认卡路径让
            # handleAIChat 原地等确认，runner 一句 Fatalf 提前退出，看起来变异必红，
            # 其实 sse_absent confirm_request 那条一次都没跑到。
            if "TestIncidentGate/" in target and "断言失败" not in red_out:
                print(f"  [FAIL] {name}: 改坏后没有任何语料断言被执行（提前 Fatal/panic），属于空转假验证")
                failed += 1
                continue
        except Exception as exc:
            print(f"  [FAIL] {name}: 变异应用失败 —— {exc}")
            failed += 1
        finally:
            if backups:
                restore(backups)

        green = run_go(target)
        if green.returncode != 0:
            print(f"  [FAIL] {name}: 复原后仍红 —— 源码没还原干净或用例本身不稳定")
            print("\n".join((green.stdout + green.stderr).splitlines()[-15:]))
            failed += 1
            continue
        print(f"  [PASS] {name}: 改坏必红 / 复原必绿")
        # 打印变异说明，方便报告里逐条核对「到底拆了哪道闸」
        for m in group:
            if m.get("note"):
                print(f"         · {m['note']}")

    print(f"\n{'[FAILED] ' + str(failed) + ' 组未通过' if failed else '[PASS] 全部 ' + str(len(groups)) + ' 组通过'}")
    return 1 if failed else 0


def main() -> int:
    ap = argparse.ArgumentParser(description="事故回放 / 影子判定门禁")
    ap.add_argument("--check", action="store_true", help="跑离线门禁（默认）")
    ap.add_argument("--shadow", action="store_true", help="生成影子判定报告")
    ap.add_argument("--mutate", action="store_true", help="跑变异验证")
    ap.add_argument("--out", type=Path, default=GOTMP / "incident_shadow_report.md",
                    help="--shadow 的报告输出路径")
    a = ap.parse_args()
    if a.mutate:
        return cmd_mutate()
    if a.shadow:
        return cmd_shadow(a.out)
    return cmd_check()


if __name__ == "__main__":
    sys.exit(main())
