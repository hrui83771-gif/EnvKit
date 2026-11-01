package main

// briefing_test.go —— Phase 2：晨报（确定性事实报告）。
//
// 这一组测试守的是「无模型编造空间」这条验收标准：
//   - 报告里的每条判断都必须能追到某个记录，没有记录就明说"没有/尚未"；
//   - 时间窗口算错（把两天前的崩溃算成今天）比不写更坏，所以要显式验证；
//   - 磁盘趋势只能来自历史快照，没有历史就说"首次记录"。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"envkit/internal/platform"
)

// withBriefingEnv 隔离晨报的全部外部依赖：报告目录、日志目录、服务运行态、
// 链端快照、版本检查结果、配置、行动文件、预案文件。
func withBriefingEnv(t *testing.T) func() {
	t.Helper()
	undoMission := withMissionEnv(t)
	dir := t.TempDir()

	oldDirFn := briefingDirFn
	briefingDirFn = func() string { return dir }

	oldLogDir := logDirOverride
	logDirOverride = dir

	svcRT.Lock()
	oldSvc := svcRT.m
	svcRT.m = map[string]*svcRuntimeRec{}
	svcRT.Unlock()

	oldCfg := cfg
	cfg = Config{Services: []Service{
		{ID: "backend", Label: "后端", Adapter: "local"},
		{ID: "chain", Label: "链端", Adapter: "ssh_chain"},
	}}

	chainMu.Lock()
	oldChain := chainInfo
	chainInfo = ChainInfo{}
	chainMu.Unlock()

	updMu.Lock()
	oldUpd := upd
	upd = updateInfo{Current: appVersion}
	updMu.Unlock()

	return func() {
		briefingDirFn = oldDirFn
		logDirOverride = oldLogDir
		svcRT.Lock()
		svcRT.m = oldSvc
		svcRT.Unlock()
		cfg = oldCfg
		chainMu.Lock()
		chainInfo = oldChain
		chainMu.Unlock()
		updMu.Lock()
		upd = oldUpd
		updMu.Unlock()
		undoMission()
	}
}

// TestBriefingRecentCrashAndBreaker 近 24h 崩溃与熔断必须进"需关注"，且带判定依据。
func TestBriefingRecentCrashAndBreaker(t *testing.T) {
	defer withBriefingEnv(t)()

	now := time.Now()
	at := now.Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	svcWith("backend", func(r *svcRuntimeRec) {
		r.Restarts = 3
		r.BreakerTripped = true
		r.Crashes = []CrashEvent{{At: at, ErrKind: "exit", Why: "端口 8080 被占用", Port: 8080}}
	})

	facts, err := briefingGenerate(now)
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if facts.Crash != 1 {
		t.Fatalf("近 24h 崩溃应计 1 条，实为 %d", facts.Crash)
	}
	joined := strings.Join(facts.Attn, "\n")
	for _, want := range []string{"连续失败 3 次", "熔断"} {
		if !strings.Contains(joined, want) {
			t.Errorf("需关注里缺少 %q：\n%s", want, joined)
		}
	}

	b, err := os.ReadFile(filepath.Join(briefingDirFn(), facts.Report))
	if err != nil {
		t.Fatalf("读报告：%v", err)
	}
	md := string(b)
	for _, want := range []string{
		"# EnvKit 晨报",
		"判定口径",
		"端口 8080 被占用",
		"## 服务",
		"## 结论",
		"**需关注",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("报告缺少 %q：\n%s", want, md)
		}
	}
	// 报告文件名必须是固定格式（前端"下载"用它当文件名）
	if len(facts.Report) != len("2006-01-02-1504.md") || !strings.HasSuffix(facts.Report, ".md") {
		t.Fatalf("报告文件名格式不对：%q", facts.Report)
	}
}

