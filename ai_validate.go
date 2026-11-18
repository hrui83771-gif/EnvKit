package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// 工具参数校验：把「模型说的」和「工具要的」对齐。
//
// 为什么需要这一层 —— 起因是一类**静默错误**：
// 工具内部读参数用的是类型断言（`args["lines"].(float64)`），断言失败就落回默认值。
// 模型传 `{"lines": "100"}`（字符串）时，工具不会报错，而是按 100 跑——
// 用户看到的是“跑了但结果不对”，模型也不知道自己传错了，下一轮继续这么传。
//
// 每个工具的 `Schema` 本来就要发给上游，是同一份契约。
// 拿它做本地校验，零额外维护，而且能把错误变成**模型能自己改的一手信息**：
//
//	参数不合规，未执行「get_logs」：
//	- lines：应为整数，实际 字符串
//	请按参数说明修正后重试；不要用同样的参数重复调用。
//
// 校验位置（重要）：在 aiExecTool（所有执行的唯一入口）和 ai_loop 的策略闸之后、
// 确认卡之前。顺序不能反——forbidden 必须先生效（否则模型会把“禁止”理解成
// “参数写错了”，反复重试），而确认卡不能让用户去确认一个注定失败的调用。
var (
	aiSchemaMu    sync.RWMutex
	aiSchemaCache = map[string]*jsonschema.Schema{}
	// 编译失败只记一次：自己的 schema 写坏了是开发期缺陷，
	// 不能变成用户每次调用都重编一遍、还白刷日志。
	aiSchemaBroken = map[string]string{}
)

// aiToolSchema 编译并缓存某工具的参数 schema。
// 返回 (nil, 原因) 表示这份 schema 本身不可用——调用方应当**放行**，
// 因为“schema 坏了”是我们的 bug，不该让用户的功能整体不可用。
func aiToolSchema(name string) (*jsonschema.Schema, string) {
	aiSchemaMu.RLock()
	sch, ok := aiSchemaCache[name]
	broken, bad := aiSchemaBroken[name]
	aiSchemaMu.RUnlock()
	if ok {
		return sch, ""
	}
	if bad {
		return nil, broken
	}

	t := aiToolRegistry[name]
	raw, err := json.Marshal(t.Schema)
	if err != nil {
		aiSchemaMu.Lock()
		aiSchemaBroken[name] = err.Error()
		aiSchemaMu.Unlock()
		return nil, err.Error()
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		aiSchemaMu.Lock()
		aiSchemaBroken[name] = err.Error()
		aiSchemaMu.Unlock()
		return nil, err.Error()
	}
	// 用固定域名而不是自定义 scheme：AddResource 要按 URI 解析 $ref，
	// 让所有工具共享同一个 host，将来加 $ref 也不会因为 scheme 不认识而失败。
	loc := "https://envkit.local/tool/" + name + ".json"
	c := jsonschema.NewCompiler()
	if err := c.AddResource(loc, doc); err != nil {
		aiSchemaMu.Lock()
		aiSchemaBroken[name] = err.Error()
		aiSchemaMu.Unlock()
		return nil, err.Error()
	}
	sch, err = c.Compile(loc)
	if err != nil {
		aiSchemaMu.Lock()
		aiSchemaBroken[name] = err.Error()
		aiSchemaMu.Unlock()
		return nil, err.Error()
	}
	aiSchemaMu.Lock()
	aiSchemaCache[name] = sch
	aiSchemaMu.Unlock()
	return sch, ""
}

// aiValidateToolArgs 校验一次工具调用的参数。
//
// 返回 nil 表示放行。返回 error 时，文本会被原样交给模型（见 ai_loop 的
// 「参数不合规」分支），所以措辞要**面向模型**：说清哪个字段、期望什么、实际是什么。
func aiValidateToolArgs(name string, args map[string]any) error {
	t, known := aiToolRegistry[name]
	if !known || len(t.Schema) == 0 {
		return nil
	}
	sch, broken := aiToolSchema(name)
	if sch == nil {
		// 自己的 schema 编译不过：只记一次，然后放行。
		// 用 warn 而不是 fail——这不是用户环境的故障，是代码缺陷，
		// 而 warn 会进日志页，写代码的人能看见。
		warn(scSys, "AI", "工具 %s 的参数 schema 不可用（已跳过校验）：%s", name, broken)
		return nil
	}
	if args == nil {
		args = map[string]any{}
	}
	if err := sch.Validate(args); err != nil {
		return errors.New(aiSchemaErrText(name, err))
	}
	return nil
}

