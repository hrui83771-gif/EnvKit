# EnvKit 代码地图

> 单 exe 零依赖 Go 项目，全部业务代码在模块根（`package main`，Go 的目录=包约束下
> 拆目录=拆包，仅平台层与未来真正自洽的子系统才进 `internal/`）。按职责分组如下，
> 改代码前先在这里定位文件，避免"看着像 AI 的函数其实在 core"这类误判。

## internal/platform —— 操作系统平台层（叶子包）

控制台生命周期、磁盘指标、DPAPI 加密、单实例互斥、气泡通知、Job Object。
不 import 任何业务代码；业务依赖经回调注入（`InstallConsoleHook(onClose)`、
`KeepJobAlive(alivePid)`）。

## 启动与装配

`main.go`（路由注册+启动序列）· `state.go`（全局 cfg/token/scope 常量）·
`runtime.go` · `lifecycle.go`（退出路径）· `install.go` · `av.go`（杀软检测/白名单）
· 平台分支 `console_*/disk_*/secrets_*/winjob_*/notify_*` 已收进 internal/platform

## 配置与持久化

`configfile.go`（加载/默认值/原子保存/DPAPI/.bak 容灾链）· `configschema.go` ·
`secrets`（已入 platform）· `persist.go` · `distexport.go`（分发导出）· `policy.go`

## 日志 / 审计 / 追踪 / 告警

`loghub.go`（SSE 日志中枢，`info/warn` + scope 常量）· `audit.go`（写审计 +
`aiAuditText/aiAuditDay` + 三态 auditVerify）· `trace.go`/`trace_phase.go` ·
`alerts.go` · `metrics.go` · `svcstate.go`（服务状态与崩溃计数）

## 操作结果契约（AI 与 core 共用）

`opresult.go`（三态 OpResult + `aiOpEvidence/aiOpErr/rrRes/aiOpResultText` 桥接）
· `verify.go`（复验 + `aiStartVerify` 启动即复验 + `aiVerifyBadge*` 徽章）
· `txbudget.go`（重试预算）· `retry.go`（子进程重试）

## 任务执行

`program.go`（前后端启动编排）· `exec.go`（进程执行/`stopByKey`/`shutdownChildren`）
· `procops.go`（进程清理）· `launch.go` · `mysql.go` · `dbops.go`（备份/校验）·
`chain.go`（链端检测/可达性）· `chainip.go`（config.toml 链地址一致性 + sync_chain_ip）
· `portscan.go` · `detect.go` · `diag.go` · `envreq.go`（环境符合性）

## 拓扑与守护（v3.0 T2）

`topology.go`（Service 清单+三接口）· `topology_registry.go` ·
`topo_local.go`/`topo_db.go`/`topo_chain.go`（三适配器）· `strategy.go`/`strategy_map.go`
· `guard.go`（拓扑无关守护循环）· `guardstate.go` · `correlate.go`（跨节点关联）·
`shadow.go`（影子模式+晋升）

## AI 子系统

`ai.go`（AIConfig/aiSnap/aiMutate/上游调用）· `ai_sse.go`（流式）· `ai_loop.go`
（主循环）· `ai_history.go` · `ai_intent.go`（L1 本地意图路由）· `ai_confirm.go` /
`ai_preconfirm.go`（确认卡）· `ai_tools.go`（工具注册表/执行器）· `ai_dbquery.go`
（只读 DB 查询）· `aibackup.go`（AI 备份助手）· `aidebug.go` · `memory.go`/
`memory_api.go`（AI 记忆）· `lesson.go` · `plan.go`/`replan.go` · `explore.go`/
`filesel.go`/`projectbrief.go`（项目画像/文件探索）· `dbbrowse.go` · `webterm.go`

> 注：`aiVerify*`/`aiAudit*`/`aiSafePath`/`aiArgInt` 等前缀带 ai 的函数**定义在
> verify/audit/explore 等 core 文件**——命名前缀≠文件归属，以本地图为准。

## 文档 / 评测 / 分发

`docs/`（设计文档、评测装置、原型 `docs/prototypes/`、图片 `docs/assets/`）·
`eval/`（评测 fixture）· `tools/`（评测脚本/i18n）· `web/`（前端，embed 进 exe）·
`dist/`、`distassets/`（分发产物）

## 根目录运行时产物（勿挪，路径为代码约定）

`config.json`+`.bak1-3`（容灾链）· `state.json` · `memories.json` ·
`lesson-outcomes.json` · `sql_history.json` · `fault-inject-state.json` ·
`audit-*.jsonl` · `trace-*.jsonl` · `envkit-*.log` · `av_whitelist.marker` ·
`backups/`
