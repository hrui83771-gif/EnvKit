package main

// AI 对话循环：流式转发 + 工具调用调度（读工具自动执行，写工具转确认流程）。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func sseWrite(w http.ResponseWriter, fl http.Flusher, v map[string]any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
	fl.Flush()
}

func aiChatReady(w http.ResponseWriter) (http.Flusher, bool) {
	if !aiSnap().ready() {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","text":"AI 助手未配置：请到「程序配置 → AI 助手」填写 Base URL 与 API Key"}`))
		return nil, false
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	fl.Flush()
	return fl, true
}

func handleAIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	fl, ok := aiChatReady(w)
	if !ok {
		return
	}
	usage := aiUsage{} // 空请求错误路径不会产生用量，这里只为复用统一的收尾 helper
	var body aiChatReq
	decErr := json.NewDecoder(r.Body).Decode(&body)
	msgs := body.Messages

	// 实测：某些本机代理/预览容器会剥掉 POST body（服务端表现为 decode 出 0 条消息，
	// 模型因此对着"空输入+环境状态"自说自话，用户以为 AI 不听话）。鉴权令牌走请求头
	// 能完好穿过，所以前端把"最后一条用户消息 + 确认回执"也放进 X-EnvKit-Chat-Fallback
	// 头兜底；body 丢失时据此恢复，保证对话和确认流程在代理环境下依然可用。
	if (decErr != nil || len(msgs) == 0) && body.Confirm == nil {
		if fbRaw, ferr := url.QueryUnescape(r.Header.Get("X-EnvKit-Chat-Fallback")); ferr == nil && fbRaw != "" {
			var fb struct {
				Last    string        `json:"last"`
				Confirm *aiConfirmReq `json:"confirm"`
				Lang    string        `json:"lang"`
			}
			if json.Unmarshal([]byte(fbRaw), &fb) == nil {
				body.Confirm = fb.Confirm
				if body.Lang == "" {
					body.Lang = fb.Lang
				}
				if len(msgs) == 0 && fb.Last != "" {
					msgs = []aiMsg{{Role: "user", Content: fb.Last}}
					warn(scSys, "AI", "请求体未到达（被本机代理/预览容器拦截），已用回退头恢复对话")
				}
			}
		}
	}
	// 仍然拿不到任何用户内容：显式报错，绝不静默地把空输入丢给模型空转
	if len(msgs) == 0 && body.Confirm == nil {
		sseWrite(w, fl, map[string]any{"type": "error", "text": "AI 收到空请求：请求体未到达服务端" +
			"（通常是被本机代理或嵌入预览容器拦截）。请用系统浏览器直接打开控制台地址后重试。"})
		sseDoneUsage(w, fl, &usage)
		return
	}
	// 悬挂的 assistant(tool_calls)（用户无视确认卡片直接发新消息、或确认续接的历史回传）
	// 会让上游 OpenAI 协议报 400，统一剥掉（协议里工具结果一律走 aiToolResultAsUser）。
	msgs = aiTrimDanglingToolCalls(msgs)
	// 滚动摘要：历史变长后把较早的部分压缩成要点备忘（按前缀缓存，不每回合重复生成），
	// 必须在下面的 40 条硬裁剪之前做——否则最早的用户目标会被无感截断，模型显得健忘。
	msgs = aiMaybeSummarize(msgs)
	if len(msgs) > 40 {
		msgs = msgs[len(msgs)-40:]
	}
	// 确认回执：执行（或拒绝）写工具，然后继续对话
	result := ""
	confirmed := false
	if body.Confirm != nil {
		confirmed = true
		t, exists := aiToolRegistry[body.Confirm.Tool]
		switch {
		case !exists:
			result = "错误：该工具不在白名单内"
		case !body.Confirm.Approved:
			result = "用户拒绝了该操作。请给出替代的手动操作步骤。"
		default:
			// 执行前先给可见反馈：go build / npm install / mysqldump 可能耗时 1~2 分钟，
			// 期间一条输出都没有，用户会以为"点了确认没反应"而重复点击。
			sseWrite(w, fl, map[string]any{"type": "delta", "text": "正在执行：" +
				aiToolCnName(body.Confirm.Tool, body.Confirm.Args) + "…（可能需要几十秒到几分钟，请稍候）\n"})
			if res, err := t.Execute(body.Confirm.Args); err != nil {
				result = "执行出错：" + err.Error()
			} else {
				result = res
			}
		}
		// 不合成 assistant(tool_calls)+tool（thinking 模型要求回传 reasoning_content，合成消息会 400），
		// 改为剥掉该回合并用一条 user 消息把结果交给模型。
		msgs = aiTrimLastToolTurn(msgs)
		msgs = append(msgs, aiToolResultAsUser(body.Confirm.Tool, firstLines(result, 3000)))
		sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": body.Confirm.Tool, "result": firstLines(result, 300)})
		// 确定性结果行：不让"到底成没成"取决于模型怎么描述
		name := aiToolCnName(body.Confirm.Tool, body.Confirm.Args)
		if body.Confirm.Approved {
			res := firstLines(strings.TrimSpace(result), 160)
			sseWrite(w, fl, map[string]any{"type": "delta", "text": "✓ 已执行：" + name + " —— " + res + "\n\n"})
		} else {
			sseWrite(w, fl, map[string]any{"type": "delta", "text": "✗ 已取消：" + name + "\n\n"})
		}
	}
	tw := &errTrackWriter{ResponseWriter: w}
	aiRunLoop(r.Context(), tw, fl, msgs, confirmed, true, body.Lang)
}

// ---------- 诊断解读入口 ----------

func handleAIExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body aiChatReq // 只为了拿界面语言（lang）；messages 由服务端用诊断报告生成
	_ = json.NewDecoder(r.Body).Decode(&body)
	fl, ok := aiChatReady(w)
	if !ok {
		return
	}
	report := buildDiagReport()
	msgs := []aiMsg{{Role: "user", Content: "请解读以下 EnvKit 诊断报告：先给出现状总结（2-3 句），再列出发现的问题与建议步骤。\n<untrusted_data>\n" + report + "\n</untrusted_data>"}}
	tw := &errTrackWriter{ResponseWriter: w}
	aiRunLoop(r.Context(), tw, fl, msgs, false, false, body.Lang)
}

// errTrackWriter 包装 ResponseWriter，记录写出错（客户端关闭页面后 SSE 写入会失败）。
// Go 的 HTTP server 在"只写不读"的长连接上不会主动感知对端断开，必须靠写失败来判断。
type errTrackWriter struct {
	http.ResponseWriter
	err error
}

func (e *errTrackWriter) Write(b []byte) (int, error) {
	n, err := e.ResponseWriter.Write(b)
	if err != nil {
		e.err = err
	}
	return n, err
}

