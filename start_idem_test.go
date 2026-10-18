// start_idem_test.go —— v3.0 T1.3（启动幂等 + 进程条目身份守卫）与
// T1.4（看门狗注册 + 超时核对）的单测。
//
// 这两个任务修的是同一类事故的两个半边：
//   · T1.3：重复 start 派生孤儿进程，旧监控 goroutine 醒来把新进程的
//     activeProcs 条目删掉 → "停不掉"；
//   · T1.4：任务超时只放锁不杀子进程（go build 挂死 → 组锁永远占着），
//     且超时后不核对实际状态 → "以为超时其实成功了"。
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ---------- 小工具 ----------

// cmdWithPID 造一个"进程已退出但句柄还在"的 cmd（善后逻辑只认 PID）。
func cmdWithPID(pid int) *exec.Cmd {
	return &exec.Cmd{Process: &os.Process{Pid: pid}}
}

// setSvcRunning 临时把某服务标记成运行中（测试后恢复）。
func setSvcRunning(t *testing.T, target string, pid int) {
	t.Helper()
	svcMu.Lock()
	old := svcState[target]
	svcState[target] = &SvcInfo{Running: true, PID: pid, Since: "12:00:00"}
	svcMu.Unlock()
	t.Cleanup(func() {
		svcMu.Lock()
		svcState[target] = old
		svcMu.Unlock()
	})
}

func setSvcVerified(t *testing.T, target string, v bool) {
	t.Helper()
	svcWith(target, func(r *svcRuntimeRec) { r.Verified = v })
	t.Cleanup(func() { svcReset(target) })
}

// ---------- T1.3 幂等前置 ----------

// Running && Verified → 必须整条跳过，且结论是"已复验通过"（不是未复验）。
func TestSvcStartSkipRunningVerified(t *testing.T) {
	setSvcRunning(t, "web", os.Getpid())
	setSvcVerified(t, "web", true)
	res, hit := svcStartSkip("web")
	if !hit {
		t.Fatalf("Running && Verified 必须跳过，实际未跳过")
	}
	if !res.Ok || !res.Verified {
		t.Fatalf("跳过结论必须是 ok+verified，实际：%s", res.String())
	}
	if !strings.Contains(res.Msg, "未重复启动") {
		t.Fatalf("措辞必须点明「未重复启动」，实际：%q", res.Msg)
	}
}

// Running && !Verified → 仍然跳过派生，但结论只能是"未复验"——
// 把"进程在跑"说成"已恢复"正是 R3 禁止的三态合并。
func TestSvcStartSkipRunningUnverified(t *testing.T) {
	setSvcRunning(t, "web", os.Getpid())
	setSvcVerified(t, "web", false)
	res, hit := svcStartSkip("web")
	if !hit {
		t.Fatalf("Running 时必须跳过派生，实际未跳过")
	}
	if !res.Ok || res.Verified {
		t.Fatalf("Running && !Verified 必须返回 ok 且未复验，实际：%s", res.String())
	}
	if !strings.Contains(res.Msg, "复验结论") {
		t.Fatalf("措辞必须引导去复验，实际：%q", res.Msg)
	}
}

func TestSvcStartSkipNotRunning(t *testing.T) {
	svcMu.Lock()
	old := svcState["web"]
	svcState["web"] = &SvcInfo{Running: false}
	svcMu.Unlock()
	t.Cleanup(func() {
		svcMu.Lock()
		svcState["web"] = old
		svcMu.Unlock()
	})
	if _, hit := svcStartSkip("web"); hit {
		t.Fatalf("未运行时不应当跳过")
	}
}

// opStartWeb 在"已在运行且复验通过"时必须原地返回，走不到目录校验——
// 反证：若跳过失效，dir 为空会先撞上"未指定前端目录"的 opFail。
func TestOpStartWebIdempotentSkip(t *testing.T) {
	setSvcRunning(t, "web", os.Getpid())
	setSvcVerified(t, "web", true)
	res := opStartWeb("", "", "")
	if !res.Ok || !res.Verified {
		t.Fatalf("幂等跳过应返回 ok+verified，实际：%s", res.String())
	}
	if !strings.Contains(res.Msg, "未重复启动") {
		t.Fatalf("实际：%q", res.Msg)
	}
}

func TestOpStartBackendIdempotentSkip(t *testing.T) {
	setSvcRunning(t, "backend", os.Getpid())
	setSvcVerified(t, "backend", true)
	res := opStartBackend("", "")
	if !res.Ok || !res.Verified {
		t.Fatalf("幂等跳过应返回 ok+verified，实际：%s", res.String())
	}
}

