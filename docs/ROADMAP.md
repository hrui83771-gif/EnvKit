# EnvKit 长期规划：v2.1 → v3

> **Runtime 是底座 · Agent 是上层 · UI 是仪表盘 · Trace/Memory 让它成长 · Evaluation 证明它变强**
>
> 分支：`v3`（长期开发分支，逐版本推进，每版打 tag）
> 基点：`6eae5c7`（v2.0.0 + 启动方式推断）
> 文档状态： living plan，随代码事实修订

---

## 零、现状盘点

> 本节写于 v2.1 规划时。**v2.2 已交付五项，标注了哪些缺口被填上。**
> 结论全部锚到源码，可复核。

### 0.1 主链路八环节的成熟度

| 环节 | 现状 | 缺口 |
|---|---|---|
| Observe | 组件检测、端口扫描、进程统计（`detect.go` / `portscan.go`） | 只看"装没装"，不看"配得对不对"（无环境符合性判断） |
| Understand | `projectBrief`（项目画像）+ `aiHealthSnapshot`（环境快照） | **两个独立事实源**，字段不互通；**观察与推断未分层** |
| Plan | **不存在**。AI 直接调工具，无显式计划、无依赖序、无回滚 | 无法回答"你打算怎么做""失败怎么退" |
| Policy | ✅ v2.2：`PolicyGate` 四档单点裁决（`policy.go`） | ✅ 已解决；剩余是把 Plan 步骤纳入判定 |
| Execute | 任务函数返回 `OpResult`；v2.1 已统一前端启动脚本与后端入口 | 部分路径仍返回裸 `error`；`StartSpec` 尚未泛化到备份/恢复 |
| Verify | 三个验证器 + ✅ v2.2 存活观察 5s / 崩溃取证 / 端口漂移 | **备份未验可还原**（v2.3 最高优先 🔴） |
| Audit | 完整（JSONL，30 天，含 verify 字段） | ✅ 已解决；`trace_id` 由 Trace 补齐 |
| Learn | `lesson.go` + `memory.go` | 无 Scope / Confidence / 失效 / 纠正；**无 Human Trace 监督信号** |

### 0.2 规模与结构

```
代码      14,259 行 Go（v2.1 盘点）
          14,119 行 Go（v2.2 实测）+ 4,079 行单文件前端
API       68 → 70+ 路由（新增 /api/runtime/state /api/launch /api/lessons /api/memory）
          工具 17 个      界面 5 个面板
测试      156 → 235 项单测 + 20 题冒烟集 + 13 套 CDP + 14 项端到端 AI 验收
状态计算  ✅ 已统一：/api/runtime/state 是唯一源，/api/health 从它取数
```

### 0.3 四个结构性问题

| # | 问题 | 状态 |
|---|---|---|
| 1 | **事实源分裂**：`projectBrief` / `aiHealthSnapshot` / `chainInfo` 无一致性约束 | 🟡 统一状态源已建立；五层事实结构待 v2.3 |
| 2 | **观察与推断不分**：`Source=config\|package.json\|probe\|infer` 把"读到了什么"和"由此得出什么"混在一起，出错无法定位哪一层错了 | 🔴 待 v2.3（Observation/Fact/Inference/Decision/ActionSpec） |
| 3 | **执行语义分散**：按钮 / AI / 批量三条路径校验强度不同 | 🟡 Policy 已统一；`ActionSpec` 泛化待 v2.3 |
| 4 | **状态各算各的**：前端三套轮询重复计算 | ✅ **v2.2 已解决**（`runtime.go`） |

---

## 一、总体架构：四条线

```
┌──────────────────────────────────────────────────────────────┐
│  Evaluation    用真实任务证明它变强（消费 Trace）              │
├──────────────────────────────────────────────────────────────┤
│  Learning      Trace → Lesson → Memory（消费 Trace，反哺）     │
├──────────────────────────────────────────────────────────────┤
│  UI            仪表盘：状态全部来自 Runtime，用户可绕过 AI     │
├──────────────────────────────────────────────────────────────┤
│  Agent         Plan → Policy → Execute → Verify → Re-plan     │
│                 （只编排，不掌握底层执行逻辑）                 │
├──────────────────────────────────────────────────────────────┤
│  Runtime       可靠底座：观察 / 事实 / 状态机 / 动作 / 验证    │
│                 环境·项目·进程·端口·数据·链端                   │
└──────────────────────────────────────────────────────────────┘
```

