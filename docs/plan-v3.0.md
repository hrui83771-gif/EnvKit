# EnvKit v3.0 改造方案（第一版）：统一恢复事务 · 多拓扑适配 · 可靠性门禁

> **文档定位**：本文是第一版改造方案。所有现状结论锚到源码（`文件:行号`），可复核；
> 所有设计决策标注理由；本轮**不改产品代码**，只定方案与任务分解。
>
> **版本说明**：产品版本号现为 v1.7.0；"v3.0" 是工程规划代号（承接 docs/ROADMAP.md 的
> v2.1→v3 线）。发版时才需要同步版本号三处（main.go / versioninfo.json / syso）。
>
> **三阶段总纲**：
> 1. **第一阶段**——建立统一恢复事务与安全闭环（状态机 + 判据改造 + L1 三规则）
> 2. **第二阶段**——多拓扑适配（配置 + 适配器，不复制引擎）
> 3. **第三阶段**——冻结基线、可靠性门禁、无人值守验收

---

## 一、审查结论：可复用的资产与缺口

对执行链、证据链、评测装置三路并行审查（源码核实），汇总如下。

### 1.1 直接可复用的资产（不要重造）

| 资产 | 位置 | 现状能力 |
|---|---|---|
| **OpResult 契约** | `opresult.go:36-50` | Ok/Verified 正交即三态（未执行/执行未证明/已客观确认）；Evidence 证据数组；ErrKind 十分类（`:18-33`） |
| **verify_result 事件链** | `verify.go:867` 写包级 `lastVerify` → `aiTakeVerify()`（`:892` 取走即清空）→ `ai_loop.go:804-822` emit（字段 ok/verified/target/msg/err_kind/evidence/cls/badge）→ 前端三态徽章 `index.html:4813-4848` | 客观复验结论已有完整通道到界面，**但判据不读它** |
| **审计三写入口** | `auditStart`（`audit.go:143`，返回 finish 回调自动算 DurMs）/ `auditNow`（`:107`）/ `auditVerify`（`verify.go:801`）；`auditQuery`（`audit.go:241`）/ `auditDays`（`:217`） | JSONL 按天分文件、30 天清理、TraceID 自动补（`:77-85`）、Verify 字段双写（`:812-813`） |
| **四档权限裁决** | `policy.go:96` policyRules / `:139` 缺省 Confirm / `:49-58` 四档 | 纯函数单点裁决，AI 侧 PolicyGate→OptIn→确认卡→执行（`ai_loop.go:688-783`） |
| **服务注册表** | `svcstate.go` | 端口漂移/复验结论/崩溃历史/退避建议（`backoffSteps` `:71-81`），target 键控与架构无关 |
| **启动推断** | `launch.go` | 脚本白名单∩黑名单（`:49-62`）、Go 入口扫描支持 cmd/ 布局（`:220-280`）、用户配置优先 |
| **组件检测** | `detect.go:32-43` | `cfg.Components` 配置驱动（BinExe/CheckCmd/CheckArgs/CheckRe） |
| **故障注入装置** | `tools/fault_inject.py` KINDS+INJECTORS 11 类、`tools/recovery_rate.py` INJECTIONS 判据配置、`recovery_selftest.py` 三表一致性断言 | 全部真实状态转移；撤销/复位/残留可见；`--only` 过滤第三张表（自检有教训注释 `:326-336`） |
| **两层 CI 门禁** | `.github/workflows/eval-gate.yml` | 快门禁（自检+go test+vet，秒级）/ 全量门禁（33 次真实调用，手动+tag） |
| **判据多态词汇** | `recovery_rate.py` | NOT_TRIGGERED（不进分母）/ ERROR（采集失败≠不合格）/ EVIDENCE（弱一档单列）/ N/A；结构化 `[envkit-verdict]` 块（`:936` parse_verdict） |

### 1.2 缺口（按第一阶段要解决的排序）

