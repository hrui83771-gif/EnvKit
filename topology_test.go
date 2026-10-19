// topology_test.go —— T2.1/T2.2 验收（plan §4.6）
//
// 三条红线的测法刻意选"读客观事实"而不是"读注释声称"：
//
//		R1  Probe 无执行入口 / Recoverer 不吃任意字符串
//		    → 直接读 topology.go 源码，断言它不 import os/exec、不出现 exec.Command；
//		      再用反射核对 Execute 的入参类型是 Plan 而不是 string。
//		R3  Verifier 三态可区分
//	     → 造三种 OpResult，断言三者互不相同（合并了就一定相等）。
//
// 为什么值得这么写：靠"代码里写了注释说我没提供任意执行入口"来验收，
// 得到的是文档一致，不是安全属性。
package main

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// ---------- T2.2：三条红线的客观检查 ----------

// TestTopologyFileHasNoExecImport R1：拓扑抽象文件不得引入进程执行能力。
// 探针/适配器的契约文件里出现 os/exec，就意味着"采集"或"规划"层
// 多了一条绕过 Plan 安全核查的直达通道。
func TestTopologyFileHasNoExecImport(t *testing.T) {
	src, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("读不到 topology.go：%v", err)
	}
	s := string(src)
	if strings.Contains(s, `"os/exec"`) {
		t.Error("topology.go 引入了 os/exec——抽象层不应持有进程执行能力（R1）")
	}
	if strings.Contains(s, "exec.Command") {
		t.Error("topology.go 出现 exec.Command——命令必须经适配器与 Plan 的安全核查（R1）")
	}
}

// TestRecovererExecuteTakesPlan R1：Execute 的入参类型必须是 Plan。
// 入参是 string 的那一刻，"执行任意字符串"就从类型上成为可能；
// 是 Plan 时，调用方必须先构造一个过完安全检查的计划。
func TestRecovererExecuteTakesPlan(t *testing.T) {
	// 接口方法经 Elem().MethodByName 取到，**方法类型不含接收者**，
	// 所以入参就是真实参数个数（这里应为 1）。
	m, ok := reflect.TypeOf((*Recoverer)(nil)).Elem().MethodByName("Execute")
	if !ok {
		t.Fatal("Recoverer 接口没有 Execute 方法")
	}
	if m.Type.NumIn() != 1 {
		t.Fatalf("Execute 应有 1 个入参，实际 %d", m.Type.NumIn())
	}
	got := m.Type.In(0)
	if got != reflect.TypeOf(Plan{}) {
		t.Errorf("Execute 入参类型 = %s，应为 main.Plan（R1：不允许 string）", got)
	}
	// 返回值必须是三态载体
	if m.Type.NumOut() != 1 || m.Type.Out(0) != reflect.TypeOf(OpResult{}) {
		t.Errorf("Execute 返回值应为 OpResult，实际 %v", m.Type.Out(0))
	}
}

// TestProbeObserveReturnsObservation Probe 的出口只有 Observation（纯事实）。
// 刻意不断言"没有执行入口"——那件事由 TestTopologyFileHasNoExecImport
// 与"接口方法表里只有 Observe"共同保证。
func TestProbeObserveReturnsObservation(t *testing.T) {
	pt := reflect.TypeOf((*Probe)(nil)).Elem()
	if pt.NumMethod() != 1 || pt.Method(0).Name != "Observe" {
		t.Fatalf("Probe 接口应只有 Observe 一个方法，实际 %d 个", pt.NumMethod())
	}
	if pt.Method(0).Type.Out(0) != reflect.TypeOf(Observation{}) {
		t.Errorf("Observe 返回值应为 Observation，实际 %s", pt.Method(0).Type.Out(0))
	}
}

