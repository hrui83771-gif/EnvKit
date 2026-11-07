# 全链路体检（v3.9 P0）实施记录

日期：2026-11-07

## 要回答的问题

> 这套环境现在能不能交付？哪几项不通过，分别卡在哪里。

之前三段各管各的：环境检测只看组件（`detect.go`）、链端校验只看链（`chain.go`）、
诊断报告（`dbops.go:568`）只是把**已有快照**拼成 Markdown —— 没跑过就写"未检测"，
没有服务/端口章节，也不给结论。用户得点三遍、自己合起来判断。

体检 = 主动按顺序跑一遍 + 输出一页结论。不新增检测原理，只做编排与收口。

## 三条红线

1. **只读**。不启动服务、不建库、不自动恢复、不复位熔断。
   不通过的项挂 `Acts`，点的是页面上已有的按钮（`issueActInfo` 白名单），
   写操作照旧走 428 确认。唯一例外是检测本身会刷新自己的快照
   （`results` / `chainInfo`），这是用户主动发起的一次真实检测。
2. **绝不调用 `verifyService`**。它会写 `svcRecordVerify`、累加连续失败次数、
   影响熔断与守护策略——"体检一遍"不该改变被体检对象的状态。
   服务段只用 `svcAlive` / `firstListeningPort` / `portOwner` / `httpProbe` 这些只读探针。
3. **跳过 ≠ 失败，未知 ≠ 0**。没配链端/数据库整段跳过并写明原因；
   配了却连不上才算失败。`Skip` 不否决 `Deliverable`，但结论里必须如实写出跳过数，
   且不许说"全部检查通过"。`hcAdd` 对缺原因的 skip 有兜底。

一个口径：跑完调 `runtimeInvalidateCache()`，`/api/state` 的「当前待处理」用同一套
状态源重算，不产生第二份真相。

## 22 项

| 段 | 项 | 主要判据 |
|---|---|---|
| 环境 3 | 组件齐全 / 版本符合要求 / 管理员权限 | `runCheck` + `runtimeStateSlow` + `checkAllEnvReq`（含 `mismatch`） |
| 项目 5 | 前端目录 / 依赖 / 启动脚本 / 后端入口 / SQL 文件 | `runtimeProject` + `os.Stat`；`WebDepsUnknown` → 跳过 |
| 数据库 3 | 可连接 / 目标库存在 / 库内已有表 | `dbTest`（不写 `dbHealth`）+ `information_schema.TABLES` 计数 |
| 链端 5 | 主机可达 / 节点存活 / WeBASE / 链信息 / 共识推进 | `doChainCheckWithRecover(false)` + `verifyChain`；未配主机整段跳过 |
| 服务 4 | 后端可用 / 前端可用 / 端口无外来占用 / 无熔断 | 只读探活（进程+端口+占用者+HTTP 5xx）+ `scanPortsOnce` + `runtimeServices` |
| 运行期 2 | 最近日志无错误 / 最近操作无失败 | `hub.history` 最近 200 行 `[FAIL]`/panic；`auditSummary` 的 `→ fail` |

结论：`Deliverable = (Fail == 0)`，附 `N 通过 / M 不通过 / K 跳过` 与要修的项（最多列 3 个 + 总数）。

**口径要诚实**：`hub.history` 只有行文本没有时间戳，所以是「最近 200 行」而非「最近 5 分钟」；
「库内有表」是弱判据，严格的「SQL 里的表是否齐」留到 P1。

## 接口与文件

- 新增 `healthcheck.go`：`HCItem` / `HCReport` / `hcSummarize`（纯函数）/ `hcMarkdown`、
  六个 `hcCheckXxx` 段、`handleHealthCheck`（POST 启动 / GET 进度与结果）、
  `handleHealthCheckExport`（下载 Markdown，未完成时 409）。
- `main.go`：注册 `/api/healthcheck`、`/api/healthcheck/export`（沿用全局 `authMW`）。
- 前端 `index.tpl.html`：首页「全链路体检」按钮 + 结论摘要 + 报告弹窗；
  `65-healthcheck.js`：发起 → 800ms 轮询 → 分段渲染 → 复制/导出 → `ek:lang` 重渲染；
  `app.css`：`.hc-*` 样式（全走 CSS 变量，明暗两套）。
- `tools/i18n_en.json`：+31 条译文，字典 1844 → 1875，缺口 0。

## 门禁

- `healthcheck_test.go` 9 条：全通过=可交付、任一失败否决、跳过不否决、跳过必须写原因、
  结论与证据都进导出、导出未完成 409、GET 不改状态。
  **变异验证**：把 `deliverable = (fail==0)` 改成 `(fail==0 && skip==0)` → `TestHCSkipDoesNotBlock` 红
  （断言失败，非 panic/编译错）；复原后绿。
- `tools/cdp_healthcheck_test.js` 10 条：真实鼠标点按钮、抓真实 `POST /api/healthcheck` 请求字节
  （含 `X-EnvKit-Token`）、真轮询、报告 22 项计数自洽、跳过项都有原因、失败项有动作按钮且
  点了真跳到 `#fe_dir`、首页保留结论、导出 Markdown 200 且含结论与只读声明、
  英文模式标签无中文残留。沙箱复制 exe 到 `.gotmp/cdp-hc/`，不碰真实 `config.json`；
  启动前排除已占端口（用户自己开着的实例常在 18765）。
- 其它：`gofmt -l` / `go vet` / `go test ./...` / systray / `web_build --check` /
  `dist_check` / `version_check` / `ci_workflow_check(step=13)` / `i18n_gap` / 引导回归。

## P1（这轮没做，别当已有）

1. **表是否齐全**：SQL 文件 `CREATE TABLE` 与 `information_schema` 差集。
   ⚠️ SQL 带 `USE`/库前缀/注释时直接差集会误报，要么严格解析，要么标「未核实」。
2. **业务合约是否已部署**："链通了"和"业务能跑"是两件事。
3. **前后端真联通**：前端能起不代表能打到后端（35 处写死 `127.0.0.1:8888` 的存量风险）。
4. **报告正文的英文**：`Detail` / `Conclusion` 是后端拼好的含变量中文串，
   英文模式下仍中文。与 `UX-2026-11-06 §P2-11` 的 issue 正文同源，要先模板化再进字典。
5. **诊断报告增强**：`buildDiagReport()` 目前不含服务/端口/结论章节，可与体检报告合并口径。

## 使用

首页「全链路体检」→ 面板显示 22 项进度 → 顶部一句结论 + 分段证据 →
失败项直接点「去填写 / 立即检测 / 重试 / 问 AI」→ 可复制结论或导出 Markdown。
体检会重新跑一遍检测，日志按既有规则更新；界面上已写明"不会启动服务、不会建库、不会自动修复"。
