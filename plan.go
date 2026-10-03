package main

// plan.go —— v2.3 N5：Plan 层与工具调用预算
//
// ## 要解决的真实事故
//
// 用户反馈：「我在问一个大问题时，他找了很多次文件，导致超出工具 8 次调用上限」。
//
// 固定 `for turn := 0; turn < 8; turn++` 有三个问题，一个比一个严重：
//
//  1. **静默截断**：达到上限后直接退出循环，用户看到的是一段没说完的话，
//     既不知道"到此为止"也不知道"为什么"。这与 v2.0 修掉的"谎报"同源——
//     **没把失败说成失败**。
//  2. **一刀切**：问"这个项目怎么跑起来"和问"端口被占是谁占的"用同一个上限。
//     前者天然需要多轮探索（列目录→搜文件→读配置→推断），后者一轮就够。
//  3. **看不到规划**：用户不知道 AI 打算做什么，只能等它转完 8 圈。
//
// ## 关键判断：Plan 要"轻"，不能变成审批流
//
// 完整的 Plan 层（生成计划 → 等用户批准 → 逐步执行 → Re-plan）是 v2.2 规划的
// 大工程。但本项刻意**只做两件最有用的事**：
//
//  1. **展示**：AI 每回合的工具调用序列以"正在做什么"的形式流给用户。
//     用户看到"正在搜索 xxx"就知道它在干吗，而不是卡住了。
//  2. **预算**：按任务类型给不同的工具调用次数，而不是固定 8。
//
// **刻意不做"计划审批"**：那会让每个简单问题都多一次点击，
// 而 EnvKit 的定位是"AI 增强本地能力，不是取代"——
// 用户随时可以自己点按钮，不该被 AI 的计划卡住。
// 真正的多步编排（Re-plan）等 v2.2 的 Plan 层完整落地。
//
// ## 预算怎么定
//
// 按"任务需要探索多少"分档，而不是按字数或模型能力：
//
//	trivial   1 轮  状态查询、单个日志、确认类
//	normal    4 轮  单点排查（端口被占、服务起不来）
//	explore  10 轮  需要读项目代码才能回答的（怎么跑起来、入口在哪、怎么改）
//	deep     16 轮  跨多个子系统的分析
//
// 探索类给得宽是因为它们**读的都是有用信息**：多一轮就多一条事实。
// 而非探索类给得紧，是因为多轮往往意味着模型在打转。
//
// 判档只看"用户诉求的性质"，不猜意图——猜错方向的代价（多烧 token
// 或过早截断）都比保守估一次大。

import (
	"fmt"
	"strings"
)

// ===== 工具调用预算 =====

// turnBudget 一次任务允许的 AI 回合（= 模型请求轮次）上限。
type turnBudget struct {
	Max    int    // 上限
	Reason string // 给用户看的理由
}

