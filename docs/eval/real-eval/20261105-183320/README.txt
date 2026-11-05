run_id: 20261105-183320
ran_at: 2026-11-05 18:33:20
exe: EnvKit_realeval.exe
model: deepseek-flash
replay: cleared
upstream exchanges: 6
offline gate rc: 0

report.md      人读结论（入库）
raw.json        逐条 SSE 事件 / 判据 / 回答（入库，复核主要靠它）
mutate.json     判据变异验证结果（入库）
upstream.jsonl  真实上游响应原始字节（约 800KB，本机数据，被 .gitignore 排除；
                重跑一次 --run 即可重得，不会因 git clean 丢失结论）

入库的 report.md / raw.json 已脱敏内网地址、配对码、令牌；
判据只看工具名 / 确认卡 / 标记串，脱敏不影响结论。
