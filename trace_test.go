package main

// trace_test.go —— v2.2 M9 任务轨迹单测
//
// 重点不是"能不能写文件"，而是**指标能不能真的算出来**：
// 基线报告里"无效操作次数"与"验证准确性"两项写的是"不可测，卡在 Trace"，
// 这组测试就是来确认它们现在可测的。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTraceEnv 把轨迹文件指向临时目录，并确保没有任务在追踪中。
func withTraceEnv(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	oldDir, old := traceDir, tracePath
	traceDir = func() string { return dir }
	tracePath = func() string { return filepath.Join(dir, "trace-test.jsonl") }
	traceMu.Lock()
	traceCur = nil
	// v2.7：结局记录器也必须重置，否则上一个测试的判定会漏进下一个 ——
	// 症状是「单跑通过、全跑时随机失败」，极难定位。
	traceOutcomeCur = nil
	traceMu.Unlock()
	traceIDHolder.Store("")
	return func() {
		traceDir, tracePath = oldDir, old
		traceMu.Lock()
		traceCur = nil
		traceOutcomeCur = nil
		traceMu.Unlock()
		traceIDHolder.Store("")
	}
}

// closeAuditForTest 关闭审计文件句柄。
// **凡是替换了 auditDir 的测试都必须调用它**：t.TempDir() 在测试结束时会删目录，
// 而 Windows 不允许删除仍被打开的文件，句柄没关就会报
// "The process cannot access the file because it is being used by another process"。
// 生产代码不需要这个——进程退出时 OS 会回收句柄。
func closeAuditForTest() {
	auditMu.Lock()
	if auditFile != nil {
		_ = auditFile.Close()
		auditFile = nil
	}
	auditFileAt = ""
	auditMu.Unlock()
}

// withAuditEnv 把审计目录指向临时目录，返回恢复函数（含句柄清理）。
func withAuditEnv(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	old := auditDir
	auditDir = func() string { return dir }
	auditMu.Lock()
	auditFile, auditFileAt = nil, ""
	auditMu.Unlock()
	return func() {
		closeAuditForTest()
		auditDir = old
	}
}

func readTraces(t *testing.T) []Trace {
	t.Helper()
	b, err := os.ReadFile(tracePath())
	if err != nil {
		return nil
	}
	var out []Trace
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var tr Trace
		if json.Unmarshal([]byte(ln), &tr) == nil {
			out = append(out, tr)
		}
	}
	return out
}

func TestTraceLifecycle(t *testing.T) {
	defer withTraceEnv(t)()

	if currentTraceID() != "" {
		t.Fatal("起始状态不该有 trace")
	}
	id := beginTrace("帮我把项目跑起来", actAI)
	if id == "" {
		t.Fatal("beginTrace 必须返回 id")
	}
	if currentTraceID() != id {
		t.Fatalf("当前 trace 应为 %q，实际 %q", id, currentTraceID())
	}
	traceStep(phAction, actAI, "start_service", "web", "ok", 120, "", "")
	traceStep(phVerify, actAI, "verify_environment", "web", "ok", 0, "", "已复验通过：端口 8080 在监听")
	endTrace("success")

	ts := readTraces(t)
	if len(ts) != 1 {
		t.Fatalf("应落盘 1 条轨迹，实际 %d", len(ts))
	}
	tr := ts[0]
	if tr.Outcome != "success" {
		t.Fatalf("outcome 应为 success，实际 %q", tr.Outcome)
	}
	if !tr.Verified {
		t.Error("有 verify 步就该标记 Verified——这是「验证准确性」指标的前提")
	}
	if len(tr.Steps) != 2 {
		t.Fatalf("应有 2 步，实际 %d", len(tr.Steps))
	}
	if tr.Steps[0].Seq != 1 || tr.Steps[1].Seq != 2 {
		t.Error("步序编号必须连续")
	}
	if tr.Ended == "" {
		t.Error("收尾必须写 Ended，否则算不出耗时")
	}
	if currentTraceID() != "" {
		t.Error("收尾后应清空当前 trace，否则后续操作会误挂到这条轨迹上")
	}
}

