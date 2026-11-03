package main

// ai_replay_test.go —— 录像/回放（ai_replay.go）的回归用例
//
// 三条，各守一件事：
//
//  1. **回放能驱动主循环走完整个状态机，并产出可断言的轨迹** ——
//     这是"AI 主循环有客观回归保护"的唯一落点。fixture 里刻意放了几种
//     手写帧时代从未覆盖过的上游形态：arguments 分片、reasoning_content、
//     usage 挂在 choices 为空的那一帧、正文分块（走首句缓冲）。
//  2. **录像用尽必须响亮失败** —— 静默返回 200 空体会让主循环把
//     "没有录像"当成"模型没说话"，最后红在一个完全无关的断言上。
//  3. **录像器逐字节保真** —— 录下来的 body 必须与上游给的完全一致，
//     否则回放验证的就不是真实解析路径了。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// replayFixture 指向 data 目录里的录像样本。
func replayFixture(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("testdata", "ai-rec", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("缺少录像样本 %s：%v", p, err)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("解析绝对路径失败：%v", err)
	}
	return abs
}

// stubReplayTool 注册一个只读桩工具，并返回它收到的参数（供断言分片拼接结果）。
//
// 用只读工具（Write=false）而不是写工具：写工具会走确认卡，测试就得模拟点击，
// 而这条用例要验的是"字节 → 解析 → 执行 → 轨迹"这条链，不该被确认交互干扰。
func stubReplayTool(t *testing.T) *[]map[string]any {
	t.Helper()
	const name = "zz_replay_probe"
	var mu sync.Mutex
	got := &[]map[string]any{}
	aiToolRegistry[name] = aiTool{
		Desc:   "测试用只读探针：记录收到的参数",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Write:  false,
		Execute: func(a map[string]any) (string, error) {
			mu.Lock()
			*got = append(*got, a)
			mu.Unlock()
			return "探针结果：ok", nil
		},
	}
	t.Cleanup(func() { delete(aiToolRegistry, name) })
	return got
}

// withTempTraceDir 把轨迹写到临时目录，返回读取已落盘轨迹的入口。
func withTempTraceDir(t *testing.T) func() []Trace {
	t.Helper()
	// 目录**只能取一次**：写成 traceDir = func() string { return t.TempDir() }
	// 会让每次调用都换一个新目录，于是"写进去的"和"读出来的"不是同一个地方，
	// 表现为"轨迹 0 条"（第一次写的版本正是这个错）。
	dir := t.TempDir()
	oldDir := traceDir
	traceDir = func() string { return dir }
	t.Cleanup(func() { traceDir = oldDir })
	return func() []Trace { return traceQuery(10, "") }
}

func TestReplayDrivesLoopAndTrace(t *testing.T) {
	fixture := replayFixture(t, "two-turn-tool-call.jsonl")
	gotArgs := stubReplayTool(t)
	traces := withTempTraceDir(t)

	// BaseURL 故意指向一个必然连不上的地址：**只要真的走了网络，这条用例就会失败**。
	// 这是"回放确实没碰网络"最直接的证据，比断言某个日志字符串可靠。
	oldAI := cfg.AI
	cfg.AI = &AIConfig{Enabled: true, Provider: "test", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "replay-demo"}
	oldRO := aiReadOnly.Load()
	aiReadOnly.Store(false)
	t.Cleanup(func() {
		cfg.AI = oldAI
		aiReadOnly.Store(oldRO)
	})

	t.Setenv("ENVKIT_AI_REPLAY", fixture)
	t.Setenv("ENVKIT_AI_RECORD", "")
	aiReplayReset()

	rec := httptest.NewRecorder()
	beginTrace("probe the replay path", actAI)
	done := make(chan struct{})
	go func() {
		defer close(done)
		aiRunLoop(context.Background(), rec, rec,
			[]aiMsg{{Role: "user", Content: "probe the replay path"}}, false, true, "zh")
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("回放驱动的主循环没有结束")
	}
	endTrace(outAborted) // 兜底值是 aborted，真实结局由 traceOutcomeBox 覆盖

	// ① 工具收到了**拼完整**的参数：fixture 里 arguments 是 `{"key"` + `:"v1"}` 两片
	muArgs := *gotArgs
	if len(muArgs) != 1 {
		t.Fatalf("应当只执行一次探针工具，实际 %d 次", len(muArgs))
	}
	if v, _ := muArgs[0]["key"].(string); v != "v1" {
		t.Fatalf("arguments 分片没有拼接正确：期望 key=v1，实际 %#v", muArgs[0])
	}

	// ② 流里应该有模型正文，且**不含** reasoning 片段被当成正文
	out := rec.Body.String()
	if !strings.Contains(out, "环境就绪") {
		t.Fatalf("最终回答没有推到前端，流尾部：%s", tailOf(out, 300))
	}
	if strings.Contains(out, `"type":"error"`) {
		t.Fatalf("回放驱动下不应出现错误事件，流内容：%s", tailOf(out, 600))
	}

	// ③ 轨迹落盘且形态正确
	list := traces()
	if len(list) != 1 {
		t.Fatalf("应当落盘 1 条轨迹，实际 %d 条", len(list))
	}
	tr := list[0]
	if tr.Outcome != outSuccess {
		t.Fatalf("正常答完的回合结局应为 %s，实际 %q", outSuccess, tr.Outcome)
	}
	var actions, verifies int
	for _, s := range tr.Steps {
		if s.Phase == phAction && s.Action == "zz_replay_probe" {
			actions++
			if s.Result != "ok" {
				t.Fatalf("工具步结果应为 ok，实际 %q", s.Result)
			}
		}
		if s.Phase == phVerify {
			verifies++
		}
	}
	if actions != 1 {
		t.Fatalf("轨迹里应有 1 个 zz_replay_probe 动作步，实际 %d（步序列：%s）", actions, stepDump(tr))
	}
	if verifies != 0 {
		t.Fatalf("这次没有复验工具，不应出现 verify 步，实际 %d", verifies)
	}

	// ④ 用量按两次往返累加（900+1200 / 30+40），缓存细分也要带上
	if tr.Usage == nil {
		t.Fatal("轨迹应记录 token 用量")
	}
	if tr.Usage.Prompt != 2100 || tr.Usage.Completion != 70 {
		t.Fatalf("用量累加不对：期望 prompt=2100 completion=70，实际 %d/%d",
			tr.Usage.Prompt, tr.Usage.Completion)
	}
	if !tr.Usage.HasCache || tr.Usage.Hit != 1700 || tr.Usage.Miss != 400 {
		t.Fatalf("缓存细分不对：期望 hit=1700 miss=400，实际 hit=%d miss=%d has=%v",
			tr.Usage.Hit, tr.Usage.Miss, tr.Usage.HasCache)
	}
	// Turns 记的是"本回合收尾写用量的次数"，**不是上游往返次数** ——
	// recordTurnUsage 只在 sseDoneUsage/sseDoneConfirm 里调用一次，传的是整回合累计值。
	// 所以两次上游往返、一次收尾，这里就是 1。（trace.go 原先的注释写的是
	// "上游往返次数"，与实际不符，已一并改正。）
	if tr.Usage.Turns != 1 {
		t.Fatalf("收尾写用量的次数应为 1，实际 %d", tr.Usage.Turns)
	}
}

