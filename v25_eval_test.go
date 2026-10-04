package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// v2.5 评测抓到的真缺陷：小于 1KB 的备份被报成「0 KB」
//
// 实测：注入的损坏备份只有 200 多字节，而 list_backups 用
// st.Size() / 1024 整除 → 0。AI 读到「大小 0 KB，文件是空的」，
// 它对文件内容的判断全建立在这个错数字上。
//
// **小文件恰恰是最该被认真检查的那一类**（截断的备份就是），
// 报成 0 KB 会让模型直接跳过内容检查。
// ---------------------------------------------------------------------------

// TestHumanSizeNeverReportsZeroForNonEmpty：非空文件不得被报成 0。
//
// 断言必须精确到「整个输出就是 0」，不能用 strings.Contains(got, "0")——
// "1.0 KB" / "1.0 MB" 里都有 "0"，那种写法会假失败。
func TestHumanSizeNeverReportsZeroForNonEmpty(t *testing.T) {
	cases := []struct {
		in      int64
		wantSub string
	}{
		{1, "字节"},
		{200, "字节"},
		{1023, "字节"},
		{1024, "KB"},
		{1536, "KB"},
		{1024 * 1024, "MB"},
	}
	for _, c := range cases {
		got := humanSize(c.in)
		// 唯一允许出现「0」的情形：真的是 0 字节
		if c.in > 0 && !strings.Contains(got, "字节") && strings.HasPrefix(got, "0") {
			t.Errorf("humanSize(%d) = %q：非空文件不得报成 0", c.in, got)
		}
		if !strings.Contains(got, c.wantSub) {
			t.Errorf("humanSize(%d) = %q，期望单位含 %q", c.in, got, c.wantSub)
		}
	}
	// 真的是 0 字节时如实报 0
	if got := humanSize(0); !strings.HasPrefix(got, "0") {
		t.Errorf("humanSize(0) = %q，应如实报 0", got)
	}
}

// 小文件必须能被看出「非空」——这是本次修复的核心。
func TestHumanSizeSmallFileNotZero(t *testing.T) {
	for _, n := range []int64{1, 200, 999} {
		got := humanSize(n)
		if got == "0 KB" || got == "0.0 KB" {
			t.Fatalf("humanSize(%d) = %q，会被读成空文件", n, got)
		}
		if !strings.Contains(got, "字节") {
			t.Errorf("humanSize(%d) = %q，小于 1KB 应给字节而不是 KB", n, got)
		}
	}
}

// list_backups 的输出里，小文件必须带真实字节数。
//
// 文件名带库名前缀是**必须的**：listBackups(db) 按 `db + "-"` 过滤。
// v2.5 评测踩到过——注入文件叫 `corrupt-injected.sql`（无前缀），
// 被过滤掉了，AI 看到的是别的残留文件，而判分仍判它"调查了备份"。
func TestListBackupsSmallFileReportsBytes(t *testing.T) {
	// ~200 字节 —— 评测实测里 AI 看到的就是这个量级
	body := strings.Repeat("-- filler line for a small dump\n", 7)
	if len(body) > 400 {
		t.Fatalf("测试样本应小于 1KB，实际 %d 字节", len(body))
	}
	defer withBackupDir(t, map[string]string{
		"farm-20260101-000000-injected-corrupt.sql": body,
	})()

	lst := listBackups("farm")
	if len(lst) != 1 {
		t.Fatalf("注入的小文件应出现在列表里，实际 %d 条 —— "+
			"若为 0 说明文件名没带库名前缀，被 listBackups 过滤掉了", len(lst))
	}
	if lst[0].SizeBytes != int64(len(body)) {
		t.Errorf("SizeBytes 应等于真实字节数 %d，实际 %d",
			len(body), lst[0].SizeBytes)
	}

	brief := backupsBrief("farm")
	if strings.Contains(brief, "0 KB") {
		t.Errorf("非空文件被报成 0 KB：%q", brief)
	}
	if !strings.Contains(brief, "字节") {
		t.Errorf("小于 1KB 的文件应报告字节数：%q", brief)
	}
}