四条线的关系是**单向依赖**：Agent 只能通过 Runtime 提供的能力动作，UI 只能读 Runtime 的状态，学习只从 Trace 提取，评测只消费 Trace。**任何一条线都不能绕过 Runtime 自己算状态或自己动手。**

---

## 二、Runtime 线（底座）

> Runtime 的职责：把"这个环境现在是什么状态"和"要让它变成某个状态"这两件事，做到**可靠、可验证、可追溯**。

### 2.1 认知分层：Observation → Fact → Inference → Decision → ActionSpec

这是 Runtime 最核心的结构。**当前规划里的 `DecisionFact` 把中间几层压成了一层，导致出错时无法定位。** 拆成五层：

| 层 | 含义 | 例子（前端启动） | 谁能写 |
|---|---|---|---|
| **Observation** | 传感器读到的原始现象，不做解释 | `package.json` 存在，mtime=X，大小=Y | 只有探测器 |
| **Fact** | 从观察中解析出的、无歧义的结构事实 | `scripts.dev = "vite"` | 只有解析器 |
| **Inference** | 依据规则从事实推出的判断，可能错 | "`dev` 很可能是启动脚本"（置信 0.9） | 规则引擎 |
| **Decision** | 在多个推断中做出的选择 | "选 `dev` 而不是 `serve`" | Agent 或用户 |
| **ActionSpec** | 可执行的、带完整上下文的动作描述 | `cwd` + `npm run dev` + 来源链 | Policy 校验后生成 |

**价值**：出错时能精确定位——

| 症状 | 哪一层错了 |
|---|---|
| 启动命令完全不对 | Observation（读错文件）或 Fact（解析错） |
| 命令对但服务没起 | Inference（规则错）或 Decision（选错） |
| 服务起了但 AI 说没起 | Verify（判据不足） |
| 复验说好了但实际没好 | Verify（判据错） |

```go
type Observation struct { Key, Raw, ProbeAt, Err string }

type Fact struct {
    Key      string
    Value    any
    Source   string      // 指向 Observation 的引用
    Observed time.Time
}

type Inference struct {
    Key    string
    Value  string
    Rule   string        // 哪条规则推出来的
    Conf   float64
    From   []string      // 依赖的 Fact key
}

type Decision struct {
    Key    string
    Value  string
    Reason string        // 为什么选它而不选其他
    Alt    []string      // 被放弃的候选
    By     string        // ai | user | rule
}

type ActionSpec struct {
    Target  string      // web | backend | db | chain
    Action  string      // start | restart | backup | restore
    Cmd     []string    // argv，不经 shell
    Cwd     string
    Chain   []string    // 溯源链：decision ← inference ← fact ← observation
    Conf    float64
}
```

**硬约束**：

- `ActionSpec.Chain` 必须完整可回溯到 `Observation`，断链的动作**不允许执行**。
- `Inference` 层的东西**不得直接用于执行**，必须经过 `Decision`。
- 每层写入都带时间戳，超过 TTL 的 `Observation` 触发重采（见 2.2）。

### 2.2 环境与项目事实

| 事实 | 内容 | 失效触发 |
|---|---|---|
| `env.node.version` | 已装版本 | 组件升级 |
| `env.node.satisfies` | 是否满足项目要求（≥/^range） | `package.json` engines 变更 |
| `env.gopath` / `env.npm_registry` | 是否为项目实际使用 | `.npmrc` / `go env` 变更 |
| `proj.kind` | go / node / vue / react | 依赖清单变更 |
| `proj.entry` | 入口文件 | 文件移动 |
| `proj.web.launch` | 启动脚本 | `package.json` mtime 变更 |

**符合性检查**（v2.2 首批）：只报"装了 v1.23.6"没用，要报"项目要求 Node ≥18，当前 14，**跑不起来**"。

### 2.3 动作层：ActionSpec 统一

```
现在：启动（StartSpec）· 备份（无统一输入）· 恢复（无统一输入）· 停止（各自 handler）
目标：全部产出 ActionSpec → Policy → Execute → Verify
```

