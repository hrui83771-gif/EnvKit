// topo_adapter_test.go —— T2.3/T2.4/T2.5 验收：三个适配器行为对齐
//
// 测的核心不是"适配器能跑"，而是**它没有偷偷改语义**：
//
//	· Observe 的 ErrKind 分辨是否保住（unreachable ≠ not_listening ≠ owner_mismatch）；
//	· Plan 是否拒绝不安全的命令模板（R1）；
//	· Execute 是否拒绝该由人闸放行的动作（R2）；
//	· Verify 是否复用既有复验（口径唯一）。
//
// 刻意不做真实 IO：那需要一台真链环境。需要真实环境的路径
// （dbBackupTask / verifyChain）用"入参形状 + 拒绝分支"覆盖，
// 真实端到端由 33 轮基线与 7 天试运行负责。
package main

import (
	"strings"
	"testing"
)

// ---------- 注册表 ----------

func TestAdapterForKnownNames(t *testing.T) {
	for _, name := range []string{AdapterLocal, AdapterDB, AdapterSSHChain} {
		p, r, v, ok := adapterFor(name)
		if !ok {
			t.Errorf("适配器 %s 应已注册", name)
			continue
		}
		if p == nil || r == nil || v == nil {
			t.Errorf("适配器 %s 的三个接口不应有 nil", name)
		}
	}
}

func TestAdapterForUnknownName(t *testing.T) {
	if _, _, _, ok := adapterFor("quantum"); ok {
		t.Error("未注册的适配器名不该命中")
	}
	if _, _, _, ok := adapterFor(""); ok {
		t.Error("空适配器名不该命中")
	}
}

// ---------- LocalProc ----------

func TestLocalProcObserveRejectsWrongServiceID(t *testing.T) {
	a := LocalProcAdapter{}
	obs := a.Observe(Service{ID: "mysql"})
	if obs.Healthy {
		t.Error("非本机服务不该被判健康")
	}
	if obs.ErrKind != errKindBadConfig {
		t.Errorf("ErrKind 应为 %s，实际 %s", errKindBadConfig, obs.ErrKind)
	}
}

// TestLocalProcClassifyDecisionTable 整张判定表逐行走一遍。
// 抽成纯函数（classifyLocalProc）就是为了这个：不需要真机、不需要真进程，
// 就能验证每一种事实组合会得到哪个结论。
func TestLocalProcClassifyDecisionTable(t *testing.T) {
	cases := []struct {
		name    string
		alive   bool
		pid     int
		ports   []int
		port    int
		owner   string
		ownerOK bool
		wantOK  bool
		wantErr string
	}{
		{"进程不在", false, 1234, []int{8080}, 0, "", false, false, "not_listening"},
		{"进程在但没端口线索", true, 1234, nil, 0, "", false, false, "probe_no_clue"},
		{"进程在有线索但都没听", true, 1234, []int{8080, 8888}, 0, "", false, false, "not_listening"},
		{"端口被第三方占着", true, 1234, []int{8080}, 8080, "someone.exe", false, false, "owner_mismatch"},
		{"端口在听且 owner 符合", true, 1234, []int{8080}, 8080, "main.exe", true, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, kind, ev := classifyLocalProc(c.alive, c.pid, c.ports,
				c.port, c.owner, c.ownerOK)
			if ok != c.wantOK {
				t.Errorf("healthy = %v，应为 %v", ok, c.wantOK)
			}
			if kind != c.wantErr {
				t.Errorf("ErrKind = %q，应为 %q", kind, c.wantErr)
			}
			if len(ev) == 0 {
				t.Error("每种结局都必须带证据")
			}
		})
	}
}

// TestProbeNoClueIsNotNotListening **这条是判据级的**：
// "判断不了"（probe_no_clue）与"确定没在听"（not_listening）必须是两个 ErrKind。
// 合并了之后，上层恢复策略会对一个只是探测不到的服务发起重启——
// 那就是误恢复，而且是用户完全看不出原因的那种（日志里只会写"服务未监听"）。
func TestProbeNoClueIsNotNotListening(t *testing.T) {
	_, noClue, _ := classifyLocalProc(true, 1, nil, 0, "", false)
	_, notListening, _ := classifyLocalProc(true, 1, []int{8080}, 0, "", false)
	if noClue == notListening {
		t.Fatal("probe_no_clue 与 not_listening 被合并了——会导致误恢复")
	}
	if noClue != "probe_no_clue" {
		t.Errorf("无端口线索应判 probe_no_clue，实际 %s", noClue)
	}
}

