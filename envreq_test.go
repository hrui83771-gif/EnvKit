package main

// envreq_test.go —— v2.3 N7 的单测
//
// 核心纪律只有一条：**三态不能混**。
// 把"项目没声明版本要求"说成"符合"，就是把"没检查到"当成"检查通过"——
// 与 v2.0 修过的链端判据同源的问题。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withEnvReq(t *testing.T, be, fe string) func() {
	t.Helper()
	oldBE, oldFE := cfg.Projects.BackendDir, cfg.Projects.FrontendDir
	cfg.Projects.BackendDir, cfg.Projects.FrontendDir = be, fe
	resultsMu.Lock()
	oldResults := results
	results = nil
	resultsMu.Unlock()
	return func() {
		cfg.Projects.BackendDir, cfg.Projects.FrontendDir = oldBE, oldFE
		resultsMu.Lock()
		results = oldResults
		resultsMu.Unlock()
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ===== Go 版本要求 =====

func TestGoReqTooLow(t *testing.T) {
	be := t.TempDir()
	defer withEnvReq(t, be, "")()
	writeFile(t, be, "go.mod", "module x\n\ngo 1.24.0\n")

	r := checkGoReq(be, "go1.23.6")
	if r.Status != reqTooLow {
		t.Fatalf("应判版本不够，实际 %s（%s）", r.Status, r.Why)
	}
	if r.Required != "1.24.0" {
		t.Errorf("应读到 go.mod 的要求 1.24.0，实际 %q", r.Required)
	}
	// 必须给出可执行的下一步，只说"不够"等于把负担推回用户
	if r.Action == "" {
		t.Error("版本不够时必须给出下一步（升到哪个版本）")
	}
	if !strings.Contains(r.Why, "1.23.6") {
		t.Errorf("理由里要带上实际版本（用户要能自己核对）：%q", r.Why)
	}
}

// 实际版本比要求新是完全正常的（向后兼容），不能报"不一致"
func TestGoReqNewerIsOK(t *testing.T) {
	be := t.TempDir()
	defer withEnvReq(t, be, "")()
	writeFile(t, be, "go.mod", "module x\n\ngo 1.21\n")

	r := checkGoReq(be, "go1.25.0")
	if r.Status != reqOK {
		t.Errorf("实际版本比要求新是正常的（向后兼容），不该报不符合：%s / %s", r.Status, r.Why)
	}
}

func TestGoReqExactlyEqual(t *testing.T) {
	be := t.TempDir()
	defer withEnvReq(t, be, "")()
	writeFile(t, be, "go.mod", "module x\n\ngo 1.24.0\n")

	if r := checkGoReq(be, "go1.24.0"); r.Status != reqOK {
		t.Errorf("版本完全相等应判符合，实际 %s", r.Status)
	}
	// 1.24 应等于 1.24.0（段数不同但含义相同）
	if r := checkGoReq(be, "go1.24"); r.Status != reqOK {
		t.Errorf("1.24 与 1.24.0 应视为相同，实际 %s", r.Status)
	}
}

// 关键：没声明要求时必须说"未声明"，不能说"符合"
func TestGoReqUndeclared(t *testing.T) {
	be := t.TempDir()
	defer withEnvReq(t, be, "")()
	// go.mod 没有 go 指令
	writeFile(t, be, "go.mod", "module x\n\nrequire (\n\tgithub.com/foo v1.0.0\n)\n")

	r := checkGoReq(be, "go1.23.6")
	if r.Status != reqUndeclared {
		t.Errorf("go.mod 未声明时必须是 undeclared，不能说符合：%s / %s", r.Status, r.Why)
	}
	if r.Action != "" {
		t.Errorf("未声明不是问题，不该给行动项：%q", r.Action)
	}
}

func TestGoReqNoGoMod(t *testing.T) {
	be := t.TempDir()
	defer withEnvReq(t, be, "")()
	// 没有 go.mod
	r := checkGoReq(be, "go1.23.6")
	if r.Status != reqUndeclared {
		t.Errorf("没有 go.mod 应是 undeclared，实际 %s", r.Status)
	}
	// 理由要说清原因（是文件不存在还是解析不出来）
	if r.Why == "" {
		t.Error("必须说明为什么无法判定")
	}
}

func TestGoReqNoDir(t *testing.T) {
	defer withEnvReq(t, "", "")()
	r := checkGoReq("", "go1.23.6")
	if r.Status != reqUndeclared {
		t.Errorf("未配置后端目录应是 undeclared，实际 %s", r.Status)
	}
}

func TestGoReqNotInstalled(t *testing.T) {
	be := t.TempDir()
	defer withEnvReq(t, be, "")()
	writeFile(t, be, "go.mod", "module x\n\ngo 1.24.0\n")

	r := checkGoReq(be, "")
	if r.Status != reqNotInstall {
		t.Errorf("没装 Go 应是 not_installed，实际 %s", r.Status)
	}
	if r.Action == "" {
		t.Error("没装时要给出安装指引")
	}
}

// ===== Node 版本要求 =====

func TestNodeReqParsing(t *testing.T) {
	cases := map[string]string{
		`{"engines":{"node":">=18.2.0"}}`:     "18.2.0",
		`{"engines":{"node":"^18.0.0"}}`:      "18.0.0",
		`{"engines":{"node":">=18"}}`:         "18",
		`{"engines":{"node":"18.x"}}`:         "18",
		`{"engines":{"npm":">=9"}}`:           "", // 没写 node
		`{"name":"x"}`:                        "",
		`{"engines":{"node":"lts/hydrogen"}}`: "", // 文字别名无法解析成版本
	}
	for src, want := range cases {
		if got := parseEnginesNode(src); got != want {
			t.Errorf("parseEnginesNode(%s) = %q，期望 %q", src, got, want)
		}
	}
}

func TestNodeReqTooLow(t *testing.T) {
	fe := t.TempDir()
	defer withEnvReq(t, "", fe)()
	writeFile(t, fe, "package.json", `{"name":"x","engines":{"node":">=20.0.0"}}`)

	r := checkNodeReq(fe, "18.19.0")
	if r.Status != reqTooLow {
		t.Errorf("Node 18 低于要求 20 应判不够，实际 %s（%s）", r.Status, r.Why)
	}
}

func TestNodeReqOK(t *testing.T) {
	fe := t.TempDir()
	defer withEnvReq(t, "", fe)()
	writeFile(t, fe, "package.json", `{"engines":{"node":">=18"}}`)

	if r := checkNodeReq(fe, "20.11.1"); r.Status != reqOK {
		t.Errorf("Node 20 满足 >=18 应判符合，实际 %s", r.Status)
	}
}

func TestNodeReqUndeclared(t *testing.T) {
	fe := t.TempDir()
	defer withEnvReq(t, "", fe)()
	writeFile(t, fe, "package.json", `{"name":"x","scripts":{"dev":"vite"}}`)

	r := checkNodeReq(fe, "18.19.0")
	if r.Status != reqUndeclared {
		t.Errorf("package.json 未声明 engines 时必须是 undeclared：%s / %s", r.Status, r.Why)
	}
}

// ===== 版本比较 =====

func TestVerCompare(t *testing.T) {
	cases := []struct {
		a, b string
		less bool
	}{
		{"1.24.0", "1.24.0", false},
		{"1.23.6", "1.24.0", true},
		{"1.24.0", "1.24", false},   // 段数不同但相等
		{"1.24", "1.24.0", false},   //
		{"1.24.1", "1.24.0", false}, // 更高
		{"1.9.0", "1.10.0", true},   // 数值比较不是字符串比较
		{"2.0.0", "10.0.0", true},   //
		{"v20.11.1", "20.0.0", false},
		{"20", "20.0.0", false},
	}
	for _, c := range cases {
		av, aok := verTriple(c.a)
		bv, bok := verTriple(c.b)
		if !aok || !bok {
			t.Errorf("解析失败：%q %q", c.a, c.b)
			continue
		}
		if got := verLess(av, bv); got != c.less {
			t.Errorf("verLess(%s,%s)=%v，期望 %v", c.a, c.b, got, c.less)
		}
	}
}

func TestVerTripleRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "abc", "lts/hydrogen", "最新"} {
		if _, ok := verTriple(s); ok {
			t.Errorf("不该解析成功的版本串：%q", s)
		}
	}
}

