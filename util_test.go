package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// ---------- 版本比较 / 版本提取 ----------

func TestVerCmp(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.23.6", "1.23.3", 1},
		{"1.23.3", "1.23.6", -1},
		{"8.0.40", "8.0.40", 0},
		{"22.11.0", "22.22.2", -1},
		{"22.22.2", "22.11.0", 1},
		{"1.6", "1.6.0", 0},   // 缺位按 0 补齐
		{"1.6.1", "1.6", 1},   // 1 > 0
		{"2.0", "10.0", -1},   // 按数值比较，不是字符串比较
		{"1.23.6", "1.23", 1}, // 1 > 0
	}
	for _, c := range cases {
		if got := verCmp(c.a, c.b); got != c.want {
			t.Errorf("verCmp(%q,%q)=%d, 期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestVersionFromURL(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://go.dev/dl/go1.23.6.windows-amd64.zip", "1.23.6"},
		{"https://npmmirror.com/mirrors/node/v22.22.0/node-v22.22.0-win-x64.zip", "22.22.0"},
		{"https://cdn.mysql.com/Downloads/MySQL-8.0/mysql-8.0.40-winx64.zip", "8.0.40"},
		{"https://example.com/no-version-here.zip", ""},
	}
	for _, c := range cases {
		if got := versionFromURL(c.url); got != c.want {
			t.Errorf("versionFromURL(%q)=%q, 期望 %q", c.url, got, c.want)
		}
	}
}

// ---------- 文本工具 ----------

func TestFirstLineAndFirstLines(t *testing.T) {
	if got := firstLine("a\nb"); got != "a" {
		t.Errorf("firstLine 多行=%q", got)
	}
	if got := firstLine("abc"); got != "abc" {
		t.Errorf("firstLine 单行=%q", got)
	}
	if got := firstLines("abcdef", 3); got != "abc..." {
		t.Errorf("firstLines 截断=%q", got)
	}
	if got := firstLines("ab", 3); got != "ab" {
		t.Errorf("firstLines 不截断=%q", got)
	}
}

func TestStripANSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[32mOK\x1b[0m", "OK"},
		{"plain", "plain"},
		{"\x1b]0;title\x07hi", "hi"},
	}
	for _, c := range cases {
		if got := stripANSI(c.in); got != c.want {
			t.Errorf("stripANSI(%q)=%q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestReOfCaches(t *testing.T) {
	a := reOf(`^\d+$`)
	b := reOf(`^\d+$`)
	if a != b {
		t.Error("reOf 应返回同一份缓存实例（避免每轮对话重复编译）")
	}
	if !a.MatchString("123") || a.MatchString("12a") {
		t.Error("reOf 返回的正则行为不对")
	}
}

// ---------- 路径变体 ----------

func TestDirVariants(t *testing.T) {
	if got := dirVariants("", "   "); len(got) != 0 {
		t.Errorf("空目录应被忽略，实际 %v", got)
	}
	got := dirVariants(`D:\a\b`)
	if len(got) != 2 || got[0] != `D:\a\b` || got[1] != `D:/a/b` {
		t.Errorf("反斜杠路径变体不对：%v", got)
	}
	got = dirVariants(`D:/a/b/`)
	if len(got) != 2 || got[0] != `D:/a/b` || got[1] != `D:\a\b` {
		t.Errorf("正斜杠+尾斜杠变体不对：%v", got)
	}
	if got := dirVariants(`D:\a\b`, `D:/a/b`); len(got) != 2 {
		t.Errorf("两个等价目录应去重为 2 个变体，实际 %v", got)
	}
}

// ---------- 子进程重试策略（不真正起进程） ----------

func TestRunRetryZeroOutputThenWrap(t *testing.T) {
	var wraps []bool
	hasOut, err := runRetry(retryPolicy{
		Attempts: 3, Backoff: time.Millisecond, WrapLast: true,
		Scope: scSys, Tag: "测试",
	}, func(wrap bool) (bool, error) {
		wraps = append(wraps, wrap)
		if wrap {
			return true, nil // cmd 包装后成功
		}
		return false, os.ErrDeadlineExceeded // 零输出秒退
	})
	if err != nil || !hasOut {
		t.Fatalf("最后一次包装成功时不应返回错误：hasOut=%v err=%v", hasOut, err)
	}
	want := []bool{false, false, true}
	if len(wraps) != len(want) {
		t.Fatalf("尝试次数 %d，期望 %d", len(wraps), len(want))
	}
	for i := range want {
		if wraps[i] != want[i] {
			t.Fatalf("第 %d 次尝试 wrap=%v，期望 %v", i+1, wraps[i], want[i])
		}
	}
}

func TestRunRetryOutputMeansRealError(t *testing.T) {
	n := 0
	_, err := runRetry(retryPolicy{Attempts: 3, Backoff: time.Millisecond, Scope: scSys, Tag: "测试"},
		func(bool) (bool, error) {
			n++
			return true, os.ErrInvalid // 有输出 = 真实报错
		})
	if err == nil {
		t.Fatal("真实报错应原样返回")
	}
	if n != 1 {
		t.Fatalf("有输出时不应重试，实际尝试 %d 次", n)
	}
}

func TestRunRetryAnyErr(t *testing.T) {
	n := 0
	_, err := runRetry(retryPolicy{Attempts: 2, Backoff: time.Millisecond, AnyErr: true, Scope: scSys, Tag: "测试"},
		func(bool) (bool, error) {
			n++
			return true, os.ErrInvalid
		})
	if err == nil || n != 2 {
		t.Fatalf("AnyErr 策略应重试到用满次数：n=%d err=%v", n, err)
	}
}

// ---------- 配置合并（防"改一个输入框就把 API Key 抹掉"） ----------

func TestMergeFormConfigKeepsMissingSections(t *testing.T) {
	cur := Config{
		AppName:    "EnvKit",
		AppTitle:   "助手",
		AI:         &AIConfig{Model: "kimi-k3", APIKey: "sk-xxx", BaseURL: "https://x/v1"},
		Components: []Component{{Name: "Go"}, {Name: "Node.js"}},
		Chain:      ChainConfig{SSHHost: "1.2.3.4", ChainDir: "/root/fisco"},
	}
	// 前端表单的形状：只有界面上的字段（历史上没有 ai）
	form := Config{InstallDir: `C:\EnvKit\tools`}

	got := mergeFormConfig(form, cur)
	if got.AI == nil || got.AI.APIKey != "sk-xxx" || got.AI.Model != "kimi-k3" {
		t.Fatalf("表单未提交 ai 时应保留原 AI 配置，实际 %+v", got.AI)
	}
	if len(got.Components) != 2 {
		t.Fatalf("表单未提交 components 时应保留原组件表，实际 %d 项", len(got.Components))
	}
	if got.Chain.SSHHost != "1.2.3.4" {
		t.Fatalf("表单未提交链端配置时应保留，实际 %+v", got.Chain)
	}
	if got.AppName != "EnvKit" || got.AppTitle != "助手" {
		t.Fatalf("标题字段被清空：%q %q", got.AppName, got.AppTitle)
	}
	if got.InstallDir != `C:\EnvKit\tools` {
		t.Fatalf("表单显式提交的字段应生效，实际 %q", got.InstallDir)
	}

	// 表单显式带了 ai（新前端会回传 cfg.ai）→ 以表单为准
	form2 := Config{AI: &AIConfig{Model: "glm-4"}}
	if got2 := mergeFormConfig(form2, cur); got2.AI == nil || got2.AI.Model != "glm-4" {
		t.Fatalf("表单显式提交 ai 时应以表单为准，实际 %+v", got2.AI)
	}
	// 表单带了 components → 以表单为准
	form3 := Config{Components: []Component{{Name: "MySQL"}}}
	if got3 := mergeFormConfig(form3, cur); len(got3.Components) != 1 || got3.Components[0].Name != "MySQL" {
		t.Fatalf("表单显式提交 components 时应以表单为准，实际 %+v", got3.Components)
	}
}

// ---------- 命令构造 ----------

func TestBuildCmdShape(t *testing.T) {
	t.Setenv("ENVKIT_INJECT_FAIL", "") // 关掉失败注入
	resetInjectFail()

	plain := buildCmdDetached(false, "go", "version")
	if plain.Args[0] != "go" || len(plain.Args) != 2 {
		t.Errorf("普通命令构造不对：%v", plain.Args)
	}
	if plain.Cancel != nil {
		t.Error("长驻命令不应绑定超时取消（会被误杀）")
	}

	wrapped := buildCmdDetached(true, "go", "version")
	if wrapped.Args[0] != "cmd" || wrapped.Args[1] != "/c" || wrapped.Args[2] != "call" || wrapped.Args[3] != "go" {
		t.Errorf("包装命令构造不对：%v", wrapped.Args)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if buildCmd(ctx, false, "go").Cancel == nil {
		t.Error("带 ctx 的命令应绑定取消函数")
	}
}

// TestInjectFailExec 验证失败注入开关真的能造出"零输出秒退"，且额度用尽后自动恢复。
func TestInjectFailExec(t *testing.T) {
	t.Setenv("ENVKIT_INJECT_FAIL", "1")
	resetInjectFail()

	out, err := buildCmdDetached(false, "go", "version").CombinedOutput()
	if err == nil {
		t.Error("注入后应返回非零退出码")
	}
	if len(out) != 0 {
		t.Errorf("注入应零输出，实际 %q", out)
	}
	// 额度已用尽 → 恢复成正常命令（这里只看构造出的命令形状，避免依赖真实 go 是否可用）
	after := buildCmdDetached(false, "go", "version")
	if after.Args[0] != "go" {
		t.Errorf("额度用尽后应恢复正常命令，实际 %v", after.Args)
	}
	resetInjectFail()
}