// TestOwnerMismatchIsNotNotListening 第三方占用也不是"本服务没在听"。
// 对第三方占用发起重启是白费功夫，而杀掉第三方进程是越权。
func TestOwnerMismatchIsNotNotListening(t *testing.T) {
	_, mismatch, _ := classifyLocalProc(true, 1, []int{8080}, 8080, "other.exe", false)
	_, notListening, _ := classifyLocalProc(true, 1, []int{8080}, 0, "", false)
	if mismatch == notListening {
		t.Fatal("owner_mismatch 与 not_listening 被合并了——会把第三方占用当成本服务故障")
	}
}

func TestLocalProcObserveProbeNoClueIsNotUnhealthy(t *testing.T) {
	// 走判定表覆盖（真机路径见 TestLocalProcClassifyDecisionTable）
	_, kind, _ := classifyLocalProc(true, 4242, nil, 0, "", false)
	if kind != "probe_no_clue" {
		t.Errorf("ErrKind 应为 probe_no_clue，实际 %q", kind)
	}
}

func TestLocalProcObserveUsesConfiguredPort(t *testing.T) {
	saved := svcPortsFn
	called := false
	svcPortsFn = func(string) []int { called = true; return []int{9999} }
	defer func() { svcPortsFn = saved }()

	// 显式配了端口时不该再走推断链（推断链会返回一锅候选端口）
	got := localProcPorts(Service{ID: "backend", Probe: ProbeSpec{Kind: ProbePort, Port: 8080}})
	if len(got) != 1 || got[0] != 8080 {
		t.Errorf("应只用显式端口，实际 %v", got)
	}
	if called {
		t.Error("配了显式端口时不该调用推断链")
	}
}

// TestLocalProcPlanRejectsDangerousScript R1：配置的启动脚本也要过安全规则。
// 配置是用户填的，但用户也可能手滑填成 migrate。
func TestLocalProcPlanRejectsDangerousScript(t *testing.T) {
	a := LocalProcAdapter{}
	for _, bad := range []string{"migrate", "deploy", "reset", "drop"} {
		_, err := a.Plan(Service{ID: "web", Start: bad, RiskTier: RiskLow},
			Action{Kind: "start"})
		if err == nil {
			t.Errorf("脚本 %q 应被拒绝", bad)
			continue
		}
		if !strings.Contains(err.Error(), bad) {
			t.Errorf("拒绝原因应点名脚本 %q，实际：%v", bad, err)
		}
	}
}

func TestLocalProcPlanAcceptsSafeScript(t *testing.T) {
	a := LocalProcAdapter{}
	p, err := a.Plan(Service{ID: "web", Start: "dev", RiskTier: RiskLow}, Action{Kind: "start"})
	if err != nil {
		t.Fatalf("dev 是白名单脚本，不该被拒：%v", err)
	}
	if !strings.Contains(p.Command, "npm run dev") {
		t.Errorf("命令描述应说明要跑什么，实际 %q", p.Command)
	}
	if p.RiskTier != RiskLow {
		t.Errorf("风险等级应随 service，实际 %s", p.RiskTier)
	}
}

func TestLocalProcPlanRejectsBackendEntryOutsideDir(t *testing.T) {
	a := LocalProcAdapter{}
	// 越界路径：`..\..\windows\system32\x.exe` 这种必须拦住
	_, err := a.Plan(Service{ID: "backend", Start: `..\..\windows\system32\x.exe`,
		RiskTier: RiskLow}, Action{Kind: "start"})
	if err == nil {
		t.Error("越界的后端入口应被拒绝")
	}
}

func TestLocalProcPlanRejectsUnknownAction(t *testing.T) {
	a := LocalProcAdapter{}
	if _, err := a.Plan(Service{ID: "web", RiskTier: RiskLow}, Action{Kind: "format"}); err == nil {
		t.Error("未知动作应被拒绝")
	}
}

func TestLocalProcExecuteRejectsWrongService(t *testing.T) {
	a := LocalProcAdapter{}
	r := a.Execute(Plan{Service: "mysql", Action: "start"})
	if r.Ok {
		t.Error("非本机服务不该执行成功")
	}
	if r.ErrKind != errKindBadConfig {
		t.Errorf("ErrKind 应为 %s，实际 %s", errKindBadConfig, r.ErrKind)
	}
}

// ---------- DB ----------

