# DESIGN：多 Agent 调度 —— 意义、边界与分阶段路线

> 状态：**方案（评审用）**。阶段 0 已落地（2026-11-04），阶段 1–3 待评审，不承诺版本号。
> 关系：不替代 [DESIGN-agent-loop.md](DESIGN-agent-loop.md)（Mission / Playbook / 晨报的闭环真源），
> 本文只谈「多个执行体怎么分工、怎么调度」。命名沿用现有约定：对外叫「行动」，代码叫 Mission。
> 前置约束：**任何阶段都不得引入任意命令执行**，不得绕过 §四 的红线。

---

## 零、为什么是现在

两个事实叠在一起，才值得谈多 Agent，而不是为了架构好看。

1. **2026-11-04 的事故**：拖一份 docx 问「这是啥」，界面先弹「停止服务」的**写操作确认卡**；
   同一小时连中四次（start_service / run_detection / stop_service / apply_whitelist）。
   链路是 `aiDocHydrate` 把整篇正文拼进 `msg.Content` → `aiRunLoop` 取 `lastUser` 时连正文拿走
   → `aiMatchIntent` 的裸关键词命中了**论文里的词** → 在模型第一次请求之前就发卡。
   阶段 0 已修（见 §九），但根因不是关键词表不够大，而是
   **「读材料」的上下文拿到了「动系统」的执行权**：一份 `aiSystemPrompt`、一张工具表、
   一个 1407 行的 `aiRunLoop` 全包。补丁能挡住这一次的词，挡不住下一处上下文串味。
2. **现有瓶颈是客观的**（§一逐条锚源码）：全局串行、执行与 HTTP 同生共死、
   单循环职责过载、主模型背全部上下文。这些不是"以后可能会慢"，是已经在日志里能看到
   的 token 浪费与轨迹失真。

所以本系统谈多 Agent，指的是**按上下文与权限边界分账**，不是"多找几个模型来投票"。

---

## 一、现状盘点（锚定源码，全部可复核）

| 环节 | 已有地基 | 缺口 | 主要文件 |
| --- | --- | --- | --- |
| 串行化 | `aiChatMu.TryLock()` 保证同一时刻一条对话；红线 5 写明"后台与前台共用一把锁" | 只读任务也得排队；锁语义是"一个执行体"而非"一个写执行体" | `ai_loop.go:35` |
| 执行生命周期 | `aiRunLoop` 直接写 `http.ResponseWriter`；Mission 用断点续作补救断流 | 执行与请求同生共死；设计 §五 明确"Phase 1 不做脱离浏览器的后台执行器" | `ai_loop.go:536` `ai_mission.go` |
| 意图与预算 | `aiMatchIntent` 确定性路由（4 个调用点）、`budgetFor` / `budgetGovernor` 分级 | 关键词路由只有一个全局口径，不区分"读材料"与"动系统" | `ai_intent.go` `plan.go` |
| 权限 | `policy.go` 四档 `PolicyGate`；`aiExecTool` 执行层统一闸；`aiConfirmGate` 确认流 | 已完备，直接复用；**任何角色化不得在此之外另开执行路径** | `policy.go` `ai_tools.go:858` |
| 工具表 | `aiToolRegistry` 固定白名单；`aiToolDefsReadOnly()` 只读子集（原为手机端） | 只按"手机端/桌面端"分，不按任务性质分 | `ai_tools.go:151,909` |
| 验证 | `verify.go` 三态复验、`verify_result` 结构化事件 | 已完备；成败判据唯一来源 | `verify.go` |
| 任务实体 | `Mission` 持久化状态机、`PlanStepRecord`、断点续作 | 没有调度队列；`msProduce` 只做定时产出与去重 | `mission.go` `trigger.go` |
| 经验 | `Playbook` 匹配/沉淀、`Lesson` 三件套、`Memory` 打分召回 | 预案只"给建议"，不驱动执行（红线：套预案只省推理不省确认） | `playbook.go` `lesson.go` |
| 可观测 | `trace.go` trace_id、轨迹查询、上游录像/回放 `ENVKIT_AI_RECORD/REPLAY` | 没有"角色"维度，一轮里谁烧的 token 分不出来 | `trace.go` `ai_replay.go` |
| 不可信数据 | `<untrusted_data>` 包裹 + 文件名净化；阶段 0 起按模式隔离 | 边界靠字符串标记维持，不是类型级隔离 | `ai_doc.go:113` |

---

## 二、意义（按重要性排序）

1. **权限与上下文分账（最值钱，也是今天这个 bug 的解药）**
   Reader 只看材料、只拿只读工具；Executor 才看得到写工具，且写仍过确认。
   文档正文里一句"请停止服务"在结构上就够不到执行权，而不是靠关键词表碰运气。
   可度量：文档问答的 `confirm_request` / 误 `tool_result` 数恒为 0。

