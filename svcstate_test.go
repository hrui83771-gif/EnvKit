package main

// svcstate_test.go —— v2.2 M12 服务运行时注册表单测
//
// 重点三条：
//  1. **合并后的注册表要能一次快照**（原先三个分散的 map 做不到）
//  2. **崩溃计数与退避建议要真的有用**，不是摆设
//  3. **只记录不自动重启**——本项刻意不实现自动重启，用测试把这个决定钉住

import (
	"strings"
	"testing"
)

func TestSvcRecordCrashCounts(t *testing.T) {
	svcReset("web")
	defer svcReset("web")

	svcRecordCrash("web", errKindCrash, "panic: out of memory", 8080)
	if n := svcRestartCount("web"); n != 1 {
		t.Fatalf("首次崩溃后计数应为 1，实际 %d", n)
	}
	svcRecordCrash("web", errKindCrash, "panic: out of memory", 8080)
	svcRecordCrash("web", errKindCrash, "panic: out of memory", 8080)
	if n := svcRestartCount("web"); n != 3 {
		t.Fatalf("三次崩溃后计数应为 3，实际 %d", n)
	}
	if b := svcBackoff("web"); b == "" {
		t.Error("崩溃后必须给出退避建议——反复重启只会刷屏")
	}
	ev := svcCrashes("web")
	if len(ev) != 3 {
		t.Fatalf("崩溃历史应有 3 条，实际 %d", len(ev))
	}
	// 新在前：最近一次崩溃在最前
	if !strings.Contains(ev[0].Why, "out of memory") {
		t.Errorf("崩溃历史应新在前，实际首条 %q", ev[0].Why)
	}
	if ev[0].Port != 8080 {
		t.Errorf("崩溃记录应带上端口，实际 %d", ev[0].Port)
	}
}

// 退避建议要随失败次数变化，且有上限（否则"等服务自己好"变成永远等下去）
func TestBackoffAdviceEscalates(t *testing.T) {
	first := backoffAdvice(1)
	mid := backoffAdvice(3)
	lots := backoffAdvice(9)
	if first == mid || mid == lots {
		t.Fatal("退避建议应随连续失败次数变化")
	}
	if !strings.Contains(first, "直接重启") {
		t.Errorf("首次失败应建议直接重启，实际 %q", first)
	}
	if !strings.Contains(lots, "盲目重启") || !strings.Contains(lots, "崩溃原因") {
		t.Errorf("多次失败后应劝阻盲目重启并指向日志里的崩溃原因，实际 %q", lots)
	}
	// 更大值仍要有结果（不能越界返回空）
	if backoffAdvice(100) == "" {
		t.Error("超多次失败也该给出建议（上限建议，而不是沉默）")
	}
}

func TestSvcCrashHistoryCapped(t *testing.T) {
	svcReset("backend")
	defer svcReset("backend")
	for i := 0; i < svcMaxCrashEvents+8; i++ {
		svcRecordCrash("backend", errKindCrash, "crash", 0)
	}
	if n := len(svcCrashes("backend")); n != svcMaxCrashEvents {
		t.Fatalf("崩溃历史应被截断到 %d 条，实际 %d", svcMaxCrashEvents, n)
	}
}

// 崩溃后复验结论必须作废：服务已经不在了，"已复验通过"是历史而非现状。
// 这是"执行不等于成功"在状态上的直接体现。
func TestSvcCrashInvalidatesVerify(t *testing.T) {
	svcReset("web")
	defer svcReset("web")

	svcRecordVerify("web", "前端已就绪：端口 8080 在监听", "", true)
	if !svcLastVerifyErr2("web") {
		t.Fatal("前置条件：复验应处于通过状态")
	}
	svcRecordCrash("web", errKindCrash, "启动后崩溃", 8080)
	if svcLastVerifyErr2("web") {
		t.Error("崩溃后不得仍标记为已复验通过")
	}
	if svcLastVerify("web") != "" {
		t.Error("崩溃后应清空复验结论，留着会让人以为服务还验过")
	}
}

func svcLastVerifyErr2(target string) bool {
	_, ok := svcLastVerifyErr(target)
	return ok
}

// 复验通过后计数应归零——服务已经好了，继续沿用旧的失败次数会误导。
func TestSvcVerifySuccessResetsCount(t *testing.T) {
	svcReset("web")
	defer svcReset("web")

	svcRecordCrash("web", errKindCrash, "崩了", 8080)
	svcRecordCrash("web", errKindCrash, "又崩了", 8080)
	if svcRestartCount("web") != 2 {
		t.Fatal("前置条件")
	}
	svcRecordVerify("web", "已就绪", "", true)
	if n := svcRestartCount("web"); n != 0 {
		t.Errorf("复验通过后失败计数应归零，实际 %d", n)
	}
	if svcBackoff("web") != "" {
		t.Error("复验通过后不该再显示退避建议")
	}
}