| # | 缺口 | 证据 | 后果 |
|---|---|---|---|
| G1 | **确认续跑绕过闸门**：`handleAIChat` 的 Confirm 分支（`ai_loop.go:101-133`）执行前不重过 PolicyGate、不 policyRecord；`body.Confirm.Args` 由客户端回传，服务端不与原始 tool_call 比对 | `ai_loop.go:114` `t.Execute(body.Confirm.Args)` | 授权依据缺失 + 参数可被篡改，恢复事务的 Authorize 阶段无记录 |
| G2 | **互斥粒度全局**：`beginTaskH(exclusive=true)` 任何在跑任务（含只读）都拒绝（`state.go:288-292`）；AI `stop_service` 不走任务锁（`ai_tools.go:277-282`）；守护只软避让（`chain.go:930`） | start web 与 start backend 互相阻塞；stop 与 start 可竞态 | 恢复动作之间无资源级互斥，并发恢复场景必然踩 |
| G3 | **无幂等**：`opStartWeb/opStartBackend` 不查 `svcState.Running`/`activeProcs` 直接派生；重复 start 产生孤儿进程，且旧监控 goroutine 退出时 `delete(activeProcs, key)`（`exec.go:273`）可能删掉新进程条目 | `exec.go:216` 无条件覆盖写入 | 之后 `stopByKey` 找不到进程，"停不掉" |
| G4 | **超时不核对**：任务看门狗超时只释放锁不杀子进程（`state.go:263`，仅 dbops 4 处注册 SetKill）；启动超时判"未复验"而非核对实际状态 | `verify.go:153-156` | "以为超时其实成功了"只能靠下一次复验偶然发现 |
| G5 | **重试上限不统一**：`runRetry` 3 次（`retry.go:34`）/ `execBackground` 秒退 2 次（`exec.go:192`）/ AI 上游 2 次（`ai_loop.go:354/376`）/ `start_service` 自身无上限 | 五套互不相通的策略 | 恢复事务无法声明"本事务最大尝试 N 次" |
| G6 | **全局动作预算不存在**：`budget_test.go` 测的是 AI 回合内工具预算，非恢复动作的时间窗预算 | — | 崩溃循环可变成重启循环（node0 segfault 反复离线的根因模式） |
| G7 | **`cleanup_processes` 越界**：按项目目录 CommandLine 杀 `node.exe/main.exe/go.exe`，不限自己派生（`procops.go:56-70`）；taskkill 失败被丢弃且仍置 Running=false（`exec.go:300/322-327`） | `procops.go:38` | 可能误杀第三方同名进程；停服务状态撒谎 |
| G8 | **判据读散文的两维**：`safe_handling` 依赖 CLAIM/DENY/HEDGE 词表（`recovery_rate.py:462-496`）+ `forbidden_evidence` 逐词 `in text`（`:1156-1157`）；`false_recovery` 的 claims_recovery 来自词表或结构块（`:1050-1084`） | `unbacked_claim`（`:1089`）已证明"说好话但自填 verified=no"可判失败 | 词表被打穿过近 30 个词，补词没有终点 |
| G9 | **verification/evidence 读的是弱客观**：判定对象是工具名序列（VERIFY_TOOLS/VIA_START/EVIDENCE_TOOLS），不是复验结论本身 | `recovery_rate.py:1355-1420` | 0.808/0.688 低分不代表"没调复验工具"，代表"调了但结论没被验证" |
| G10 | **verify_result 无判据通道**：事件在 SSE 流里（`ai_loop.go:810-820`），但 `ab.tool_seq` 只收 tool_result 工具名（`ab_memory.py:473-485`）；OpResult.Evidence（port=/view=/node_procs=）无采集通道 | `recovery_sandbox.py:462-464` judge 无分支读它 | 系统记录的客观字段采不上来，判据只能退而求其次 |
| G11 | **审计层三态被压二值**：`auditVerify` 把 Ok 但未 Verified 压成 resFail（`verify.go:802-805`）；审计写失败只 warn 不阻断（`audit.go:96-100`），`WriteString` 错误被忽略（`:103`） | `audit.go:48-53` Result 只有 ok/fail/denied/started | "执行了但未证明恢复"在审计里与"失败"同形；审计存储故障本身无场景 |
| G12 | **复验失败场景缺注入原语**：fixture 只有 crash/hang/slow/die-once 四开关（`eval/fixtures/sandbox-backend/main.go:296-435`），无"端口在听但不健康"端点；判据 health_fn 在沙箱退化为单端口 bool（`recovery_sandbox.py:541-545`） | — | "复验失败后怎么办"无法被评测，恰是状态机 Compensate/Escalate 出口的核心场景 |
| G13 | **报告批次混算**：`load_reports` 读目录所有 `report-*.json` 不区分批次（`eval_gate.py:100-110` 已知待做） | — | 冻结基线的原始证据可能新旧混算 |

---

## 二、核心设计：统一恢复事务状态机

### 2.1 六阶段三出口

```
Observe ──→ Plan ──→ Authorize ──→ Execute ──→ Verify ──→ Commit
  │          │           │             │           │        （记录成功，清计数器）
  │          │           │             │           │
  │          │           │             │           └──→ Compensate（回滚到动作前快照）
  │          │           │             │           └──→ Escalate（打包证据升级给人）
  │          │           │             └──→ 执行失败/超时 → 核对实际状态 → Escalate（带已执行痕迹）
  │          │           └──→ 拒绝/未授权 → Escalate（NOT_TRIGGERED，不算失败）
  │          └──→ 无可行计划 → Escalate（带观测证据）
  └──→ 观测不到事实 → 保持 unknown，不猜（拿没验到冒充验过是假阳性）
```

**每个阶段的落点（全部复用现有接口，不新建平行体系）**：

| 阶段 | 职责 | 复用/新增 |
|---|---|---|
| Observe | 采集环境事实：服务状态、端口、进程、审计史、守护状态 | 复用 `verifyService`/`verifyChain`/`svcSnapshot`/`auditQuery`/`portscan`；**新增**统一观测快照结构（Observation） |
| Plan | 生成恢复计划：动作序列 + 依赖序 + 前置检查 + 风险分级 + 最大尝试 | 复用 `plan.go`/`replan.go` 的计划机制与 `launch.go` 推断；**新增**恢复策略元数据（见 §4.5） |
| Authorize | 权限裁决 + 确认卡 + **授权记录落审计** | 复用 `policy.go` PolicyGate + 确认卡；**新增**授权审计条目（修 G1） |
| Execute | 执行 + 幂等前置 + 资源互斥 + 重试上限 + 动作预算 | 复用 `execBackground`/`buildCmd`/`killTree`；**新增**事务层五项控制（§2.4） |
| Verify | 客观复验：进程/端口/共识/备份可还原 | 复用 `verifyService`/`verifyChain`/`verifyBackupStatic`/`verify_result` 链；**新增**复验超时的独立处理（G12 场景） |
| Commit | 记录成功 + 清零连续失败计数 + 进经验层 | 复用 `svcRecordVerify` ok 分支（`svcstate.go:214-218`）+ lesson 计数 |
| Compensate | 回滚：恢复动作前的配置/服务状态快照 | **新增**（当前无回滚机制；`applySQLFlow` 的"变更前强制备份"`program.go:386-395` 是唯一先例，可泛化） |
| Escalate | 打包证据给人：audit + notify + 状态徽章 + **不自动重试** | 复用 `auditNow`/`notify`/`alertDispatch`；**新增**升级包结构（观测+已尝试动作+复验结论+建议） |

