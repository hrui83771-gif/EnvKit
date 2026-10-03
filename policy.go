package main

// policy.go —— v2.2 PolicyGate：写操作的唯一裁决点
//
// ## 为什么要收拢
//
// 现状是三处各自为政，语义不一致、"什么算危险"没有唯一答案：
//
//  1. aiIsOptInTool（ai_loop.go）：只有两个工具在名单里，模型不得主动发起
//  2. needConfirm + highrisk（dbops.go）：三个 HTTP 入口用，428 + 视觉升级
//  3. 执行层拦截：危险脚本名在 launchWebScriptOrDefault 里被拒，但那是**执行时**
//     才发现，模型已经白花了一轮 token，而且不知道这是"策略禁止"而非"找不到"
//
// 更要紧的是：AI 侧**没有 forbidden 概念**。危险操作在 AI 侧只能靠执行层兜，
// 而执行层兜不住的时候（用户直接调 API、或换个入口）就漏了。
//
// ## 收拢后的样子
//
// PolicyGate 是**纯函数**：无副作用、可单测、不碰 http.ResponseWriter。
// 三个调用方（AI 工具侧、HTTP handler 侧、执行层）问同一个问题、拿同一个答案。
// 规则表是 data 不是 switch——加动作只改数据不改控制流。
//
// ## 三条硬边界（这里用代码保证，不依赖提示词自觉）
//
//  1. **Memory 不是权限**：记忆只影响"建议先做什么"，不参与档位判定。
//     任何"因为记忆里说可以所以放行"的路径都不存在——PolicyGate 不读记忆。
//  2. **Lesson 不绕过 Policy**：经验命中不改变 verdict。经验能改变的是
//     建议顺序与预检选择（见 ai_tools 的召回逻辑），不是判定结果。
//  3. **Inference 不直接执行**：forbidden 档位在这里落地，模型无法绕过。
//
// ## 为什么档位是四档而不是布尔
//
// 两档（允许/拒绝）会逼着实现者在"太啰嗦"和"太危险"之间二选一：
// 要么所有写操作都弹确认（确认疲劳，用户会闭眼点同意，反而更危险），
// 要么只有删除类才拦（还原数据库这种"会覆盖数据"的和"重启服务"混为一谈）。
//
// 四档让"确认"这件事有强弱之分：elevated 会让前端用红框 + 建议先备份，
// 让用户在高风险前多想一秒。

import (
	"fmt"
	"strings"
	"time"
)

// PolicyLevel 策略档位。
type PolicyLevel string

const (
	// PolicyAuto 直接执行，不打扰用户。
	PolicyAuto PolicyLevel = "auto"
	// PolicyConfirm 需用户确认。
	PolicyConfirm PolicyLevel = "confirm"
	// PolicyElevated 需确认 + 明示风险 + 建议先备份（前端用高危样式）。
	PolicyElevated PolicyLevel = "elevated"
	// PolicyForbidden 拒绝执行，任何调用方都不可绕。
	PolicyForbidden PolicyLevel = "forbidden"
)

// PolicyVerdict 一次裁决的结果。
type PolicyVerdict struct {
	Level  PolicyLevel `json:"level"`
	Rule   string      `json:"rule"`   // 命中的规则名，进审计与指标
	Reason string      `json:"reason"` // 给人看的说明
	Action string      `json:"action"`
	Target string      `json:"target"`
	Actor  string      `json:"actor"`
	OptIn  bool        `json:"opt_in"` // 是否要求"用户明确说过才做"（仅对 AI 有意义）
}

// Allowed 档位是否允许继续执行。forbidden 与未知动作都不允许。
func (v PolicyVerdict) Allowed() bool {
	return v.Level == PolicyAuto || v.Level == PolicyConfirm || v.Level == PolicyElevated
}

// NeedsConfirm 是否需要用户点头。
func (v PolicyVerdict) NeedsConfirm() bool {
	return v.Level == PolicyConfirm || v.Level == PolicyElevated
}

// HighRisk 是否用高危视觉样式。
func (v PolicyVerdict) HighRisk() bool { return v.Level == PolicyElevated }

// policyRule 一条规则。
type policyRule struct {
	Level  PolicyLevel
	OptIn  bool
	Reason string
}

