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
		t.Fatalf("端口在监听时不应判失败，实际：%s", r.String())
	}
	joined := strings.Join(r.Evidence, " ")
	if !strings.Contains(joined, "LISTENING") || !strings.Contains(joined, strconv.Itoa(port)) {
		t.Fatalf("证据里应有端口与 LISTENING，实际：%v", r.Evidence)
	}
	// 占用者是测试进程本身（不是 node.exe）：不许被认定成前端已就绪
	if r.Verified {
		t.Fatalf("端口被非预期进程占用时不应判定为已验证，实际：%s", r.String())
	}
	if !strings.Contains(r.Msg, "不能认定") {
		t.Fatalf("应说明无法认定服务已就绪，实际：%s", r.Msg)
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
