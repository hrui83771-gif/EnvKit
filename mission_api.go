package main

// mission_api.go —— 行动 API（Phase 1 T4）。
//
// 风格对齐 memory_api.go：本库**没有路径参数路由**（main.go 全是平铺
// HandleFunc），所以不用 /api/missions/:id/pause，而是单端点 + op 字段。
// 鉴权由 authMW 统一做（/api/* 必带令牌 + Origin 本机校验），handler 只管方法。
//
// 端点：
//   GET  /api/missions          列表（含 paused 的「待你继续」）
//   POST /api/missions          {op: create|pause|resume|cancel, ...}
//   POST /api/trigger           入站触发（inbound_hook 默认关，只服务本机）
//   GET  /api/missions/events   SSE：行动列表实时状态（前端可降级为轮询）
//
// 红线：POST /api/trigger 只产出 **pending** 行动——等人批才执行任何动作
//（红线 2），触发端点自己永远不启动执行。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// missionNotify 行动变更通知（SSE 即时推送；容量 1，合并突发）。
var missionNotify = make(chan struct{}, 1)

func notifyMissions() {
	select {
	case missionNotify <- struct{}{}:
	default:
	}
}

// handleMissions 行动列表与操作。
func handleMissions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"ok": true, "missions": missionList()})
	case http.MethodPost:
		var body struct {
			Op   string `json:"op"`
			ID   string `json:"id"`
			Goal string `json:"goal"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "参数错误"})
			return
		}
		var (
			m   Mission
			err error
		)
		switch body.Op {
		case "create":
			m, err = missionNew(body.Goal, Trigger{Kind: "manual"})
			if err == nil {
				auditNow(actUser, "mission_create", m.ID, "", resOK, firstLines(m.Goal, 80))
			}
		case "pause":
			m, err = missionUpdate(body.ID, func(x *Mission) error {
				if err := missionGo(x, msPaused); err != nil {
					return err
				}
				x.PauseWhy = "用户暂停"
				return nil
			})
			if err == nil {
				auditNow(actUser, "mission_pause", body.ID, "", resOK, "")
			}
		case "resume":
			// resume 只校验「可继续」：真正的状态切换发生在下一次
			// 带 mission_id 的对话（aiMissionBind，paused → running）。
			// 这样执行永远只有一条入口，不会出现「API 说在跑、其实没人跑」。
			var found bool
			m, found = missionGet(body.ID)
			if !found {
				err = fmt.Errorf("行动不存在：%s", body.ID)
			} else {
				switch m.State {
				case msPending, msPaused, msAwaitingConfirm:
				default:
					err = errMissionTerminal(m.State)
				}
			}
			if err == nil {
				auditNow(actUser, "mission_resume", body.ID, "", resOK, "")
			}
		case "cancel":
			m, err = missionUpdate(body.ID, func(x *Mission) error {
				return missionGo(x, msCanceled)
			})
			if err == nil {
				auditNow(actUser, "mission_cancel", body.ID, "", resOK, "")
			}
		default:
			writeJSON(w, map[string]any{"ok": false, "error": "op 必须是 create / pause / resume / cancel"})
			return
		}
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		notifyMissions()
		writeJSON(w, map[string]any{"ok": true, "mission": m})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleMissionTrigger 入站触发端点（Phase 1 验收 3）。
//
// 只服务本机（Web 只听 127.0.0.1，鉴权沿用 authMW）；inbound_hook 默认关。
// 产出 pending 行动，**等人批**——触发器自己绝不执行动作。
func handleMissionTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !cfg.Mission.inboundOn() {
		writeJSON(w, map[string]any{"ok": false,
			"error": "入站触发未开启（config.json → missions.inbound_hook = true 才启用）"})
		return
	}
	var body struct {
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	body.Title = truncateRunes(strings.TrimSpace(body.Title), 200)
	if body.Title == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "title 不能为空"})
		return
	}
	switch body.Kind {
	case "":
		body.Kind = "webhook"
	case "patrol", "schedule", "webhook", "guard":
	default:
		writeJSON(w, map[string]any{"ok": false, "error": "kind 必须是 patrol / schedule / webhook / guard"})
		return
	}
	m, err := missionNew(body.Title, Trigger{Kind: body.Kind, Detail: truncateRunes(strings.TrimSpace(body.Detail), 500)})
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	auditNow(actSys, "mission_trigger", m.ID, body.Kind, resOK, firstLines(body.Title, 80))
	info(scSys, "行动", "入站触发（%s）→ 待批行动 %s", body.Kind, m.ID)
	notifyMissions()
	writeJSON(w, map[string]any{"ok": true, "mission": m})
}

// handleMissionEvents SSE：行动列表实时状态。
//
// 双通道：变更通知（即时）+ 2s 定时快照对比（兜底，兼作前端轮询降级的
// 同款数据源）。快照对比用 JSON 序列化结果，避免依赖相等性陷阱。
func handleMissionEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")

	snap := func() string {
		b, _ := json.Marshal(missionList())
		return string(b)
	}
	emit := func(payload string) bool {
		if _, err := fmt.Fprintf(w, "data: {\"type\":\"missions\",\"missions\":%s}\n\n", payload); err != nil {
			return false // 客户端断开
		}
		fl.Flush()
		return true
	}
	last := snap()
	if !emit(last) {
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		case <-missionNotify:
		}
		cur := snap()
		if cur == last {
			continue
		}
		last = cur
		if !emit(cur) {
			return
		}
	}
}