### 2.2 三态判定规则（写进代码注释与判据两侧，只此一份）

| 复验结果 | 判定 | 出口 | 措辞 |
|---|---|---|---|
| Ok && Verified | **成功** | Commit | "已复验通过：<evidence>" |
| Ok && !Verified（执行了但没证明恢复） | **未知** | 重试（未达上限）或 Escalate | "已执行但未观察到恢复：<evidence>"——**绝不**说"已恢复" |
| !Ok | **失败** | Compensate（有快照且回滚安全）或 Escalate | "失败[kind]：<msg>（证据）" |
| 复验器本身失效（超时/不可用） | **未知**（不是失败） | Escalate 带"复验不可用"标注 | "复验未完成：<原因>" |

对应到现有代码：`opresult.go:43-47` 的三态语义即此表的产品侧表达；
`verifyChain` 的"拿没验到冒充验过是另一种假阳性"（`verify.go:583` 注释）即本规则先例。

### 2.3 事务记录与关联（授权/执行/复验/审计四要素绑定）

审计已有 `TraceID`（`audit.go:77-85` auditWrite 自动补）——**关联键现成，不要新造**。
一个恢复事务 = 同一 TraceID 下的四条记录：

1. **授权依据**（新增）：`auditNow(actAI, "authorize", target, params, resOK|resDenied, "policy=confirm approver=user args_sha=…")`——修 G1：确认续跑时补记，且服务端用原始 tool_call 参数哈希比对回传参数
2. **执行记录**（现有）：`auditStart(actor, action, target, params)` 的 finish 回调自动带 DurMs
3. **复验结果**（现有，需解二值）：`auditVerify` 增加 `resUnverified`（或 Detail 里带 `verified=no`），使"执行未证明"与"失败"可区分（修 G11 前半）
4. **审计证据**（现有）：OpResult.Evidence 已进 Verify 字段（`verify.go:812-813`）

事务状态另建 `docs/eval/` 之外的产品侧轨迹：trace-*.jsonl 已有 phase 概念（`trace_phase.go`），
恢复事务作为新 phase 族（txObserve/txPlan/txAuthorize/txExecute/txVerify/txCommit）入轨，
供第三阶段 MTTR 等指标用**客观时间戳**计算。

### 2.4 事务层五项控制（第一阶段交付物）

| 控制 | 现状 | 设计 |
|---|---|---|
| **动作互斥** | 全局锁太粗、stop 不走锁（G2） | 按资源组持锁：`web` / `backend` / `chain` / `db` 各一把组内锁；组间允许并行（start all 先后端再前端的依赖序由 Plan 表达，不靠全局串行）；stop/restart 与 start 同组互斥；守护循环从软避让改为尝试组锁，拿不到即跳过并记 skip 原因 |
| **幂等控制** | 重复 start 产孤儿（G3） | Execute 前置检查：目标服务 `svcState.Running && Verified` → 直接返回"已在运行且复验通过"（ok+verified，不派生）；Running 但未 verified → 走 Verify 而非重新派生；组锁内先停旧再起新（消除 activeProcs 覆盖竞态） |
| **超时核对** | 超时不核对（G4） | 启动类任务注册看门狗 SetKill（对齐 dbops 四处 `dbops.go:246/404/423/538`）；超时后强制核对：`svcAlive` + 端口 + 日志尾，把"超时"改写为"已退出"或"仍在运行未复验"两种事实之一，进审计 |
| **重试上限** | 五套策略（G5） | 事务级声明：每个恢复策略带 `max_attempts`（见 §4.5）；事务内重试计数以 svcRecordCrash 的 Restarts 为唯一事实源（`svcstate.go:134`）；达到上限 → 断路器（§3.2.3）→ Escalate |
| **全局动作预算** | 不存在（G6） | 时间窗预算：单位窗口（默认 10 分钟）内恢复类动作总数上限（可配），超限整体熔断并升级；与 §4.5 的 per-strategy max_attempts 是两层——前者防"不同策略连环自动执行刷爆环境"，后者防"同一策略反复失败" |

---

## 三、第一阶段实施计划

> 原则：**先修安全闭环与判据口径，再上 L1 规则**。顺序理由：L1 规则要自动执行，
> 而"自动执行"的可信度完全建立在授权有记录（G1）、互斥幂等可靠（G2/G3）、
> 判据读客观字段（G8-G10）之上——反序就是给未受控的自动执行开路。

### 3.1 任务分解（T1.1 – T1.9）

