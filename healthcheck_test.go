package main

// healthcheck_test.go —— 全链路体检的纯函数门禁。
//
// 只测「结论判定」与「导出内容」，不跑真实检测（会连数据库/链端/起子进程）。
// 变异验证做过的断言：
//   · deliverable = (fail==0) 改成 (fail==0 && skip==0) → TestSkipDoesNotBlock 必红
//   · hcSummarize 把 fail 计进 skip              → TestFailBlocksDelivery 必红
//   · hcAdd 去掉「跳过必须写原因」的兜底         → TestSkipReasonGuard 必红
//   · hcMarkdown 去掉只读声明                    → TestMarkdownStatesReadOnly 必红

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func hcItemOf(status, title, detail string) HCItem {
	return HCItem{ID: "t", Group: hcgEnv, Title: title, Status: status, Detail: detail}
}

func TestHCSummarizeAllPass(t *testing.T) {
	items := []HCItem{
		hcItemOf(hcPass, "组件齐全", "3 个组件都在"),
		hcItemOf(hcPass, "数据库可连", "MySQL 8.0"),
	}
	p, f, s, ok, concl := hcSummarize(items)
	if p != 2 || f != 0 || s != 0 || !ok {
		t.Fatalf("全通过应判可交付：pass=%d fail=%d skip=%d deliverable=%v", p, f, s, ok)
	}
	if !strings.Contains(concl, "全部检查通过") || !strings.Contains(concl, "2 项通过") {
		t.Fatalf("结论没写清统计与交付状态：%s", concl)
	}
}

func TestHCFailBlocksDelivery(t *testing.T) {
	items := []HCItem{
		hcItemOf(hcPass, "组件齐全", "3 个组件都在"),
		hcItemOf(hcFail, "MySQL 可连接", "连接失败：Access denied"),
		hcItemOf(hcSkip, "共识在推进", "WeBASE 不通，无法采样"),
	}
	p, f, s, ok, concl := hcSummarize(items)
	if p != 1 || f != 1 || s != 1 {
		t.Fatalf("计数错：pass=%d fail=%d skip=%d", p, f, s)
	}
	if ok {
		t.Fatal("有 fail 却判成可交付——这是最危险的假绿")
	}
	if !strings.Contains(concl, "先处理") || !strings.Contains(concl, "MySQL 可连接") {
		t.Fatalf("结论没点名要修哪一项：%s", concl)
	}
}

func TestHCSkipDoesNotBlock(t *testing.T) {
	// 未配链端/数据库：整段跳过。没查 ≠ 查过没问题，也不能因此判不可交付。
	items := []HCItem{
		hcItemOf(hcSkip, "链端主机可达", "未配置链端主机"),
		hcItemOf(hcSkip, "目标库存在", "未配置目标数据库"),
	}
	_, f, s, ok, concl := hcSummarize(items)
	if f != 0 || s != 2 || !ok {
		t.Fatalf("跳过不该构成否决：fail=%d skip=%d deliverable=%v", f, s, ok)
	}
	if strings.Contains(concl, "全部检查通过") {
		t.Fatalf("全跳过不许说成「全部检查通过」：%s", concl)
	}
	if !strings.Contains(concl, "未核实") || !strings.Contains(concl, "2 项跳过") {
		t.Fatalf("结论没如实写跳过数与未核实：%s", concl)
	}
}

func TestHCSummarizeManyFails(t *testing.T) {
	items := []HCItem{
		hcItemOf(hcFail, "A", ""), hcItemOf(hcFail, "B", ""),
		hcItemOf(hcFail, "C", ""), hcItemOf(hcFail, "D", ""),
	}
	_, f, _, ok, concl := hcSummarize(items)
	if f != 4 || ok || !strings.Contains(concl, "等 4 项") {
		t.Fatalf("失败项多时结论要收口并给总数：f=%d ok=%v concl=%s", f, ok, concl)
	}
}

func TestHCSkipReasonGuard(t *testing.T) {
	r := HCReport{Status: "running", Items: []HCItem{}}
	hcAdd(&r, hcgDB, "x", "没理由的跳过", hcSkip, "")
	got := r.Items[0].Detail
	if strings.TrimSpace(got) == "" || !strings.Contains(got, "未核实") {
		t.Fatalf("跳过项没写原因时必须兜底，不能留空：%q", got)
	}
}

func hcReportFixture() HCReport {
	return HCReport{
		Status: "done", StartedAt: "2026-11-07 18:40:00", FinishedAt: "2026-11-07 18:40:30",
		Version: "3.8.0", Deliverable: false,
		Conclusion: "1 项通过 / 1 项不通过 / 1 项跳过。暂不能交付，先处理：MySQL 可连接。",
		Items: []HCItem{
			{ID: "a", Group: hcgEnv, Title: "组件齐全", Status: hcPass, Detail: "3 个组件都在"},
			{ID: "b", Group: hcgDB, Title: "MySQL 可连接", Status: hcFail, Detail: "Access denied"},
			{ID: "c", Group: hcgChain, Title: "链端主机可达", Status: hcSkip, Detail: "未配置链端主机"},
		},
	}
}

func TestHCMarkdownCarriesConclusionAndEvidence(t *testing.T) {
	md := hcMarkdown(hcReportFixture())
	for _, want := range []string{
		"暂不能交付", "1 项通过", "3.8.0", "2026-11-07 18:40:00",
		"MySQL 可连接", "Access denied", "未配置链端主机", "[跳过]",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("导出缺少 %q：\n%s", want, md)
		}
	}
	// 结论必须在证据之前，否则「一页结论」又变成让人自己翻的长日志
	if strings.Index(md, "暂不能交付") > strings.Index(md, "## 环境与组件") {
		t.Fatal("结论没有放在报告最上面")
	}
}

func TestHCMarkdownStatesReadOnly(t *testing.T) {
	md := hcMarkdown(hcReportFixture())
	if !strings.Contains(md, "只读") || !strings.Contains(md, "未启动/重启服务") {
		t.Fatalf("导出必须写明体检的只读边界：\n%s", md)
	}
}

func TestHCExportRejectsUnfinishedReport(t *testing.T) {
	old := hcSnapshot()
	defer hcPublish(old)
	hcPublish(HCReport{Status: "running"})
	w := httptest.NewRecorder()
	handleHealthCheckExport(w, httptest.NewRequest("GET", "/api/healthcheck/export", nil))
	if w.Code != 409 {
		t.Fatalf("没跑完就导出应 409，实际 %d", w.Code)
	}
	hcPublish(hcReportFixture())
	w2 := httptest.NewRecorder()
	handleHealthCheckExport(w2, httptest.NewRequest("GET", "/api/healthcheck/export", nil))
	if w2.Code != 200 || !strings.Contains(w2.Body.String(), "暂不能交付") {
		t.Fatalf("完成后的导出应为 200 且含结论，实际 %d / %q", w2.Code, w2.Body.String())
	}
}

func TestHCHandlerGetNeverMutates(t *testing.T) {
	old := hcSnapshot()
	defer hcPublish(old)
	hcPublish(hcReportFixture())
	w := httptest.NewRecorder()
	handleHealthCheck(w, httptest.NewRequest("GET", "/api/healthcheck", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "deliverable") {
		t.Fatalf("GET 应返回报告 JSON，实际 %d / %q", w.Code, w.Body.String())
	}
	after := hcSnapshot()
	if after.Status != "done" || len(after.Items) != 3 {
		t.Fatalf("GET 不该改动报告：status=%s items=%d", after.Status, len(after.Items))
	}
}
