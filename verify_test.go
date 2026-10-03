package main

// verify_test.go —— 验证器与结果契约的单测（v2.0 P2）
//
// 这些用例盯的是"会不会再次谎报"这条底线：
//   · 产物被删、校验和被改、dump 半截 —— 都必须 fail，且带可复核的证据；
//   · 端口被别人的进程占着 —— 不许认定服务已就绪（假阳性比漏报更伤信任）；
//   · Ok / Verified 三态语义 —— 文本里必须能看出"验过"还是"没验过"。

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------- 备份复验 ----------

const backupBody = "-- MySQL dump 10.13  Distrib 8.0.33\n" +
	"-- Host: localhost    Database: farm\n" +
	"CREATE TABLE `product` (\n  `id` int NOT NULL\n);\n" +
	"INSERT INTO `product` VALUES (1);\n" +
	"-- Dump completed on 2026-10-03 10:00:00\n"

// makeBackup 在 dir 下写一份"像样"的备份（含 .sha256），返回路径。
func makeBackup(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("写备份失败：%v", err)
	}
	if body != "" {
		sum, err := fileSHA256(p)
		if err != nil {
			t.Fatalf("算 sha256 失败：%v", err)
		}
		if err := os.WriteFile(p+".sha256", []byte(fmt.Sprintf("%s  %s\n", sum, name)), 0644); err != nil {
			t.Fatalf("写校验和失败：%v", err)
		}
	}
	return p
}

func withBackupCfg(t *testing.T, dir, db string) {
	t.Helper()
	old := cfg.Projects.BackupDir
	oldDB := cfg.Projects.DBName
	cfg.Projects.BackupDir, cfg.Projects.DBName = dir, db
	t.Cleanup(func() { cfg.Projects.BackupDir, cfg.Projects.DBName = old, oldDB })
}

func TestVerifyBackupAllGreen(t *testing.T) {
	dir := t.TempDir()
	withBackupCfg(t, dir, "farm")
	p := makeBackup(t, dir, "farm-20261003-100000.sql", backupBody)

	r := verifyBackup(p)
	if !r.Ok || !r.Verified {
		t.Fatalf("完整备份应通过复验，实际：%s", r.String())
	}
	if r.Artifact != p {
		t.Fatalf("应回传产物路径，实际 %q", r.Artifact)
	}
	joined := strings.Join(r.Evidence, " ")
	for _, want := range []string{"size=", "sha256=一致", "Dump completed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("证据里应包含 %q，实际：%v", want, r.Evidence)
		}
	}
}

// V5 验收用例：备份成功后手动删掉产物 → 必须 fail，并说清缺的是哪份证据。
func TestVerifyBackupDeleted(t *testing.T) {
	dir := t.TempDir()
	withBackupCfg(t, dir, "farm")
	p := makeBackup(t, dir, "farm-20261003-100000.sql", backupBody)
	if err := os.Remove(p); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	r := verifyBackup(p)
	if r.Ok {
		t.Fatalf("产物被删必须判定失败，实际：%s", r.String())
	}
	if r.ErrKind != errKindVerifyFail {
		t.Fatalf("失败归类应为 verify_fail，实际 %q", r.ErrKind)
	}
	if !strings.Contains(r.Msg, "不存在") {
		t.Fatalf("应说明产物不存在，实际：%s", r.Msg)
	}
}

func TestVerifyBackupChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	withBackupCfg(t, dir, "farm")
	p := makeBackup(t, dir, "farm-20261003-100000.sql", backupBody)
	// 只改正文、不动校验和：模拟产物被改动/损坏
	if err := os.WriteFile(p, []byte(backupBody+"\n-- tampered\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r := verifyBackup(p)
	if r.Ok || r.ErrKind != errKindVerifyFail {
		t.Fatalf("校验和不一致必须失败并归类 verify_fail，实际：%s", r.String())
	}
}

func TestVerifyBackupTruncatedAndNoChecksum(t *testing.T) {
	dir := t.TempDir()
	withBackupCfg(t, dir, "farm")

	// 半截文件：没有 Dump completed 结尾 → 不能说"备份完整"
	p1 := makeBackup(t, dir, "farm-20261003-110000.sql", "-- MySQL dump\nCREATE TABLE `x` (id int);\n")
	if r := verifyBackup(p1); !r.Ok || r.Verified {
		t.Fatalf("半截文件不应判定为已验证，实际：%s", r.String())
	}

	// 缺 .sha256 → 无法复算比对，同样只能算"未复验"
	p2 := filepath.Join(dir, "farm-20261003-120000.sql")
	if err := os.WriteFile(p2, []byte(backupBody), 0644); err != nil {
		t.Fatal(err)
	}
	r := verifyBackup(p2)
	if !r.Ok || r.Verified {
		t.Fatalf("缺少校验和文件时不应判定为已验证，实际：%s", r.String())
	}
	if !strings.Contains(strings.Join(r.Evidence, " "), "sha256=缺失") {
		t.Fatalf("证据里应说明校验和缺失，实际：%v", r.Evidence)
	}
}

// 空库备份：没有建表语句，但 dump 头 + 完整结尾 + 校验和一致，应算有效产物而不是坏备份。
func TestVerifyBackupEmptyDatabase(t *testing.T) {
	dir := t.TempDir()
	withBackupCfg(t, dir, "farm")
	// 有些版本的 dump 头不带 "Database: xxx" 行，只剩 dump 头与 SET 语句
	body := "-- MySQL dump 10.13  Distrib 8.0.33\n-- Host: localhost\n" +
		"/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;\n" +
		"-- Dump completed on 2026-10-03 10:00:00\n"
	p := makeBackup(t, dir, "farm-20261003-140000.sql", body)
	r := verifyBackup(p)
	if !r.Ok || !r.Verified {
		t.Fatalf("空库备份不应被判为无效，实际：%s", r.String())
	}
	if !strings.Contains(strings.Join(r.Evidence, " "), "空库") {
		t.Fatalf("证据里应注明未见建表语句，实际：%v", r.Evidence)
	}
}

func TestVerifyBackupEmptyFile(t *testing.T) {
	dir := t.TempDir()
	withBackupCfg(t, dir, "farm")
	p := makeBackup(t, dir, "farm-20261003-130000.sql", "")
	r := verifyBackup(p)
	if r.Ok || r.ErrKind != errKindEmptyOutput {
		t.Fatalf("空文件应归类 empty_output，实际：%s", r.String())
	}
}

func TestLatestBackupPicksNewest(t *testing.T) {
	dir := t.TempDir()
	withBackupCfg(t, dir, "farm")
	makeBackup(t, dir, "farm-20261001-100000.sql", backupBody)
	newest := makeBackup(t, dir, "farm-20261003-100000.sql", backupBody)
	makeBackup(t, dir, "other-20261003-100000.sql", backupBody) // 别的库，不该被选中

	got := latestBackup("farm")
	if got != newest {
		t.Fatalf("应取该库最新一份，实际 %q，期望 %q", got, newest)
	}
	if r := verifyBackup(""); !r.Ok || !r.Verified {
		t.Fatalf("不传路径时应复验最新一份，实际：%s", r.String())
	}
}

func TestVerifyBackupNoArtifact(t *testing.T) {
	dir := t.TempDir()
	withBackupCfg(t, dir, "farm")
	r := verifyBackup("")
	if r.Ok || r.ErrKind != errKindEmptyOutput {
		t.Fatalf("没有任何产物时应失败并归类 empty_output，实际：%s", r.String())
	}
}

// ---------- 服务复验 ----------

func TestVerifyServiceBadTarget(t *testing.T) {
	r := verifyService("db", time.Second)
	if r.Ok || r.ErrKind != errKindBadConfig {
		t.Fatalf("非法 target 应归类 bad_config，实际：%s", r.String())
	}
}

// 服务根本没起（svcState 无记录）→ 必须按失败报，不能降级成"未复验"。
func TestVerifyServiceProcessGone(t *testing.T) {
	svcMu.Lock()
	old := svcState["backend"]
	svcState["backend"] = &SvcInfo{Running: false}
	svcMu.Unlock()
	t.Cleanup(func() {
		svcMu.Lock()
		svcState["backend"] = old
		svcMu.Unlock()
	})

	r := verifyService("backend", 2*time.Second)
	if r.Ok {
		t.Fatalf("进程已退出时必须判失败，实际：%s", r.String())
	}
	if r.ErrKind != errKindSpawnFail {
		t.Fatalf("应归类 spawn_fail，实际 %q", r.ErrKind)
	}
}

// 起一个真实监听端口，走通"端口 LISTENING"这条主路径。
func TestVerifyServiceListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听：%v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	svcMu.Lock()
	old := svcState["web"]
	svcState["web"] = &SvcInfo{Running: true, PID: os.Getpid(), URL: fmt.Sprintf("http://localhost:%d/", port)}
	svcMu.Unlock()
	t.Cleanup(func() {
		svcMu.Lock()
		svcState["web"] = old
		svcMu.Unlock()
	})

	r := verifyService("web", 8*time.Second)
	if !r.Ok {
		t.Fatalf("端口被非预期进程占着不该判动作失败（它只是没验到），实际：%s", r.String())
	}
	// 占用者是测试进程本身（不是 node.exe）：永远不许被认定成前端已就绪。
	// 这是防假阳性的底线——非预期进程占的端口会被直接排除，永远走不到"已就绪"分支。
	if r.Verified {
		t.Fatalf("端口被非预期进程占用时不应判定为已验证，实际：%s", r.String())
	}
	// 语义变化（v2.0 P3 修复）：旧实现命中别人的端口就立刻下结论"不能认定"，
	// 等待窗口形同虚设——整套启动时后端占着 8888，前端还在编译就会被误判。
	// 新实现把该端口排除后继续等，超时后如实报告"本服务端口仍未监听"并列出被排除的端口。
	joined := strings.Join(r.Evidence, " ")
	if !strings.Contains(r.Msg, "未观察到本服务端口监听") {
		t.Fatalf("应如实报告本服务端口未监听，实际：%s", r.Msg)
	}
	if !strings.Contains(joined, "excluded=") || !strings.Contains(joined, strconv.Itoa(port)) {
		t.Fatalf("证据里应列出被排除的端口 %d，实际：%v", port, r.Evidence)
	}
}

