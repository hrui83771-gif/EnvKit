package main

// playbook_test.go —— Phase 2：预案库（匹配/衰减/沉淀/套用）。
//
// 测试重点不是"能跑通"，而是三条最容易悄悄坏掉的规则：
//  1. 匹配门槛（服务/症状/来源不符就**不能**推荐，猜错的建议比没有建议更糟）；
//  2. 并列时的确定性（同分看成功率、再同看 ID，结果与文件顺序无关）；
//  3. 连败达限进 review 且成功会清零 streak（否则放出来一次就又被弹回去）。

import (
	"strings"
	"testing"
	"time"
)

// putPlaybook 往隔离后的预案文件里塞一条（测试夹具）。
func putPlaybook(t *testing.T, p Playbook) {
	t.Helper()
	pbkMu.Lock()
	defer pbkMu.Unlock()
	all := loadPlaybooks()
	all = append(all, p)
	if err := savePlaybooks(all); err != nil {
		t.Fatalf("写预案夹具：%v", err)
	}
}

func mkPbk(id string, m PlaybookMatcher) Playbook {
	now := time.Now()
	return Playbook{
		ID: id, Title: "预案 " + id, Matcher: m,
		Steps:     []PlaybookStep{{Tool: "verify_environment"}},
		State:     pbkActive,
		CreatedAt: now, UpdatedAt: now,
	}
}

// TestPbkScoreGates 匹配门槛：结构化键是 AND，关键词是加分项。
func TestPbkScoreGates(t *testing.T) {
	cases := []struct {
		name  string
		m     PlaybookMatcher
		q     PlaybookQuery
		ok    bool
		score int
	}{
		{"服务命中", PlaybookMatcher{Services: []string{"backend"}},
			PlaybookQuery{SvcID: "backend"}, true, 4},
		{"服务不符则排除", PlaybookMatcher{Services: []string{"backend"}},
			PlaybookQuery{SvcID: "web"}, false, 0},
		{"服务未知不猜", PlaybookMatcher{Services: []string{"backend"}},
			PlaybookQuery{SvcID: ""}, false, 0},
		{"症状命中", PlaybookMatcher{ErrKinds: []string{errKindVerifyFail}},
			PlaybookQuery{ErrKind: errKindVerifyFail}, true, 2},
		{"症状不符则排除", PlaybookMatcher{ErrKinds: []string{errKindVerifyFail}},
			PlaybookQuery{ErrKind: "exit"}, false, 0},
		{"来源命中", PlaybookMatcher{Triggers: []string{"guard"}},
			PlaybookQuery{Trigger: "guard"}, true, 1},
		{"关键词命中", PlaybookMatcher{Keywords: []string{"端口"}},
			PlaybookQuery{Text: "启动失败：端口没监听"}, true, 1},
		{"关键词未命中则排除", PlaybookMatcher{Keywords: []string{"端口"}},
			PlaybookQuery{Text: "磁盘满了"}, false, 0},
		{"关键词加分上限 3", PlaybookMatcher{Keywords: []string{"端口", "超时", "崩溃", "内存"}},
			PlaybookQuery{Text: "端口 超时 崩溃 内存"}, true, 3},
		{"关键词大小写不敏感", PlaybookMatcher{Keywords: []string{"Port"}},
			PlaybookQuery{Text: "port 8080 已占用"}, true, 1},
		{"组合全中", PlaybookMatcher{
			Services: []string{"backend"}, ErrKinds: []string{errKindVerifyFail},
			Triggers: []string{"guard"}, Keywords: []string{"端口"},
		}, PlaybookQuery{SvcID: "backend", ErrKind: errKindVerifyFail, Trigger: "guard",
			Text: "端口没监听"}, true, 8},
		// 全空 matcher 会匹配任何告警 —— 必须永不命中，否则用户看到一个
		// 莫名其妙又永远查不出出处的建议
		{"空匹配器永不命中", PlaybookMatcher{}, PlaybookQuery{SvcID: "backend", Text: "x"}, false, 0},
	}
	for _, c := range cases {
		got, ok := pbkScore(mkPbk("p-x", c.m), c.q)
		if ok != c.ok || got != c.score {
			t.Errorf("%s：期望 ok=%v score=%d，实为 ok=%v score=%d",
				c.name, c.ok, c.score, ok, got)
		}
	}
}

// TestPbkPickTieBreak 并列时的排序规则：分数 → 成功率 → ID。
func TestPbkPickTieBreak(t *testing.T) {
	m := PlaybookMatcher{Services: []string{"backend"}}
	q := PlaybookQuery{SvcID: "backend"}

	low := mkPbk("p-a", m)
	low.Stats = PbkStats{Used: 4, Succ: 2}
	best := mkPbk("p-b", m)
	best.Stats = PbkStats{Used: 4, Succ: 4}
	zero := mkPbk("p-c", m) // 没跑过 = 0%，不该压过已验证的

	p, score, ok := pbkPick([]Playbook{low, zero, best}, q)
	if !ok || score != 4 {
		t.Fatalf("应匹配：ok=%v score=%d", ok, score)
	}
	if p.ID != "p-b" {
		t.Fatalf("应选成功率最高的 p-b，实为 %s", p.ID)
	}

	// 成功率相同 → ID 字典序小的优先（与文件顺序无关）
	x := mkPbk("p-z", m)
	y := mkPbk("p-y", m)
	if p, _, _ := pbkPick([]Playbook{x, y}, q); p.ID != "p-y" {
		t.Fatalf("同分同率应选 ID 较小的 p-y，实为 %s", p.ID)
	}
	if p, _, _ := pbkPick([]Playbook{y, x}, q); p.ID != "p-y" {
		t.Fatalf("结果不该依赖文件顺序，实为 %s", p.ID)
	}
}