// aiSchemaErrText 把库的校验错误翻成模型能直接照做的一段话。
func aiSchemaErrText(name string, err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return "参数不合规，未执行「" + name + "」：" + err.Error()
	}
	items := aiSchemaErrItems(ve, 0)
	if len(items) == 0 {
		items = []string{ve.ErrorKind.KeywordPath()[0] + " 约束未满足"}
	}
	// 最多列 4 条：参数错多半是同一个原因（类型全写成字符串），
	// 列太多会把这条工具结果撑长，而它会永久留在历史里、每轮重算。
	if len(items) > 4 {
		items = append(items[:4], fmt.Sprintf("（另有 %d 处同类问题）", len(items)-4))
	}
	return "参数不合规，未执行「" + name + "」：\n- " + strings.Join(items, "\n- ") +
		"\n请按工具的参数说明修正后重试；不要用同样的参数重复调用。"
}

// aiSchemaErrItems 展平成「字段：原因」列表。
//
// 库把错误做成树（对象级错误 → 各字段的 Causes），只有叶子才说得出具体字段，
// 所以必须递归到叶子；直接读顶层会得到一句没有字段名的 "doesn't validate with ..."
// ——那句话对模型毫无用处。
func aiSchemaErrItems(ve *jsonschema.ValidationError, depth int) []string {
	if depth > 6 {
		return nil
	}
	if len(ve.Causes) == 0 {
		if s := aiSchemaErrItem(ve); s != "" {
			return []string{s}
		}
		return nil
	}
	var out []string
	for _, c := range ve.Causes {
		out = append(out, aiSchemaErrItems(c, depth+1)...)
	}
	return out
}

func aiSchemaErrItem(ve *jsonschema.ValidationError) string {
	field := strings.Join(ve.InstanceLocation, ".")
	if field == "" {
		field = "（参数）"
	}
	switch k := ve.ErrorKind.(type) {
	case *kind.Type:
		return field + "：应为" + aiJSONTypeCNList(k.Want) + "，实际 " + aiJSONTypeCN(k.Got)
	case *kind.Required:
		// 缺字段时 InstanceLocation 指向父对象，用字段名当主体更直接。
		return "缺少必填参数：" + strings.Join(k.Missing, "、")
	case *kind.Enum:
		want := make([]string, 0, len(k.Want))
		for _, w := range k.Want {
			want = append(want, fmt.Sprintf("%v", w))
		}
		return field + "：取值必须是 " + strings.Join(want, " / ") + " 之一，实际 " +
			aiJSONValueBrief(k.Got)
	case *kind.Minimum:
		return field + "：不能小于 " + k.Want.RatString()
	case *kind.Maximum:
		return field + "：不能大于 " + k.Want.RatString()
	case *kind.AdditionalProperties:
		return field + "：出现了未声明的参数"
	case *kind.MinLength:
		return field + "：长度不足"
	case *kind.MaxLength:
		return field + "：超出长度上限"
	}
	// 兜底：至少告诉它违反的是哪条关键字，比 "doesn't validate" 有用。
	kw := ve.ErrorKind.KeywordPath()
	if len(kw) == 0 {
		return field + "：不满足约束"
	}
	return field + "：不满足 " + kw[0] + " 约束"
}

// aiJSONTypeCN 把库输出的 JSON 类型名换成中文。
// 库给的 Got / Want 是 ASCII 名（integer/string/...），直接给模型看也能懂，
// 但用户会在计划步骤里看到这句话，中文更省一次翻译。
func aiJSONTypeCN(t string) string {
	switch t {
	case "integer":
		return "整数"
	case "number":
		return "数字"
	case "string":
		return "字符串"
	case "boolean":
		return "布尔值"
	case "object":
		return "对象"
	case "array":
		return "数组"
	case "null":
		return "null"
	}
	return t
}

func aiJSONTypeCNList(ts []string) string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, aiJSONTypeCN(t))
	}
	if len(out) == 1 {
		return out[0]
	}
	return strings.Join(out, " 或 ")
}

// aiJSONValueBrief 把非法值缩成一句话，避免把一整个对象打进历史。
func aiJSONValueBrief(v any) string {
	s := fmt.Sprintf("%v", v)
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}