| 动作 | ActionSpec 关键字段 | Verify 判据 |
|---|---|---|
| start(web) | cwd, argv, 入口来源链 | 端口 + HTTP 握手 + 存活观察 |
| start(backend) | cwd, argv, 入口来源链 | 同上 |
| restart | 前两者 + 停机确认 | 停干净后重新 Verify |
| backup | 输出路径, 目标库 | 文件非空 + sha256 + **可还原**（深度） |
| restore | 备份文件, 目标库, **预检结果** | 还原后 schema 校验 + 行数比对 |
| stop | 目标服务, 进程标识 | 端口释放 + 进程退出 |

**这是 Agent 不自己动手的保证**：Agent 只能请求 Runtime 执行一个 `ActionSpec`，执行逻辑、判据、权限全在 Runtime 侧。

### 2.4 进程与服务生命周期

```go
type ServiceState struct {
    Phase    string   // stopped | starting | running | degraded | failed
    PID      int
    Since    time.Time
    Restarts int      // 本周期内重启次数
    Backoff  string   // 退避状态（指数退避 + 上限）
    LastErr  string
    LastVerify time.Time
    LastEvidence []Evidence
}
```

补齐：进程树视图 + 孤儿检测（`procops.go` 已有基础）、崩溃重启策略与退避上限、端口漂移检测（实际监听 vs 配置预期）。

### 2.5 健康检查矩阵

| 检查 | 判据 | 状态 |
|---|---|---|
| 端口监听 | netstat + 进程归属 | ✅ |
| HTTP 握手 | 响应码与头 | ✅ |
| 端口漂移 | 实际 vs 预期 | ❌ v2.2 |
| 存活观察 | N 秒后仍在 | ❌ v2.2 |
| 崩溃检测 | 退出码 + 日志尾部 | ❌ v2.2 |
| 数据库连通 | SELECT 1 + 库存在 | ✅ |
| 备份完整性 | sha256 + 尾部标记 | ✅ |
| **备份可还原** | 导入临时库 + schema 校验 | ❌ **v2.2 最高优先** |
| 链端活性 | 共识视图推进 | ✅ |
| 环境符合性 | 版本满足 requirements | ❌ v2.2 |

### 2.6 备份可还原性（最高优先缺口）

现在验到"SHA256 一致 + 尾部完整"就算成功。**文件没坏 ≠ 能还原。**

深度验证：导入临时库 → 跑 schema 与行数校验 → 丢弃临时库。不覆盖真实库，设计为用户显式触发。

### 2.7 状态统一（供 UI 与 AI 共用）

现在前端三套轮询各算各的。Runtime 暴露**唯一状态源**：

```
GET /api/runtime/state
{
  "observed_at": ...,
  "env":    { "node": {...}, "go": {...}, "satisfies": [...] },
  "project":{ "kind": [...], "web": {...}, "backend": {...} },
  "services":{ "web": ServiceState, "backend": ServiceState },
  "db":     { "connected": true, ... },
  "chain":  { "phase": "running", "view": 869701 },
  "issues": [ { "level": "warn", "what": "node_modules 缺失", "action": "先执行 npm install" } ]
}
```

UI 与 AI 读同一份。**这是"UI 不再加入口"的前提**——状态中心没有数据源就做不出来。

### 2.8 可观测性

- Trace（见第四章）：一次任务的完整轨迹
- 指标时序：启动耗时、失败率、复验通过率、重启次数
- 审计不可变、只追加，保留 30 天

---

## 三、Agent 线（上层能力）

> Agent 只做四件事：**Plan → Policy → Execute → Verify → Re-plan**。它不掌握任何底层执行逻辑。

### 3.1 Plan：可核查的步骤序列

```go
type PlanStep struct {
    ID      string
    Spec    *ActionSpec    // 动作描述，不是命令
    Depends []string
    Expect  string         // 可证伪的期望状态
    Rollback string        // 破坏性步骤必须有
}
type Plan struct {
    Goal    string
    Steps   []PlanStep
    Unknown []string        // 明确"我不知道"的点
}
```

规则：

- `Unknown` 非空 → **必须问用户**，不许用猜测填坑。这是"承认无知"的结构化表达。
- `Expect` 必须可验证（端口、哈希、视图推进），不能是"服务正常"这种无法证伪的描述。
- Plan 里出现破坏性动作必须有 `Rollback`，否则 Policy 直接拒绝。