// 合并注册表的核心价值：能一次拿到全部状态
func TestSvcAllRecordsSnapshot(t *testing.T) {
	svcReset("web")
	svcReset("backend")
	defer func() { svcReset("web"); svcReset("backend") }()

	svcRememberPort("web", 8080)
	svcRecordVerify("web", "已就绪", "", true)
	svcRecordCrash("backend", errKindCrash, "崩了", 8888)

	all := svcAllRecords()
	if len(all) != 2 {
		t.Fatalf("应一次快照到 2 个服务，实际 %d", len(all))
	}
	w, ok := all["web"]
	if !ok {
		t.Fatal("快照应含 web")
	}
	if w.Port != 8080 {
		t.Errorf("快照应含端口，实际 %d", w.Port)
	}
	if !w.Verified {
		t.Error("快照应含复验结论")
	}
	b := all["backend"]
	if b.Restarts != 1 {
		t.Errorf("快照应含崩溃计数，实际 %d", b.Restarts)
	}
}

// 快照必须是深拷贝：调用方改了不该影响注册表
func TestSvcSnapshotIsDeepCopy(t *testing.T) {
	svcReset("web")
	defer svcReset("web")
	svcRecordCrash("web", errKindCrash, "原始原因", 8080)

	rec := svcSnapshot("web")
	if len(rec.Crashes) != 1 {
		t.Fatal("前置条件")
	}
	rec.Crashes[0].Why = "被改了"
	rec.Restarts = 999

	if svcSnapshot("web").Crashes[0].Why != "原始原因" {
		t.Error("快照必须是深拷贝：调用方改了 Crashes 会污染注册表")
	}
	if svcRestartCount("web") != 1 {
		t.Error("快照必须是深拷贝：调用方改了字段会污染注册表")
	}
}

// svcReset 一次清空全部（原先要分别调 forgetPort / forgetVerify）
func TestSvcResetClearsEverything(t *testing.T) {
	svcReset("web")
	svcRememberPort("web", 8080)
	svcRecordVerify("web", "已就绪", "", true)
	svcRecordCrash("web", errKindCrash, "崩了", 8080)

	svcReset("web")
	rec := svcSnapshot("web")
	if rec.Port != 0 || rec.Verified || rec.Restarts != 0 || len(rec.Crashes) != 0 {
		t.Errorf("svcReset 应清空全部运行时记录，实际 %+v", rec)
	}
}

// 端口漂移与复验仍要正常工作（从 lifecycle.go / runtime.go 迁入后不能坏）
func TestSvcPortAndVerifyStillWork(t *testing.T) {
	svcReset("web")
	defer svcReset("web")
	if prev := svcRememberPort("web", 8080); prev != 0 {
		t.Errorf("首次记录应返回 0，实际 %d", prev)
	}
	if prev := svcRememberPort("web", 3000); prev != 8080 {
		t.Errorf("第二次应返回上次值 8080，实际 %d", prev)
	}
	if p, ok := svcPortOf("web"); !ok || p != 3000 {
		t.Errorf("端口查询失效：%d %v", p, ok)
	}
	svcRecordVerify("web", "已就绪", "", true)
	kind, ok := svcLastVerifyErr("web")
	if !ok || kind != "" {
		t.Errorf("复验记录失效：%q %v", kind, ok)
	}
}

// ===== 明确不做的：自动重启 =====

// 本项刻意不实现崩溃自动重启。这个测试把这个决定钉住：
// 反复崩溃的服务在 3 次以上时，状态里必须带着"别再盲目重启"的建议，
// 而不是系统自作主张地把它拉起来。
//
// 自动重启的三个问题：
//   - 用户正在调试时反复重启会直接干扰排查
//   - 配置错误导致的崩溃，重启一万次也没用，只会把日志刷爆
//   - 停止服务后守护又拉起来，与用户意图直接冲突
func TestNoAutoRestartOnlyAdvice(t *testing.T) {
	svcReset("web")
	defer svcReset("web")
	for i := 0; i < 5; i++ {
		svcRecordCrash("web", errKindCrash, "配置错误导致崩溃", 8080)
	}
	rec := svcSnapshot("web")
	if rec.Restarts != 5 {
		t.Fatalf("应记录 5 次崩溃，实际 %d", rec.Restarts)
	}
	if !strings.Contains(rec.Backoff, "盲目重启") {
		t.Errorf("多次崩溃后必须劝阻盲目重启并指向根因，实际 %q", rec.Backoff)
	}
	// 关键：状态里不应该有任何"正在重启"的标记——
	// 系统没资格替用户决定什么时候重启。
	if strings.Contains(rec.Backoff, "正在重启") || strings.Contains(rec.Backoff, "已安排重启") {
		t.Errorf("不应自动安排重启（那是替用户做决定），实际 %q", rec.Backoff)
	}
}
