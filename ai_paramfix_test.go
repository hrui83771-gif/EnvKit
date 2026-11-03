package main

// ai_paramfix_test.go —— 参数自适应的名额必须按类别分账
//
// ## 这个文件锁的是什么缺陷
//
// 老实现是一个共享计数器（`paramFix < 2`），而 temperature 一个槽位自己就要走两跳：
//
//	""  --(上游拒 temperature)-->  "one"  --(还是拒)-->  "none"
//
// 两跳正好把名额吃满。于是「拒绝 temperature」且「要求 max_completion_tokens」
// 的推理型模型（这是很常见的组合）第三跳被拦下 —— 上游 400 原样甩给用户，
// quirk 永远学不进去，表现为"这个模型怎么配都用不了"。
//
// 而老代码那行的注释写的是「各最多一次」：注释与实现从一开始就不一致，
// 且循环里的这个上限**从来没有测试**（intent_test.go 只测了纯函数 aiFixParamError 的三个分支）。
//
// ## 为什么测纯函数就够
//
// 要真跑那个场景得模拟上游连续返回 400，测试会变成"测 http 桩"而不是测判据。
// 名额判定与类别识别都是纯函数，把三跳的真实错误文案串起来跑一遍，
// 就已经覆盖了"共享计数器会死在哪一步"这个判据本身。

import "testing"

// TestQuirkFixKindIdentifiesSlot 类别识别：靠快照差异，不靠错误文案。
func TestQuirkFixKindIdentifiesSlot(t *testing.T) {
	base := AIQuirk{}
	cases := []struct {
		name  string
		after AIQuirk
		want  string
	}{
		{"temperature 被改", AIQuirk{TempMode: "one"}, "temp"},
		{"token 字段被改", AIQuirk{MaxTokensField: "max_completion_tokens"}, "tokens"},
		{"工具被关", AIQuirk{NoTools: true}, "tools"},
		{"没改动", AIQuirk{}, ""},
	}
	for _, c := range cases {
		if got := aiQuirkFixKind(base, c.after); got != c.want {
			t.Errorf("%s：aiQuirkFixKind = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestParamFixerBudgetIsPerKind **核心回归**：一个类别用满不得连带拒绝别的类别。
//
// 变异验证：把 allow 改回"共享一个计数器"（不看 kind），
// 下面第二条断言立刻红 —— 那正是老实现的行为。
func TestParamFixerBudgetIsPerKind(t *testing.T) {
	var f aiParamFixer
	for i := 0; i < aiParamFixMaxPerKind; i++ {
		if !f.allow("temp") {
			t.Fatalf("temperature 第 %d 跳就该放行（它自身要两跳）", i+1)
		}
		f.take("temp")
	}
	if f.allow("temp") {
		t.Errorf("temperature 已用满 %d 次仍放行：名额超发会导致无限重试", aiParamFixMaxPerKind)
	}
	if !f.allow("tokens") {
		t.Error("temperature 用满后 tokens 被连带拒绝 —— 这正是共享计数器（paramFix < 2）的缺陷：" +
			"「拒绝 temperature + 要求 max_completion_tokens」的模型会永远配不通")
	}
	if !f.allow("tools") {
		t.Error("temperature 用满后 tools 被连带拒绝（同上）")
	}
	if f.allow("") {
		t.Error("kind 为空（本次没改动任何槽位）不该放行，否则会对着同一个 400 空转")
	}
}

// TestParamFixChainCoversTripleFix 三跳全链路：真实错误文案 + 真实 quirk 演进。
//
// 三跳都放行才算通过。老实现走到第三跳必然失败 —— 这就是用户看到的
// "换了个推理模型，一直 400"。
func TestParamFixChainCoversTripleFix(t *testing.T) {
	restore := withTempConfig(t, AIConfig{Model: "reasoner-x", Temperature: 0.3})
	defer restore()

	// 三条真实形态的上游报错（OpenAI 兼容接口的常见措辞）
	rejects := []string{
		"Unsupported value: 'temperature' does not support 0.3 with this model",
		"temperature is not supported with this model",
		"max_tokens is not supported for this model, use max_completion_tokens",
	}
	var f aiParamFixer
	for i, msg := range rejects {
		before := aiSnap().quirkRead()
		newQ, changed, _ := aiFixParamError(msg)
		if !changed {
			t.Fatalf("第 %d 跳：这条报错应能被修，实际没改动任何槽位（%s）", i+1, msg)
		}
		kind := aiQuirkFixKind(before, newQ)
		if !f.allow(kind) {
			t.Fatalf("第 %d 跳（%s 类）被名额拒绝 —— 三跳修不全，这类模型永远调不通（%s）",
				i+1, kind, msg)
		}
		f.take(kind)
		q := newQ // 避免闭包捕获循环变量
		aiMutate(func(a *AIConfig) { a.setQuirk(q) })
	}
	q := aiSnap().quirkRead()
	if q.TempMode != "none" {
		t.Errorf("temperature 两跳后应学成 none（不再发送），实际 %q", q.TempMode)
	}
	if q.MaxTokensField != "max_completion_tokens" {
		t.Errorf("token 字段应学成 max_completion_tokens，实际 %q", q.MaxTokensField)
	}
}