func TestSvcOwnerAcceptable(t *testing.T) {
	if !svcOwnerAcceptable("", []string{"node.exe"}) {
		t.Fatal("占用者未知时应放行（不冤枉）")
	}
	if !svcOwnerAcceptable("node.exe", []string{"node.exe"}) {
		t.Fatal("占用者匹配时应放行")
	}
	if svcOwnerAcceptable("python.exe", []string{"node.exe"}) {
		t.Fatal("占用者是别的程序时应拒绝（防假阳性）")
	}
}

func TestPortFromURL(t *testing.T) {
	cases := map[string]int{
		"http://localhost:8080/": 8080,
		"http://127.0.0.1:5173":  5173,
		"http://localhost":       0,
		"":                       0,
	}
	for in, want := range cases {
		if got := portFromURL(in); got != want {
			t.Fatalf("portFromURL(%q)=%d，期望 %d", in, got, want)
		}
	}
}

func TestFirstListeningPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听：%v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if got := firstListeningPort([]int{port + 1, port}); got != port {
		t.Fatalf("应找到监听中的端口 %d，实际 %d", port, got)
	}
	if got := firstListeningPort([]int{}); got != 0 {
		t.Fatalf("空候选应返回 0，实际 %d", got)
	}
}

// ---------- 链端复验 ----------

func TestVerifyChainUnconfigured(t *testing.T) {
	old := cfg.Chain
	cfg.Chain = ChainConfig{}
	t.Cleanup(func() { cfg.Chain = old })

	r := verifyChain(time.Second)
	if r.Ok || r.ErrKind != errKindBadConfig {
		t.Fatalf("未配置链端主机时应归类 bad_config（而不是 panic 或空指针），实际：%s", r.String())
	}
}

// ---------- 链端活性判据 ----------
//
// 旧判据是"块高在窗口内增长"。实测一条空闲链：6 秒内块高 +0，共识视图 +7——
// FISCO-BCOS 的 PBFT 无交易时只共识空块、不落盘（omitEmptyBlock=true），
// 块高静止是**正常态**。旧判据于是把健康链永久判成"未复验"，
// AI 永远只能说"已执行、还没确认成功"。下面这组用例把这个回归钉死。

// setupChainListening 让端口探测这一步通过：起一个本地监听并把链配置指过去。
// SSHHost 刻意留空——否则会去走真实 SSH，单测里不可接受。
func setupChainListening(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起本地监听失败：%v", err)
	}
	t.Cleanup(func() { ln.Close() })
	old := cfg.Chain
	cfg.Chain = ChainConfig{
		ChainHost: "127.0.0.1",
		ChainPort: ln.Addr().(*net.TCPAddr).Port,
		GroupID:   1,
	}
	t.Cleanup(func() { cfg.Chain = old })
}

