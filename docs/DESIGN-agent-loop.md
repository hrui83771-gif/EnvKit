# DESIGN：Agent 闭环 —— 行动化 · 主动化 · 记忆化（A+B+C）

> 文档状态：**评审定案 + Phase 1/2/3 全部已落地**（2026-10-26 拍板，见第六节定案记录；
> Phase 2 的落地细节与偏离见 §3.3，Phase 3 见 §4.3。三阶段的验收条目见各节末）
> 关系：不替代 [ROADMAP.md](ROADMAP.md)（v2.x–v3.1 盘点仍有效），这是 v3.3 之后
> 的一条新主线（版本号待定，暂不承诺 v4）。
> 命名约定：本文把新的持久化任务实体叫 **Mission**（代码/JSON 字段名），
> 以区别于 `state.go` 里已存在的 `TaskHandle`（一次脚本执行的**内存**句柄，
> 生命周期 = 一次调用）。**对外中文名定为「行动」**（2026-10-26 评审定案）：
> UI 文案一律写「行动进行中 / 待你继续的行动 / 行动列表」，不叫「任务」，
> 避免与现有「任务轨迹/任务名」（trace 文案）混淆。

---

## 零、为什么是 A+B+C 一起做

三者是同一个 Agent 循环的三个面，拆开做任何一个都会很快撞墙：

```
        ┌──────────────────────────────────────────┐
        │                                          │
   B 感知/触发                                  C 记忆
   (巡检、入站告警、晨报)     →   A 规划与执行   →  (经验、画像、预案)
        ↑                                          │
        └────────────── Mission 留下的痕迹沉淀 ────┘
```

- **只做 A**：任务永远等用户开口，做完即焚，处置经验不沉淀，每次都从零推理。
- **只做 B**：触发了也接不住——现在一轮 SSE 断了整轮作废（无持久化任务实体），
  主动弹的建议没有「接得住、可续作」的执行容器。
- **只做 C**：记忆只能在对话里被动召回，没有任务流去**生产**新的高质量经验。

### 现状盘点（锚定源码，全部可复核）

| 循环环节 | 已有地基 | 缺口 |
|---|---|---|
| 感知 | `guard.go:246-276` 守护循环已做「检测→自动重启→复验→告警」；`alerts.go:32` `alertDispatch` 出站告警（带抑制窗口） | **入站**触发不存在（外部系统无法让 EnvKit 起一个任务）；需要诊断判断的事件没有进任务系统的通道 |
| 规划 | `plan.go` `planTrack`/`budgetFor`（回合内步骤跟踪与预算分级）；`ai_loop.go` plan_start/step/end SSE 事件 | 计划只活在一个 SSE 回合内，回合结束即焚；无「任务」跨回合实体 |
| 执行 | `policy.go` PolicyGate 四档裁决；`ai_confirm.go`/`ai_wait.go` 确认流（就地等待 + 旧路径兜底）；`aiChatMu` 串行化 | 执行与 HTTP 请求同生共死（`aiRunLoop` 写 `http.ResponseWriter`），客户端断开 = 任务死亡 |
| 验证 | `verify.go` 三态复验 + `verify_result` 结构化事件（判据唯一来源） | 已完备，直接复用 |
| 记忆 | `lesson.go` Lesson（Scope/Confidence/Succ/Fail/Stale 三件套 + `lesson-outcomes.json`）；`memory.go` Memory（`memories.json`，stale 检测、打分召回） | 只在对话里被动注入；没有「任务结束自动沉淀」的生产线；`Trigger` 字段靠人工描述，无结构化触发特征 |
| 任务实体 | `state.go:239` `TaskHandle`（互斥组/超时/结果三态）——但它是**内存**的单次执行句柄 | **不存在**可持久化、可恢复、可排队的任务实体 |

---

## 一、设计红线（与全库既有哲学一致，先立住再写代码）

1. **判据来自系统记录，不来自模型转述**（红线 R3 的延伸）：
   Mission 成败由 `verify_result`（`verify.go` `aiVerifyEvent`）判定；
   预案是否「有效」由历史执行的复验证据统计，不由模型打分。