// budgetFor 按任务性质选预算。
//
// 判据是"回答这个问题需不需要读项目代码"，这是唯一可从用户诉求文本
// 可靠推断的性质（关键词匹配），比猜复杂度稳。
func budgetFor(task string) turnBudget {
	t := strings.ToLower(task)
	// 深分析：明确要看数据/多个子系统
	deep := containsAny(t, "分析", "统计", "对比", "分别", "所有", "每个", "趋势", "分布")
	// 探索：需要读项目才能答
	explore := containsAny(t, "怎么跑", "如何跑", "怎么启动", "如何启动", "入口",
		"在哪", "哪个文件", "怎么改", "为什么", "架构", "实现", "逻辑", "流程",
		"依赖", "配置在哪", "读一下", "看一下项目", "熟悉", "梳理")
	// 状态查询：问的是"现在怎样"而不是"为什么"
	status := containsAny(t, "现在", "状态", "健康", "多少", "有没有", "开着", "是否")

	switch {
	case deep && explore:
		return turnBudget{Max: aiTurnDeep, Reason: "跨子系统分析 + 需要读代码"}
	case deep:
		return turnBudget{Max: aiTurnDeep, Reason: "数据分析类问题"}
	case explore && !status:
		return turnBudget{Max: aiTurnExplore, Reason: "需要探索项目才能回答"}
	case status && !explore:
		return turnBudget{Max: aiTurnNormal, Reason: "状态查询类问题"}
	case explore && status:
		return turnBudget{Max: aiTurnExplore, Reason: "既要查状态又要读代码"}
	default:
		return turnBudget{Max: aiTurnNormal, Reason: "常规排查"}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, k := range subs {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// 各档预算。名称带数字是为了让 grep 一步看到全部。
const (
	aiTurnNormal  = 4  // 状态查询、单点排查
	aiTurnExplore = 10 // 需要读项目
	aiTurnDeep    = 16 // 跨子系统分析
	aiTurnMin     = 2  // 下限：再简单也够两轮（问 + 答）
)

// budgetNotice 达上限时给用户的话。
//
// **关键是"说清楚发生了什么"**而不是静默停：用户要知道是预算用完了、
// 不是它不想说了。同时给出可执行的下一步（分几次问 / 缩小范围）。
func budgetNotice(b turnBudget, used int) string {
	return fmt.Sprintf(
		"已达到本轮 %d 次工具调用上限（%s）。\n"+
			"这不是出错，是防止在没边界的探索里空转。\n"+
			"可以这样继续：把问题拆成更小的一块再问一次（我保留了这个会话的上下文），"+
			"或者直接告诉我你已经知道的信息、我从那里接着查。",
		used, b.Reason)
}

// ===== Plan 展示 =====

// planStep 一条"正在做什么"。
type planStep struct {
	Seq    int    `json:"seq"`
	Tool   string `json:"tool"`
	Target string `json:"target,omitempty"`
	Why    string `json:"why,omitempty"`
}

// planTrack 收集本次任务的工具调用序列，供 UI 展示。
//
// 存在 Trace 里而不是独立变量：Trace 已经有一份，这里只是**汇总视图**，
// 不重复记录。
type planTrack struct {
	Steps []planStep
}

func (p *planTrack) add(tool, target, why string) {
	if p == nil {
		return
	}
	if len(p.Steps) >= 40 {
		return // 防止死循环把列表撑爆
	}
	p.Steps = append(p.Steps, planStep{Seq: len(p.Steps) + 1, Tool: tool, Target: target, Why: why})
}

// planLine 把工具调用渲染成一行"正在做什么"。
//
// 刻意用自然语言而不是工具名：用户看到"搜索 xxx"比看到
// "search_files(pattern=xxx)"有用得多。
func planLine(tool, target string) string {
	switch tool {
	case "list_project":
		return "正在查看项目结构"
	case "search_files":
		if target != "" {
			return "正在搜索 " + target
		}
		return "正在搜索项目文件"
	case "read_file":
		return "正在读取 " + target
	case "db_query":
		return "正在查询数据库：" + firstLines(target, 60)
	case "db_list":
		return "正在列出数据库与表"
	case "get_logs":
		return "正在读取 " + target + " 日志"
	case "get_system_state":
		return "正在读取环境状态"
	case "get_project_brief":
		return "正在读取项目画像"
	case "start_service":
		return "正在启动 " + target
	case "stop_service":
		return "正在停止 " + target
	case "verify_environment":
		return "正在复验 " + target
	case "recall_lessons":
		return "正在查阅历史经验"
	case "get_chain_guard":
		return "正在查询链端守护状态"
	default:
		if target != "" {
			return "正在执行 " + tool + "（" + firstLines(target, 40) + "）"
		}
		return "正在执行 " + tool
	}
}

// planSummary 汇总为一句话，放在 AI 最终回答之后作为"这次怎么做的"。
func planSummary(p *planTrack, outcome string) string {
	if p == nil || len(p.Steps) == 0 {
		return ""
	}
	uniq := map[string]int{}
	var order []string
	for _, s := range p.Steps {
		if uniq[s.Tool] == 0 {
			order = append(order, s.Tool)
		}
		uniq[s.Tool]++
	}
	var parts []string
	for _, t := range order {
		if uniq[t] > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", t, uniq[t]))
		} else {
			parts = append(parts, t)
		}
	}
	return fmt.Sprintf("本次共 %d 步：%s（%s）", len(p.Steps), strings.Join(parts, "、"), outcome)
}
