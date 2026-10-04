package main

// ai_intent.go —— 意图判定：从用户的自然语言里认出"要做什么"
//
// 从 ai_loop.go 拆出。这块是**确定性匹配**（关键词、数字、菜单形态），
// 不依赖模型——它的价值恰恰在于模型失灵或答非所问时仍能工作。
// 早先 v1.x 有过"AI 只会念菜单"的毛病，这层就是那次事故的产物。
//
// 放在独立文件还有个好处：改关键词时不会碰到主循环，
// 而主循环里每一行都关系到 token 消耗与中止时机。

import (
	"strings"
)

// aiBareAffirm 用户只回了表示同意/催促的词（"好""嗯""可以""确认""需要"等）。
// 这类输入无法确定性映射到具体操作，需配合 aiBacktrackIntent 回溯上下文。
func aiBareAffirm(t string) bool {
	return reOf(`^(好|好的|好呀|好吧|好嘞|嗯|嗯嗯|行|行吧|可以|是的|对|确认|确定|开始|开始吧|继续|麻烦了|辛苦了|需要|需要呀|需要吧|需要滴|要|要吧|要的|当然|当然可以|走起|执行吧|就这么办|go|ok|OK)[吧呢呀啊~！!。.]*$`).MatchString(t)
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
	// ---- 备份：先判「查询/检查类」 ----
	//
	// v2.5 修复的真实缺陷（评测抓到，四次注入实验全部复现）：
	// 用户问「检查一下最近的备份文件，看看能不能用」，
	// 本条规则命中的是 `strings.Contains(t, "备份")` → 直接创建备份，
	// **AI 一次工具都没调就要执行 mysqldump**。
	//
	// 危害不是"多做了一次备份"，而是**会覆盖问题现场**：
	// 损坏的备份被新备份覆盖后，就再也查不出"当时到底坏在哪"。
	//
	// ## 位置很关键：必须排在「启动/停止」之前
	//
	// 踩过一次：这段原本排在启动分支之后，
	// 于是「最近那份备份能真的还原吗」被上面 `(启动|运行|拉起|起|开)`里的
	// 裸「起」字命中 → 判成 **start_service**（写操作）。
	// 问"能不能还原"却要去启动服务，比判成备份更离谱。
	// 查询类诉求的共同特征是**有"能不能/是否/检查"这类疑问词**，
	// 它的优先级必须高于任何具体动作词。
	case has(`(备份).{0,10}(文件|那?份|哪些|列表|在哪|什么时候|多久) ?(完整|可用|能不能|能不能用|能还原|能否还原|情况|状态)`) ||
		has(`(检查|查看|看看|查一下|查询|验证|确认|测试|评估).{0,6}备份`) ||
		has(`备份.{0,12}(完整吗|完整不完整|能用吗|能不能用|能还原|能否还原|可不可用|怎么样)`) ||
		has(`(最近|上次|上一次|那份|这个|现有).{0,6}备份.{0,10}(怎么样|如何|怎样|吗|呢|能不能|能不能用)`) ||
		has(`能不能.{0,4}还原`) || has(`能否.{0,4}还原`) || has(`可不可.{0,4}还原`) ||
		// 纯查询式：只提到"备份"这个名词 + 一个疑问/列举动作，没有"做备份"的动词。
		// 覆盖「备份在哪」「有哪些备份」「备份都有啥」这类最短的说法——
		// 它们不带上面任何修饰词，靠单一正则很难穷尽，所以单独兜一层。
		has(`(备份).{0,4}(在哪|有哪些|都有啥|都有哪些|什么|多久了|什么时候|哪些能|能用吗)`):
		// 只读查询：列出现有备份 + 体检结果。
		// **刻意不是 get_system_state** —— 它不含任何备份信息，
		// 模型看不到备份就没法判断，拿到「只能创建」的选项时
		// 它会选那个有副作用的（这正是 v2.4 评测抓到的缺陷）。
		return "list_backups", map[string]any{}, false
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
	// ---- 备份（创建类；查询/检查类已在文件开头拦下） ----
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