### 3.2 Re-plan：失败后的重来

现状：工具失败 → AI 自己解释一句就完了。

Re-plan 要求：失败后按 `Plan.Unknown` 与失败证据重新规划，**且必须声明"我改了什么判断"**。不允许静默换方案——静默重来是"AI 在瞎试"的典型特征。

### 3.3 PolicyGate：四档统一

现在三处分散（AI 白名单 / 428 确认 / 高危标记），语义不一致。收成四档：

| 档位 | 含义 | 例子 |
|---|---|---|
| `auto` | 直接执行 | 读状态、看日志、列目录 |
| `confirm` | 需用户确认 | 启停服务、备份 |
| `elevated` | 确认 + 明示风险 + 建议先备份 | 还原数据库、改配置 |
| `forbidden` | 拒绝，AI 不可绕 | 删除数据、危险脚本名 |

**三条硬边界（写成代码断言与单测，不只写在提示词里）**：

1. **Memory 不是权限** —— 记忆只影响建议顺序，不提升档位
2. **Lesson 不绕过 Policy** —— 经验命中不改变判定结果
3. **Inference 不直接执行** —— 必须经 Decision + Policy

### 3.4 AI 的边界

- 不许编造启动命令（用 ActionSpec）
- 执行后必须 Verify（`auto` 档的只读动作除外）
- 未复验不得说"已完成"
- AI 不可用时 Runtime 与 UI 完整可用

---

## 四、Learning & Evaluation 线

### 4.1 Trace：一次任务的完整轨迹

> 这是本规划相对上一版的**重点增强**。Audit 是"发生了什么"的流水，Trace 是"一次任务怎么走完"的完整过程。

```
一次「帮我把项目跑起来」应当形成：

Observe → Fact → Plan → Policy → Action → Evidence → Verify
   → Failure → Human Intervention → Recovery → Success → Lesson
```

| 环节 | 记录什么 |
|---|---|
| Observe/Fact | 读到了什么（原始观察 + 解析结果） |
| Plan | 打算怎么做、预期什么、哪里不确定 |
| Policy | 判定档位、用户是否确认、确认耗时 |
| Action | ActionSpec 全文 + 溯源链 |
| Evidence | 客观证据（端口、pid、sha256、视图值、日志尾部） |
| Verify | 复验判据与结论 |
| Failure | 失败归类（err_kind）+ 证据 |
| **Human Intervention** | **用户在本次任务中做了什么（详见 4.2）** |
| Recovery | 重试/换方案的过程与代价 |
| Success | 最终达成状态 |
| Lesson | 本次提取到的经验（若有） |

**Trace 与 Audit 的分工**：

| | Audit | Trace |
|---|---|---|
| 目的 | 合规留痕、防抵赖 | 优化与学习 |
| 粒度 | 一条操作一行 | 一次任务一棵 |
| 可变性 | 不可变、只追加 | 可裁剪（Evidence 只留摘要） |
| 保留 | 30 天 | 可短（Trace 数据量大） |
| 消费者 | 审计页、用户 | 学习层、评测层 |

### 4.2 Human Trace：人工操作作为隐式监督信号

**核心机制**：Agent 第一次尝试 A 失败，用户手动采用 B 成功 → 记录这个过程 → 后续同项目同环境下**优先尝试 B**。

这是当前 `lesson.go` 里 R2 规则（AI 试错 → 用户手动修好）的推广，但要做成**通用监督信号**而非单条经验。

关键设计：**介入判定**。用户动手不等于在监督本任务——

| 情形 | 判为介入？ |
|---|---|
| Agent 失败后 5 分钟内，用户执行同类动作并成功 | ✅ 是（大概率在接手） |
| Agent 运行期间，用户执行**无关**操作 | ❌ 否（噪声） |
| 用户动作与 Agent 失败动作**不同目标** | ❌ 否 |
| 用户动作与 Agent 计划内下一步**重复** | ✅ 是（用户 impatient，抢先做了） |

实现约束：介入判定必须在**同一个 Trace ID 上下文**内做，不能跨任务关联——否则会把"用户碰巧做了件事"误判成监督信号，污染经验。

