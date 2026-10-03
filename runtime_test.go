package main

// runtime_test.go —— v2.2 M11 统一状态源单测
//
// 重点不是"字段齐不齐"，而是两条：
//  1. **issues 必须可执行**——每条都要带"该怎么做"，只说"有问题"等于把负担推回用户
//  2. **running ≠ verified**——状态要能表达"进程在但没验过"，
//     这正是"执行不等于成功"在状态上的体现

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rtIssue(issues []Issue, level, scope string) *Issue {
	for i := range issues {
		if issues[i].Level == level && issues[i].Scope == scope {
			return &issues[i]
		}
	}
	return nil
}

// ===== issues 的可执行性 =====

func TestRuntimeIssues_HasAction(t *testing.T) {
	env := &EnvInfo{HasResults: true, Ready: true}
	proj := ProjInfo{FrontendDir: `D:\p\web`, BackendDir: `D:\p\server`,
		Web: "dev", WebSource: "package.json", Backend: "main.go"}
	svcs := map[string]ServiceState{
		"web":     {Phase: phDegraded, Running: true},
		"backend": {Phase: phFailed, Running: false, LastErr: "panic: out of memory"},
	}
	issues := runtimeIssues(env, proj, svcs, DBHealth{Connected: true, DBExists: true},
		ChainInfo{Checked: true, Port20200: true, WebaseOK: true})

	if len(issues) == 0 {
		t.Fatal("上述状态明显有问题，不该返回空 issues")
	}
	for _, is := range issues {
		if is.What == "" {
			t.Errorf("issue 缺 What：%+v", is)
		}
		if is.Action == "" {
			t.Errorf("issue [%s] %s 缺 Action——只说有问题不说怎么办等于把负担推回用户",
				is.Level, is.What)
		}
	}
	// 错误级别要排在最前
	if issues[0].Level != rtIssError {
		t.Errorf("error 级应排最前，实际首位是 %s", issues[0].Level)
	}
}