// TestPbkPickSkipsInactive review / retired 不再推荐（衰减的落点）。
func TestPbkPickSkipsInactive(t *testing.T) {
	m := PlaybookMatcher{Services: []string{"backend"}}
	q := PlaybookQuery{SvcID: "backend"}
	rev := mkPbk("p-rev", m)
	rev.State = pbkReview
	ret := mkPbk("p-ret", m)
	ret.State = pbkRetired
	if _, _, ok := pbkPick([]Playbook{rev, ret}, q); ok {
		t.Fatalf("review/retired 不该被推荐")
	}
}

// TestPbkSettleDecay 连败达限进 review；成功后 streak 清零。
func TestPbkSettleDecay(t *testing.T) {
	defer withMissionEnv(t)()
	putPlaybook(t, mkPbk("p-1", PlaybookMatcher{Services: []string{"backend"}}))

	pbkSettle("p-1", false)
	pbkSettle("p-1", false)
	p, _ := pbkGet("p-1")
	if p.State != pbkActive || p.Stats.Streak != 2 {
		t.Fatalf("两次失败还不该进 review：state=%s streak=%d", p.State, p.Stats.Streak)
	}

	pbkSettle("p-1", false)
	p, _ = pbkGet("p-1")
	if p.State != pbkReview {
		t.Fatalf("连续 3 次未通过应进 review，实为 %s", p.State)
	}
	if p.Stats.Used != 3 || p.Stats.Fail != 3 || p.Stats.Succ != 0 {
		t.Fatalf("统计不对：%+v", p.Stats)
	}

	// 成功一次 → streak 清零（但不会自动解除 review：那要人表态）
	pbkSettle("p-1", true)
	p, _ = pbkGet("p-1")
	if p.Stats.Streak != 0 || p.Stats.Succ != 1 {
		t.Fatalf("成功后 streak 应清零：%+v", p.Stats)
	}
	if p.State != pbkReview {
		t.Fatalf("成功不该自动解除 review，实为 %s", p.State)
	}
}

// TestPbkFromMission 从行动沉淀：步骤来自 Plan，匹配键来自 Trigger。
func TestPbkFromMission(t *testing.T) {
	defer withMissionEnv(t)()

	m, err := missionNew("后端恢复失败", Trigger{Kind: "guard", Detail: "backend | 已拉起但复验未通过：端口没监听"})
	if err != nil {
		t.Fatalf("missionNew: %v", err)
	}
	if _, err := missionUpdate(m.ID, func(x *Mission) error {
		x.Plan = []PlanStepRecord{
			{Seq: 1, Tool: "get_logs", Why: "看崩溃原因", OK: true},
			{Seq: 2, Tool: "restart_service", Target: "backend", Why: "重启后端", OK: true},
			{Seq: 3, Tool: "verify_environment", Target: "backend", Why: "复验", OK: true},
		}
		return nil
	}); err != nil {
		t.Fatalf("注入步骤: %v", err)
	}

	p, err := pbkFromMission(m.ID, "")
	if err != nil {
		t.Fatalf("沉淀失败：%v", err)
	}
	if len(p.Steps) != 3 || p.Steps[1].Tool != "restart_service" || p.Steps[1].Target != "backend" {
		t.Fatalf("步骤没抄全：%+v", p.Steps)
	}
	if !pbkHas(p.Matcher.Services, "backend") || !pbkHas(p.Matcher.Triggers, "guard") {
		t.Fatalf("匹配键应来自 Trigger：%+v", p.Matcher)
	}
	if p.SourceMission != m.ID || p.State != pbkActive {
		t.Fatalf("来源/状态不对：%+v", p)
	}
	if p.Title != "后端恢复失败" { // 未给标题 → 用行动目标
		t.Fatalf("标题应回落到目标，实为 %q", p.Title)
	}

	// 落盘了才算数（重启后预案要还在）
	found := false
	for _, x := range pbkList() {
		if x.ID == p.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("沉淀后应能从 pbkList 读到")
	}
}

