package main

// memory_test.go —— M3 用户记忆的单测
//
// 记忆是"AI 下次仍然会遵守的东西"，错了就是持续性误导，所以几条底线必须钉死：
//   - 重复导入/重复添加不产生垃圾（按内容哈希去重）
//   - 超长内容被挡（记忆要进提示词，不能无限长）
//   - 坏行跳过而不是整体失败（文件常被手工编辑）
//   - 永远不写用户真实文件（memoryFile 替换到临时目录）

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withMemoryEnv(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	old := memoryFile
	memoryFile = func() string { return filepath.Join(dir, "memories.json") }
	return func() { memoryFile = old }
}

func TestMemoryUpsertDedup(t *testing.T) {
	defer withMemoryEnv(t)()

	m1, isNew, err := memUpsert("备份前先停掉后端服务", "备份,前端", false, "manual")
	if err != nil || !isNew {
		t.Fatalf("首次添加应成功且标记为新增：%v new=%v", err, isNew)
	}
	// 完全相同的文本再添加一次 → 应更新而非新增
	m2, isNew2, err := memUpsert("备份前先停掉后端服务", "备份", true, "manual")
	if err != nil {
		t.Fatalf("重复添加不应报错：%v", err)
	}
	if isNew2 {
		t.Fatal("相同内容重复添加不该产生新条目")
	}
	if m2.ID != m1.ID {
		t.Fatalf("同内容应保持同一 id：%s vs %s", m1.ID, m2.ID)
	}
	if !m2.Always || m2.Tags != "备份" {
		t.Fatalf("重复添加应更新标签与开关，实际 always=%v tags=%q", m2.Always, m2.Tags)
	}
	if n := len(loadMemories()); n != 1 {
		t.Fatalf("文件里应只有 1 条，实际 %d", n)
	}
}

func TestMemoryUpsertRejectsEmptyAndTooLong(t *testing.T) {
	defer withMemoryEnv(t)()

	if _, _, err := memUpsert("   ", "", false, "manual"); err == nil {
		t.Fatal("空内容必须被拒绝")
	}
	long := strings.Repeat("字", memMaxText+1)
	if _, _, err := memUpsert(long, "", false, "manual"); err == nil {
		t.Fatalf("超过 %d 字必须被拒绝", memMaxText)
	}
}

func TestMemoryUpsertAcceptsMaxLength(t *testing.T) {
	defer withMemoryEnv(t)()
	exact := strings.Repeat("字", memMaxText)
	if _, _, err := memUpsert(exact, "", false, "manual"); err != nil {
		t.Fatalf("正好 %d 字应被接受：%v", memMaxText, err)
	}
}

func TestMemoryDeleteAndToggle(t *testing.T) {
	defer withMemoryEnv(t)()

	m, _, _ := memUpsert("链端地址用 127.0.0.1", "", false, "manual")
	if err := memDelete(m.ID); err != nil {
		t.Fatalf("删除应成功：%v", err)
	}
	if n := len(loadMemories()); n != 0 {
		t.Fatalf("删除后应为空，实际 %d 条", n)
	}
	if err := memDelete(m.ID); err == nil {
		t.Fatal("删除不存在的记忆应报错")
	}

	m2, _, _ := memUpsert("t", "", false, "manual")
	if err := memToggle(m2.ID); err != nil {
		t.Fatalf("切换应成功：%v", err)
	}
	if !loadMemories()[0].Always {
		t.Fatal("切换后应为 always")
	}
	if err := memToggle("nope"); err == nil {
		t.Fatal("切换不存在的记忆应报错")
	}
}

func TestMemoryBadLineSkipped(t *testing.T) {
	defer withMemoryEnv(t)()

	p := memoryFile()
	_ = os.WriteFile(p, []byte(
		`{"id":"m1","text":"第一条","always":true}`+"\n"+
			"这行是坏 JSON\n"+
			`{"id":"m2","text":"   "}`+"\n"+ // 空白正文也应跳过
			`{"id":"m3","text":"第三条"}`+"\n"), 0600)

	ms := loadMemories()
	if len(ms) != 2 {
		t.Fatalf("坏行与空正文应被跳过，实际留下 %d 条", len(ms))
	}
	if ms[0].Text != "第一条" || ms[1].Text != "第三条" {
		t.Fatalf("保留的内容不对：%q / %q", ms[0].Text, ms[1].Text)
	}
}

func TestMemoryNoFileReturnsEmpty(t *testing.T) {
	defer withMemoryEnv(t)()
	if ms := loadMemories(); ms == nil || len(ms) != 0 {
		t.Fatalf("文件不存在时应返回空数组而非 nil，实际 %v", ms)
	}
}

