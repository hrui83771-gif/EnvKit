#!/usr/bin/env python3
"""托盘恢复决策的变异验证：改坏必红、复原必绿。

背景：third_party/systray 是 go.mod replace 指向的**独立模块**，主模块的
`go test ./...` 覆盖不到，所以恢复逻辑自带包内测试
（addicon_recovery_windows_test.go），本脚本负责证明那些断言确实会红，
而不是「看起来改了、其实断言没落在真正出问题的那一层」。

纪律：
  - 只做源码级字节替换，每处 old 必须唯一（或显式 all），否则直接报错；
  - **编译不过的变异不算验证**：所有变异都保证能通过编译；
  - try/finally 字节级还原，末尾再跑一次复原后的测试；
  - GOPROXY=off + 指定 Go 1.27.3 绝对路径，避免 PATH 上的旧 Go 造成假红。
"""
from __future__ import annotations

import os
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
SRC = ROOT / "third_party" / "systray" / "systray_windows.go"
FORK = SRC.parent
GO = r"C:\EnvKit\tools\go\bin\go.exe"

ENV = dict(os.environ)
ENV.update(
    GOROOT=r"C:\EnvKit\tools\go",
    GOTOOLCHAIN="local",
    GOPROXY="off",
    GOTMPDIR=str(ROOT / ".gotmp"),
    PYTHONUTF8="1",
)

# old / new 一律写明文，脚本自己 encode("utf-8")；手写字节锚点极易出错。
MUTATIONS = [
    {
        "name": "M1 撞了同一个 ID 不换键（退回上一版死重试）",
        "old": "\t\t\tt.nid.ID++",
        "new": "\t\t\tt.nid.ID = t.nid.ID",
        "all": True,
        "expect": "换键测试变红",
    },
    {
        "name": "M2 不用 NIM_MODIFY 接管已存在条目",
        "old": "if iconModifyOp(t.nid) == nil {",
        "new": "if false {",
        "expect": "接管测试变红",
    },
    {
        "name": "M3 诊断写回未证实的推测",
        "old": 'state := fmt.Sprintf("任务栏窗口%s、通知区域窗口%s、宿主窗口%s",',
        "new": 'state := fmt.Sprintf("策略限制或权限级别不同：%s、%s、%s",',
        "expect": "客观事实断言变红",
    },
    {
        "name": "M4 首次成功也刷「已恢复」",
        "old": "if i > 0 || id != firstID {",
        "new": "if true {",
        "expect": "首次成功不报恢复变红",
    },
    {
        "name": "M5 任务栏不在也照调 Shell_NotifyIcon",
        "old": "\t\tif shell != 0 {\n",
        "new": "\t\tif shell != 0 || true {\n",
        "expect": "任务栏缺席不调 API 变红",
    },
    {
        "name": "M6 恢复说明不写清换键",
        "old": 'fmt.Sprintf("原 ID=%d 被占，换到 ID=%d 后 NIM_ADD 成功", id, t.nid.ID)',
        "new": 'fmt.Sprintf("NIM_ADD 成功")',
        "expect": "恢复说明断言变红",
    },
]


def run_test() -> tuple[int, str]:
    p = subprocess.run(
        [GO, "test", "-count=1", "./..."],
        cwd=str(FORK), env=ENV, capture_output=True, text=True,
        encoding="utf-8", errors="replace",
    )
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def main() -> int:
    if not SRC.exists() or not pathlib.Path(GO).exists():
        print("环境不满足：缺 fork 源码或指定 Go", file=sys.stderr)
        return 2

    original = SRC.read_bytes()
    rc0, out0 = run_test()
    if rc0 != 0:
        print("基线就不绿，先修好再变异：\n" + out0, file=sys.stderr)
        return 2
    print("基线：fork 包内测试全绿")

    passed = 0
    try:
        for m in MUTATIONS:
            old, new = m["old"].encode("utf-8"), m["new"].encode("utf-8")
            count = original.count(old)
            if count == 0:
                print(f"[SKIP] {m['name']}：源码里找不到锚点（实现已变？）")
                continue
            if not m.get("all") and count != 1:
                print(f"[SKIP] {m['name']}：锚点出现 {count} 次，不唯一")
                continue
            mutated = original.replace(old, new) if m.get("all") else original.replace(old, new, 1)
            SRC.write_bytes(mutated)
            rc, out = run_test()
            red = rc != 0 and "FAIL" in out
            # 编译不过的变异不算验证：Go 的编译错误会以 [build failed] 形式出现
            if "[build failed]" in out or "setup failed" in out:
                print(f"[FAIL] {m['name']}：变异没编过，不算验证\n{out}")
            elif red:
                failed = [ln for ln in out.splitlines() if ln.startswith("--- FAIL")]
                print(f"[PASS] {m['name']} → {' / '.join(failed) or 'FAIL'}")
                passed += 1
            else:
                print(f"[FAIL] {m['name']}：改坏了却没红（断言没落在真链路）\n{out}")
            SRC.write_bytes(original)
    finally:
        SRC.write_bytes(original)

    restored = SRC.read_bytes() == original
    rc1, out1 = run_test()
    print(f"复原后：字节一致={restored}，测试 rc={rc1}")
    if not restored or rc1 != 0:
        print(out1, file=sys.stderr)
        return 1
    print(f"变异验证：{passed}/{len(MUTATIONS)} 通过")
    return 0 if passed == len(MUTATIONS) else 1


if __name__ == "__main__":
    sys.exit(main())
