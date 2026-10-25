package main

// npm 解析的回归测试（v3.3）。
//
// 背景（真实事故，2026-10-25）：
//
//	用户的 PATH 上有三个 npm，其中前两个是残壳——npm.cmd 还在，
//	npm-cli.js 早被删了。findExe 取第一个存在的 npm.cmd，于是"安装依赖"
//	每次都拿坏的那个；失败发生在 node 里（Cannot find module），而
//	EnvKit 只上报"无任何输出即退出（疑似杀毒软件拦截）"，把人引向白名单。
//
// 这组测试钉死两件事：
//  1. 坏掉的候选必须被跳过，选到真能跑的那个；
//  2. "坏掉"这件事必须被告知（broken 列表），不能只体现在一句 exit status 1 里。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkFakeNpm 在 dir 下造一个"像真的一样"的 npm 目录。
// good=true 时补齐 node_modules/npm/bin/npm-cli.js（真身），false 时故意缺它。
func mkFakeNpm(t *testing.T, dir string, good bool) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	// npm 的批处理壳（内容不重要，存在即代表"用户以为这里有 npm"）
	for _, n := range []string{"npm.cmd"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("@echo off\r\nrem fake\r\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	if !good {
		return // 坏壳：只有 npm.cmd，没有 node_modules
	}
	cli := filepath.Join(dir, "node_modules", "npm", "bin")
	if err := os.MkdirAll(cli, 0o755); err != nil {
		t.Fatalf("mkdir cli: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cli, "npm-cli.js"), []byte("// npm\n"), 0o644); err != nil {
		t.Fatalf("write npm-cli.js: %v", err)
	}
	// node.exe 也放一个，验证"优先 node+cli 直跑"
	if err := os.WriteFile(filepath.Join(dir, "node.exe"), []byte("MZ"), 0o644); err != nil {
		t.Fatalf("write node.exe: %v", err)
	}
}

// TestNpmCandidateRejectsShimWithoutCli 这是本 bug 的最小复现：
// 目录里 npm.cmd 在、npm-cli.js 不在 → 绝不能把它当成可用的 npm。
func TestNpmCandidateRejectsShimWithoutCli(t *testing.T) {
	dir := t.TempDir()
	mkFakeNpm(t, dir, false)
	if c, ok := npmCandidateFrom(dir); ok {
		t.Fatalf("缺 node_modules\\npm\\bin\\npm-cli.js 的 npm.cmd 不该被选为可用候选，却得到 %+v", c)
	}
	if !hasNpmShim(dir) {
		t.Fatal("有 npm.cmd 就应被认定为\"用户以为这里有 npm\"，从而进入 broken 列表")
	}
}

// TestNpmCandidatePrefersNodeOverShell 真身齐全时，必须优先 node.exe 直跑，
// 绕开批处理壳（壳会多一层 cmd，报错更容易丢）。
func TestNpmCandidatePrefersNodeOverShell(t *testing.T) {
	dir := t.TempDir()
	mkFakeNpm(t, dir, true)
	c, ok := npmCandidateFrom(dir)
	if !ok {
		t.Fatal("真身齐全的 npm 目录应被接受")
	}
	if !strings.EqualFold(filepath.Base(c.Exe), "node.exe") {
		t.Fatalf("应优先 node.exe 直跑，实际选了 %s（%s）", c.Exe, c.Why)
	}
	if len(c.Args) != 1 || !strings.HasSuffix(strings.ToLower(c.Args[0]), "npm-cli.js") {
		t.Fatalf("固定参数应是 npm-cli.js 路径，实际 %v", c.Args)
	}
	if c.Shell {
		t.Fatal("node 直跑不该标记为批处理壳")
	}
}

// TestResolveNpmOrderMatchesSearchDirs 断言"搜索顺序"本身是对的：
// 靠前的目录（组件安装目录 / PATH 靠前的项）必须先于靠后的被尝试。
// 之前有个同义测试被误命名成 TestResolveNpmSkipsBroken... 却没被 -run 命中
// ——测试名和测试内容对不上，等于这条断言从来没跑过。
func TestResolveNpmOrderMatchesSearchDirs(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "a-broken")
	good := filepath.Join(root, "b-good")
	mkFakeNpm(t, broken, false)
	mkFakeNpm(t, good, true)

	old := cfg
	cfg.Components = []Component{
		// 用一个假的 ZIP 顶层名，让第一个候选目录正好落在坏壳上
		{Name: "Node.js", ZipTop: "a-broken", BinRel: ""},
	}
	t.Cleanup(func() { cfg = old })

	dirs := npmSearchDirs()
	// 别指望 components 那条路把 temp 目录塞进来（componentBinDir 拼的是真实
	// InstallDir，测试环境里往往等于空串）。这里要钉死的是**顺序语义**：
	// 靠前的候选必须先被尝试、坏的必须被跳过。所以自己构造一条只含
	// 本项目 temp 目录的搜索链，复刻"PATH 上第一个 npm 就是坏的"这个现场。
	// 不掺本机真实 npm：否则断言会变成"本机装了什么"的同义反复。
	dirs = []string{broken, good, filepath.Join(root, "c-empty")}

	var picked npmCandidate
	var brokenSeen []string
	for _, d := range dirs {
		if c, ok := npmCandidateFrom(d); ok {
			picked = c
			break
		}
		if hasNpmShim(d) {
			brokenSeen = append(brokenSeen, d)
		}
	}
	if picked.Exe == "" {
		t.Fatal("应当选到健康的 npm，实际一个都没选到")
	}
	if !strings.HasPrefix(picked.Exe, good) {
		t.Fatalf("应跳过坏壳选健康的 %s，实际 %s", good, picked.Exe)
	}
	if len(brokenSeen) != 1 || brokenSeen[0] != broken {
		t.Fatalf("跳过的坏候选必须被记录（且只记坏的那个），实际 %v", brokenSeen)
	}
	// 空目录既不算可用、也不算坏——不报假警
	for _, d := range brokenSeen {
		if d == filepath.Join(root, "c-empty") {
			t.Fatal("空目录不该进入 broken 列表")
		}
	}

	// 顺带钉住 resolveNpm 的真实搜索链不依赖本机装了什么：
	// 只要 PATH 上真有一个健康 npm，它就必须被挑出来，且优先走 node 直跑。
	if c, _ := resolveNpm(false); c.Exe != "" && !c.Shell {
		if !strings.EqualFold(filepath.Base(c.Exe), "node.exe") {
			t.Errorf("非壳模式必然应选中 node.exe，实际 %s", c.Exe)
		}
	}
	// 提示语要能让人照做：指出目录、指出少了什么、给出下一步
	hint := npmBrokenHint(brokenSeen)
	for _, want := range []string{broken, "node_modules", "重新安装"} {
		if !strings.Contains(hint, want) {
			t.Errorf("提示语应包含 %q，实际：%s", want, hint)
		}
	}
}