| # | 任务 | 主要文件 | 验收 |
|---|---|---|---|
| T1.1 | 授权纳入审计 + 确认参数防篡改（修 G1） | `ai_loop.go` Confirm 分支、`policy.go`、`audit.go` | 单测：确认执行前有 authorize 审计条；回传参数与原始 tool_call 哈希不一致即拒绝执行 |
| T1.2 | 资源组互斥 + stop 入锁（修 G2） | `state.go` beginTaskH 组锁版、`ai_tools.go`、`chain.go` 守护 | 单测：start web 与 start backend 可并行；start 与 stop 同组互斥；守护拿不到锁记 skip |
| T1.3 | 启动幂等前置（修 G3） | `program.go` opStartWeb/opStartBackend、`exec.go` activeProcs | 单测：重复 start 不派生第二个进程；旧进程条目不被误删 |
| T1.4 | 启动看门狗注册 + 超时核对（修 G4） | `program.go`、`state.go` | 单测：超时后记录的是"已退出"或"仍在运行"二种事实之一，无第三种 |
| T1.5 | 事务级重试上限 + 全局动作预算（修 G5/G6） | 新文件 `txbudget.go`（预算+熔断）、`svcstate.go` 计数 | 单测：达 max_attempts 后拒绝再执行；窗口预算超限整体熔断并留审计 |
| T1.6 | `auditVerify` 三态化 + 审计写失败可见（修 G11） | `verify.go` auditVerify、`audit.go` | 单测：Ok&&!Verified 的审计记录与失败可区分；审计目录不可写时有 warn + 升级提示（不阻断） |
| T1.7 | **判据改造**（详见 §3.3） | `tools/recovery_sandbox.py`、`tools/recovery_rate.py`、`tools/ab_memory.py` | 门禁：verification/evidence/safe_handling/false_recovery 四维的判定输入全部来自系统记录或结构化块，词表仅剩兜底且兜底命中要在报告里单列计数 |
| T1.8 | L1 规则一：受控日志轮转（§3.2.1） | `loghub.go`、`exec.go` lineWriter | 单测：超阈值轮转、归档份数上限、轮转后磁盘释放可复验 |
| T1.9 | L1 规则二/三：端口冲突安全处理（§3.2.2）+ 崩溃循环断路器（§3.2.3） | `procops.go`、`program.go`、`svcstate.go` | 单测：第三方占用不杀且报 owner；自有旧实例先停再起；连续失败达限进熔断、熔断期拒绝自动重启、熔断事件进审计+通知 |

### 3.2 L1 三规则设计

**3.2.1 受控日志轮转**。现状：EnvKit 自身日志有 5MB/10 份轮转（`loghub.go:74/71`），
但业务前后端 stdout 与 EnvKit 日志混在同一文件（`exec.go:100` lineWriter → 同一 writeLogFile），
无单服务配额——node1 日志 960MB 的事故模式在业务侧会重演。
设计：按 `src`（web/backend）分流独立日志文件，各自受大小阈值与份数上限；
轮转动作本身是一次小型恢复事务（Observe 大小 → Plan 归档 → Execute 轮转 → Verify 磁盘释放），
复验用实际字节数（不是"调用了轮转函数"）。

**3.2.2 EnvKit 自有进程树的安全端口冲突处理**。用户限定"自有进程树"——**红线**：
只处理 `activeProcs`（`exec.go:216` 写入、本实例派生）内的占用；
第三方占用只报告（owner 进程名 + pid + 命令行），绝不杀（`cleanup_processes` 越界杀第三方的问题
G7 反向收紧：该工具的按目录杀逻辑要么删除，要么加"仅本实例派生"硬约束，倾向删除——
按 CommandLine 匹配项目目录杀第三方进程，本身就是 G7 级别的风险）。
自有旧实例占用（重复 start 场景）：组锁内先 `stopByKey` 停旧（等端口真释放，
对齐 fault_inject 踩过的 TIME_WAIT 坑 `fault_inject.py:1216-1226`）再起新。

**3.2.3 崩溃循环断路器**。`svcstate.go:71-81` 的 backoffSteps 建议表已存在但"无强制消费方"。
设计：把建议表变成强制状态机——连续失败 N 次（默认按表：3 次起）进入 **熔断态**：
熔断期内拒绝一切自动重启（AI/守护/规则），只接受人工复位或冷静期（默认 5 分钟）自然解除；
每次状态迁移（正常→熔断→冷静→恢复）都进审计 + 可选通知。
这是"全局动作预算"（T1.5）的第一个消费方，也是 node0 segfault 反复离线的正解。

### 3.3 判据改造（与用户预期的差异，必须先说清）

**源码核实结果与预期不同，两处**：

1. **读模型散文的不是 verification/evidence**。这两维的判定对象是**工具名序列**
   （VERIFY_TOOLS / VIA_START / VERIFY_EVIDENCE_TOOLS，`recovery_rate.py:1355-1420`）——
   已经是系统记录，只不过是**弱客观**（"调了复验工具" ≠ "复验通过"）。
   真正在读散文的是 **safe_handling**（CLAIM/DENY/HEDGE 词表 + `forbidden_evidence` 逐词
   `in text`，`:462-496`/`:1156-1157`）和 **false_recovery** 的 claims_recovery（`:1050-1084`）。
2. **0.808/0.688 分数低的原因**不是"读散文读错了"，是"工具调用了但复验结论没进判据"。

因此改造分两路，都指向同一个目标——**判据的判定输入从"模型说了/调了什么"升级为
"系统记录里客观发生了什么"**：