2. **写操作永远过确认**：自动化的上限是「把方案准备好 + 一键执行」，
   不是「不问就做」。预案执行同样过 `PolicyGate` + `aiConfirmGate`；
   批量授权沿用现有族分级（`ai_grant.go`，目前仅服务启停族）。
3. **会写状态的主动能力默认关闭**（opt-in，与 `MemoryEnabled` 同哲学）：
   **动作类** Mission 的自动执行调度、入站触发端点全部默认关，配置显式
   开启。**纯只读巡检/晨报类默认开**（2026-10-26 定案：只产出报告、
   不写任何状态，争议最小；若报告里含动作建议，建议本身仍走确认）。
4. **一切可审计**：每个 Mission 全程带 `trace_id`（`trace.go` 现有机制），
   自动触发与手动触发在审计链上同权重可追溯。
5. **后台执行与前台对话共用一把执行锁**：`aiChatMu` 语义升级为
   「同一时刻只有一个 AI 执行体在跑」，Mission 后台跑时用户对话排队，
   而不是绕开串行化（绕开就回到了本轮刚修掉的串台问题）。

---

## 二、Phase 1：Mission 成为一等公民（A 的骨架 + B 的容器）

### 2.1 实体与存储

新文件 `mission.go` + `missions.json`（与 `memories.json`/`lesson-outcomes.json`
同款落盘风格：`var missionsFile = func() string {...}`，测试重定向到 TempDir）。

```go
// Mission 一个跨回合、可持久化、可恢复的任务实体。
// 与 TaskHandle 的区别：TaskHandle = 一次脚本执行的内存句柄（秒级，互斥组）；
// Mission = 用户/触发器交给助手的一个目标（分钟~小时级，含多个 AI 回合）。
type Mission struct {
    ID        string    `json:"id"`         // m-<时间>-<短随机>
    Goal      string    `json:"goal"`       // 自然语言目标（晨报类任务放触发摘要）
    State     string    `json:"state"`      // 见 2.2 状态机
    Trigger   Trigger   `json:"trigger"`    // manual | patrol | webhook | schedule
    TraceID   string    `json:"trace_id"`   // 关联审计轨迹（trace.go 现有机制）
    Plan      []PlanStepRecord `json:"plan"` // 已完成步骤的落地记录（见 2.4）
    Pending   *PendingStep `json:"pending,omitempty"` // 断点：待确认/待执行的下一步
    Playbook  string    `json:"playbook,omitempty"`  // 套用的预案 ID（Phase 2 起）
    Result    *OpResult `json:"result,omitempty"`    // 终态结论（复验驱动）
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
    ExpiresAt time.Time `json:"expires_at"` // 终态保留期（默认 30 天，对齐审计窗口）
}
```

配置面（`config.json` 新增 `"missions"` 段；**巡检类默认开、动作类默认关**，
2026-10-26 定案）：

```json
{ "missions": { "enabled": true, "patrol_interval_min": 60,
                "inbound_hook": false, "briefing_at": "08:30" } }
```

- `enabled`/`patrol_interval_min`/`briefing_at`：只读巡检与晨报，默认开
  （产出报告型 Mission，不含写操作）；
- `inbound_hook`：入站触发端点，默认关；
- 任何会写状态的动作，无论 Mission 来源，一律过确认（红线 2，不受此配置
  影响——开关只控制「要不要主动起行动」，不控制「要不要问」）。

### 2.2 状态机（转移表即测试用例）

```
            ┌──────── 用户取消 ────────┐
            ↓                          │
 pending ──确认/批准──▶ running ◀──续作── paused（暂停/EnvKit 重启降级/断流）
    │                      │  ▲
    │（触发器产出、待人批）  │  └─ 用户批准续作
    └──▶ awaiting_confirm ─┘
                           │
              ┌────────────┼─────────────┐
              ▼            ▼             ▼
            done        failed        expired
        （复验通过）  （复验未通过/    （超 ExpiresAt
                       预算耗尽）        未到终态自动收）
```

