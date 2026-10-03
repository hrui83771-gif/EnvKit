package main

// lifecycle_test.go —— v2.2 M10 进程生命周期验证单测
//
// 核心用例只有一个，但它是最重要的一个：
// **"起来又崩了"必须被验证层抓到**，而不是报"已就绪"。
// 现有验证只看"监听的那一刻"，对这个故障完全失明。
//
// ⚠️ 写这类测试必须注意一个 Go 陷阱：
//
//	defer withLifecycleFake(t, ...)   // ❌ 延迟的是【调用】，不是求值
//	restore := withLifecycleFake(t, …) // ✅ 立即替换
//	defer restore()                    //    返回时恢复
//
// 第一种写法里替换**根本没发生**，测试跑的是真实实现——会得到一片假绿。

import (
	"strings"
	"testing"
	"time"
)

// withLifecycleFake 注入假的存活/端口探测，把观察窗压到很短以免单测拖时间。
func withLifecycleFake(t *testing.T, alive func(string) (bool, int), listen func(int) bool) func() {
	t.Helper()
	oldAlive, oldListen := lifecycleAlive, lifecycleListen
	oldWindow, oldEvery := verifyObserveWindow, verifyObserveEvery
	lifecycleAlive, lifecycleListen = alive, listen
	verifyObserveWindow, verifyObserveEvery = 400*time.Millisecond, 50*time.Millisecond
	svcForgetPort("web")
	svcForgetPort("backend")
	return func() {
		lifecycleAlive, lifecycleListen = oldAlive, oldListen
		verifyObserveWindow, verifyObserveEvery = oldWindow, oldEvery
		svcForgetPort("web")
		svcForgetPort("backend")
	}
}

// withHubLog 临时替换 hub.history，测试后恢复。
//
// 必须**替换而不是 append**：svcLogTail 从后往前取且有 12 条上限，
// 全量跑时别的测试往 hub 里塞了日志，append 进去的崩溃证据会被挤出取值范围
// （首次实测就是这样：单独跑绿、全量跑失败）。
func withHubLog(t *testing.T, msgs ...LogMsg) func() {
	t.Helper()
	hub.mu.Lock()
	old := hub.history
	hub.history = append([]LogMsg(nil), msgs...)
	hub.mu.Unlock()
	return func() {
		hub.mu.Lock()
		hub.history = old
		hub.mu.Unlock()
	}
}

// ===== 关键用例：启动即崩 =====

// 端口监听起来过，但进程在观察窗内消失 → 必须判失败，且带上日志尾部。
// 这正是旧验证层的盲区：它会在这里返回"已就绪"。
func TestLifecycle_CrashAfterStart(t *testing.T) {
	restoreLF := withLifecycleFake(t,
		func(string) (bool, int) { return false, 9999 }, // 进程已死
		func(int) bool { return true },                  // 端口还在（僵尸监听）
	)
	defer restoreLF()
	restoreHL := withHubLog(t, LogMsg{Scope: scStart, Src: "web",
		Line: "Error: listen EADDRINUSE: address already in use :::8080"})
	defer restoreHL()

	r := verifyObserve("verify_service", "web", 8080, 1234, "node.exe",
		[]string{"pid=1234", "port=8080 LISTENING"})

	if r.Ok {
		t.Fatal("进程已崩溃却判为成功——这正是 v2.2 要修的核心缺陷")
	}
	if r.ErrKind != errKindCrash {
		t.Fatalf("错误归类应为 %s（与「起不来」区分），实际 %q", errKindCrash, r.ErrKind)
	}
	if !strings.Contains(r.Msg, "崩溃") {
		t.Errorf("结论应明确说崩溃，实际 %q", r.Msg)
	}
	ev := aiOpEvidence(r)
	if !strings.Contains(ev, "EADDRINUSE") {
		t.Errorf("崩溃证据必须附日志尾部（让用户自己翻日志猜是没用的），实际 %q", ev)
	}
	if !strings.Contains(ev, "crash_log_tail") {
		t.Errorf("证据里应标明这是崩溃日志尾部，实际 %q", ev)
	}
}