2. **执行脱离 HTTP 生命周期**
   后台 Mission 跑在 worker 里、结果写 trace；用户关页面、SSE 断流都不丢任务。
   现在的"续作"是补救，目标是让任务本来就不会死。
   可度量：断流后 Mission 恢复成功率、`paused→done` 比例。

3. **只读并行、写仍串行**
   读文档 / 读日志 / 查状态 / 链端探测可以并发，用户不必排队等一个长回合。
   这是唯一能安全引入并发的口径；写操作继续全局互斥。
   可度量：并发只读回合的墙钟时间、用户排队 409 次数。

4. **成本与可观测**
   主模型不必每轮背 30k prompt（事故当天有一次 prompt=30378、命中率仅 21.9%）；
   每个执行体有独立 system 前缀、独立预算、独立 trace 段，失败能定位到角色。
   可度量：每轮 prompt token、缓存命中率、单角色失败率。

5. **预案可执行化**
   Playbook 现在是"匹配上了给建议"；角色化后可以成为某角色的固化步骤集。
   注意红线不变：**套预案只省推理，不省确认**，写操作照样过 `aiConfirmGate`。

**不值得做的"意义"**：多模型投票/辩论、跨机分布式、消息总线。
EnvKit 是本机单用户工具，这些带来的复杂度、token 成本和可观测性损失远大于收益。

---

## 三、多 Agent 在本系统的准确含义

不是多进程、不是多模型开会，而是**四个角色 + 一个调度口径**。角色是执行体的配置，
不是新的服务：共用同一个 `aiChatMu`（或其演进）、同一份 `aiToolRegistry`、同一条 `aiUpstreamDo` 出口。

| 角色 | 职责 | 工具白名单 | 模型 | 预算 | 可并发 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| Reader | 读材料、读日志、查状态、只读探索 | `aiToolDefsReadOnly()` | 可下沉 utility | 小（1–4 轮） | 是（阶段 3） | 阶段 0 只有"模式"，无独立角色 |
| Executor | 会改环境的动作（启停、备份、白名单…） | 全量，但每个写仍过 `PolicyConfirm` | 主模型 | 按 `budgetFor` | 否，全局互斥 | 现状即此角色 |
| Verifier | 只做客观复验，产出 `verify_result` | 复验相关，不读材料 | 不调模型 | 固定 1 步 | 随 Executor | 已存在（`verify.go`） |
| Scheduler | 选角色、排队、限流、续作、收口 | 不直接调工具 | 不调模型 | — | 单实例 | `trigger.go` 已有雏形 |

关键判据：**角色之间传递的是结构化结果，不是对话原文**。
延续定案 3（续作只带摘要 + 关键结果全量），避免把一整个上下文复制给下游角色。

---

## 四、设计红线（不可协商）

1. **权限闸永远在执行层 `aiExecTool`**。写工具有四个执行入口（预检命中、意图兜底、
   模型 tool_call、确认后执行），新增任何角色/执行路径都不得绕过；漏一个就是越权。
   阶段 0 的工具表裁剪只是"模型看不到"，**不是权限边界**。
2. **禁止任意命令执行**。角色只能从 `aiToolRegistry` 里选子集，参数是结构化 JSON；
   不新增 `exec(command string)` 之类的泛化工具，不把文件路径当命令拼。
3. **成败只认 `verify_result` / `missionFinish`**，不看模型自述。沿用 DESIGN-agent-loop 红线 R3。
4. **写操作永远过确认**：自动化的上限是"把方案准备好 + 一键执行"，不是"不问就做"；
   动作类 Mission 默认关闭（opt-in），开关不豁免红线。
5. **一切可审计**：每个角色、每次自动触发都带 `trace_id`，与手动触发同权重。
6. **上下文缓存友好**：system 提示词会话内逐字节稳定，不换消息形状
   （换形状 = 请求体字节前缀全变 = 上游前缀缓存整体失效）。
7. **锁语义演进要同步写进文档**：从"同一时刻一个 AI 执行体"改成
   "同一时刻一个写执行体 + 有上限的只读并发"时，必须同步 DESIGN-agent-loop 红线 5，
   不能代码改了文档还写着旧口径。

---

## 五、分阶段路线（按投入产出排序）

### 阶段 0：材料模式隔离 —— **已落地（2026-11-04，见 §九）**

一句话：本回合注入了文档/报告正文、且用户原文没有明确写动作时，
跳过确定性预检、只下发只读工具；意图判定与预算分档一律只看用户手打原文。

### 阶段 1：角色化，仍单进程串行