规则：
- `canceled` 是用户取消的终态（2026-10-26 实现时补入：原图只有
  done/failed/expired 三个终态，但「取消」需要落点，任何非终态都可转）；
- `running → awaiting_confirm`（2026-10-26 实现时补入）：回合中途弹出确认卡
  后连接断开时，收口落 `awaiting_confirm` 而不是 `paused`——状态语义更准
  （卡在确认上 ≠ 主动暂停），续作上下文会提示「待确认 XXX」；
- `running` 的 Mission 在 EnvKit 启动时一律降级为 `paused`（**绝不自动续跑**：
  隔夜重启后无人值守地执行写操作违背红线 2）。
- `awaiting_confirm` 超 30 分钟（对齐 `aiConfirmWaitMax` 的 10 分钟 + 余量）
  降级为 `paused`，不作废——卡片登记 TTL 本来就有 30 分钟。
- `done`/`failed` 的判定**只认** `verify_result` 三态（红线 1），
  没有复验证据的一律 `failed` 而不是 `done`（宁可错杀，不发假徽章）。
- **两类例外（v3.6 修订，判据都落在客观事实上，不是模型自述）**：
  ① 报告型（`trigger=patrol|schedule`）以「步骤全部成功且 >0」为完成 ——
  产出即完成，没有执行阶段；② **纯只读行动**以「全部步骤成功 + 没有任何写工具」
  为完成 —— 只读检查没有环境状态可复验，用动作判据卡它会让「检查数据库」这种
  一步成功、什么都没改的行动**永远 failed**，进而把首次解决率、R6 连败、
  预案命中率一起算错（2026-10-30 由 `missions.json` 的真实记录发现）。
  判定函数 `missionReadOnlyDone`：注册表里查不到的工具名一律按写处理。

### 2.3 API 面

```
GET    /api/missions            列表（含 paused 的「待你继续」）
POST   /api/missions            手动新建（body: {goal}）→ 走 AI 对话通道执行
POST   /api/missions/:id/{pause|resume|cancel}
POST   /api/trigger             入站触发端点（schedule/patrol 与 guard 降级挂钩共用
                               入口，`inbound_hook` 默认关）。**只服务本机调用**：
                               Web 本来就只听本机，不新增暴露面，鉴权沿用现有
                               API 机制。body: {kind, title, detail} → 产出 pending 行动
GET    /api/missions/events     SSE：行动列表实时状态（前端轮询降级兼容）
```

> 入口（2026-10-26 定案）：行动列表只放 **AI 抽屉新增一个 tab**，不进桌宠
> 右键菜单（入口收敛、改动最小；右键菜单入口 Phase 2 视使用反馈再议）。

> 已定案（2026-10-26 评审）：无企微/外部系统接入计划，入站触发不做对外暴露、
> 不做回调鉴权设计。外部告警将来若要接，走「出站 Webhook 的格式适配层」
> （`alerts.go` 现有通道反向复用），而不是入站端点公网化。

### 2.4 与执行层的关系（关键架构决策：Phase 1 **不动** `aiRunLoop`）

现状 `aiRunLoop(ctx, w, fl, ...)` 直接写 `http.ResponseWriter`——执行与请求
同生共死。Phase 1 不做「脱离浏览器的后台执行」，而是先解决 80% 的问题：

- **Mission 记录回放点**：每个 plan step 完成即落盘（`PlanStepRecord`），
  断流后「续作」= 从最后完成的步骤接着跑（把 `PendingStep` 交给新一轮
  `/api/ai/chat`），而不是整任务重头再来。这同时消掉两轮复审都点名的
  「网络抖动断流 = 整轮白烧」。
- **续作时的模型上下文**（2026-10-26 定案）：**摘要 + 关键结果全量**——
  复用现有摘要通道传前半段压缩摘要，`PlanStepRecord` 里每步的
  `verify_result`/工具结论**全量**带上（成败判据与关键事实不丢），
  中间过程对话原文不回传。请求体可控、缓存友好，且符合红线 1
  （判据来自系统记录，全量带的就是记录本身）。
- **执行仍绑定一个 HTTP 请求**：SSE 断开 = 该回合失败 → Mission 落
  `paused`（带失败原因），用户回来点「继续」。
