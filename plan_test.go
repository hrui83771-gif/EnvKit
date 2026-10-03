package main

// plan_test.go —— v2.3 N4/N5/N6 的单测
//
// 三组：
//   N4 守护状态：不可达时不谎报"正常"、能力告知要能让模型直接引用
//   N5 工具调用预算：分级要给宽探索类、超限必须说清楚
//   N6 数据库只读：查询结果必须显式告知截断，绝不让模型误以为看到全部

import (
	"strings"
	"testing"
)

// ===== N4 守护状态 =====

func withGuardCfg(t *testing.T, host string, guard, auto bool, secs int) func() {
	t.Helper()
	oldHost, oldGuard, oldAuto, oldSecs :=
		cfg.Chain.SSHHost, cfg.Chain.ChainGuard, cfg.Chain.ChainAutoRecover, cfg.Chain.ChainGuardSecs
	cfg.Chain.SSHHost, cfg.Chain.ChainGuard = host, guard
	cfg.Chain.ChainAutoRecover, cfg.Chain.ChainGuardSecs = auto, secs
	guardMu.Lock()
	guardRec = guardStatus{}
	guardMu.Unlock()
	return func() {
		cfg.Chain.SSHHost, cfg.Chain.ChainGuard = oldHost, oldGuard
		cfg.Chain.ChainAutoRecover, cfg.Chain.ChainGuardSecs = oldAuto, oldSecs
		guardMu.Lock()
		guardRec = guardStatus{}
		guardMu.Unlock()
	}
}

// 守护刚启动、还没跑过第一轮 → 状态必须是"尚未检测"，不能报"正常"
//
// 这是本项最重要的断言：说"正常"是撒谎——它只是还不知道。
// 与 v2.0 修过的链端判据同源：拿不到事实就说没验到。
func TestGuardStatusUnknownBeforeFirstCheck(t *testing.T) {
	defer withGuardCfg(t, "192.0.2.10", true, true, 60)()

	s := guardSnapshot()
	if s.LastCheck != "" {
		t.Skip("前置不成立：已有检测记录")
	}
	if !strings.Contains(s.Note, "尚未完成第一轮检测") {
		t.Errorf("未检测过时不得说正常（那是撒谎），实际：%q", s.Note)
	}
	if !strings.Contains(s.Note, "60") {
		t.Errorf("应告知检测间隔，让用户知道多久会跑一轮：%q", s.Note)
	}
}

func TestGuardStatusDisabledIsExplicit(t *testing.T) {
	defer withGuardCfg(t, "192.0.2.10", false, false, 60)()

	s := guardSnapshot()
	if !strings.Contains(s.Note, "未开启") {
		t.Errorf("守护未开启必须明说，否则用户以为有自动恢复：%q", s.Note)
	}
	// 关键：AI 必须知道"有这能力但没开"，才能建议用户去开
	brief := guardBrief()
	if !strings.Contains(brief, "具备链端自动恢复能力") {
		t.Errorf("即使未开启也要告知能力存在（模型据此建议用户开启）：%q", brief)
	}
	if !strings.Contains(brief, "未开启") {
		t.Errorf("必须同时说清当前没开：%q", brief)
	}
}

// 连续不可达时必须说清"不会自动恢复"，不能让模型建议重启
func TestGuardStatusUnreachableSaysNoAutoRecover(t *testing.T) {
	defer withGuardCfg(t, "192.0.2.10", true, true, 60)()
	guardMark("skip_unreachable", "连不上", false)
	guardSetUnreachable(3)

	s := guardSnapshot()
	if !strings.Contains(s.Note, "不会") || !strings.Contains(s.Note, "3") {
		t.Errorf("连续不可达要说明不会自动恢复并给出轮数，实际：%q", s.Note)
	}
	// 不可达时不该说"正在自动恢复"——那会让模型对用户说谎
	if strings.Contains(s.Note, "已自动恢复") || strings.Contains(s.Note, "正在自动恢复") {
		t.Errorf("不可达时不得声称在恢复：%q", s.Note)
	}
}

func TestGuardMarkCounts(t *testing.T) {
	defer withGuardCfg(t, "192.0.2.10", true, true, 60)()

	guardMark("ok", "正常", true)
	guardMark("recover", "节点已拉起", true)
	guardMark("skip_unreachable", "连不上", false)

	s := guardSnapshot()
	if s.RecoverCount != 1 {
		t.Errorf("恢复次数应累计，实际 %d", s.RecoverCount)
	}
	if s.SkipCount != 1 {
		t.Errorf("跳过次数应累计，实际 %d", s.SkipCount)
	}
	if s.LastUnreachable != 1 {
		t.Errorf("连续不可达应累计，实际 %d", s.LastUnreachable)
	}
	// 恢复成功后连续不可达应清零（它恢复了）
	guardMark("recover", "又拉起一次", true)
	if s = guardSnapshot(); s.LastUnreachable != 0 {
		t.Errorf("恢复后连续不可达应清零，实际 %d", s.LastUnreachable)
	}
}