// 注入的故障文件必须能被 list_backups 看到。
//
// 这条锁的是 v2.5 的一个隐蔽错位：注入器写了文件，AI 调工具却看不到它，
// 于是"AI 检查了备份"这句话与 AI 实际看到的东西无关——
// **装置注入的东西必须能被被测对象看到。**
func TestListBackupsIncludesInjectedFiles(t *testing.T) {
	defer withBackupDir(t, map[string]string{
		"farm-20260101-000000-injected-corrupt.sql": "-- dump\n" + strings.Repeat("x", 100),
	})()
	lst := listBackups("farm")
	if len(lst) != 1 {
		t.Fatalf("注入文件必须可见，实际列出 %d 条", len(lst))
	}
	if !strings.Contains(lst[0].Name, "injected") {
		t.Errorf("列出的应是被注入的那个文件，实际 %q", lst[0].Name)
	}
}

// ---------------------------------------------------------------------------
// v2.5 评测抓到的真缺陷：AI 分不清「曾成功过」与「从未成功」
//
// 沙箱kill_service 实测：AI 连查 7~8 轮，把「服务被杀后没人重启」
// 读成「反复启动失败」，从第一步就错，start_service 一次没调。
//
// 根因：快照的 services 只给 SvcInfo（全是"当前"状态，没有"历史"），
// 两种情况在 AI 眼里完全同形，只能靠猜。
// ---------------------------------------------------------------------------

// withSvcStubs 隔离服务运行态，返回还原函数。
//
// **必须隔离而不是就地改**：svcState / svcRT 都是包级单例，
// 测试之间共用会互相污染——而"AI 有没有拿到正确判据"这种断言
// 一旦读到别的测试留下的记录就会假绿。
func withSvcStubs(t *testing.T) func() {
	t.Helper()
	svcMu.Lock()
	oldState := svcState
	svcState = map[string]*SvcInfo{
		"web":     {Running: true, PID: 100, Since: "10:00:00"},
		"backend": {Running: false},
	}
	svcMu.Unlock()

	svcRT.Lock()
	oldRT := svcRT.m
	svcRT.m = map[string]*svcRuntimeRec{}
	svcRT.Unlock()

	return func() {
		svcMu.Lock()
		svcState = oldState
		svcMu.Unlock()
		svcRT.Lock()
		svcRT.m = oldRT
		svcRT.Unlock()
	}
}

// 曾复验通过 + 现在进程不在 → 必须明确告诉 AI「重新拉起」，
// 且**不能**让它去排查端口占用。
func TestSvcAIViewDistinguishesKilledFromNeverStarted(t *testing.T) {
	defer withSvcStubs(t)()

	// 场景 A：曾经成功过，现在被杀
	svcWith("backend", func(r *svcRuntimeRec) {
		r.Verified = true
		r.Conclusion = "已复验通过：后端已就绪：进程存活且端口 8888 在监听"
		r.VerifyAt = "2026-10-04 10:00:05"
	})
	killed := svcAIView("backend", SvcInfo{Running: false})

	// 场景 B：从未成功过
	svcWith("backend", func(r *svcRuntimeRec) {
		r.Verified = false
		r.Conclusion = "未复验：端口被占"
		r.VerifyAt = "2026-10-04 10:00:05"
	})
	never := svcAIView("backend", SvcInfo{Running: false})

	// 两者必须给出**不同**的处置指引——这是本次修复的全部意义
	if killed["what_to_do"] == never["what_to_do"] {
		t.Fatalf("曾成功与从未成功必须给出不同处置建议，实际都是 %q",
			killed["what_to_do"])
	}
	if !strings.Contains(killed["what_to_do"].(string), "start_service") {
		t.Errorf("曾成功后被杀应指示重新拉起，实际：%v", killed["what_to_do"])
	}
	if strings.Contains(never["what_to_do"].(string), "start_service") {
		t.Errorf("从未成功过不应指示直接重启（那是排查问题，不是重启），实际：%v",
			never["what_to_do"])
	}
	if !strings.Contains(never["what_to_do"].(string), "端口") &&
		!strings.Contains(never["what_to_do"].(string), "编译") {
		t.Errorf("从未成功过应指向排查方向（端口/编译/依赖），实际：%v",
			never["what_to_do"])
	}
}