- 收益：`aiChatMu`、确认流、预算治理全部原样复用，Phase 1 改动面收敛在
  「加实体 + 加记录点 + 加触发入口」。

Phase 2 再评估「脱离请求的后台 runner」（`context.Background()` + 事件落盘 +
前端 SSE 订阅），前提是 Phase 1 的续作数据结构已被证明够用。

### 2.5 触发框架（B 的第一块）

- **schedule 类（巡检/晨报）**：固定间隔或 `briefing_at` 时刻产出「巡检
  Mission」——内容是只读诊断（`get_system_state`/`get_diag_report`/
  崩溃记录/磁盘趋势），产出**报告型**结果，不含写操作。默认开。
- **入站 webhook**：`POST /api/trigger`（`inbound_hook` 默认关；带本机令牌
  校验，与现有 API 同款）。调用方（本机脚本/监控）投递事件 → 产出
  `pending` Mission，**等人批**才执行任何动作（红线 2）。
- **与 guard.go 的分工**（防重复建设，最重要的一条）：
  guard 是**确定性快路径**（死了就重启，已有完整闭环），继续原样跑；
  触发器只接 guard **接不住**的事件——「重启了但复验没过」「恢复未确认」
  （`guard.go:269/276` 这两类告警）才是需要诊断判断的，转 Mission。
  即：guard 降级/失败的告警自动升级为 Mission 候选。

### Phase 1 验收

1. 中途杀掉 EnvKit 重启：Mission 还在、状态正确降级为 paused；
2. 断流后续作：从最后完成的步骤接着跑，不重头（可用桩上游验证）；
3. 发一条 `POST /api/trigger`，行动列表出现 pending 项，批准后执行；
4. guard 复验失败的告警自动变成 Mission 候选；
5. 状态机转移表 100% 有单测（表驱动）。

---

## 三、Phase 2：预案库 + 主动闭环（A 纵深 + B 兑现）

### 3.1 Playbook（预案）

新文件 `playbook.go` + `playbooks.json`。**不是** Lesson 的替代品：
Lesson 是「一句话经验」（给模型看的提示词素材），Playbook 是「结构化动作
序列 + 验证模板」（给系统执行的）。两者互相引用：

```go
type Playbook struct {
    ID        string   `json:"id"`
    Title     string   `json:"title"`       // 「后端反复崩溃：清端口→重启→复验」
    // 触发特征：结构化，供触发器匹配（告警标题模式 + 关键症状）。
    // 这是 Lesson.Trigger 自由文本的结构化版本——自动沉淀必须有机器可匹配的键。
    Matcher   PlaybookMatcher `json:"matcher"`
    Steps     []PlaybookStep  `json:"steps"`  // {tool, args_template, expect_verify}
    LessonIDs []string  `json:"lesson_ids,omitempty"` // 关联经验（注入提示词）
    Stats     PbkStats  `json:"stats"`       // used/succ/fail（succ 只认复验通过）
    State     string    `json:"state"`       // active | review | retired
}
```

- **沉淀入口（人工确认制，Phase 2 不做全自动）**：
  Mission 终态后，界面出「存为预案」按钮——把 `PlanStepRecord` 序列 +
  复验证据模板打包。自动沉淀（Phase 3）须等成功率统计攒够再说。
- **执行仍是「预案起草 + 用户确认」**：套预案只是省掉「推理怎么修」，
  不省确认（红线 2）。套用时每个写步骤照常过 PolicyGate。
- **质量衰减**：连续 3 次执行复验未通过 → `state=review`（界面标黄
  「待复审」，不再推荐套用），复用 `lesson.go` 的 stale 哲学。

### 3.2 告警闭环与晨报

- **闭环**：入站/守护告警 → 匹配 Playbook（有）→ Mission 预填步骤，
  桌宠举徽标「检测到 X，建议执行 Y（预案 Z）」→ 用户确认 → 执行 →
  `verify_result` 徽章。无预案 → 走完整 AI 诊断（就是现在的对话能力）。