// healthyProj 造一个"一切正常"的项目：目录真实存在且已装依赖。
//
// 早先这个测试用 `D:\p\web` 这种假路径，于是 dirHasNodeModules 必然返回 false，
// 报出"依赖未安装"——测试失败暴露出 fixture 不真实，但顺带也暴露了
// issues 计算里重复做磁盘遍历的问题（已改为在 runtimeProject 里查一次）。
func healthyProj(t *testing.T) ProjInfo {
	t.Helper()
	web := t.TempDir()
	if err := os.MkdirAll(filepath.Join(web, "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	return ProjInfo{
		FrontendDir: web, BackendDir: t.TempDir(),
		Web: "dev", WebSource: "package.json", Backend: "main.go",
		WebDepsInstalled: true,
	}
}

func TestRuntimeIssues_AllHealthy(t *testing.T) {
	env := &EnvInfo{HasResults: true, Ready: true}
	proj := healthyProj(t)
	svcs := map[string]ServiceState{
		"web":     {Phase: phRunning, Running: true, Verified: true},
		"backend": {Phase: phRunning, Running: true, Verified: true},
	}
	issues := runtimeIssues(env, proj, svcs, DBHealth{Connected: true, DBExists: true},
		ChainInfo{Checked: true, Port20200: true, WebaseOK: true})
	for _, is := range issues {
		t.Errorf("一切正常却报了 issue：[%s] %s", is.Level, is.What)
	}
}

// 目录不可读时不该报"依赖缺失"——那会让人去装一个不存在的项目的依赖
func TestRuntimeIssues_UnknownDirNotReported(t *testing.T) {
	env := &EnvInfo{HasResults: true, Ready: true}
	proj := ProjInfo{FrontendDir: `D:\不存在的项目\web`, BackendDir: t.TempDir(),
		Web: "dev", WebSource: "package.json", Backend: "main.go",
		WebDepsUnknown: true}
	issues := runtimeIssues(env, proj,
		map[string]ServiceState{"web": {Phase: phStopped}, "backend": {Phase: phStopped}},
		DBHealth{Connected: true, DBExists: true}, ChainInfo{})
	for _, is := range issues {
		if strings.Contains(is.What, "依赖未安装") {
			t.Errorf("目录不可读时不该断言依赖缺失（会误导人去 npm install）：%q", is.What)
		}
	}
}

// 识别不出启动方式是最容易让人卡住的一类问题，必须单列并给手动入口
func TestRuntimeIssues_UndetectableLaunch(t *testing.T) {
	env := &EnvInfo{HasResults: true, Ready: true}
	proj := ProjInfo{FrontendDir: `D:\p\web`, BackendDir: `D:\p\server`,
		WebSource: "none", Kinds: []string{"react"}}
	svcs := map[string]ServiceState{
		"web": {Phase: phStopped}, "backend": {Phase: phStopped},
	}
	issues := runtimeIssues(env, proj, svcs, DBHealth{}, ChainInfo{})

	var found *Issue
	for i := range issues {
		if strings.Contains(issues[i].What, "识别不出前端启动脚本") {
			found = &issues[i]
		}
	}
	if found == nil {
		t.Fatalf("识别不出启动脚本必须报出来，实际：%+v", issues)
	}
	if !strings.Contains(found.What, "react") {
		t.Errorf("应带上项目类型帮助判断，实际 %q", found.What)
	}
	if !strings.Contains(found.Action, "启动方式") {
		t.Errorf("Action 必须指向「启动方式」设置项，实际 %q", found.Action)
	}
}

// 链端不可达只是 warn 不是 error：绝大多数情况是"本机虚拟机没开"，
// 报成 error 会让人以为链坏了。
func TestRuntimeIssues_ChainIsWarnNotError(t *testing.T) {
	env := &EnvInfo{HasResults: true, Ready: true}
	proj := ProjInfo{FrontendDir: `D:\p\web`, BackendDir: `D:\p\server`,
		Web: "dev", WebSource: "package.json", Backend: "main.go"}
	svcs := map[string]ServiceState{
		"web": {Phase: phStopped}, "backend": {Phase: phStopped},
	}
	issues := runtimeIssues(env, proj, svcs, DBHealth{}, ChainInfo{Checked: true})
	if issues == nil {
		t.Skip("依赖检查可能产生了其他 issue")
	}
	for _, is := range issues {
		if is.Scope == "chain" && is.Level == rtIssError {
			t.Errorf("链端不可达不该是 error 级：%q", is.What)
		}
	}
}

// 未检测环境只是 info，不该报错吓人
func TestRuntimeIssues_NotCheckedIsInfo(t *testing.T) {
	issues := runtimeIssues(&EnvInfo{HasResults: false},
		ProjInfo{FrontendDir: `D:\p\web`, BackendDir: `D:\p\server`,
			Web: "dev", WebSource: "package.json", Backend: "main.go"},
		map[string]ServiceState{"web": {Phase: phStopped}, "backend": {Phase: phStopped}},
		DBHealth{Connected: true, DBExists: true}, ChainInfo{})
	is := rtIssue(issues, rtIssInfo, "env")
	if is == nil {
		t.Fatalf("未检测环境应给 info 提示，实际：%+v", issues)
	}
	if !strings.Contains(is.Action, "检测") {
		t.Errorf("Action 应指向检测动作，实际 %q", is.Action)
	}
}

// ===== running ≠ verified =====

// 进程在跑但没复验过 → degraded，并给出"别当作已就绪"的提示。
// 这是本项目核心主张在状态上的直接体现。
func TestRuntimeServiceDegradedWhenNotVerified(t *testing.T) {
	oldFE, oldBE := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	cfg.Projects.FrontendDir, cfg.Projects.BackendDir = t.TempDir(), t.TempDir()
	svcMu.Lock()
	svcState["web"] = &SvcInfo{Running: true, PID: 1234, Since: "2026-10-03 10:00:00"}
	svcState["backend"] = &SvcInfo{Running: false}
	svcMu.Unlock()
	svcForgetVerify("web")
	progMu.Lock()
	delete(progState, "web-start")
	progMu.Unlock()
	defer func() {
		cfg.Projects.FrontendDir, cfg.Projects.BackendDir = oldFE, oldBE
		svcMu.Lock()
		svcState["web"] = &SvcInfo{}
		svcMu.Unlock()
	}()

	st := runtimeService("web")
	if st.Phase != phDegraded {
		t.Fatalf("进程在跑但未复验应为 degraded，实际 %q", st.Phase)
	}
	if st.Verified {
		t.Error("未复验不得标记 Verified")
	}

	// 记一次通过的复验 → 升级为 running
	svcRecordVerify("web", "前端已就绪：端口 8080 在监听且进程持续存活 5s", "", true)
	st2 := runtimeService("web")
	if st2.Phase != phRunning {
		t.Fatalf("复验通过后应为 running，实际 %q（err=%s）", st2.Phase, st2.LastErr)
	}
	if !st2.Verified {
		t.Error("复验通过应标记 Verified")
	}
	// 判定必须靠布尔而不是文案：换一种说法也不该影响状态
	svcRecordVerify("web", "服务运行正常", "", true)
	if !runtimeService("web").Verified {
		t.Error("Verified 判定不该依赖结论文本措辞（早先靠匹配「已复验通过」，换说法就误判）")
	}
}

func TestRuntimeServiceStoppedPhase(t *testing.T) {
	svcMu.Lock()
	svcState["web"] = &SvcInfo{Running: false}
	svcMu.Unlock()
	svcForgetVerify("web")
	st := runtimeService("web")
	if st.Phase != phStopped {
		t.Fatalf("未运行应为 stopped，实际 %q", st.Phase)
	}
}

func TestRuntimeServiceStartingPhase(t *testing.T) {
	svcMu.Lock()
	svcState["web"] = &SvcInfo{Running: false}
	svcMu.Unlock()
	progMu.Lock()
	progState["web-start"] = ProgStatus{Running: true}
	progMu.Unlock()
	defer func() {
		progMu.Lock()
		delete(progState, "web-start")
		progMu.Unlock()
	}()

	if st := runtimeService("web"); st.Phase != phStarting {
		t.Fatalf("启动任务进行中应为 starting，实际 %q", st.Phase)
	}
}

// 复验失败要记下错误归类，供状态中心显示"为什么失败"
func TestRuntimeServiceRecordsVerifyError(t *testing.T) {
	svcRecordVerify("backend", "后端在启动成功后的 5s 内崩溃退出", errKindCrash, false)
	kind, ok := svcLastVerifyErr("backend")
	if kind != errKindCrash {
		t.Fatalf("应记下错误归类 %s，实际 %q", errKindCrash, kind)
	}
	if ok {
		t.Error("失败复验的 ok 应为 false")
	}
	if svcLastVerify("backend") == "" {
		t.Error("失败复验也要留结论")
	}
	// 清掉
	svcForgetVerify("backend")
	if svcLastVerify("backend") != "" {
		t.Error("停止服务后应清掉复验记录——留着旧的会让下次误以为验过")
	}
}

// ===== 兼容层 =====

// /api/health 保留旧字段形状，CDP 测试与既有前端不能因此崩
func TestHealthServicesCompatShape(t *testing.T) {
	got := healthServicesCompat(map[string]ServiceState{
		"web": {Running: true, PID: 42, Since: "2026-10-03 10:00:00", Port: 8080,
			Phase: phRunning, Verified: true},
	})
	w, ok := got["web"]
	if !ok {
		t.Fatal("应包含 web")
	}
	if !w.Running || w.PID != 42 || w.Since == "" {
		t.Errorf("旧字段未正确回填：%+v", w)
	}
	if w.URL != "http://127.0.0.1:8080" {
		t.Errorf("URL 应由端口回填，实际 %q", w.URL)
	}
}

func TestRuntimeIssueSummary(t *testing.T) {
	if runtimeIssueSummary(nil) != "" {
		t.Error("nil 状态应返回空串")
	}
	s := &RuntimeState{Issues: []Issue{
		{Level: "error", Scope: "db", What: "数据库未连接", Action: "检查 MySQL 服务"},
	}}
	got := runtimeIssueSummary(s)
	if !strings.Contains(got, "数据库未连接") || !strings.Contains(got, "检查 MySQL 服务") {
		t.Errorf("摘要应同时包含问题与下一步，实际 %q", got)
	}
}

func TestRuntimeStateShape(t *testing.T) {
	runtimeInvalidateCache()
	st := currentRuntimeState()
	if st == nil {
		t.Fatal("状态源不应返回 nil")
	}
	if st.ObservedAt == "" {
		t.Error("必须有 observed_at（调用方要能判断数据新鲜度）")
	}
	if st.Services == nil {
		t.Error("services 不该为 nil（前端直接遍历）")
	}
	if st.Issues == nil {
		t.Error("issues 不该为 nil（前端直接遍历，null 会让 forEach 报错）")
	}
	// 二次调用应命中缓存且内容稳定
	st2 := currentRuntimeState()
	if st2.Env.Ready != st.Env.Ready {
		t.Error("慢变部分在缓存期内不应变化")
	}
}
