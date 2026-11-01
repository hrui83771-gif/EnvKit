package main

// ai_tasklock_test.go —— v3.0 T1.2 单测：资源组互斥
//
// 锁四类行为：
//  1. 冲突判定（纯函数）：读写组合 × 组相交 × 全局任务；
//  2. beginTaskHG：跨组并行、同组互斥、多组原子获取；
//  3. AI 工具入锁：stop_service 被占用时拒（旧实现完全不锁）；
//  4. restart 全程持锁：停与起之间没有可插入的空档。

import (
	"strings"
	"testing"
	"time"
)

func TestTaskGroupsConflict(t *testing.T) {
	cases := []struct {
		name         string
		a            []string
		aWrite       bool
		b            []string
		bWrite       bool
		wantConflict bool
	}{
		{"两个只读同组不冲突", []string{grpWeb}, false, []string{grpWeb}, false, false},
		{"两个只读不同组不冲突", []string{grpWeb}, false, []string{grpDB}, false, false},
		{"同组写写冲突", []string{grpWeb}, true, []string{grpWeb}, true, true},
		{"同组读写冲突", []string{grpWeb}, true, []string{grpWeb}, false, true},
		{"同组写读冲突", []string{grpWeb}, false, []string{grpWeb}, true, true},
		{"跨组写写不冲突", []string{grpWeb}, true, []string{grpBackend}, true, false},
		{"全局写与组写冲突", nil, true, []string{grpWeb}, true, true},
		{"组写与全局写冲突", []string{grpWeb}, true, nil, true, true},
		{"全局读与组写冲突", nil, false, []string{grpWeb}, true, true},
		{"两个全局读不冲突", nil, false, nil, false, false},
		{"多组部分相交即冲突", []string{grpBackend, grpWeb}, true, []string{grpWeb, grpDB}, true, true},
		{"多组不相交不冲突", []string{grpBackend, grpWeb}, true, []string{grpChain, grpDB}, true, false},
	}
	for _, c := range cases {
		got := taskGroupsConflict(c.a, c.aWrite, c.b, c.bWrite)
		if got != c.wantConflict {
			t.Errorf("%s：got=%v want=%v", c.name, got, c.wantConflict)
		}
	}
}

func TestBeginTaskHGGroups(t *testing.T) {
	// 持 web 写锁
	h1, ok := beginTaskHG("写web", []string{grpWeb}, true)
	if !ok {
		t.Fatal("首个 web 写任务应拿到锁")
	}
	// 同组再写 → 拒
	if _, ok := beginTaskHG("再写web", []string{grpWeb}, true); ok {
		t.Error("同组第二个写任务应被拒")
	}
	// 同组读 → 拒（写任务存在时只读也拒）
	if _, ok := beginTaskHG("读web", []string{grpWeb}, false); ok {
		t.Error("写任务存在时同组只读应被拒")
	}
	// 跨组写 → 放行（T1.2 的核心：start web 与 start backend 可并行）
	h2, ok := beginTaskHG("写backend", []string{grpBackend}, true)
	if !ok {
		t.Fatal("跨组写任务应放行")
	}
	// 跨组读 → 放行
	if _, ok := beginTaskHG("读db", []string{grpDB}, false); !ok {
		t.Error("跨组只读应放行")
	}
	// 全局写 → 拒（有任何任务在跑时）
	if _, ok := beginTaskH("全局写", true); ok {
		t.Error("有任务在跑时全局写任务应被拒")
	}
	h1.Done()
	h2.Done()

	// 全部放掉后：两个只读可并行
	r1, ok1 := beginTaskHG("读a", []string{grpWeb}, false)
	r2, ok2 := beginTaskHG("读b", []string{grpWeb}, false)
	if !ok1 || !ok2 {
		t.Error("两个同组只读应可并行")
	}
	r1.Done()
	r2.Done()
}

func TestBeginTaskHGMultiGroupAtomic(t *testing.T) {
	// 拿着 web 时，要 [backend,web] 的必须整体被拒（不能拿到一半）
	hw, _ := beginTaskHG("写web", []string{grpWeb}, true)
	if _, ok := beginTaskHG("停all", []string{grpBackend, grpWeb}, true); ok {
		t.Error("多组任务有一组被占就应整体被拒")
	}
	hw.Done()
	// 空窗后整体拿到
	hall, ok := beginTaskHG("停all", []string{grpBackend, grpWeb}, true)
	if !ok {
		t.Fatal("无冲突时多组任务应整体拿到")
	}
	// 持期间：单组也要被拒（两端都占着）
	if _, ok := beginTaskHG("写backend", []string{grpBackend}, true); ok {
		t.Error("多组任务持有期间，其任一组都应互斥")
	}
	if _, ok := beginTaskHG("写web", []string{grpWeb}, true); ok {
		t.Error("多组任务持有期间，其任一组都应互斥")
	}
	hall.Done()
	if _, ok := beginTaskHG("写web", []string{grpWeb}, true); !ok {
		t.Error("释放后应可再拿")
	}
	// 清理
	busyMu.Lock()
	tasks = map[int]*TaskHandle{}
	busyMu.Unlock()
}