// 进程活着、端口一直在 → 观察窗通过后才判 verified
func TestLifecycle_Survives(t *testing.T) {
	restoreLF := withLifecycleFake(t,
		func(string) (bool, int) { return true, 1234 },
		func(int) bool { return true },
	)
	defer restoreLF()
	restoreHL := withHubLog(t, LogMsg{Scope: scStart, Src: "web", Line: "服务已就绪"})
	defer restoreHL()

	r := verifyObserve("verify_service", "web", 8080, 1234, "node.exe",
		[]string{"pid=1234", "port=8080 LISTENING"})

	if !r.Ok || !r.Verified {
		t.Fatalf("稳定运行应判已复验通过，实际 Ok=%v Verified=%v Msg=%q", r.Ok, r.Verified, r.Msg)
	}
	ev := aiOpEvidence(r)
	if !strings.Contains(ev, "survived") {
		t.Errorf("证据里应记录观察窗结果（让人知道验证做了多久），实际 %q", ev)
	}
	if !strings.Contains(ev, "observe_window") {
		t.Errorf("证据里应写明观察窗长度，实际 %q", ev)
	}
}

// 进程还在但端口不监听了（健康检查失败把 listener 关掉）→ 也不能算就绪
func TestLifecycle_PortClosedWhileAlive(t *testing.T) {
	restoreLF := withLifecycleFake(t,
		func(string) (bool, int) { return true, 1234 },
		func(int) bool { return false },
	)
	defer restoreLF()
	restoreHL := withHubLog(t, LogMsg{Scope: scStart, Src: "web", Line: "health check failed"})
	defer restoreHL()

	r := verifyObserve("verify_service", "backend", 8888, 4321, "main.exe",
		[]string{"pid=4321", "port=8888 LISTENING"})

	if r.Ok {
		t.Fatal("端口已不监听却判成功")
	}
	if r.ErrKind != errKindCrash {
		t.Fatalf("归类应为 %s，实际 %q", errKindCrash, r.ErrKind)
	}
	if !strings.Contains(r.Msg, "不再监听") {
		t.Errorf("结论应点明端口不再监听，实际 %q", r.Msg)
	}
	if !strings.Contains(aiOpEvidence(r), "port_closed=8888") {
		t.Errorf("证据应记录端口已关闭，实际 %q", aiOpEvidence(r))
	}
}

// ===== 端口漂移 =====

// 服务换了端口：记入证据但不判失败——判失败会造成误报，
// 而误报在验证层特别糟糕（用户会开始不信复验结论）。
func TestLifecycle_PortDriftRecordedNotFailed(t *testing.T) {
	restoreLF := withLifecycleFake(t,
		func(string) (bool, int) { return true, 1234 },
		func(int) bool { return true },
	)
	defer restoreLF()
	restoreHL := withHubLog(t, LogMsg{Scope: scStart, Src: "web", Line: "x"})
	defer restoreHL()

	r1 := verifyObserve("verify_service", "web", 8080, 1, "node.exe", nil)
	if !r1.Verified {
		t.Fatal("首次应通过")
	}
	if strings.Contains(aiOpEvidence(r1), "port_drift") {
		t.Error("首次不应报漂移（没有上一次可比）")
	}
	r2 := verifyObserve("verify_service", "web", 3000, 1, "node.exe", nil)
	if !r2.Verified {
		t.Fatal("换端口不应导致验证失败——服务可能确实换了端口")
	}
	ev := aiOpEvidence(r2)
	if !strings.Contains(ev, "port_drift=8080→3000") {
		t.Errorf("端口漂移必须记入证据（配置里的 scan_ports 可能已过期），实际 %q", ev)
	}
	r3 := verifyObserve("verify_service", "web", 3000, 1, "node.exe", nil)
	if strings.Contains(aiOpEvidence(r3), "port_drift") {
		t.Error("端口未变时不该重复报漂移")
	}
}

func TestLifecycle_ForgetPortClearsDrift(t *testing.T) {
	restoreLF := withLifecycleFake(t,
		func(string) (bool, int) { return true, 1 },
		func(int) bool { return true },
	)
	defer restoreLF()
	restoreHL := withHubLog(t, LogMsg{Scope: scStart, Src: "web", Line: "x"})
	defer restoreHL()

	verifyObserve("verify_service", "web", 8080, 1, "node.exe", nil)
	svcForgetPort("web")
	r := verifyObserve("verify_service", "web", 8080, 1, "node.exe", nil)
	if strings.Contains(aiOpEvidence(r), "port_drift") {
		t.Error("服务重启后端口记录已清空，不该误报漂移")
	}
}