**存储边界（硬要求）**：

| 采集 | 不采集 |
|---|---|
| 任务过程（步骤、判定、耗时） | 文件内容 |
| 环境变化（版本、依赖变更） | 密码 / Token / API Key |
| 命令名与参数（脱敏后） | 环境变量值 |
| 验证证据（端口、哈希、视图） | 数据库内容 |

即：**采集"过程"，不采集"内容"**。所有入库字段走既有 `auditSanitize`，并额外做本地脱敏。

### 4.3 Memory：第一批（v2.3）

先做最小可用集，**验证确实降低重复探索后再加复杂机制**。

| 字段 | 作用 |
|---|---|
| `Scope` | 适用边界：项目路径 + 环境指纹（OS/组件版本/依赖版本） |
| `Evidence` | 支撑证据（Trace ID 列表，可点进去看） |
| `SuccessCount` / `FailureCount` | 成败结算，决定置信度 |
| `Stale` | 项目或环境指纹变化即置位 |
| `Confidence` | 由成败计数推导的置信度 |

失效规则：

1. Scope 不匹配 → 不注入
2. 指纹变化 → `Stale` + 注入时降权到 0.3
3. 同 Action 连续失败 2 次 → 置信度归零并停止注入

**验收实验**：同一项目连跑 10 次"启动服务"，第 6 次手动改错启动脚本，观察：

- 前 5 次经验被正确命中，AI 直接采用（探索成本下降）
- 第 6 次起旧经验被标记 `Stale`，AI 重新读 `package.json` 并发现变化
- 全程无"AI 坚持用过时经验导致重复失败"
- **对照**：关闭 Memory 层跑同样序列，统计重复探索次数 → 这是"经验增益"的量化证据

### 4.4 Memory：第二批（v3.1 后，视第一批效果）

`Conflict`（新旧经验矛盾不静默覆盖）、`Correction`（用户可标记经验为错）、`Retired`（长期失效归档）。

**前提**：第一批能证明经验确实有用。否则第二批只是复杂度，没有收益。

### 4.5 Evaluation：消费 Trace

```
eval/
  tasks/       任务定义（ground_truth + success_criteria）
  fixtures/    可复现项目样本
  runner/      执行器
  metrics/     指标计算（数据源=Trace）
  reports/     报告
```

**评测时序**（不是等到 v3.1 才开始）：

| 优先级 | 做什么 | 何时 |
|---|---|---|
| P0 | 八项指标**口径**定义（不取数） | 现在 |
| P0 | **跑一次基线** | 现在 |
| P1 | 冒烟集 10 题（只覆盖已交付能力） | v2.1 |
| P2 | 全量集 40 题 + 自动化 Harness | v3.1 |

**基线现在能测**：启动推断正确性、验证层判据、经验提取准确率、越权拦截率。
**测不了**：Plan 合规率（v2.2）、经验增益（v2.3）、误拒率（需 PolicyGate）。

| 指标 | 精确定义 | 采集来源 | 可测起点 |
|---|---|---|---|
| 任务成功率 | 完成全部 criteria 的比例 | Trace + 执行器 | v2.1 |
| 首次成功率 | 第一次尝试即通过 | Trace 的尝试计数 | v2.1 |
| 故障恢复率 | 注入异常后自主恢复成功 | Trace 的 Recovery 段 | v2.1 |
| 无效操作次数 | 与目标无关的工具调用总数 | Trace 的 Action 段 | v2.1 |
| **验证准确性** | 声称成功与客观事实一致的比例 | Verify vs 真实探测 | v2.1 |
| 安全违规率 | 触碰 `forbidden` 的比例 | Policy 段 | v2.2 |
| 误拒率 | 合法操作被判 deny/confirm | Policy 段 | v2.2 |
| **经验增益** | 有经验 vs 无经验的成功率与耗时差 | A/B 对照 | v2.3 |

其中"验证准确性"是 EnvKit 独有的指标——别的项目测不了它，因为它正是本项目的主张。**优先做深。**

**基线会决定 v2.2 的优先级排序**，不由主观判断定。

---

## 五、UI 线

> 原则：**不增加入口，重新组织状态。用户始终拥有手动驾驶权。**

### 5.1 目标结构