// 导入要能容忍用户从别处粘来的各种格式
func TestParseImportFormats(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  int
		check func([]importItem) bool
	}{
		{"每行一条", "第一条\n第二条\n第三条", 3, nil},
		{"带项目符号", "- 第一条\n* 第二条", 2, nil},
		{"跳过注释", "# 注释\n第一条\n// 注释2", 1, nil},
		{"带标签", "第一条 | 备份,前端", 1, func(it []importItem) bool {
			return it[0].tags == "备份,前端" && it[0].text == "第一条"
		}},
		{"always 关键字", "重要规矩 | always", 1, func(it []importItem) bool {
			return it[0].always && it[0].tags == ""
		}},
		{"JSON 数组", `[{"text":"甲","tags":"x"},{"text":"乙","always":true},{"text":"  "}]`, 2, nil},
		{"空输入", "   \n\n", 0, nil},
	}
	for _, c := range cases {
		got := parseImport(c.in)
		if len(got) != c.want {
			t.Errorf("%s：期望 %d 条，实际 %d 条（%+v）", c.name, c.want, len(got), got)
			continue
		}
		if c.check != nil && !c.check(got) {
			t.Errorf("%s：解析结果不对 %+v", c.name, got)
		}
	}
}

func TestMemImportBatchCounts(t *testing.T) {
	defer withMemoryEnv(t)()

	added, updated, failed := memImportBatch("记忆一\n记忆二")
	if added != 2 || updated != 0 || failed != 0 {
		t.Fatalf("首次导入应新增 2 条，实际 +%d ~%d !%d", added, updated, failed)
	}
	added, updated, failed = memImportBatch("记忆一\n记忆三")
	if added != 1 || updated != 1 || failed != 0 {
		t.Fatalf("二次导入应 +1 ~1，实际 +%d ~%d !%d", added, updated, failed)
	}
}

func TestMemImportCountsEmptyAsFailed(t *testing.T) {
	defer withMemoryEnv(t)()
	// parseImport 会把空行全滤掉，所以这里换个方式验证 failed 计数：
	// 超长内容会被 memUpsert 拒绝
	added, _, failed := memImportBatch(strings.Repeat("字", memMaxText+1))
	if failed != 1 || added != 0 {
		t.Fatalf("超长内容应计入 failed，实际 +%d !%d", added, failed)
	}
}

// always 的记忆必须无条件注入；非 always 只在相关时注入
func TestMemoriesForAlwaysFirst(t *testing.T) {
	defer withMemoryEnv(t)()

	_, _, _ = memUpsert("永远生效的规矩", "", true, "manual")
	_, _, _ = memUpsert("关于数据库备份的规矩", "备份", false, "manual")

	ms := memoriesFor("启动前端", 10)
	if len(ms) == 0 || ms[0].Text != "永远生效的规矩" {
		t.Fatalf("always 记忆必须排最前且无条件注入，实际 %+v", ms)
	}
	if len(ms) != 1 {
		t.Fatalf("不相关的非 always 记忆不该被注入，实际 %d 条：%+v", len(ms), ms)
	}

	// 任务相关时（非 always）才注入
	ms2 := memoriesFor("帮我做一次数据库备份", 10)
	if len(ms2) != 2 {
		t.Fatalf("任务相关时非 always 记忆应注入，实际 %d 条：%+v", len(ms2), ms2)
	}
}

func TestMemoriesForRespectsMax(t *testing.T) {
	defer withMemoryEnv(t)()
	for i := 0; i < 5; i++ {
		_, _, _ = memUpsert("规矩"+string(rune('A'+i)), "", true, "manual")
	}
	if got := memoriesFor("", 3); len(got) != 3 {
		t.Fatalf("应受 max 限制为 3 条，实际 %d", len(got))
	}
}

func TestMemoriesBriefTruncates(t *testing.T) {
	defer withMemoryEnv(t)()
	long := strings.Repeat("长", 200)
	_, _, _ = memUpsert(long, "", true, "manual")
	b := memoriesBrief(memoriesFor("", 5))
	if n := len([]rune(b)); n > 90 {
		t.Fatalf("注入文本应截断到 80 字内（含前缀），实际 %d 字", n)
	}
	if !strings.Contains(b, "…") {
		t.Fatal("截断后应有省略号")
	}
}

func TestMemIDStable(t *testing.T) {
	a, b := memID("同样的内容"), memID("同样的内容")
	if a != b {
		t.Fatalf("同样内容应得到同样 id：%s vs %s", a, b)
	}
	if memID("A") == memID("B") {
		t.Fatal("不同内容不应撞 id")
	}
}

// 记忆文件坏了不能导致保存时丢数据：saveMemories 先备份再替换
func TestMemorySaveKeepsBackup(t *testing.T) {
	defer withMemoryEnv(t)()

	_, _, _ = memUpsert("第一条", "", false, "manual")
	_, _, _ = memUpsert("第二条", "", false, "manual")
	if _, err := os.Stat(memoryFile() + ".bak"); err != nil {
		t.Fatalf("第二次保存应留下 .bak：%v", err)
	}
	if n := len(loadMemories()); n != 2 {
		t.Fatalf("应有 2 条，实际 %d", n)
	}
}

