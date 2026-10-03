package main

// scope_test.go —— v2.3 N2 的单测
//
// 这一批测的是"经验会不会害用户"。可能的害处有四种：
//  1. 换项目还用 → 建议完全错
//  2. 环境变了还用 → 结论过时
//  3. 少样本当真理 → 把两次巧合当规律
//  4. 置信度被当成权限 → 绕过确认（这是最严重的，必须钉死）

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withScopeEnv 隔离统计文件 + 记忆文件，并把项目目录指向临时位置。
func withScopeEnv(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	oldOut, oldMem := outcomeFile, memoryFile
	outcomeFile = func() string { return filepath.Join(dir, "outcomes.json") }
	memoryFile = func() string { return filepath.Join(dir, "memories.json") }
	oldFE, oldBE := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	cfg.Projects.FrontendDir = filepath.Join(dir, "web")
	cfg.Projects.BackendDir = filepath.Join(dir, "server")
	resultsMu.Lock()
	oldResults := results
	results = nil
	resultsMu.Unlock()
	return func() {
		outcomeFile, memoryFile = oldOut, oldMem
		cfg.Projects.FrontendDir, cfg.Projects.BackendDir = oldFE, oldBE
		resultsMu.Lock()
		results = oldResults
		resultsMu.Unlock()
		_ = os.Remove(outcomeFile())
	}
}

// ===== 置信度 =====

func TestConfidenceLaplace(t *testing.T) {
	cases := []struct {
		succ, fail int
		want       float64
	}{
		{0, 0, 0},           // 无样本
		{1, 0, 2.0 / 3.0},   // 1 次成功不该是 100%
		{0, 1, 1.0 / 3.0},   // 1 次失败同理
		{9, 1, 10.0 / 12.0}, // 10 次里 9 成
		{1, 9, 2.0 / 12.0},  // 10 次里 1 成
	}
	for _, c := range cases {
		got := Confidence(c.succ, c.fail)
		if got < c.want-0.001 || got > c.want+0.001 {
			t.Errorf("Confidence(%d,%d)=%.3f，期望 %.3f", c.succ, c.fail, got, c.want)
		}
	}
	// 单调性：成功越多越高，失败越多越低
	if Confidence(5, 1) <= Confidence(1, 5) {
		t.Error("成功次数多的经验置信度必须更高")
	}
}

func TestConfidenceGate(t *testing.T) {
	// 高置信度主动提示
	if ok, _ := confidenceGate(0.9); !ok {
		t.Error("高置信度应主动提示")
	}
	// 中等：提示但标注
	ok, note := confidenceGate(0.6)
	if !ok || note == "" {
		t.Error("中等置信度应提示但带不确定性说明")
	}
	// 低：不主动提示
	if ok, _ := confidenceGate(0.2); ok {
		t.Error("低置信度经验不应主动提示（噪声大于价值）")
	}
	// 无样本
	if ok, _ := confidenceGate(0); ok {
		t.Error("无样本不应提示")
	}
}

// ===== 作用域与失效 =====

func TestScopeProjectChangeInvalidates(t *testing.T) {
	defer withScopeEnv(t)()

	sc := currentScope("start_service")
	if ok, _ := scopeApplies(sc, "start_service"); !ok {
		t.Fatal("刚建的作用域应适用于当前项目")
	}

	// 换项目：指纹必须变
	cfg.Projects.FrontendDir = filepath.Join(t.TempDir(), "other-web")
	other := currentScope("start_service")
	if other.Project == sc.Project {
		t.Error("换项目后指纹必须不同，否则作用域形同虚设")
	}
	// 用旧作用域去匹配新项目 → 必须失效
	if ok, why := scopeApplies(sc, "start_service"); ok {
		t.Error("换项目后旧经验必须失效")
	} else if why == "" {
		t.Error("失效必须给理由（界面要能解释为什么这条不适用了）")
	}
}

func TestScopeGlobalIgnoresProject(t *testing.T) {
	defer withScopeEnv(t)()

	g := Scope{Kind: scopeGlobal}
	cfg.Projects.FrontendDir = filepath.Join(t.TempDir(), "far-away")
	if ok, _ := scopeApplies(g, ""); !ok {
		t.Error("global 作用域应无视项目变化")
	}
	// 但动作限定仍然生效
	ga := Scope{Kind: scopeGlobal, Action: "db_backup"}
	if ok, _ := scopeApplies(ga, "start_service"); ok {
		t.Error("global 也应尊重动作限定：这条只关于备份")
	}
}

func TestScopeActionLimit(t *testing.T) {
	defer withScopeEnv(t)()

	sc := currentScope("start_service")
	if ok, _ := scopeApplies(sc, "db_backup"); ok {
		t.Error("限定了动作的经验不应匹配别的动作")
	}
	if ok, _ := scopeApplies(sc, "start_service"); !ok {
		t.Error("同一动作应匹配")
	}
}