```
首页（Status）      项目状态中心：现在什么状态、哪里有问题、最近发生了什么
├ Run               启停 / 重启 / 进程管理 / 端口诊断
├ Diagnose          健康检查 / 日志 / 链端诊断 / 诊断报告
├ Data              数据库 / 备份 / 还原 / SQL / 数据浏览
├ Agent             对话 / 任务 / 记忆与经验 / 操作审计 / Trace 视图
└ Settings          组件 / 项目配置 / 链端配置 / AI 配置
```

### 5.2 首页 = 状态中心

以**实体**组织而非功能组织，且**所有状态来自 `/api/runtime/state`**，每页自己算的必须去掉：

```
┌─────────────────────────────────────────────────┐
│  FarmTrace                          [ 全部启动 ] │
├───────────┬───────────┬──────────────────────────┤
│ 前端 ●运行 │ 后端 ●运行 │ 数据库 ●已连接           │
│ :8080·2h  │ :8888·45s │ farm                    │
│ [重启][停]│ [重启][停]│ [备份]                   │
├───────────┴───────────┴──────────────────────────┤
│ 链端 ●共识推进  块1105  视图869701                 │
│ 依赖 ⚠ 1 项待处理（node_modules 缺失）            │
├─────────────────────────────────────────────────┤
│ 最近：前端已启动 45s 前 · 备份完成 2h 前          │
│ ⚠ 1 条经验已失效（依赖变更）  [查看]              │
└─────────────────────────────────────────────────┘
```

要点：

- 状态灯全局统一：绿=健康 / 黄=降级或未验证 / 红=失败 / 灰=停止
- 手动控制按钮**直接可用**，不经过 AI
- **AI 不可用时首页与所有工作区功能完整**（硬要求）

### 5.3 手动驾驶权保障

| 保证 | 实现 |
|---|---|
| AI 能做的，手动都能做 | 手动路径不依赖 AI 配置与记忆开关 |
| 手动与 AI 同等留痕 | Trace 的 actor 区分 user / ai，结构一致 |
| 手动可覆盖 AI 判断 | Settings 可锁定关键项，Agent 不得覆盖 |
| AI 挂了系统完整 | AI 模块故障不影响 Runtime |

---

## 六、版本路线

| 版本 | Runtime | Agent | Learning | UI | 评测 |
|---|---|---|---|---|---|
| **v2.1** ✅ | 五层认知结构、启动推断 | — | Trace 骨架 | 状态字段就位 | **口径 + 基线 + 冒烟集 10 题（10/10）** |
| **v2.2** ✅ | 生命周期验证、服务状态机、`/api/runtime/state` | **PolicyGate 四档** | Trace + `trace_id` 贯通 | 状态源统一 | **冒烟集 20 题（20/20）+ 基线回填** |
| **v2.3** | 备份可还原、环境符合性、ActionSpec 泛化、`ai_loop` 拆分 | Plan + Re-plan | **Trace 补齐 + Human Trace + Memory 第一批** | Trace 视图 | **经验增益 A/B** |
| **v3.0** | 可观测性指标时序、恢复向导 | — | Memory 第二批（若需要） | **仪表盘化** | 全量集 40 题 + Harness |
| **v3.1** | — | — | — | 手动驾驶权验收 | 自动化 Harness + 指标回退阻断发布 |

每个版本独立可发布、独立可回滚。**任一版本未达标不进下一版。**

> v2.1 计划里的「事实 Stale 机制」与「`program.go` 拆 `sqlrisk.go`」未做，
> 顺延至 v2.3。前者被 Memory 的 `Scope/Stale` 覆盖（同一问题的更完整解法），
> 后者优先级低于 v2.3 的两项 🔴。

### 6.0 指标可测性演进（实测）

| | 🟢 可测 | 🟡 数据源就位 | 🔴 缺前置 |
|---|---|---|---|
| v2.1 基线 | 0 | 0 | 8 |
| **v2.2 基线** | **1** | **3** | **4** |

仍然最关键的两项：**任务成功率**（需 v3.1 Harness）与**经验增益**（需 v2.3 Memory）。
v3.0 发布时若仍无这两项数据，"比上一版更强"没有支撑。

### 6.1 明确不做

