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
	svcForgetVerify("web")
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

// TestRuntimeServiceNotStartingAfterVerified v3.6.2 修「已经好了，还显示启动中」。
//
// 这条钉的是一个**互斥关系**：服务已复验通过 ⟹ 一定不在"启动中"。
//
// 起因（真实 bug）：progState["web-start"].Running 这面旗标表示"一轮启动还在进行"，
// 但旧实现只在进程退出时才清它 —— 只要进程活着，它就永远为真，
// runtimeService 于是永远判 phStarting。用户在界面上看到的就是
// "服务明明已经起来，状态灯还写着启动中"。
//
// 为什么用"已复验"当判据而不是"进程在跑"：
// 进程刚派生、端口还没起来的那几秒里，它确实既在跑又没验过 ——
// 那段时间显示"启动中"是对的。等复验过了还显示"启动中"就是错的。
// 这条断言宁可少报一次"启动中"，也不能把"已经好了"说成"还在启动"
// （把对判成错，比漏报严重得多）。
func TestRuntimeServiceNotStartingAfterVerified(t *testing.T) {
	svcMu.Lock()
	svcState["web"] = &SvcInfo{Running: true, PID: 999, Since: "2026-10-31 10:00:00"}
	svcMu.Unlock()
	// 复验通过（Verified=true）
	svcRecordVerify("web", "前端已就绪：端口 8080 在监听", "", true)
	// 但"启动中"旗标还在（模拟漏复位的老路径）
	progMu.Lock()
	progState["web-start"] = ProgStatus{Running: true, Msg: "启动中..."}
	progMu.Unlock()
	defer func() {
		progMu.Lock()
		delete(progState, "web-start")
		progMu.Unlock()
		svcMu.Lock()
		svcState["web"] = &SvcInfo{}
		svcMu.Unlock()
		svcForgetVerify("web")
	}()

	st := runtimeService("web")
	if st.Phase == phStarting {
		t.Fatal("服务已复验通过，却还报「启动中」—— 状态永远翻不了页")
	}
	if st.Phase != phRunning {
		t.Fatalf("已复验通过应为 running，实际 %q", st.Phase)
	}
	if !st.Verified {
		t.Error("复验结论已记录，Verified 应为 true")
	}
}

// TestEndStartFlagClearsStartingFlag 复验收口要真的把"启动中"旗标清掉。
//
// 这是复位动作本身：verifyService 是所有启动路径（按钮 / AI / 守护）的共同出口，
// 它收尾时必须把 progState["<target>-start"].Running 置否。
// 少了这一步，状态就永远停在"启动中"（真 bug 的根因）。
//
// 这里**走 verifyService 这条真实路径**（而不是直接调 endStartFlag）——
// 直接调只能证明那个函数本身能用，证明不了"复验收口真的调了它"。
// 真正的 bug 恰恰是"复位函数写对了，但复验收口忘了调"。
// 用一个既不是 web 也不是 backend 的 target：verifyServiceInner 会立刻判
// bad_config 返回（不碰网络、不等端口），但 svcRecordVerify + endStartFlag
// 仍会执行 —— 正是我们要验的那段收尾逻辑。
func TestEndStartFlagClearsStartingFlag(t *testing.T) {
	progMu.Lock()
	progState["webtest-start"] = ProgStatus{Running: true, Msg: "启动中..."}
	progMu.Unlock()
	defer func() {
		progMu.Lock()
		delete(progState, "webtest-start")
		progMu.Unlock()
		// svcRecordVerify 会给这个 target 建一条运行时记录。
		// 不清掉的话，后面的快照测试会多看到一个服务（跨测试污染）。
		svcRT.Lock()
		delete(svcRT.m, "webtest")
		svcRT.Unlock()
	}()

	verifyService("webtest", 0)

	progMu.Lock()
	ps := progState["webtest-start"]
	progMu.Unlock()
	if ps.Running {
		t.Error("复验收口后「启动中」旗标仍为 true —— 状态会永远卡在启动中")
	}
}

// endStartFlag 自己不该凭空造出一条记录
// （否则会把一条历史结论覆盖成"启动流程已结束"）。
func TestEndStartFlagNoopWhenNotStarting(t *testing.T) {
	progMu.Lock()
	_, exists := progState["backend-start"]
	progMu.Unlock()
	if exists {
		t.Fatal("前置条件不成立：backend-start 已存在")
	}
	endStartFlag("backend", opOK("verify_service", "backend", "不该被写进来"))
	progMu.Lock()
	_, exists = progState["backend-start"]
	progMu.Unlock()
	if exists {
		t.Error("没有正在进行的启动时不该创建旗标记录")
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

// TestEndStartFlagTriState 启动收口的三种结论必须分开落，不能都压成"失败"。
//
// 真实 bug（用户报）：45 秒窗口内没监听端口 → 程序其实已经启动好了，
// 界面却写"失败"。根因是 endStartFlag 只拿到一个 bool——"已派生但未复验"
// 与"脚本报错"在那里同形，前端只能一起画红。
func TestEndStartFlagTriState(t *testing.T) {
	cases := []struct {
		name string
		res  OpResult
		want string
	}{
		{"已复验通过", opVerified("verify_service", "web", "端口 + HTTP 正常"), progOK},
		{"已派生但未复验", opOK("verify_service", "web", "进程已派生，但 45 秒内未观察到端口监听"), progWarn},
		{"硬失败", opFail("verify_service", "web", errKindSpawnFail, "进程已退出"), progFail},
	}
	for _, c := range cases {
		progMu.Lock()
		progState["web-start"] = ProgStatus{Running: true, Status: progRun, Msg: "启动中..."}
		progMu.Unlock()

		endStartFlag("web", c.res)

		progMu.Lock()
		ps := progState["web-start"]
		progMu.Unlock()
		if ps.Running {
			t.Fatalf("%s：收口后仍标记为进行中", c.name)
		}
		if ps.Status != c.want {
			t.Errorf("%s：status = %q，期望 %q（中间态不能被说成失败）", c.name, ps.Status, c.want)
		}
		if c.want == progFail && ps.Ok {
			t.Errorf("%s：失败结论不该带 Ok=true", c.name)
		}
	}
	progMu.Lock()
	delete(progState, "web-start")
	progMu.Unlock()
}

// TestSetProgStatusNormalStop setProgStatus 的契约：正常停止是结局，不是失败。
func TestSetProgStatusNormalStop(t *testing.T) {
	setProgStatus("web-start", false, progStopped, "已停止")
	progMu.Lock()
	ps := progState["web-start"]
	progMu.Unlock()
	defer func() {
		progMu.Lock()
		delete(progState, "web-start")
		progMu.Unlock()
	}()
	if ps.Running || ps.Ok {
		t.Fatalf("正常停止不该是 running/ok：%+v", ps)
	}
	if ps.Status != progStopped {
		t.Fatalf("正常停止的 status 应为 %q，实际 %q", progStopped, ps.Status)
	}

	// 旧写法（setProg(false,false,...)）必须仍然表达"失败"，
	// 否则真失败的路径会被改坏成"已停止"。
	setProg("web-start", false, false, "npm 派生失败")
	progMu.Lock()
	ps = progState["web-start"]
	progMu.Unlock()
	if ps.Status != progFail {
		t.Fatalf("setProg(false,false) 应落 %q，实际 %q", progFail, ps.Status)
	}
}
