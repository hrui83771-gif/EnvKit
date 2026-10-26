package main

// mission_api_test.go —— T4：行动 API / 入站触发 / SSE。
//
// 验收 3：发一条 POST /api/trigger → pending 出现 →（批准执行在 ai_mission 测）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// withMissionAPIEnv 一次备齐：行动文件 + 审计目录 + 配置还原。
func withMissionAPIEnv(t *testing.T) func() {
	t.Helper()
	undoMission := withMissionEnv(t)
	withAuditDir(t, nil) // 自带 t.Cleanup 还原
	oldCfg := cfg
	return func() {
		cfg = oldCfg
		undoMission()
	}
}

func doReq(h http.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/missions", rd)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func decodeOK(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON：%s", rec.Body.String())
	}
	return out
}

func TestHandleMissionsList(t *testing.T) {
	defer withMissionAPIEnv(t)()

	rec := doReq(handleMissions, http.MethodGet, "")
	out := decodeOK(t, rec)
	if out["ok"] != true {
		t.Fatalf("列表应 ok：%v", out)
	}
	if _, ok := out["missions"].([]any); !ok {
		t.Fatalf("missions 应为数组：%T", out["missions"])
	}

	// 创建一条后列表能读到
	rec = doReq(handleMissions, http.MethodPost, `{"op":"create","goal":"巡检磁盘"}`)
	out = decodeOK(t, rec)
	if out["ok"] != true {
		t.Fatalf("create 失败：%v", out)
	}
	m := out["mission"].(map[string]any)
	if m["state"] != msPending || m["goal"] != "巡检磁盘" {
		t.Fatalf("新建行动字段不符：%v", m)
	}
	rec = doReq(handleMissions, http.MethodGet, "")
	out = decodeOK(t, rec)
	if got := out["missions"].([]any); len(got) != 1 {
		t.Fatalf("列表应有 1 条，实为 %d", len(got))
	}
}

func TestHandleMissionsOps(t *testing.T) {
	defer withMissionAPIEnv(t)()

	// create → pause → resume → cancel 全链路
	rec := doReq(handleMissions, http.MethodPost, `{"op":"create","goal":"g"}`)
	id := decodeOK(t, rec)["mission"].(map[string]any)["id"].(string)

	op := func(body string) map[string]any {
		return decodeOK(t, doReq(handleMissions, http.MethodPost, body))
	}
	if out := op(`{"op":"pause","id":"` + id + `"}`); out["ok"] != true {
		t.Fatalf("pause 失败：%v", out)
	}
	if m, _ := missionGet(id); m.State != msPaused || m.PauseWhy == "" {
		t.Fatalf("pause 后应为 paused 且带原因：%s / %q", m.State, m.PauseWhy)
	}
	// paused 可 resume（只校验，不改状态——执行入口唯一）
	if out := op(`{"op":"resume","id":"` + id + `"}`); out["ok"] != true {
		t.Fatalf("resume 失败：%v", out)
	}
	if m, _ := missionGet(id); m.State != msPaused {
		t.Fatalf("resume 不该改状态，实为 %s", m.State)
	}
	// cancel → 终态
	if out := op(`{"op":"cancel","id":"` + id + `"}`); out["ok"] != true {
		t.Fatalf("cancel 失败：%v", out)
	}
	if m, _ := missionGet(id); m.State != msCanceled {
		t.Fatalf("cancel 后应为 canceled，实为 %s", m.State)
	}
	// 终态不可再 resume / pause
	if out := op(`{"op":"resume","id":"` + id + `"}`); out["ok"] == true {
		t.Fatalf("终态不该 resume 成功：%v", out)
	}
	if out := op(`{"op":"pause","id":"` + id + `"}`); out["ok"] == true {
		t.Fatalf("终态不该 pause 成功：%v", out)
	}
	// 非法 op / 不存在的 id
	if out := op(`{"op":"explode"}`); out["ok"] == true {
		t.Fatalf("非法 op 该报错")
	}
	if out := op(`{"op":"pause","id":"m-nope"}`); out["ok"] == true {
		t.Fatalf("不存在的 id 该报错")
	}
	// 空目标
	if out := op(`{"op":"create","goal":"  "}`); out["ok"] == true {
		t.Fatalf("空目标该报错")
	}
	// 非 POST
	if rec := doReq(handleMissions, http.MethodDelete, ""); rec.Code != 405 {
		t.Fatalf("非 GET/POST 应 405，实为 %d", rec.Code)
	}
}

