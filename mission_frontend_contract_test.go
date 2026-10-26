package main

// mission_frontend_contract_test.go —— 行动（Mission）前后端契约。
//
// 沿用 runtime_frontend_contract_test.go 的教训：后端结构体建好了不算数，
// **前端真的能读到**才算数。这里钉三层：
//  1. Mission 的 JSON 字段名 = app.js 里实际读取的那些（m.state / m.pause_why …）；
//  2. aiChatReq 的 wire 字段 mission_id = 前端 aiConsume 注入的 body.mission_id；
//  3. 构建产物 web/index.html 含行动 tab —— 改了 src 忘跑 web_build.py 时立刻红。

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMissionJSONFieldsForFrontend 前端 aiMissionsRender 逐个读的字段，
// 序列化里一个都不能少（json tag 写错 = 结构体对、线上没有）。
func TestMissionJSONFieldsForFrontend(t *testing.T) {
	m := Mission{
		ID:       "m-20261026-120000-ABCDEF",
		Goal:     "检查磁盘趋势",
		State:    msPaused,
		Trigger:  Trigger{Kind: "patrol", Detail: "定时产出 08:30"},
		TraceID:  "t-20261026-120000-123456",
		PauseWhy: "连接中断或已停止，可点「继续」",
		Result: &OpResult{
			Ok: true, Verified: true, Action: "mission", Target: "m-20261026-120000-ABCDEF",
			Msg: "已复验通过：端口监听中",
		},
		CreatedAt: time.Now(), UpdatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	// 前端读取清单（app.js aiMissionsRender）：
	for _, k := range []string{
		`"id"`, `"goal"`, `"state"`, `"trigger"`, `"kind"`, `"detail"`,
		`"pause_why"`, `"result"`, `"verified"`, `"msg"`, `"updated_at"`,
	} {
		if !strings.Contains(s, k) {
			t.Errorf("Mission JSON 缺字段 %s（前端要读它）：%s", k, s)
		}
	}
	// 列表接口的外层形状：{ok, missions:[...]}（aiMissionsLoad 只认这个）
	lb, _ := json.Marshal(map[string]any{"ok": true, "missions": []Mission{m}})
	if !strings.Contains(string(lb), `"missions":[`) {
		t.Errorf("列表响应缺 missions 数组：%s", lb)
	}
}

// TestChatReqMissionIDWire 前端发的是 body.mission_id，后端必须按这个名字收。
//
// 这类契约断的是**重命名事故**：后端把 json tag 改成 missionId、
// 或前端漏掉注入，两个编译期都发现不了——对话照常跑，只是行动永远不收口。
func TestChatReqMissionIDWire(t *testing.T) {
	var req aiChatReq
	if err := json.Unmarshal([]byte(`{"messages":[],"mission_id":"m-1"}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.MissionID != "m-1" {
		t.Fatalf("wire 字段 mission_id 未映射到 MissionID，实为 %q", req.MissionID)
	}
	// 反向：omitempty —— 普通对话不带任务字段，不给上游添无用字节
	b, _ := json.Marshal(aiChatReq{Messages: []aiMsg{}})
	if strings.Contains(string(b), "mission_id") {
		t.Errorf("空 mission_id 不该序列化出来：%s", b)
	}
}

// TestBuiltHTMLHasMissionTab 构建产物不许过期（改 src 忘跑 web_build.py 立刻红）。
func TestBuiltHTMLHasMissionTab(t *testing.T) {
	htmlB, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 web/index.html: %v", err)
	}
	html := string(htmlB)
	for _, want := range []string{
		`data-tab="missions"`,  // tab 按钮
		`ai-pane-missions`,     // 面板容器
		"aiMissionsLoad",       // 渲染入口
		"body.mission_id",      // 绑定注入（aiConsume）
		"/api/missions/events", // SSE 数据源
	} {
		if !strings.Contains(html, want) {
			t.Errorf("web/index.html 缺 %q —— 改了 web/src 后没跑 python tools/web_build.py？", want)
		}
	}
}