func TestGuardNoHostIsNotRunning(t *testing.T) {
	defer withGuardCfg(t, "", true, true, 60)()
	s := guardSnapshot()
	if !strings.Contains(s.Note, "未配置") {
		t.Errorf("未配置地址时应说不运行：%q", s.Note)
	}
	if guardBrief() != "" {
		t.Error("未配置地址时不该给 AI 注入说明（会干扰它的判断）")
	}
}

func TestGuardIntervalClampedToMinimum(t *testing.T) {
	defer withGuardCfg(t, "192.0.2.10", true, true, 5)() // 低于下限 30
	if s := guardSnapshot(); s.IntervalSec != 30 {
		t.Errorf("间隔应夹到下限 30，实际 %d", s.IntervalSec)
	}
}

// ===== N5 工具调用预算 =====

// 探索类问题必须拿到比状态类宽得多的预算
//
// 用户报的真实事故是"问大问题找了很多次文件，超出 8 次上限"——
// 固定 8 对探索类问题不够，是本项要修的核心。
func TestBudgetExploreGetsMoreThanStatus(t *testing.T) {
	explore := budgetFor("这个项目怎么跑起来？入口在哪")
	status := budgetFor("服务现在是什么状态？")

	if explore.Max <= status.Max {
		t.Errorf("探索类预算应大于状态类：explore=%d status=%d", explore.Max, status.Max)
	}
	if explore.Max < 8 {
		t.Errorf("探索类预算不应低于原来的固定 8（用户报的问题正是被它截断），实际 %d", explore.Max)
	}
	if status.Max > 6 {
		t.Errorf("状态类不需要那么多轮（多轮往往意味着打转），实际 %d", status.Max)
	}
}

func TestBudgetDeepAnalysis(t *testing.T) {
	b := budgetFor("统计一下每个产品的销量分布并对比上个月")
	if b.Max < budgetFor("服务现在状态如何").Max {
		t.Errorf("数据分析类预算不应低于状态类：deep=%d", b.Max)
	}
	if !strings.Contains(b.Reason, "数据") {
		t.Errorf("理由要说清为什么给这么多（要让用户能理解）：%q", b.Reason)
	}
}

func TestBudgetAlwaysAboveMin(t *testing.T) {
	for _, q := range []string{"", "在吗", "帮我备份", "这个项目的架构是怎样的，请梳理依赖与实现逻辑"} {
		if b := budgetFor(q); b.Max < aiTurnMin {
			t.Errorf("预算下限应保证够两轮（问+答），实际 %d（q=%q）", b.Max, q)
		}
	}
}

// 超限提示必须说清"发生了什么 + 下一步怎么做"
func TestBudgetNoticeActionable(t *testing.T) {
	b := budgetFor("这个项目怎么跑起来")
	n := budgetNotice(b, b.Max)

	if !strings.Contains(n, itoa(b.Max)) {
		t.Errorf("提示要包含实际预算值（写死 8 会误导）：%s", n)
	}
	// 必须说清"没答完"而不是让用户以为这就是完整答案
	if !strings.Contains(n, "继续") {
		t.Errorf("应告诉用户可以怎么继续（否则他以为这就是全部结论）：%s", n)
	}
	// 反向检查：不能出现"出错"这种让人误以为故障的措辞。
	// 文案里的"这不是出错"是明确否定，但断言只查"达到上限"这一句不含它。
	head := strings.SplitN(n, "\n", 2)[0]
	if strings.Contains(head, "出错") || strings.Contains(head, "失败") {
		t.Errorf("首句不应让用户以为故障（要与故障区分开）：%s", head)
	}
}

// ===== Plan 展示 =====

func TestPlanTrackAndLine(t *testing.T) {
	p := &planTrack{}
	p.add("list_project", "", "")
	p.add("search_files", "db.go", "")
	p.add("search_files", "conf", "")
	if len(p.Steps) != 3 {
		t.Fatalf("应记录 3 步，实际 %d", len(p.Steps))
	}
	// 序号连续
	for i, s := range p.Steps {
		if s.Seq != i+1 {
			t.Errorf("第 %d 步序号应为 %d，实际 %d", i, i+1, s.Seq)
		}
	}
	// 自然语言而不是工具名：用户看到"正在搜索 x"比"search_files(x)"有用
	if l := planLine("search_files", "db.go"); !strings.Contains(l, "正在搜索") || !strings.Contains(l, "db.go") {
		t.Errorf("计划行应是自然语言且带目标，实际：%q", l)
	}
}

func TestPlanSummaryDeduplicates(t *testing.T) {
	p := &planTrack{}
	p.add("search_files", "a", "")
	p.add("search_files", "b", "")
	p.add("read_file", "c", "")
	s := planSummary(p, "已得出结论")

	if !strings.Contains(s, "3 步") {
		t.Errorf("应报总步数：%s", s)
	}
	if !strings.Contains(s, "search_files×2") {
		t.Errorf("重复工具应合并计数：%s", s)
	}
	if !strings.Contains(s, "read_file") {
		t.Errorf("单次工具也应列出：%s", s)
	}
}

