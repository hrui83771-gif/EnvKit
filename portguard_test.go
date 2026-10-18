// portguard_test.go —— v3.0 T1.9 端口冲突安全处理（plan §3.2.2）的单测。
//
// 验收口径（plan §3.1 T1.9）：第三方占用不杀且报 owner；自有旧实例先停再起
// （等端口真释放）。核心断言全部用真实监听器实测——"没杀"不是看代码没写
// kill，而是看监听端在流程跑完之后还活着、还能accept。
package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// occPort 起一个真实监听器占用端口，返回 (端口, 关闭函数)。
// 占用者的进程名就是测试二进制（envkit.test.exe）——classifyOccupants
// 的 own/keep 名单里当然没有它，所以它永远是"第三方"。
func occPort(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听器失败：%v", err)
	}
	// 必须 accept 循环：Windows 上只 Listen 不 Accept 的端口在 netstat 里
	// 照样 LISTENING，但连接会挂起——探测只需要 LISTENING 行，无所谓；
	// 这里仍然 accept 一下，让"还活着"的断言名副其实。
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	return port, func() { _ = ln.Close() }
}

// stillAlive 端口是否仍在监听（连接成功即活着）。
func stillAlive(t *testing.T, port int) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ---------- 端口真释放等待 ----------

func TestWaitPortsFreeWhenFree(t *testing.T) {
	// 没人听的端口：立即放行，不吃满超时
	start := time.Now()
	ok, foreign := waitPortsFree([]int{19999}, 3*time.Second, nil, nil)
	if !ok {
		t.Error("空闲端口应判已释放")
	}
	if len(foreign) != 0 {
		t.Errorf("空闲端口不该有占用者：%v", foreign)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("空闲端口应立即返回，实际耗时 %v", elapsed)
	}
}

func TestWaitPortsFreeEmptyList(t *testing.T) {
	start := time.Now()
	ok, _ := waitPortsFree(nil, 3*time.Second, nil, nil)
	if !ok || time.Since(start) > time.Second {
		t.Error("ports 为空应立即放行")
	}
}

func TestWaitPortsFreeForeignReportedNotWaited(t *testing.T) {
	port, close := occPort(t)
	defer close()
	// ownNames 不含测试进程名 → 第三方：不等待，直接报
	start := time.Now()
	ok, foreign := waitPortsFree([]int{port}, 3*time.Second, []string{"node.exe", "main.exe"}, nil)
	if !ok {
		t.Error("只有第三方占用时不应判'旧进程没释放'")
	}
	if len(foreign) != 1 || foreign[0].Port != port {
		t.Fatalf("应报出该端口的第三方占用，实际 %v", foreign)
	}
	if foreign[0].PID <= 0 {
		t.Error("占用报告必须带 PID（人工处理时要用）")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("第三方占用不该被等待，实际耗时 %v", elapsed)
	}
}

func TestWaitPortsFreeOwnDyingTimesOut(t *testing.T) {
	port, close := occPort(t)
	defer close()
	// ownNames 命中测试进程名 → 自家旧进程：等到超时，判"没停透"
	start := time.Now()
	ok, _ := waitPortsFree([]int{port}, 1*time.Second, []string{"envkit.test.exe"}, nil)
	if ok {
		t.Error("自家旧进程还占着端口，不能判已释放")
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("应轮询到超时（约 1s），实际 %v 就返回了", elapsed)
	}
}

// ---------- restartWaitPorts：第三方占用不杀、报 owner ----------

func TestRestartWaitPortsReportsForeignOccupant(t *testing.T) {
	port, close := occPort(t)
	defer close()
	oldPortsFn := svcPortsFn
	svcPortsFn = func(string) []int { return []int{port} }
	t.Cleanup(func() { svcPortsFn = oldPortsFn })

	why := restartWaitPorts("web")
	if why == "" {
		t.Fatal("端口被第三方占用时必须拒绝起新进程并给出原因")
	}
	if !strings.Contains(why, "端口") || !strings.Contains(why, "占用") {
		t.Errorf("原因必须点明端口与占用，实际：%s", why)
	}
	// 红线：只报告，不杀——监听端必须还活着
	if !stillAlive(t, port) {
		t.Fatal("第三方进程被杀了！restartWaitPorts 只能报告不能结束进程")
	}
}

func TestRestartWaitPortsFreePort(t *testing.T) {
	oldPortsFn := svcPortsFn
	svcPortsFn = func(string) []int { return []int{19999} }
	t.Cleanup(func() { svcPortsFn = oldPortsFn })
	if why := restartWaitPorts("backend"); why != "" {
		t.Errorf("空闲端口应放行，实际：%s", why)
	}
}

// ---------- cleanup_processes：只停自有树 + 报告第三方（实测不杀） ----------

func TestAiCleanupProcessesReportsNotKills(t *testing.T) {
	port, close := occPort(t)
	defer close()
	oldPortsFn := svcPortsFn
	svcPortsFn = func(string) []int { return []int{port} }
	t.Cleanup(func() { svcPortsFn = oldPortsFn })
	withAuditDir(t, nil)

	msg, err := aiCleanupProcesses()
	if err != nil {
		t.Fatalf("清理入口不应报错：%v", err)
	}
	if !strings.Contains(msg, "占用") {
		t.Errorf("消息必须报告端口占用，实际：%s", msg)
	}
	// 红线实测：流程跑完，第三方监听端还活着
	if !stillAlive(t, port) {
		t.Fatal("第三方进程被杀了！cleanup 只能停自有服务 + 报告占用")
	}
}

// ---------- 红线 tripwire：procops.go 不得再出现杀第三方进程的 shell-out ----------
//
// G7 的修法不是"加约束"而是"删除"（plan §3.2.2 倾向删除）。这条断言盯着
// 别哪天有人把按目录杀 / 按名杀加回来——那种代码回归不会有任何测试
// 自然失败（它只杀第三方进程，而测试环境里没有"正确的第三方"可杀）。
func TestProcopsHasNoThirdPartyKill(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".", "procops.go"))
	if err != nil {
		t.Fatalf("读 procops.go 失败：%v", err)
	}
	for _, bad := range []string{"taskkill", "Stop-Process", "Win32_Process", "tasklist"} {
		if strings.Contains(string(src), bad) {
			t.Errorf("procops.go 出现了 %q——第三方进程杀逻辑不允许回来（自有树停止走 stopByKey/killTree，人工结束走端口诊断的二次确认）", bad)
		}
	}
}