func withChainFetch(t *testing.T, f func(path string) (string, error)) {
	t.Helper()
	old := webaseFetch
	webaseFetch = f
	t.Cleanup(func() { webaseFetch = old })
}

type csOpts struct {
	view, block, connected, nodeNum int64
	cfgErr, leaderFailed            bool
	omitView                        bool
}

// csJSON 伪造 consensusStatus 响应，字段一律用字符串——WeBASE 就是这么返回的。
func csJSON(o csOpts) string {
	m := map[string]any{
		"currentView":            strconv.FormatInt(o.view, 10),
		"connectedNodes":         strconv.FormatInt(o.connected, 10),
		"nodeNum":                strconv.FormatInt(o.nodeNum, 10),
		"highestblockNumber":     strconv.FormatInt(o.block, 10),
		"consensusedBlockNumber": strconv.FormatInt(o.block+1, 10),
		"cfgErr":                 strconv.FormatBool(o.cfgErr),
		"leaderFailed":           strconv.FormatBool(o.leaderFailed),
		"omitEmptyBlock":         "true",
	}
	if o.omitView {
		delete(m, "currentView")
	}
	b, _ := json.Marshal(map[string]any{"baseConsensusInfo": m})
	return string(b)
}

func syncJSON(block, known int64, syncing bool) string {
	b, _ := json.Marshal(map[string]any{
		"isSyncing":          strconv.FormatBool(syncing),
		"blockNumber":        strconv.FormatInt(block, 10),
		"knownHighestNumber": strconv.FormatInt(known, 10),
		"txPoolSize":         "0",
	})
	return string(b)
}

// TestVerifyChainIdleChainIsHealthy 核心回归：空闲链（块高纹丝不动）必须判为已恢复。
// 这是旧判据踩的坑——它只看块高，于是永远给出"未复验"。
func TestVerifyChainIdleChainIsHealthy(t *testing.T) {
	setupChainListening(t)
	call := 0
	withChainFetch(t, func(p string) (string, error) {
		switch {
		case strings.Contains(p, "consensusStatus"):
			call++
			v := int64(869110)
			if call > 1 {
				v = 869117 // 视图推进 7
			}
			return csJSON(csOpts{view: v, block: 1105, connected: 3, nodeNum: 4}), nil // 块高恒 1105
		case strings.Contains(p, "syncStatus"):
			return syncJSON(1105, 1105, false), nil
		}
		return "1105", nil
	})

	r := verifyChain(200 * time.Millisecond)
	if !r.Ok || !r.Verified {
		t.Fatalf("块高静止但共识在推进，必须判「已复验通过」，实际：%s", r.String())
	}
	if !strings.Contains(r.String(), "共识在推进") {
		t.Fatalf("结论要说清判据是共识而非块高，实际：%s", r.String())
	}
	// 证据里必须主动解释块高静止，否则看的人又会怀疑链卡死了
	if ev := strings.Join(r.Evidence, " "); !strings.Contains(ev, "静止属正常") {
		t.Fatalf("证据里必须写明块高静止属正常，实际：%v", r.Evidence)
	}
}

// TestVerifyChainConsensusStalled 共识真卡死：视图不动，必须判失败（不是"未复验"）。
// 节点活着、端口通、但共识不转，对业务而言就是死链。
func TestVerifyChainConsensusStalled(t *testing.T) {
	setupChainListening(t)
	withChainFetch(t, func(p string) (string, error) {
		switch {
		case strings.Contains(p, "consensusStatus"):
			return csJSON(csOpts{view: 500, block: 1105, connected: 3, nodeNum: 4}), nil
		case strings.Contains(p, "syncStatus"):
			return syncJSON(1105, 1105, false), nil
		}
		return "1105", nil
	})

	r := verifyChain(150 * time.Millisecond)
	if r.Ok || r.ErrKind != errKindVerifyFail {
		t.Fatalf("共识停滞应判 verify_fail，实际：%s", r.String())
	}
	if !strings.Contains(r.Msg, "未推进") {
		t.Fatalf("失败原因要指明是共识没推进，实际：%s", r.Msg)
	}
}