// ever_verified 布尔必须真的暴露给 AI——它是模型做判断的依据。
func TestSvcAIViewExposesEverVerified(t *testing.T) {
	defer withSvcStubs(t)()

	svcWith("backend", func(r *svcRuntimeRec) { r.Verified = true; r.Conclusion = "ok" })
	v := svcAIView("backend", SvcInfo{Running: false})
	if v["ever_verified"] != true {
		t.Errorf("曾复验通过时 ever_verified 应为 true，实际 %v", v["ever_verified"])
	}
	if v["last_verify"] == nil || v["last_verify"] == "" {
		t.Error("应把最近一次复验结论带给模型，否则它无法判断服务是否曾经可用")
	}

	svcWith("backend", func(r *svcRuntimeRec) { r.Verified = false; r.Conclusion = "fail" })
	v2 := svcAIView("backend", SvcInfo{Running: false})
	if v2["ever_verified"] != false {
		t.Errorf("复验未通过时 ever_verified 应为 false，实际 %v", v2["ever_verified"])
	}
}

// 运行中的服务不该被建议重启——那是危险的建议。
func TestSvcAIViewRunningServiceNotToldToRestart(t *testing.T) {
	defer withSvcStubs(t)()
	v := svcAIView("web", SvcInfo{Running: true, PID: 100, Since: "10:00:00"})
	advice, _ := v["what_to_do"].(string)
	if strings.Contains(advice, "start_service") {
		t.Errorf("运行中的服务不应建议 start_service，实际：%s", advice)
	}
	if !strings.Contains(advice, "复验") {
		t.Errorf("运行中但用户说不可用时，应建议先复验而不是重启，实际：%s", advice)
	}
}

// 快照里必须真的带上这个判据——svcAIView 写了但没接进快照等于没修。
func TestHealthSnapshotContainsServiceDisposition(t *testing.T) {
	defer withSvcStubs(t)()
	svcWith("backend", func(r *svcRuntimeRec) {
		r.Verified = true
		r.Conclusion = "已复验通过：端口 8888 在监听"
	})

	raw := aiHealthSnapshot()
	if raw == "" {
		t.Fatal("快照为空")
	}
	// 快照是给模型看的结构化文本；这里只断言关键判据在里面
	if !strings.Contains(raw, "ever_verified") {
		t.Errorf("快照里缺少 ever_verified，模型无法区分曾成功与从未成功：%s",
			truncateStr(raw, 300))
	}
	if !strings.Contains(raw, "what_to_do") {
		t.Errorf("快照里缺少 what_to_do，模型要自己猜处置方向：%s", truncateStr(raw, 300))
	}
}

// 快照仍是合法 JSON（不能因为加了字段就破坏结构）。
func TestHealthSnapshotStillValidJSON(t *testing.T) {
	defer withSvcStubs(t)()
	raw := aiHealthSnapshot()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("快照不是合法 JSON：%v\n%s", err, truncateStr(raw, 200))
	}
	svcs, ok := m["services"].(map[string]any)
	if !ok {
		t.Fatalf("services 字段类型异常：%T", m["services"])
	}
	be, ok := svcs["backend"].(map[string]any)
	if !ok {
		t.Fatalf("backend 应是对象，实际 %T", svcs["backend"])
	}
	if _, has := be["ever_verified"]; !has {
		t.Error("backend 服务里应有 ever_verified 字段")
	}
}