- **晨报**：`briefing_at` 配置时刻，产出一个报告型 Mission（只读）：
  昨晚崩溃记录（`svcRecord`）、磁盘趋势、组件状态漂移、到期项。
  聚合的数据源全部已存在，无新采集面。

### Phase 2 验收

1. 人为制造后端崩溃（guard 重启失败场景）→ 桌宠主动弹建议 →
   确认后套预案执行 → 复验徽章；
2. 同场景第二次：Mission 直接预填预案步骤，处置步数显著下降；
3. 预案连败 3 次自动进 review 态；
4. 晨报在配置时刻产出，内容全部来自系统记录（无模型编造空间）。

### 3.3 落地记录（2026-10-27，代码已合入 main）

**新文件**：`playbook.go` + `playbook_api.go` + `playbooks.json`（复用
`missionsFile` 同款测试重定向）、`briefing.go` + `briefings/<日>.md` +
`briefing-hist.jsonl`（磁盘趋势快照）、`playbook_test.go` / `briefing_test.go`。

**预案匹配**（`pbkPick`）：`Matcher{svc, err_kind, keywords}` 里
`svc`/`err_kind` 是 **AND 门槛**（对不上直接出局），`keywords` 只加分。
排序完全确定：分数 → 历史成功率 → ID 字典序——没有"随机挑一个"，
否则同一故障两次点击给出的建议会不一样，用户无法建立信任。
连败 3 次（`pbkFailReview=3`）自动 `state=review`，界面标黄、不再推荐套用；
复验通过一次即清零连败计数（不是"总失败率"，久病成医的预案不该被判死）。

**告警闭环**：入站/守护产出 pending 行动的同一处（`trigger.go` `msProduce`）
套预案 → 写入 `Mission.Playbook` + `Mission.Draft` → 前端在行动卡片上
列出"预案步骤（未执行，需你批准）"，写操作额外标 `【写操作】`。
**套预案不产生任何执行入口**：它只把步骤摆好，执行照旧走
`/api/ai/chat` + PolicyGate + 确认卡（红线 2 未被"省推理"顺带省掉）。

**Draft 与 Plan 严格分开**（设计外的补丁）：`Plan` = 已发生的步骤记录，
`Draft` = 预案起草的未执行步骤。混进 `Plan` 会让"步骤数"和"续作回放点"
两个语义同时失真——续作时模型会以为这些步骤已经跑过了。

**晨报**（`briefingGenerate`）：纯函数聚合 `briefingCompose`，七个板块
（服务状态 / 近 24h 崩溃 / 磁盘趋势 / 未收口行动 / 组件 / 链端 / 版本），
每条关注项后面直接写判定依据（哪个阈值、哪个快照）。**不调模型**——
有模型参与就有编造空间，"晨报里那句没依据的话"是最难被发现的错。
产出即 `done`（报告型行动的收口口径），报告同时挂在 `Mission.Report` 上，
`GET /api/missions/report?id=` 读、`&download=1` 下载。

**状态机的一处例外**（`missionFinish`）：报告型行动允许 `pending → done`，
因为晨报没有执行阶段，先转 `running` 再 `done` 是给状态机演戏，而"假
running"会污染一切按状态统计的东西。**但判据落在事实上**——必须已经挂上
报告文件名（`m.Report != ""`），不是调用方传的 flag；可执行行动（不会设
`Report`）依旧被转移表挡在门外。`TestMissionFinishReportTypeFromPending`
用两条反向断言钉住这条边界，`TestMissionTransitionTable` 里
`pending → done` 仍记为**非法**。

**顺手修掉的真缺陷**：`aiMissionResumeContext` 原来只在有 `Plan` 时注入
上下文，于是巡检/晨报/守护/入站产出的**全新 pending 行动**（`Plan` 为空）
点「继续」时，模型收不到目标——行动一开局就是"你继续，但继续什么我不知道"。
改为**绑定即注入目标**，有已完成步骤才附摘要。