// policyRules 动作 → 规则。**规则表是数据**，加动作只改这里。
//
// 未列出的动作一律按 PolicyConfirm 处理（不是 auto 也不是 forbidden）：
// 缺省确认是安全的缺省——新加的工具忘了定级，代价是多一次点击；
// 而缺省放行的代价是安全漏洞。
var policyRules = map[string]policyRule{
	// ---- 只读：直接执行 ----
	"get_system_state":   {PolicyAuto, false, "读取环境状态，无副作用"},
	"get_logs":           {PolicyAuto, false, "读取日志，无副作用"},
	"get_diag_report":    {PolicyAuto, false, "生成诊断报告，无副作用"},
	"run_detection":      {PolicyAuto, false, "环境检测只读取版本信息"},
	"db_check":           {PolicyAuto, false, "数据库连通性检查是只读查询"},
	"list_project":       {PolicyAuto, false, "列目录，沙箱内只读"},
	"search_files":       {PolicyAuto, false, "搜文件，沙箱内只读"},
	"read_file":          {PolicyAuto, false, "读文件，沙箱内只读"},
	"verify_environment": {PolicyAuto, false, "复验只读取客观状态"},
	"recall_lessons":     {PolicyAuto, false, "读取经验，不改变任何状态"},
	// v2.3 N6：只读查询。判据是"能否修改数据"——答案是否定的：
	// dbReadOnlySQL 限定首词、强制 LIMIT、readOnlyExec 先执行
	// SET SESSION TRANSACTION READ ONLY、库名经 quoteIdent 转义。
	// 四道防线都在 MySQL 侧生效，不依赖"调用方是否老实"，
	// 所以给确认卡没有意义——只会训练用户闭眼点确认。
	"db_query": {PolicyAuto, false, "只读查询：限定 SELECT/SHOW/DESC/EXPLAIN/WITH + 强制 LIMIT + 会话级 READ ONLY"},
	"db_list":  {PolicyAuto, false, "列出库与表，只读 information_schema"},
	// v2.3 N4：查守护状态是纯读取
	"get_chain_guard": {PolicyAuto, false, "读取链端守护状态，不触发任何恢复动作"},
	// v2.3 N7：读 go.mod / package.json + 比对版本，纯只读
	"check_env_req": {PolicyAuto, false, "只读项目版本要求并与实际比对"},

	// ---- 需确认：可逆或影响可控 ----
	"start_service":     {PolicyConfirm, false, "启动服务会占用端口并可能改动运行环境"},
	"restart_service":   {PolicyConfirm, false, "重启服务会中断当前连接"},
	"stop_service":      {PolicyConfirm, false, "停止服务会中断当前连接"},
	"cleanup_processes": {PolicyConfirm, false, "结束进程会终止正在运行的任务"},
	"db_backup":         {PolicyConfirm, true, "备份会占用磁盘与数据库资源；仅在用户明确要求时执行"},
	"apply_whitelist":   {PolicyConfirm, true, "修改系统安全设置会降低防护；仅在用户明确要求时执行"},
	"manage_memories":   {PolicyConfirm, true, "记忆会长期影响 AI 行为；仅在用户明确要求时执行"},
	"save_config":       {PolicyConfirm, false, "修改配置会影响后续所有操作"},

	// ---- 高危：需确认 + 红框 + 建议先备份 ----
	"db_restore":        {PolicyElevated, false, "恢复数据库会覆盖现有数据，不可逆"},
	"apply_sql":         {PolicyElevated, false, "执行 SQL 可能修改或删除数据"},
	"kill_process":      {PolicyElevated, false, "强制结束进程可能导致未保存数据丢失"},
	"db_create":         {PolicyElevated, false, "建库会改动数据库结构"},
	"chain_autorecover": {PolicyElevated, false, "重启链端节点会中断共识与交易"},
}

// policyFallback 未登记动作的默认档位。见 policyRules 注释里的理由。
const policyFallback = PolicyConfirm

// PolicyGate 问一句"这个动作能不能做、要不要确认"，然后给出唯一答案。
//
// target 的含义随动作而变（库名 / PID / 主机:端口），**只有显式写成
// "script=<名字>" 时才会触发危险脚本检查**——不要靠猜 target 的语义。
// actor 只影响 Reason 文案与 OptIn 的适用范围（AI 侧才需要 opt-in，
// 用户自己点的按钮不需要"再确认一次自己点的按钮"）。
func PolicyGate(action, target, actor string) PolicyVerdict {
	action = strings.TrimSpace(action)
	v := PolicyVerdict{
		Action: action,
		Target: target,
		Actor:  actor,
		Level:  policyFallback,
		Rule:   "fallback",
		Reason: "该动作未登记安全策略，按需确认处理",
	}
	// 危险脚本名在任何入口都必须 forbidden：
	// 这条优先级最高，且**先于动作规则**——即便调用方把 action 写成 start_service，
	// 只要参数里带着 deploy，也不能放行。
	if bad := policyForbiddenByParam(target); bad != "" {
		v.Level = PolicyForbidden
		v.Rule = "dangerous_script"
		v.Reason = fmt.Sprintf("脚本 %q 带副作用或破坏性语义（deploy/migrate/reset 等），按安全策略拒绝执行", bad)
		return v
	}
	r, ok := policyRules[action]
	if !ok {
		return v
	}
	v.Level = r.Level
	v.Rule = "action:" + action
	v.Reason = r.Reason
	v.OptIn = r.OptIn && actor == actAI
	return v
}