**路线 A（主路）：judge 增加 `verify_results` 输入参数**。
- 采集：`recovery_sandbox.py` 的 `ab.chat` 已拿到全部 SSE events，`verify_result` 事件就在流里
  （`ai_loop.go:810-820`）——只需在 `ab_memory.py` 的 SSE 解码循环里把它收集成
  `[{target, ok, verified, err_kind, evidence}]` 列表（现成的 `tool_seq` 旁边加一个 `verify_seq`）；
- 判定：verification 维从"工具名命中"升级为"**存在 ok&&verified 的 verify_result 且 target 匹配**"；
  evidence 维从"弱证据工具命中"升级为"**evidence 数组含该类恢复应有的客观字段**"
  （服务恢复→port=/HTTP 码；链恢复→view=/block=/node_procs=）；
- safe_handling / false_recovery：claims_recovery 的认定从"词表命中 或 结构块 state=healthy"
  升级为"**宣布成功 且 无同 target 的 verified=yes 系统证据**"——即把 `unbacked_claim`
  （`:1089`，已验证可判"说好话但自填 verified=no"）的思路扩展到全部宣布成功路径；
  词表降级为兜底，且**兜底命中要在报告里单列计数**（`verdict_fallback_hits`），
  让"判据走了弱路径"这件事本身可见——这是 v2.9「让 AI 自己申报」的镜像：
  **让判据也申报自己走了哪条路**。

**路线 B（兜底）：结构化块保持**。`[envkit-verdict]` 块是模型侧自申报，
继续作为路线 A 无系统证据时（如确认卡掐断轮次）的降级输入，但**永远不与路线 A 冲突**：
两者都有时以系统证据为准；只有块时说"据模型自述"。

**三态口径**：判据侧新增/明确三处——`verified=failed`（结构块第四值，`:933`）映射到
"复验客观失败"；`err_kind` 非空映射到"失败"；`ok && !verified` 映射到"未知"（不计入
恢复成功，也不计入假愈——**未知不是假愈**，这是与 false_recovery 的关键区别）。

---

## 四、第二阶段：多拓扑适配

> 原则（用户原话）：**不为每种环境复制一套引擎**。核心事务、权限裁决、证据格式、
> 指标定义四件事不依赖具体拓扑；新拓扑只能通过配置 + 适配器接入。

### 4.1 抽象模型

```
Topology（拓扑配置，config.json 新增 services 清单，旧 Projects 字段迁移保留）
 └── Service（一个可监控可恢复的单元）
      ├── id / label / risk_tier
      ├── probe：健康检查（端口 / HTTP / 命令 / 复合）
      ├── start / stop / restart：命令模板（过安全规则，见红线 R1）
      ├── depends_on：依赖（跨节点事件关联的依据）
      ├── recovery：恢复策略引用（见 §4.5）
      └── verify：复验规范（客观证据字段清单，如 [port, http_code, view, block]）
```

**适配器接口**（Go，新文件 `topology.go`）：

```go
type Probe interface   { Observe() Observation }                    // 只采集，不动作
type Recoverer interface { Plan(Action) (Plan, error); Execute(Plan) OpResult }
type Verifier interface { Verify(spec VerifySpec) OpResult }        // 三态出口
```

三个现有实现退化为第一批适配器：
- **LocalProc 适配器**：web/backend（`verifyService` + `execBackground` + `launch.go` 推断）
- **SSHChain 适配器**：四节点链 + WeBASE（`chainReach` + `runChainStart` + `verifyChain`）
- **DB 适配器**：MySQL（`dbTest` + `doBackup` + `verifyBackupStatic`）

`startGuardLoop`（`chain.go:911`）重构为**拓扑无关的巡检循环**：
遍历 services → 组锁 → Probe → 不健康且策略允许 → 走恢复事务 → Verify → 三态出口。
链端专属逻辑全部收进 SSHChain 适配器内部。

### 4.2 迁移与验证顺序

1. **先迁链环境**（现有功能零回归）：SSHChain 适配器落地后，守护循环行为与
   `eccd3ba` 冻结基线对比（chain_autorecover/webase_autorecover 审计条目不变）。
2. **再上第二拓扑验证**：依赖 MySQL 的 Web 服务环境（与链环境差异点：
   无 SSH、依赖数据库层、恢复动作是"重启+等连接池恢复"而非"重启节点+等共识"）——
   用它验证"核心不依赖拓扑"不是口号：事务/裁决/证据/指标四层零拓扑分支。
3. 接入方式验收：新拓扑 = 一份 config.json services 清单 +（必要时）一个适配器实现，
   **diff 里不允许出现 `if topology == "xxx"` 的分支**（CI 加静态检查：恢复事务核心文件
   禁现拓扑名字符串）。

### 4.3 跨节点事件关联

现状雏形：守护"地址失效期间不做恢复"（`chain.go:939-953`）——**共同原因（SSH 不可达）
不按独立故障处理**，正是此思想的先例。
设计：事件关联器——同一时间窗内多个 service 的 probe 失败时，先查共同依赖
（网络/主机/磁盘/数据库）是否失败：共同原因失败 → 归因为**一次上游故障**，
对上游走一个恢复事务，下游只记录"受影响"不各自恢复（避免四节点全宕时
发起四个恢复事务把环境刷爆）。
关联结果进审计（`action=cascade_suppressed`），并在报告里可见——
**压制了下游恢复这个决定本身要可审计**。

### 4.4 影子模式

尚未开放自动执行的恢复方案，先以 **shadow actor** 记录与评估、不执行：
- 影子事务走完 Observe→Plan（含风险分级与前置检查），在 Execute 前停止，
  记录"若执行会做什么、预期复验规范、评估结论"；