func TestHandleMissionTrigger(t *testing.T) {
	defer withMissionAPIEnv(t)()

	// 默认关（红线 3）：403 语义的 ok:false
	rec := doReq(handleMissionTrigger, http.MethodPost, `{"kind":"webhook","title":"磁盘告警"}`)
	out := decodeOK(t, rec)
	if out["ok"] != false || !strings.Contains(out["error"].(string), "未开启") {
		t.Fatalf("默认关时应拒绝：%v", out)
	}
	if list := missionList(); len(list) != 0 {
		t.Fatalf("被拒绝的触发不该产生行动")
	}

	// 开启后：产出 pending（等人批，红线 2）
	cfg.Mission.InboundHook = true
	rec = doReq(handleMissionTrigger, http.MethodPost,
		`{"kind":"webhook","title":"磁盘告警","detail":"/ 分区 92%"}`)
	out = decodeOK(t, rec)
	if out["ok"] != true {
		t.Fatalf("开启后应成功：%v", out)
	}
	m := out["mission"].(map[string]any)
	if m["state"] != msPending {
		t.Fatalf("触发只产出 pending，实为 %v", m["state"])
	}
	tr := m["trigger"].(map[string]any)
	if tr["kind"] != "webhook" || !strings.Contains(tr["detail"].(string), "92%") {
		t.Fatalf("trigger 字段不符：%v", tr)
	}

	// 校验：空 title / 非法 kind / 非 POST
	if out := decodeOK(t, doReq(handleMissionTrigger, http.MethodPost, `{"title":"  "}`)); out["ok"] == true {
		t.Fatalf("空 title 该报错")
	}
	if out := decodeOK(t, doReq(handleMissionTrigger, http.MethodPost,
		`{"kind":"telepathy","title":"x"}`)); out["ok"] == true {
		t.Fatalf("非法 kind 该报错")
	}
	if rec := doReq(handleMissionTrigger, http.MethodGet, ""); rec.Code != 405 {
		t.Fatalf("GET 应 405，实为 %d", rec.Code)
	}
	// kind 留空默认 webhook
	if out := decodeOK(t, doReq(handleMissionTrigger, http.MethodPost, `{"title":"默认种类"}`)); out["ok"] != true {
		t.Fatalf("kind 留空应默认 webhook：%v", out)
	}
}

// sseCapWriter 记录 SSE 写入，用 channel 通知，避免读写竞争。
type sseCapWriter struct {
	mu     sync.Mutex
	header http.Header
	ch     chan string
}

func (w *sseCapWriter) Header() http.Header  { return w.header }
func (w *sseCapWriter) WriteHeader(code int) {}
func (w *sseCapWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case w.ch <- string(p):
	default:
	}
	return len(p), nil
}
func (w *sseCapWriter) Flush() {}

func TestHandleMissionEvents(t *testing.T) {
	defer withMissionAPIEnv(t)()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/missions/events", nil).WithContext(ctx)
	w := &sseCapWriter{header: http.Header{}, ch: make(chan string, 8)}
	done := make(chan struct{})
	go func() {
		handleMissionEvents(w, req)
		close(done)
	}()

	// 首帧快照必须立刻到
	select {
	case first := <-w.ch:
		if !strings.Contains(first, `"type":"missions"`) || !strings.Contains(first, `"missions":[`) {
			t.Fatalf("首帧格式不符：%q", first)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("2s 内没收到首帧快照")
	}

	// 变更通知 → 增量帧
	_, _ = missionNew("SSE 测试目标", Trigger{Kind: "manual"})
	notifyMissions()
	select {
	case next := <-w.ch:
		if !strings.Contains(next, "SSE 测试目标") {
			t.Fatalf("变更帧应包含新行动：%q", next)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("变更后 2s 内没收到新帧")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("ctx 取消后 handler 应退出")
	}
}