// TestVerifyChainUnreadableIsNotVerified 共识状态读不到时只能算"没验到"，
// 不能像旧版那样直接判 Verified——那是拿"没验到"冒充"验过了"。
func TestVerifyChainUnreadableIsNotVerified(t *testing.T) {
	setupChainListening(t)
	withChainFetch(t, func(p string) (string, error) {
		return "", fmt.Errorf("HTTP 502")
	})

	r := verifyChain(150 * time.Millisecond)
	if !r.Ok {
		t.Fatalf("端口与进程都正常，不该判失败，实际：%s", r.String())
	}
	if r.Verified {
		t.Fatalf("没验到活性就不许带验证标记，实际：%s", r.String())
	}
}

// TestVerifyChainCfgErr 节点自报配置错误：硬故障，直接失败。
func TestVerifyChainCfgErr(t *testing.T) {
	setupChainListening(t)
	withChainFetch(t, func(p string) (string, error) {
		if strings.Contains(p, "consensusStatus") {
			return csJSON(csOpts{view: 1, block: 1, connected: 3, nodeNum: 4, cfgErr: true}), nil
		}
		return "1", nil
	})
	r := verifyChain(150 * time.Millisecond)
	if r.Ok || !strings.Contains(r.Msg, "cfgErr") {
		t.Fatalf("cfgErr=true 应判失败并点名原因，实际：%s", r.String())
	}
}

// TestVerifyChainNodesDisconnected 端口只反映被探的那一个节点，
// 其他共识节点掉线必须靠 connectedNodes 发现。
func TestVerifyChainNodesDisconnected(t *testing.T) {
	setupChainListening(t)
	withChainFetch(t, func(p string) (string, error) {
		if strings.Contains(p, "consensusStatus") {
			// 4 节点只连上 1 个：1+自己=2 < 4
			return csJSON(csOpts{view: 1, block: 1, connected: 1, nodeNum: 4}), nil
		}
		return "1", nil
	})
	r := verifyChain(150 * time.Millisecond)
	if r.Ok || !strings.Contains(r.Msg, "未全部互联") {
		t.Fatalf("节点未互联应判失败，实际：%s", r.String())
	}
}

// TestVerifyChainLaggingBehind 视图推进只证明"本节点在自转"。
// 刚拉起的节点一边追块一边转共识，此时不能算完全恢复。
func TestVerifyChainLaggingBehind(t *testing.T) {
	setupChainListening(t)
	call := 0
	withChainFetch(t, func(p string) (string, error) {
		switch {
		case strings.Contains(p, "consensusStatus"):
			call++
			v := int64(100)
			if call > 1 {
				v = 108
			}
			return csJSON(csOpts{view: v, block: 900, connected: 3, nodeNum: 4}), nil
		case strings.Contains(p, "syncStatus"):
			return syncJSON(900, 1105, true), nil // 落后 205 个块
		}
		return "900", nil
	})
	r := verifyChain(150 * time.Millisecond)
	if !r.Ok {
		t.Fatalf("落后不该判失败（它在恢复中），实际：%s", r.String())
	}
	if r.Verified {
		t.Fatalf("落后于网络时不许判已恢复，实际：%s", r.String())
	}
	if !strings.Contains(r.Msg, "落后") {
		t.Fatalf("要说清是落后于网络，实际：%s", r.Msg)
	}
}

// TestVerifyChainArrayShape 兼容另一种响应形状（数组），别把老版本 WeBASE 判成读不到。
func TestVerifyChainArrayShape(t *testing.T) {
	setupChainListening(t)
	call := 0
	withChainFetch(t, func(p string) (string, error) {
		switch {
		case strings.Contains(p, "consensusStatus"):
			call++
			v := int64(10)
			if call > 1 {
				v = 16
			}
			one := map[string]any{
				"currentView": strconv.FormatInt(v, 10), "connectedNodes": "3", "nodeNum": "4",
				"highestblockNumber": "77", "cfgErr": "false", "leaderFailed": "false",
			}
			b, _ := json.Marshal([]map[string]any{one})
			return string(b), nil
		case strings.Contains(p, "syncStatus"):
			return syncJSON(77, 77, false), nil
		}
		return "77", nil
	})
	r := verifyChain(150 * time.Millisecond)
	if !r.Ok || !r.Verified {
		t.Fatalf("数组格式也应正常解析并判通过，实际：%s", r.String())
	}
}

