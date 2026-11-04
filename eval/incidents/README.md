# 事故语料库 · 离线回放门禁 · AI 影子判定

> 实验性设施，默认关、能证伪、坏了随时删。**不改 `ai_loop.go` / `ai_intent.go` /
> `aiExecTool` / `aiConfirmGate` 的任何一行**（变异验证时的临时改坏会精确复原）。

## 为什么要有它

11/4 连着两次「单测 / CDP 全绿，用户先在界面上撞到」：

| 事故 | 当时的验证 | 盲区 |
| --- | --- | --- |
| 丢 `docs`，模型收不到附件 | CDP 断言「传给 `aiConsume` 的参数」 | 把整个 `aiConsume` 桩掉了，丢字段的白名单重建**根本没执行** |
| 文档正文污染意图，弹「停止服务」确认卡 | 意图单测只喂短句；真实对话直接构造 API | 绕过了「拖文件 → 点发送 → 服务端 hydrate + 预检」 |

两次都是同一个毛病：**观测点不在真正出问题的那一层**。所以这里刻意：

1. 驱动 `handleAIChat` 而不是拼几条 `aiMsg` 直接调 `aiRunLoop` —— 请求体解析、文档落盘、
   `aiDocHydrate`、预检、主循环、SSE 全走一遍；
2. 断言不只看 SSE，还包一层 `aiUpstreamDo` 抓**真正发给上游的请求字节**。
   光看模型回答是空转：回答是录像里写死的，证明不了材料有没有被拼进去；
3. 上游用 `ENVKIT_AI_REPLAY` 完全离线回放，`BaseURL` 指向 `127.0.0.1:1` ——
   只要真走了网络，用例立刻失败。**零 token、零依赖、可重复**。

## 目录

```
eval/incidents/
  cases.json                 5 条回放场景（4 事故 + 1 负向对照）+ 各自的变异点
  shadow_cases.json          8 条影子评测集（人工标注 ground truth）+ 3 组变异
  recordings/*.jsonl         原始 SSE 录像，格式与 aiExchange 一致
  docs/*.txt                 文档正文夹具（04 是 80KB 分页夹具）
  gen_recordings.py          重生成录像（产物已固化，脚本仅供核对）
  gen_paging.py              重生成 80KB 分页夹具
tools/incident_gate.py        --check / --shadow / --mutate
ai_incident_gate_test.go     数据驱动的回放门禁（go test ./... 自动带上）
ai_shadow_intent.go          影子判定评估器（纯函数、默认关）
ai_shadow_intent_test.go     影子单测 + 报告出口
```

`{{DOC_ID}}` 是录像里的占位符：文档 id 由 `aiDocStore` 随机生成，runner 落盘后
替换到临时文件，再设 `ENVKIT_AI_REPLAY`。

## 三个入口

```bash
# 离线门禁（默认）：TestIncidentGate + TestShadow*，约 1.7 秒
python tools/incident_gate.py --check

# 影子判定报告 + 事实流（写到 .gotmp/，已忽略）
python tools/incident_gate.py --shadow

# 8 组变异：改坏必红、复原必绿
python tools/incident_gate.py --mutate
```

`go test ./...` 本身就会带上前两项，不需要额外步骤。脚本补的是变异门禁、
人读报告、以及统一的 Go 工具链环境。

## 加一条新事故

1. `docs/` 放文档正文（要能客观复现事故，别用随机数）；
2. `recordings/` 放录像。可以用真实上游 `ENVKIT_AI_RECORD` 录，也可以照
   `gen_recordings.py` 的写法造最小帧 —— 必须是**原始 SSE 字节**，不是结构化回答；
3. `cases.json` 加一条：
   - `user_text` / `doc` / `tools`（`watch` 只记录工具结果，`stub` 替换成固定返回）；
   - `asserts` 见下表；
   - **`mutations` 必填**，没有变异点的断言等于没有。
4. 跑 `--check` 必须绿，再跑 `--mutate` 确认改坏必红。

| 断言 kind | 含义 |
| --- | --- |
| `sse_present` / `sse_absent` | SSE 事件流里含/不含某子串 |
| `req_present` / `req_absent` | **真正发给上游的请求体**里含/不含 |
| `tool_called` | 指定工具真的被调用过 |
| `tool_result_contains` | 工具真实返回值里含（不是录像里写死的模型措辞） |
| `no_error` | 流里没有 `type:error` |

两个已踩过的坑，写死在 runner 里：

- **Go 必须用绝对路径** `C:\EnvKit\tools\go\bin\go.exe`。PATH 里那个系统 Go 是 1.23.3，
  `go.mod` 要求 1.26 —— 用错版本时「改坏=红」全是**编译失败的假红**。找不到管理版
  Go 时脚本直接报错退出，不静默 fallback。
- **「红了」还不够，得是断言红的**。实测踩过：写操作确认卡让 `handleAIChat` 原地等确认、
  不发上游请求，runner 一句 `Fatalf` 提前退出，看着变异必红，核心的
  `sse_absent confirm_request` 一次都没跑。现在观测点检查用 `Errorf`（不终止），
  `req_absent` 在空请求体时判失败，runner 还会检查变异输出里有没有「断言失败」。

## 影子判定：借范式、另起结构

`shadow.go` 已有成熟治理范式（走到 Execute **之前**停下、只记「若执行会做什么」、
只追加事实流）。但 `ShadowRecord` 全是恢复策略字段，硬塞 AI 意图会污染结构，
也会让 `shadowPassesOf` 的「连续 20 次」语义变得莫名其妙。所以：

- 独立结构 `AIShadowVerdict`、独立文件 `ai-shadow-<day>.jsonl`、不复用晋升计数；
- `stopped_before` 恒为 `execute`，`ENVKIT_AI_SHADOW` 不设时**一个字节都不写**；
- 反事实判定 `aiShadowNaiveIntent` 是事故前裸词规则的快照，刻意不复用
  现已被收口的 `aiMatchIntent`；
- `WouldBlock` 由复核器自己的规则算（候选动作在用户原文里能不能独立命中），
  `Correct` 由 `shadow_cases.json` 里**人工标注**的 `user_wants_tool` 裁决 ——
  两者来源独立，规则写反了 `Correct` 会变 false；`ground_truth_mismatch`
  再把「标注与实现不一致」显式报出来。

### 当前边界（别误读报告）

**本轮是纯离线实验**：`aiShadowObserve` 没有产品调用点，影子**没有接触真实用户流量**，
报告里的「误拦率 0%」是评测集上的数字，不是线上统计。要接线上需在 `aiRunLoop`
入口显式加一行并设置 `ENVKIT_AI_SHADOW` —— 属于「要不要让影子接触真实输入」的独立决策，
本轮没做。

## 与多 Agent 方案的关系

见 `docs/DESIGN-multi-agent.md`。11/4 的结论是：**当前不需要多 agent，程序内部
I/O 并行即可**。本目录是那个结论之后选的实验方向（2 → 1）：先把证据门槛从恢复策略
推广到 AI 侧，再谈别的。重新评估多 agent 的触发条件仍写在设计文档里。