func TestEnvFingerprintChangesWithPorts(t *testing.T) {
	defer withScopeEnv(t)()

	fp1 := envFingerprint()
	cfg.Projects.MySQLPort = 3307
	if envFingerprint() == fp1 {
		t.Error("端口配置变了，环境指纹必须变（否则端口类经验永不失效）")
	}
}

// 环境指纹**不能**包含 IP：否则虚拟机每次重启全部经验失效，Stale 变噪声源
func TestEnvFingerprintIgnoresIP(t *testing.T) {
	defer withScopeEnv(t)()

	oldHost := cfg.Chain.SSHHost
	fp1 := envFingerprint()
	cfg.Chain.SSHHost = "203.0.113.99"
	fp2 := envFingerprint()
	cfg.Chain.SSHHost = oldHost
	if fp1 != fp2 {
		t.Error("链端 IP 变化不应让环境指纹变化（虚拟机 NAT 的 IP 每次都可能不同，" +
			"把它算进去会让全部经验失效，Stale 就成了噪声源）")
	}
}

func TestEnvFingerprintStableAcrossCalls(t *testing.T) {
	defer withScopeEnv(t)()
	if envFingerprint() != envFingerprint() {
		t.Error("环境指纹必须稳定（map 遍历顺序会让它乱变）")
	}
}

// ===== 成败计数 =====

func TestOutcomeRecordAndRead(t *testing.T) {
	defer withScopeEnv(t)()

	for i := 0; i < 3; i++ {
		recordOutcome("ls-1", "start_service", resFail)
	}
	for i := 0; i < 6; i++ {
		recordOutcome("ls-1", "start_service", resOK)
	}
	st := outcomeOf("ls-1")
	if st.Fail != 3 || st.Succ != 6 {
		t.Errorf("成败计数应分别是 3/6，实际 %+v", st)
	}
	// 成功后置信度必须显著高于 0（"这个做法后来通了"要反映出来）
	if c := Confidence(st.Succ, st.Fail); c < 0.6 {
		t.Errorf("6 成 3 败的置信度应 >= 0.6，实际 %.2f", c)
	}
}

func TestOutcomeIgnoresNonOutcomeResults(t *testing.T) {
	defer withScopeEnv(t)()

	// denied / started 不是"做了但没成"，不该计入成败
	recordOutcome("ls-2", "db_backup", resDenied)
	recordOutcome("ls-2", "db_backup", resStart)
	st := outcomeOf("ls-2")
	if st.Fail != 0 || st.Succ != 0 {
		t.Errorf("denied/started 不应计入成败，实际 %+v", st)
	}
}

func TestOutcomeScopedPerProject(t *testing.T) {
	defer withScopeEnv(t)()

	recordOutcome("ls-3", "start_service", resFail)
	recordOutcome("ls-3", "start_service", resFail)
	// 换项目后同一条经验的统计必须独立
	cfg.Projects.FrontendDir = filepath.Join(t.TempDir(), "other")
	if st := outcomeOf("ls-3"); st.Fail != 0 {
		t.Errorf("换项目后统计应归零（否则两个项目的成败互相抵消），实际 %+v", st)
	}
}

func TestOutcomeFileBounded(t *testing.T) {
	defer withScopeEnv(t)()

	// 键里带项目指纹，改目录会产生新键——不限制条数会无限增长
	for i := 0; i < 620; i++ {
		recordOutcome("ls-"+itoa(i), "act", resFail)
	}
	b, err := os.ReadFile(outcomeFile())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(strings.TrimRight(string(b), "\n"), "\n") + 1
	if lines > 500 {
		t.Errorf("统计文件应封顶 500 条，实际 %d", lines)
	}
}

// ===== 硬边界：置信度不是权限 =====

// 这是本批最重要的断言。置信度高只影响"怎么用"，绝不影响"能不能做"。
func TestConfidenceNeverElevatesPolicy(t *testing.T) {
	defer withScopeEnv(t)()

	// 造出高置信度状态
	for i := 0; i < 30; i++ {
		recordOutcome("ls-p", "db_restore", resOK)
	}
	if c := Confidence(30, 0); c < 0.8 {
		t.Fatalf("前置不成立：置信度只有 %.2f", c)
	}
	// 即便如此，db_restore 仍必须是 elevated、危险脚本仍必须 forbidden
	if v := PolicyGate("db_restore", "farm", actAI); v.Level != PolicyElevated {
		t.Errorf("高置信度不得降低档位：%s", v.Level)
	}
	if v := PolicyGate("start_service", "script=migrate", actAI); v.Level != PolicyForbidden {
		t.Errorf("高置信度不得解除禁止：%s", v.Level)
	}
	if v := PolicyGate("db_backup", "farm", actAI); v.Level != PolicyConfirm {
		t.Errorf("高置信度不得绕过确认：%s", v.Level)
	}
}

// ===== 提示词筛选 =====