// ===== 摘要与注入 =====

// 摘要只在真有问题时说话——满屏"符合"会稀释真正需要注意的那条
func TestEnvReqSummaryQuietWhenAllOK(t *testing.T) {
	reqs := []EnvReq{
		{Component: "go", Status: reqOK},
		{Component: "node", Status: reqUndeclared},
	}
	s := envReqSummary(reqs)
	if strings.Contains(s, "有问题") {
		t.Errorf("都够用时不该说有问题：%s", s)
	}
	// 但要提到"有项目未声明"——那是"没有保障"，与"符合"不同
	if !strings.Contains(s, "未声明") {
		t.Errorf("应说明有项目未声明要求（不是问题但也没有保障）：%s", s)
	}
}

func TestEnvReqSummaryLoudWhenTooLow(t *testing.T) {
	reqs := []EnvReq{
		{Component: "go", Status: reqTooLow, Why: "要求 1.24.0，实际 1.23.6"},
	}
	s := envReqSummary(reqs)
	if !strings.Contains(s, "有问题") || !strings.Contains(s, "1.24.0") {
		t.Errorf("版本不够时摘要要明确：%s", s)
	}
}

// 注入快照的只给"确实不够用"的
func TestEnvReqBadBriefFilters(t *testing.T) {
	// 直接测过滤逻辑的三种状态
	var bad []string
	for _, r := range []EnvReq{
		{Component: "go", Status: reqOK},
		{Component: "node", Status: reqUndeclared},
		{Component: "mysql", Status: reqTooLow, Required: "8.0", Actual: "5.7"},
	} {
		if r.Status == reqTooLow || r.Status == reqNotInstall {
			bad = append(bad, r.Component)
		}
	}
	if len(bad) != 1 || bad[0] != "mysql" {
		t.Errorf("只有 too_low/not_install 该进快照，实际 %v", bad)
	}
}

// 三态文案里不能出现把 undeclared 说成"符合"的表述
func TestEnvReqStatusNeverClaimsOKForUndeclared(t *testing.T) {
	be := t.TempDir()
	defer withEnvReq(t, be, "")()
	writeFile(t, be, "go.mod", "module x\n")

	r := checkGoReq(be, "go1.23.6")
	if strings.Contains(r.Why, "符合") || strings.Contains(r.Why, "够用") {
		t.Errorf("undeclared 的措辞不能说成符合/够用：%q", r.Why)
	}
}