// TestBriefingOldCrashIgnored 两天前的崩溃不算"近 24 小时"。
func TestBriefingOldCrashIgnored(t *testing.T) {
	defer withBriefingEnv(t)()

	now := time.Now()
	old := now.AddDate(0, 0, -2).Format("2006-01-02 15:04:05")
	svcWith("backend", func(r *svcRuntimeRec) {
		r.Crashes = []CrashEvent{{At: old, ErrKind: "exit", Why: "很久以前崩的"}}
	})

	facts, err := briefingGenerate(now)
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if facts.Crash != 0 {
		t.Fatalf("两天前的崩溃不该计入 24h，实为 %d", facts.Crash)
	}
	b, _ := os.ReadFile(filepath.Join(briefingDirFn(), facts.Report))
	if !strings.Contains(string(b), "没有崩溃记录") {
		t.Fatalf("应明说没有崩溃记录：\n%s", string(b))
	}
	if strings.Contains(string(b), "很久以前崩的") {
		t.Fatalf("过期崩溃不该出现在 24h 段里")
	}
}

// TestBriefingHonestOnEmpty 没数据就明说"没有/尚未"，不编。
func TestBriefingHonestOnEmpty(t *testing.T) {
	defer withBriefingEnv(t)()

	facts, err := briefingGenerate(time.Now())
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	b, _ := os.ReadFile(filepath.Join(briefingDirFn(), facts.Report))
	md := string(b)
	for _, want := range []string{
		"没有崩溃记录",
		"没有未收口的行动",
		"尚未检测",   // 链端未检测
		"尚未检查更新", // 版本未检查
	} {
		if !strings.Contains(md, want) {
			t.Errorf("空数据下报告缺少 %q：\n%s", want, md)
		}
	}
	// 空环境不该凭空产生需关注项（会把"需关注"训练成噪音）
	if len(facts.Attn) != 0 {
		t.Fatalf("空环境不该有关注项：%v", facts.Attn)
	}
}

// TestBriefingDiskDeltaFromHistory 磁盘趋势只来自历史快照；没有历史说"首次记录"。
func TestBriefingDiskDeltaFromHistory(t *testing.T) {
	defer withBriefingEnv(t)()

	now := time.Now()
	dir := briefingDirFn()
	free, hasFree := platform.DiskFreeBytes()
	if !hasFree {
		t.Skip("本平台不采集磁盘，跳过趋势断言")
	}

	// 无历史 → 首次记录
	facts, err := briefingGenerate(now)
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, facts.Report))
	if !strings.Contains(string(b), "首次记录") {
		t.Fatalf("没有历史时应说首次记录：\n%s", string(b))
	}

	// 造一条 3 天前的快照（比现在多 1GB），再生成一次
	three := now.AddDate(0, 0, -3).Format("2006-01-02")
	line, _ := json.Marshal(diskSnap{Day: three, At: three + " 08:30:00", FreeBytes: int64(free) + (1 << 30)})
	if err := os.WriteFile(diskHistPath(dir), append(line, '\n'), 0o644); err != nil {
		t.Fatalf("写历史：%v", err)
	}
	facts2, err := briefingGenerate(now.Add(time.Minute))
	if err != nil {
		t.Fatalf("二次生成失败：%v", err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, facts2.Report))
	if !strings.Contains(string(b2), "3 天前") || !strings.Contains(string(b2), "变化 -") {
		t.Fatalf("应给出与 3 天前的对比且为减少：\n%s", string(b2))
	}

	// 今天的快照只该有一条（重复生成不叠加）
	hist := loadDiskHist(dir)
	today := now.Format("2006-01-02")
	n := 0
	for _, h := range hist {
		if h.Day == today {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("今天的磁盘快照应只有 1 条，实为 %d", n)
	}
}

// TestBriefingPruneKeepsNewest 只保留最近 30 份。
func TestBriefingPruneKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 8, 30, 0, 0, time.Local)
	for i := 0; i < brfKeepFiles+5; i++ {
		name := base.AddDate(0, 0, i).Format("2006-01-02-1504") + ".md"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("造文件：%v", err)
		}
	}
	briefingPrune(dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != brfKeepFiles {
		t.Fatalf("应保留 %d 份，实为 %d", brfKeepFiles, len(entries))
	}
	// 留下的必须是最新的那批
	oldest := base.Format("2006-01-02-1504") + ".md"
	for _, e := range entries {
		if e.Name() == oldest {
			t.Fatalf("最老的一份应被清掉")
		}
	}
}