// TestVerifierThreeStatesDistinguishable R3：三态必须可区分。
// 用"三者两两不等"来表达——任何把 Ok&&!Verified 与 Ok&&Verified
// 合并的实现，都会让这两个值相等，这条断言就会红。
// 注意 OpResult 含 []string 不可直接 ==，按三态契约的字段比。
func TestVerifierThreeStatesDistinguishable(t *testing.T) {
	fail := OpResult{Ok: false, Action: "start_service", Target: "backend", ErrKind: "spawn_fail"}
	unverified := OpResult{Ok: true, Verified: false, Action: "start_service", Target: "backend"}
	verified := OpResult{Ok: true, Verified: true, Action: "start_service", Target: "backend"}

	sig := func(r OpResult) string {
		return fmt.Sprintf("ok=%v verified=%v errkind=%q", r.Ok, r.Verified, r.ErrKind)
	}
	if sig(fail) == sig(unverified) || sig(fail) == sig(verified) {
		t.Fatal("三态中有两个相等——合并了不同的结论（R3）")
	}
	if sig(unverified) == sig(verified) {
		t.Error("已执行未复验 与 已执行且复验 相等——Ok&&!Verified 被合并进 Ok&&Verified（R3）")
	}
	// 反过来说：fail 与 unverified 也必须能分开（"动作失败"≠"动作做了但没验"）
	if sig(fail) == sig(unverified) {
		t.Error("动作失败 与 已执行未复验 相等")
	}
}

// fakeAdapter 一个最小适配器，用来验证三个接口能被同时实现
// （以及 Verifier 确实返回三态 OpResult）。
type fakeAdapter struct{}

func (fakeAdapter) Observe(svc Service) Observation {
	return Observation{Service: svc.ID, Healthy: true, Evidence: []string{"stub"}}
}

func (fakeAdapter) Plan(svc Service, a Action) (Plan, error) {
	return Plan{Service: svc.ID, Action: a.Kind, Command: "echo stub", RiskTier: svc.RiskTier}, nil
}

func (fakeAdapter) Execute(p Plan) OpResult {
	return opOK("stub", p.Service, "stub 已执行（未复验）")
}

func (fakeAdapter) Verify(svc Service, spec VerifySpec) OpResult {
	return OpResult{Ok: true, Verified: true, Action: "verify", Target: verifyTargetOf(svc)}
}

func TestFakeAdapterSatisfiesAllThreeInterfaces(t *testing.T) {
	var a any = fakeAdapter{}
	if _, ok := a.(Probe); !ok {
		t.Error("fakeAdapter 未实现 Probe")
	}
	if _, ok := a.(Recoverer); !ok {
		t.Error("fakeAdapter 未实现 Recoverer")
	}
	if _, ok := a.(Verifier); !ok {
		t.Error("fakeAdapter 未实现 Verifier")
	}
}

// ---------- T2.1：services 清单解析与校验 ----------

func svcFull(id string) Service {
	return Service{
		ID:       id,
		Label:    id + " 服务",
		Adapter:  AdapterLocal,
		RiskTier: RiskLow,
		Probe:    ProbeSpec{Kind: ProbePort, Port: 8080},
		Verify:   VerifySpec{Evidence: []string{"port"}},
	}
}

func TestValidateServicesAcceptsMinimalValidList(t *testing.T) {
	if bad := validateServices([]Service{svcFull("web")}); len(bad) != 0 {
		t.Errorf("最小合法清单不该报错：%v", bad)
	}
}

func TestValidateServicesRejectsMissingProbeKind(t *testing.T) {
	s := svcFull("web")
	s.Probe.Kind = ""
	bad := validateServices([]Service{s})
	if len(bad) == 0 {
		t.Fatal("probe.kind 为空必须报错——不能静默默认成某种探针")
	}
	if !strings.Contains(bad[0], "probe.kind") {
		t.Errorf("报错应指向 probe.kind 字段，实际：%s", bad[0])
	}
}

func TestValidateServicesRejectsUnknownProbeKind(t *testing.T) {
	s := svcFull("web")
	s.Probe.Kind = "magic"
	if bad := validateServices([]Service{s}); len(bad) == 0 {
		t.Error("未知 probe.kind 必须报错")
	}
}

func TestValidateServicesRejectsPortOutOfRange(t *testing.T) {
	s := svcFull("web")
	s.Probe.Port = 70000
	bad := validateServices([]Service{s})
	if len(bad) == 0 {
		t.Fatal("端口越界必须报错")
	}
	if !strings.Contains(bad[0], "1-65535") {
		t.Errorf("报错应说明端口范围，实际：%s", bad[0])
	}
}