// policyForbiddenByParam 从动作参数里识别必须禁止的启动脚本。
//
// **只认显式的 `script=` 前缀**，绝不从 target 裸猜。踩过的坑：
// 最初写成"target 不在白名单就是危险脚本"，结果 db_restore 的 target 是库名
// （farm）、kill_process 的 target 是 "PID 123"——两者都被判成危险脚本名，
// 还原数据库和结束进程这两个最需要正常工作的操作被直接 forbidden。
// 猜测字符串语义是这类策略代码最典型的失效方式。
//
// 返回空串表示没有可判定的脚本名（此时按动作规则走）。
func policyForbiddenByParam(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	const key = "script="
	i := strings.Index(t, key)
	if i < 0 {
		return ""
	}
	name := strings.TrimSpace(t[i+len(key):])
	if name == "" {
		return ""
	}
	// 形如 "npm run migrate" 或 "npm run dev -- --port 3000" 都取脚本名本身
	if strings.HasPrefix(name, "npm run ") {
		name = strings.TrimSpace(name[len("npm run "):])
		if j := strings.IndexAny(name, " \t"); j > 0 {
			name = name[:j]
		}
	}
	if launchScriptAllowed(name) {
		return ""
	}
	return name
}

// policyRecord 把判定结果写入审计。**只记非 auto 档**——
// 只读操作每秒可能十几次，全记会把审计文件冲垮，而 auto 档没有分析价值。
//
// 这是"安全违规率"与"误拒率"两个指标的数据源：
//   - forbidden 记录 → 违规率分子
//   - confirm/elevated 记录 + 后续用户是否确认 → 误拒率
func policyRecord(v PolicyVerdict, decision string) {
	if v.Level == PolicyAuto {
		return
	}
	auditNow(v.Actor, "policy_"+string(v.Level), v.Action,
		"level="+string(v.Level)+" rule="+v.Rule, decision, v.Reason)
}

// policyTargetOf 把工具调用参数拼成 PolicyGate 认识的 target 形式。
//
// 只有含 script 参数时才用 "script=<名>" 形式——PolicyGate 据此做危险脚本检查。
// 其余动作原样返回描述性 target（库名 / 服务名），不参与脚本判定。
func policyTargetOf(tool string, args map[string]any) string {
	// 服务名与脚本参数都要带上：缺一个都会让"AI 试过什么"与"人做了什么"对不上号。
	// 第一版只返回先命中的那个（script=xxx 会把 service=web 直接吞掉），
	// 结果 AI 侧归一得到 start_service、用户侧得到 start_service:web，
	// Human Trace 永远匹配不上——而且两边都"看起来正常"，极难发现。
	script := ""
	if s, _ := args["script"].(string); strings.TrimSpace(s) != "" {
		script = strings.TrimSpace(s)
	} else if s, _ := args["web_script"].(string); strings.TrimSpace(s) != "" {
		script = strings.TrimSpace(s)
	}
	obj := ""
	switch tool {
	case "start_service", "restart_service", "stop_service":
		obj, _ = args["service"].(string)
	case "db_backup", "db_restore", "apply_sql", "db_create":
		obj = cfg.Projects.DBName
	}
	obj = strings.TrimSpace(obj)
	switch {
	case obj != "" && script != "":
		return obj + " script=" + script
	case obj != "":
		return obj
	case script != "":
		return "script=" + script
	}
	return ""
}

// policyAIView 给 AI 看的档位说明。返回空串表示无需向模型说明（auto）。
func policyAIView(v PolicyVerdict) string {
	if v.Level == PolicyAuto {
		return ""
	}
	switch v.Level {
	case PolicyForbidden:
		return "该操作被安全策略禁止：" + v.Reason + "。不要重试，也不要换个工具绕过；" +
			"如实告诉用户它被禁止以及原因。"
	case PolicyElevated:
		return "该操作属高危：" + v.Reason + "。发起确认后必须向用户明示风险。"
	case PolicyConfirm:
		return "该操作需要用户确认：" + v.Reason
	}
	return ""
}

// policyVerdictAge 判定结果的时间戳（毫秒），供审计与 Trace 对齐。
func policyVerdictAge() int64 { return time.Now().UnixMilli() }