- ❌ 自建 LLM 推理层（用现成模型 API）
- ❌ 通用工作流引擎（Plan 只做可核查的步骤序列）
- ❌ 多项目 / 多环境编排（当前是单项目本地工具）
- ❌ 自动修改用户配置（除非显式确认且可回滚）
- ❌ **把记忆做成权限来源**（永远不是）
- ❌ **采集文件内容与凭据**（只采过程，不采内容）

---

## 七、代码结构

### 7.1 大文件拆分（判据：职责边界 + 这块是否正在改）

```
ai_loop.go       1054   7 种职责混在一起
chain.go          885   5 种职责 + 一个约 200 行巨型函数
program.go        788   混入了与启停无关的 SQL 风险扫描 172 行
verify.go         693   3 个验证器共享 Evidence 构造，职责内聚
dbops.go          562   职责内聚
ai_tools.go       531   工具注册表，天然该集中
launch.go         304   职责单一 —— 同样是"不大但清晰"的好样板
```

**不做预防性重构**——v2.0 刚建立的测试网经不起一次纯搬家。拆分跟着功能演进走：

| 文件 | 拆法 | 时机 |
|---|---|---|
| `program.go` | `sqlrisk.go`（6 个函数 172 行属错位） | **v2.1 立即**，机械改动可立即验证 |
| `ai_loop.go` | `loop.go` / `stream.go` / `intent.go` / `summary.go` / `toolresult.go` | **v2.2 前必须**——Plan 层要改的正是主循环 |
| `chain.go` | `chain_ssh.go` / `chain_probe.go` / `chain_webase.go` / `chain_recover.go` | v2.2，与链诊断重构同批 |
| `verify.go` | 随 v2.2 状态机统一重构 | v2.2 |
| `dbops.go` / `ai_tools.go` | 不拆 | — |

原则：**新代码不再进 `ai_loop.go`**。

### 7.2 目标结构

```
runtime/    observe  fact  inference  decision  action  policy  verify  state
agent/      plan  execute  replan  tools
learn/      trace  lesson  memory  scope
adapter/    chain  mysql  node  ssh
ui/         index.html（内嵌）
```

---

## 八、系统基础能力专项

| 能力 | 现状缺口 | 计划 |
|---|---|---|
| 环境识别 | 不判"配得对不对" | 环境符合性检查 v2.2 |
| 进程管理 | 无孤儿检测与进程树视图 | v2.2 |
| 服务生命周期 | 无崩溃重启策略与退避 | 状态机 + 退避 v2.2 |
| 健康检查 | 不覆盖漂移/存活/崩溃/可还原 | 3 项 → 10 项 v2.2 |
| 配置 | 无漂移检测 | 指纹变化标记待复核 v2.1 起 |
| 备份 | **未验可还原** | **最高优先 v2.2** |
| 审计 | 无跨任务轨迹 | Trace v2.1~v2.3 |
| 恢复 | 流程不完整 | 一键向导 + 恢复前预检 v2.3 |
| 可观测性 | 无指标趋势 | 指标时序 v3.0 |

---

## 九、风险与取舍

| 风险 | 概率 | 影响 | 对策 |
|---|---|---|---|
| 大重构引入回归 | 高 | 高 | 每版独立发布独立回滚；保留旧路径适配层 |
| 五层结构过度设计 | 中 | 中 | 先只对"启动"一条链路落地，验证好用再推广到备份/恢复 |
| UI 重构丢能力 | 中 | 高 | 首页只做重组不删减；逐项验收功能可达性 |
| Human Trace 误判监督信号 | **高** | 中 | 介入判定必须在同一 Trace 内；先只取"失败后短时间内的同类成功动作"这一种最稳的模式 |
| 记忆机制越复杂越不可用 | 中 | 中 | 分两批（4.3 / 4.4），第一批无效就不做第二批 |
| Trace 数据量膨胀 | 中 | 中 | Trace 与 Audit 分开存；Evidence 只留摘要；Trace 保留期短于 Audit |
| 评测集变负担 | 中 | 中 | 分级：冒烟 10 题随提交，全量 40 题随版本 |
| 性能退化（快照膨胀） | 中 | 中 | 快照字段配额 + 分层注入，超限字段改为按需工具获取 |
| 工具链不稳定影响开发 | 已发生 | 中 | 验收以 Go 单测 + CDP 为主，减少对 Python 脚本依赖 |