func TestValidateServicesRejectsHTTPProbeWithoutScheme(t *testing.T) {
	s := svcFull("web")
	s.Probe = ProbeSpec{Kind: ProbeHTTP, URL: "127.0.0.1:8080"}
	if bad := validateServices([]Service{s}); len(bad) == 0 {
		t.Error("http 探针缺 scheme 必须报错")
	}
}

func TestValidateServicesRejectsEmptyComposite(t *testing.T) {
	s := svcFull("web")
	s.Probe = ProbeSpec{Kind: ProbeComposite}
	bad := validateServices([]Service{s})
	if len(bad) == 0 {
		t.Fatal("composite 探针没有子项必须报错")
	}
	if !strings.Contains(bad[0], "parts") {
		t.Errorf("报错应指向 parts，实际：%s", bad[0])
	}
}

// TestValidateServicesRejectsDuplicateID 重复 id 必须报错。
// 不报的后果：serviceByID 永远返回第一个，第二个服务的探针/恢复/复验
// 全部静默丢失——而界面上看起来"配了两个服务"。
func TestValidateServicesRejectsDuplicateID(t *testing.T) {
	bad := validateServices([]Service{svcFull("web"), svcFull("web")})
	if len(bad) == 0 {
		t.Fatal("重复 id 必须报错")
	}
	if !strings.Contains(bad[0], "重复") {
		t.Errorf("报错应说明 id 重复，实际：%s", bad[0])
	}
}

func TestValidateServicesRejectsDanglingDependsOn(t *testing.T) {
	s := svcFull("web")
	s.DependsOn = []string{"mysql"}
	bad := validateServices([]Service{s})
	if len(bad) == 0 {
		t.Fatal("depends_on 指向不存在的 service 必须报错")
	}
	if !strings.Contains(bad[0], "mysql") {
		t.Errorf("报错应点名缺失的依赖，实际：%s", bad[0])
	}
}

func TestValidateServicesRejectsUnknownStrategyRef(t *testing.T) {
	s := svcFull("web")
	s.Recovery = "no_such_strategy"
	bad := validateServices([]Service{s})
	if len(bad) == 0 {
		t.Fatal("引用未注册策略必须报错——不静默当成无策略")
	}
	if !strings.Contains(bad[0], "no_such_strategy") {
		t.Errorf("报错应点名未注册的策略名，实际：%s", bad[0])
	}
}

func TestValidateServicesRejectsBadRiskTier(t *testing.T) {
	s := svcFull("web")
	s.RiskTier = "medium"
	if bad := validateServices([]Service{s}); len(bad) == 0 {
		t.Error("非法 risk_tier 必须报错")
	}
}

// TestServiceByIDDoesNotFallBackToFirst 找不到就返回 false，
// 不静默返回第一个——否则拼错 id 会去操作另一个服务。
func TestServiceByIDDoesNotFallBackToFirst(t *testing.T) {
	svcs := []Service{svcFull("web"), svcFull("backend")}
	if _, ok := serviceByID(svcs, "bakend"); ok {
		t.Error("拼错 id 不该命中")
	}
	if s, ok := serviceByID(svcs, "backend"); !ok || s.ID != "backend" {
		t.Error("正确 id 应命中自身")
	}
}

// TestVerifyTargetFallsBackToServiceID v3.0.1 那个补口的模型侧根除：
// target 为空时补成 service id，让"有事件"永远不会被写成"没事件"。
func TestVerifyTargetFallsBackToServiceID(t *testing.T) {
	s := svcFull("backend")
	if got := verifyTargetOf(s); got != "backend" {
		t.Errorf("空 target 应补成 service id，得到 %q", got)
	}
	s.Verify.Target = "db"
	if got := verifyTargetOf(s); got != "db" {
		t.Errorf("显式 target 应被尊重，得到 %q", got)
	}
	// 空白串也不算"填了"
	s.Verify.Target = "   "
	if got := verifyTargetOf(s); got != "backend" {
		t.Errorf("纯空白 target 应视为未填，得到 %q", got)
	}
}

// ---------- T2.1：旧字段等价推导 ----------