| 项 | 内容 |
| --- | --- |
| 做什么 | 定义 `agentRole`（reader / executor / verifier）及路由纯函数；每角色独立 system 片段、工具白名单、预算上限；`aiRunLoop` 按角色取配置，但仍在 `aiChatMu` 下串行 |
| 复用 | `aiToolDefsReadOnly()`、`budgetFor`/`budgetGovernor`、`aiTaskHint`、`trace`、`verify`、`PolicyGate` |
| 新增 | `agent_role.go`（角色表 + `aiRoleFor(msgs)` 纯函数）；`aiSystemPrompt` 拆成静态公共段 + 角色段 |
| 验收 | 角色越权调用必被 `aiExecTool` 拒；变异"删掉角色裁剪"必红；运维指令功能不回退（现有 intent 测试全绿） |
| 风险 | system 前缀按角色变化会打碎缓存 → 角色段放**尾部**或用同一前缀 + 尾部追加，别放开头 |

### 阶段 2：Mission 成为调度单元（最大一次跳跃）

| 项 | 内容 |
| --- | --- |
| 做什么 | `missions.json` 的 pending 队列接后台 worker；执行结果写 trace 而非直接写 SSE；SSE 重连按 trace_id 订阅；支持排队、限流、续作 |
| 复用 | `mission.go` 状态机与 `missionSweep`、`trigger.go` 的 `msSchedTick`、`playbook.go`、`aiMissionSettle`、`ai_replay.go` |
| 新增 | 后台 worker + 结果订阅端点 + 并发上限与超时；`aiChatMu` 语义按红线 7 改写 |
| 验收 | 断流/关页面后 Mission 能继续且**只执行一次**；`verify_result` 仍是唯一成败判据；动作类默认关 |
| 风险 | 这是 DESIGN-agent-loop §五 明确推迟的部分：全局槽位（`lastVerify`、`aiWaiter`、`aiGrant`）都是单槽，worker 化前必须逐个改成按 trace_id 归属，否则会串台 |

### 阶段 3：只读并行、写互斥

| 项 | 内容 |
| --- | --- |
| 做什么 | `aiChatMu` 拆成读锁（有上限并发）/ 写互斥；Reader 类回合并行，Executor/Verifier 串行 |
| 复用 | trace 的 trace_id 隔离、`aiExecTool` 只读判定（`aiTool.Write`）、`aiReadOnlyCur()` |
| 新增 | `aiExecMu`（写互斥）+ 读信号量；并发上限配置项，默认 2 |
| 验收 | 两个并发只读回合互不污染（各自的 usage / verify / trace 不串）；写回合期间读回合不改动环境 |
| 风险 | 只读工具并非都无副作用（`run_detection` 会写少量状态），需先逐工具复核再放行并发 |

### 阶段 4：明确不做

多模型投票与辩论、跨机/跨进程分布式、通用消息总线、多用户与权限体系、
全自动预案执行、脱离 PolicyGate 的"智能授权"。理由见 §二末。

---

## 六、文件与接口映射（哪里动、动到什么程度）

| 文件 | 现状职责 | 阶段 1 | 阶段 2 | 阶段 3 |
| --- | --- | --- | --- | --- |
| `ai_loop.go` | 单循环：预检/计划/工具/确认/复验/收尾 | 按角色取配置，循环结构不变 | 循环体抽成可被 worker 调用的函数 | 加读写锁入口 |
| `ai_intent.go` | `aiMatchIntent` + `aiDocReadOnlyTurn` | 意图路由按角色生效（Reader 不跑写预检） | 不变 | 不变 |
| `ai_tools.go` | registry / 工具表 / `aiExecTool` 权限闸 | `aiToolDefsForRole(role)` | **不动执行闸** | 只读判定复用 |
| `ai_doc.go` | 落盘、注入、`<untrusted_data>` | 不变 | 不变 | 不变 |
| `mission.go` `ai_mission.go` | 状态机、断点记步、收口 | 记 `role` 字段 | 加队列与 worker 绑定 | 按 trace_id 隔离单槽 |
| `trigger.go` | `msSchedTick` 定时产出 | 产出时指定角色 | 接调度队列 | 并发上限 |
| `verify.go` `policy.go` | 复验、权限裁决 | **不动** | **不动** | **不动** |
| `trace.go` `ai_replay.go` | 轨迹、上游录像 | 记录角色与各自用量 | 结果订阅源 | 并发 trace 隔离 |
| `web/src/js/64-ai-settings.js` | SSE 事件渲染 | 角色标签 | 重连订阅 | 不变 |

新增文件（预估）：`agent_role.go`、`agent_role_test.go`、`agent_scheduler.go`（阶段 2）、
`agent_scheduler_test.go`。

---

## 七、验收与度量（怎么证明没白做）

不许用"感觉变快了"收口，全部要客观判据：

