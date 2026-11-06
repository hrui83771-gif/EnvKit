package main

// ai_done_reason_test.go —— 回合"为什么结束"必须说出口。
//
// 起因是 2026-10-30 的真实反馈：探索型长任务跑着跑着自己停了，
// 界面上没有任何说明，只能重发一条消息再问一次才拿到结果。
//
// 日志里的证据（envkit-20261030.log）：
//
//	21:07:44  上游 #5：prompt=21563 …
//	21:07:44  上游读取被本地中断（… use of closed network connection）——按正常收尾处理
//	21:08:06  上游 #1：…            ← 22 秒后用户重发，等于整轮探索白跑
//
// 断流、模型空回复、预算耗尽在界面上长得一模一样：都是"它自己停了"。
// 区别只在 done 事件的 reason —— 所以这个字段是契约，不是装饰。

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// readSSE 把 recorder 里的 data: 行解析成事件列表。
func readSSE(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(w.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("事件不是合法 JSON：%q", line)
		}
		out = append(out, ev)
	}
	return out
}

func lastDone(t *testing.T, evs []map[string]any) map[string]any {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i]["type"] == "done" {
			return evs[i]
		}
	}
	t.Fatal("整条流里没有 done 事件：前端会一直以为回合没结束")
	return nil
}

// 正常答完必须带 reason=completed，且不带 note（别拿"一切正常"去打扰用户）。
func TestDoneUsageCarriesCompletedReason(t *testing.T) {
	w := httptest.NewRecorder()
	sseDoneUsage(w, w, &aiUsage{})
	d := lastDone(t, readSSE(t, w))
	if d["reason"] != doneCompleted {
		t.Fatalf("正常收尾的 reason 应为 %q，实际 %v", doneCompleted, d["reason"])
	}
	if _, ok := d["note"]; ok {
		t.Fatalf("正常收尾不该带 note，否则每一轮都会多一行提示")
	}
}

// 断流：必须说明原因，并告诉用户"回一句继续就行"——
// 用户原来的做法是重开一段对话，等于把已经查到的上下文全丢了。
func TestAbortDoneExplainsAndOffersContinue(t *testing.T) {
	w := httptest.NewRecorder()
	note := "连接在出结果前断了。已查到的步骤在上面，回一句「继续」我就基于它们接着答，不用从头再来。"
	sseDoneWith(w, w, &aiUsage{}, doneAborted, note)
	d := lastDone(t, readSSE(t, w))
	if d["reason"] != doneAborted {
		t.Fatalf("断流的 reason 应为 %q，实际 %v", doneAborted, d["reason"])
	}
	got, _ := d["note"].(string)
	if !strings.Contains(got, "继续") {
		t.Fatalf("断流提示必须给出下一步（回一句继续），实际 %q", got)
	}
}

// 空回复：同样要有原因，否则界面什么都不多，用户只会觉得"点了没反应"。
func TestEmptyDoneExplains(t *testing.T) {
	w := httptest.NewRecorder()
	sseDoneWith(w, w, &aiUsage{}, doneEmpty, "这一轮模型没有返回内容。")
	d := lastDone(t, readSSE(t, w))
	if d["reason"] != doneEmpty {
		t.Fatalf("空回复的 reason 应为 %q，实际 %v", doneEmpty, d["reason"])
	}
	if d["note"] == "" || d["note"] == nil {
		t.Fatal("空回复必须带一句说明")
	}
}

// aiAbortNotice：有步骤才给进度摘要；没有步骤时不能发空的 plan_end
// （空节点会让界面多出一个"本次共 0 步"的折叠块）。
func TestAbortNoticeShowsProgress(t *testing.T) {
	empty := &planTrack{}
	w0 := httptest.NewRecorder()
	aiAbortNotice(w0, w0, empty)
	if strings.TrimSpace(w0.Body.String()) != "" {
		t.Fatalf("没有步骤时不该发任何事件，实际：%q", w0.Body.String())
	}

	p := &planTrack{}
	p.add("search_files", "envkit", "正在搜索 envkit")
	p.add("read_file", "ai_loop.go", "正在读取 ai_loop.go")
	w := httptest.NewRecorder()
	aiAbortNotice(w, w, p)
	evs := readSSE(t, w)
	if len(evs) != 1 || evs[0]["type"] != "plan_end" {
		t.Fatalf("期望恰好一条 plan_end，实际 %v", evs)
	}
	text, _ := evs[0]["text"].(string)
	// 结局词必须是"中途中断"而不是"已得出结论"——
	// 后者是把中断说成成功，正是本项目反复反对的那种谎报。
	if !strings.Contains(text, "中途中断") {
		t.Fatalf("进度摘要应标为「中途中断」，实际 %q", text)
	}
	if !strings.Contains(text, "2 步") {
		t.Fatalf("进度摘要要带上步数，实际 %q", text)
	}
}