**测试面**：`playbook_test.go`（门槛 / 并列排序 / 衰减 / 沉淀 / 工具名校验 /
套用 / 归一化 / SvcID 解析）+ `briefing_test.go`（崩溃与熔断窗口 / 过期忽略 /
空数据诚实 / 磁盘趋势 / 保留份数 / attach 收口 / humanBytes）。
`withMissionEnv` 一并隔离 `playbookFile` 与 `notifyFn`——否则单测会往
真实桌面弹通知。

**未做（照 Phase 2 的边界）**：预案全自动沉淀（仍人工确认制）、
预案的跨版本迁移、晨报的推送（只挂在行动列表里）。

---

## 四、Phase 3：记忆闭环 + 成长可度量（C 兑现）

### 4.1 生产线：Mission → Lesson/Memory 自动沉淀

- 终态 Mission 自动产出 Lesson 候选（复用现有 kind 体系）：
  连败路径 → `repeat_fail`；用户否了 AI 方案后手动修好 → `user_fix`；
  复验分歧 → `verify_fail`。沉淀须过现有防摆锤阈值（`lsRepeatMin` 等），
  不是每个任务都写一条。
- 项目画像（`get_project_brief`）吸收 Mission 历史：端口布局漂移、
  组件版本变化、故障模式——画像从「静态登记」变「自动生长」。
  观察与推断分层照旧（`Source=config|probe|infer` 不破）。
- **预案与记忆互检**：预案执行失败的证据回流到关联 Lesson 的
  `Fail` 计数（`lesson-outcomes.json` 现有成败统计通道）。

### 4.2 成长指标（把「越用越聪明」变成曲线）

复用 `docs/eval` 评测框架 + 真实任务数据，按周出报表：

| 指标 | 定义 | 数据源 |
|---|---|---|
| 首次解决率 | 无人工修正即 done 的 Mission 占比 | missions.json |
| 平均步数/时长 | 同 Matcher 分组环比 | PlanStepRecord |
| 用户修正率 | `user_fix` Lesson 增速（越低越好） | lesson-outcomes.json |
| 预案命中率 | 告警中能匹配到 active 预案的占比 | missions + playbooks |
| 预案有效率 | 套预案后复验通过率（红线 1：只认复验） | verify 结果 |

### Phase 3 验收

1. 一次完整崩溃处置后，相关 Lesson/Memory 自动出现且带 Scope/Confidence；
2. 周报指标可从数据直接算出（不依赖模型转述）；
3. `ab_memory` 评测集纳入 Mission 续作场景（断流续作不丢上下文）。

### 4.3 落地记录（v3.5，含偏离）

实现落在 `sediment.go`（沉淀）+ `growth.go`（指标）+ `growth_api.go`。

**偏离 1：沉淀仍然写成「审计视图」，没有新建经验库。**

§4.1 说「终态 Mission 自动产出 Lesson 候选」，最直白的写法是读 missions.json
造 Lesson。但那会推翻 `lesson.go` 的第二条设计决策（经验 = 审计的一个视图），
并立刻产生两个事实源：用户否掉一条经验、审计被 30 天清理、missions 被淘汰，
三者时序一乱就会出现「经验还在、依据没了」——而那种经验没法被质证。

实际做法是**反向接**：终态收口时写一条规范化审计
（`action=mission_fix`，`target=<服务ID>`，`detail="[state] err=… goal=…"`），
再让新增的 R6 规则从审计里提取。代价是 Mission 经验也随审计过期；
这与 `missions.json` 的 30 天保留期一致，语义也对。

**偏离 2：R6 只做两条规则，不是三条。**

设计点名三类（连败 / 用户否了 AI 后自己修好 / 复验分歧）。后两类审计已有的
R2、R5 就在提取，Mission 维度再写一遍会让同一个事实生成两条措辞不同的经验，
互相抢提示词里的名额。所以 R6 只补审计**看不见**的两种：

| 规则 | 事实 | 为什么审计做不到 |
|---|---|---|
| R6a 目标级连败 | 同一服务连续 N 次行动没做成 | 审计看到的是十几个普通工具调用，没有任何一条说「这个目标没达成」 |
| R6c 用户放弃后接手 | 用户取消整轮 → 30 分钟内自己在同服务上做成 | R2 要求「同 action 先失败后成功」，而用户放弃后往往换了动作，action 对不上 |