func (e *errTrackWriter) Flush() {
	if f, ok := e.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (e *errTrackWriter) writeErr() error { return e.err }

// Unwrap 让 http.ResponseController 能找到底层 writer 的 FlushError，从而拿到真实的写失败
func (e *errTrackWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }

// clientGone 判断客户端是否已不在（context 取消 或 SSE 写失败）
func clientGone(ctx context.Context, w http.ResponseWriter) bool {
	if ctx.Err() != nil {
		return true
	}
	if ew, ok := w.(interface{ writeErr() error }); ok {
		return ew.writeErr() != nil
	}
	return false
}

// ---------- 对话循环 ----------

// 上游流式响应的工具调用累积器
type aiAccTool struct {
	ID    string
	Name  string
	ArgsB strings.Builder
}

// aiBareAffirm 用户只回了表示同意/催促的词（"好""嗯""可以""确认""需要"等）。
// 这类输入无法确定性映射到具体操作，需配合 aiBacktrackIntent 回溯上下文。
func aiBareAffirm(t string) bool {
	return reOf(`^(好|好的|好呀|好吧|好嘞|嗯|嗯嗯|行|行吧|可以|是的|对|确认|确定|开始|开始吧|继续|麻烦了|辛苦了|需要|需要呀|需要吧|需要滴|要|要吧|要的|当然|当然可以|走起|执行吧|就这么办|go|ok|OK)[吧呢呀啊~！!。.]*$`).MatchString(t)
}

// aiActionFamilies 动作主题分类：统计一条回复涉及几个不同动作主题，
// 用于区分"单一动作的提议/汇报"与"多选项菜单"（见 aiLooksLikeMenu）。
var aiActionFamilies = []struct {
	key  string
	pats []string
}{
	{"start", []string{"启动", "起服务", "拉起", "跑起来"}},
	{"stop", []string{"停止", "停掉", "关掉", "关闭"}},
	{"restart", []string{"重启", "重新启动", "重新起"}},
	{"backup", []string{"备份", "导出数据库", "dump"}},
	{"whitelist", []string{"白名单", "杀软", "defender"}},
	{"logs", []string{"日志"}},
	{"detect", []string{"检测"}},
	{"diag", []string{"诊断", "体检"}},
	{"cleanup", []string{"清理进程", "清理后台"}},
}

// aiActionTopicCount 返回文本涉及的动作主题数
func aiActionTopicCount(text string) int {
	low := strings.ToLower(text)
	n := 0
	for _, f := range aiActionFamilies {
		for _, p := range f.pats {
			if strings.Contains(low, strings.ToLower(p)) {
				n++
				break
			}
		}
	}
	return n
}

// aiAsstReportedDone 助手消息是否属于"已完成动作的汇报"（"备份完成，文件在…"）。
// 这类消息里的动作词是过去式，用户回"好"不代表要再做一遍，不得用于提取意图。
func aiAsstReportedDone(text string) bool {
	for _, pt := range []string{"已启动", "已停止", "已重启", "已备份", "已完成", "已执行", "已取消",
		"执行成功", "备份成功", "启动成功", "已发送停止信号", "备份任务已执行"} {
		if strings.Contains(text, pt) {
			return true
		}
	}
	return false
}

// aiPromisedAction 判断助手上一条回复是否属于"承诺要执行却没发起工具调用"的空转回复
// （如"只要你给一句明确指令，我立即发起对应工具调用"）。
func aiPromisedAction(text string) bool {
	for _, pt := range []string{
		"我立即发起", "我就发起", "我将发起", "发起对应工具调用", "立即执行对应", "明确指令",
		"说一声", "给一句", "给我一句", "点名", "需你点", "需要你点", "按先后端再前端", "我就按",
	} {
		if strings.Contains(text, pt) {
			return true
		}
	}
	return false
}

// aiMatchIntent 确定性意图识别（不依赖模型，保证"说了就执行"）。
// 返回 (工具名, 参数, 是否写操作)。用于预检快通道与模型不发起调用时的兜底。
func aiMatchIntent(text string) (string, map[string]any, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return "", nil, false
	}
	has := func(p string) bool { return reOf(p).MatchString(t) }
	switch {
	// ---- 停止 ----
	case has(`(停止|停掉|关掉|关闭)\s*(全部|所有|前后端)`) || strings.Contains(t, "全部停止"):
		return "stop_service", map[string]any{"service": "all"}, true
	case has(`(停止|停掉|关掉|关闭).{0,4}(前端|web|前台)`) || has(`(前端|web|前台).{0,4}(停|关)`):
		return "stop_service", map[string]any{"service": "web"}, true
	case has(`(停止|停掉|关掉|关闭).{0,4}(后端|后台|服务|server)`) || has(`(后端|后台|服务).{0,4}(停|关)`):
		return "stop_service", map[string]any{"service": "backend"}, true
	// ---- 重启（必须排在启动/停止之前："重新启动"含"启动"、"重启服务"不是停止） ----
	case has(`(重启|重新启动|重新起)\s*(一下|个)?\s*(前端|web|前台)`) || has(`(前端|web|前台)\s*(重启|重新启动)`):
		return "restart_service", map[string]any{"service": "web"}, true
	case has(`(重启|重新启动|重新起)\s*(一下|个)?\s*(后端|后台|server|api)`) || has(`(后端|后台|server|api)\s*(重启|重新启动)`):
		return "restart_service", map[string]any{"service": "backend"}, true
	case reOf(`^(重启|重新启动|重新起)(服务|前后端|前后台|整套|全部|所有|整个项目|全部服务|一下|吧|呀|啊|呗|哈|么|吗)?$`).MatchString(t) ||
		has(`(重启|重新启动|重新起)\s*(一下|个)?\s*(全部|所有|前后端|前后台|整套|整个项目|全部服务)`) ||
		has(`(全部|所有|前后端|前后台|整套|整个项目|全部服务)\s*(重启|重新启动)`):
		return "restart_service", map[string]any{"service": "all"}, true
	// ---- 启动 ----
	case has(`(启动|运行|拉起|起|开)\s*(一下|个)?\s*(前端|web|前台)`) || t == "前端" || t == "web":
		return "start_service", map[string]any{"service": "web"}, true
	case has(`(启动|运行|拉起|起|开)\s*(一下|个)?\s*(后端|后台|server|api)`) || t == "后端":
		return "start_service", map[string]any{"service": "backend"}, true
	case has(`(把|将)?\s*(服务|前后端|项目|环境)\s*(起|启动|拉起|跑起来)`) || strings.Contains(t, "起来") ||
		strings.Contains(t, "起服务") || strings.Contains(t, "全部启动") || strings.Contains(t, "启动服务") ||
		strings.Contains(t, "一键启动") ||
		// 「启动前后端」「前后端跑起来」「整个项目拉起」等说法（前后端三字夹在中间，不能按词组匹配）
		has(`(启动|起|拉起|运行|跑)\s*(一下|个)?\s*(前后端|前后台|全套|整个项目|全部服务)`) ||
		has(`(前后端|前后台|全套|整个项目|全部服务)\s*(启动|起|拉起|跑|运行)`) ||
		// 裸指令：「启动」「启动吧」「起服务一下」等
		reOf(`^(启动|起服务|启动服务|拉起|跑起来|全部启动|一键启动)(服务|吧|呀|啊|呗|哈|一下|么|吗)?$`).MatchString(t):
		// 一次确认完成整套启动（先后端再前端），避免来回弹两次确认
		return "start_service", map[string]any{"service": "all"}, true
	// ---- 备份 ----
	case strings.Contains(t, "备份") || strings.Contains(t, "导出数据库") || has(`(?i)dump`):
		return "db_backup", map[string]any{}, true
	// ---- 检测 ----
	case has(`(重新)?检测\s*(组件|环境|一下|一遍)?$`) || has(`检测组件|检测环境|重新检测`) || has(`^\s*检测\s*$`):
		return "run_detection", map[string]any{}, true
	// ---- 清理后台进程 ----
	case has(`(清理|清掉|结束|杀掉|杀死|停掉).{0,6}(后台)?(进程|服务残留|残留进程)`) ||
		has(`(清理|杀|结束).{0,4}端口占用`) || strings.Contains(t, "清理进程") || strings.Contains(t, "清理后台"):
		return "cleanup_processes", map[string]any{}, true
	// ---- 数据库连接检测 ----
	// 注意：不能裸匹配"数据库/mysql"关键词——否则"MySQL 8 有什么新特性"这类普通问答也会触发检测
	case has(`(数据库|mysql|3306).{0,10}(检测|检查|测试|测一下|连接|连不上|通不通|状态|正常)`) ||
		has(`(检测|检查|测试|看看).{0,6}(数据库|mysql|连接)`):
		return "db_check", map[string]any{}, false
	// ---- 日志 ----
	case strings.Contains(t, "日志") || has(`(?i)\blog\b`):
		scope := "all"
		switch {
		case strings.Contains(t, "安装") || has(`(?i)install`):
			scope = "install"
		case strings.Contains(t, "配置") || has(`(?i)config`):
			scope = "config"
		case strings.Contains(t, "链") || has(`(?i)chain`):
			scope = "chain"
		case strings.Contains(t, "系统") || has(`(?i)\bsys\b`):
			scope = "sys"
		case strings.Contains(t, "启动") || has(`(?i)start`):
			scope = "start"
		}
		return "get_logs", map[string]any{"scope": scope, "lines": 100}, false
	// ---- 诊断报告 ----
	case strings.Contains(t, "诊断") || strings.Contains(t, "体检") || strings.Contains(t, "报告"):
		return "get_diag_report", map[string]any{}, false
	// ---- 白名单 ----
	case strings.Contains(t, "白名单") || strings.Contains(t, "杀软") || has(`(?i)defender`):
		return "apply_whitelist", map[string]any{}, true
	// ---- 状态（仅短问句）----
	case len([]rune(t)) <= 30 && has(`(版本|状态|环境|怎么样|如何|正常吗|好不好|看看)`):
		return "get_system_state", map[string]any{}, false
	}
	return "", nil, false
}

