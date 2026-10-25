package main

// chain_probe_test.go —— 链端复验「三探针」并发化的单独回归。
//
// 背景：verify_environment 的链端复验原来把「端口」「进程数」两次 SSH 往返串着跑，
// 每次复验都白等一整轮网络延迟。改动只动采集方式（串行 → errgroup 并发），
// **判据一个都没动**，所以这里只钉两件事：
//  1. 两路探针确实是同时开始的（串行会让后一路等到超时）；
//  2. 返回值的语义与串行版逐字一致（端口错误文案、procs 的 -1 / 0 / 解析失败）。
//
// 用探针变量做接缝，不连真实 SSH——单测不该依赖外部主机。

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubProbes 临时替换两路探针，用例结束自动还原。
func stubProbes(t *testing.T, port func(int) (bool, error), procs func() (string, error)) {
	t.Helper()
	oldPort, oldProcs := probePortOpenFn, probeProcsFn
	probePortOpenFn, probeProcsFn = port, procs
	t.Cleanup(func() { probePortOpenFn, probeProcsFn = oldPort, oldProcs })
}

// 两路探针互相等对方进场：只有并发才能都通过，串行时先跑的那一路会等到超时。
func TestChainNodeProbesRunConcurrently(t *testing.T) {
	var arrived int32
	barrier := make(chan struct{})
	meet := func() error {
		if atomic.AddInt32(&arrived, 1) == 2 {
			close(barrier)
		}
		select {
		case <-barrier:
			return nil
		case <-time.After(2 * time.Second):
			return fmt.Errorf("另一路探针始终没开始（说明是串行跑的）")
		}
	}
	stubProbes(t,
		func(int) (bool, error) { return true, meet() },
		func() (string, error) { return "4", meet() },
	)

	ok, probeErr, procs := chainNodeProbes(ChainConfig{SSHHost: "127.0.0.1", ChainPort: 20200}, "127.0.0.1")
	if !ok || probeErr != "" || procs != 4 {
		t.Fatalf("并发探针结果不对：ok=%v probeErr=%q procs=%d", ok, probeErr, procs)
	}
}

// 端口探针失败时：不通过，且错误文案仍带「SSH 探测失败」前缀（AI 与界面都按它归因）。
func TestChainNodeProbesPortError(t *testing.T) {
	stubProbes(t,
		func(int) (bool, error) { return false, fmt.Errorf("dial tcp: i/o timeout") },
		func() (string, error) { return "4", nil },
	)
	ok, probeErr, _ := chainNodeProbes(ChainConfig{SSHHost: "127.0.0.1", ChainPort: 20200}, "127.0.0.1")
	if ok {
		t.Fatal("端口探测报错时不能判端口通")
	}
	if !strings.Contains(probeErr, "SSH 探测失败") {
		t.Fatalf("错误文案丢了归因前缀：%q", probeErr)
	}
}

// 进程数解析的三种真形态：正常数字 / 0（端口被别人占着）/ 解析失败（保持 -1=未探测）。
func TestChainNodeProbesParsesProcs(t *testing.T) {
	cases := []struct {
		out  string
		want int
	}{
		{" 4 ", 4},
		{"0", 0},
		{"abc", -1},
		{"", -1},
	}
	for _, tc := range cases {
		stubProbes(t,
			func(int) (bool, error) { return true, nil },
			func() (string, error) { return tc.out, nil },
		)
		ok, probeErr, procs := chainNodeProbes(ChainConfig{SSHHost: "127.0.0.1", ChainPort: 20200}, "127.0.0.1")
		if !ok || probeErr != "" {
			t.Fatalf("端口应判通：ok=%v probeErr=%q", ok, probeErr)
		}
		if procs != tc.want {
			t.Fatalf("进程数解析 %q 应为 %d，实际 %d", tc.out, tc.want, procs)
		}
	}
}

// 没配 SSH 时不走 SSH 探针：procs 保持 -1（未探测），端口改由本机直连判定。
func TestChainNodeProbesNoSSH(t *testing.T) {
	called := int32(0)
	stubProbes(t,
		func(int) (bool, error) { t.Error("没配 SSH 不该走 sshLocalPortOpen"); return false, nil },
		func() (string, error) {
			atomic.AddInt32(&called, 1)
			return "4", nil
		},
	)
	// 本机随便找一个没人监听的端口：判不通，但结论形态不变
	_, _, procs := chainNodeProbes(ChainConfig{ChainPort: 1}, "127.0.0.1")
	if procs != -1 {
		t.Fatalf("没配 SSH 时进程数应为未探测(-1)，实际 %d", procs)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("没配 SSH 时不该调用远程进程探针")
	}
}