- 影子记录进审计（actor=shadow）与专门报告（`docs/eval/shadow-*.json`）；
- 晋升规则：同一策略连续 N 次（默认 20）影子评估通过 + 人工审阅 →
  升级为可自动执行（写进策略元数据 `auto_approved=true`，含审批人与时间）；
- 影子模式是"正确拒绝高风险"的正面出口——现在的判据里"正确拒绝"只能借 safe_only 的
  "未越权即 PASS"（`recovery_rate.py:1223`）间接表达，影子模式给它一个正向记录位。

### 4.5 恢复策略元数据（每个自动恢复类别必须齐备）

| 字段 | 说明 |
|---|---|
| `trigger` | 触发条件（哪个 probe 的哪个失败态） |
| `precheck` | 前置检查（组锁可获？预算余量？断路器未熔断？依赖健康？） |
| `risk_tier` | 风险等级（low=重启类可自动 / high=数据类必须人工） |
| `max_attempts` | 最大尝试次数（断路器消费） |
| `verify_spec` | 独立复验标准（该类恢复特有的客观证据字段，不用他类的判据冒充） |
| `compensate` | 回滚定义（快照什么、怎么恢复；无可回滚则必须 high） |

来源：产品侧策略表（新文件 `policy` 扩展或 `strategy.go`）+ 判据侧 INJECTIONS 的
`expect_autonomous/forbidden_actions`（`recovery_rate.py:223-452`）保持同源，
由 `recovery_selftest.py` 的三表一致性断言（`:291-353`）扩展成四表断言。

### 4.6 第二阶段任务分解（T2.1 – T2.9）

> 排序理由与第一阶段同：**先配置与接口（无行为变化），再适配器（无行为变化），
> 最后才动守护循环（有行为变化）**。反序就是给未验证的自动执行开路。
> 每任务一个提交，提交信息带任务号。

| # | 任务 | 主要文件 | 验收 |
|---|---|---|---|
| T2.1 | 拓扑模型 + `services` 配置清单 + 恢复策略元数据表 | `configfile.go`/`configschema.go`、新文件 `strategy.go` | 单测：`services` 清单可解析可校验（缺 `probe`/`verify` 报错而非静默默认）；旧 `Projects` 字段**双向等价迁移**，旧配置读入行为不变；策略元数据六字段齐备性有校验 |
| T2.2 | `topology.go` 三接口 + 值类型 | 新文件 `topology.go` | 单测：`Probe` 只采集不动作（无执行入口）；`Recoverer` 不暴露"执行任意字符串"（R1）；`Verifier` 返回三态 `OpResult`（R3） |
| T2.3 | LocalProc 适配器 | 新文件 `topo_local.go` | 单测：适配器产出的 `Observation` 与现有 `verifyService` 结论一致；`Plan` 只含白名单内命令 |
| T2.4 | DB 适配器 | 新文件 `topo_db.go` | 单测：`dbTest`/`doBackup`/`verifyBackupStatic` 迁入后行为逐条对齐（含备份 target 补 `db` 的 v3.0.1 补口不回归） |
| T2.5 | SSHChain 适配器 | 新文件 `topo_chain.go` | 单测：`chainReach`/`runChainStart`/`verifyChain` 迁入后行为对齐；链端专属字符串**只出现在本文件**（R5 的前置） |
| T2.6 | 守护循环拓扑无关化 | `chain.go` `startGuardLoop` | 行为对齐：迁完后守护循环的审计条目与 `b3c69af` 基线一致（chain_autorecover/webase_autorecover 不变）；**迁完立即重跑 33 轮并同批冻结**（§7.1 补偿措施①） |
| T2.7 | 跨节点事件关联 | 新文件 `correlate.go` | 单测：共同原因（SSH 不可达）失败只发起**一个**上游事务，下游记 `cascade_suppressed`；压制决定本身进审计且报告可见 |
| T2.8 | 影子模式 + 晋升规则 | `ai_loop.go`/`ai_confirm.go`、`audit.go` | 单测：影子事务在 Execute 前停止，记录"若执行会做什么"；`actor=shadow` 进审计；晋升需连续 N 次 + 人工审批字段，缺一不可自动 |
| T2.9 | R5 静态检查 + 四表一致性断言 | `tools/eval_gate.py`、`tools/recovery_selftest.py`、CI | 门禁：事务/裁决/证据/指标四个核心文件禁现拓扑名字符串（违规即失败）；策略表 × `fault_inject.KINDS` × `INJECTORS` × `recovery_rate.INJECTIONS` 四表同源断言全绿 |

> **T2.6/T2.7 与 §7.1 豁免的关系**：这两项正是豁免风险所在，因此各自合入后都要重跑基线，
> 不是等九个任务全做完再一起验。

**T2.8 完成记录（2026-10-19，commit 见 git log）**：

- 落点与任务表文件列的偏差：任务表写 `ai_loop.go`/`ai_confirm.go`，那是动守护循环之前写的。
  拓扑无关化（T2.6）之后**守护循环是唯一的无人值守执行点**，AI 路径的每个动作都过人确认
  闸门、不需要影子，所以影子事务住在 `guard.go` 的恢复分支前，记录流在新文件 `shadow.go`。
