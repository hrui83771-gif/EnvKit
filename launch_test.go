package main

// launch_test.go —— v2.1 启动方式推断的单测
//
// 这组用例盯的是三条底线，任何一条被破坏都是"AI 能对用户项目做危险的事"：
//  1. 危险脚本名进不了候选集（白名单为准 + 黑名单兜底，黑名单优先）
//  2. 识别不出时明确失败，**绝不静默回退到 serve**
//  3. 推断不出后端入口时报错，而不是盲跑 go run main.go

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withLaunchCfg 临时替换配置里的项目目录，返回恢复函数。
func withLaunchCfg(t *testing.T, fe, be, webScript string) func() {
	t.Helper()
	oldFE, oldBE := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	oldWS := cfg.Projects.WebScript
	cfg.Projects.FrontendDir, cfg.Projects.BackendDir = fe, be
	cfg.Projects.WebScript = webScript
	return func() {
		cfg.Projects.FrontendDir, cfg.Projects.BackendDir = oldFE, oldBE
		cfg.Projects.WebScript = oldWS
	}
}

func writePkg(t *testing.T, dir, scripts string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"t","version":"1.0.0","scripts":{` + scripts + `}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// ---------- 白名单 / 黑名单 ----------

func TestLaunchScriptAllowed(t *testing.T) {
	ok := []string{"serve", "dev", "start", "watch", "dev:serve", "DEV", " start "}
	for _, s := range ok {
		if !launchScriptAllowed(s) {
			t.Errorf("%q 应当是合法的启动脚本名", s)
		}
	}
	bad := []string{"deploy", "migrate", "reset", "drop", "install", "build", "test",
		"lint", "publish", "release", "seed", "truncate", "backup", "restore", ""}
	for _, s := range bad {
		if launchScriptAllowed(s) {
			t.Errorf("%q 必须被拒绝：有副作用或破坏性语义", s)
		}
	}
}

// 黑名单优先于白名单：即便某个名字同时出现在两边也必须拒。
// 这条不能靠"维护时小心"，必须有测试钉住。
func TestLaunchDangerBeatsSafe(t *testing.T) {
	launchSafeScripts["migrate"] = true // 故意污染白名单
	defer delete(launchSafeScripts, "migrate")
	if launchScriptAllowed("migrate") {
		t.Fatal("黑名单必须优先于白名单：migrate 永远不可作为启动脚本")
	}
}

// 未知名字（既不在白名单也不在黑名单）应当拒绝——白名单是准入，不是排除。
func TestLaunchUnknownScriptRejected(t *testing.T) {
	if launchScriptAllowed("some-random-thing") {
		t.Fatal("未知脚本名必须拒绝：白名单是准入制，不在黑名单不等于放行")
	}
}

// ---------- 前端脚本推断 ----------

func TestInferWebScript(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, `"serve":"vue-cli-service serve","build":"vue-cli-service build","test":"jest"`)
	defer withLaunchCfg(t, dir, "", "")()

	script, all, src, blocked := inferWebScript(dir, "")
	if script != "serve" {
		t.Fatalf("应推断出 serve，实际 %q", script)
	}
	if src != "package.json" {
		t.Fatalf("来源应是 package.json，实际 %q", src)
	}
	if len(all) != 1 || all[0] != "serve" {
		t.Fatalf("合法候选应只有 serve，实际 %v", all)
	}
	// build / test 是黑名单，应出现在 blocked 里告知用户
	if !containsStr(blocked, "build") || !containsStr(blocked, "test") {
		t.Fatalf("build/test 应被列入 blocked 供界面提示，实际 %v", blocked)
	}
}

func TestInferWebScriptPrefersDev(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, `"serve":"x","dev":"vite","start":"y"`)
	defer withLaunchCfg(t, dir, "", "")()

	script, all, _, _ := inferWebScript(dir, "")
	if script != "dev" {
		t.Fatalf("多个候选时应优先 dev（开发期最常用），实际 %q", script)
	}
	if len(all) != 3 {
		t.Fatalf("三个合法脚本都该进候选集，实际 %v", all)
	}
}

func TestInferWebScriptColonVariant(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, `"dev:serve":"vite --mode dev"`)
	defer withLaunchCfg(t, dir, "", "")()
	if s, _, _, _ := inferWebScript(dir, ""); s != "dev:serve" {
		t.Fatalf("冒号子命令形态应被识别，实际 %q", s)
	}
}

// 全部脚本都危险时：必须返回 none + 列出被拒项，绝不挑一个"最不像的"。
func TestInferWebScriptAllDangerous(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, `"build":"webpack","deploy":"node deploy.js","test":"jest"`)
	defer withLaunchCfg(t, dir, "", "")()

	script, all, src, blocked := inferWebScript(dir, "")
	if script != "" || src != "none" {
		t.Fatalf("全是危险脚本时必须返回 none，实际 script=%q src=%q", script, src)
	}
	if len(all) != 0 {
		t.Fatalf("不应有合法候选，实际 %v", all)
	}
	if len(blocked) != 3 {
		t.Fatalf("三个被拒脚本都应报出，实际 %v", blocked)
	}
}

func TestInferWebScriptNoPackageJSON(t *testing.T) {
	dir := t.TempDir()
	defer withLaunchCfg(t, dir, "", "")()
	if s, _, src, _ := inferWebScript(dir, ""); s != "" || src != "none" {
		t.Fatalf("没有 package.json 时应返回 none，实际 %q/%q", s, src)
	}
}

