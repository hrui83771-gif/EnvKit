package main

// stats_test.go —— 看板统计的口径守卫
//
// ## 为什么这些断言要这么严
//
// 看板数字一旦口径错了，画得再好看也是**一条与事实相反的信号**。
// 与其事后发现"这个数看着不对"，不如把口径钉在测试里。
//
// 重点保护三条：
//
//  1. **无样本 ≠ 0** ——没数据报0 会让"零失败"和"没测到"看起来一样
//  2. **分桶之和 == 合计** —— 分桶画图、合计显示，两者不一致就是算错了
//  3. **分母不同不可平均** —— 复验率的分母是「有复验的次数」，
//     不是操作总数；混进去会让比率失去意义

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withStatsDirs 把审计与轨迹都重定向到临时目录。
//
// 返回恢复函数。**测试绝不能写用户真实目录** ——
// 一次全量跑就把用户的审计与轨迹污染了，而看板数字会跟着变。
func withStatsDirs(t *testing.T) (string, string, func()) {
	t.Helper()
	adir := t.TempDir()
	tdir := t.TempDir()

	oldAuditDir, oldTraceDir := auditDir, traceDir
	oldAuditFile, oldTraceFile := auditFile, traceFile
	auditDir = func() string { return adir }
	traceDir = func() string { return tdir }
	auditFile, traceFile = nil, nil
	traceIDHolder.Store("")
	traceMu.Lock()
	traceCur, traceOutcomeCur = nil, nil
	traceMu.Unlock()

	return adir, tdir, func() {
		auditDir, traceDir = oldAuditDir, oldTraceDir
		auditFile, traceFile = oldAuditFile, oldTraceFile
		traceIDHolder.Store("")
		traceMu.Lock()
		traceCur, traceOutcomeCur = nil, nil
		traceMu.Unlock()
	}
}