func TestBusyReasonNamesTask(t *testing.T) {
	h, _ := beginTaskHG("构建并启动后端", []string{grpBackend}, true)
	defer h.Done()
	got := busyReason(grpWeb)
	if got == "有任务正在执行，请稍候" {
		t.Error("busyReason 应带上当前任务名（只说'有任务'等于没说）")
	}
	if !strings.Contains(got, "构建并启动后端") {
		t.Errorf("busyReason 应包含任务名，实际 %q", got)
	}
}

// TestStopServiceTakesLock AI 停服务必须持组锁（G2：旧实现完全不锁）。
func TestStopServiceTakesLock(t *testing.T) {
	withAuditDir(t, nil)
	tool := aiToolRegistry["stop_service"]
	// 先占住 web 组
	blocker, _ := beginTaskHG("占位", []string{grpWeb}, true)
	_, err := tool.Execute(map[string]any{"service": "web"})
	if err == nil {
		t.Error("web 组被占时 stop_service(web) 应被拒")
	}
	blocker.Done()
	// 释放后能停（无进程时是 no-op，但应返回成功且留审计）
	out, err := tool.Execute(map[string]any{"service": "web"})
	if err != nil {
		t.Fatalf("锁释放后 stop_service 应成功：%v", err)
	}
	if out == "" {
		t.Error("应返回明确结论")
	}
	es := auditQuery("", "", 20)
	if !aiAuditHas(es, actAI, "stop_service", resOK, "") {
		t.Error("AI 停服务必须落审计（此前这条路径不留痕）")
	}
	// 坏参数仍然拒（且在拿锁之前就拒，不占锁）
	if _, err := tool.Execute(map[string]any{"service": "nope"}); err == nil {
		t.Error("非法 service 应报错")
	}
}

// TestRestartHoldsLockAcrossPhases 重启的"停"与"起"之间必须没有空档。
//
// 观测方式：**用钩子精确落在"已停未起"那一刻**，不用 sleep 猜。
// 原先写的是「sleep 300ms 后抢锁，应当抢不到」，前提是"stop 后的
// 等待真实存在"。但 T1.9 把盲等 2 秒改成了 restartWaitPorts（等端口
// 真正释放）—— 端口本来就空闲时它瞬间返回，窗口可能只有几微秒。
// 结果就是本机碰巧有服务占着端口才测得过，CI 上必红（run#34）。
//
// 顺带修掉一个连锁误报：原来第一条断言若意外抢到了锁却没释放，
// 第二条"结束后应释放"也会跟着红，看着像两个缺陷，其实只有一个。
func TestRestartHoldsLockAcrossPhases(t *testing.T) {
	withAuditDir(t, nil)
	// 不配前端目录：opStartWeb 会以 bad_config 快速失败，
	// 这样整段 restart 的耗时只由"停 → 等端口"决定，没有编译噪音。
	oldDir := cfg.Projects.FrontendDir
	cfg.Projects.FrontendDir = ""
	defer func() { cfg.Projects.FrontendDir = oldDir }()

	atWindow := make(chan struct{}, 1) // 已停、未起
	release := make(chan struct{}, 1)  // 测试放行
	oldNotify := restartPhaseNotify
	restartPhaseNotify = func(phase string) {
		if phase != "web-stopped" {
			return
		}
		select {
		case atWindow <- struct{}{}:
		default:
		}
		<-release
	}
	t.Cleanup(func() {
		restartPhaseNotify = oldNotify
		// 兜底放行：断言失败走 t.Fatal 时，restart 协程还卡在钩子里
		select {
		case release <- struct{}{}:
		default:
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = aiToolRegistry["restart_service"].Execute(map[string]any{"service": "web"})
	}()

	// 确认真的进入了那个窗口 —— 否则下面的断言是空转（钩子没被调到还判绿）。
	select {
	case <-atWindow:
	case <-time.After(10 * time.Second):
		t.Fatal("restart 没走到「已停未起」窗口，钩子可能没被调用")
	}

	if h, ok := beginTaskHG("插队", []string{grpWeb}, true); ok {
		h.Done() // 已抢到就立刻放掉，别把第二条断言也带红
		t.Error("restart 持锁期间同组任务必须被拒（停→起之间存在空档）")
	}

	release <- struct{}{}
	<-done

	// 结束后锁应释放
	if _, ok := beginTaskHG("插队2", []string{grpWeb}, true); !ok {
		t.Error("restart 结束后应释放组锁")
	}
	busyMu.Lock()
	tasks = map[int]*TaskHandle{}
	busyMu.Unlock()
}