// TestVerifyChainFallbackBlockStalled 降级路径：拿不到视图时块高静止，
// 既可能是空闲也可能是卡死——此时不许判失败（那会冤枉每条空闲的链），只能判未复验。
func TestVerifyChainFallbackBlockStalled(t *testing.T) {
	setupChainListening(t)
	withChainFetch(t, func(p string) (string, error) {
		if strings.Contains(p, "consensusStatus") {
			return csJSON(csOpts{block: 1105, connected: 3, nodeNum: 4, omitView: true}), nil
		}
		return "1105", nil
	})
	r := verifyChain(150 * time.Millisecond)
	if !r.Ok {
		t.Fatalf("降级且块高静止时不能判失败，实际：%s", r.String())
	}
	if r.Verified {
		t.Fatalf("证据不足时不许带验证标记，实际：%s", r.String())
	}
	if !strings.Contains(r.Msg, "无法区分") {
		t.Fatalf("降级结论必须说清分不清，实际：%s", r.Msg)
	}
}

// TestVerifyChainFallbackBlockGrows 降级路径里块高确实涨了，那也是硬证据。
func TestVerifyChainFallbackBlockGrows(t *testing.T) {
	setupChainListening(t)
	call := 0
	withChainFetch(t, func(p string) (string, error) {
		if strings.Contains(p, "consensusStatus") {
			return csJSON(csOpts{block: 1105, connected: 3, nodeNum: 4, omitView: true}), nil
		}
		call++
		if call > 1 {
			return "1107", nil
		}
		return "1105", nil
	})
	r := verifyChain(150 * time.Millisecond)
	if !r.Ok || !r.Verified {
		t.Fatalf("降级路径下块高增长应判通过，实际：%s", r.String())
	}
}

// TestAnyIntWeBASEShapes WeBASE 的字段有时是字符串有时是数字，两种都得认。
func TestAnyIntWeBASEShapes(t *testing.T) {
	if n, ok := anyInt("869078"); !ok || n != 869078 {
		t.Fatalf("字符串数字应解析，实际 %d %v", n, ok)
	}
	if n, ok := anyInt(float64(42)); !ok || n != 42 {
		t.Fatalf("JSON 数字应解析，实际 %d %v", n, ok)
	}
	if _, ok := anyInt(nil); ok {
		t.Fatal("字段缺失必须返回 ok=false，让调用方降级，而不是当成 0")
	}
}

// ---------- 结果契约 ----------

func TestOpResultVerifiedSemantics(t *testing.T) {
	a := opOK("start_service", "web", "进程已派生")
	if a.Verified {
		t.Fatal("opOK 不应自带验证标记")
	}
	if !strings.Contains(a.String(), "未复验") {
		t.Fatalf("未验证的结果文本里必须能看出未复验，实际：%s", a.String())
	}
	b := opVerified("start_service", "web", "已就绪", "port=8080")
	if !b.Verified {
		t.Fatal("opVerified 应带验证标记")
	}
	if !strings.Contains(b.String(), "已复验通过") {
		t.Fatalf("已验证的结果文本应明示，实际：%s", b.String())
	}
	c := b.withArtifact("D:/x/farm.sql")
	if c.Artifact == "" || !c.Verified {
		t.Fatal("withArtifact 不应丢掉验证标记")
	}
	f := opFail("db_backup", "farm", "", "炸了")
	if f.ErrKind != errKindUnknown {
		t.Fatalf("空 err_kind 应兜底为 unknown，实际 %q", f.ErrKind)
	}
}

func TestAiOpResultTextThreeStates(t *testing.T) {
	failTxt := aiOpResultText("启动", opFail("start_service", "web", errKindBuildFail, "编译失败"))
	if !strings.Contains(failTxt, "build_fail") {
		t.Fatalf("失败文本应带 err_kind，实际：%s", failTxt)
	}
	okTxt := aiOpResultText("启动", opVerified("start_service", "web", "已就绪"))
	if strings.Contains(okTxt, "禁止") {
		t.Fatalf("已验证时不应再说禁止，实际：%s", okTxt)
	}
	midTxt := aiOpResultText("启动", opOK("start_service", "web", "进程已派生"))
	if !strings.Contains(midTxt, "未通过复验") || !strings.Contains(midTxt, "禁止说已经好了") {
		t.Fatalf("未验证时必须堵死「那就当成功」的脑补，实际：%s", midTxt)
	}
}

