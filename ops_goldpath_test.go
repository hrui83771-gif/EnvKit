package main

// ops_goldpath_test.go —— 执行层（"手"）的黄金路径测试。
//
// 这几个模块此前只有集成/评测覆盖（dbops 11%、chainip/chain 10%、program 14%），
// 日常 push 的回归保护恰好缺在这里。挑的都是纯逻辑/文件系统函数：
//   · config.toml 只改 NodeURL 地址、注释与缩进一个字节不动；
//   · 备份保留策略按时间序删旧留新，且连 .sha256/.objects 一起清；
//   · Java 版本要求与 JDK home 推导。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNodeURLParseAndRewrite(t *testing.T) {
	const content = `[[Network.Connection]]
# NodeURL="192.168.1.9:20200"   # 备用，别动
NodeURL="192.168.1.10:20200"   # 主节点
  NodeURL="oldhost"
NodeURL = "10.0.0.2:8545"
`
	lines := parseNodeURLLines(content)
	if len(lines) != 3 {
		t.Fatalf("应只收未注释的 3 行，得 %d：%+v", len(lines), lines)
	}
	if got := mismatchCount(lines, "127.0.0.1", "20200"); got != 3 {
		t.Errorf("三行都与权威源不一致，得 %d", got)
	}

	cases := []struct {
		in, want string
	}{
		{`NodeURL="192.168.1.10:20200"   # 主节点`, `NodeURL="127.0.0.1:20200"   # 主节点`},
		{"  NodeURL=\"oldhost\"", `  NodeURL="127.0.0.1"`},
		{`NodeURL = "10.0.0.2:8545"`, `NodeURL = "127.0.0.1:20200"`},
	}
	for _, c := range cases {
		if got := rebuildNodeURLLine(c.in, "127.0.0.1", "20200"); got != c.want {
			t.Errorf("改写 %q\n得 %q\n要 %q", c.in, got, c.want)
		}
	}

	// IPv6 权威源要带方括号，且能再被解析回来。
	ipv6 := `NodeURL="10.0.0.2:20200"`
	rewritten := rebuildNodeURLLine(ipv6, "::1", "20200")
	if rewritten != `NodeURL="[::1]:20200"` {
		t.Fatalf("IPv6 应写成 [::1]:20200，得 %s", rewritten)
	}
	again := parseNodeURLLines(rewritten)
	if len(again) != 1 || again[0].host != "::1" || again[0].port != "20200" {
		t.Fatalf("改写结果应可被解析回 host/port，得 %+v", again)
	}
}

func TestPruneBackupsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"farm-20260101-000000.sql",
		"farm-20260102-000000.sql",
		"farm-20260103-000000.sql",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 附属文件与无关文件：前者要跟着删，后者不许动。
	for _, n := range []string{"farm-20260101-000000.sql.sha256", "farm-20260101-000000.sql.objects.json"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "other-20260101-000000.sql"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if removed := pruneBackups(dir, "farm", 2); removed != 1 {
		t.Errorf("应只删最旧的 1 个备份，得 %d", removed)
	}
	for _, n := range []string{
		"farm-20260101-000000.sql",
		"farm-20260101-000000.sql.sha256",
		"farm-20260101-000000.sql.objects.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("旧备份及其附属文件应被删除：%s", n)
		}
	}
	for _, n := range []string{"farm-20260102-000000.sql", "farm-20260103-000000.sql", "other-20260101-000000.sql"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("不该删的文件被删了：%s", n)
		}
	}
}

func TestReadStoredSum(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "farm-20260101-000000.sql")
	if err := os.WriteFile(backup, []byte("dump"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readStoredSum(backup); got != "" {
		t.Fatalf("没有 .sha256 时应返回空，得 %q", got)
	}
	// sha256sum 的两种输出格式：仅摘要，或"摘要 + 文件名"。
	if err := os.WriteFile(backup+".sha256", []byte("ABC123  farm-20260101-000000.sql\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readStoredSum(backup); got != "ABC123" {
		t.Errorf("应取第一列摘要，得 %q", got)
	}
}

func TestJavaPathHelpers(t *testing.T) {
	// pom 声明的版本：<java.version> 与 maven.compiler.release 都认。
	dir := t.TempDir()
	pom := `<project><properties><java.version>17</java.version></properties></project>`
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(pom), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mavenRequiredJava(dir); got != "17" {
		t.Errorf("应读出 pom 的 java.version=17，得 %q", got)
	}
	if got := mavenRequiredJava(filepath.Join(dir, "nope")); got != "" {
		t.Errorf("没有 pom 应返回空，得 %q", got)
	}

	// JDK home 推导：<home>\bin\java.exe → <home>；不在 bin 下则不猜。
	jdk := filepath.Join(dir, "jdk17")
	if err := os.MkdirAll(filepath.Join(jdk, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	javaExe := filepath.Join(jdk, "bin", "java.exe")
	if err := os.WriteFile(javaExe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := javaHomeOf(javaExe); !sameFileDir(got, jdk) {
		t.Errorf("应推导出 JDK home %s，得 %s", jdk, got)
	}
	if got := javaHomeOf(filepath.Join(dir, "jdk17", "lib", "java.exe")); got != "" {
		t.Errorf("非 bin 形态不应猜 home，得 %q", got)
	}

	// 版本比较：1.8 < 17，17.0.11 不小于 17。
	if !javaVerLess("1.8.0", "17") {
		t.Error("1.8.0 应小于 17")
	}
	if javaVerLess("17.0.11", "17") {
		t.Error("17.0.11 不应小于 17")
	}
}
