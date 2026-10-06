package main

// ai_loop.go —— AI 对话主循环：流式转发 + 工具调用调度
//
// 读工具自动执行；写工具转确认流程（本轮结束，用户确认后带 confirm 续接）。
//
// ## 为什么只剩这些
//
// 原文件 1130 行、7 种职责混在一起，每次改主循环都要在 1130 行里定位。
// 按职责边界拆成：
//
//	ai_sse.go       SSE 写入 / 连接就绪 / 收尾 / 客户端断开检测
//	ai_intent.go    意图判定（确定性匹配，模型失灵时仍能工作）
//	ai_history.go   历史裁剪 / 滚动摘要 / 工具结果转消息
//
// **纯搬迁**：没改任何函数体、没改函数名、没改调用点。
// 拆分的判据是职责边界而不是行数——ai_intent.go 里的关键词表
// 以后还会长，它与主循环本就不该在同一个文件里互相干扰。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func handleAIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	fl, ok := aiChatReady(w)
	if !ok {
		return
	}
	usage := aiUsage{} // 空请求错误路径不会产生用量，这里只为复用统一的收尾 helper
	var body aiChatReq
	decErr := json.NewDecoder(r.Body).Decode(&body)
	msgs := body.Messages

	// v2.2 M9：一次 AI 对话 = 一条任务轨迹。此后本次会话内的所有审计记录
	// 自动带上 trace_id，"AI 是不是在瞎试"才成为可统计的问题。
	//
	// v2.7 修正：原先这里是 `traceOutcome := "aborted"` 且**从未被赋值**——
	// 结局由 aiRunLoop 通过 traceSetOutcome 写进 traceOutcomeBox，
	// endTrace 收尾时优先取 box 里的判定，这里的 outcome 只作兜底。
	// 实测该死变量导致 305 条轨迹 outcome 100% 是 aborted，
	// 任何依赖结局的统计（成功率/恢复率/失败率）恒等于同一个值。
	traceID := beginTrace(aiLastUserText(msgs), actAI)
	defer endTrace(outAborted)
	_ = traceID

	// 实测：某些本机代理/预览容器会剥掉 POST body（服务端表现为 decode 出 0 条消息，
	// 模型因此对着"空输入+环境状态"自说自话，用户以为 AI 不听话）。鉴权令牌走请求头
	// 能完好穿过，所以前端把"最后一条用户消息 + 确认回执"也放进 X-EnvKit-Chat-Fallback
	// 头兜底；body 丢失时据此恢复，保证对话和确认流程在代理环境下依然可用。
	if (decErr != nil || len(msgs) == 0) && body.Confirm == nil {
		if fbRaw, ferr := url.QueryUnescape(r.Header.Get("X-EnvKit-Chat-Fallback")); ferr == nil && fbRaw != "" {
			var fb struct {
				Last    string        `json:"last"`
				Confirm *aiConfirmReq `json:"confirm"`
				Lang    string        `json:"lang"`
			}
			if json.Unmarshal([]byte(fbRaw), &fb) == nil {
				body.Confirm = fb.Confirm
				if body.Lang == "" {
					body.Lang = fb.Lang
				}
				if len(msgs) == 0 && fb.Last != "" {
					msgs = []aiMsg{{Role: "user", Content: fb.Last}}
					warn(scSys, "AI", "请求体未到达（被本机代理/预览容器拦截），已用回退头恢复对话")
				}
			}
		}
	}
	// 仍然拿不到任何用户内容：显式报错，绝不静默地把空输入丢给模型空转
	if len(msgs) == 0 && body.Confirm == nil {
		sseWrite(w, fl, map[string]any{"type": "error", "text": "AI 收到空请求：请求体未到达服务端" +
			"（通常是被本机代理或嵌入预览容器拦截）。请用系统浏览器直接打开控制台地址后重试。"})
		sseDoneUsage(w, fl, &usage)
		return
	}
	// 悬挂的 assistant(tool_calls)（用户无视确认卡片直接发新消息、或确认续接的历史回传）
	// 会让上游 OpenAI 协议报 400，统一剥掉（协议里工具结果一律走 aiToolResultAsUser）。
	msgs = aiTrimDanglingToolCalls(msgs)
	// 滚动摘要：历史变长后把较早的部分压缩成要点备忘（按前缀缓存，不每回合重复生成），
	// 必须在下面的 40 条硬裁剪之前做——否则最早的用户目标会被无感截断，模型显得健忘。
	msgs = aiMaybeSummarize(msgs)
	if len(msgs) > 40 {
		msgs = msgs[len(msgs)-40:]
	}
	// 确认回执：执行（或拒绝）写工具，然后继续对话
	result := ""
	confirmed := false
	if body.Confirm != nil {
		confirmed = true
		t, exists := aiToolRegistry[body.Confirm.Tool]
		switch {
		case !exists:
			result = "错误：该工具不在白名单内"
		case !body.Confirm.Approved:
			result = "用户拒绝了该操作。请给出替代的手动操作步骤。"
		default:
			// 执行前先给可见反馈：go build / npm install / mysqldump 可能耗时 1~2 分钟，
			// 期间一条输出都没有，用户会以为"点了确认没反应"而重复点击。
			sseWrite(w, fl, map[string]any{"type": "delta", "text": "正在执行：" +
				aiToolCnName(body.Confirm.Tool, body.Confirm.Args) + "…（可能需要几十秒到几分钟，请稍候）\n"})
			if res, err := t.Execute(body.Confirm.Args); err != nil {
				result = "执行出错：" + err.Error()
			} else {
				result = res
			}
		}
		// 不合成 assistant(tool_calls)+tool（thinking 模型要求回传 reasoning_content，合成消息会 400），
		// 改为剥掉该回合并用一条 user 消息把结果交给模型。
		msgs = aiTrimLastToolTurn(msgs)
		msgs = append(msgs, aiToolResultAsUser(body.Confirm.Tool, firstLines(result, 3000)))
		sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": body.Confirm.Tool, "result": firstLines(result, 300)})
		// 确定性结果行：不让"到底成没成"取决于模型怎么描述
		name := aiToolCnName(body.Confirm.Tool, body.Confirm.Args)
		if body.Confirm.Approved {
			res := firstLines(strings.TrimSpace(result), 160)
			sseWrite(w, fl, map[string]any{"type": "delta", "text": "✓ 已执行：" + name + " —— " + res + "\n\n"})
		} else {
			sseWrite(w, fl, map[string]any{"type": "delta", "text": "✗ 已取消：" + name + "\n\n"})
		}
	}
	tw := &errTrackWriter{ResponseWriter: w}
	aiRunLoop(r.Context(), tw, fl, msgs, confirmed, true, body.Lang)
}