// aiResolveNumberInput 用户只回复数字/序号时，从助手最近一条消息里取出该编号对应的选项文字。
// 例如助手列了 "5. 查看日志"，用户回 "5" → 返回 "查看日志"。
func aiResolveNumberInput(text string, msgs []aiMsg) string {
	num := strings.TrimSpace(text)
	num = strings.Trim(num, ".)、．:：")
	if !reOf(`^\d{1,2}$`).MatchString(num) {
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" || strings.TrimSpace(msgs[i].Content) == "" {
			continue
		}
		for _, line := range strings.Split(msgs[i].Content, "\n") {
			l := strings.TrimSpace(line)
			// 注意：Go 的 regexp 不支持 \uXXXX 转义（只能用 \x{...} 或直接写字面量字符），
			// 之前写成 `[\s\u00b7...]` 会让 MustCompile panic —— 用户只回一个数字就 500。
			m := reOf(`^[\s·•\-\*●]*` + num + `[\.、)．：:]\s*(.+)$`).FindStringSubmatch(l)
			if len(m) == 2 {
				return strings.TrimSpace(m[1])
			}
		}
		break // 只看最近一条助手消息
	}
	return ""
}

// aiTrimLastToolTurn 去掉末尾的 "assistant(tool_calls) + tool" 回合。
// 思考型模型（如 deepseek 的 thinking 模式）要求助手消息原样回传 reasoning_content，
// 我们合成的 assistant 消息带不上它 → 上游会报 400。因此不合成该结构，
// 改为用一条 user 消息把工具结果交给模型，兼容性最好。
// aiBacktrackIntent 用户输入无法直接映射到工具（典型：裸肯定"好/需要/要"）时，
// 从上下文回溯出真正要执行的动作。回溯顺序：
//  1. 输入本身能直接匹配意图（"启动"）→ 直接返回；
//  2. 用户回裸肯定 + 助手上一条是"承诺要执行"（"给我一句指令我立即发起"）→
//     从最近 6 条用户消息里找明确指令；
//  3. 用户回裸肯定 + 助手上一条是"主动提议"（"需要我帮你备份数据库吗？"）→
//     从助手消息本身提取动作。
//
// 找不到返回空。写操作仍会走确认卡片，不会越权。
func aiBacktrackIntent(lastUser string, msgs []aiMsg) (string, map[string]any, bool) {
	if tool, args, isWrite := aiMatchIntent(lastUser); tool != "" {
		return tool, args, isWrite
	}
	if !aiBareAffirm(strings.TrimSpace(lastUser)) {
		return "", nil, false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" || strings.TrimSpace(msgs[i].Content) == "" {
			continue
		}
		asst := msgs[i].Content
		if aiPromisedAction(asst) {
			seen := 0
			for j := len(msgs) - 1; j >= 0 && seen < 6; j-- {
				if msgs[j].Role != "user" {
					continue
				}
				seen++
				if tt, aa, ww := aiMatchIntent(msgs[j].Content); tt != "" {
					return tt, aa, ww
				}
			}
		}
		if !aiAsstReportedDone(asst) && !aiLooksLikeMenu(asst) {
			if tt, aa, ww := aiMatchIntent(asst); tt != "" {
				return tt, aa, ww
			}
		}
		break // 只看最近一条助手消息
	}
	return "", nil, false
}

