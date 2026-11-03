package main

// trace_api_test.go —— 轨迹查询接口的判据
//
// 这里刻意测**纯函数**（traceRowOf / traceMatches / traceStatsOf）与
// "过滤先于截断"这一条顺序性质。后者是真会犯错的地方：把截断放在过滤前面，
// 表现为"搜一个三天前才出现过一次的动作，永远搜不到"，
// 而本机数据恰好都匹配时，任何断言都不会红。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkTrace(id, goal, outcome string, steps ...TraceStep) Trace {
	return Trace{
		TraceID: id, Goal: goal, Actor: actAI, Outcome: outcome,
		Started: "2026-11-01 10:00:00.000", Ended: "2026-11-01 10:00:08.000",
		Steps: steps,
	}
}

func st(phase, action string, result ...string) TraceStep {
	r := ""
	if len(result) > 0 {
		r = result[0]
	}
	return TraceStep{Phase: phase, Actor: actAI, Action: action, Result: r}
}

func TestTraceRowOfCountsPhases(t *testing.T) {
	tr := mkTrace("t-1", "启动后端", outSuccess,
		st(phPlan, "plan"),
		st(phAction, "get_system_state", resOK),
		st(phAction, "start_service", resFail),
		st(phFailure, "start_service", resFail),
		st(phVerify, "verify_environment", resOK),
		st(phHuman, "human_intervention", resOK),
		st(phPolicy, "start_service", "confirmed"),
	)
	r := traceRowOf(&tr)
	if r.AICalls != 2 || r.AIFails != 1 {
		t.Fatalf("AI 调用/失败计数不对：calls=%d fails=%d", r.AICalls, r.AIFails)
	}
	if r.Verifies != 1 || r.Humans != 1 || r.Policies != 1 {
		t.Fatalf("环节计数不对：verify=%d human=%d policy=%d", r.Verifies, r.Humans, r.Policies)
	}
	if r.Steps != 7 {
		t.Fatalf("步数应为 7，实际 %d", r.Steps)
	}
	if r.DurMs != 8000 {
		t.Fatalf("耗时应为 8000ms，实际 %d", r.DurMs)
	}
	if r.Summary == "" {
		t.Fatal("列表行应当带上与 traceSummary 同源的摘要")
	}
}

func TestTraceDurMsToleratesGarbage(t *testing.T) {
	if got := traceDurMs("不是时间", "2026-11-01 10:00:08.000"); got != 0 {
		t.Fatalf("解析失败应返回 0，实际 %d", got)
	}
	// 结束早于开始（时钟回拨）也要给 0，不能出现负耗时
	if got := traceDurMs("2026-11-01 10:00:08.000", "2026-11-01 10:00:00.000"); got != 0 {
		t.Fatalf("倒挂的时间应返回 0，实际 %d", got)
	}
	if got := traceDurMs("2026-11-01 10:00:00.000", ""); got != 0 {
		t.Fatalf("未结束的轨迹应返回 0，实际 %d", got)
	}
}

func TestTraceMatches(t *testing.T) {
	tr := mkTrace("t-abc123", "帮我把后端重启一下", outFailed,
		st(phAction, "restart_service"),
		st(phFailure, "restart_service", resFail),
	)

	// 关键词应能搜到**动作名**——用户记得的是"它反复重启后端"，不是 trace_id
	if !traceMatches(&tr, traceFilter{Q: "restart_service"}, "restart_service") {
		t.Fatal("关键词应命中动作名")
	}
	if !traceMatches(&tr, traceFilter{Q: "abc123"}, "abc123") {
		t.Fatal("关键词应命中 trace_id")
	}
	if !traceMatches(&tr, traceFilter{Q: "后端"}, "后端") {
		t.Fatal("关键词应命中 goal")
	}
	if traceMatches(&tr, traceFilter{Q: "mysql"}, "mysql") {
		t.Fatal("不存在的关键词不该命中")
	}

	if !traceMatches(&tr, traceFilter{Outcome: outFailed}, "") {
		t.Fatal("按结局过滤应命中")
	}
	if traceMatches(&tr, traceFilter{Outcome: outSuccess}, "") {
		t.Fatal("结局不符时不该命中")
	}

	// onlybad：有 failure 步就算有问题
	if !traceMatches(&tr, traceFilter{OnlyBad: true}, "") {
		t.Fatal("有失败步的轨迹应被 onlybad 命中")
	}
	// **未复验不算问题**：问一句常识本来就不需要复验，
	// 把它算进异常会让"只看异常"里全是正常对话。
	plain := mkTrace("t-2", "区块链是什么", outSuccess, st(phAction, "get_system_state", resOK))
	if traceMatches(&plain, traceFilter{OnlyBad: true}, "") {
		t.Fatal("正常答完、没有失败的轨迹不该被判为异常")
	}

	// since：起点之后才留
	since := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	if traceMatches(&tr, traceFilter{Since: since}, "") {
		t.Fatal("11-01 的轨迹不该被 11-02 起的过滤留下")
	}
	before := time.Date(2026, 10, 31, 0, 0, 0, 0, time.Local)
	if !traceMatches(&tr, traceFilter{Since: before}, "") {
		t.Fatal("11-01 的轨迹应当被 10-31 起的过滤留下")
	}
}