// 核心价值：审计记录能自动挂上 trace_id，**现有调用点零改动**
func TestTraceIDInAudit(t *testing.T) {
	defer withAuditEnv(t)()
	defer withTraceEnv(t)()

	// 无任务时不该有 trace_id
	auditNow(actUser, "config_save", "x", "", resOK, "")
	// 有任务时应自动带上
	beginTrace("跑起来", actAI)
	auditNow(actAI, "start_service", "web", "script=dev", resOK, "")
	endTrace("success")

	b, _ := os.ReadFile(auditPath())
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		t.Fatalf("应有两行审计，实际 %d", len(lines))
	}
	if strings.Contains(lines[0], "trace_id") {
		t.Error("无任务时的审计不该带 trace_id")
	}
	if !strings.Contains(lines[1], `"trace_id"`) {
		t.Fatalf("任务内的审计必须自动带 trace_id，实际：%s", lines[1])
	}
	// auditStart 路径（真实写操作走的是它）也要带
	beginTrace("再跑一次", actAI)
	auditStart(actAI, "db_backup", "farm", "")("ok", "")
	endTrace("success")
	b2, _ := os.ReadFile(auditPath())
	if !strings.Contains(string(b2), `"trace_id"`) {
		t.Error("auditStart 路径的审计也必须带 trace_id")
	}
	// 两条任务的 trace_id 应当不同
	ids := map[string]bool{}
	for _, ln := range strings.Split(strings.TrimSpace(string(b2)), "\n") {
		i := strings.Index(ln, `"trace_id":"`)
		if i < 0 {
			continue
		}
		rest := ln[i+len(`"trace_id":"`):]
		if j := strings.Index(rest, `"`); j > 0 {
			ids[rest[:j]] = true
		}
	}
	if len(ids) < 2 {
		t.Errorf("两次任务应产生两个不同的 trace_id，实际 %d 个：%v", len(ids), ids)
	}
}

// "无效操作次数"指标的数据源：连续失败与被拒后重试要能从轨迹看出来
func TestTraceDetectInvalidOps(t *testing.T) {
	defer withTraceEnv(t)()

	beginTrace("起服务", actAI)
	// 同 action 连续失败两次 —— 这正是基线报告里定义的"无效操作"
	traceToolStep("start_service", map[string]any{"service": "web"}, "", errFake("端口占用"), 100)
	traceToolStep("start_service", map[string]any{"service": "web"}, "", errFake("端口占用"), 100)
	traceToolStep("start_service", map[string]any{"service": "web"}, "已复验通过：端口 8080 在监听", nil, 3000)
	endTrace("success")

	ts := readTraces(t)
	if len(ts) != 1 {
		t.Fatalf("应落盘 1 条，实际 %d", len(ts))
	}
	steps := ts[0].Steps

	// 期望结构：action/fail/action/fail/action/verify
	acts, fails, verif := 0, 0, 0
	for _, s := range steps {
		switch s.Phase {
		case phAction:
			acts++
		case phFailure:
			fails++
		case phVerify:
			verif++
		}
	}
	if acts != 3 {
		t.Errorf("应记录 3 次 action 调用，实际 %d（这是算重复尝试的基础）", acts)
	}
	if fails != 2 {
		t.Errorf("应记录 2 次失败，实际 %d", fails)
	}
	if verif != 1 {
		t.Errorf("应记录 1 次复验，实际 %d", verif)
	}
	if !ts[0].Verified {
		t.Error("有复验就应标记 Verified")
	}
	// 连续同 action 失败是"在瞎试"的核心信号，必须能从轨迹里数出来
	retry := 0
	for i := 1; i < len(steps); i++ {
		if steps[i].Action == steps[i-1].Action && steps[i].Result == "fail" {
			retry++
		}
	}
	if retry < 1 {
		t.Error("连续同 action 失败必须可识别（无效操作指标依赖它）")
	}
}