// ===== 崩溃日志 =====

func TestLifecycle_LogTail(t *testing.T) {
	restoreHL := withHubLog(t,
		LogMsg{Scope: scStart, Src: "web", Line: "第 1 行"},
		LogMsg{Scope: scStart, Src: "web", Line: "第 2 行"},
		LogMsg{Scope: scStart, Src: "backend", Line: "后端日志不该被取走"},
		LogMsg{Scope: scStart, Src: "web", Line: "第 3 行"},
	)
	defer restoreHL()

	tail := svcLogTail("web", 10)
	if tail == "" {
		t.Fatal("应取到 web 的日志")
	}
	if strings.Contains(tail, "后端日志") {
		t.Errorf("只应取目标服务的日志，实际 %q", tail)
	}
	for _, want := range []string{"第 1 行", "第 2 行", "第 3 行"} {
		if !strings.Contains(tail, want) {
			t.Errorf("应取到 %q，实际 %q", want, tail)
		}
	}
}

// EnvKit 自己写的"已启动"不是崩溃原因，不应混进证据
func TestLifecycle_LogTailSkipsNoise(t *testing.T) {
	restoreHL := withHubLog(t,
		LogMsg{Scope: scStart, Src: "web", Line: "前端已启动（npm run dev）"},
		LogMsg{Scope: scStart, Src: "web", Line: "panic: runtime error: index out of range"},
	)
	defer restoreHL()

	tail := svcLogTail("web", 10)
	if strings.Contains(tail, "已启动") {
		t.Errorf("EnvKit 自己的启动成功日志不是崩溃原因，不该进证据：%q", tail)
	}
	if !strings.Contains(tail, "panic") {
		t.Errorf("真正的崩溃原因必须保留：%q", tail)
	}
}

// 系统级日志（Src 为空）绝不能混进服务的崩溃证据。
// 曾经踩过：过滤条件写成 `m.Src != "" && m.Src != target`，
// 结果配置校验之类的系统日志挤满 12 条上限，崩溃原因被顶出去。
func TestLifecycle_LogTailExcludesSystemLogs(t *testing.T) {
	restoreHL := withHubLog(t,
		LogMsg{Scope: scSys, Line: "[配置] 存在问题：Go 代理不能为空"},
		LogMsg{Scope: scSys, Line: "[告警] 磁盘使用率 91%"},
		LogMsg{Scope: scStart, Src: "web", Line: "FATAL: out of memory"},
	)
	defer restoreHL()

	tail := svcLogTail("web", 12)
	if !strings.Contains(tail, "out of memory") {
		t.Fatalf("崩溃原因应被保留，实际 %q", tail)
	}
	if strings.Contains(tail, "Go 代理") || strings.Contains(tail, "磁盘") {
		t.Errorf("系统级日志不属于崩溃证据，不该混进来：%q", tail)
	}
}

func TestLifecycle_LogTailEmpty(t *testing.T) {
	restoreHL := withHubLog(t)
	defer restoreHL()
	if svcLogTail("web", 10) != "" {
		t.Error("没有日志时应返回空串（证据里就不该有 crash_log_tail）")
	}
}

// ===== 错误归类与提示 =====

// 崩溃与"起不来"必须分开指引：前者看运行时日志，后者看环境。
func TestLifecycle_Hints(t *testing.T) {
	crash := lifecycleHint(errKindCrash)
	spawn := lifecycleHint(errKindSpawnFail)
	if crash == spawn {
		t.Fatal("崩溃与启动失败的指引不应相同——处置方式完全不同")
	}
	if !strings.Contains(crash, "日志") {
		t.Errorf("崩溃指引应指向日志，实际 %q", crash)
	}
	if !strings.Contains(spawn, "端口") && !strings.Contains(spawn, "依赖") {
		t.Errorf("启动失败指引应指向环境原因，实际 %q", spawn)
	}
	if lifecycleHint("other") != "" {
		t.Error("未知归类应返回空串")
	}
}