R6a 的「连续」是**从最后一次成功往后数**，不是历史累计：按累计算的话，
一条经验会在问题修好之后继续告诉模型「这个服务修不好」。

**必须记下来的一处坑：R1 要跳过 `mission_fix`。**

R1（同 action+target 反复失败）与 R6a 生成的 ID 完全相同
（`repeat_fail|mission_fix|<svc>`）。不跳过的话先跑的 R1 占位，而 R1 统计的是
**累计**失败、成功不清零——结果是那条经验永远在说反话。两处都改会对同一事件
双倍计数。最终只在 `lesson.go` 的 R1 循环里跳过沉淀动作，R6 独占这个 ID。

**取消不算失败。**

`canceled` / `expired` 记 `resUnverified` 而不是 `resFail`。用户主动取消不是
「修不好」，超期也不是。把它们算进失败聚合，迟早产出一条「这个服务修不好」的
假经验，而真相只是用户那天没空。这条有反向单测钉着
（`TestSedimentCanceledIsNotFailure`）。

**预案与记忆互检（§4.1 第三条）没有新增任何写路径。**

要求是「预案执行失败的证据回流到关联 Lesson 的 Fail 计数」。查了一遍发现
它**已经自动成立**：`missionSediment` 写审计 → `auditNow` 里的 `lessonIDFor`
认出那条经验 → `recordOutcome` 记一次。在这里再加一条 `recordOutcome` 就是
双倍计数，而双倍的坏处不是数字难看，是置信度被同一次事件抬高两档。
所以这一项交付的是：① `pbkFromMission` 回填 `LessonIDs`（可解释）；
② `TestMissionSedimentFeedsLessonOutcome` 把这条隐式链钉死——
它最容易「以为通了其实没通」，断了只表现为「置信度涨得慢」。

**成长指标的三个口径决定。**

- 分母为 0 返回 `rateNA`（-1），界面显示「—」。0/0 显示成 0% 是撒谎：
  它看起来像「全都没成」。
- 分桶用**收口时间**（`UpdatedAt`）。用创建时间会让跨周处置落在它开始的那周，
  看起来像「上周积压一堆、这周零产出」。
- 各桶之和必须等于总数，写成 `growthAssertBuckets` 并有单测。
  加不回去说明分类口径重叠——那比数字难看严重得多：两个页面会给出两个
  「解决率」，而用户没法判断哪个是真的。

**未做（照旧）**：§4.1 说的「项目画像吸收 Mission 历史」这一条没做。
`projectbrief.go` 的画像目前是配置 + 探测的视图，要吸收故障模式得先定
「观察到的事实」与「推断」怎么分层展示，否则会把推测写成登记项——
那正是 `Source=config|probe|infer` 分层一直在防的事。留到有真实故障样本再动。

### 4.4 落地记录（v3.6.1：先把闭环串起来测）

**问题不是某一段坏了，是没人测接缝。**

到 v3.6.0 为止，mission / playbook / sediment / lesson 各有单测（合计 700+ 项），
但**没有一条测试把五段串起来**。而实战中出问题的恰恰全是接缝，且每次都
「每段都对、连起来不对」：

| 真实故障 | 单测反应 |
|---|---|
| 注入只在「有已完成步骤」时发生 → 全新 pending 行动点「继续」时模型反问“你想让我做什么” | 全绿（`aiMissionResumeContext` 自己测得很完整） |
| 「存为预案」没去重 → 连点两下得到两条一样的预案，删一条另一条还在 | 全绿（`pbkFromMission` 单次调用完全正确） |

所以补的是**顺序**，不是覆盖率：`agent_loop_e2e_test.go` 按用户能感知的顺序
走一遍，这一条测试里七个阶段全部断言：

```
① 告警 → pending 行动（库里没预案时不许凭空套）
② 执行 + 复验 → done（结论来自 verify_result），沉淀审计已写出
③ 存为预案（步骤取自 Plan、匹配键取自 Trigger）→ 再存一次必须被拒
④ 同类告警 → 命中 + 起草 Draft（Plan 仍为空：两个字段严禁混）
⑤ 执行 + 复验 → done，预案统计 1 用 1 成
⑥ 复验连败 3 次 → 转待复审，且下一次告警不再推荐它
⑦ 3 次失败沉成 R6 目标级经验，中文任务（「后端起不来」）能召回
```