// aiTrimDanglingToolCalls 去掉历史中所有"没有紧跟 tool 结果"的 assistant(tool_calls) 消息。
// 场景：确认卡片弹出后用户不理会，直接发新消息 → 前端历史里留下悬挂的 tool_calls，
// 位置在中间（aiTrimLastToolTurn 只裁末尾），上游 OpenAI 协议直接 400。
// 非确认续接路径（prompt 分支）也调用它兜底。
func aiTrimDanglingToolCalls(msgs []aiMsg) []aiMsg {
	out := msgs[:0:0]
	for i, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			if i+1 >= len(msgs) || msgs[i+1].Role != "tool" {
				continue // 悬挂：丢弃
			}
		}
		out = append(out, m)
	}
	return out
}

func aiTrimLastToolTurn(msgs []aiMsg) []aiMsg {
	i := len(msgs)
	for i > 0 && msgs[i-1].Role == "tool" {
		i--
	}
	if i > 0 && i < len(msgs) && msgs[i-1].Role == "assistant" && len(msgs[i-1].ToolCalls) > 0 {
		i--
	}
	if i < len(msgs) {
		return msgs[:i]
	}
	return msgs
}

// ---------- 滚动摘要（上下文智能化） ----------
// 痛点：客户端每回合回传全部历史，此前只做"留最近 40 条"的硬裁剪——最早的用户目标、
// 已确认的关键操作会被无感截断，长对话里模型显得健忘、反复确认。
// 方案：历史超过 aiSummarizeAfter 条时，把较早部分压缩成一条要点备忘放在开头，
// 最近 aiSummarizeKeep 条原样保留。摘要结果按"前缀指纹"缓存 + 边界按 aiSumQuantum
// 条量化，保证摘要调用频率被压到约每 3 个用户回合一次，而不是每条消息都多打一次上游。

const (
	aiSummarizeAfter = 24 // 历史超过该条数才启用摘要
	aiSummarizeKeep  = 12 // 摘要后最近原样保留的条数
	aiSumQuantum     = 6  // 摘要边界量化粒度（条）
)

var (
	aiSumMu    sync.Mutex
	aiSumCache struct {
		key  string
		text string
	}
)

// aiSummaryCut 返回摘要切分点：<=0 表示无需摘要。
// 边界量化是缓存的关键：cut 随消息增长每回合 +1~2，若直接做切分点，
// 缓存永远不命中、每回合多一次上游调用；对 aiSumQuantum 取整后，
// 只有累计满 6 条新消息边界才前移一次。
func aiSummaryCut(n int) int {
	if n <= aiSummarizeAfter {
		return 0
	}
	cut := n - aiSummarizeKeep
	return cut - cut%aiSumQuantum
}

// aiHistoryKey 历史前缀指纹（FNV-1a）：头部内容不变 → 摘要缓存命中。
func aiHistoryKey(head []aiMsg) string {
	h := fnv.New32a()
	for _, m := range head {
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(m.Content))
		h.Write([]byte{0})
	}
	return fmt.Sprint(h.Sum32())
}

// aiMaybeSummarize 把 msgs 的较早部分替换成一条摘要消息；摘要失败时静默降级为原始历史
// （宁可上下文长一点，也不能因为摘要挂掉而且回合）。
func aiMaybeSummarize(msgs []aiMsg) []aiMsg {
	cut := aiSummaryCut(len(msgs))
	if cut <= 0 {
		return msgs
	}
	head := msgs[:cut]
	key := aiHistoryKey(head)
	aiSumMu.Lock()
	cached := aiSumCache.key == key
	text := aiSumCache.text
	aiSumMu.Unlock()
	if !cached {
		var err error
		text, err = aiSummarizeHistory(head)
		if err != nil {
			warn(scSys, "AI", "对话摘要生成失败，本回合按原始历史继续：%v", err)
			return msgs
		}
		aiSumMu.Lock()
		aiSumCache.key, aiSumCache.text = key, text
		aiSumMu.Unlock()
		info(scSys, "AI", "已生成对话摘要（压缩 %d 条 → %d 字），长对话上下文已续上", cut, len([]rune(text)))
	}
	out := make([]aiMsg, 0, len(msgs)-cut+1)
	out = append(out, aiMsg{Role: "user", Content: "（系统注入的此前对话要点备忘，不是用户发言，无需回应）\n<untrusted_data>" + text + "</untrusted_data>"})
	out = append(out, msgs[cut:]...)
	return out
}

// aiSummarizeHistory 用当前配置的模型把历史头部压缩成要点备忘（非流式，一次调用）。
func aiSummarizeHistory(head []aiMsg) (string, error) {
	var b strings.Builder
	b.WriteString("以下是 EnvKit 助手与用户的对话历史。请压缩成不超过 400 字的要点备忘，必须保留：\n" +
		"1) 用户的目标与明确要求；\n" +
		"2) 已执行/已确认/被用户拒绝的操作及其结果（保留关键文件名、路径、版本号、报错关键词）；\n" +
		"3) 助手尚未兑现的承诺或未完成的待办。\n" +
		"直接输出要点列表，不要评论，不要提问。\n\n<untrusted_data>\n")
	n := 0
	for _, m := range head {
		c := strings.TrimSpace(m.Content)
		if c == "" || (m.Role != "user" && m.Role != "assistant") {
			continue
		}
		if m.Role == "user" {
			b.WriteString("用户：")
		} else {
			b.WriteString("助手：")
		}
		b.WriteString(firstLines(c, 400))
		b.WriteString("\n")
		n++
		if n >= 40 {
			break
		}
	}
	b.WriteString("\n</untrusted_data>")
	out, err := aiCallLLM([]aiMsg{{Role: "user", Content: b.String()}}, nil)
	if err != nil {
		return "", err
	}
	return firstLines(strings.TrimSpace(out), 1200), nil
}

// aiToolResultAsUser 把工具结果包装成一条用户消息（供模型基于结果作答）
func aiToolResultAsUser(tool, result string) aiMsg {
	return aiMsg{Role: "user", Content: "[工具 " + tool + " 已执行，结果如下]\n" + result +
		"\n\n请基于以上结果用简体中文简要回答用户（不要罗列选项菜单，不要重复问我需要什么）。"}
}