func TestTraceStatsOf(t *testing.T) {
	// 空集：不能出现 0/0，也不能让界面把"没有数据"显示成"成功率 0%"
	z := traceStatsOf(nil)
	if z.Total != 0 || z.AvgSteps != 0 || z.CacheHitPct != 0 {
		t.Fatalf("空集应全零：%+v", z)
	}

	rows := []traceRow{
		{Outcome: outSuccess, Steps: 10, Verified: true, Usage: &TraceUsage{Prompt: 1000, Completion: 100, Hit: 900, Miss: 100, HasCache: true}},
		// 第二条刻意做成"命中率极低但体量大"：这样加权与算术平均会给出不同答案，
		// 这条断言才真的守得住口径。（第一版两条数据恰好让两种算法都得 45%，等于没测。）
		{Outcome: outFailed, Steps: 20, Usage: &TraceUsage{Prompt: 2000, Completion: 200, Hit: 10, Miss: 1990, HasCache: true}},
		{Outcome: outPartial, Steps: 30}, // 无 usage：不参与平均值，否则会被当 0 拉低
		{Outcome: outAborted, Steps: 40, Humans: 2},
	}
	s := traceStatsOf(rows)
	if s.Total != 4 || s.Success != 1 || s.Failed != 1 || s.Partial != 1 || s.Aborted != 1 {
		t.Fatalf("结局分布不对：%+v", s)
	}
	if s.Verified != 1 || s.Humans != 2 {
		t.Fatalf("复验/人工计数不对：verified=%d humans=%d", s.Verified, s.Humans)
	}
	// 平均步数对**全部**轨迹取平均：(10+20+30+40)/4 = 25
	if s.AvgSteps != 25 {
		t.Fatalf("平均步数应为 25，实际 %v", s.AvgSteps)
	}
	// 平均 token 只对**有数据**的两条取平均：(1000+2000)/2 = 1500
	if s.AvgPromp != 1500 || s.AvgCompl != 150 {
		t.Fatalf("平均 token 应为 1500/150，实际 %v/%v", s.AvgPromp, s.AvgCompl)
	}
	// 缓存命中率必须**按 token 加权**：命中 900+10=910，总读数 910+(100+1990)=3000
	// → 30.3%。若误用"各回合计中率的算术平均"会得到 (90%+0.5%)/2 = 45.3%。
	if s.CacheHitPct != 30.3 {
		t.Fatalf("缓存命中率应为按 token 加权的 30.3（算术平均会是 45.3），实际 %v", s.CacheHitPct)
	}
}

