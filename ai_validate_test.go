package main

import (
	"strings"
	"testing"
)

// 工具参数校验的门禁。
//
// 这里刻意分两组：
//   - 「必须拒」：类型/必填/枚举写错，必须报错，且报错文本里要有字段名与期望值
//     （模型就是靠这句话改参数的，缺了字段名它只能瞎猜）；
//   - 「必须放」：合法值、模型常见的等价写法、schema 为空或编译不过时都不许拦。
//     后一半同样重要——校验做过头会把现在能跑通的调用变成失败，
//     而这种回归在自己写的测试里最容易漏（只测"该拒的都拒了"是天然不会红的）。

// TestAllToolSchemasCompile 全表 schema 必须能编译。
//
// 这条是 aiValidateToolArgs 放行分支的反例：它允许"schema 坏了就跳过校验"，
// 所以必须有一条门禁证明**出厂的 schema 一个都没坏**，
// 否则那个放行分支会悄悄把整个校验层变成空操作。
func TestAllToolSchemasCompile(t *testing.T) {
	if len(aiToolRegistry) < 20 {
		t.Fatalf("工具表只有 %d 个，注册没生效，这条测试会失去意义", len(aiToolRegistry))
	}
	var broken []string
	for name := range aiToolRegistry {
		if sch, why := aiToolSchema(name); sch == nil {
			broken = append(broken, name+": "+why)
		}
	}
	if len(broken) > 0 {
		t.Fatalf("这些工具的 schema 编译不过（校验层会整体放行）：\n%s", strings.Join(broken, "\n"))
	}
}

// TestValidateRejectsWrongType 模型把整数写成字符串必须被拦。
//
// 这正是改造前的静默路径：工具里 `args["lines"].(float64)` 断言失败 → 落回默认 100，
// 不报错、结果不对。现在必须在执行前失败，并把字段名与期望类型说清楚。
func TestValidateRejectsWrongType(t *testing.T) {
	err := aiValidateToolArgs("get_logs", map[string]any{"lines": "100"})
	if err == nil {
		t.Fatal("lines 传字符串应当被拒，实际放行了（静默回落默认值的老问题会复现）")
	}
	msg := err.Error()
	if !strings.Contains(msg, "lines") {
		t.Errorf("报错里没有字段名，模型无法定位：%s", msg)
	}
	if !strings.Contains(msg, "整数") || !strings.Contains(msg, "字符串") {
		t.Errorf("报错里没有「期望 vs 实际」类型，模型无法照做：%s", msg)
	}
}

// TestValidateRejectsMissingRequired 少必填字段必须被拦，且点名缺了哪个。
func TestValidateRejectsMissingRequired(t *testing.T) {
	err := aiValidateToolArgs("search_files", map[string]any{})
	if err == nil {
		t.Fatal("缺 pattern 应当被拒")
	}
	if !strings.Contains(err.Error(), "pattern") {
		t.Errorf("报错没点名缺失字段：%s", err.Error())
	}
}

// TestValidateRejectsBadEnum 枚举外的值必须被拦，并把合法取值列出来。
func TestValidateRejectsBadEnum(t *testing.T) {
	err := aiValidateToolArgs("get_logs", map[string]any{"scope": "nope"})
	if err == nil {
		t.Fatal("scope=nope 不在枚举里，应当被拒")
	}
	if !strings.Contains(err.Error(), "install") {
		t.Errorf("报错没给出合法取值，模型只能猜：%s", err.Error())
	}
}

// TestValidateAcceptsModelShapedArgs 合法输入不许拦。
//
// 重点在 float64：JSON 解到 any 里整数就是 float64，
// 如果 schema 的 integer 不认 float64(100)，这条会红——
// 那等于把**每一次正常的数字参数**都判成错。
func TestValidateAcceptsModelShapedArgs(t *testing.T) {
	cases := []map[string]any{
		{"lines": float64(50), "scope": "all"},
		{"lines": float64(50)},        // 可选字段缺省
		{},                            // 全缺省
		{"scope": "install"},          // 只给枚举
	}
	for _, args := range cases {
		if err := aiValidateToolArgs("get_logs", args); err != nil {
			t.Errorf("合法参数被拒 %v：%v", args, err)
		}
	}
}