// TestBriefingListsPendingMissions 未收口的行动要出现在晨报里（这就是"待你继续"）。
func TestBriefingListsPendingMissions(t *testing.T) {
	defer withBriefingEnv(t)()

	m, err := missionNew("检查磁盘趋势", Trigger{Kind: "manual"})
	if err != nil {
		t.Fatalf("missionNew: %v", err)
	}
	if _, err := missionUpdate(m.ID, func(x *Mission) error {
		x.PauseWhy = "连接中断，可点继续"
		return missionGo(x, msPaused)
	}); err != nil {
		t.Fatalf("置 paused: %v", err)
	}

	facts, err := briefingGenerate(time.Now())
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	b, _ := os.ReadFile(filepath.Join(briefingDirFn(), facts.Report))
	md := string(b)
	if !strings.Contains(md, "检查磁盘趋势") || !strings.Contains(md, "待你继续") {
		t.Fatalf("未收口行动应列在晨报里：\n%s", md)
	}

	// skipID：晨报自己的行动不该把自己算进"待你继续"
	facts2, err := briefingGenerateInto(briefingDirFn(), time.Now().Add(time.Minute), m.ID)
	if err != nil {
		t.Fatalf("二次生成失败：%v", err)
	}
	b2, _ := os.ReadFile(filepath.Join(briefingDirFn(), facts2.Report))
	if strings.Contains(string(b2), "检查磁盘趋势") {
		t.Fatalf("skipID 指定的行动不该出现")
	}
}

// TestBriefingAttachMarksMissionDone 晨报挂载后行动直接收口（只读产出，不等人点）。
func TestBriefingAttachMarksMissionDone(t *testing.T) {
	defer withBriefingEnv(t)()

	m, err := missionNew("晨报 测试", Trigger{Kind: "schedule"})
	if err != nil {
		t.Fatalf("missionNew: %v", err)
	}
	briefingAttach(m.ID, time.Now())

	got, ok := missionGet(m.ID)
	if !ok {
		t.Fatalf("行动丢了")
	}
	if got.State != msDone {
		t.Fatalf("晨报生成后应直接 done，实为 %s", got.State)
	}
	if got.Report == "" {
		t.Fatalf("应挂上报告文件名")
	}
	if got.Result == nil || !got.Result.Verified {
		t.Fatalf("报告型行动的收口结论应标记为已验证产出")
	}
	if _, err := os.Stat(filepath.Join(briefingDirFn(), got.Report)); err != nil {
		t.Fatalf("报告文件应存在：%v", err)
	}
}