---

## 十、近期行动项

### 已完成

**v2.1**

- [x] 五层认知结构（Observation/Fact/Inference/Decision/ActionSpec）落地，先只做启动链路
- [x] Trace 最小版（单任务轨迹，环节先记核心几个）
- [x] 八项指标口径写入 `eval/metrics/`
- [x] 冒烟集 10 题 → **实测 10/10**
- [x] 跑基线，产出 `docs/eval/baseline-v2.1.md` → **🟢 0 项可测 / 🔴 8 项**
- [x] 基线短板决定 v2.2 优先级（PolicyGate 与 Trace 提前）

**v2.2**（`edb318b` `0596e62` `3aefd93` `238ef30` `b83e892`）

- [x] PolicyGate 四档统一 + 判定日志（旧 `aiIsOptInTool` 改为委托，消除双源）
- [x] Trace + `trace_id` 零改动贯通审计（Trace 独立存储，不塞进 Audit）
- [x] 生命周期验证：存活观察 5s + 崩溃取证 + 端口漂移
- [x] `/api/runtime/state` 唯一状态源（含 `issues[]`，每条带可执行下一步）
- [x] 服务状态机：五态 + 崩溃聚合 + 退避建议（**明确不做自动重启**）
- [x] 冒烟集扩到 20 题 → **实测 20/20**，全量单测 235 项
- [x] 基线回填 `docs/eval/baseline-v2.2.md` → **🟢 1 项可测 / 🟡 3 项数据源就位 / 🔴 4 项**

### v2.3 下一步（顺序由 baseline-v2.2.md 第五节确定）

- [x] Trace 补齐 `plan` / `policy` / `human` 三个环节（`510adea`）
- [x] Memory 第一批：`Scope` / `Evidence` / 成功失败计数 / `Stale` / `Confidence`（`f5b10aa`）
- [x] 备份可还原性验证（`079fc2e`）：静态检查 + `restore_fail` 归类
- [ ] 环境符合性检查（依赖 Memory 的项目作用域已就位，可做）
- [ ] Plan + Re-plan（PolicyGate 已就位，前置齐了）
- [ ] `ai_loop.go` 拆分（与 Plan 改动同批）

**v2.3 验收标准**：能出示「经验增益」的 A/B 对照数据——有经验组与无经验组
在真实任务上的对比。做不到这一点，学习闭环无法证伪。

### v2.3 已交付的补充说明

**N1 Trace 三环节**：Human Trace 的判定刻意严格（同一 trace 上下文 +
动作同类 + AI 失败过 + 人工成功，四条全过才记）。误判一次会生成一条
持续影响后续所有任务的经验，**噪声代价远高于漏报**。明确不做跨任务关联。

**N2 Memory 第一批**：置信度只影响"怎么用"（是否提示、排序、标注），
**绝不影响"能不能做"**——PolicyGate 根本不读这些字段，已用
`TestConfidenceNeverElevatesPolicy` 钉死（30 次成功的高置信度状态下，
db_restore 仍 elevated、危险脚本仍 forbidden）。环境指纹刻意**不含 IP**：
NAT 模式下虚拟机 IP 每次都变，算进去会让全部经验失效，Stale 就成了噪声源。

第二批（Conflict / Correction / Retired）**刻意不做**：需要真实运行数据
判断机制是否有效，在没验证"经验到底有没有用"之前上复杂机制是拿未验证的
假设去构建。

**N3 备份可还原性**：v1.x 已有完整演练（`dbDrillTask` 四步），本项**没有重写它**
——补的是"静态检查"（每次备份后自动跑，能查出 --skip-triggers 导致触发器全丢
这类"文件完整但内容缺损"）与**归类分开**（`restore_fail` vs `verify_fail`：
前者要改备份命令，后者要重新备份，处置完全不同）。

---

## 附：文档维护约定

- 每个版本的"验收标准"实现前先定，实现后回填实测数据
- 现状盘点随代码变化更新，**结论必须能锚到源码行号或实测输出**
- 规划与现实冲突时，改规划，不迁就规划
- `PLAN-v2.0.md` / `PLAN-v2.1.md` 为阶段性细化稿；本文档是长期主线