// ---------- T1.3 进程条目身份守卫 ----------

// 旧监控 goroutine 不得删掉别人的条目：activeProcs[key] 已经是新进程时，
// 按旧 cmd 来删必须扑空。
func TestProcDropIfCurrentIdentity(t *testing.T) {
	oldCmd := &exec.Cmd{}
	newCmd := &exec.Cmd{}
	procMu.Lock()
	prev, had := activeProcs["web"]
	activeProcs["web"] = oldCmd
	procMu.Unlock()
	t.Cleanup(func() {
		procMu.Lock()
		if had {
			activeProcs["web"] = prev
		} else {
			delete(activeProcs, "web")
		}
		procMu.Unlock()
	})

	procDropIfCurrent("web", newCmd) // 别人家的进程：不许删
	procMu.Lock()
	_, ok := activeProcs["web"]
	procMu.Unlock()
	if !ok {
		t.Fatalf("按旧 cmd 删除时新进程条目被误删")
	}

	// 换成新进程后再按旧 cmd 删：条目必须还在
	procMu.Lock()
	activeProcs["web"] = newCmd
	procMu.Unlock()
	procDropIfCurrent("web", oldCmd)
	procMu.Lock()
	_, ok = activeProcs["web"]
	procMu.Unlock()
	if !ok {
		t.Fatalf("旧进程善后删掉了新进程条目（G3 原型事故）")
	}

	procDropIfCurrent("web", newCmd) // 自己的条目：照常删
	procMu.Lock()
	_, ok = activeProcs["web"]
	procMu.Unlock()
	if ok {
		t.Fatalf("自己的条目没被删掉")
	}
}

// svcMarkStoppedIfCurrent：只许把"还记着自己"的状态改成已停止。
func TestSvcMarkStoppedIfCurrent(t *testing.T) {
	svcMu.Lock()
	old := svcState["web"]
	svcState["web"] = &SvcInfo{Running: true, PID: 4242}
	svcMu.Unlock()
	t.Cleanup(func() {
		svcMu.Lock()
		svcState["web"] = old
		svcMu.Unlock()
	})

	svcMarkStoppedIfCurrent("web", cmdWithPID(9999)) // 新进程的 PID：不许动
	svcMu.Lock()
	s := svcState["web"]
	svcMu.Unlock()
	if !s.Running || s.PID != 4242 {
		t.Fatalf("新进程状态被旧进程善后改写：%+v", s)
	}

	svcMarkStoppedIfCurrent("web", cmdWithPID(4242)) // 自己的：照常写
	svcMu.Lock()
	s = svcState["web"]
	svcMu.Unlock()
	if s.Running || s.PID != 0 {
		t.Fatalf("自己的退出没把状态改成已停止：%+v", s)
	}
}

// ---------- T1.4 前台命令注册表 ----------

func TestFgRegistryKillAbortUnregister(t *testing.T) {
	key := "test/fg"
	cmd := &exec.Cmd{}
	fgRegister(key, cmd, nil)
	t.Cleanup(func() { fgUnregister(key, cmd) })

	if fgKill("test/missing") {
		t.Fatalf("没有注册中的命令时 fgKill 应返回 false")
	}
	if !fgKill(key) {
		t.Fatalf("已注册的命令 fgKill 应返回 true")
	}
	if !fgAborted(key) {
		t.Fatalf("杀过之后必须打上 aborted 标记（重试循环靠它收手）")
	}
	// 别的 cmd 来注销：不许删掉这一条
	fgUnregister(key, &exec.Cmd{})
	if !fgAborted(key) {
		t.Fatalf("身份不符的注销把条目删了")
	}
	fgUnregister(key, cmd)
	if fgAborted(key) {
		t.Fatalf("自己的注销没生效")
	}
}

// 看门狗杀完之后，重试循环必须立即收手——否则"杀了还重试"等于无锁执行。
func TestRunRetryAbortStopsFurtherAttempts(t *testing.T) {
	calls := 0
	aborted := false
	_, err := runRetry(retryPolicy{
		Attempts: 3,
		Backoff:  time.Millisecond,
		Abort:    func() bool { return aborted },
	}, func(bool) (bool, error) {
		calls++
		aborted = true // 模拟看门狗在第一次 attempt 期间杀掉命令
		return false, errors.New("killed")
	})
	if calls != 1 {
		t.Fatalf("被 abort 后不应再有第二次 attempt，实际 %d 次", calls)
	}
	if err == nil {
		t.Fatalf("abort 时必须交回最后一次错误，不能伪装成功")
	}
}

