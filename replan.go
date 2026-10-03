package main

// replan.go —— v2.3 N8：卡住时换思路，而不是重复同一个动作
//
// ## 要解决什么
//
// N5 把工具调用预算从固定 8 改成按任务分级（4/10/16），
// 缓解了"探索类问题被截断"。但**预算只是给了更多机会**，
// 模型仍可能在同一个动作上反复失败——
//
//	search_files(pattern="启动") → 没找到
//	search_files(pattern="启动") → 没找到
//	search_files(pattern="启动") → 没找到     ← 三轮全废
//
// 三个问题叠在一起：
//
//  1. **纯烧 token**：每次搜索都要过一遍几千个文件。
//  2. **看不到卡住**：用户以为它在工作，实际上在原地打转。
//  3. **它自己知道该停**：OpResult 里带着 err_kind 与 hint，
//     但模型可能忽略它继续试同一个。
//
// ## 核心判断：不能只说"别重复了"
//
// 现有提示词已经有一条"失败时换思路"的原则，但那是**祈使句**——
// 模型未必遵守。真正管用的是**结构化地告诉它换什么**：
//
//	search_files 连续失败 → 建议 search_files(不同关键词)
//	                  → 或 read_file(已知存在的文件)
//	                  → 或直接问用户要路径
//
// 按工具给出**具体的替代动作**，而不是空喊"再试别的"。
//
// ## 为什么不做完整 Plan 层
//
// 完整的 Plan（生成计划 → 等批准 → 逐步执行 → Re-plan）是 v2.2 的大工程。
// 本项只解决"卡在同一个动作上"这一个具体问题——
// 计划审批流会让每个简单问题都多一次点击，与 EnvKit
// "AI 增强本地能力、用户始终能手动驾驶"的定位冲突。
//
// ## 判定阈值：为什么是 2 次而不是 3 次
//
// 第一次失败可能是运气（网络抖动、文件刚被创建）。
// 第二次用同样参数失败，基本可以断定这条路走不通——
// 再试第三次是纯浪费。而 search_files 换关键词成本极低，
// 早一点引导它换方向，用户等的时间更短。

import (
	"fmt"
	"strings"
)

// replanState 记录本次任务里各工具的失败次数。
//
// 挂在 planTrack 上而不是全局：它必须是**每个任务独立**的——
// 上一个任务失败过的工具不该影响这一个任务的判定。
type replanState struct {
	fails map[string]int
	last  string // 上一个失败的工具
	// stuckTools 已判定"卡住"的工具，不重复提示
	stuckTools map[string]bool
}

func newReplan() *replanState {
	return &replanState{fails: map[string]int{}, stuckTools: map[string]bool{}}
}

// replanKey 失败计数的键。抽成函数是为了让测试用同一套规则——
// 手写 key 容易与实现漂移，那样的测试通过也不代表真的拦住了。
func replanKey(tool, target string) string {
	if target == "" {
		return tool
	}
	return tool + "\x00" + target
}

// markFail 记一次失败，返回是否判定为"卡住"。
func (r *replanState) markFail(tool, target string) (stuck bool, count int) {
	if r == nil {
		return false, 0
	}
	// 带参数的失败与不带参数的要分开算：
	// search_files("a") 失败后换 search_files("b") 是正确做法，
	// 不该被判成"还在重复"。这与 policyActionKey 的"参数不进键"同源。
	key := replanKey(tool, target)
	r.fails[key]++
	r.last = tool
	return r.fails[key] >= replanStuckAt, r.fails[key]
}

// replanStuckAt 连续同参数失败多少次判定为卡住。
const replanStuckAt = 2

// shouldIntervene 只在"同一工具+同一参数连续失败"且未提示过时返回 true。
func (r *replanState) shouldIntervene(tool, target string) bool {
	if r == nil {
		return false
	}
	key := replanKey(tool, target)
	if r.stuckTools[key] {
		return false // 已经引导过一次了，不重复
	}
	if r.fails[key] < replanStuckAt {
		return false
	}
	// 只对"探索类"工具引导。写操作连续失败通常是环境问题
	// （端口被占、依赖缺失），换工具没用，该说的是"先解决环境"。
	return isExplorationTool(tool)
}

func isExplorationTool(tool string) bool {
	switch tool {
	case "search_files", "read_file", "list_project", "db_query", "get_logs":
		return true
	}
	return false
}

// replanHint 给模型的"换思路"指引。
//
// 写法要点：
//   - **给具体替代动作**，不空喊"再试别的"
//   - 说清为什么原动作不行（它已经看到 err_kind 了，这里给方向）
//   - 明确"不要再用完全相同的参数重试"（这是它最容易犯的）
func replanHint(tool, target string) string {
	var alt []string
	switch tool {
	case "search_files":
		alt = []string{
			"换一个关键词搜（去掉太具体的词，用目录名或标识符这类更通用的词）",
			"先 list_project 看目录结构，锁定文件位置后用 read_file 精读",
			"如果怀疑路径在项目目录外，先问用户要绝对路径（探索工具只能读已配置的目录）",
		}
	case "read_file":
		alt = []string{
			"确认文件名是否猜错了——用 list_project 或 search_files 先定位真实文件名",
			"确认路径在已配置的项目目录内，超出范围要问用户要正确路径",
			"文件可能很大：先确认偏移量与行数是否落在有效范围",
		}
	case "list_project":
		alt = []string{
			"确认项目目录配置对不对（这个工具只能在「程序配置」里设定的目录下工作）",
			"目录可能被沙箱拒绝，此时要问用户实际路径",
		}
	case "db_query":
		alt = []string{
			"先用 db_list 看真实的库名与表名，不要猜",
			"把查询改简单：先 SELECT COUNT(*) 确认表有数据，再逐步加条件",
			"检查字段名是否拼错——用 DESCRIBE 表名 确认真实字段",
		}
	case "get_logs":
		alt = []string{
			"换个日志分类看（install / config / start / chain / sys）",
			"减少行数只看最近的，前面可能是历史噪音",
		}
	}
	if len(alt) == 0 {
		return ""
	}
	who := tool
	if target != "" {
		who = fmt.Sprintf("%s(%s)", tool, firstLines(target, 40))
	}
	var sb strings.Builder
	sb.WriteString("（系统提醒）")
	sb.WriteString(who + " 用**完全相同的参数**已经失败 " +
		itoa(replanStuckAt) + " 次了。换一个做法，不要第三次重复同样的调用。\n")
	sb.WriteString("可以改用：\n")
	for i, a := range alt {
		sb.WriteString("  " + itoa(i+1) + ". " + a + "\n")
	}
	sb.WriteString("如果都不通，就直接告诉用户你需要什么信息（例如具体文件路径、表名），" +
		"让他给你——比反复试探快得多。")
	return sb.String()
}

// replanUserNotice 给用户看的版本（终局时说明它卡在哪了）。
//
// 用户有权知道"它试了几次都没成"而不是只看到最终答案——
// 尤其当他等了很久却只得到一句"做完了"。
func replanUserNotice(tool, target string, fails int) string {
	who := tool
	if target != "" {
		who = fmt.Sprintf("%s(%s)", tool, firstLines(target, 40))
	}
	return fmt.Sprintf("（提示：%s 用同样的方式试了 %d 次都没成功，已换方法。"+
		"如果结果不对，直接告诉我缺什么信息会比反复试探更快。）", who, fails)
}