// TestValidateAllowsExtraKeys 多传的键不许拦。
//
// 工具的 schema 都没有 additionalProperties:false。模型偶尔会带上
// "reason"、"note" 这类解释字段；这些不该让调用失败——
// 校验的目的是对齐类型，不是收紧接口。
func TestValidateAllowsExtraKeys(t *testing.T) {
	if err := aiValidateToolArgs("get_system_state", map[string]any{"reason": "看看状态"}); err != nil {
		t.Fatalf("空 schema 的工具不该因为多余键被拒：%v", err)
	}
	if err := aiValidateToolArgs("get_logs", map[string]any{"lines": float64(10), "note": "x"}); err != nil {
		t.Fatalf("多余键不该被拒：%v", err)
	}
}

// TestValidateUnknownToolIsNoop 未知工具交给 aiExecTool 报「不在白名单」，
// 校验层不许抢答——两处各报一句会让模型收到两条互相矛盾的错误。
func TestValidateUnknownToolIsNoop(t *testing.T) {
	if err := aiValidateToolArgs("definitely_not_a_tool", map[string]any{"x": 1}); err != nil {
		t.Fatalf("未知工具应交给执行层报错，校验层不该插话：%v", err)
	}
}

// TestExecToolRefusesInvalidArgsBeforeExecute 校验必须真的接在执行链上。
//
// 上面那些测试都直接调 aiValidateToolArgs——它们证明不了「AI 调错参数时工具不会跑」。
// 这条才是用户真正遇到的那条路：aiExecTool → 校验 → Execute。
// 少了它，把 aiExecTool 里的那两行删掉，测试依然全绿（接线断了也看不出来）。
func TestExecToolRefusesInvalidArgsBeforeExecute(t *testing.T) {
	const name = "zz_arg_guard_probe"
	ran := false
	aiToolRegistry[name] = aiTool{
		Desc: "测试用的参数探针",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"n": map[string]any{"type": "integer"},
		}, "required": []string{"n"}},
		Execute: func(map[string]any) (string, error) {
			ran = true
			return "ok", nil
		},
	}
	defer func() {
		delete(aiToolRegistry, name)
		aiSchemaMu.Lock()
		delete(aiSchemaCache, name)
		delete(aiSchemaBroken, name)
		aiSchemaMu.Unlock()
	}()

	if _, err := aiExecTool(name, map[string]any{"n": "not-a-number"}); err == nil {
		t.Fatal("参数不合规时 aiExecTool 必须返回错误")
	}
	if ran {
		t.Fatal("参数不合规时 Execute 不该被调用（校验没接在执行链上）")
	}

	// 反向对照：合法参数必须真跑。
	// 没有这一半，"永远不执行"也能让上面两条通过——那样测的就不是校验，是瘫痪。
	ran = false
	if _, err := aiExecTool(name, map[string]any{"n": float64(3)}); err != nil {
		t.Fatalf("合法参数不该被拦：%v", err)
	}
	if !ran {
		t.Fatal("合法参数必须真正执行")
	}
}

//
// TestValidateBrokenSchemaDoesNotBlock 自己的 schema 坏了不许拖垮用户功能。
//
// 用不可解析的 $ref 造一个必然编译失败的 schema：这模拟的是"我们写错了"，
// 而写错代码的代价应该是日志里一条 warn + 该工具退化成不校验，
// 不是让用户点的按钮全都报错。
func TestValidateBrokenSchemaDoesNotBlock(t *testing.T) {
	const name = "zz_broken_schema_probe"
	aiToolRegistry[name] = aiTool{
		Desc:   "测试用的坏 schema",
		Schema: map[string]any{"type": "object", "$ref": "https://envkit.local/does-not-exist.json"},
		Execute: func(map[string]any) (string, error) {
			return "ok", nil
		},
	}
	defer func() {
		delete(aiToolRegistry, name)
		aiSchemaMu.Lock()
		delete(aiSchemaCache, name)
		delete(aiSchemaBroken, name)
		aiSchemaMu.Unlock()
	}()

	if sch, _ := aiToolSchema(name); sch != nil {
		t.Fatal("这个 schema 本该编译失败，编译器放过了它，测试前提不成立")
	}
	if err := aiValidateToolArgs(name, map[string]any{"anything": 1}); err != nil {
		t.Fatalf("schema 坏了应放行（只记 warn），实际拦住了：%v", err)
	}
}
