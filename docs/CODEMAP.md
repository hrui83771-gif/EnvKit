# EnvKit 代码地图

> 单 exe 零依赖 Go 项目，全部业务代码在模块根（`package main`，Go 的目录=包约束下
> 拆目录=拆包，仅平台层与未来真正自洽的子系统才进 `internal/`）。按职责分组如下，
> 改代码前先在这里定位文件，避免"看着像 AI 的函数其实在 core"这类误判。

## internal/platform —— 操作系统平台层（叶子包）

控制台生命周期、托盘图标与右键菜单（systray）、磁盘指标、DPAPI 加密、单实例互斥、
气泡通知、Job Object。不 import 任何业务代码；业务依赖经回调注入
（`InstallConsoleHook(onClose)`、`KeepJobAlive(alivePid)`、`RunTray(icon, TrayActions)`）。

## 启动与装配

`main.go`（路由注册+启动序列）· `state.go`（全局 cfg/token/scope 常量）·
`runtime.go` · `lifecycle.go`（退出路径）· `install.go` · `av.go`（杀软检测/白名单）
· `weather.go`（桌宠天气：/api/pet/config + /api/pet/weather，默认关闭、短 TTL 缓存）
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

## 可观测性（进程资源 / 系统事件）

`procstats.go`（gopsutil 每 2s 采 EnvKit 自身 + `svcState` 里 Running 的托管进程：
RSS/CPU/线程/句柄/运行时长/60 点历史，`/api/proc/stats` 只读；增长判据
「30 分钟涨 50% 且 ≥64MB」→ 疑似泄漏，只提示不动手）·
`winevents.go`（`wevtutil /f:xml` 拉 Application+System 的 Level 1/2 事件，
30s 缓存，按本项目栈关键词标 `related`；`/api/win/events` 只读。
**坑**：只匹配到一条时根元素是 `<Event>` 不是 `<Events>`，两种都要认）

## 手机访问（局域网，v3.6.2）

`lanaccess.go` —— **另开一个端口**（默认 18766），不是把主控制台绑到 0.0.0.0。
主控制台上有 90 多个端点（改配置/跑命令/导库/杀进程），绑到局域网再按来源分流，
等于"分流逻辑漏一处 = 全权限泄漏"；另起 mux 后**只注册 13 条路由**
（手机页 + 12 个 API）+ 3 条 xterm 静态资源，
配置/审计/终端那些 handler 在这个 mux 上**压根不存在**（404，不是"被拒绝"）。

> 路由条数曾写成「6 条」——那是 v3.6.2 刚加手机访问时的数量，
> v3.7.0 加了屏幕/剪贴板/终端/SSH/日志/晨报之后已变成 12 个 API。
> 数字以 `lanserve.go` 的 `lanMux()` 为准。

v3.7.2 按职责拆成同包 7 个文件（**纯搬家，路由一条没变**）：
`lanaccess.go`（配置段/设备/令牌）、`lanaddr.go`（地址探测）、
`lanlimit.go`（Host 白名单 + 限速）、`lanserve.go`（**生命周期 + `lanMux()` 路由表**）、
`lanpage.go`（页面/配对/鉴权）、`lanapi.go`（手机只读 API）、
`lanconf.go`（桌面端配置入口）。

三道闸：① 独立端口，只在开关打开时监听（关掉就 Close）② Host 白名单
（只认回环/本机网卡 IP/计算机名，防 DNS rebinding —— "Origin==Host"挡不住）③ 配对码/密码
→ 下发 HttpOnly cookie，磁盘只存 sha256。6 位码只有 100 万组合，所以失败限速是方案的一部分
（同 IP 5 次失败锁 10 分钟）。主控制台继续只听 127.0.0.1，一行没动。

手机页 `web/src/mobile.html` → `web/mobile.html`（`go:embed`，自包含单文件）。

**手机端 AI 是只读的**，两道闸：
① `aiToolDefsReadOnly()` 写工具不发给模型；② `aiExecTool()` 执行层再拦一次。
**坑**：写工具在主循环里走 `case def.Write && verdict.NeedsConfirm()` → 发确认卡 →
**就地等用户点按（最长 10 分钟）**。手机页没有确认按钮 → 这一轮会挂到超时，
而执行层的闸**根本轮不到执行**。所以只读判定必须排在确认分支**之前**
（`case def.Write && aiReadOnlyCur()`），否则手机端会挂死 10 分钟。
意图兜底路径（关键词匹配，不经模型）同样要判。

## 拓扑与守护（v3.0 T2）

`topology.go`（Service 清单+三接口）· `topology_registry.go` ·
`topo_local.go`/`topo_db.go`/`topo_chain.go`（三适配器）· `strategy.go`/`strategy_map.go`
· `guard.go`（拓扑无关守护循环）· `guardstate.go` · `correlate.go`（跨节点关联）·
`shadow.go`（影子模式+晋升）

## AI 子系统