func legacyCfg() Config {
	return Config{
		Projects: Projects{
			FrontendDir: `D:\proj\web`,
			BackendDir:  `D:\proj\server`,
			DBName:      "farm",
		},
		Chain: ChainConfig{
			SSHHost:        "10.0.0.9",
			ChainPort:      20200,
			WebasePort:     5002,
			ChainGuard:     true,
			ChainGuardSecs: 300,
			ChainNotify:    true,
		},
	}
}

// TestLegacyDerivationDoesNotExpandAutoRecovery **本次最关键的一条**。
// 旧守护循环只管链端。推导出的清单如果给 web/backend/mysql 也挂上
// Recovery，升级即扩大无人值守自动恢复的作用面——而那正是 plan §7.1
// 豁免风险里点名、且没有任何实跑证据支撑的那件事。
func TestLegacyDerivationDoesNotExpandAutoRecovery(t *testing.T) {
	svcs := servicesFromLegacy(legacyCfg())
	withRecovery := map[string]bool{}
	for _, s := range svcs {
		if s.Recovery != "" {
			withRecovery[s.ID] = true
		}
	}
	if len(withRecovery) == 0 {
		t.Fatal("链端 service 应挂恢复策略（旧守护循环确实恢复它）")
	}
	for id := range withRecovery {
		if id != "chain" && id != "webase" {
			t.Errorf("service %s 被挂了恢复策略，但旧守护循环从不恢复它——"+
				"这会静默扩大无人值守自动恢复的作用面", id)
		}
	}
}

func TestLegacyDerivationListsExpectedServices(t *testing.T) {
	svcs := servicesFromLegacy(legacyCfg())
	want := []string{"web", "backend", "mysql", "chain", "webase"}
	if len(svcs) != len(want) {
		t.Fatalf("应推导出 %d 个 service，实际 %d：%v", len(want), len(svcs), svcs)
	}
	for i, id := range want {
		if svcs[i].ID != id {
			t.Errorf("第 %d 个应为 %s，实际 %s", i, id, svcs[i].ID)
		}
	}
	// webase 依赖 chain：跨节点事件关联的依据（plan §4.3）
	if got := serviceByIDMust(t, svcs, "webase").DependsOn; len(got) != 1 || got[0] != "chain" {
		t.Errorf("webase 应 depends_on chain，实际 %v", got)
	}
	// mysql 是 high 档：数据类必须人工
	if got := serviceByIDMust(t, svcs, "mysql").RiskTier; got != RiskHigh {
		t.Errorf("mysql 应为 %s 档，实际 %s", RiskHigh, got)
	}
	// 复验 target 永不为空
	for _, s := range svcs {
		if verifyTargetOf(s) == "" {
			t.Errorf("service %s 的复验 target 为空", s.ID)
		}
	}
}

// TestLegacyDerivationWithoutSSHHasNoChain 没有 SSH 主机就没有链端 service
// ——与 startGuardLoop 的开头判断一致。
func TestLegacyDerivationWithoutSSHHasNoChain(t *testing.T) {
	c := legacyCfg()
	c.Chain.SSHHost = ""
	svcs := servicesFromLegacy(c)
	for _, s := range svcs {
		if s.ID == "chain" || s.ID == "webase" {
			t.Fatalf("未配 SSH 主机却推导出了 %s", s.ID)
		}
	}
}

// TestLegacyDerivationPassesItsOwnValidation 推导结果必须自洽：
// 旧配置本身写坏时（端口越界等）要能报出来，不能因为是老配置就免检。
func TestLegacyDerivationPassesItsOwnValidation(t *testing.T) {
	_, bad := effectiveServices(legacyCfg())
	if len(bad) != 0 {
		t.Errorf("合法旧配置的推导结果不应有校验问题，实际 %v", bad)
	}
	c := legacyCfg()
	c.Chain.WebasePort = 99999 // 越界
	_, bad2 := effectiveServices(c)
	if len(bad2) == 0 {
		t.Error("旧配置端口越界应被校验抓到（老配置不免检）")
	}
	if !strings.Contains(strings.Join(bad2, " "), "99999") {
		t.Errorf("报错应点名越界的端口值，实际 %v", bad2)
	}
}

func serviceByIDMust(t *testing.T, svcs []Service, id string) Service {
	t.Helper()
	s, ok := serviceByID(svcs, id)
	if !ok {
		t.Fatalf("清单里找不到 %s", id)
	}
	return s
}