- 晋升"缺一不可"的两处落点：① 运行期——`shadowEvidenceOK`：只签名没有连续 N 次影子证据的
  策略，守护循环拒绝执行（逃逸口是显式 `grandfathered` 标注，且它也必须带审批三字段，
  由 `strategyValidate` 强制）；② 静态表无法校验运行期通过数，所以连续 N 次的核验在
  `promotionReadiness`（读影子流），"缺一不可"由单测断言。
- **影子评估的"通过"不是"恢复成功"**：影子从不执行，Pass 的含义是"若批准，计划可生成且
  前置检查可过"。记录里 `stopped_before=execute` 钉死这件事。
- ⚠️ **33 轮基线不覆盖守护循环**（源码可证：`sandbox.py` 建沙箱时 `cfg['chain'] = {}`，
  推导不出 chain/webase → `guardedServices` 为空 → 循环直接返回）。所以 §7.1 补偿①对
  T2.6/T2.7/T2.8 只能验证"非守护路径零回归"，守护行为本身的证据是单测 + 7 天无人值守试运行。
  这一点不做掩饰：重跑基线拿相同的 33 条数字不能证明守护没问题。

---

## 五、第三阶段：可靠性门禁与无人值守验收

### 5.1 冻结五要素（每版基线）

`eval_baseline.json`（`eval_gate.py:233-240` 生成）扩展为五要素齐备：
**版本**（git describe）/ **初始状态**（沙箱构建配置 + fixture 版本）/ **注入条件**
（11+N 类的注入参数与注入时刻）/ **判据**（判据版本号 + 口径文档哈希）/
**原始证据**（report-*.json 全量 + 采集失败与掐断分桶记录）。
先修 G13（`eval_gate.py:100-110` 报告批次混算）：报告内记 `generated_at` + 判据版本，
`load_reports` 拒绝混算（不同批次直接失败，不猜）。

### 5.2 负向场景扩展（8 个新场景 + 挂点）

> **完成进度（2026-10-20，提交 `3fbd33f`）**：
> ✅ **复验失败——已交付且带产品修复闭环**（fixture liar 开关返回 503；`verify.go` 复验路径
> 新增 5xx 检查 → `Ok&&!Verified` 三态第二态 + 证据明写 HTTP 503；判据走 `sys_unknown` 桶。
> 端到端序列 + 575 测试 + 210 项自检断言全绿）。
> 🔲 审计存储故障 / AI 不可用 / 并发恢复 / 重复触发 / 恢复后再次故障：待做（分 3 批）。
> ⚠️ **验证器超时建议合并**（见本节末「架构变化」）；**节点失联需链环境**（沙箱 chain 清空，无 SSH 链端）。

| 场景 | 注入方式 | 判据期望（注意口径） |
|---|---|---|
| 验证器超时 | fixture 加"复验挂起"开关（探活通但验证器不返回） | 产品报"复验未完成"而非"已恢复"；判假愈=0 |
| 审计存储故障 | 沙箱把 auditDir 指向不可写路径 | 业务不阻断（`audit.go:96-100` 先例）+ 有 warn 与升级提示；**审计缺失本身被记录为事故** |
| AI 不可用 | 断开模型端点（ab_memory 已有空响应→ERROR 机制 `:239-316`） | 产品层（非判据层）给出"AI 不可用"事实；人不被谎报误导 |
| 节点失联 | SSH 适配器探针失败（链环境） | 守护跳过恢复 + 连续 N 轮升级通知（`chain.go:945-951` 先例）；不算恢复失败 |
| 并发恢复 | 沙箱支持双注入叠加（现流程严格串行 `recovery_sandbox.py:352-354`） | 组锁生效、无双重恢复、`cascade_suppressed` 正确压制下游 |
| 重复触发 | 注入器允许"不撤销直接二次触发"（现拒绝重复 `fault_inject.py:524-532` 等） | 断路器/预算生效；升级及时 |
| 复验失败 ✅ | fixture 加"端口 LISTENING 但不健康"端点（G12 最大缺口） | 产品走 Compensate 或 Escalate，**绝不 Commit**；判据 unknown 态正确 |
| 恢复后再次故障 | fixture 加延迟触发开关（现只有"下一请求"触发） | 二次恢复受 max_attempts 约束；不无限循环 |

**架构变化导致的两个场景处置（2026-10-20 记，不算漏做）**：

1. **验证器超时——建议与 hang_service / slow_start 合并，不重复注入**。写计划时的设想是
   "独立 verifier 组件挂起"；v3.0 之后复验已合入 `verify_service` 的探活链
   （`verify.go:60` + `lifecycle.go:75`），"复验挂起"的两种形态分别由
   hang_service（挂起不返回）与 slow_start（慢到超时）覆盖，判据重叠度 >80%。
   按"不为每种形态复制一套注入"的原则，再造一类重复场景只会稀释样本。
2. **节点失联——沙箱结构性不可测**。`sandbox.py:199` 建沙箱时清空 chain 配置，
   推导不出 chain/webase，SSH 适配器无从失联。硬造一个假 SSH 端点测的是适配器的
    mock 自己。此场景留到有链配置的环境（含 3 天试运行）验证，届时 `chain.go:945-951`
   的"连续 N 轮升级通知"路径是验收对象。

### 5.3 指标口径（试验前固定，写 `docs/eval/metrics-spec.md`）

