package main

// replan_test.go —— v2.3 N8 的单测
//
// 这批测试的判据是"该拦的拦住、不该拦的不拦"。
// 误拦的代价是模型被无意义地打断（它本来可以用不同参数重试）；
// 漏拦的代价是它在同一个动作上白烧三轮 token。

import (
	"strings"
	"testing"
)

func TestReplanTwoSameFailsIsStuck(t *testing.T) {
	r := newReplan()
	// 第一次失败：还不到门槛
	r.markFail("search_files", "启动")
	if r.shouldIntervene("search_files", "启动") {
		t.Fatal("第 1 次失败不该判定为卡住（可能是网络抖动或文件刚被创建）")
	}
	// 第二次同参数失败：判定卡住
	stuck, n := r.markFail("search_files", "启动")
	if !stuck || n != 2 {
		t.Fatalf("第 2 次同参数失败应判定卡住，实际 stuck=%v n=%d", stuck, n)
	}
	if !r.shouldIntervene("search_files", "启动") {
		t.Error("应触发换思路引导")
	}
}

// 关键：换参数不算重复
//
// search_files("a") 失败后换 search_files("b") 是**正确做法**，
// 不该被判成"还在重复"。这与 policyActionKey 的"参数不进键"同源。
func TestReplanDifferentParamIsNotStuck(t *testing.T) {
	r := newReplan()
	r.markFail("search_files", "启动")
	r.markFail("search_files", "配置")
	if r.shouldIntervene("search_files", "配置") {
		t.Error("换关键词重试是正确的探索方式，不该被判成卡住")
	}
	if r.shouldIntervene("search_files", "启动") {
		t.Error("第一个关键词确实重复了，该拦")
	}
}

// 只提示一次，反复拦截会打断正常流程
func TestReplanIntervenesOnlyOnce(t *testing.T) {
	r := newReplan()
	r.markFail("read_file", "a.go")
	r.markFail("read_file", "a.go") // 第二次才到门槛
	if !r.shouldIntervene("read_file", "a.go") {
		t.Fatal("应触发引导")
	}
	// 用生产代码的同一套 key 规则标记（手写 key 容易与实现漂移，
	// 那样的测试通过也不代表真的拦住了）
	r.stuckTools[replanKey("read_file", "a.go")] = true
	if r.shouldIntervene("read_file", "a.go") {
		t.Error("已引导过的不该重复引导（会打断模型的新思路）")
	}
}

// 写操作连续失败不该引导换工具——那是环境问题，换工具没用
func TestReplanSkipsWriteTools(t *testing.T) {
	r := newReplan()
	r.markFail("start_service", "web")
	r.markFail("start_service", "web")
	if r.shouldIntervene("start_service", "web") {
		t.Error("写操作连续失败是环境问题（端口被占/依赖缺失），换工具没用，不该引导")
	}
	// 但仍应记次数（供后续统计）
	if r.fails["start_service\x00web"] != 2 {
		t.Error("写操作的失败次数也应记录（供指标与诊断）")
	}
}

func TestReplanNilSafe(t *testing.T) {
	var r *replanState
	if stuck, n := r.markFail("x", "y"); stuck || n != 0 {
		t.Error("nil 状态不该崩，也不该报卡住")
	}
	if r.shouldIntervene("x", "y") {
		t.Error("nil 状态不该触发引导")
	}
}

// 指引必须给**具体替代动作**，不能空喊"再试别的"
func TestReplanHintIsConcrete(t *testing.T) {
	cases := map[string][]string{
		"search_files": {"list_project", "read_file"},
		"read_file":    {"list_project", "search_files"},
		"db_query":     {"db_list", "DESCRIBE"},
	}
	for tool, mustHave := range cases {
		h := replanHint(tool, "x")
		if h == "" {
			t.Fatalf("%s 应有换思路指引", tool)
		}
		for _, m := range mustHave {
			if !strings.Contains(h, m) {
				t.Errorf("%s 的指引应提到 %q（要给具体动作，不是空喊）：\n%s", tool, m, h)
			}
		}
		// 必须明说"不要重复相同参数"——它最容易犯这个
		if !strings.Contains(h, "不要") {
			t.Errorf("%s 的指引应明确禁止重复同一参数：\n%s", tool, h)
		}
	}
}

func TestReplanHintUnknownToolIsEmpty(t *testing.T) {
	if h := replanHint("some_unknown_tool", "x"); h != "" {
		t.Errorf("未知工具应返回空串（没准备指引就别硬给）：%q", h)
	}
}

// 给用户的提示要说清"它卡在哪、试了几次"
func TestReplanUserNotice(t *testing.T) {
	n := replanUserNotice("search_files", "启动", 2)
	for _, must := range []string{"search_files", "启动", "2"} {
		if !strings.Contains(n, must) {
			t.Errorf("用户提示应包含 %q（他有权知道它卡在哪）：%s", must, n)
		}
	}
	// 必须给出"你也可以帮我"的出路，否则用户只能干等
	if !strings.Contains(n, "告诉我") && !strings.Contains(n, "缺什么") {
		t.Errorf("用户提示要给出他可以做的事：%s", n)
	}
}

// 任务之间不能互相影响
func TestReplanIsPerTask(t *testing.T) {
	r1 := newReplan()
	r1.markFail("search_files", "a")
	r1.markFail("search_files", "a")
	if !r1.shouldIntervene("search_files", "a") {
		t.Fatal("任务1 应判定卡住")
	}
	// 新任务：全新状态
	r2 := newReplan()
	if r2.shouldIntervene("search_files", "a") {
		t.Error("新任务不该受上一个任务的失败影响")
	}
}