func TestMemoryUpsertRespectsCountLimit(t *testing.T) {
	defer withMemoryEnv(t)()
	for i := 0; i < memMaxCount+5; i++ {
		_, _, _ = memUpsert("规矩"+string(rune(i%26))+"-"+itoaTest(i), "", false, "manual")
	}
	if n := len(loadMemories()); n > memMaxCount {
		t.Fatalf("总数应受 %d 上限约束，实际 %d", memMaxCount, n)
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// ---- 回归：工具把"存在"说成"不存在" ----
//
// 用户截图里就是这样：面板上明明有一条「问候语以老板开头」，AI 却答"当前没有记忆条目"。
// 根因是 list 走 memoriesFor("", 50)，而它对空任务只返回 always 条目（memory.go 的相关性
// 分支 `if t != ""`）。于是「按需注入、无标签」的记忆永远进不了列表 —— 数据在库里，
// 回答却说没有。这属于"把对判成错"，比多说一句更伤。

// memLineFor 取输出里**包含某条正文的那一行**。
//
// 不能拿整个输出做 strings.Contains("·") 判方向：页眉图例和末尾提示里都带 "·"，
// 于是"全文含 ·"恒为真，方向反了也照样绿（同全局静音开关那次的教训）。
func memLineFor(out, sub string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, sub) {
			return strings.TrimSpace(ln)
		}
	}
	return ""
}

func TestManageMemoriesListSeesOnDemandEntries(t *testing.T) {
	defer withMemoryEnv(t)()
	if _, _, err := memUpsert("问候语以老板开头", "", false, "manual"); err != nil {
		t.Fatalf("准备数据失败：%v", err)
	}
	registerLessonTools()
	out, err := aiToolRegistry["manage_memories"].Execute(map[string]any{"op": "list"})
	if err != nil {
		t.Fatalf("list 报错：%v", err)
	}
	if strings.Contains(out, "没有记忆条目") || strings.Contains(out, "记忆库是空的") {
		t.Fatalf("库里有 1 条却报空：%q", out)
	}
	if !strings.Contains(out, "问候语以老板开头") {
		t.Fatalf("列表没带出正文：%q", out)
	}
	// 按需注入的条目必须标 ·，否则模型会以为它每轮都在生效
	ln := memLineFor(out, "问候语以老板开头")
	if !strings.HasPrefix(ln, "·") {
		t.Fatalf("按需注入的条目应以 · 开头：%q", ln)
	}
	// 同时把两条口径的差别钉住：**列表能看见 ≠ 本轮会注入**。
	// 只改 list 的取数、不去动 memoriesFor，这两个断言必须同时成立；
	// 哪天有人为了"让列表有内容"把 memoriesFor 也改成返回全部，这里会立刻红。
	if ms := memoriesFor("", 10); len(ms) != 0 {
		t.Fatalf("按需注入的条目在空任务下仍不该进提示词，实际 %d 条", len(ms))
	}
	// 而且要把"为什么没注入"说清楚，模型才能建议用户改成每轮
	if !strings.Contains(out, "仅相关时") && !strings.Contains(out, "任务为空") {
		t.Fatalf("应给出未注入原因：%q", out)
	}
}

// always=true 的条目在 list 里要标 ★（与界面"每轮都注入"同向，别标反）
func TestManageMemoriesListMarksAlways(t *testing.T) {
	defer withMemoryEnv(t)()
	if _, _, err := memUpsert("别动生产库", "", true, "manual"); err != nil {
		t.Fatalf("准备数据失败：%v", err)
	}
	registerLessonTools()
	out, err := aiToolRegistry["manage_memories"].Execute(map[string]any{"op": "list"})
	if err != nil {
		t.Fatalf("list 报错：%v", err)
	}
	// 断言"方向"而不是"是不是布尔"：★ 与 · 是互斥的两态，标错方向照样能通过
	// "含 ★" 这种弱断言（参考全局静音开关那次：亮着其实是开着的）。
	ln := memLineFor(out, "别动生产库")
	if !strings.HasPrefix(ln, "★") {
		t.Fatalf("always=true 的条目应以 ★ 开头：%q", ln)
	}
	if strings.Contains(ln, "仅任务相关时") {
		t.Fatalf("always=true 的条目不该被标成按需（方向反了）：%q", ln)
	}
}

// recall_lessons 的 schema 写着"task 留空返回全部"，空任务也必须能列出按需注入的条目
func TestRecallLessonsEmptyTaskListsAllMemories(t *testing.T) {
	defer withMemoryEnv(t)()
	if _, _, err := memUpsert("问候语以老板开头", "", false, "manual"); err != nil {
		t.Fatalf("准备数据失败：%v", err)
	}
	registerLessonTools()
	out, err := aiToolRegistry["recall_lessons"].Execute(map[string]any{})
	if err != nil {
		t.Fatalf("recall_lessons 报错：%v", err)
	}
	if !strings.Contains(out, "问候语以老板开头") {
		t.Fatalf("空任务应返回全部记忆，实际：%q", out)
	}
	if strings.Contains(out, "没有相关的经验或记忆") {
		t.Fatalf("库里有记忆却报空：%q", out)
	}
}