配套的两条纪律：

- **断言先验证过能红**。抽掉 `x.Draft = …` → ④ 红；抽掉 `pbkSettle` → ⑤ 红。
  会误报的断言比没有断言更坏，所以新断言都要先跑一次「故意改坏」。
- **不测模型**。决定调哪个工具是模型的事，这里用夹具注入；判据读的是落盘的
  `PlanStepRecord.ok` 与 verify 结论——与模型是谁无关。真实模型的收口由评测题
  T16 覆盖（判据读 `/api/missions` 的 state，不读回答文本）。

**顺手修掉的一个「接口说谎」**：`missionFromGuard` 命中预案后返回的仍是
套预案**之前**的副本——日志写「命中预案 X，等用户批准」，返回值里 `Playbook`
是空的。目前唯一的调用方（`guard.go`）不用这个返回值，所以还没造成故障；
但 `handleMissionTrigger` 结尾特意重读一遍（注释：「回包带上最新状态」），
正是同一个坑的补丁。这次补在源头，并在 E2E 第 ④ 阶段用断言钉住。

**这一版不覆盖什么（明写）**：
- T16 与 T15 一样需要真实模型额度与可连的 MySQL，**本机未实跑**；
- 预案的**自动**沉淀仍然没做（人工确认制是设计选择，不是欠账）；
- 成长指标首期基线仍未出数：现在有真实样本了（闭环跑起来才产生 `done`/`failed`），
  但样本量远不够做周环比。

---

## 五、与现有代码的映射（复用 vs 新建）

| 复用（原样接上） | 新建 |
|---|---|
| `verify.go` 三态复验 + `aiVerifyEvent`（成败唯一判据） | `mission.go` + `missions.json`（实体/状态机/续作） |
| `policy.go` PolicyGate、`ai_confirm.go`/`ai_wait.go` 确认流 | `trigger.go`（入站端点 + patrol 调度） |
| `plan.go` `planTrack`（回合内步骤 → 落盘成 `PlanStepRecord`） | `playbook.go` + `playbooks.json`（Phase 2） |
| `lesson.go` Scope/Confidence/Stale + 防摆锤阈值 | Mission→Lesson 沉淀管道（Phase 3） |
| `memory.go` 打分召回/stale | 成长指标报表（Phase 3，消费 eval 框架） |
| `alerts.go` 抑制窗口（出站告警不变） | 入站触发（方向相反，独立端点） |
| `guard.go` 确定性快路径（不动） | guard 降级事件 → Mission 候选的挂钩 |
| `trace.go` trace_id、`aiChatMu` 串行化 | —— |

明确**不做**（防 scope 膨胀）：
- Phase 1 不做脱离浏览器的后台执行器（先用续作拿 80% 收益）；
- 不做多用户/权限（EnvKit 是本机单用户工具）；
- 不动 guard 的自动重启语义（它已验证可靠，职责边界清晰）；
- 不做预案的全自动沉淀（Phase 2 人工确认制）。

---

## 六、评审定案记录（2026-10-26 全部拍板）

1. **命名**：对外中文名 **「行动」**（见文档头命名约定）；代码实体仍叫
   Mission。不与「任务轨迹/任务名」（trace 文案）混淆。
2. **入站触发的鉴权**：无企微/外部系统接入计划，只做本机面，
   不新增暴露面、鉴权沿用现有 API 机制（见 API 节说明块）。
3. **续作的模型上下文**：**摘要 + 关键结果全量**（见 2.4），
   中间过程对话原文不回传。
4. **晨报默认配置**：**巡检类默认开、动作类默认关**（见红线 3 与配置面）；
   动作类永远过确认，开关不豁免红线 2。
5. **行动列表入口**：**AI 抽屉加一个 tab**，不进桌宠右键菜单
   （见 API 节入口说明）。