// writeTraceFile 把若干轨迹写成一个按天命名的轨迹文件。
func writeTraceFile(t *testing.T, dir, day string, trs ...Trace) {
	t.Helper()
	var buf []byte
	for _, tr := range trs {
		b, err := json.Marshal(tr)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(buf, append(b, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(dir, "trace-"+day+".jsonl"), buf, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTraceQueryFiltersBeforeTruncate 过滤必须先于截断。
//
// 构造：最近的一天里全是无关轨迹，唯一匹配的那条在更早的一天。
// 若实现先取满 n 条再过滤，这条会查不到 —— 而用户看到的解释只会是
// "搜不到"，没人会想到是顺序问题。
func TestTraceQueryFiltersBeforeTruncate(t *testing.T) {
	dir := t.TempDir()
	oldDir := traceDir
	traceDir = func() string { return dir }
	defer func() { traceDir = oldDir }()

	writeTraceFile(t, dir, "20260101",
		mkTrace("t-old", "部署失败排查", outFailed, st(phAction, "restart_service")))
	// 更新的一天：三条都与关键词无关
	writeTraceFile(t, dir, "20260103",
		mkTrace("t-new1", "看看日志", outSuccess, st(phAction, "get_logs")),
		mkTrace("t-new2", "看看日志", outSuccess, st(phAction, "get_logs")),
		mkTrace("t-new3", "看看日志", outSuccess, st(phAction, "get_logs")),
	)

	got := traceQueryFiltered(1, traceFilter{Q: "restart_service"})
	if len(got) != 1 {
		t.Fatalf("应当只返回 1 条，实际 %d 条", len(got))
	}
	if got[0].TraceID != "t-old" {
		t.Fatalf("应当命中更早那天的 t-old，实际 %q —— 说明截断发生在过滤之前", got[0].TraceID)
	}

	// 不带关键词时仍应"新的在前"
	all := traceQueryFiltered(2, traceFilter{})
	if len(all) != 2 || all[0].TraceID != "t-new3" {
		t.Fatalf("新在前的顺序不对：%+v", all)
	}
}

func TestHandleTraceOneMissingID(t *testing.T) {
	dir := t.TempDir()
	oldDir := traceDir
	traceDir = func() string { return dir }
	defer func() { traceDir = oldDir }()

	rec := httptest.NewRecorder()
	handleTraceOne(rec, httptest.NewRequest(http.MethodGet, "/api/trace", nil))
	if rec.Code != 400 {
		t.Fatalf("缺 id 应返回 400，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handleTraceOne(rec, httptest.NewRequest(http.MethodGet, "/api/trace?id=t-nope", nil))
	if rec.Code != 404 {
		t.Fatalf("找不到应返回 404，实际 %d", rec.Code)
	}
	// 文案必须说清"可能被保留期清掉了"，否则用户会以为功能坏了
	if !strings.Contains(rec.Body.String(), "保留期") {
		t.Fatalf("404 文案应说明可能已被清理，实际：%s", rec.Body.String())
	}
}

func TestHandleTracesShape(t *testing.T) {
	dir := t.TempDir()
	oldDir := traceDir
	traceDir = func() string { return dir }
	defer func() { traceDir = oldDir }()

	writeTraceFile(t, dir, "20260103",
		mkTrace("t-a", "启动后端", outSuccess, st(phAction, "start_service")),
		mkTrace("t-b", "启动前端", outFailed, st(phAction, "start_service"), st(phFailure, "start_service", resFail)),
	)

	rec := httptest.NewRecorder()
	handleTraces(rec, httptest.NewRequest(http.MethodGet, "/api/traces?n=10", nil))
	if rec.Code != 200 {
		t.Fatalf("应返回 200，实际 %d", rec.Code)
	}
	var out struct {
		OK     bool       `json:"ok"`
		Traces []traceRow `json:"traces"`
		Stats  traceStats `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if !out.OK || len(out.Traces) != 2 {
		t.Fatalf("应返回 2 条：%s", rec.Body.String())
	}
	if out.Stats.Total != 2 || out.Stats.Failed != 1 {
		t.Fatalf("统计口径不对：%+v", out.Stats)
	}

	// onlybad 过滤
	rec = httptest.NewRecorder()
	handleTraces(rec, httptest.NewRequest(http.MethodGet, "/api/traces?onlybad=1", nil))
	var only struct {
		Traces []traceRow `json:"traces"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &only)
	if len(only.Traces) != 1 || only.Traces[0].TraceID != "t-b" {
		t.Fatalf("onlybad 应只剩失败那条，实际 %+v", only.Traces)
	}
}