// aiLooksLikeMenu 判断模型是否给出了"菜单 / 反问"式无效回复。
// 表现：拿到了工具结果却只罗列能力、反问用户要做什么。
func aiLooksLikeMenu(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	// 单一动作主题的问句是"提议下一步"（"需要我帮你备份数据库吗？"），不是菜单：
	// 屏蔽它会导致用户回"好/需要"时上下文断裂、提议永远无法被确认。
	// 多选项菜单（≥2 个动作主题）不享受此豁免。
	if t := strings.TrimSpace(text); (strings.HasSuffix(t, "？") || strings.HasSuffix(t, "?")) && aiActionTopicCount(t) == 1 {
		return false
	}
	pats := []string{`需要我`, `可以直接说`, `请问需要`, `请告诉我`, `回复序号`, `你可以让我`,
		`我能帮`, `请回复`, `要不要我`, `如果需要我`, `请直接说`, `等待你的指示`, `请问您`,
		`直接说需求`, `说需求即可`, `不用问我在不在`, `需要的话直接说`,
		// 承诺式空转（"给我一句明确指令，我立即发起工具调用"）：只保留不会出现在正常结果汇报里的词
		`我立即发起`, `我就发起`, `发起对应工具调用`, `明确指令`, `给我一句`, `点名启动`}
	for _, pt := range pats {
		if strings.Contains(text, pt) {
			return true
		}
	}
	// 3 个以上编号项：仅当同时出现菜单/反问语汇、或涉及 ≥2 个动作主题时才判定为菜单。
	// 纯编号总结（"1. Go 已安装 2. Node 已安装 3. MySQL 正常"）是合法回复，不能误杀。
	// 正则须同时覆盖行首编号与行内编号（"1. 启动服务 2. 备份数据库 3. 查看日志"），
	// (?:^|\s) + 最多两位数字可排除 127.0.0.1、8.0 这类数字串误匹配。
	if n := reOf(`(?:^|\s)\d{1,2}[\.、)]`).FindAllString(text, -1); len(n) >= 3 {
		t := strings.TrimSpace(text)
		askVocab := strings.Contains(text, "？") || strings.Contains(text, "?") ||
			strings.Contains(text, "请选择") || strings.Contains(text, "需要哪个") || strings.Contains(text, "要哪个")
		if askVocab || aiActionTopicCount(t) >= 2 {
			return true
		}
	}
	return false
}

// aiToolCnName 工具名中文说明（用于确认卡片与提示语）
func aiToolCnName(tool string, args map[string]any) string {
	switch tool {
	case "start_service":
		svc, _ := args["service"].(string)
		if svc == "backend" {
			return "启动后端（go build + go run）"
		}
		if svc == "all" {
			return "启动前后端服务（先后端、再前端）"
		}
		return "启动前端（npm run serve）"
	case "stop_service":
		if svc, _ := args["service"].(string); svc == "all" {
			return "停止前后端服务"
		}
		if svc, _ := args["service"].(string); svc == "backend" {
			return "停止后端服务"
		}
		return "停止前端服务"
	case "restart_service":
		svc, _ := args["service"].(string)
		if svc == "backend" {
			return "重启后端服务（先停旧进程再启动）"
		}
		if svc == "all" {
			return "重启前后端服务（先停后起，先后端再前端）"
		}
		return "重启前端服务（先停旧进程再启动）"
	case "db_backup":
		return "备份数据库（mysqldump）"
	case "run_detection":
		return "重新检测组件"
	case "apply_whitelist":
		return "加入 Defender 白名单"
	case "get_logs":
		return "读取日志"
	case "get_diag_report":
		return "生成诊断报告"
	case "db_check":
		return "检测数据库连接"
	case "cleanup_processes":
		return "清理后台进程"
	case "get_system_state":
		return "读取环境状态"
	case "get_project_brief":
		return "读取项目画像"
	case "list_project":
		return "查看项目目录"
	case "search_files":
		return "在项目内搜索文件"
	case "read_file":
		return "读取项目文件"
	}
	return tool
}

// aiLooksLikeMenuPrefix 首句判定：这些开场白基本可确定是"自我介绍/念菜单"式无效回复
func aiLooksLikeMenuPrefix(s string) bool {
	if aiLooksLikeMenu(s) {
		return true
	}
	for _, pt := range []string{`你好，我是`, `你好！我是`, `我是 EnvKit`, `EnvKit 助手`, `EnvKit 运维助手`,
		`助手在线`, `助手已就绪`, `助手就绪`, `直接说需求`, `直接说即可`, `有需要直接说`, `需要我做什么`} {
		if strings.Contains(s, pt) {
			return true
		}
	}
	return false
}

// aiIsOptInTool 必须由用户明确要求才允许执行的工具（模型不得主动发起）。
//
// v2.2：改为直接查询 PolicyGate，**消除双源**。原先这里是硬编码的 switch，
// 加上 PolicyGate 后两套名单开始漂移——单测抓到 manage_memories 只在
// PolicyGate 里是 opt-in（记忆会长期影响后续所有对话，模型不该自作主张记住东西），
// 而这里漏了。两处各写一份的代价就是这种不一致迟早发生。
//
// 注意 stop_service 不在此列（v1.9.7）：它是写工具，本来就要过确认卡片这道闸，
// opt-in 拦截反而把用户"停一下前端"这类说法（关键词匹配不到）整轮挡死，
// 模型还会每轮重试形成拦截刷屏——有确认卡片兜底就够了。
func aiIsOptInTool(tool string) bool {
	return PolicyGate(tool, "", actAI).OptIn
}

// aiUserAskedFor 最近 3 条用户消息里是否明确提到该工具对应的事项
func aiUserAskedFor(tool string, msgs []aiMsg) bool {
	kws := map[string][]string{
		"apply_whitelist": {"白名单", "杀软", "defender", "Defender", "秒退", "拦截"},
		"db_backup":       {"备份", "dump", "导出数据库"},
		"stop_service":    {"停止", "停掉", "关掉", "关了"},
	}
	list, ok := kws[tool]
	if !ok {
		return true
	}
	seen := 0
	for i := len(msgs) - 1; i >= 0 && seen < 3; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		seen++
		for _, k := range list {
			if strings.Contains(msgs[i].Content, k) {
				return true
			}
		}
	}
	return false
}

// aiTurnTimeout 单轮上游最长等待：共享 client（httpStream）不设全局超时，
// 由这里的计时器兜底，避免上游假死时永久挂住（此前是靠 client 的 120s 超时）。
const aiTurnTimeout = 6 * time.Minute

// aiSleepCtx 可被客户端断开打断的退避等待；返回 false 表示上下文已取消
func aiSleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// aiUsage 一次回合的 token 用量（多轮工具调用累加）。HasCache=上游是否报了缓存细分
// （DeepSeek 专有字段）；自定义模型通常只有总 token 甚至没有 usage。
type aiUsage struct {
	Prompt     int64 `json:"prompt_tokens"`
	Completion int64 `json:"completion_tokens"`
	HasCache   bool  `json:"has_cache"`
	Hit        int64 `json:"cache_hit"`
	Miss       int64 `json:"cache_miss"`
}

// sseDoneUsage / sseDoneConfirm：回合收尾。有 usage 就在 done 前补一条独立 usage 事件
// （前端据此在回复尾部显示 token 统计并累计到桌宠"今日统计"），没有就只发 done。
func sseDoneUsage(w http.ResponseWriter, fl http.Flusher, u *aiUsage) {
	if u.Prompt > 0 || u.Completion > 0 {
		sseWrite(w, fl, map[string]any{"type": "usage", "usage": *u})
	}
	sseWrite(w, fl, map[string]any{"type": "done"})
}

