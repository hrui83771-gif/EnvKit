package main

// ai_sse.go —— SSE 传输层：事件写入、连接就绪、收尾事件、客户端断开检测
//
// 从 ai_loop.go 拆出。拆的理由是职责边界而非行数：这四件事
// 与"AI 该调什么工具"完全无关，改主循环时不该看到它们。
//
// 客户端断开检测（clientGone）特别值得独立：它是整个循环里
// 唯一的"逃生阀"，关窗/刷新页面时靠它停止向上游要 token，
// 混在主循环里容易被改坏而不自知。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func sseWrite(w http.ResponseWriter, fl http.Flusher, v map[string]any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
	fl.Flush()
}

// sseWait 在"用户只能干等"的阶段开始时发一条带原因和真实上限的事件。
//
// budget 一律取服务端真实的超时常量（0 = 没有可承诺的上限）。前端据此显示
// 「原因 · 最长约 Ns · 已等 Ms」；超过上限只把文案换成「比预期久」，计时继续往上走
// ——绝不画一个会归零的假倒计时。reason 是稳定标识，文案由前端按界面语言翻译，
// 不把中文原文塞进事件里（英文界面就不会冒出中文）。
func sseWait(w http.ResponseWriter, fl http.Flusher, reason string, budget time.Duration) {
	ev := map[string]any{"type": "wait", "reason": reason}
	if budget > 0 {
		ev["budget_ms"] = int64(budget / time.Millisecond)
	}
	sseWrite(w, fl, ev)
}

// sseToolResult 发 tool_result 事件，并带上**结构化的成败标志** ok。
//
// 为什么必须有这个字段：前端轨迹时间轴以前靠中文正则猜"这条结果是好是坏"
// （/^(错误|执行出错)|失败|拒绝|不在白名单/），换个措辞、或者上游报一句英文
// （connection refused）就猜不出来 —— 而这个系统自己的原则是
// "前端不重新判定结果，判据只来自服务端字段"。猜文本就是自打脸。
//
// ok 一律由调用方按真实判据给（exErr / verdict / d.Proceed），
// 不要在这里从 result 文本反推。
func sseToolResult(w http.ResponseWriter, fl http.Flusher, tool, result string, ok bool) {
	sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": tool, "result": result, "ok": ok})
}