// ---------- T1.4 句柄结论与超时核对 ----------

// 句柄关闭后迟到的 SetResult 不许覆盖结论：被看门狗终止的任务
// 不能在被杀之后又把结论改成"build_fail"。
func TestSetResultIgnoredAfterDone(t *testing.T) {
	h := &TaskHandle{ID: 999001, Name: "测试任务", Timeout: time.Second}
	h.SetResult(opOK("start_service", "web", "进程已派生"))
	h.Done()
	h.SetResult(opFail("start_service", "web", errKindBuildFail, "go build 失败"))
	r, set := h.Result()
	if !set {
		t.Fatalf("句柄应保有结论")
	}
	if !r.Ok || r.Verified {
		t.Fatalf("关闭后的迟到写入覆盖了结论：%s", r.String())
	}
}

// 超时之后审计里记的必须是两种事实之一（这里是"已退出"），
// 且句柄结论保持 timeout 不被迟到写入覆盖。
func TestTerminateRecordsTimeoutFact(t *testing.T) {
	withAuditDir(t, nil)
	const fact = "后端已退出（超时核对：进程不存在，本次启动未成功）"
	h := &TaskHandle{ID: 999002, Name: "构建并启动后端", Timeout: 5 * time.Millisecond}
	h.TimeoutFact = func() string { return fact }
	h.terminate()

	r, set := h.Result()
	if !set || r.ErrKind != errKindTimeout {
		t.Fatalf("超时结论必须是 timeout，实际：%s", r.String())
	}
	// 迟到的任务返回不许覆盖 timeout 结论
	h.SetResult(opFail("start_service", "backend", errKindBuildFail, "go build 失败"))
	r2, _ := h.Result()
	if r2.ErrKind != errKindTimeout {
		t.Fatalf("迟到写入覆盖了超时结论：%s", r2.String())
	}

	var found bool
	for _, e := range auditTail(20) {
		if e.Action == "task_timeout" && e.Target == "构建并启动后端" {
			found = true
			if !strings.Contains(e.Detail, "超时核对") || !strings.Contains(e.Detail, fact) {
				t.Fatalf("审计里没有超时核对事实：%q", e.Detail)
			}
			if e.Result != resFail {
				t.Fatalf("task_timeout 必须是 fail，实际 %q", e.Result)
			}
		}
	}
	if !found {
		t.Fatalf("审计里没有 task_timeout 记录")
	}
}

// 核对事实一：进程已不在 → "已退出"（不允许说成"未复验"糊弄）。
func TestStartTimeoutFactExited(t *testing.T) {
	svcMu.Lock()
	old := svcState["backend"]
	svcState["backend"] = &SvcInfo{Running: false, PID: 0}
	svcMu.Unlock()
	t.Cleanup(func() {
		svcMu.Lock()
		svcState["backend"] = old
		svcMu.Unlock()
	})
	fact := startTimeoutFact("backend")
	if !strings.Contains(fact, "已退出") {
		t.Fatalf("进程不在时必须说「已退出」，实际：%q", fact)
	}
}

// 核对事实二：进程在、端口在听 → "仍在运行未复验"。
// 端口在听也只能说未复验——没过存活观察窗就不算恢复（R3）。
func TestStartTimeoutFactRunningUnverified(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听：%v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	setSvcRunning(t, "backend", os.Getpid())
	oldPortsFn := svcPortsFn
	svcPortsFn = func(string) []int { return []int{port} }
	t.Cleanup(func() { svcPortsFn = oldPortsFn })

	fact := startTimeoutFact("backend")
	if !strings.Contains(fact, "仍在运行未复验") {
		t.Fatalf("进程与端口都在时必须说「仍在运行未复验」，实际：%q", fact)
	}
	if !strings.Contains(fact, fmt.Sprintf("端口 %d", port)) {
		t.Fatalf("必须带上端口事实，实际：%q", fact)
	}
}

// 核对事实二（变体）：进程在但端口没听 → 仍是"仍在运行未复验"，
// 附上"候选端口均未监听"让人知道该去看什么。
func TestStartTimeoutFactRunningNoPort(t *testing.T) {
	setSvcRunning(t, "backend", os.Getpid())
	oldPortsFn := svcPortsFn
	svcPortsFn = func(string) []int { return []int{} }
	t.Cleanup(func() { svcPortsFn = oldPortsFn })
	fact := startTimeoutFact("backend")
	if !strings.Contains(fact, "仍在运行未复验") || !strings.Contains(fact, "均未监听") {
		t.Fatalf("实际：%q", fact)
	}
}
