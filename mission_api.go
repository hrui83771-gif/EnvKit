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

// ---------- 配置段端点（设置页「基础配置 → 行动」） ----------

// handleMissionConfig 行动配置的独立出入口（/api/missions/config）。
//
// 与 handlePetConfig/handleAlertConfig 同范式：GET 回显生效值、POST 指针字段增量提交。
// 为什么不复用 POST /api/config：那是整包替换，missions 段此前靠 mergeFormConfig
// 的无条件保留才没被老页面抹掉（MissionConfig.Enabled 是指针，零值表达不了"关"）——
// 有了独立通道，两处配置面谁写谁的段，互不携带。
//
// 生效时机：调度器每轮 tick 读 cfg.Mission（trigger.go msSchedTick），
// 改完下一轮就按新参数跑，无需重启；正在等待的晨报时刻按新 BriefingAt 重新对表。
func handleMissionConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfgMu.Lock()
		mc := cfg.Mission
		cfgMu.Unlock()
		// 回显生效值（与 applyConfigDefaults 的默认一致）
		if mc.PatrolIntervalMin == 0 {
			mc.PatrolIntervalMin = 60
		}
		if mc.BriefingAt == "" {
			mc.BriefingAt = "08:30"
		}
		writeJSON(w, mc)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var body struct {
		Enabled           *bool   `json:"enabled"`
		PatrolIntervalMin *int    `json:"patrol_interval_min"`
		InboundHook       *bool   `json:"inbound_hook"`
		BriefingAt        *string `json:"briefing_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	// 晨报时刻沿用调度器的解析口径（trigger.go 用 "15:04"），
	// 校验放行前而不是事后：写错格式应当场拒绝，而不是保存成功后晨报静默消失。
	if body.BriefingAt != nil {
		bt := strings.TrimSpace(*body.BriefingAt)
		if bt != "" && bt != "off" {
			if _, err := time.ParseInLocation("15:04", bt, time.Local); err != nil {
				http.Error(w, "晨报时刻需为 HH:MM（24 小时制）或 off", 400)
				return
			}
		}
	}

	cfgMu.Lock()
	before := cfg.Mission
	if body.Enabled != nil {
		e := *body.Enabled
		cfg.Mission.Enabled = &e
	}
	if body.PatrolIntervalMin != nil {
		iv := *body.PatrolIntervalMin
		if iv < 0 {
			iv = 0
		}
		if iv > 24*60 {
			iv = 24 * 60 // 巡检间隔上限一天：再大等于关掉巡检，那该用开关表达
		}
		cfg.Mission.PatrolIntervalMin = iv
	}
	if body.InboundHook != nil {
		cfg.Mission.InboundHook = *body.InboundHook
	}
	if body.BriefingAt != nil {
		cfg.Mission.BriefingAt = strings.TrimSpace(*body.BriefingAt)
	}
	after := cfg.Mission
	saved := cfg
	cfgMu.Unlock()

	if !missionCfgEqual(before, after) {
		saveExternalConfig(saved)
		auditNow(actUser, "mission_config", "config.json", "missions", resOK,
			fmt.Sprintf("定时产出=%v 入站=%v 巡检间隔=%dmin 晨报=%s",
				after.on(), after.inboundOn(), after.PatrolIntervalMin, after.BriefingAt))
		notifyMissions() // 调度状态展示依赖行动列表刷新的同一通道
	}
	writeJSON(w, after)
}

// missionCfgEqual 按值比较两份行动配置（Enabled 是指针，直接 == 比的是地址）。
func missionCfgEqual(a, b MissionConfig) bool {
	on := func(p *bool) bool { return p == nil || *p }
	return on(a.Enabled) == on(b.Enabled) &&
		a.PatrolIntervalMin == b.PatrolIntervalMin &&
		a.InboundHook == b.InboundHook &&
		a.BriefingAt == b.BriefingAt
}