// aiToolOKFromText 没有 exErr 可用时的兜底判据（只认这几类确定失败的前缀）。
// 用在"结果只有字符串、拿不到 error"的路径上，避免每处各写一遍正则。
func aiToolOKFromText(res string) bool {
	t := strings.TrimSpace(res)
	for _, bad := range []string{"执行出错", "错误：", "错误:", "不在白名单", "策略拒绝", "已拒绝", "已取消"} {
		if strings.HasPrefix(t, bad) {
			return false
		}
	}
	return true
}
func aiChatReady(w http.ResponseWriter) (http.Flusher, bool) {
	if !aiSnap().ready() {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","text":"AI 助手未配置：请到「程序配置 → AI 助手」填写 Base URL 与 API Key"}`))
		return nil, false
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	fl.Flush()
	return fl, true
}

// clientGone 判断客户端是否已不在（context 取消 或 SSE 写失败）
func clientGone(ctx context.Context, w http.ResponseWriter) bool {
	if ctx.Err() != nil {
		return true
	}
	if ew, ok := w.(interface{ writeErr() error }); ok {
		return ew.writeErr() != nil
	}
	return false
}

// sseDoneUsage / sseDoneConfirm：回合收尾。有 usage 就在 done 前补一条独立 usage 事件
// （前端据此在回复尾部显示 token 统计并累计到桌宠"今日统计"），没有就只发 done。
//
// ## 为什么收尾要在这里记，而不是在 handleAIChat
//
// 收尾点有 13 处（成功、参数自适应、连接失败、确认待批、预算耗尽……），
// 每处都自己判断结局必然漏——**实测已经漏过一次**：
// handleAIChat 里的 traceOutcome 声明后从未被赋值，305 条轨迹全是 aborted。
// 这两个 helper 是 13 处**唯一的公共出口**，在这里记一次就全覆盖。
//
// usage 也在这里记：它是回合内累加的（跨多轮工具调用），
// 只有收尾时才是最终值。
// sseUsage 带上"提供方 / 模型"：用量卡要显示是哪一次调用花的钱与缓存。
// 不加字段的话前端只能去猜当前配置——而配置可以在会话中途被改，
// 事后回看历史卡片就会张冠李戴。
func sseUsage(u *aiUsage) map[string]any {
	ac := aiSnap()
	return map[string]any{"type": "usage", "usage": *u, "model": ac.Model, "provider": ac.Provider}
}

func sseDoneUsage(w http.ResponseWriter, fl http.Flusher, u *aiUsage) {
	sseDoneWith(w, fl, u, doneCompleted, "")
}

// done 事件的 reason：为什么这一轮结束。
const (
	doneCompleted    = "completed"        // 模型正常答完
	doneAborted      = "aborted"          // 流被中途打断（连接断了/被掐）
	doneEmpty        = "empty"            // 模型一句话都没返回
	doneAwaitConfirm = "awaiting_confirm" // 等用户点确认，回合还没结束
)

// sseDoneWith 在 done 事件里带上**为什么结束**。
//
// done 以前只有 {"type":"done"}，前端也就无从显示：预算耗尽、中途断流、
// 模型空回复在界面上长得一模一样 —— 都是"它自己停了"，用户既不知道为什么，
// 也不知道能不能接着拿结果，只能重发一条消息试探（2026-10-30 真实反馈：
// "探索完自己就停了，不知道咋回事，我可能要重新对话才能获取结果"）。
//
// 所以收尾必须带上原因。note 是给用户看的一句话，没有就别硬编。
func sseDoneWith(w http.ResponseWriter, fl http.Flusher, u *aiUsage, reason, note string) {
	recordTurnUsage(u)
	if u.Prompt > 0 || u.Completion > 0 {
		sseWrite(w, fl, sseUsage(u))
	}
	ev := map[string]any{"type": "done", "reason": reason}
	if note != "" {
		ev["note"] = note
	}
	sseWrite(w, fl, ev)
}

// aiAbortNotice 流被中途打断时，先把"走到哪一步"交代清楚。
//
// 与 budgetNotice 的分工：那个说"预算用完，我按计划停"（这是有终点的），
// 这个说"没走到终点，中途断了"。两者都不能静默 —— 用户看到的都是话没说完，
// 但能不能接着拿结果，取决于他知不知道发生了什么。
func aiAbortNotice(w http.ResponseWriter, fl http.Flusher, p *planTrack) {
	if s := planSummary(p, "中途中断"); s != "" {
		sseWrite(w, fl, map[string]any{"type": "plan_end", "text": s})
	}
}

func sseDoneConfirm(w http.ResponseWriter, fl http.Flusher, u *aiUsage) {
	recordTurnUsage(u)
	if u.Prompt > 0 || u.Completion > 0 {
		sseWrite(w, fl, sseUsage(u))
	}
	// 待用户确认：这不是失败也不是成功，**不写 outcome**——
	// 让它留在 aborted 兜底上，等确认后的下一段回合再定。
	// 写成 pending 会在看板里凭空多一类"没结论"。
	sseWrite(w, fl, map[string]any{"type": "done", "reason": doneAwaitConfirm})
}

// recordTurnUsage 把本回合 token 累加进轨迹，并按「有没有产出」定结局。
func recordTurnUsage(u *aiUsage) {
	b := currentOutcomeBox()
	if b == nil {
		return
	}
	b.addUsage(u)
}

// traceSetOutcome 记录本回合结局。空轨迹时静默忽略。
func traceSetOutcome(o string) {
	if b := currentOutcomeBox(); b != nil {
		b.set(o)
	}
}

// aiTurnOutcome 判定一次 AI 回合的结局。
//
// ## 判据为什么这样定
//
// 关键是**不把「模型说了话」当「事情做成了」**——
// 那正是本项目反复反对的「执行即成功」在 AI 侧的重演。
// 所以失败工具留下的痕迹会拉低结局，即使模型最终答得很漂亮。
//
// | 情形 | 结局 | 理由 |
// |---|---|---|
// | 有工具失败 + 答出内容 | partial | 结论有，但过程不干净，不能算干净成功 |
// | 有工具失败 + 没有内容 | failed | 什么都没答出来 |
// | 菜单式回复无法纠正 | failed | 没理解需求就是没做成 |
// | 正常答完 | success | — |
func aiTurnOutcome(hasToolFailure bool, contentLen int, holdMenu bool) string {
	if holdMenu {
		return outFailed
	}
	if hasToolFailure {
		if contentLen > 0 {
			return outPartial
		}
		return outFailed
	}
	return outSuccess
}
