package main

// audit_test.go —— 审计查询的单测
//
// 审计此前只写不读（没有 UI、没有接口，`auditTail` 甚至没人调用）。
// 加了查看页之后，查询的正确性就成了必须钉住的东西：
// 顺序错了会让人把"故障"看成"最后发生的事"，过滤错了会让人以为 AI 没动过手。

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAuditFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatalf("写审计文件失败：%v", err)
	}
}

// withAuditDir 把审计目录指向临时目录，避免污染用户真实的审计记录。
func withAuditDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		writeAuditFile(t, dir, name, body)
	}
	old := auditDir
	auditDir = func() string { return dir }
	t.Cleanup(func() { auditDir = old })
	return dir
}

const (
	auditDayOld = `{"ts":"2026-10-01 10:00:00.000","actor":"user","action":"apply_sql","target":"farm","result":"ok","detail":"建表"}` + "\n" +
		`{"ts":"2026-10-01 11:00:00.000","actor":"ai","action":"db_backup","target":"farm","result":"fail","detail":"连接超时"}` + "\n"
	auditDayNew = `{"ts":"2026-10-03 09:00:00.000","actor":"guard","action":"chain_autorecover","target":"111.x","result":"ok","verify":"已复验通过：共识在推进"}` + "\n" +
		`{"ts":"2026-10-03 10:00:00.000","actor":"user","action":"config_save","target":"config","result":"ok"}` + "\n" +
		`{"ts":"2026-10-03 11:00:00.000","actor":"system","action":"diag_export","target":"d.zip","result":"started","dur_ms":120}` + "\n" +
		`{"ts":"2026-10-03 11:30:00.000","actor":"ai","action":"start_service","target":"web","result":"ok","verify":"已复验通过：端口+HTTP"}` + "\n"
)

func setupAuditFiles(t *testing.T) string {
	return withAuditDir(t, map[string]string{
		"audit-20261001.jsonl": auditDayOld,
		"audit-20261003.jsonl": auditDayNew,
		"audit-20260922.log":   "不是审计文件，不该被收进来\n",
	})
}

func actions(es []AuditEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Action)
	}
	return out
}

// TestAuditQueryAcrossDaysOrder 跨天合并必须严格按时间倒序：
// 顺序错了，"最后发生了什么"就全错了。
func TestAuditQueryAcrossDaysOrder(t *testing.T) {
	setupAuditFiles(t)
	got := actions(auditQuery("", "", 0))
	want := []string{"start_service", "diag_export", "config_save", "chain_autorecover", "db_backup", "apply_sql"}
	if len(got) != len(want) {
		t.Fatalf("条数不对：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条应为 %s，实际 %s（全序：%v）", i, want[i], got[i], got)
		}
	}
}

// TestAuditQueryFilterActor 按主体过滤：用来回答"AI 到底动过什么"。
func TestAuditQueryFilterActor(t *testing.T) {
	setupAuditFiles(t)
	got := actions(auditQuery("", "ai", 0))
	want := []string{"start_service", "db_backup"} // 跨天，新在前
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("AI 过滤结果应为 %v，实际 %v", want, got)
	}
	if g := actions(auditQuery("", "guard", 0)); len(g) != 1 || g[0] != "chain_autorecover" {
		t.Fatalf("守护过滤结果不对：%v", g)
	}
	if g := auditQuery("", "nobody", 0); len(g) != 0 {
		t.Fatalf("不存在的主体应返回空，实际 %v", g)
	}
}

// TestAuditQuerySingleDay 指定某天时只看那天，别把别的天混进来。
func TestAuditQuerySingleDay(t *testing.T) {
	setupAuditFiles(t)
	got := actions(auditQuery("20261001", "", 0))
	want := []string{"db_backup", "apply_sql"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("单日查询应为 %v，实际 %v", want, got)
	}
}

// TestAuditQueryLimit TakesNewest n 限制取的是"最新的 n 条"，不是最旧的——
// 后者会让页面永远显示最无关的历史。
func TestAuditQueryLimitTakesNewest(t *testing.T) {
	setupAuditFiles(t)
	got := actions(auditQuery("", "", 2))
	want := []string{"start_service", "diag_export"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("取最新 2 条应为 %v，实际 %v", want, got)
	}
}

// TestAuditQueryEmpty 没有审计文件时返回空数组而不是 null：
// 前端 `d.entries || []` 能兜住，但 null 序列化成 JSON 是 null，接口契约上应当给 []。
func TestAuditQueryEmpty(t *testing.T) {
	withAuditDir(t, nil)
	got := auditQuery("", "", 0)
	if got == nil {
		t.Fatal("空结果必须返回非 nil 切片（序列化为 []）")
	}
	if len(got) != 0 {
		t.Fatalf("应为空，实际 %v", got)
	}
	if d := auditDays(); len(d) != 0 {
		t.Fatalf("空目录不该列出日期，实际 %v", d)
	}
}

// TestAuditDaysIgnoresOtherFiles 只认 audit-YYYYMMDD.jsonl，别把日志/配置捞进来。
func TestAuditDaysIgnoresOtherFiles(t *testing.T) {
	withAuditDir(t, map[string]string{
		"audit-20261001.jsonl": auditDayOld,
		"audit-20261003.jsonl": auditDayNew,
		"envkit-20261003.log":  "log\n",
		"config.json":          "{}\n",
		"audit-notes.txt":      "随手记的\n",
	})
	got := auditDays()
	want := []string{"20261003", "20261001"} // 倒序
	if len(got) != len(want) {
		t.Fatalf("日期列表应为 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("日期顺序应为 %v，实际 %v", want, got)
		}
	}
}

// TestAuditQuerySkipsBrokenLines 文件被人为编辑坏了几行时，剩下的仍要能看——
// 查看页因为一条坏记录整个打不开是不可接受的。
func TestAuditQuerySkipsBrokenLines(t *testing.T) {
	withAuditDir(t, map[string]string{
		"audit-20261003.jsonl": "not json at all\n" +
			`{"ts":"2026-10-03 10:00:00.000","actor":"user","action":"ok_one","result":"ok"}` + "\n" +
			"\n" +
			`{"ts":"broken` + "\n",
	})
	got := actions(auditQuery("", "", 0))
	if len(got) != 1 || got[0] != "ok_one" {
		t.Fatalf("坏行应被跳过且保留好行，实际 %v", got)
	}
}

// TestAuditQueryKeepsVerifyField 复验结论是 P2 刚加的字段，查看页靠它展示
// "验没验、验出什么"——查询时不能把它丢了。
func TestAuditQueryKeepsVerifyField(t *testing.T) {
	setupAuditFiles(t)
	for _, e := range auditQuery("", "", 0) {
		if e.Action == "start_service" && e.Verify == "" {
			t.Fatal("复验结论（verify 字段）必须保留，否则查看页看不到验了什么")
		}
		if e.Action == "diag_export" && e.DurMs != 120 {
			t.Fatalf("耗时字段应保留，实际 %d", e.DurMs)
		}
	}
}
