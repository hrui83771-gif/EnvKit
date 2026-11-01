package main

// startstatus_test.go —— "启动状态"这条状态链的三态语义。
//
// 为什么值得单独测：这里出过一类很难靠肉眼发现的错 —— **服务明明起来了，
// 某处state却写着失败**。界面卡片走 runtimeService（五态），
// 程序启动面板读 progState，AI 读环境快照，三处口径一旦分叉，
// 用户看到的就是"通过复验了，但 AI 说报错"。
//
// 这三条断言锁的正是"什么情况才配叫失败"：
//   1. 幂等跳过（已在运行、未复验）不是失败；
//   2. 后到的复验结论能纠正先前的失败；
//   3. 用户主动停止是终态，不被后续复验改写。

import "testing"

// withProgState 临时替换全局进度表，测试结束自动还原。
func withProgState(t *testing.T, init map[string]ProgStatus) {
	t.Helper()
	progMu.Lock()
	old := progState
	progState = init
	progMu.Unlock()
	t.Cleanup(func() {
		progMu.Lock()
		progState = old
		progMu.Unlock()
	})
}

func progOf(key string) ProgStatus {
	progMu.Lock()
	defer progMu.Unlock()
	return progState[key]
}

func TestProgSetSkipIsNotFailure(t *testing.T) {
	withProgState(t, map[string]ProgStatus{})

	// 未复验的跳过：服务在跑，只是这一轮没新鲜证据 → warn，不是 fail。
	progSetSkip("web-start", OpResult{Ok: true, Msg: "进程已在运行（PID 1），未重复启动"})
	if got := progOf("web-start"); got.Status != progWarn {
		t.Fatalf("未复验的幂等跳过应落 %q，实际 %q", progWarn, got.Status)
	}

	// 已复验的跳过：ok。
	progSetSkip("web-start", OpResult{Ok: true, Verified: true, Msg: "已在运行且复验通过"})
	if got := progOf("web-start"); got.Status != progOK || !got.Ok {
		t.Fatalf("已复验的幂等跳过应落 %q，实际 %q(ok=%v)", progOK, got.Status, got.Ok)
	}

	// 真正的失败仍要如实落 fail，别把三态测成"永远不报错"。
	progSetSkip("web-start", OpResult{Ok: false, Msg: "进程已退出"})
	if got := progOf("web-start"); got.Status != progFail {
		t.Fatalf("Ok=false 应落 %q，实际 %q", progFail, got.Status)
	}
}

func TestEndStartFlagLetsLaterVerifyCorrectEarlierFailure(t *testing.T) {
	withProgState(t, map[string]ProgStatus{
		"backend-start": {Running: false, Status: progFail, Msg: "上一轮启动失败", At: "10:00:00"},
	})
	// 一轮启动先被幂等跳过（Running 置 false），紧接着复验通过。
	// 旧实现因 "!ps.Running 就 return" 把这句结论丢了，状态里留着红字。
	endStartFlag("backend", OpResult{Ok: true, Verified: true, Msg: "端口 8888 已监听，已复验通过"})
	got := progOf("backend-start")
	if got.Status != progOK || !got.Ok {
		t.Fatalf("后到的复验通过应纠正为 %q，实际 %q(ok=%v)", progOK, got.Status, got.Ok)
	}
	if got.Running {
		t.Fatal("落结论时 Running 必须为 false")
	}
}

func TestEndStartFlagKeepsUserStopped(t *testing.T) {
	withProgState(t, map[string]ProgStatus{
		"web-start": {Running: false, Status: progStopped, Msg: "已停止"},
	})
	// 停止之后的复验失败只是在确认"它确实停了"，不是一次故障。
	endStartFlag("web", OpResult{Ok: false, Msg: "进程已退出"})
	if got := progOf("web-start"); got.Status != progStopped {
		t.Fatalf("主动停止是终态，不该被改写成 %q", got.Status)
	}
}

func TestEndStartFlagWarnsWhenStartedButUnverified(t *testing.T) {
	withProgState(t, map[string]ProgStatus{
		"web-start": {Running: true},
	})
	endStartFlag("web", OpResult{Ok: true, Verified: false, Msg: "进程已派生，窗口内未观察到端口"})
	got := progOf("web-start")
	if got.Status != progWarn {
		t.Fatalf("起来了但没验到应落 %q，实际 %q", progWarn, got.Status)
	}
	if got.Running {
		t.Fatal("一轮启动结束时 Running 必须落下")
	}
}
