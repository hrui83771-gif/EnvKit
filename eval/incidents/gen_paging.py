#!/usr/bin/env python3
"""重新生成 04-paging.txt（分页事故夹具）。

夹具本身已经固化在仓库里；这个脚本只是让"它为什么长这样"可核对、可重生成。

构造约束（每一条都对应一个客观判据）：
  * 纯 ASCII、每行固定 200 字节 —— 让 64KB 注入预算落在第 ~327 行，可精确推算；
  * 共 400 行 ≈ 80KB —— 必须大于 exploreMaxBytes()（默认 64KB），否则根本不会分页；
  * 唯一答案串放在第 330 行 —— 在第一段之外，只有真调 read_doc 才拿得到；
  * 其余行是确定性填充，绝不用随机数（夹具不确定会让断言空转）。
"""
from pathlib import Path

LINE_BYTES = 200
TOTAL_LINES = 400
ANSWER_LINE = 330
ANSWER = "THE_HIDDEN_ANSWER_IS_BLUE_7742"

out = []
for n in range(1, TOTAL_LINES + 1):
    if n == ANSWER_LINE:
        core = f"LINE {n:04d}: {ANSWER}"
    else:
        core = f"LINE {n:04d}: padding for the pagination incident fixture"
    # 固定到 200 字节：不足补 '.'，末尾换行另算（aiReadLines 按行内容累计）
    line = core + "." * (LINE_BYTES - len(core))
    assert len(line) == LINE_BYTES, (n, len(line))
    out.append(line)

text = "\n".join(out) + "\n"
p = Path(__file__).with_name("docs") / "04-paging.txt"
p.write_text(text, encoding="utf-8", newline="\n")
print(f"wrote {p} bytes={len(text.encode())} lines={TOTAL_LINES} "
      f"answer_at={ANSWER_LINE} over_64kb={len(text.encode()) > 64 * 1024}")