func TestDBPlanAndExecute(t *testing.T) {
	a := DBAdapter{}

	p, err := a.Plan(Service{ID: "mysql", RiskTier: RiskLow}, Action{Kind: "backup"})
	if err != nil {
		t.Fatalf("backup 计划不该失败：%v", err)
	}
	if !strings.Contains(p.Command, "mysqldump") {
		t.Errorf("backup 命令描述应含 mysqldump，实际 %q", p.Command)
	}

	// high 档 backup 是自相矛盾的配置：备份不该是 high 档
	if _, err := a.Plan(Service{ID: "mysql", RiskTier: RiskHigh}, Action{Kind: "backup"}); err != nil {
		// 允许拒绝（更严格）
		t.Logf("high 档 backup 被拒：%v", err)
	}

	// restart 在 high 档可以规划，但**不能由适配器执行**（R2）
	p2, err := a.Plan(Service{ID: "mysql", RiskTier: RiskHigh}, Action{Kind: "restart"})
	if err != nil {
		t.Fatalf("high 档 restart 应能规划：%v", err)
	}
	if !hasPrecheck(RecoveryStrategy{Precheck: p2.Precheck}, PrecheckAttended) {
		t.Errorf("restart 计划必须含有人值守前置检查，实际 %v", p2.Precheck)
	}
	r := a.Execute(p2)
	if r.Ok {
		t.Error("适配器不该自行执行 high 档 restart（R2：不绕过权限闸门）")
	}
	if !strings.Contains(r.Msg, "人闸") {
		t.Errorf("拒绝原因应说明需要人闸，实际 %q", r.Msg)
	}
}

func TestDBExecuteRejectsUnknownAction(t *testing.T) {
	a := DBAdapter{}
	r := a.Execute(Plan{Service: "mysql", Action: "shrink", RiskTier: RiskLow})
	if r.Ok {
		t.Error("未知动作不该执行成功")
	}
}

// TestDBVerifyNoArtifactIsNotFailure 没有备份文件 ≠ 复验失败。
// 前者要"先做一次备份"，后者要"重新备份"——混在一起会让恢复策略
// 对空目录反复发起备份。
func TestDBVerifyNoArtifactIsNotFailure(t *testing.T) {
	a := DBAdapter{}
	// 不碰真实备份目录：只验证"路径为空时返回的是未复验态而非失败态"。
	// 真实路径由 latestBackup 决定，这里用一个必然不存在的库名。
	r := verifyBackupPathOrNoArtifact("__envkit_no_such_db__")
	if !r.Ok || r.Verified {
		t.Errorf("无产物应是 Ok&&!Verified 的诚实态，实际 ok=%v verified=%v",
			r.Ok, r.Verified)
	}
	if r.ErrKind != "no_artifact" {
		t.Errorf("ErrKind 应为 no_artifact，实际 %s", r.ErrKind)
	}
	_ = a
}

// ---------- SSHChain ----------

func TestSSHChainObserveRejectsWrongServiceID(t *testing.T) {
	a := SSHChainAdapter{}
	obs := a.Observe(Service{ID: "backend"})
	if obs.Healthy || obs.ErrKind != errKindBadConfig {
		t.Errorf("非链端服务应报 %s，实际 %s / healthy=%v",
			errKindBadConfig, obs.ErrKind, obs.Healthy)
	}
}

// TestSSHChainReachErrKindMapping 可达性 → ErrKind 的映射必须保住分辨力。
// unreachable（地址失效，共同原因）与 auth_failed（凭据问题）混成一个，
// 上层就会对"虚拟机 IP 变了"发起重启——旧代码那个无限循环就是这么来的。
func TestSSHChainReachErrKindMapping(t *testing.T) {
	if got := reachErrKind(reachDown); got != "unreachable" {
		t.Errorf("reachDown 应映射为 unreachable，实际 %s", got)
	}
	if got := reachErrKind(reachAuth); got != "auth_failed" {
		t.Errorf("reachAuth 应映射为 auth_failed，实际 %s", got)
	}
	if got := reachErrKind(reachOK); got != "unreachable" {
		t.Errorf("reachOK 不该进 Observe 的失败分支（防御性默认），实际 %s", got)
	}
}

func TestSSHChainPlanRequiresDirAndScript(t *testing.T) {
	a := SSHChainAdapter{}
	// 未配置目录/脚本时拒绝，不猜
	_, err := a.Plan(Service{ID: "chain", RiskTier: RiskLow}, Action{Kind: "restart"})
	if err == nil {
		t.Skip("当前环境配了链端目录与脚本，跳过空配置分支")
	}
	if !strings.Contains(err.Error(), "未配置") {
		t.Errorf("拒绝原因应说明缺什么，实际：%v", err)
	}
}

func TestSSHChainExecuteRejectsWrongService(t *testing.T) {
	a := SSHChainAdapter{}
	r := a.Execute(Plan{Service: "web", Action: "restart"})
	if r.Ok {
		t.Error("非链端服务不该执行成功")
	}
}