func writeLine(t *testing.T, dir, name, line string) {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("写不了 %s: %v", p, err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// seedStats 造一份可手算的数据：3 天、结果四态齐全、1 条轨迹带 token。
func seedStats(t *testing.T, adir, tdir string) {
	t.Helper()
	day := time.Now().Format("20060102")
	prev := time.Now().AddDate(0, 0, -1).Format("20060102")

	// 今天：ok 2 / fail 1 / denied 1/ started 1
	recs := []AuditEntry{
		{Ts: "x", Actor: actUser, Action: "env_check", Result: resOK},
		{Ts: "x", Actor: actAI, Action: "get_logs", Result: resOK, DurMs: 100},
		{Ts: "x", Actor: actAI, Action: "db_backup", Result: resFail, DurMs: 900},
		{Ts: "x", Actor: actAI, Action: "db_restore", Result: resDenied},
		{Ts: "x", Actor: actUser, Action: "web_start", Result: resStart},
	}
	for i, e := range recs {
		e.Ts = time.Now().Format("2006-01-02") + " 10:0" + strconv.Itoa(i) + ":00.000"
		b, _ := json.Marshal(e)
		writeLine(t, adir, "audit-"+day+".jsonl", string(b))
	}

	// 昨天：一条带复验结论的（通过）
	e := AuditEntry{
		Ts: "x", Actor: actAI, Action: "verify_service", Result: resOK,
		Verify: "已复验通过：服务就绪", DurMs: 200,
	}
	b, _ := json.Marshal(e)
	writeLine(t, adir, "audit-"+prev+".jsonl", string(b))

	// 前天：一条复验未通过
	e2 := AuditEntry{
		Ts: "x", Actor: actAI, Action: "verify_service", Result: resOK,
		Verify: "未复验：端口被其他进程占用", DurMs: 300,
	}
	b2, _ := json.Marshal(e2)
	writeLine(t, adir, "audit-"+time.Now().AddDate(0, 0, -2).Format("20060102")+".jsonl", string(b2))

	// 一条轨迹：success + 带 token
	tr := Trace{
		TraceID: "t-test-1", Actor: actAI,
		Started: time.Now().Format("2006-01-02 15:04:05.000"),
		Ended:   time.Now().Format("2006-01-02 15:04:06.000"),
		Outcome: outSuccess, Verified: true,
		Usage: &TraceUsage{Prompt: 1000, Completion: 200, HasCache: true, Hit: 600, Miss: 400},
	}
	tb, _ := json.Marshal(tr)
	writeLine(t, tdir, "trace-"+day+".jsonl", string(tb))
}

// ===== 断言 =====

// TestStatsBucketsSumToTotals 分桶之和必须等于合计。
//
// 这条最关键：**看板画的是分桶、显示的是合计**，
// 两者不一致就意味着图和数字在讲不同的故事。
func TestStatsBucketsSumToTotals(t *testing.T) {
	adir, tdir, restore := withStatsDirs(t)
	defer restore()
	seedStats(t, adir, tdir)

	b := statsRange(30)
	var sumOps, sumOK, sumFail, sumDenied int
	for _, d := range b.Days {
		sumOps += d.Ops
		sumOK += d.OK
		sumFail += d.Fail
		sumDenied += d.Denied
	}
	if sumOps != b.Totals.Ops {
		t.Errorf("分桶 ops 之和 %d != totals %d", sumOps, b.Totals.Ops)
	}
	if sumOK != b.Totals.OK {
		t.Errorf("分桶 ok 之和 %d != totals %d", sumOK, b.Totals.OK)
	}
	if sumFail != b.Totals.Fail {
		t.Errorf("分桶 fail 之和 %d != totals %d", sumFail, b.Totals.Fail)
	}
	if sumDenied != b.Totals.Denied {
		t.Errorf("分桶 denied 之和 %d != totals %d", sumDenied, b.Totals.Denied)
	}
	if b.Totals.Ops != 7 {
		t.Errorf("总操作数应为 7（5+1+1），实际 %d", b.Totals.Ops)
	}
}

// TestStatsCountsAllFourResults 四态都要分开统计。
//
// **denied 不能算 fail** —— 它是权限裁决器在派发前终止，属预期行为。
// 混在一起会让「失败率」凭空高一截，而那不是失败。
func TestStatsCountsAllFourResults(t *testing.T) {
	adir, tdir, restore := withStatsDirs(t)
	defer restore()
	seedStats(t, adir, tdir)

	b := statsRange(30)
	tot := b.Totals
	if tot.OK != 4 {
		t.Errorf("ok 应为 4（今天2 + 昨天1 + 前天1），实际 %d", tot.OK)
	}
	if tot.Fail != 1 {
		t.Errorf("fail 应为 1，实际 %d", tot.Fail)
	}
	if tot.Denied != 1 {
		t.Errorf("denied 应为 1，实际 %d", tot.Denied)
	}
	if tot.Started != 1 {
		t.Errorf("started 应为 1，实际 %d", tot.Started)
	}
	// 全部相加必须等于总数——少算一个就说明漏了一态
	if tot.OK+tot.Fail+tot.Denied+tot.Started != tot.Ops {
		t.Errorf("四态之和 %d != 总数 %d，说明漏算了某一态",
			tot.OK+tot.Fail+tot.Denied+tot.Started, tot.Ops)
	}
}

// TestStatsVerifyRateDenominator 分母是「有复验的次数」，不是操作总数。
//
// 造的数据：7 次操作，只有 2 次带复验结论（1 通过 1 未通过）。
// 正确答案是 1/2 = 0.5；若错用操作总数做分母会得到 1/7≈0.143。
func TestStatsVerifyRateDenominator(t *testing.T) {
	adir, tdir, restore := withStatsDirs(t)
	defer restore()
	seedStats(t, adir, tdir)

	b := statsRange(30)
	if b.Totals.VerifyCount != 2 {
		t.Fatalf("有复验结论的操作应�� 2，实际 %d", b.Totals.VerifyCount)
	}
	if b.Totals.VerifyOK != 1 {
		t.Errorf("复验通过应为 1，实际 %d", b.Totals.VerifyOK)
	}
	if !b.Totals.VerifyRateHasSample {
		t.Fatal("有样本时 has_sample 应为 true")
	}
	if b.Totals.VerifyRate < 0.49 || b.Totals.VerifyRate > 0.51 {
		t.Errorf("复验率应≈0.5（分母是有复验的次数），实际 %v", b.Totals.VerifyRate)
	}
}

// TestStatsNoSampleIsMinusOne 无样本时必须报 -1 而不是 0。
//
// **「没测到」与「测到是 0」是两件事。** 报0 会让
// 「零失败」看起来像结论，而它其实什么都没测。
func TestStatsNoSampleIsMinusOne(t *testing.T) {
	// 故意**不写任何数据** —— 这个测试验的就是「什么都没有」时的表现
	_, _, restore := withStatsDirs(t)
	defer restore()
	b := statsRange(30)
	if b.Totals.VerifyRateHasSample {
		t.Error("无数据时 has_sample 应为 false")
	}
	if b.Totals.VerifyRate != statNoSample {
		t.Errorf("无样本时复验率应为 %v（-1），实际 %v —— 报 0 等于把「没测到」说成「测到是 0」",
			statNoSample, b.Totals.VerifyRate)
	}
	if b.Totals.TokenSamples != 0 {
		t.Errorf("无数据时 token 样本数应为 0，实际 %d", b.Totals.TokenSamples)
	}
	// 应当给出提醒
	found := false
	for _, s := range b.Notices {
		if strings.Contains(s, "无样本") || strings.Contains(s, "无数据") {
			found = true
		}
	}
	if !found {
		t.Errorf("无样本时应给出提醒，实际 notices = %v", b.Notices)
	}
}

// TestStatsTokenFromTrace token 只能来自轨迹的 usage 字段。
func TestStatsTokenFromTrace(t *testing.T) {
	adir, tdir, restore := withStatsDirs(t)
	defer restore()
	seedStats(t, adir, tdir)

	b := statsRange(30)
	if b.Totals.TokenSamples != 1 {
		t.Errorf("带 usage 的轨迹应为 1，实际 %d", b.Totals.TokenSamples)
	}
	if b.Totals.TokenPrompt != 1000 || b.Totals.TokenCompletion != 200 {
		t.Errorf("token = %d/%d，期望 1000/200",
			b.Totals.TokenPrompt, b.Totals.TokenCompletion)
	}
	if b.Totals.TokenTotal != 1200 {
		t.Errorf("token 合计应�� 1200，实际 %d", b.Totals.TokenTotal)
	}
	if !b.Totals.TokenHasCache {
		t.Error("HasCache 应为 true")
	}
	if b.Totals.TokenCacheHit != 600 {
		t.Errorf("缓存命中应为 600，实际 %d", b.Totals.TokenCacheHit)
	}
}

// TestStatsTraceOutcomes 轨迹结局要分档统计，不能合成一个数。
func TestStatsTraceOutcomes(t *testing.T) {
	adir, tdir, restore := withStatsDirs(t)
	defer restore()
	seedStats(t, adir, tdir)

	b := statsRange(30)
	if b.Totals.Traces != 1 {
		t.Fatalf("轨迹数应为 1，实际 %d", b.Totals.Traces)
	}
	if b.Totals.TracesSuccess != 1 {
		t.Errorf("success 应为 1，实际 %d", b.Totals.TracesSuccess)
	}
	// 分档之和必须等于总数
	if b.Totals.TracesSuccess+b.Totals.TracesFailed+
		b.Totals.TracesPartial+b.Totals.TracesAborted != b.Totals.Traces {
		t.Error("轨迹四档之和 != 总数，说明漏算了某一档")
	}
}

// TestStatsDaysOldestFirst 天数必须旧→新（画图友好）。
func TestStatsDaysOldestFirst(t *testing.T) {
	adir, tdir, restore := withStatsDirs(t)
	defer restore()
	seedStats(t, adir, tdir)

	b := statsRange(30)
	if len(b.Days) != 30 {
		t.Fatalf("days 应为 30 桶，实际 %d", len(b.Days))
	}
	for i := 1; i < len(b.Days); i++ {
		if b.Days[i].Date < b.Days[i-1].Date {
			t.Fatalf("days 必须旧→新，第 %d 项 %s < 第 %d 项 %s",
				i, b.Days[i].Date, i-1, b.Days[i-1].Date)
		}
	}
}

// TestStatsEmptyDaysAreZeroButTotalsHonest 空的日期桶必须是 0，
// 但不能因此说「无样本」。
func TestStatsEmptyDaysAreZeroButTotalsHonest(t *testing.T) {
	adir, tdir, restore := withStatsDirs(t)
	defer restore()
	seedStats(t, adir, tdir)

	b := statsRange(30)
	empty := 0
	for _, d := range b.Days {
		if d.Ops == 0 {
			empty++
		}
	}
	if empty == 0 {
		t.Error("造的数据只覆盖 3 天，30 桶里应有 27 个空桶")
	}
	// 空桶的 map 不能是 nil——前端直接遍历
	for _, d := range b.Days {
		if d.ByActor == nil || d.ByResult == nil {
			t.Fatal("空桶的 ByActor/ByResult 不能为 nil（前端直接遍历会报错）")
		}
	}
}

// TestHandleStatsDaysParam days 参数要生效，且非法值要退回默认。
func TestHandleStatsDaysParam(t *testing.T) {
	adir, tdir, restore := withStatsDirs(t)
	defer restore()
	seedStats(t, adir, tdir)

	req := httptest.NewRequest(http.MethodGet, "/api/stats?days=7", nil)
	rec := httptest.NewRecorder()
	handleStats(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP %d", rec.Code)
	}
	var b StatsBoard
	if err := json.NewDecoder(rec.Body).Decode(&b); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(b.Days) != 7 {
		t.Errorf("days=7 应返回 7 桶，实际 %d", len(b.Days))
	}

	// 非法值必须退回默认而不是崩掉或返回 0 桶
	for _, bad := range []string{"", "abc", "-5", "0"} {
		req := httptest.NewRequest(http.MethodGet, "/api/stats?days="+bad, nil)
		rec := httptest.NewRecorder()
		handleStats(rec, req)
		if rec.Code != 200 {
			t.Errorf("days=%q 不该报错，HTTP %d", bad, rec.Code)
		}
		var bb StatsBoard
		if json.NewDecoder(rec.Body).Decode(&bb) != nil {
			t.Errorf("days=%q 响应不是合法 JSON", bad)
			continue
		}
		if len(bb.Days) != 30 {
			t.Errorf("days=%q 应退回默认 30 桶，实际 %d", bad, len(bb.Days))
		}
	}
}

// TestStatsDoesNotWriteAudit 看板只读，不该污染审计数据。
func TestStatsDoesNotWriteAudit(t *testing.T) {
	adir, tdir, restore := withStatsDirs(t)
	defer restore()
	seedStats(t, adir, tdir)

	before := statsRange(30)
	_ = statsRange(30)
	_ = statsRange(30)
	after := statsRange(30)
	if before.Totals.Ops != after.Totals.Ops {
		t.Errorf("反复调用看板不应改变操作数：%d -> %d",
			before.Totals.Ops, after.Totals.Ops)
	}
}