// "验证准确性"的数据源：从工具返回里抽出复验结论
func TestTraceExtractVerify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"已复验通过：端口 8080 在监听（进程 main.exe）", "已复验通过：端口 8080 在监听（进程 main.exe）"},
		{"复验未通过[spawn_fail]：进程已退出", "复验未通过[spawn_fail]：进程已退出"},
		{"未复验：动作已执行但环境尚未被证明恢复", "未复验：动作已执行但环境尚未被证明恢复"},
		{"服务已启动（go build 通过）", ""},
	}
	for _, c := range cases {
		if got := traceExtractVerify(c.in); got != c.want {
			t.Errorf("traceExtractVerify(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }

// 死循环的 AI 不能把轨迹写到几十 MB
func TestTraceStepCap(t *testing.T) {
	defer withTraceEnv(t)()

	beginTrace("死循环", actAI)
	for i := 0; i < traceMaxSteps+50; i++ {
		traceStep(phAction, actAI, "get_logs", "", "ok", 1, "", "")
	}
	endTrace("aborted")

	ts := readTraces(t)
	if len(ts) != 1 {
		t.Fatalf("应落盘 1 条，实际 %d", len(ts))
	}
	steps := ts[0].Steps
	if len(steps) != traceMaxSteps+1 {
		t.Fatalf("步数应被截断在 %d（含 1 个占位），实际 %d", traceMaxSteps+1, len(steps))
	}
	if steps[len(steps)-1].Phase != "truncated" {
		t.Error("截断必须留占位标记，否则看轨迹的人以为任务就这么结束了")
	}
}

// 步数超限的占位标记不能被当成正常步骤
func TestTraceTruncationMarked(t *testing.T) {
	defer withTraceEnv(t)()
	beginTrace("x", actAI)
	for i := 0; i < traceMaxSteps+10; i++ {
		traceStep(phAction, actAI, "x", "", "ok", 0, "", "")
	}
	tr := currentTraceSnapshot()
	if tr == nil {
		t.Fatal("应有当前轨迹快照")
	}
	real := 0
	for _, s := range tr.Steps {
		if s.Phase != "truncated" {
			real++
		}
	}
	if real != traceMaxSteps {
		t.Fatalf("真实步骤应恰好 %d，实际 %d", traceMaxSteps, real)
	}
}

// 无任务时记步必须静默丢弃：守护 goroutine 与用户操作并行，不是每条都属于某个任务
func TestTraceStepWithoutTask(t *testing.T) {
	defer withTraceEnv(t)()
	traceStep(phAction, actGuard, "chain_autorecover", "h", "ok", 0, "", "")
	traceToolStep("db_check", nil, "ok", nil, 5)
	if currentTraceSnapshot() != nil {
		t.Fatal("无任务时不应产生轨迹")
	}
	// 也不该写出文件
	if _, err := os.Stat(tracePath()); err == nil {
		t.Error("无任务时不该落盘轨迹文件")
	}
}

func TestTraceQuery(t *testing.T) {
	defer withTraceEnv(t)()

	beginTrace("任务A", actAI)
	traceStep(phAction, actAI, "x", "", "ok", 0, "", "")
	endTrace("success")

	beginTrace("任务B", actAI)
	traceStep(phFailure, actAI, "y", "", "fail", 0, "e", "")
	endTrace("failed")

	beginTrace("任务C", actAI)
	endTrace("aborted")

	if got := traceQuery(10, ""); len(got) != 3 {
		t.Fatalf("应查到 3 条轨迹，实际 %d", len(got))
	}
	if got := traceQuery(10, "failed"); len(got) != 1 || got[0].Goal != "任务B" {
		t.Fatalf("按 outcome 过滤失效：%+v", got)
	}
	if got := traceQuery(2, ""); len(got) != 2 {
		t.Fatalf("n 参数失效，应返回 2 条，实际 %d", len(got))
	}
}

// TraceID 格式要可读且能排序
func TestTraceIDFormat(t *testing.T) {
	defer withTraceEnv(t)()
	id := beginTrace("x", actAI)
	endTrace("success")
	if !strings.HasPrefix(id, "t-") {
		t.Errorf("trace id 应以 t- 开头，实际 %q", id)
	}
	// 形如 t-20261003-214258-0D2C
	parts := strings.Split(id, "-")
	if len(parts) != 4 {
		t.Fatalf("trace id 应为 t-日期-时间-随机码 四段，实际 %q（%d 段）", id, len(parts))
	}
	if len(parts[1]) != 8 || len(parts[2]) != 6 {
		t.Errorf("日期应为 8 位、时间应为 6 位，实际 %q", id)
	}
	if len(parts[3]) != 6 {
		t.Errorf("随机码应为 6 位（时间 4 + 序号 2），实际 %q", id)
	}
	// 连开多条不应撞号
	seen := map[string]bool{id: true}
	for i := 0; i < 200; i++ {
		a := beginTrace("x", actAI)
		endTrace("success")
		if seen[a] {
			t.Fatalf("trace id 撞号：%s", a)
		}
		seen[a] = true
	}
}

// 目标描述要取真实用户诉求，不能把工具结果当成任务
func TestTraceGoalIsUserText(t *testing.T) {
	cases := []struct {
		name string
		msgs []aiMsg
		want string
	}{
		{"普通提问", []aiMsg{{Role: "user", Content: "帮我把服务起起来"}}, "帮我把服务起起来"},
		{"跳过工具结果", []aiMsg{
			{Role: "user", Content: "起服务"},
			{Role: "assistant", Content: ""},
			{Role: "user", Content: "[工具 get_logs 已执行，结果如下]\n日志"},
		}, "起服务"},
		{"跳过系统注入", []aiMsg{
			{Role: "user", Content: "启动"},
			{Role: "user", Content: "（系统注入的当前环境快照）"},
		}, "启动"},
		{"无消息", []aiMsg{}, ""},
	}
	for _, c := range cases {
		if got := aiLastUserText(c.msgs); got != c.want {
			t.Errorf("%s：期望 %q，实际 %q", c.name, c.want, got)
		}
	}
}

// 轨迹里的证据要截断，不能把整份日志塞进去
func TestTraceEvidenceTruncated(t *testing.T) {
	defer withTraceEnv(t)()
	long := strings.Repeat("A", 5000)
	beginTrace("x", actAI)
	traceStep(phEvidence, actAI, "t", "", "ok", 0, "", long)
	endTrace("success")
	ts := readTraces(t)
	if len(ts) != 1 {
		t.Fatal("应有轨迹")
	}
	ev := ts[0].Steps[0].Evidence
	// firstLines 按字节截断并追加 "..."（三个半角点）
	if len(ev) > 305 {
		t.Fatalf("证据应截断到 300 字节量级，实际 %d", len(ev))
	}
	if !strings.HasSuffix(ev, "...") {
		t.Errorf("截断后应带省略标记，让人一眼看出内容被裁过，实际尾部 %q", ev[max(0, len(ev)-6):])
	}
}