func sseDoneConfirm(w http.ResponseWriter, fl http.Flusher, u *aiUsage) {
	if u.Prompt > 0 || u.Completion > 0 {
		sseWrite(w, fl, map[string]any{"type": "usage", "usage": *u})
	}
	sseWrite(w, fl, map[string]any{"type": "done", "reason": "awaiting_confirm"})
}

func aiRunLoop(ctx context.Context, w http.ResponseWriter, fl http.Flusher, msgs []aiMsg, confirmed bool, preflight bool, lang string) {
	usage := aiUsage{} // 本回合 token 用量累计（跨多轮工具调用）
	// 预检快通道：不依赖模型，确定性识别明确指令并直接执行（写操作仍走确认卡片）。
	// 这样即使模型"只会念菜单"，用户的明确需求也一定会被执行。
	preChecked := false
	menuRetried := 0
	optInBlocked := 0 // opt-in 工具拦截计数：同一轮 ≥3 次判定模型死循环，终止并给可见提示
	paramFix := 0     // 参数自适应重试次数（temperature / token 字段 / tools 各最多一次）
	netRetry := 0     // 瞬时故障重试次数（网络错误 / 429 限流 / 5xx，指数退避 1s→3s）
	if !confirmed && preflight {
		lastUser := ""
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				lastUser = strings.TrimSpace(msgs[i].Content)
				break
			}
		}
		cand := lastUser
		if n := aiResolveNumberInput(lastUser, msgs); n != "" {
			cand = n
		}
		if tool, args, isWrite := aiBacktrackIntent(cand, msgs); tool != "" {
			preChecked = true
			info(scSys, "AI", "预检命中：%q \u2192 %s（写操作=%v）", firstLines(lastUser, 30), tool, isWrite)
			callID := fmt.Sprintf("intent_%d", time.Now().UnixNano())
			if isWrite {
				// 先给一句文字反馈，避免用户只看到卡片以为没反应
				sseWrite(w, fl, map[string]any{"type": "delta", "text": "好的，准备执行：" + aiToolCnName(tool, args) + "。请在下方点「确认执行」。\n"})
				sseWrite(w, fl, map[string]any{"type": "confirm_request", "tool_call_id": callID, "tool": tool, "args": args,
					"reason": "intent", "text": aiToolCnName(tool, args)})
				sseDoneConfirm(w, fl, &usage)
				return
			}
			// 读工具：直接执行，结果交给模型做自然语言总结
			res, exErr := aiToolRegistry[tool].Execute(args)
			resText := res
			if exErr != nil {
				resText = "执行出错：" + exErr.Error()
			}
			sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": tool, "result": firstLines(resText, 300)})
			// 用 user 消息承载工具结果（避免合成 assistant+tool 触发 thinking 模型 400）
			msgs = append(msgs, aiToolResultAsUser(tool, firstLines(resText, 3000)))
		}
	}
	for turn := 0; turn < 8; turn++ {
		// 客户端（页面）已关闭/刷新：立即停止，不再向上游要 token
		if clientGone(ctx, w) {
			warn(scSys, "AI", "客户端已断开，停止本轮对话")
			return
		}
		// 统一按模型怪癖构造请求体（temperature / token 字段 / tools 均可自适应）。
		// 缓存友好布局：system 只放静态提示词+语言指令（会话内逐字节稳定，前缀可命中）；
		// 动态环境快照挪到对话末尾追加——含区块高度/服务 PID 等易变数据，放在开头会
		// 让 DeepSeek 前缀缓存在 system 处即分叉，导致全部对话历史反复重算（命中率 60~70%）。
		acTurn := aiSnap()
		full := make([]aiMsg, 0, len(msgs)+2)
		full = append(full, aiMsg{Role: "system", Content: aiSystemPrompt + aiLangDirective(lang)})
		full = append(full, msgs...)
		// 任务提示在循环外算一次：msgs 会随工具结果增长，但用户的原始诉求不变，
		// 每轮重算只会让记忆匹配抖动（同一问题匹配到不同记忆）。
		taskHint := aiTaskHint(msgs)
		full = append(full, aiMsg{Role: "user", Content: "（系统注入的当前环境快照，仅供你了解最新状态，不是用户发言，无需回应）\n<env_state>" + aiHealthSnapshotFor(taskHint) + "</env_state>"})
		payload := aiPayload(full, aiToolDefs(), acTurn.quirkRead(), -1, true)
		b, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, "POST", acTurn.endpoint(), strings.NewReader(string(b)))
		if err != nil {
			sseWrite(w, fl, map[string]any{"type": "error", "text": err.Error()})
			sseDoneUsage(w, fl, &usage)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+acTurn.keyPlain())
		resp, err := httpStream.Do(req)
		if err != nil {
			// 网络抖动/DNS 抖动等瞬时错误：退避后重试（429/5xx 同理，见下方状态码分支）
			if netRetry < 2 && ctx.Err() == nil {
				netRetry++
				d := time.Second
				if netRetry > 1 {
					d = 3 * time.Second
				}
				warn(scSys, "AI", "上游请求失败（%v），%s 后重试（第 %d/2 次）", err, d, netRetry)
				if !aiSleepCtx(ctx, d) {
					return
				}
				continue
			}
			sseWrite(w, fl, map[string]any{"type": "error", "text": "AI 服务连接失败：" + err.Error()})
			sseDoneUsage(w, fl, &usage)
			return
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
			resp.Body.Close()
			msg := firstLines(strings.TrimSpace(string(b)), 300)
			// 参数自适应：temperature 被拒 → 改 1 → 再不行就不发；token 字段/工具不支持同理
			if paramFix < 2 {
				if newQ, changed, hint := aiFixParamError(msg); changed {
					paramFix++
					aiMutate(func(a *AIConfig) { a.setQuirk(newQ); saveExternalConfig(cfg) })
					warn(scSys, "AI", "%s（模型 %s）：%s", hint, acTurn.Model, firstLines(msg, 120))
					continue
				}
			}
			// 429 限流 / 5xx 服务端故障：瞬时性问题，退避重试而不是直接把错误抛给用户
			if resp.StatusCode == 429 || resp.StatusCode >= 500 {
				if netRetry < 2 && ctx.Err() == nil {
					netRetry++
					d := time.Second
					if netRetry > 1 {
						d = 3 * time.Second
					}
					warn(scSys, "AI", "上游返回 HTTP %d，%s 后重试（第 %d/2 次）", resp.StatusCode, d, netRetry)
					if !aiSleepCtx(ctx, d) {
						return
					}
					continue
				}
			}
			sseWrite(w, fl, map[string]any{"type": "error", "text": fmt.Sprintf("AI 服务返回 HTTP %d：%s", resp.StatusCode, msg)})
			sseDoneUsage(w, fl, &usage)
			return
		}

		contentB := strings.Builder{}
		reasonB := strings.Builder{}
		streamOpen := false        // 是否已开始向界面推送正文
		holdMenu := false          // 本 turn 判定为菜单式无效回复 → 整段丢弃
		holdB := strings.Builder{} // 首句缓冲区：先判定是不是菜单，再决定是否推送
		acc := map[int]*aiAccTool{}
		finish := ""
		upErr := ""

		// 客户端断开时立刻关闭上游响应体：解除 Scanner 阻塞（上游安静时也能马上停），
		// 避免"用户已关页面、后台还在等模型吐字"的白烧 token。
		// 上游按行读取放到独立 goroutine，主循环 select 三路：客户端断开 / 心跳 / 上游行。
		// ① 写入只发生在一个 goroutine（无竞态）；② 心跳写失败即代表页面已关闭 → 实时中断；
		// ③ 心跳同时可防止反向代理/中间层的空闲断连。
		var readErr error
		lineCh := make(chan string, 128)
		go func() {
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				lineCh <- sc.Text()
			}
			readErr = sc.Err() // 先写后 close：读侧在 channel 关闭后读取，channel 提供 happens-before
			close(lineCh)
		}()

		rc := http.NewResponseController(w)
		ticker := time.NewTicker(10 * time.Second)
		turnTimer := time.NewTimer(aiTurnTimeout) // 单轮上游最长等待（见 aiTurnTimeout 注释）
		stopTimers := func() { ticker.Stop(); turnTimer.Stop() }
	reading:
		for {
			var line string
			select {
			case <-ctx.Done():
				stopTimers()
				resp.Body.Close()
				warn(scSys, "AI", "客户端已断开，已中断本轮上游流式响应")
				return
			case <-turnTimer.C:
				stopTimers()
				resp.Body.Close()
				warn(scSys, "AI", "上游 %s 内未返回任何数据，已中断本轮请求（可换更快的模型或检查网络/代理）", aiTurnTimeout)
				sseWrite(w, fl, map[string]any{"type": "error", "text": fmt.Sprintf("AI 上游超时（%s 无数据），已中断本轮", aiTurnTimeout)})
				sseDoneUsage(w, fl, &usage)
				return
			case <-ticker.C:
				// 心跳：既保持长连接，也用写失败来判定"页面已关闭"（Flusher 本身不返回错误）
				_, werr := io.WriteString(w, ": ping\n\n")
				if werr == nil {
					werr = rc.Flush()
				}
				if werr != nil {
					stopTimers()
					resp.Body.Close()
					warn(scSys, "AI", "客户端已断开（心跳写入失败：%v），已中断本轮上游流式响应", werr)
					return
				}
				continue
			case l, ok := <-lineCh:
				if !ok {
					break reading
				}
				line = l
			}
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}
			var ev struct {
				Usage *struct {
					PromptTokens          int64 `json:"prompt_tokens"`
					CompletionTokens      int64 `json:"completion_tokens"`
					PromptCacheHitTokens  int64 `json:"prompt_cache_hit_tokens"`
					PromptCacheMissTokens int64 `json:"prompt_cache_miss_tokens"`
					PromptTokensDetails   *struct {
						CachedTokens int64 `json:"cached_tokens"`
					} `json:"prompt_tokens_details"`
				} `json:"usage"`
				Choices []struct {
					Delta struct {
						Content   string `json:"content"`
						ToolCalls []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
						ReasoningContent string `json:"reasoning_content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			if ev.Error != nil {
				upErr = ev.Error.Message
				break
			}
			if ev.Usage != nil {
				usage.Prompt += ev.Usage.PromptTokens
				usage.Completion += ev.Usage.CompletionTokens
				// 缓存细分：DeepSeek 专用字段优先，兼容 OpenAI 风格 prompt_tokens_details.cached_tokens
				if ev.Usage.PromptCacheHitTokens > 0 || ev.Usage.PromptCacheMissTokens > 0 {
					usage.Hit += ev.Usage.PromptCacheHitTokens
					usage.Miss += ev.Usage.PromptCacheMissTokens
				} else if ev.Usage.PromptTokensDetails != nil && ev.Usage.PromptTokensDetails.CachedTokens > 0 {
					usage.Hit += ev.Usage.PromptTokensDetails.CachedTokens
					if ev.Usage.PromptTokens > ev.Usage.PromptTokensDetails.CachedTokens {
						usage.Miss += ev.Usage.PromptTokens - ev.Usage.PromptTokensDetails.CachedTokens
					}
				}
				usage.HasCache = usage.Hit > 0 || usage.Miss > 0
			}
			if len(ev.Choices) == 0 {
				continue
			}
			ch := ev.Choices[0]
			if ch.Delta.ReasoningContent != "" {
				reasonB.WriteString(ch.Delta.ReasoningContent)
				sseWrite(w, fl, map[string]any{"type": "reasoning", "text": ch.Delta.ReasoningContent})
			}
			if ch.Delta.Content != "" {
				contentB.WriteString(ch.Delta.Content)
				if streamOpen {
					sseWrite(w, fl, map[string]any{"type": "delta", "text": ch.Delta.Content})
				} else if !holdMenu {
					holdB.WriteString(ch.Delta.Content)
					hs := holdB.String()
					decided := strings.ContainsAny(hs, "。！？\n") || len([]rune(hs)) >= 60
					if decided {
						if aiLooksLikeMenuPrefix(hs) {
							holdMenu = true
						} else {
							streamOpen = true
							sseWrite(w, fl, map[string]any{"type": "delta", "text": hs})
							holdB.Reset()
						}
					}
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				a := acc[tc.Index]
				if a == nil {
					a = &aiAccTool{}
					acc[tc.Index] = a
				}
				if tc.ID != "" {
					a.ID = tc.ID
				}
				if tc.Function.Name != "" {
					a.Name += tc.Function.Name
				}
				a.ArgsB.WriteString(tc.Function.Arguments)
			}
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
		}
		stopTimers()
		resp.Body.Close()
		if clientGone(ctx, w) {
			warn(scSys, "AI", "客户端已断开，已中断本轮上游流式响应")
			return
		}
		if readErr != nil {
			upErr = readErr.Error()
		}
		if upErr != "" {
			sseWrite(w, fl, map[string]any{"type": "error", "text": "AI 服务返回错误：" + upErr})
			sseDoneUsage(w, fl, &usage)
			return
		}

		// turn 结束：一直没达到推送条件但内容有效（短回答）时补推
		if !streamOpen && !holdMenu && holdB.Len() > 0 {
			streamOpen = true
			sseWrite(w, fl, map[string]any{"type": "delta", "text": holdB.String()})
			holdB.Reset()
		}

		// 无工具调用：先做确定性意图兜底（模型不发起调用时保证"说了就执行"），否则正常结束
		if finish != "tool_calls" && len(acc) == 0 {
			// 把本轮助手回复记入本地 msgs：若它是"需要我帮你…吗？"式提议，
			// 下一轮用户回"好/需要"时兜底才能从这条提议里提取动作。
			if contentB.Len() > 0 {
				msgs = append(msgs, aiMsg{Role: "assistant", Content: contentB.String(), ReasoningContent: reasonB.String()})
			}
			lastUser := ""
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == "user" {
					lastUser = msgs[i].Content
					break
				}
			}
			// 确认续接轮（最后一条是 tool 结果）不做兜底，避免重复触发
			if lastUser != "" && !confirmed && !preChecked && preflight {
				tool, args, isWrite := aiBacktrackIntent(lastUser, msgs)
				if tool != "" {
					callID := fmt.Sprintf("intent_%d", time.Now().UnixNano())
					if isWrite {
						sseWrite(w, fl, map[string]any{"type": "confirm_request", "tool_call_id": callID, "tool": tool, "args": args})
						sseDoneConfirm(w, fl, &usage)
						return
					}
					res, exErr := aiToolRegistry[tool].Execute(args)
					resText := res
					if exErr != nil {
						resText = "执行出错：" + exErr.Error()
					}
					sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": tool, "result": firstLines(resText, 300)})
					msgs = append(msgs, aiToolResultAsUser(tool, firstLines(resText, 3000)))
					continue // 让模型基于工具结果作答
				}
			}
			// 菜单/反问式无效回复：丢弃该段（用户看不到）并强令重答（最多 2 次）
			if menuRetried < 2 && (holdMenu || aiLooksLikeMenu(contentB.String())) {
				menuRetried++
				warn(scSys, "AI", "检测到菜单式无效回复（第 %d 次，已屏蔽不显示），要求模型重新作答", menuRetried)
				msgs = append(msgs, aiMsg{Role: "user", Content: "（系统强制要求）你上一条回复无效：禁止罗列能力清单、禁止编号菜单、禁止反问用户。" +
					"现在必须直接给出结论：如果上面已有工具结果，就基于它总结现状与建议；" +
					"如果需要执行操作，就立即发起对应工具调用，不要先问。"})
				continue
			}
			if holdMenu {
				warn(scSys, "AI", "菜单式回复无法纠正，改用确定性提示")
				sseWrite(w, fl, map[string]any{"type": "delta", "text": "没理解你的需求。可以直接说：「启动服务」「备份数据库」「查看日志」「检测数据库连接」。"})
			}
			sseDoneUsage(w, fl, &usage)
			return
		}

		// 有工具调用：逐个处理
		msgs = append(msgs, aiMsg{Role: "assistant", Content: contentB.String(), ReasoningContent: reasonB.String()})
		assistantIdx := len(msgs) - 1
		toolMsgs := []aiMsg{}

		for i := 0; i < len(acc); i++ {
			a, ok := acc[i]
			if !ok {
				continue
			}
			var args map[string]any
			argsStr := strings.TrimSpace(a.ArgsB.String())
			if argsStr == "" {
				argsStr = "{}"
			}
			if json.Unmarshal([]byte(argsStr), &args) != nil {
				args = map[string]any{}
			}
			def, known := aiToolRegistry[a.Name]
			toolMsg := aiMsg{Role: "tool", ToolCallID: a.ID}
			// v2.2 PolicyGate：先问策略再动手。forbidden 让模型当场放弃，
			// 而不是发出去被执行层拒绝——那样它会以为"再换个参数试试"，
			// 白花 token 且可能反复试探。
			verdict := PolicyGate(a.Name, policyTargetOf(a.Name, args), actAI)
			switch {
			case !known:
				toolMsg.Content = "错误：该工具不在白名单内"
				sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": a.Name, "result": toolMsg.Content})
			case verdict.Level == PolicyForbidden:
				policyRecord(verdict, "denied")
				msg := policyAIView(verdict)
				warn(scSys, "AI", "策略拒绝 %s：%s", a.Name, verdict.Reason)
				sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": a.Name, "result": msg})
				msgs = append(msgs, aiMsg{Role: "user", Content: "（系统）" + msg})
				continue
			case verdict.OptIn && !aiUserAskedFor(a.Name, msgs):
				// "需用户明确要求"的工具（如加白名单）：模型不能自作主张发起，丢弃并要求直接回答。
				// 同一轮拦截 ≥3 次说明模型在死循环重试，直接终止并给用户可见提示，避免日志刷屏。
				optInBlocked++
				warn(scSys, "AI", "已拦截模型未经要求发起的 %s 调用（第 %d 次）", a.Name, optInBlocked)
				if optInBlocked >= 3 {
					hint := "这个操作需要你明确说一句（例如「备份数据库」「加白名单」）我才会发起；也可以直接用界面上对应的按钮。"
					if lang == "en" {
						hint = "This action needs your explicit request (e.g. \"back up the database\", \"add to Defender whitelist\"); or use the matching button in the UI."
					}
					sseWrite(w, fl, map[string]any{"type": "delta", "text": "\n" + hint + "\n"})
					sseDoneUsage(w, fl, &usage)
					return
				}
				msgs = append(msgs, aiMsg{Role: "user", Content: "（系统）不要发起用户没有要求的操作。请直接回答用户刚才的问题。"})
				continue
			case def.Write:
				// 写工具：转确认流程，本轮结束，等用户在 UI 确认后带 confirm 重新提交
				sseWrite(w, fl, map[string]any{"type": "confirm_request", "tool_call_id": a.ID, "tool": a.Name, "args": args,
					"text": aiToolCnName(a.Name, args)})
				// 把未完成的 assistant 消息保留在本地 msgs（供确认后续接），并结束本次 SSE
				msgs[assistantIdx].ToolCalls = append(msgs[assistantIdx].ToolCalls, func() aiToolCall {
					var t aiToolCall
					t.ID = a.ID
					t.Type = "function"
					t.Function.Name = a.Name
					t.Function.Arguments = argsStr
					return t
				}())
				_ = toolMsg
				sseDoneConfirm(w, fl, &usage)
				return
			default:
				res, exErr := def.Execute(args)
				toolMsg.Content = res
				if exErr != nil {
					toolMsg.Content = "执行出错：" + exErr.Error()
				}
				sseWrite(w, fl, map[string]any{"type": "tool_result", "tool": a.Name, "result": firstLines(toolMsg.Content, 300)})
			}
			msgs[assistantIdx].ToolCalls = append(msgs[assistantIdx].ToolCalls, func() aiToolCall {
				var t aiToolCall
				t.ID = a.ID
				t.Type = "function"
				t.Function.Name = a.Name
				t.Function.Arguments = argsStr
				return t
			}())
			toolMsgs = append(toolMsgs, toolMsg)
		}
		msgs = append(msgs, toolMsgs...)
		// 继续下一轮（模型消化工具结果）
	}
	sseWrite(w, fl, map[string]any{"type": "error", "text": "已达到单次任务的工具调用轮数上限（8），请拆分请求或重新提问"})
	sseDoneUsage(w, fl, &usage)
}
