package main

// ai_wait.go —— 确认卡"就地等待"：确认不再终结回合。
//
// ## 要修的问题
//
// 旧流程：写工具 → 发确认卡 → `done(awaiting_confirm)` 结束 SSE → 用户点确认 →
// 客户端**重新发起一次完整请求**（带 confirm）。那一次请求要把 system + 26 个工具
// 定义 + 全部历史重新预填一遍：多一次往返、丢一次前缀缓存、确认前后的两段回答
// 也被切成两个回合（轨迹、用量、审计都断成两截）。一个三步任务要经历三次。
//
// 新流程：发卡后**不结束 SSE**，在当前请求里等用户点按；决定到达后立刻接着执行、
// 继续同一个循环。上下文一次都没重建，缓存、预算、轨迹都留在原地。
//
// ## 边界（两条路都留着，不是替换）
//
//   - 等不到（超时 / 浏览器关闭 / 旧版前端）→ 退回旧行为：发 `done(awaiting_confirm)`
//     并结束本次 SSE，客户端仍可用 confirm 重新提交。
//   - 裁决复用 `aiConfirmGate`（纯函数）：参数哈希比对、登记 TTL、PolicyGate 重过，
//     与旧路径**同一套判据**，不存在"新路径更松"。
//   - 同一时刻只登记一个等待者：对话本身串行，多开也不会串台。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// aiConfirmWaitMax 在本请求内等用户点确认的上限。超时后退回旧路径，
// 卡片仍然有效（登记 TTL 另有 30 分钟），不会把用户的操作丢掉。
const aiConfirmWaitMax = 10 * time.Minute

// aiConfirmKeepAlive 等待期间的心跳间隔。SSE 注释行（": ..."）会被前端解析器忽略，
// 只用来让中间代理别把"长时间无数据"的连接掐掉。
const aiConfirmKeepAlive = 20 * time.Second

type aiConfirmWaiter struct {
	callID string
	ch     chan aiConfirmDecision
	used   bool
}

var (
	aiWaiterMu sync.Mutex
	aiWaiter   *aiConfirmWaiter
)

func aiWaitInstall(callID string) *aiConfirmWaiter {
	w := &aiConfirmWaiter{callID: callID, ch: make(chan aiConfirmDecision, 1)}
	aiWaiterMu.Lock()
	aiWaiter = w
	aiWaiterMu.Unlock()
	return w
}

func aiWaitRemove(w *aiConfirmWaiter) {
	aiWaiterMu.Lock()
	if aiWaiter == w {
		aiWaiter = nil
	}
	aiWaiterMu.Unlock()
}

// aiWaitDeliver 把一次裁决交给正在等待的请求；没有等待者时返回 false，
// 调用方据此告诉前端"走旧路径"，而不是把决定悄悄吞掉。
func aiWaitDeliver(callID string, d aiConfirmDecision) bool {
	aiWaiterMu.Lock()
	w := aiWaiter
	if w == nil || w.callID != callID || w.used {
		aiWaiterMu.Unlock()
		return false
	}
	w.used = true
	aiWaiterMu.Unlock()
	select {
	case w.ch <- d:
		return true
	default:
		return false
	}
}

// aiAwaitConfirm 在本次请求内等用户决定。
// ok=false 表示没等到（超时/断开/前端不支持）——调用方按旧路径收尾，不要当成"用户拒绝"。
func aiAwaitConfirm(ctx context.Context, w http.ResponseWriter, fl http.Flusher, callID string) (aiConfirmDecision, bool) {
	wt := aiWaitInstall(callID)
	defer aiWaitRemove(wt)
	deadline := time.NewTimer(aiConfirmWaitMax)
	defer deadline.Stop()
	tick := time.NewTicker(aiConfirmKeepAlive)
	defer tick.Stop()
	// 先告诉前端"这一轮没结束，它在等你的决定"
	sseWrite(w, fl, map[string]any{"type": "waiting_confirm", "tool_call_id": callID})
	for {
		select {
		case d := <-wt.ch:
			return d, true
		case <-ctx.Done():
			return aiConfirmDecision{}, false
		case <-deadline.C:
			return aiConfirmDecision{}, false
		case <-tick.C:
			if clientGone(ctx, w) {
				return aiConfirmDecision{}, false
			}
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return aiConfirmDecision{}, false
			}
			fl.Flush()
		}
	}
}

// handleAIConfirm 确认卡的决定投递口：只把决定交给正在等待的那个循环，
// **不自己执行、不新开回合**——执行仍发生在原请求里（w/fl 是那一侧的）。
func handleAIConfirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ToolCallID string         `json:"tool_call_id"`
		Tool       string         `json:"tool"`
		Args       map[string]any `json:"args"`
		Approved   bool           `json:"approved"`
		Batch      bool           `json:"batch"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ToolCallID == "" {
		writeJSON(w, map[string]any{"ok": false, "reason": "missing_call_id"})
		return
	}
	aiWaiterMu.Lock()
	live := aiWaiter != nil && aiWaiter.callID == body.ToolCallID && !aiWaiter.used
	aiWaiterMu.Unlock()
	if !live {
		// 404 = "当前没有等待者"，前端据此退回旧路径（带 confirm 重新提交）。
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"ok": false, "reason": "no_waiter"})
		return
	}
	d := aiConfirmGate(body.Tool, body.ToolCallID, body.Approved, body.Args)
	// 只有在**放行**时才带上批量授权意图：拒绝的卡不可能顺手开授权
	d.Batch = body.Batch && d.Proceed
	if !aiWaitDeliver(body.ToolCallID, d) {
		w.WriteHeader(http.StatusGone)
		writeJSON(w, map[string]any{"ok": false, "reason": "waiter_gone"})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "proceed": d.Proceed, "rule": d.Rule})
}