// 审计记录要能看出"验没验、验出什么"（V6 依赖这条）。
func TestAuditEntryVerifyField(t *testing.T) {
	e := AuditEntry{Action: "verify_chain", Result: resOK, Verify: opVerified("verify_chain", "h", "在出块").String()}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["verify"]; !ok {
		t.Fatalf("审计记录应含 verify 字段，实际：%s", string(b))
	}
	if !strings.Contains(m["verify"].(string), "已复验通过") {
		t.Fatalf("verify 字段应是可读的复验结论，实际：%v", m["verify"])
	}
}

// 工具注册表里必须能找到复验工具，且是只读的（不进确认闸门）。
func TestVerifyToolRegistered(t *testing.T) {
	tool, ok := aiToolRegistry["verify_environment"]
	if !ok {
		t.Fatal("verify_environment 未注册")
	}
	if tool.Write {
		t.Fatal("复验是只读操作，不应进二次确认闸门")
	}
	names := make([]string, 0, len(aiToolRegistry))
	for n := range aiToolRegistry {
		names = append(names, n)
	}
	if len(names) < 16 {
		t.Fatalf("工具数量异常：%v", names)
	}
}

// ---------- 候选端口归属：整套启动时的误判回归 ----------

// 回归场景：start_service=all 时后端先起来占住 8888，随后前端编译。
// 前端复验的候选端口表是前后端共享的（内置默认就是 {8080, 8888}），
// 老代码第一次轮询就命中后端的 8888，发现 owner=main.exe 与预期的 node.exe 不符，
// 于是立刻判定"不能认定前端已就绪"——45 秒等待窗口形同虚设。
//
// 正确行为：把 8888 排除掉，继续等前端自己的 8080。
func TestFirstListeningPortSkipsExcluded(t *testing.T) {
	lnA := listenLocal(t) // 模拟后端端口
	defer lnA.Close()
	lnB := listenLocal(t) // 模拟前端端口
	defer lnB.Close()
	pA, pB := portOf(lnA), portOf(lnB)

	// 不排除时：两个都在监听，探测逻辑本身没问题
	if got := firstListeningPortExcept([]int{pA, pB}, nil); got != pA && got != pB {
		t.Fatalf("无排除集时应命中其中一个监听端口，实际 %d", got)
	}

	// 排除 A 之后必须落到 B——这正是"命中别人的端口后继续等"的核心语义
	if got := firstListeningPortExcept([]int{pA, pB}, map[int]string{pA: "main.exe"}); got != pB {
		t.Fatalf("排除 %d 后应命中 %d，实际 %d", pA, pB, got)
	}

	// 全部排除时必须返回 0，让调用方走"超时未复验"分支而不是误判
	if got := firstListeningPortExcept([]int{pA, pB}, map[int]string{pA: "main.exe", pB: "python.exe"}); got != 0 {
		t.Fatalf("全部排除时应返回 0，实际 %d", got)
	}
}

// 旧实现 firstListeningPort 行为不能变（有测试与其它调用方依赖）
func TestFirstListeningPortNoExclusion(t *testing.T) {
	ln := listenLocal(t)
	defer ln.Close()
	p := portOf(ln)
	if got := firstListeningPort([]int{p + 1, p}); got != p {
		t.Fatalf("应跳过未监听端口命中 %d，实际 %d", p, got)
	}
	if got := firstListeningPort([]int{p + 1}); got != 0 {
		t.Fatalf("无监听时应返回 0，实际 %d", got)
	}
}

// 超时结论必须说清"是被别的服务占了"还是"根本没监听"，否则读的人无法判断该不该再等
func TestSvcExcludedNote(t *testing.T) {
	got := svcExcludedNote(map[int]string{8888: "main.exe", 8080: ""})
	if !strings.Contains(got, "8080=未知进程") || !strings.Contains(got, "8888=main.exe") {
		t.Fatalf("排除说明不完整：%q", got)
	}
	// 端口小的排前面，输出稳定可比对
	if !strings.HasPrefix(got, "8080=") {
		t.Fatalf("端口应升序渲染，实际 %q", got)
	}
	if svcExcludedNote(nil) != "" {
		t.Fatal("空排除集应渲染为空串")
	}
}

// listenLocal 起一个本机监听器，用完由调用方 Close。
func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("无法监听本机端口：%v", err)
	}
	return ln
}

// portOf 取监听器实际占用的端口。
func portOf(ln net.Listener) int {
	return ln.Addr().(*net.TCPAddr).Port
}
