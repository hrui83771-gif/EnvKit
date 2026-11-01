# -*- coding: utf-8 -*-
"""清理 CDP 测试留下的隔离目录（tools/_<tag>/）。

每个 CDP 测试为了不污染仓库根，都会在 tools/ 下建一个 `_<tag>/` 目录，
把一份 15–28MB 的 exe、一个 Chrome profile、以及从真实 config.json 复制来的
AI 配置放进去。测试脚本现在"成功即删"（`CDP_KEEP=1` 可保留），
但失败留现场、以及历史上已经累积的那些还在——本脚本负责收尾。

用法：
    python tools/cdp_clean.py              # 只报告（默认不删）
    python tools/cdp_clean.py --yes        # 真删
    python tools/cdp_clean.py --yes --all  # 连 _shots 也删（截图留档，一般不要）

保留白名单：
    _shots      留档截图（settings-base-v371.png 等，是证据不是垃圾）
    _cdptmp_*   临时 profile，删掉安全
"""
import os
import shutil
import sys

try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

TOOLS = os.path.dirname(os.path.abspath(__file__))
KEEP = {'_shots'}


def dir_size(p):
    total = 0
    for root, _dirs, files in os.walk(p):
        for f in files:
            try:
                total += os.path.getsize(os.path.join(root, f))
            except OSError:
                pass
    return total


def main():
    do = '--yes' in sys.argv
    keep_shots = '--all' not in sys.argv
    targets = []
    for name in sorted(os.listdir(TOOLS)):
        if not name.startswith('_'):
            continue
        p = os.path.join(TOOLS, name)
        if not os.path.isdir(p):
            continue
        if keep_shots and name in KEEP:
            continue
        targets.append((name, p, dir_size(p)))

    if not targets:
        print("没有需要清理的隔离目录")
        return 0

    total = sum(t[2] for t in targets)
    print("%s %d 个隔离目录，共 %.1f MB" % ("将清理" if do else "可清理", len(targets), total / 1048576.0))
    for name, _p, size in targets:
        print("  %-22s %6.1f MB" % (name, size / 1048576.0))
    if not do:
        print("\n这是预演。确认后加 --yes 真删（_shots 默认保留）。")
        return 0

    freed, failed = 0, []
    for name, p, size in targets:
        try:
            shutil.rmtree(p)
            freed += size
            print("  已删 %s" % name)
        except OSError as e:
            failed.append((name, str(e)))
            print("  删不掉 %s：%s（多半是进程还在占着 exe）" % (name, e))
    print("\n释放 %.1f MB" % (freed / 1048576.0))
    if failed:
        print("有 %d 个没删掉，先关掉对应测试进程再跑一次" % len(failed))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