func TestLessonBriefSkipsStaleAndLowConfidence(t *testing.T) {
	ls := []Lesson{
		{Title: "高置信度可用", Confidence: 0.9, ConfLabel: "高", Succ: 9, Fail: 1, Scope: Scope{Frontend: "web"}},
		{Title: "低置信度仅供参考", Confidence: 0.2, ConfLabel: "低", Fail: 3, Scope: Scope{Frontend: "web"}},
		{Title: "已失效不该出现", Confidence: 0.9, ConfLabel: "高", Stale: true, StaleWhy: "项目已变", Scope: Scope{Frontend: "web"}},
	}
	brief := lessonBrief(ls)
	if !strings.Contains(brief, "高置信度可用") {
		t.Errorf("高置信度经验应进提示词：%s", brief)
	}
	if strings.Contains(brief, "已失效不该出现") {
		t.Errorf("Stale 经验绝不能进提示词（环境都变了还提等于给错建议）：%s", brief)
	}
	if !strings.Contains(brief, "仅供参考") {
		t.Errorf("低置信度必须标注为仅供参考：%s", brief)
	}
	// 高置信度应排前面（提示词前部权重更高）
	if strings.Index(brief, "高置信度可用") > strings.Index(brief, "低置信度") {
		t.Errorf("高置信度应排在前面：%s", brief)
	}
}

func TestLessonBriefEmptyWhenAllStale(t *testing.T) {
	ls := []Lesson{{Title: "x", Stale: true}, {Title: "y", Stale: true}}
	if brief := lessonBrief(ls); brief != "" {
		t.Errorf("全部失效时应返回空串：%q", brief)
	}
}

func TestSplitTrigger(t *testing.T) {
	// 注意：第二个返回值不能叫 t（会遮蔽 *testing.T）
	act, tgt := splitTrigger("start_service @ web")
	if act != "start_service" || tgt != "web" {
		t.Errorf("拆解结果 = (%q,%q)，期望 (start_service,web)", act, tgt)
	}
	act, tgt = splitTrigger("verify_chain @ 203.0.113.1:22")
	if act != "verify_chain" || tgt != "203.0.113.1:22" {
		t.Errorf("目标含冒号时不应被误拆：(%q,%q)", act, tgt)
	}
	act, tgt = splitTrigger("bare_action")
	if act != "bare_action" || tgt != "" {
		t.Errorf("无 @ 时目标应为空：(%q,%q)", act, tgt)
	}
}

// ===== 记忆作用域 =====

func TestMemoryStaleOnProjectChange(t *testing.T) {
	defer withScopeEnv(t)()

	m, _, err := memUpsert("备份前先停掉后端服务", "备份", true, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if m.ScopeFP == "" {
		t.Fatal("创建时应写入项目指纹")
	}
	// 同项目：应注入
	if got := memoriesFor("备份", 10); len(got) != 1 {
		t.Errorf("同项目应注入该记忆，实际 %d 条", len(got))
	}

	// 换项目：应失效
	cfg.Projects.FrontendDir = filepath.Join(t.TempDir(), "other")
	if got := memoriesFor("备份", 10); len(got) != 0 {
		t.Errorf("换项目后该记忆必须失效（'上次那台机器'的经验套到当前项目就是错建议），实际注入 %d 条", len(got))
	}
}

func TestMemoryGlobalSurvivesProjectChange(t *testing.T) {
	defer withScopeEnv(t)()

	// 先建一条本项目记忆，再走真实接口标 global（模拟用户勾选）
	if _, _, err := memUpsert("备份前先停掉后端服务", "备份", true, "manual"); err != nil {
		t.Fatal(err)
	}
	memMu.Lock()
	id := ""
	ms := loadMemories()
	if len(ms) != 1 {
		memMu.Unlock()
		t.Fatalf("前置不成立：记忆条数 = %d，期望 1", len(ms))
	}
	id = ms[0].ID
	memMu.Unlock()
	if err := memSetGlobal(id, true); err != nil {
		t.Fatal(err)
	}

	cfg.Projects.FrontendDir = filepath.Join(t.TempDir(), "other")
	if got := memoriesFor("", 10); len(got) != 1 {
		t.Errorf("global 记忆应跨项目生效，实际注入 %d 条", len(got))
	}
}

// 老条目（无指纹）不能被静默作废
func TestMemoryWithoutFingerprintStaysActive(t *testing.T) {
	defer withScopeEnv(t)()

	memMu.Lock()
	line := `{"id":"legacy1","text":"老条目没有指纹字段","always":true,"source":"manual","created":"2026-01-01 00:00:00"}`
	if err := os.WriteFile(memoryFile(), []byte(line+"\n"), 0600); err != nil {
		memMu.Unlock()
		t.Fatal(err)
	}
	memMu.Unlock()

	if got := memoriesFor("", 10); len(got) != 1 {
		t.Errorf("无指纹的老条目应继续生效（静默作废比跨项目更糟），实际 %d 条", len(got))
	}
}