// 断流后自动补结论：探索已经做完、只差总结时，替用户把那句话补上。
// 没有这一步，用户只能重开一段对话 —— 刚查到的工具结果全丢，模型再探索一遍。
func TestAbortSettleRescuesWhenStepsExist(t *testing.T) {
	oldFn := aiCallLLMFunc
	calls := 0
	aiCallLLMFunc = func(msgs []aiMsg, tools []aiUpstreamTool) (string, error) {
		calls++
		if len(tools) > 0 {
			t.Fatalf("补结论不该带工具定义（那是又要探索一轮）")
		}
		return "结论：3306 被旧进程占着，先停掉它再启动。", nil
	}
	defer func() { aiCallLLMFunc = oldFn }()

	p := &planTrack{}
	p.add("db_check", "127.0.0.1:3306", "正在检测数据库")
	p.add("get_logs", "backend", "正在读取 backend 日志")
	w := httptest.NewRecorder()
	rescued, note := aiAbortSettle(w, w, p, []aiMsg{{Role: "user", Content: "数据库为什么连不上"}}, true)
	if !rescued || calls != 1 {
		t.Fatalf("有步骤且本轮无正文时必须补一次结论，rescued=%v calls=%d", rescued, calls)
	}
	if !strings.Contains(note, "断线后") {
		t.Fatalf("补答成功的文案要说清结论是断线后补出来的，实际 %q", note)
	}
	evs := readSSE(t, w)
	// P0-2 起"告知在补"由 wait 事件承载（info 只能写死一句措辞）。
	// 判据随之升级：不仅要有这个事件，reason 必须对得上、上限必须是真实预算，
	// 否则前端的"最长 Ns"就是编的。
	var hasPlanEnd, hasWait, hasDelta bool
	for _, e := range evs {
		switch e["type"] {
		case "plan_end":
			hasPlanEnd = true
		case "wait":
			if r, _ := e["reason"].(string); r == "rescue" {
				if budget, _ := e["budget_ms"].(float64); budget <= 0 {
					t.Fatalf("补结论的等待态要带真实上限（budget_ms>0），实际 %v", e)
				}
				hasWait = true
			}
		case "delta":
			hasDelta = true
			if txt, _ := e["text"].(string); !strings.Contains(txt, "3306") {
				t.Fatalf("补出来的结论没推给用户：%q", txt)
			}
		}
	}
	if !hasPlanEnd || !hasWait || !hasDelta {
		t.Fatalf("补答要交代进度、用 wait:rescue 告知在补、并把结论推出去，实际事件 %v", evs)
	}
}

// 没有工具步骤时不补：没有事实可总结，多打一次模型只是白花钱。
func TestAbortSettleDoesNotRescueWithoutSteps(t *testing.T) {
	oldFn := aiCallLLMFunc
	calls := 0
	aiCallLLMFunc = func(msgs []aiMsg, tools []aiUpstreamTool) (string, error) {
		calls++
		return "不该被调用", nil
	}
	defer func() { aiCallLLMFunc = oldFn }()

	w := httptest.NewRecorder()
	rescued, note := aiAbortSettle(w, w, &planTrack{}, []aiMsg{{Role: "user", Content: "在吗"}}, true)
	if rescued || calls != 0 {
		t.Fatalf("没有工具结果时不该补结论，rescued=%v calls=%d", rescued, calls)
	}
	if !strings.Contains(note, "继续") {
		t.Fatalf("没补到时必须告诉用户回一句继续，实际 %q", note)
	}
}

// 主循环必须**真的调用** aiAbortSettle。
//
// 只测 helper 是不够的：当初的 bug 形态就是这个 helper 的内容散在主循环里、
// 且被"按正常收尾处理"一句跳过。有人把调用删掉换回静默收尾，
// 上面几条用例照样全绿，用户又会回到"它自己停了"。
func TestAbortSettleIsWiredIntoLoop(t *testing.T) {
	b, err := os.ReadFile("ai_loop.go")
	if err != nil {
		t.Fatalf("读不到 ai_loop.go：%v", err)
	}
	src := string(b)
	if !strings.Contains(src, "aiAbortSettle(w, fl, plan") {
		t.Fatal("主循环没调用 aiAbortSettle：断流又会变成静默收尾")
	}
	if strings.Contains(src, "——按正常收尾处理") {
		t.Fatal("静默收尾的旧文案又回来了：断流时必须说明原因")
	}
}

// 跨语言契约：前端用字面量判断 reason，Go 侧改常量却漏改前端 = 提示静默失效。
// 这个用例就是那道提醒——它红的时候说明两边对不上了。
func TestDoneReasonContractWithFrontend(t *testing.T) {
	src := readFrontendJS(t)
	for _, s := range []string{"'" + doneCompleted + "'", "'" + doneAwaitConfirm + "'"} {
		if !strings.Contains(src, s) {
			t.Fatalf("前端脚本里找不到 %s：done 的原因判断与 Go 常量脱钩了，"+
				"用户会重新回到「它自己停了，不知道为什么」", s)
		}
	}
}