func stepDump(tr Trace) string {
	var b []string
	for _, s := range tr.Steps {
		b = append(b, s.Phase+"/"+s.Action)
	}
	return strings.Join(b, " → ")
}

func TestReplayExhaustedFailsLoudly(t *testing.T) {
	// 只给 1 条录像，但让循环需要 2 次往返（先调工具、再答话）：
	// 第 2 次请求没有对应录像时，必须报出一条**能指路的**错误。
	dir := t.TempDir()
	one := filepath.Join(dir, "only-one.jsonl")
	src, err := os.ReadFile(replayFixture(t, "two-turn-tool-call.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(string(src), "\n", 2)[0]
	if err := os.WriteFile(one, []byte(first+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stubReplayTool(t)
	withTempTraceDir(t)

	oldAI := cfg.AI
	cfg.AI = &AIConfig{Enabled: true, Provider: "test", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "replay-demo"}
	oldRO := aiReadOnly.Load()
	aiReadOnly.Store(false)
	t.Cleanup(func() {
		cfg.AI = oldAI
		aiReadOnly.Store(oldRO)
	})

	t.Setenv("ENVKIT_AI_REPLAY", one)
	t.Setenv("ENVKIT_AI_RECORD", "")
	aiReplayReset()

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		aiRunLoop(context.Background(), rec, rec,
			[]aiMsg{{Role: "user", Content: "probe the replay path"}}, false, true, "zh")
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("录像用尽后主循环没有结束（可能被当成空响应继续跑）")
	}

	out := rec.Body.String()
	if !strings.Contains(out, "回放已用完") {
		t.Fatalf("录像用尽必须报出可定位的错误，实际流尾部：%s", tailOf(out, 400))
	}
	if !strings.Contains(out, "没有对应录像") {
		t.Fatalf("错误文案要说清是'哪一次请求没有录像'，实际：%s", tailOf(out, 400))
	}
}

func TestRecordWrapIsByteFaithful(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\ndata: [DONE]\n\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.jsonl")
	t.Setenv("ENVKIT_AI_RECORD", path)
	t.Setenv("ENVKIT_AI_REPLAY", "")
	aiReplayReset()

	reqBody := `{"model":"m1","stream":true,"messages":[{"role":"user"}],"tools":[{"x":1},{"y":2}]}`
	req, err := http.NewRequest("POST", "http://example.invalid/v1/chat/completions", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}
	wrapped := aiRecordWrap(resp, req)

	// 分小块读：录像器必须**边读边抄**，不能要求调用方一次读完
	var got strings.Builder
	buf := make([]byte, 7)
	for {
		n, err := wrapped.Body.Read(buf)
		got.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("读包装体失败：%v", err)
		}
	}
	_ = wrapped.Body.Close()

	if got.String() != body {
		t.Fatalf("包装体改变了字节流：\n期望 %q\n实际 %q", body, got.String())
	}

	lines, err := aiReplayLoad(path)
	if err != nil {
		t.Fatalf("录像文件读不回来：%v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("应当录 1 条，实际 %d 条", len(lines))
	}
	e := lines[0]
	if e.Body != body {
		t.Fatal("录下来的响应体与上游给的字节不一致")
	}
	if !e.Stream || e.Model != "m1" || e.NMsgs != 1 || e.NTools != 2 {
		t.Fatalf("请求特征没记全：%+v", e)
	}
	if e.Trunc {
		t.Fatal("这个体量不该被判为截断")
	}

	// 同一份文件回放出来必须逐字节一致
	t.Setenv("ENVKIT_AI_REPLAY", path)
	aiReplayReset()
	r, ok := aiReplayNext(req)
	if !ok {
		t.Fatal("设了 ENVKIT_AI_REPLAY 就应当进入回放分支")
	}
	back, _ := io.ReadAll(r.Body)
	if string(back) != body {
		t.Fatalf("回放出来的字节与录制不一致：\n期望 %q\n实际 %q", body, string(back))
	}
}
