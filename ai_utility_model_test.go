package main

// ai_utility_model_test.go —— 「内部调用模型」（utility_model）的配置语义与怪癖分账
//
// ## 为什么要测这几条
//
// 这个字段有三件事很容易做错，而且错了都不会报错：
//
//  1. **清不掉**：若沿用其它字符串字段的「空值不覆盖」，用户填过一次就再也改不回
//     "跟主模型一样"——界面上看着是空的，实际还在用那个便宜模型。
//  2. **局部保存把它抹掉**：脚本/其它客户端只 POST 一个 model 字段，
//     不该连带把 utility_model 清空（与 Enabled/MemoryEnabled 引指针是同一个事故族）。
//  3. **怪癖串账**：杂活模型若拒 temperature，把学到的 quirk 记到主模型名下，
//     主对话就会跟着少发参数——用户完全看不出是自己填了个便宜模型导致的。
//
// 三条都只能靠断言钉住：它们全都"静默生效"，不出错、不报警、只是结果不对劲。

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// decodeAICfg 读一次配置接口，返回原始 JSON（便于断言字段存在性）。
func decodeAICfg(t *testing.T) map[string]any {
	t.Helper()
	rec := getAIConfig(t)
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("配置响应不是合法 JSON: %v", err)
	}
	return m
}

// TestUtilityModelConfigClearable 设置 / 局部保存不动 / 显式清空，三条都要成立。
func TestUtilityModelConfigClearable(t *testing.T) {
	restore := withTempConfig(t, AIConfig{Model: "main-x"})
	defer restore()

	// ① 设置
	if rec := postAIConfig(t, `{"utility_model":"cheap-y"}`); rec.Code != 200 {
		t.Fatalf("设置 utility_model：HTTP %d", rec.Code)
	}
	if got := decodeAICfg(t)["utility_model"]; got != "cheap-y" {
		t.Fatalf("设置后 utility_model = %v，期望 cheap-y", got)
	}

	// ② 局部保存（只带 model）不得把它抹成空
	if rec := postAIConfig(t, `{"model":"main-z"}`); rec.Code != 200 {
		t.Fatalf("局部保存：HTTP %d", rec.Code)
	}
	if got := decodeAICfg(t)["utility_model"]; got != "cheap-y" {
		t.Errorf("只改主模型就把 utility_model 抹成了 %v —— 局部保存不该动没传的字段", got)
	}

	// ③ 显式传空串 = 清空（回到"跟主模型一样"）
	// **这正是老式"空值不覆盖"会做错的一步：用户填过就删不掉。**
	if rec := postAIConfig(t, `{"utility_model":""}`); rec.Code != 200 {
		t.Fatalf("清空 utility_model：HTTP %d", rec.Code)
	}
	if got := decodeAICfg(t)["utility_model"]; got != "" {
		t.Errorf("传空串后 utility_model = %v，期望空串（清空）", got)
	}
}

// TestUtilityModelFallbackToMain 未配置时必须回落主模型（默认行为不变）。
func TestUtilityModelFallbackToMain(t *testing.T) {
	restore := withTempConfig(t, AIConfig{Model: "main-x"})
	defer restore()
	if got := aiUtilityModel(); got != "main-x" {
		t.Errorf("未配置时应回落主模型 main-x，实际 %q", got)
	}
	// 只填空白字符等同于没填（否则会把 "   " 当成模型名发给上游）
	aiMutate(func(a *AIConfig) { a.UtilityModel = "   " })
	if got := aiUtilityModel(); got != "main-x" {
		t.Errorf("空白 utility_model 应回落主模型，实际 %q", got)
	}
	aiMutate(func(a *AIConfig) { a.UtilityModel = "cheap-y" })
	if got := aiUtilityModel(); got != "cheap-y" {
		t.Errorf("配置后应返回 cheap-y，实际 %q", got)
	}
}

// TestQuirkScopedPerModel 怪癖必须按模型名分账。
//
// 变异验证：把 quirkReadFor 改回"一律取 a.Model"，本测试立刻红。
func TestQuirkScopedPerModel(t *testing.T) {
	restore := withTempConfig(t, AIConfig{Model: "main-x"})
	defer restore()

	// 杂活模型报"不接受 temperature" → 只该改 cheap-y 的账。
	// 两跳才是完整过程（""→"one"→"none"），单跳只到 "one"。
	for hop := 1; hop <= 2; hop++ {
		q, changed, _ := aiFixParamErrorFor("cheap-y", "temperature is not supported")
		if !changed {
			t.Fatalf("第 %d 跳应能触发 temperature 自适应", hop)
		}
		aiMutate(func(a *AIConfig) { a.setQuirkFor("cheap-y", q) })
	}

	if got := aiSnap().quirkReadFor("cheap-y").TempMode; got != "none" {
		t.Errorf("cheap-y 的 TempMode 应为 none，实际 %q", got)
	}
	if got := aiSnap().quirkRead().TempMode; got != "" {
		t.Errorf("主模型 main-x 的 TempMode 被连带改成了 %q —— "+
			"杂活模型的参数怪癖污染了主对话，用户只会觉得「AI 突然变差了」", got)
	}
}

// TestUtilityModelNotUsedForUserFacingCalls 只有摘要走杂活模型。
//
// 这条是**设计约束的回归**：摘要失败用户无感，降级没问题；
// 而断流补答、诊断解读都是要交付给用户的结论，不该为了省钱换小模型。
// 判据取调用点源码——把意图钉在断言里，比写在注释里更难被无声改掉。
func TestUtilityModelNotUsedForUserFacingCalls(t *testing.T) {
	hist, err := os.ReadFile("ai_history.go")
	if err != nil {
		t.Skipf("读不到 ai_history.go: %v", err)
	}
	if !strings.Contains(string(hist), "aiUtilityModel()") {
		t.Error("ai_history.go 的滚动摘要没用 aiUtilityModel() —— 配了便宜模型也不生效")
	}
	loop, err := os.ReadFile("ai_loop.go")
	if err != nil {
		t.Skipf("读不到 ai_loop.go: %v", err)
	}
	if strings.Contains(string(loop), "aiUtilityModel()") {
		t.Error("ai_loop.go 里出现了 aiUtilityModel()：断流补答是用户可见结论，" +
			"不该降级到杂活模型")
	}
}