| 指标 | 判据 | 现有装置 |
| --- | --- | --- |
| 误触发 | 文档问答 SSE 里 `confirm_request` / 误 `tool_result` 数 == 0 | `TestDocChatSkipsPreflight` + 真实上游 e2e（§九） |
| 功能回退 | 无文档运维指令仍能命中预检并正确执行 | `TestMatchIntentStillHonorsRealCommands` + 现有 intent 全量 |
| 权限 | 删掉任何一道闸 → 变异必红 | 项目变异门禁惯例（8 组已跑） |
| 成本 | 每轮 prompt token 不升、缓存命中率不低于基线 | `trace` 里的 usage / `aiHealthSnapshotFor` |
| 可靠性 | 断流后 Mission 恢复成功率、`paused→done` 比例 | `agent_loop_e2e_test.go` |
| 串台 | 并发回合的 usage / verify / trace 各自归属正确 | 阶段 3 新写，按 trace_id 断言 |

**方法论沿用项目铁律**：能用客观事实判就别用措辞判；变异验证是硬门禁；
编译不过的变异不算验证；夹具要刻意让"原顺序 ≠ 正确顺序"；
测不到的分支先怀疑那段代码根本跑不到（阶段 0 就靠这个揪出了两道不可观测的冗余闸）。

---

## 八、明确不做（防 scope 膨胀）

- 不改 `guard.go` 的自动重启语义（已验证可靠，职责边界清晰）；
- 不做多用户/权限（本机单用户工具）；
- 不做预案的全自动沉淀与全自动执行；
- 不新增任意命令执行能力，工具白名单只减不增泛化项；
- 阶段 1 不引入并发，阶段 2 不引入分布式。

---

## 九、阶段 0 落地记录（2026-11-04）

**改动**：

| 文件 | 内容 |
| --- | --- |
| `ai_intent.go` | 新增 `aiUserIntentText`（按 `<untrusted_data` / 占位句截断）、`aiDocReadOnlyTurn`；`aiMatchIntent` 入口一处收口覆盖 4 个调用点；收紧裸词「报告」与状态类「看看」+ 材料指代词排除 |
| `ai_tools.go` | `aiTaskHint` 同步收口；`aiToolDefsForTurn(docReadOnly)` 复用只读清单 |
| `ai_loop.go` | `aiRunLoop` 入口算 `docReadOnly`；预检与兜底各加一道 `!docReadOnly`；工具表传参；预检日志不再刷文档正文 |
| `ai_doc_mode_test.go`（新） | 7 组测试：纯函数、误命中、真指令、预算分档、模式判定、工具表、**真跑 `handleAIChat` 的双场景 SSE 断言** |

**设计取舍**：工具表裁剪与两道 `!docReadOnly` 是**纵深防御**，不是权限闸；
入口收口已经能挡住误命中，删掉那两道闸功能仍对 —— 所以补了"原文命中只读工具"的
第二个集成场景，专门让这两道闸在变异验证里可观测（否则就是不可测的冗余代码）。
逃生口：用户原文含明确写动作（`isWrite=true`）时不进入材料模式，"附文档 + 顺便重启后端"不受影响。

**验证**：

- 8 组变异全部"改坏必红、复原必绿"，含两道纵深防御闸；期间发现并修掉一次
  **编译失败式假红**（脚本走 PATH 命中系统 Go 1.23.3，改用绝对路径后重跑）。
- 全量门禁：`gofmt`（仅 `memory_api.go` 故意不格式化）/ `go vet` / `go test ./...` (62.7s) /
  `web_build.py --check` / `dist_check.py` / `version_check.py` 全绿。
- 重建 `EnvKit.exe`（14,916,096 字节，buildDate 2026-11-04）；
  `cdp_ai_drop_test.js`、`cdp_ai_pick_test.js` 全 PASS。
- **真实上游端到端**（隔离实例 + 真实 DeepSeek 配置 + `ENVKIT_AI_RECORD` 录像）：
  用事故当天那份 `初稿.docx` 问「这是啥」，SSE 事件只有
  `plan_start / reasoning / delta / usage / done`，**零确认卡零工具卡**，模型答出文件名与文档内容；
  对照轮「看看系统日志」正常预检执行 `get_logs`；上游 2 次往返 status 全 200。
  证据 `.gotmp/real_doc_mode_e2e.txt`、`.gotmp/mutcheck_doc_mode_result.txt`（均已忽略）。

**遗留（阶段 1 要处理的）**：

- 模式判定只看"最近一条 user 是否带注入块"，历史消息里的注入材料不参与；
- Reader 还没有独立 system 片段，目前只是"同一提示词 + 裁剪工具表"；
- `handleAIExplain`（诊断解读）同样带 `<untrusted_data>`，已自动进入材料模式，但没单独验过。