func TestPlanTrackBounded(t *testing.T) {
	p := &planTrack{}
	for i := 0; i < 100; i++ {
		p.add("get_logs", "x", "")
	}
	if len(p.Steps) > 40 {
		t.Errorf("计划步数应封顶（死循环不能把 UI 撑爆），实际 %d", len(p.Steps))
	}
}

func TestPlanNilSafe(t *testing.T) {
	var p *planTrack
	p.add("x", "", "") // 不该 panic
	if s := planSummary(p, "x"); s != "" {
		t.Errorf("nil 轨迹应返回空串，实际 %q", s)
	}
}

// ===== N6 数据库只读 =====

// 写操作必须被拒——AI 拿到查询工具不代表能改数据
func TestDBQueryRejectsWrites(t *testing.T) {
	bad := []string{
		"DROP TABLE farm_user",
		"DELETE FROM farm_user",
		"UPDATE farm_user SET name='x'",
		"INSERT INTO farm_user (name) VALUES ('x')",
		"ALTER TABLE farm_user ADD COLUMN x int",
		"TRUNCATE TABLE farm_user",
		"CREATE TABLE t (id int)",
		"GRANT ALL ON *.* TO 'x'@'y'",
	}
	for _, s := range bad {
		if _, err := dbReadOnlySQL(s); err == nil {
			t.Errorf("写操作必须被拒绝：%s", s)
		}
	}
}

func TestDBQueryAllowsReads(t *testing.T) {
	good := []string{
		"SELECT * FROM farm_user LIMIT 10",
		"SHOW TABLES",
		"DESC farm_user",
		"EXPLAIN SELECT * FROM farm_user",
		"SELECT COUNT(*) FROM farm_log",
		"WITH t AS (SELECT 1) SELECT * FROM t",
		"SELECT name FROM user WHERE id=1 LIMIT 1",
	}
	for _, s := range good {
		if _, err := dbReadOnlySQL(s); err != nil {
			t.Errorf("只读查询不该被拒（%s）：%v", s, err)
		}
	}
}

// 多语句必须被拒：分号拼接是注入的经典载体
func TestDBQueryRejectsMultiStatement(t *testing.T) {
	bad := []string{
		"SELECT 1; DROP TABLE t",
		"SELECT 1;SELECT 2",
		// 注释里的分号不是语句分隔——`SELECT 1 -- ; DROP` 整行后都是注释，
		// MySQL 只执行 SELECT 1，这条本身是安全的（曾误判为漏洞）。
		// 真正的攻击是注释结束后再接分号：
		"SELECT 1 -- x\n; DROP TABLE t",
		"SELECT 1 # x\n; DROP TABLE t",
		"SELECT 1 /* x */; DROP TABLE t",
	}
	for _, s := range bad {
		if _, err := dbReadOnlySQL(s); err == nil {
			t.Errorf("多语句必须被拒绝：%q", s)
		}
	}
	// 注释里的分号不该误判为多语句（否则正常注释的查询会被无理由拒绝）
	if _, err := dbReadOnlySQL("SELECT name FROM t -- 这是个;注释\n LIMIT 1"); err != nil {
		t.Errorf("注释里的分号不是语句分隔，不该被拒：%v", err)
	}
	// 单条带结尾分号应允许
	if _, err := dbReadOnlySQL("SELECT * FROM t LIMIT 1;"); err != nil {
		t.Errorf("单条语句的结尾分号应允许：%v", err)
	}
}

func TestDBQueryToolIsAutoNotConfirm(t *testing.T) {
	// 只读工具不该要确认——给只读操作加确认卡只会训练用户闭眼点确认。
	// 真正兜底的是 dbReadOnlySQL 的四道防线（全在 MySQL 侧生效）。
	for _, name := range []string{"db_query", "db_list"} {
		tool, ok := aiToolRegistry[name]
		if !ok {
			t.Fatalf("工具 %s 未注册", name)
		}
		if tool.Write {
			t.Errorf("%s 是只读工具，不该标 Write（否则会弹确认卡）", name)
		}
		if v := PolicyGate(name, "farm", actAI); v.OptIn {
			t.Errorf("%s 不该要求 opt-in", name)
		}
		if v := PolicyGate(name, "farm", actAI); v.NeedsConfirm() {
			t.Errorf("%s 不该需要确认（只读）", name)
		}
	}
}

func TestDBQueryToolHasLimitInDescription(t *testing.T) {
	// 工具描述必须写清"必须自己带 LIMIT"——模型不看 schema 的 description 也该知道
	tool := aiToolRegistry["db_query"]
	if tool.Desc == "" {
		t.Fatal("db_query 必须有描述")
	}
	for _, must := range []string{"LIMIT", "COUNT"} {
		if !strings.Contains(tool.Desc, must) {
			t.Errorf("描述应提到 %q（教模型用聚合而不是拉全表）：%s", must, tool.Desc)
		}
	}
	// 只读工具也要说清它不能做什么
	if !strings.Contains(tool.Desc, "只读") {
		t.Errorf("描述应明确说是只读：%s", tool.Desc)
	}
}
