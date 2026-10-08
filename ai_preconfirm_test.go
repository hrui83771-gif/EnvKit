package main

// ai_preconfirm_test.go —— 断言「确认卡把话说完」这个机制不会被悄悄改掉
//
// ## 为什么这些断言值得写
//
// 这套机制是**一次真实评测的产物**：hang_service 三次里两次
// 模型诊断完全正确，却被产品掐断在半句（`我先发起重启`）。
//
// 而它极其容易被"简化"掉——
// 补答失败时静默返回 false、模型没输出时也算false，
// 这些都会让机制悄悄退化成"什么都不做"，
// 而**报告上看不出任何差别**（cut_by_confirm 归零而已）。
//
// 所以断言要盯住**行为**，不是盯住代码形状。

import (
	"strings"
	"testing"
)

// ---- 1. 补答提示必须要求「不带工具、不再发起操作」----
//
// 这两条是整个机制的安全边界。少了任何一条：
//   · 少了「不要调用工具」→ 模型可能再发起一次写操作 → **无限循环**
//   · 少了「不要问用户」→ 模型反问，用户看到的是问句而不是结论
func TestPreconfirmPromptForbidsToolCalls(t *testing.T) {
	if !strings.Contains(aiPreconfirmPrompt("重启后端"), "不要再调用任何工具") {
		t.Fatal("补答提示必须明确禁止模型再调用工具 —— 否则会无限循环")
	}
	if !strings.Contains(aiPreconfirmPrompt("重启后端"), "状态") ||
		!strings.Contains(aiPreconfirmPrompt("重启后端"), "风险") {
		t.Fatal("补答提示必须要求说明状态/原因/预期/风险 —— 否则用户仍然不知道为什么")
	}
}

// ---- 2. 补答提示里必须带上工具名（否则用户看到的是没有主语的结论）----
func TestPreconfirmPromptNamesTool(t *testing.T) {
	p := aiPreconfirmPrompt("重启后端服务")
	if !strings.Contains(p, "重启后端服务") {
		t.Fatalf("补答提示必须带上操作名，实际=%q", p)
	}
}

// ---- 3. 复验三态的徽章文案必须互不相同 ----
//
// **合并任何两个都等于把这个功能白做**：
// 「已执行但没复验」和「复验通过」在界面上长得一样，
// 那用户看到的仍是「执行即成功」，而这正是本功能要消除的东西。
func TestVerifyBadgeStatesAreDistinct(t *testing.T) {
	three := map[string]string{
		"ok":      aiVerifyBadgeText(true, true),
		"pending": aiVerifyBadgeText(true, false),
		"bad":     aiVerifyBadgeText(false, false),
	}
	if three["ok"] == three["pending"] {
		t.Fatalf("通过与未确认的文案相同（%q）—— 用户看不出区别，功能白做",
			three["ok"])
	}
	if three["pending"] == three["bad"] {
		t.Fatalf("未确认与未通过的文案相同（%q）—— 用户看不出区别",
			three["pending"])
	}
	if three["ok"] == three["bad"] {
		t.Fatalf("通过与未通过的文案相同（%q）", three["ok"])
	}
	for k, v := range three {
		if strings.TrimSpace(v) == "" {
			t.Fatalf("状态 %s 的徽章文案为空", k)
		}
	}
}

// ---- 4. 三态的 CSS 类必须不同（不然只有文案差、视觉上一样）----
func TestVerifyBadgeClassesAreDistinct(t *testing.T) {
	three := map[string]string{
		"ok":      aiVerifyBadgeClass(true, true),
		"pending": aiVerifyBadgeClass(true, false),
		"bad":     aiVerifyBadgeClass(false, false),
	}
	seen := map[string]string{}
	for k, v := range three {
		if prev, dup := seen[v]; dup {
			t.Fatalf("状态 %s 与 %s 用了同一个 CSS 类 %q —— 视觉上区分不开",
				k, prev, v)
		}
		seen[v] = k
	}
	// 类名必须与 HTML 里的样式对得上，否则徽章是「无样式」的
	for _, want := range []string{"is-ok", "is-pending", "is-bad"} {
		found := false
		for _, v := range three {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("缺少 CSS 类 %q（HTML 里必须已有对应样式）", want)
		}
	}
}

// ---- 5. aiTakeVerify 必须「取走即清空」----
//
// 不清空的话：上一次的复验结果会被下一次无关的工具调用当成新的，
// 于是界面上冒出一个**根本没跑过**的徽章 ——
// 而徽章的全部价值就是「它说验了就是真验了」。
func TestTakeVerifyClearsAfterRead(t *testing.T) {
	lastVerify = OpResult{Ok: true, Verified: true, Msg: "服务就绪"}
	if _, ok := aiTakeVerify(); !ok {
		t.Fatal("刚写入的复验结果应可取走")
	}
	if _, ok := aiTakeVerify(); ok {
		t.Fatal("取走之后必须清空 —— 否则会重复发同一个徽章")
	}
}

// ---- 6. 空结果不能被当成「有复验」----
func TestTakeVerifyEmptyIsNotOk(t *testing.T) {
	lastVerify = OpResult{}
	if _, ok := aiTakeVerify(); ok {
		t.Fatal("空结果不能算「有复验结果」—— 否则每次工具调用都会冒出一个徽章")
	}
}

// ---- 7. 徽章只认客观字段，不认模型说了什么 ----
//
// 这是整个功能的立意：AI 说「已恢复正常」不能改徽章颜色。
// 所以判定函数**只能**吃 ok/verified 两个布尔，
// 一旦有人给它加第三个参数（比如让模型的话参与判定），这个测试就该红。
func TestVerifyBadgeIgnoresProse(t *testing.T) {
	// 模型声称"已恢复"但 verified=false → 必须是 pending
	if got := aiVerifyBadgeClass(true, false); got != "is-pending" {
		t.Fatalf("verified=false 时应为 is-pending，实际 %q", got)
	}
	// 模型说"没恢复"但 verified=true → 仍然是 ok（客观字段优先）
	if got := aiVerifyBadgeClass(true, true); got != "is-ok" {
		t.Fatalf("verified=true 时应为 is-ok，实际 %q", got)
	}
}