// ---------- 诊断解读入口 ----------

func handleAIExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body aiChatReq // 只为了拿界面语言（lang）；messages 由服务端用诊断报告生成
	_ = json.NewDecoder(r.Body).Decode(&body)
	fl, ok := aiChatReady(w)
	if !ok {
		return
	}
	report := buildDiagReport()
	msgs := []aiMsg{{Role: "user", Content: "请解读以下 EnvKit 诊断报告：先给出现状总结（2-3 句），再列出发现的问题与建议步骤。\n<untrusted_data>\n" + report + "\n</untrusted_data>"}}
	tw := &errTrackWriter{ResponseWriter: w}
	aiRunLoop(r.Context(), tw, fl, msgs, false, false, body.Lang)
}

// errTrackWriter 包装 ResponseWriter，记录写出错（客户端关闭页面后 SSE 写入会失败）。
// Go 的 HTTP server 在"只写不读"的长连接上不会主动感知对端断开，必须靠写失败来判断。
type errTrackWriter struct {
	http.ResponseWriter
	err error
}

func (e *errTrackWriter) Write(b []byte) (int, error) {
	n, err := e.ResponseWriter.Write(b)
	if err != nil {
		e.err = err
	}
	return n, err
}

func (e *errTrackWriter) Flush() {
	if f, ok := e.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (e *errTrackWriter) writeErr() error { return e.err }

// Unwrap 让 http.ResponseController 能找到底层 writer 的 FlushError，从而拿到真实的写失败
func (e *errTrackWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }

// ---------- 对话循环 ----------

// 上游流式响应的工具调用累积器
type aiAccTool struct {
	ID    string
	Name  string
	ArgsB strings.Builder
}

// aiActionFamilies 动作主题分类：统计一条回复涉及几个不同动作主题，
// 用于区分"单一动作的提议/汇报"与"多选项菜单"（见 aiLooksLikeMenu）。
var aiActionFamilies = []struct {
	key  string
	pats []string
}{
	{"start", []string{"启动", "起服务", "拉起", "跑起来"}},
	{"stop", []string{"停止", "停掉", "关掉", "关闭"}},
	{"restart", []string{"重启", "重新启动", "重新起"}},
	{"backup", []string{"备份", "导出数据库", "dump"}},
	{"whitelist", []string{"白名单", "杀软", "defender"}},
	{"logs", []string{"日志"}},
	{"detect", []string{"检测"}},
	{"diag", []string{"诊断", "体检"}},
	{"cleanup", []string{"清理进程", "清理后台"}},
}

// ---------- 滚动摘要（上下文智能化） ----------
// 痛点：客户端每回合回传全部历史，此前只做"留最近 40 条"的硬裁剪——最早的用户目标、
// 已确认的关键操作会被无感截断，长对话里模型显得健忘、反复确认。
// 方案：历史超过 aiSummarizeAfter 条时，把较早部分压缩成一条要点备忘放在开头，
// 最近 aiSummarizeKeep 条原样保留。摘要结果按"前缀指纹"缓存 + 边界按 aiSumQuantum
// 条量化，保证摘要调用频率被压到约每 3 个用户回合一次，而不是每条消息都多打一次上游。

const (
	aiSummarizeAfter = 24 // 历史超过该条数才启用摘要
	aiSummarizeKeep  = 12 // 摘要后最近原样保留的条数
	aiSumQuantum     = 6  // 摘要边界量化粒度（条）
)

var (
	aiSumMu    sync.Mutex
	aiSumCache struct {
		key  string
		text string
	}
)

// aiTurnTimeout 单轮上游最长等待：共享 client（httpStream）不设全局超时，
// 由这里的计时器兜底，避免上游假死时永久挂住（此前是靠 client 的 120s 超时）。
const aiTurnTimeout = 6 * time.Minute

// aiSleepCtx 休眠但可被客户端断开打断（避免退出时后台空转）
func aiSleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// aiUsage 一次回合的 token 用量（多轮工具调用累加）。HasCache=上游是否报了缓存细分
// （DeepSeek 专有字段）；自定义模型通常只有总 token 甚至没有 usage。
type aiUsage struct {
	Prompt     int64 `json:"prompt_tokens"`
	Completion int64 `json:"completion_tokens"`
	HasCache   bool  `json:"has_cache"`
	Hit        int64 `json:"cache_hit"`
	Miss       int64 `json:"cache_miss"`
}

func aiRunLoop(ctx context.Context, w http.ResponseWriter, fl http.Flusher, msgs []aiMsg, confirmed bool, preflight bool, lang string) {
	usage := aiUsage{} // 本回合 token 用量累计（跨多轮工具调用）
	// 预检快通道：不依赖模型，确定性识别明确指令并直接执行（写操作仍走确认卡片）。
	// 这样即使模型"只会念菜单"，用户的明确需求也一定会被执行。
	preChecked := false
	menuRetried := 0
	optInBlocked := 0 // opt-in 工具拦截计数：同一轮 ≥3 次判定模型死循环，终止并给可见提示
	paramFix := 0     // 参数自适应重试次数（temperature / token 字段 / tools 各最多一次）
	netRetry := 0     // 瞬时故障重试次数（网络错误 / 429 限流 / 5xx，指数退避 1s→3s）
	if !confirmed && preflight {
		lastUser := ""
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				lastUser = strings.TrimSpace(msgs[i].Content)
				break
			}
		}
		cand := lastUser
		if n := aiResolveNumberInput(lastUser, msgs); n != "" {
			cand = n
		}
		if tool, args, isWrite := aiBacktrackIntent(cand, msgs); tool != "" {
			preChecked = true
			info(scSys, "AI", "预检命中：%q \u2192 %s（写操作=%v）", firstLines(lastUser, 30), tool, isWrite)
			callID := fmt.Sprintf("intent_%d", time.Now().UnixNano())
			if isWrite {
				// 先给一句文字反馈，避免用户只看到卡片以为没反应
				sseWrite(w, fl, map[string]any{"type": "delta", "text": "好的，准备执行：" + aiToolCnName(tool, args) + "。请在下方点「确认执行」。\n"})
				sseWrite(w, fl, map[string]any{"type": "confirm_request", "tool_call_id": callID, "tool": tool, "args": args,
					"reason": "intent", "text": aiToolCnName(tool, args)})
				sseDoneConfirm(w, fl, &usage)
				return
			}
			// 读工具：直接执行，结果交给模型做自然语言总结
			res, exErr := aiToolRegistry[tool].Execute(args)
			resText := res
			if exErr != nil {
				resText = "执行出错：" + exErr.Error()
			}
			sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": tool, "result": firstLines(resText, 300)})
			// 用 user 消息承载工具结果（避免合成 assistant+tool 触发 thinking 模型 400）
			msgs = append(msgs, aiToolResultAsUser(tool, firstLines(resText, 3000)))
		}
	}
	// v2.3 N5：回合上限按任务性质分级，不再一刀切 8。
	// 但**文本分档只是起点，不是判据**——用户问「区块链的定义，以及在本项目
	// 上链了什么数据」这类不含任何关键词的问题会被误判成 4 轮。
	// 治理器会在运行中按"每一轮是否都换来了新信息"提额，见 plan.go。
	budget := budgetFor(aiTaskHint(msgs))
	gov := newBudgetGovernor(budget)
	plan := &planTrack{}
	rp := newReplan()
	// v2.7：本回合是否出现过工具失败。结局判定要用它——
	// 「答完了」与「做成了」不是一回事，中间差的就是这个。
	hasToolFailure := false
	sseWrite(w, fl, map[string]any{"type": "plan_start",
		"max_turns": gov.max, "reason": gov.base.Reason})
	for turn := 0; turn < gov.max; turn++ {
		// 预算即将耗尽时**提前告知模型**，让它有机会收敛成一个完整回答。
		//
		// 为什么必须提前说：若等到真的耗尽才说，模型的第一反应是"停"，
		// 用户拿到的是半截答案 + 一句解释。提前一轮告知，它会改为
		// "用已掌握的信息先给结论，缺的部分列出来"——**这是能用的输出**。
		//
		// 这是本项目一贯主张的又一例：边界要提前讲清，不能事后补。
		if turn == gov.max-1 && gov.max > aiTurnMin {
			msgs = append(msgs, aiMsg{Role: "user", Content: "（系统提示：这是本轮最后一次工具调用机会。下一轮你必须直接给出回答——" +
				"用已经查到的信息回答用户的问题，查不到的部分明确说\"这部分没查到\"，不要再调用工具。" +
				"不要为了凑完整性而继续探索。）"})
		}
		// 客户端（页面）已关闭/刷新：立即停止，不再向上游要 token
		if clientGone(ctx, w) {
			warn(scSys, "AI", "客户端已断开，停止本轮对话")
			return
		}
		// 统一按模型怪癖构造请求体（temperature / token 字段 / tools 均可自适应）。
		// 缓存友好布局：system 只放静态提示词+语言指令（会话内逐字节稳定，前缀可命中）；
		// 动态环境快照挪到对话末尾追加——含区块高度/服务 PID 等易变数据，放在开头会
		// 让 DeepSeek 前缀缓存在 system 处即分叉，导致全部对话历史反复重算（命中率 60~70%）。
		acTurn := aiSnap()
		full := make([]aiMsg, 0, len(msgs)+2)
		full = append(full, aiMsg{Role: "system", Content: aiSystemPrompt + aiLangDirective(lang)})
		full = append(full, msgs...)
		// 任务提示在循环外算一次：msgs 会随工具结果增长，但用户的原始诉求不变，
		// 每轮重算只会让记忆匹配抖动（同一问题匹配到不同记忆）。
		taskHint := aiTaskHint(msgs)
		full = append(full, aiMsg{Role: "user", Content: "（系统注入的当前环境快照，仅供你了解最新状态，不是用户发言，无需回应）\n<env_state>" + aiHealthSnapshotFor(taskHint) + "</env_state>"})
		payload := aiPayload(full, aiToolDefs(), acTurn.quirkRead(), -1, true)
		b, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, "POST", acTurn.endpoint(), strings.NewReader(string(b)))
		if err != nil {
			sseWrite(w, fl, map[string]any{"type": "error", "text": err.Error()})
			sseDoneUsage(w, fl, &usage)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+acTurn.keyPlain())
		resp, err := httpStream.Do(req)
		if err != nil {
			// 网络抖动/DNS 抖动等瞬时错误：退避后重试（429/5xx 同理，见下方状态码分支）
			if netRetry < 2 && ctx.Err() == nil {
				netRetry++
				d := time.Second
				if netRetry > 1 {
					d = 3 * time.Second
				}
				warn(scSys, "AI", "上游请求失败（%v），%s 后重试（第 %d/2 次）", err, d, netRetry)
				if !aiSleepCtx(ctx, d) {
					return
				}
				continue
			}
			sseWrite(w, fl, map[string]any{"type": "error", "text": "AI 服务连接失败：" + err.Error()})
			traceSetOutcome(outFailed) // v2.7
			sseDoneUsage(w, fl, &usage)
			return
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
			resp.Body.Close()
			msg := firstLines(strings.TrimSpace(string(b)), 300)
			// 参数自适应：temperature 被拒 → 改 1 → 再不行就不发；token 字段/工具不支持同理
			if paramFix < 2 {
				if newQ, changed, hint := aiFixParamError(msg); changed {
					paramFix++
					aiMutate(func(a *AIConfig) { a.setQuirk(newQ); saveExternalConfig(cfg) })
					warn(scSys, "AI", "%s（模型 %s）：%s", hint, acTurn.Model, firstLines(msg, 120))
					continue
				}
			}
			// 429 限流 / 5xx 服务端故障：瞬时性问题，退避重试而不是直接把错误抛给用户
			if resp.StatusCode == 429 || resp.StatusCode >= 500 {
				if netRetry < 2 && ctx.Err() == nil {
					netRetry++
					d := time.Second
					if netRetry > 1 {
						d = 3 * time.Second
					}
					warn(scSys, "AI", "上游返回 HTTP %d，%s 后重试（第 %d/2 次）", resp.StatusCode, d, netRetry)
					if !aiSleepCtx(ctx, d) {
						return
					}
					continue
				}
			}
			sseWrite(w, fl, map[string]any{"type": "error", "text": fmt.Sprintf("AI 服务返回 HTTP %d：%s", resp.StatusCode, msg)})
			traceSetOutcome(outFailed) // v2.7
			sseDoneUsage(w, fl, &usage)
			return
		}

		contentB := strings.Builder{}
		reasonB := strings.Builder{}
		streamOpen := false        // 是否已开始向界面推送正文
		holdMenu := false          // 本 turn 判定为菜单式无效回复 → 整段丢弃
		holdB := strings.Builder{} // 首句缓冲区：先判定是不是菜单，再决定是否推送
		acc := map[int]*aiAccTool{}
		finish := ""
		upErr := ""

		// 客户端断开时立刻关闭上游响应体：解除 Scanner 阻塞（上游安静时也能马上停），
		// 避免"用户已关页面、后台还在等模型吐字"的白烧 token。
		// 上游按行读取放到独立 goroutine，主循环 select 三路：客户端断开 / 心跳 / 上游行。
		// ① 写入只发生在一个 goroutine（无竞态）；② 心跳写失败即代表页面已关闭 → 实时中断；
		// ③ 心跳同时可防止反向代理/中间层的空闲断连。
		var readErr error
		lineCh := make(chan string, 128)
		go func() {
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				lineCh <- sc.Text()
			}
			readErr = sc.Err() // 先写后 close：读侧在 channel 关闭后读取，channel 提供 happens-before
			close(lineCh)
		}()

		rc := http.NewResponseController(w)
		ticker := time.NewTicker(10 * time.Second)
		turnTimer := time.NewTimer(aiTurnTimeout) // 单轮上游最长等待（见 aiTurnTimeout 注释）
		stopTimers := func() { ticker.Stop(); turnTimer.Stop() }
	reading:
		for {
			var line string
			select {
			case <-ctx.Done():
				stopTimers()
				resp.Body.Close()
				warn(scSys, "AI", "客户端已断开，已中断本轮上游流式响应")
				return
			case <-turnTimer.C:
				stopTimers()
				resp.Body.Close()
				warn(scSys, "AI", "上游 %s 内未返回任何数据，已中断本轮请求（可换更快的模型或检查网络/代理）", aiTurnTimeout)
				sseWrite(w, fl, map[string]any{"type": "error", "text": fmt.Sprintf("AI 上游超时（%s 无数据），已中断本轮", aiTurnTimeout)})
				sseDoneUsage(w, fl, &usage)
				return
			case <-ticker.C:
				// 心跳：既保持长连接，也用写失败来判定"页面已关闭"（Flusher 本身不返回错误）
				_, werr := io.WriteString(w, ": ping\n\n")
				if werr == nil {
					werr = rc.Flush()
				}
				if werr != nil {
					stopTimers()
					resp.Body.Close()
					warn(scSys, "AI", "客户端已断开（心跳写入失败：%v），已中断本轮上游流式响应", werr)
					return
				}
				continue
			case l, ok := <-lineCh:
				if !ok {
					break reading
				}
				line = l
			}
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}
			var ev struct {
				Usage *struct {
					PromptTokens          int64 `json:"prompt_tokens"`
					CompletionTokens      int64 `json:"completion_tokens"`
					PromptCacheHitTokens  int64 `json:"prompt_cache_hit_tokens"`
					PromptCacheMissTokens int64 `json:"prompt_cache_miss_tokens"`
					PromptTokensDetails   *struct {
						CachedTokens int64 `json:"cached_tokens"`
					} `json:"prompt_tokens_details"`
				} `json:"usage"`
				Choices []struct {
					Delta struct {
						Content   string `json:"content"`
						ToolCalls []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
						ReasoningContent string `json:"reasoning_content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			if ev.Error != nil {
				upErr = ev.Error.Message
				break
			}
			if ev.Usage != nil {
				usage.Prompt += ev.Usage.PromptTokens
				usage.Completion += ev.Usage.CompletionTokens
				// 缓存细分：DeepSeek 专用字段优先，兼容 OpenAI 风格 prompt_tokens_details.cached_tokens
				if ev.Usage.PromptCacheHitTokens > 0 || ev.Usage.PromptCacheMissTokens > 0 {
					usage.Hit += ev.Usage.PromptCacheHitTokens
					usage.Miss += ev.Usage.PromptCacheMissTokens
				} else if ev.Usage.PromptTokensDetails != nil && ev.Usage.PromptTokensDetails.CachedTokens > 0 {
					usage.Hit += ev.Usage.PromptTokensDetails.CachedTokens
					if ev.Usage.PromptTokens > ev.Usage.PromptTokensDetails.CachedTokens {
						usage.Miss += ev.Usage.PromptTokens - ev.Usage.PromptTokensDetails.CachedTokens
					}
				}
				usage.HasCache = usage.Hit > 0 || usage.Miss > 0
			}
			if len(ev.Choices) == 0 {
				continue
			}
			ch := ev.Choices[0]
			if ch.Delta.ReasoningContent != "" {
				reasonB.WriteString(ch.Delta.ReasoningContent)
				sseWrite(w, fl, map[string]any{"type": "reasoning", "text": ch.Delta.ReasoningContent})
			}
			if ch.Delta.Content != "" {
				contentB.WriteString(ch.Delta.Content)
				if streamOpen {
					sseWrite(w, fl, map[string]any{"type": "delta", "text": ch.Delta.Content})
				} else if !holdMenu {
					holdB.WriteString(ch.Delta.Content)
					hs := holdB.String()
					decided := strings.ContainsAny(hs, "。！？\n") || len([]rune(hs)) >= 60
					if decided {
						if aiLooksLikeMenuPrefix(hs) {
							holdMenu = true
						} else {
							streamOpen = true
							sseWrite(w, fl, map[string]any{"type": "delta", "text": hs})
							holdB.Reset()
						}
					}
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				a := acc[tc.Index]
				if a == nil {
					a = &aiAccTool{}
					acc[tc.Index] = a
				}
				if tc.ID != "" {
					a.ID = tc.ID
				}
				if tc.Function.Name != "" {
					a.Name += tc.Function.Name
				}
				a.ArgsB.WriteString(tc.Function.Arguments)
			}
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
		}
		stopTimers()
		resp.Body.Close()
		if clientGone(ctx, w) {
			warn(scSys, "AI", "客户端已断开，已中断本轮上游流式响应")
			return
		}
		if readErr != nil {
			upErr = readErr.Error()
		}
		if upErr != "" {
			sseWrite(w, fl, map[string]any{"type": "error", "text": "AI 服务返回错误：" + upErr})
			traceSetOutcome(outFailed) // v2.7：上游报错就是没做成，不能留在 aborted 兜底里
			sseDoneUsage(w, fl, &usage)
			return
		}

		// turn 结束：一直没达到推送条件但内容有效（短回答）时补推
		if !streamOpen && !holdMenu && holdB.Len() > 0 {
			streamOpen = true
			sseWrite(w, fl, map[string]any{"type": "delta", "text": holdB.String()})
			holdB.Reset()
		}

		// 无工具调用：先做确定性意图兜底（模型不发起调用时保证"说了就执行"），否则正常结束
		if finish != "tool_calls" && len(acc) == 0 {
			// 把本轮助手回复记入本地 msgs：若它是"需要我帮你…吗？"式提议，
			// 下一轮用户回"好/需要"时兜底才能从这条提议里提取动作。
			if contentB.Len() > 0 {
				msgs = append(msgs, aiMsg{Role: "assistant", Content: contentB.String(), ReasoningContent: reasonB.String()})
			}
			lastUser := ""
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == "user" {
					lastUser = msgs[i].Content
					break
				}
			}
			// 确认续接轮（最后一条是 tool 结果）不做兜底，避免重复触发
			if lastUser != "" && !confirmed && !preChecked && preflight {
				tool, args, isWrite := aiBacktrackIntent(lastUser, msgs)
				if tool != "" {
					callID := fmt.Sprintf("intent_%d", time.Now().UnixNano())
					if isWrite {
						sseWrite(w, fl, map[string]any{"type": "confirm_request", "tool_call_id": callID, "tool": tool, "args": args})
						sseDoneConfirm(w, fl, &usage)
						return
					}
					res, exErr := aiToolRegistry[tool].Execute(args)
					resText := res
					if exErr != nil {
						resText = "执行出错：" + exErr.Error()
					}
					sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": tool, "result": firstLines(resText, 300)})
					msgs = append(msgs, aiToolResultAsUser(tool, firstLines(resText, 3000)))
					continue // 让模型基于工具结果作答
				}
			}
			// 菜单/反问式无效回复：丢弃该段（用户看不到）并强令重答（最多 2 次）
			if menuRetried < 2 && (holdMenu || aiLooksLikeMenu(contentB.String())) {
				menuRetried++
				warn(scSys, "AI", "检测到菜单式无效回复（第 %d 次，已屏蔽不显示），要求模型重新作答", menuRetried)
				msgs = append(msgs, aiMsg{Role: "user", Content: "（系统强制要求）你上一条回复无效：禁止罗列能力清单、禁止编号菜单、禁止反问用户。" +
					"现在必须直接给出结论：如果上面已有工具结果，就基于它总结现状与建议；" +
					"如果需要执行操作，就立即发起对应工具调用，不要先问。"})
				continue
			}
			if holdMenu {
				warn(scSys, "AI", "菜单式回复无法纠正，改用确定性提示")
				sseWrite(w, fl, map[string]any{"type": "delta", "text": "没理解你的需求。可以直接说：「启动服务」「备份数据库」「查看日志」「检测数据库连接」。"})
			}
			// v2.3 N5：正常收尾也给一份"这次怎么做的"。
			// 用户看着 AI 答完 knowing 它查了什么，比只看到结论更可信——
			// 这也是"可核查"的体现：结论必须能追到查过的具体东西。
			if s := planSummary(plan, "已得出结论"); s != "" {
				sseWrite(w, fl, map[string]any{"type": "plan_end", "text": s})
			}
			// v2.7：给轨迹一个真实结局。
			//
			// 此前 handleAIChat 的 traceOutcome 声明后从未被赋值，
			// 导致 305 条轨迹 outcome 100% 是 aborted，**任何依赖结局的统计恒等于同一个值**。
			// 判据（按可靠性排序）：
			//   · 有工具失败但仍答完 → partial（结论有，但过程不干净）
			//   · 有工具失败且答不出内容 → failed
			//   · 正常答完 → success
			// **不能只看"模型有没有输出文字"** —— 那等于把「说了话」当「做成了」，
			// 与本项目反复反对的「执行即成功」是同一个错误。
			traceSetOutcome(aiTurnOutcome(hasToolFailure, contentB.Len(), holdMenu))
			sseDoneUsage(w, fl, &usage)
			return
		}

		// 有工具调用：逐个处理
		msgs = append(msgs, aiMsg{Role: "assistant", Content: contentB.String(), ReasoningContent: reasonB.String()})
		assistantIdx := len(msgs) - 1
		toolMsgs := []aiMsg{}

		for i := 0; i < len(acc); i++ {
			a, ok := acc[i]
			if !ok {
				continue
			}
			var args map[string]any
			argsStr := strings.TrimSpace(a.ArgsB.String())
			if argsStr == "" {
				argsStr = "{}"
			}
			if json.Unmarshal([]byte(argsStr), &args) != nil {
				args = map[string]any{}
			}
			def, known := aiToolRegistry[a.Name]
			toolMsg := aiMsg{Role: "tool", ToolCallID: a.ID}
			// v2.2 PolicyGate：先问策略再动手。forbidden 让模型当场放弃，
			// 而不是发出去被执行层拒绝——那样它会以为"再换个参数试试"，
			// 白花 token 且可能反复试探。
			verdict := PolicyGate(a.Name, policyTargetOf(a.Name, args), actAI)
			// v2.3 N1：裁决与选型进轨迹。plan 记"为什么选这个动作"，
			// policy 记"被判成什么档位、依据哪条规则"——归因到 rule 才谈得上改。
			tracePlanFromTool(a.Name, args)
			switch {
			case !known:
				toolMsg.Content = "错误：该工具不在白名单内"
				sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": a.Name, "result": toolMsg.Content})
			case verdict.Level == PolicyForbidden:
				policyRecord(verdict, "denied")
				tracePolicyDenied(verdict)
				msg := policyAIView(verdict)
				warn(scSys, "AI", "策略拒绝 %s：%s", a.Name, verdict.Reason)
				sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": a.Name, "result": msg})
				msgs = append(msgs, aiMsg{Role: "user", Content: "（系统）" + msg})
				continue
			case verdict.OptIn && !aiUserAskedFor(a.Name, msgs):
				// "需用户明确要求"的工具（如加白名单）：模型不能自作主张发起，丢弃并要求直接回答。
				// 同一轮拦截 ≥3 次说明模型在死循环重试，直接终止并给用户可见提示，避免日志刷屏。
				optInBlocked++
				warn(scSys, "AI", "已拦截模型未经要求发起的 %s 调用（第 %d 次）", a.Name, optInBlocked)
				if optInBlocked >= 3 {
					hint := "这个操作需要你明确说一句（例如「备份数据库」「加白名单」）我才会发起；也可以直接用界面上对应的按钮。"
					if lang == "en" {
						hint = "This action needs your explicit request (e.g. \"back up the database\", \"add to Defender whitelist\"); or use the matching button in the UI."
					}
					sseWrite(w, fl, map[string]any{"type": "delta", "text": "\n" + hint + "\n"})
					sseDoneUsage(w, fl, &usage)
					return
				}
				msgs = append(msgs, aiMsg{Role: "user", Content: "（系统）不要发起用户没有要求的操作。请直接回答用户刚才的问题。"})
				continue
			case def.Write:
				// 写工具：转确认流程，本轮结束，等用户在 UI 确认后带 confirm 重新提交
				// v2.3 N1：confirm 档也进轨迹。与 forbidden 不同，它不是"拒绝"，
				// 而是"用户有机会点头"——两者的区别在归因时很关键：
				// forbidden 说明规则太严，confirm 说明档位合适但用户没批。
				tracePolicy(verdict, "pending")
				sseWrite(w, fl, map[string]any{"type": "confirm_request", "tool_call_id": a.ID, "tool": a.Name, "args": args,
					"text": aiToolCnName(a.Name, args)})
				// 把未完成的 assistant 消息保留在本地 msgs（供确认后续接），并结束本次 SSE
				msgs[assistantIdx].ToolCalls = append(msgs[assistantIdx].ToolCalls, func() aiToolCall {
					var t aiToolCall
					t.ID = a.ID
					t.Type = "function"
					t.Function.Name = a.Name
					t.Function.Arguments = argsStr
					return t
				}())
				_ = toolMsg
				sseDoneConfirm(w, fl, &usage)
				return
			default:
				t0 := time.Now()
				// v2.3 N5：执行前先把"正在做什么"告诉用户。
				// 以前用户只能在全部跑完后才知道它做了什么，
				// 中间十几秒完全不知道是在工作还是在卡住。
				tgt := policyTargetOf(a.Name, args)
				// v2.3 N8：同一工具+同一参数已失败 ≥2 次时，在执行前就拦住，
				// 并给出**具体的替代动作**。放到执行后判是晚了——
				// 第三次调用已经烧掉了，模型也已经被自己的失败结果带偏了。
				if rp.shouldIntervene(a.Name, tgt) {
					key := replanKey(a.Name, tgt)
					rp.stuckTools[key] = true
					hint := replanHint(a.Name, tgt)
					warn(scSys, "AI", "%s 用相同参数已失败 %d 次，插入换思路引导",
						a.Name, rp.fails[key])
					auditNow(actAI, "ai_stuck", a.Name, tgt, resFail,
						"同一动作连续失败 "+itoa(rp.fails[key])+" 次，插入换思路引导")
					// 用 user 消息承载：合成 assistant+tool 会触发 thinking 模型 400
					toolMsg.Content = hint
					sseWrite(w, fl, map[string]any{"type": "tool_result",
						"tool": a.Name, "result": firstLines(hint, 300)})
					sseWrite(w, fl, map[string]any{"type": "delta",
						"text": "\n" + replanUserNotice(a.Name, tgt, rp.fails[key]) + "\n"})
					toolMsgs = append(toolMsgs, toolMsg)
					continue
				}
				plan.add(a.Name, tgt, planLine(a.Name, tgt))
				sseWrite(w, fl, map[string]any{"type": "plan_step",
					"seq": len(plan.Steps), "tool": a.Name,
					"target": tgt, "text": planLine(a.Name, tgt)})
				res, exErr := def.Execute(args)
				toolDur := time.Since(t0).Milliseconds()
				toolMsg.Content = res
				// v2.3 M9：每次工具执行记一步轨迹。"无效操作次数"这个指标
				// 就是从这里算的——同一 action 连续失败 ≥2 次、denied 后仍重试、
				// 只读工具读了个与任务无关的东西，都能在轨迹里看出来。
				traceToolStep(a.Name, args, res, exErr, toolDur)
				if exErr != nil {
					rp.markFail(a.Name, tgt)
					hasToolFailure = true
					toolMsg.Content = "执行出错：" + exErr.Error()
				}
				sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": a.Name, "result": firstLines(toolMsg.Content, 300)})
				// 提额判定放在**工具执行之后**：调用前不知道结果，
				// 而"这一轮有没有换来新信息"正是要看结果才知道。
				// 失败不计入 productive——失败那一轮没有推进认知。
				if exErr == nil {
					if raised, newMax := gov.observe(a.Name); raised {
						info(scSys, "AI", "每一轮都有新信息，工具调用预算从 %d 提到 %d 轮",
							newMax-budgetEscalateStep, newMax)
						auditNow(actAI, "ai_budget_raised", a.Name, gov.base.Reason, resOK,
							fmt.Sprintf("%d->%d", newMax-budgetEscalateStep, newMax))
						sseWrite(w, fl, map[string]any{"type": "plan_extend",
							"max_turns": newMax, "reason": "每一轮都有新信息，自动放宽上限"})
					}
				}
			}
			msgs[assistantIdx].ToolCalls = append(msgs[assistantIdx].ToolCalls, func() aiToolCall {
				var t aiToolCall
				t.ID = a.ID
				t.Type = "function"
				t.Function.Name = a.Name
				t.Function.Arguments = argsStr
				return t
			}())
			toolMsgs = append(toolMsgs, toolMsg)
		}
		msgs = append(msgs, toolMsgs...)
		// 继续下一轮（模型消化工具结果）
	}
	// 预算用尽：**必须说清楚**。
	// 这与 v2.0 修掉的"谎报成功"同源——没把"到此为止"说出口，
	// 用户看到的是一段没答完的话，既不知道停在哪也不知道为什么。
	budget.Max = gov.max // 用提额后的真实上限，不让用户看到已经作废的初值
	notice := budgetNotice(budget, gov.max)
	warn(scSys, "AI", "工具调用预算用尽（%d 轮，%s），已 %d 步",
		gov.max, budget.Reason, len(plan.Steps))
	auditNow(actAI, "ai_budget_exhausted", "", budget.Reason, resFail,
		fmt.Sprintf("max=%d steps=%d raised=%d", gov.max, len(plan.Steps), gov.escalated))
	if s := planSummary(plan, notice); s != "" {
		sseWrite(w, fl, map[string]any{"type": "plan_end", "text": s})
	}
	sseWrite(w, fl, map[string]any{"type": "delta", "text": "\n" + notice + "\n"})
	// v2.7：预算耗尽是**没有达成**，不是成功。
	// 内容可能已经给了一部分，但那属于 partial —— 有产出、没做完。
	traceSetOutcome(outPartial)
	sseDoneUsage(w, fl, &usage)
}