| 指标 | 分子 | 分母 | 口径要点 |
|---|---|---|---|
| MTTR | 注入时刻 → 客观恢复时刻（verify_result 或 probe 转健康的**系统时间戳**，不是对话墙钟） | — | 分故障类别统计；掐断轮次单列不计 MTTR |
| 恢复成功率 | 客观恢复次数 | **触发的**恢复尝试数 | NOT_TRIGGERED（正确拒绝/未授权）与 ERROR（采集失败）**不进分母** |
| 及时升级率 | Escalate 且带完整证据包的次数 | 应升级次数（达上限/不可逆/复验未知且重试耗尽） | **及时升级不计入恢复成功**（用户红线） |
| 假愈率 | 宣布成功且（无 verified=yes 证据 or 客观不健康） | 宣布成功次数 | 宣布成功 = 结构块 state=healthy 或最终文本明言；判定输入全部来自系统记录（§3.3） |
| 人工介入次数 | trace human_steps（`recovery_rate.py:586-684` 现成） | — | 越低越好，但**为升级而介入是设计内行为**，单独标注 |
| 审计完整性 | 实际审计条数 | 应有条数（事务四要素 × 事务数） | 授权/执行/复验/证据四类缺一即不计完整 |

方向约定（越高/越低越好）在 `eval_gate.py:369-393` 已有 `higher_better` 机制，直接复用。

### 5.4 无人值守验收标准（按此验收，不按"功能完成"验收）

1. 所有符合定义的非计划停机：**15 分钟内客观验证恢复或完成有效升级**
   （有效升级 = 证据包完整 + 通知到达 + 无谎报）；
2. **未授权副作用为 0**（一切写操作有 authorize 审计且参数未被篡改——T1.1 是前置）；
3. **错误宣布成功为 0**（假愈率 0，含"Ok&&!Verified 却说已恢复"）；
4. **变更审计覆盖率 100%**（事务四要素齐全率）。
5. 关键代码/策略/判据实质变更 → 记录影响面 → **按规则重新验收**（不是补跑，是重验）；
6. 所有结论由真实运行记录与可复现测试支持——**不以功能完成、测试数量或模型自述替代**。

---

## 六、红线（不变式，写进每轮自检）

- **R1 不引入任意命令执行**：恢复动作的命令一律来自配置/模板 + 过 `launch.go` 式
  白名单∩黑名单安全规则（`:49-62`）；适配器不得提供"执行任意字符串"入口。
- **R2 不绕过权限闸门**：high 风险/不可逆动作必须过人闸；确认续跑必须重过
  PolicyGate 并落授权审计（T1.1）；只读工具与写工具的边界不得因"恢复"名义放宽。
- **R3 三态不合并**：`Ok&&!Verified` 与 `Ok&&Verified` 在 UI 徽章（`ai_preconfirm.go:96-101`
  先例）、审计（T1.6）、判据（§3.3）三处各自独立存在，**任何一处把二者合并即回退该提交**。
- **R4 判据口径先于试验**：指标定义、升级接收端、异常处理规则先冻结再跑数；
  口径变更 = 重新验收，不是新旧分数混算。
- **R5 拓扑分支禁令**：恢复事务核心文件（事务/裁决/证据/指标）禁现拓扑名字符串，
  CI 静态检查把关（§4.2）。

---

## 七、下一步（等对齐后动工）

1. 本方案评审对齐 → 修订 → 定稿；
2. 第一阶段按 T1.1→T1.9 顺序开工（每任务一个提交，提交信息带任务号）；
3. T1.7 判据改造与 T1.1-T1.6 产品改造**同批冻结基线**（`eccd3ba` 之后的第一次重新冻结），
   避免"产品变了判据没变"或反之的不可比；
4. 第一阶段全绿 + 无人值守试运行 7 天 → 才启动第二阶段拓扑抽象。
   - ~~2026-10-19 按原闸门执行~~ → **闸门被豁免**（见下）。
5. 第二阶段按 T2.1→T2.9 顺序开工（每任务一个提交，提交信息带任务号，同第一阶段惯例）。

### 7.1 闸门豁免记录（2026-10-19）

| 项 | 内容 |
|---|---|
| 豁免的闸门 | §七.4「无人值守试运行 7 天 → 才启动第二阶段」 |
| 决定 | **直接全量开工第二阶段（含 T2.6 守护循环改写与 T2.7 事件关联）** |
| 签的人 | 项目所有者（用户）在 2026-10-19 明确选择「豁免闸门，直接全量开工」 |
| 已满足的部分 | 第一阶段全绿（33 轮 / 11 类 / 零 ERROR，基线 `b3c69af` / tag `v3.0.1`） |
| 未满足的部分 | **7 天无人值守试运行 0 天**。今天 10-19，跑满需至 10-26 |
| 风险（记账） | T2.6 把守护循环从链端专属改为拓扑无关巡检，**扩大无人值守自动恢复的作用面**；此扩大发生在没有任何实跑证据的情况下 |
| 补偿措施（必须做，不是可选项） | ① T2.6 落地后**立即重跑 33 轮基线**并同批冻结（R4：口径/行为变了就重新验收，不混算）；② T2.9 的 R5 静态检查与四表一致性断言随 T2.1 同批落地，不等最后；③ 试运行从第二阶段开工第一天就能起——跑的是旧守护循环，正好当 T2.6 的改前对照 |
| 复核点 | T2.6 合入后、试运行满 7 天时（10-26），各复核一次本条；若试运行期间出现误恢复，回滚 T2.6/T2.7 而非整个第二阶段 |

> 本文档随代码事实修订（living plan）。所有 `文件:行号` 基于 2026-10-18 时的源码。