`ai.go`（AIConfig/aiSnap/aiMutate/上游调用）· `ai_sse.go`（流式）· `ai_loop.go`
（主循环）· `ai_history.go` · `ai_intent.go`（L1 本地意图路由）· `ai_confirm.go` /
`ai_preconfirm.go`（确认卡）· `ai_tools.go`（工具注册表/执行器）· `ai_attach.go`
（图片附件：拖入/粘贴的图落盘到 `<exeDir>/aiattach/`，消息里只留 id，发上游前才转 data URL；
**保留期 24 小时**，启动扫一次 + 每小时扫一次，`aiAttachStartGC()` 在 `main.go:107` 接线）· `ai_dbquery.go`
（只读 DB 查询）· `aibackup.go`（AI 备份助手）· `aidebug.go` · `memory.go`/
`memory_api.go`（AI 记忆 + 本轮注入视图）· `lesson.go` · `plan.go`/`replan.go` · `explore.go`/
`filesel.go`/`projectbrief.go`（项目画像/文件探索）· `dbbrowse.go` · `webterm.go`

## 行动 / 预案 / 晨报（Agent 闭环）

`mission.go`（Mission 实体 + 状态机 + 落盘）· `mission_api.go`（列表/操作/触发/
报告读取）· `ai_mission.go`（回合绑定、续作上下文、收口与预案统计回写）·
`trigger.go`（巡检调度 + 入站触发 + 到点产出）· `playbook.go`（预案匹配/套用/
连败降级）· `playbook_api.go`（预案 CRUD）· `briefing.go`（晨报事实聚合 + 报告
落盘 + 磁盘趋势快照）· `sediment.go`（终态行动 → 规范化审计 → R6 经验提取）·
`growth.go` + `growth_api.go`（成长指标：按 ISO 周分桶、比率口径）

> 边界：guard 是确定性快路径（死了就重启），只在它**接不住**的事件上才升级成
> 行动；套预案只准备步骤，执行一律过 PolicyGate 与确认卡。

> 接缝测试在 `agent_loop_e2e_test.go`（一条测试串完七段：告警→预案→起草→执行→
> 复验→收口→沉淀→复用→降级→经验召回）。单模块单测**测不出**接缝问题，
> 历史上两次真实断链（续作不注入目标、存为预案不去重）都是"每段都对"。

> 注：`aiVerify*`/`aiAudit*`/`aiSafePath`/`aiArgInt` 等前缀带 ai 的函数**定义在
> verify/audit/explore 等 core 文件**——命名前缀≠文件归属，以本地图为准。

## 文档 / 评测 / 分发

`docs/`（设计文档、评测装置、原型 `docs/prototypes/`、图片 `docs/assets/`）·
`eval/`（评测 fixture）· `tools/`（评测脚本/i18n）· `dist/`、`distassets/`（分发产物）

### 前端（v3.2 起已拆分；v3.7.2 把 js 再按面板拆开）

| 路径 | 是什么 | 能不能直接改 |
|---|---|---|
| `web/src/index.tpl.html` | HTML 骨架，含 `<!--@css-->` / `<!--@appjs-->` 两个占位符 | **改这里** |
| `web/src/app.css` | 全部样式 | **改这里** |
| `web/src/js/*.js` | 全部前端逻辑，**文件名数字前缀即拼接顺序**；`10-i18n.js` 里是 `I18N_EN` 字典（由 `tools/i18n_build.py` 写回） | **改这里** |
| `web/index.html` | 三件套拼出来的单文件（`go:embed` 的对象） | ❌ 生成物，别手改 |

| 片段 | 面板 / 职责 |
|---|---|
| `00-core.js` | 文件头、`$`、全局错误收集、`cfg` / `EK_SET` |
| `10-i18n.js` | `I18N_EN` 字典、`t()`、语言切换 |
| `20-shell.js` | API 令牌、toast、日志、路由、配置自动保存、内置浏览器、主题 |
| `30-profile.js` | 个人中心、内嵌 SSH 终端、杀软白名单、深度清除 |
| `40-ops.js` | 安装、导入导出、备份还原、诊断、体检、审计、统计、链端校验、启停 |
| `50-pet.js` | 桌宠（表情、喂食、拖拽、气泡） |
| `60-ai.js` | AI 助手对话、Markdown、历史、轨迹、审计树、用量 |
| `62-agent.js` | 行动 / 报告 / 预案库 / 成长指标 / 文件引用 / AI 面板绑定 |
| `64-ai-settings.js` | AI 表单自动保存、自动拉模型、错误一键发 AI |
| `70-settings.js` | 设置页三区（基础 / 开发者 / 评测）与交互绑定 |
| `80-runtime.js` | 全局通知、告警、定时行动、手机访问、防火墙诊断、更新检查 |

新增片段**不要用 `zz-` 往末尾扔**：前缀决定拼接顺序，顺序即语义
（`const` 会 TDZ）。取一个落在正确位置的前缀，例如 `45-`。

改完跑 `python tools/web_build.py`（`i18n_build.py` 会自动代跑）；提交前可跑
`python tools/web_build.py --check` 确认产物与源一致。
**为什么仍保留单文件产物**：`go:embed` 决定交付物必须是单文件（exe 里那一份）；
拆的只是维护面，不是产物。


## 根目录运行时产物（勿挪，路径为代码约定）

`config.json`+`.bak1-3`（容灾链）· `state.json` · `memories.json` ·
`lesson-outcomes.json` · `sql_history.json` · `fault-inject-state.json` ·
`missions.json` · `playbooks.json` · `briefings/`（晨报 .md）·
`briefing-hist.jsonl`（磁盘趋势快照，只留 30 天）·
`audit-*.jsonl` · `trace-*.jsonl` · `envkit-*.log` · `av_whitelist.marker` ·
`backups/`