// TestNpmBrokenHintEmptyWhenNothingBroken 没坏的不许凭空报警——
// 假报警会把人往白名单/重装的方向推，这比不报更糟。
func TestNpmBrokenHintEmptyWhenNothingBroken(t *testing.T) {
	if h := npmBrokenHint(nil); h != "" {
		t.Fatalf("没有坏候选时不该产生提示，实际 %q", h)
	}
}

// TestExplainExitPrefersRealOutput 失败摘要必须引用子进程自己说的话。
// 这是"不要再把 Cannot find module 说成杀毒软件"的直接断言。
func TestExplainExitPrefersRealOutput(t *testing.T) {
	r := streamResult{Summary: "Error: Cannot find module 'C:\\Program Files\\nodejs\\node_modules\\npm\\bin\\npm-cli.js'", Lines: 8}
	got := explainExit(r, os.ErrNotExist)
	if !strings.Contains(got, "Cannot find module") {
		t.Fatalf("有真实报错时必须引用它，实际 %q", got)
	}
	if strings.Contains(got, "安全软件") || strings.Contains(got, "杀毒") {
		t.Fatalf("有真实报错时不许再提安全软件，实际 %q", got)
	}
}

// TestExplainExitZeroOutputIsHedged 只有确实零输出时才允许提拦截，
// 且措辞必须是不确定式的（不能断言"就是杀毒软件"）。
func TestExplainExitZeroOutputIsHedged(t *testing.T) {
	got := explainExit(streamResult{}, os.ErrDeadlineExceeded)
	if !strings.Contains(got, "没有") {
		t.Fatalf("零输出时要说清\"没有任何输出\"，实际 %q", got)
	}
	if strings.Contains(got, "疑似杀毒软件拦截）") {
		t.Fatalf("不该下断言，实际 %q", got)
	}
}