// TestBriefingWorkStatsLast24h 晨报要带"过去一天做了什么"——
// 数据必须来自审计与轨迹本身，且严格按 24 小时窗口筛（多算一天比少算更坏）。
func TestBriefingWorkStatsLast24h(t *testing.T) {
	defer withBriefingEnv(t)()

	now := time.Now().Truncate(time.Second)
	auditRoot := withAuditDir(t, nil)

	traceRoot := t.TempDir()
	oldTrace := traceDir
	traceDir = func() string { return traceRoot }
	t.Cleanup(func() { traceDir = oldTrace })

	mkAudit := func(at time.Time, e AuditEntry) (string, string) {
		e.Ts = at.Format("2006-01-02 15:04:05.000")
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal 审计：%v", err)
		}
		return "audit-" + at.Format("20060102") + ".jsonl", string(b) + "\n"
	}
	auditFiles := map[string]string{}
	for _, row := range []struct {
		at time.Time
		e  AuditEntry
	}{
		{now.Add(-time.Hour), AuditEntry{Actor: actUser, Action: "start_service", Target: "web",
			Result: resOK, Verify: "已复验通过：端口 + HTTP"}},
		{now.Add(-2 * time.Hour), AuditEntry{Actor: actAI, Action: "db_backup", Target: "farm",
			Result: resFail}},
		// 25 小时前：属于"昨天"但不在窗口内，必须排除（否则统计会虚高）
		{now.Add(-25 * time.Hour), AuditEntry{Actor: actGuard, Action: "chain_autorecover",
			Target: "node0", Result: resOK}},
	} {
		name, line := mkAudit(row.at, row.e)
		auditFiles[name] += line
	}
	for name, body := range auditFiles {
		writeAuditFile(t, auditRoot, name, body)
	}

	traceFiles := map[string]string{}
	for _, tr := range []Trace{
		{TraceID: "t-in", Started: now.Add(-3 * time.Hour).Format("2006-01-02 15:04:05.000"),
			Outcome: outSuccess, Usage: &TraceUsage{Prompt: 1200, Completion: 300, Hit: 800, HasCache: true}},
		// 26 小时前：同样不计入
		{TraceID: "t-old", Started: now.Add(-26 * time.Hour).Format("2006-01-02 15:04:05.000"),
			Outcome: outFailed, Usage: &TraceUsage{Prompt: 99999, Completion: 99999}},
	} {
		at, _ := time.ParseInLocation("2006-01-02 15:04:05.000", tr.Started, time.Local)
		name := "trace-" + at.Format("20060102") + ".jsonl"
		b, err := json.Marshal(tr)
		if err != nil {
			t.Fatalf("marshal 轨迹：%v", err)
		}
		traceFiles[name] += string(b) + "\n"
	}
	for name, body := range traceFiles {
		if err := os.WriteFile(filepath.Join(traceRoot, name), []byte(body), 0o644); err != nil {
			t.Fatalf("写轨迹：%v", err)
		}
	}

	facts, err := briefingGenerate(now)
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if facts.Ops != 2 {
		t.Errorf("过去 24h 操作应计 2 次，实为 %d", facts.Ops)
	}
	if facts.Tokens != 1500 {
		t.Errorf("过去 24h token 应计 1500（1200+300），实为 %d", facts.Tokens)
	}

	b, _ := os.ReadFile(filepath.Join(briefingDirFn(), facts.Report))
	md := string(b)
	for _, want := range []string{
		"## 过去 24 小时的工作",
		"操作 2 次",
		"成功 1",
		"失败 1",
		"复验：1 次有结论，通过 1 次（100%）",
		"按主体：用户手动 1 · 助手执行 1",
		"start_service 1 次",
		"AI 轨迹 1 条：成功 1",
		"Token：输入 1,200 + 输出 300 = 1,500",
		"缓存命中 800",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("工作统计缺少 %q：\n%s", want, md)
		}
	}
	// 窗口外的记录不能混进来
	for _, bad := range []string{"chain_autorecover", "99999", "操作 3 次"} {
		if strings.Contains(md, bad) {
			t.Errorf("24h 窗口外的记录/数字混进了报告：%q\n%s", bad, md)
		}
	}
}

// TestBriefingWorkStatsHonestOnEmpty 没有审计与轨迹时明说"没有"，不报 0。
func TestBriefingWorkStatsHonestOnEmpty(t *testing.T) {
	defer withBriefingEnv(t)()

	withAuditDir(t, nil)
	traceRoot := t.TempDir()
	oldTrace := traceDir
	traceDir = func() string { return traceRoot }
	t.Cleanup(func() { traceDir = oldTrace })

	facts, err := briefingGenerate(time.Now())
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	b, _ := os.ReadFile(filepath.Join(briefingDirFn(), facts.Report))
	md := string(b)
	if !strings.Contains(md, "过去 24 小时没有操作与 AI 轨迹记录") {
		t.Fatalf("空数据应明说没有记录，而不是报 0：\n%s", md)
	}
}

// TestBriefCount 千分位（token 六位数不加分隔线读不出量级）。
func TestBriefCount(t *testing.T) {
	cases := map[int64]string{
		0: "0", 12: "12", 999: "999", 1000: "1,000",
		1234567: "1,234,567", -2500: "-2,500",
	}
	for in, want := range cases {
		if got := briefCount(in); got != want {
			t.Errorf("briefCount(%d) = %q，期望 %q", in, got, want)
		}
	}
}

// TestHumanBytes 展示用格式化（不参与判定，但错得很显眼）。
func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:        "512 B",
		2048:       "2.0 KB",
		5 << 20:    "5.0 MB",
		3 << 30:    "3.0 GB",
		-(2 << 30): "-2.0 GB",
		0:          "0 B",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q，期望 %q", in, got, want)
		}
	}
}