// 用户配置优先于推断，但配置本身也要过安全规则。
func TestInferWebScriptConfigOverride(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, `"serve":"x","dev":"vite"`)
	defer withLaunchCfg(t, dir, "", "dev")()

	script, _, src, _ := inferWebScript(dir, "dev")
	if script != "dev" || src != "config" {
		t.Fatalf("用户配置应优先，实际 script=%q src=%q", script, src)
	}
	// 用户手滑填了危险脚本名：必须拒绝，不能因为"是用户填的"就放行
	if s, _, src2, _ := inferWebScript(dir, "migrate"); s != "" || src2 == "config" && s != "" {
		t.Fatalf("配置里的危险脚本名也必须被拒，实际 %q", s)
	}
}

// ---------- 兜底行为：这是 v1 时代"换个项目就启动不了"的根源 ----------

// package.json 存在但没有安全候选 → 必须失败，不能回退 serve。
func TestLaunchWebNoSilentServeFallback(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, `"build":"webpack","test":"jest"`)
	defer withLaunchCfg(t, dir, "", "")()

	if _, _, ok := launchWebScriptOrDefault(dir, ""); ok {
		t.Fatal("有 package.json 但无安全候选时不得回退到 serve——那正是写死时代的 bug")
	}
}

// 压根读不到 package.json（非"有但危险"）→ 允许 serve 兜底，不打断旧流程。
func TestLaunchWebFallbackWhenNoPkg(t *testing.T) {
	dir := t.TempDir()
	defer withLaunchCfg(t, dir, "", "")()

	s, src, ok := launchWebScriptOrDefault(dir, "")
	if !ok || s != "serve" || !strings.Contains(src, "默认") {
		t.Fatalf("读不到 package.json 时应兜底 serve，实际 %q/%q/%v", s, src, ok)
	}
}

// 显式传入危险脚本名 → 一律拒绝，不因为"调用方指定"就放行。
func TestLaunchWebRejectsExplicitDangerous(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, `"serve":"x","migrate":"node m.js"`)
	defer withLaunchCfg(t, dir, "", "")()

	if _, _, ok := launchWebScriptOrDefault(dir, "migrate"); ok {
		t.Fatal("显式传入危险脚本名必须被拒绝")
	}
}

// ---------- 后端入口推断 ----------

func TestInferBackendFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}
	defer withLaunchCfg(t, "", dir, "")()
	f, why := inferBackendFile(dir)
	if f != "main.go" {
		t.Fatalf("常规布局应返回 main.go，实际 %q（%s）", f, why)
	}
}

// cmd/server 布局：写死 main.go 在这里必然失败。
func TestInferBackendCmdLayout(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "cmd", "server"), 0755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x"), 0644)
	os.WriteFile(filepath.Join(dir, "cmd", "server", "main.go"), []byte("package main"), 0644)
	defer withLaunchCfg(t, "", dir, "")()

	f, why := inferBackendFile(dir)
	if f != "cmd/server/main.go" {
		t.Fatalf("cmd 布局应返回 cmd/server/main.go，实际 %q（%s）", f, why)
	}
	if !strings.Contains(why, "cmd") {
		t.Fatalf("理由里应说明这是 cmd 布局推断出来的，实际 %q", why)
	}
}

func TestInferBackendNoMain(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x"), 0644)
	defer withLaunchCfg(t, "", dir, "")()

	if f, _ := inferBackendFile(dir); f != "" {
		t.Fatalf("找不到入口时应返回空，实际 %q", f)
	}
	if _, _, ok := launchBackendFileOrDefault(dir); ok {
		t.Fatal("识别不出后端入口时不得盲跑 go run main.go")
	}
}

// 扫描要跳过噪音目录，否则带 node_modules 的仓库会走很久。
func TestFindGoEntrySkipsNoise(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "node_modules", "somepkg", "cmd")
	os.MkdirAll(deep, 0755)
	os.WriteFile(filepath.Join(deep, "main.go"), []byte("package main"), 0644)
	if f := findGoEntry(dir, 0); f != "" {
		t.Fatalf("必须跳过 node_modules，实际找到 %q", f)
	}
}

// ---------- 快照与工具层 ----------

func TestLaunchPlanSnapshotFields(t *testing.T) {
	fe := t.TempDir()
	be := t.TempDir()
	writePkg(t, fe, `"serve":"vue-cli-service serve"`)
	os.WriteFile(filepath.Join(be, "main.go"), []byte("package main"), 0644)
	defer withLaunchCfg(t, fe, be, "")()

	p := currentLaunchPlan()
	if p.WebScript != "serve" || p.WebSource != "package.json" {
		t.Fatalf("推断错误：%+v", p)
	}
	if p.BackendFile != "main.go" {
		t.Fatalf("后端入口错误：%+v", p)
	}
	brief := launchPlanBrief(p)
	if !strings.Contains(brief, "npm run serve") || !strings.Contains(brief, "go run main.go") {
		t.Fatalf("简述应同时包含前后端命令，实际 %q", brief)
	}
}

func TestLaunchPlanNoteWhenUnknown(t *testing.T) {
	fe := t.TempDir()
	be := t.TempDir()
	writePkg(t, fe, `"deploy":"node d.js"`)
	defer withLaunchCfg(t, fe, be, "")()

	p := currentLaunchPlan()
	if p.Note == "" {
		t.Fatal("识别不出时必须给出说明，否则界面与 AI 都无从下手")
	}
	if !strings.Contains(p.Note, "deploy") {
		t.Fatalf("说明里应点名被拒的脚本，实际 %q", p.Note)
	}
}

func TestLaunchScriptInCandidates(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, `"serve":"x","dev":"vite"`)
	defer withLaunchCfg(t, dir, "", "")()

	if !launchScriptInCandidates("dev") || !launchScriptInCandidates("serve") {
		t.Fatal("合法候选应被认可")
	}
	if launchScriptInCandidates("deploy") {
		t.Fatal("不在候选集内的脚本必须被拦下")
	}
}

func containsStr(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