// TestPbkFromMissionRejects 沉淀的两条拒绝路径：没步骤 / 工具名不存在。
func TestPbkFromMissionRejects(t *testing.T) {
	defer withMissionEnv(t)()

	m, _ := missionNew("空行动", Trigger{Kind: "manual"})
	if _, err := pbkFromMission(m.ID, ""); err == nil {
		t.Fatalf("没有已完成步骤时不该能沉淀")
	}

	if _, err := missionUpdate(m.ID, func(x *Mission) error {
		x.Plan = []PlanStepRecord{{Seq: 1, Tool: "no_such_tool", OK: true}}
		return nil
	}); err != nil {
		t.Fatalf("注入步骤: %v", err)
	}
	_, err := pbkFromMission(m.ID, "")
	if err == nil || !strings.Contains(err.Error(), "no_such_tool") {
		t.Fatalf("工具名不存在必须当场拒绝（工具改名是迟早的事），实为 %v", err)
	}
}

// TestPbkCheckSteps 步骤校验（保存时的防线）。
func TestPbkCheckSteps(t *testing.T) {
	if err := pbkCheckSteps(nil); err == nil {
		t.Fatalf("空步骤应报错")
	}
	if err := pbkCheckSteps([]PlaybookStep{{Tool: "restart_service"}}); err != nil {
		t.Fatalf("已知工具应通过：%v", err)
	}
	if err := pbkCheckSteps([]PlaybookStep{{Tool: "  "}}); err == nil {
		t.Fatalf("空工具名应报错")
	}
}

// TestPbkMatcherCheckAllEmpty 全空匹配器拒绝保存。
func TestPbkMatcherCheckAllEmpty(t *testing.T) {
	if err := pbkMatcherCheck(PlaybookMatcher{}); err == nil {
		t.Fatalf("全空匹配器应被拒绝")
	}
	if err := pbkMatcherCheck(PlaybookMatcher{Keywords: []string{"  "}}); err == nil {
		t.Fatalf("全空白项等于全空，应被拒绝")
	}
	if err := pbkMatcherCheck(PlaybookMatcher{Services: []string{"backend"}}); err != nil {
		t.Fatalf("有结构化键应通过：%v", err)
	}
}

// TestPbkApplyToMission 套用到行动：只写 Playbook/Draft，**不动 Plan**。
func TestPbkApplyToMission(t *testing.T) {
	defer withMissionEnv(t)()

	pb := mkPbk("p-ok", PlaybookMatcher{Services: []string{"backend"}, Triggers: []string{"guard"}})
	pb.Steps = []PlaybookStep{
		{Tool: "get_logs", Why: "先看日志"},
		{Tool: "restart_service", Target: "backend", Why: "重启"},
	}
	putPlaybook(t, pb)

	m, err := missionNew("后端恢复失败", Trigger{Kind: "guard", Detail: "backend | 复验未通过"})
	if err != nil {
		t.Fatalf("missionNew: %v", err)
	}
	got, ok := pbkApplyToMission(m.ID, "backend", errKindVerifyFail)
	if !ok || got.ID != "p-ok" {
		t.Fatalf("应命中 p-ok，实为 ok=%v %s", ok, got.ID)
	}
	after, _ := missionGet(m.ID)
	if after.Playbook != "p-ok" {
		t.Fatalf("Playbook 未落：%s", after.Playbook)
	}
	if len(after.Draft) != 2 || len(after.Plan) != 0 {
		t.Fatalf("起草步骤该进 Draft（%d），Plan 必须保持为空（%d）", len(after.Draft), len(after.Plan))
	}

	// 服务不符 → 不套（猜错的建议比没有建议更糟）
	m2, _ := missionNew("链端异常", Trigger{Kind: "guard", Detail: "chain | 端口不通"})
	if _, ok := pbkApplyToMission(m2.ID, "chain", ""); ok {
		t.Fatalf("服务不符时不该套用 backend 的预案")
	}
	if x, _ := missionGet(m2.ID); x.Playbook != "" {
		t.Fatalf("未命中不该写 Playbook：%s", x.Playbook)
	}
}

// TestPbkSaveCleansMatcher 归一化：去空白、去空项、去重（保序）。
func TestPbkSaveCleansMatcher(t *testing.T) {
	m := pbkNormMatcher(PlaybookMatcher{
		Services: []string{" backend ", "", "BACKEND", "web"},
		Keywords: []string{"端口", " 端口 ", " "},
	})
	if len(m.Services) != 2 || m.Services[0] != "backend" || m.Services[1] != "web" {
		t.Fatalf("服务列表归一化不对：%+v", m.Services)
	}
	if len(m.Keywords) != 1 || m.Keywords[0] != "端口" {
		t.Fatalf("关键词归一化不对：%+v", m.Keywords)
	}
}

// TestMissionSvcID Trigger.Detail → 服务 ID 的解析规则。
func TestMissionSvcID(t *testing.T) {
	cases := map[string]string{
		"backend | 复验未通过":    "backend",
		"chain":              "chain",
		"":                   "",
		"这个服务挂了":             "", // 含中文/空格 → 不猜
		"web: 端口占用":          "", // 含冒号 → 不猜
		"  mysql  |  端口冲突  ": "mysql",
	}
	for in, want := range cases {
		if got := missionSvcID(in); got != want {
			t.Errorf("missionSvcID(%q) = %q，期望 %q", in, got, want)
		}
	}
}
