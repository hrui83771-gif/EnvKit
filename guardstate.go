package main

// guardstate.go —— v2.3 N4：链端守护的状态视图
//
// ## 要解决什么
//
// 用户的原话：「AI 没有操作链的能力，但至少应该让他知道我们可以自动恢复链宕机，
// 让用户查看是不是开启了。」
//
// 这暴露了两个缺口，方向完全不同：
//
// **① AI 不知道有这回事。** 守护是后台 goroutine，AI 只能从快照的
// chain.guard 字段看到一个 true/false，不知道它意味着什么、也不知道
// EnvKit 能主动做链端恢复。结果：用户说"链挂了"，AI 只能建议用户手动检查，
// 而实际上后台可能正在自动拉起、或已经拉起过很多次——**它掌握的信息比用户少**。
//
// **② 用户看不到守护做了什么。** 守护动作只落在 audit 里
// （chain_autorecover / chain_unreachable），要用户自己去审计页翻 JSONL。
// 而守护是**无人值守**运行的——用户恰恰最需要知道"它到底有没有在管我的链"。
//
// ## 设计取舍：状态是"有信号就亮"，不是"没信号就报正常"
//
// 守护刚启动时还没跑过第一轮，此时说"正常"是撒谎（它只是还不知道）。
// 所以 last_check 为空时状态是 unknown 而不是 ok。
//
// 这与 v2.0 修过的链端判据是同一个原则：
// **拿不到事实就说没验到，不拿"没消息"当"好消息"。**

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// guardStatus 守护状态。UI 与 AI 快照共用这一份。
type guardStatus struct {
	Enabled      bool   `json:"enabled"`      // 用户是否开启了守护
	AutoRecover  bool   `json:"auto_recover"` // 宕机后是否自动拉起（一键检测时）
	IntervalSec  int    `json:"interval_sec"` // 实际检测间隔（秒）
	Host         string `json:"host,omitempty"`
	LastCheck    string `json:"last_check,omitempty"`  // 最近一轮检测时间
	LastAction   string `json:"last_action,omitempty"` // 最近一次动作：recover / skip_unreachable / none
	LastDetail   string `json:"last_detail,omitempty"` // 动作详情（已脱敏）
	LastOK       bool   `json:"last_ok"`
	RecoverCount int    `json:"recover_count"` // 本次运行期内的自动恢复次数
	SkipCount    int    `json:"skip_count"`    // 因不可达而跳过的次数
	// LastUnreachable 连续不可达次数。持续不为 0 是需要用户看的信号：
	// 地址失效时守护不会（也不该）尝试恢复，只能提示人去处理。
	LastUnreachable int `json:"last_unreachable"`
	// Note 给 AI 与用户的一句话说明，避免各自去解读布尔值
	Note string `json:"note,omitempty"`
}

var guardMu sync.Mutex
var guardRec = guardStatus{}

// guardMark 记一次守护动作。由 startGuardLoop 与一键检测调用。
func guardMark(action, detail string, ok bool) {
	guardMu.Lock()
	defer guardMu.Unlock()
	guardRec.LastCheck = time.Now().Format("15:04:05")
	guardRec.LastAction = action
	guardRec.LastDetail = firstLines(detail, 160)
	guardRec.LastOK = ok
	switch action {
	case "recover":
		guardRec.RecoverCount++
		guardRec.LastUnreachable = 0
	case "skip_unreachable":
		guardRec.SkipCount++
		guardRec.LastUnreachable++
	}
}

// guardSetUnreachable 只更新连续不可达计数（不记为动作——它不是动作）。
func guardSetUnreachable(n int) {
	guardMu.Lock()
	guardRec.LastUnreachable = n
	guardMu.Unlock()
}

// guardSnapshot 取当前守护状态，并按现配置刷新"是否开启"这两个字段。
//
// 为什么每次现读而不是只在配置变更时记：配置可以在界面上改，
// 而守护循环要等下一轮（最短 30 秒）才会真正生效。
// 界面上显示"已开启但守护循环还没重启"会让人以为已经生效了，
// 所以这里把 ConfigEnabled 与 Running 分开报。
func guardSnapshot() guardStatus {
	c := chainCfg()
	guardMu.Lock()
	s := guardRec
	guardMu.Unlock()
	s.Enabled = c.ChainGuard
	s.AutoRecover = c.ChainAutoRecover
	s.IntervalSec = c.ChainGuardSecs
	if s.IntervalSec < 30 {
		s.IntervalSec = 30
	}
	s.Host = c.SSHHost
	if c.SSHHost == "" {
		s.Note = "未配置链端地址，守护不运行"
		return s
	}
	if !c.ChainGuard {
		s.Note = "链端守护未开启：链端宕机不会自动恢复，需要你手动检测"
		return s
	}
	if s.LastCheck == "" {
		s.Note = fmt.Sprintf("链端守护已开启（每 %d 秒一轮），尚未完成第一轮检测", s.IntervalSec)
		return s
	}
	if s.LastUnreachable > 0 {
		s.Note = fmt.Sprintf("链端守护在跑，但已连续 %d 轮连不上链端（%s）——"+
			"此时不会尝试恢复，因为恢复命令本身要靠 SSH 送达。请确认虚拟机是否启动、IP 是否变化",
			s.LastUnreachable, reachHint(c, reachDown))
		return s
	}
	if s.LastAction == "recover" {
		s.Note = fmt.Sprintf("链端守护在跑：最近一次检测发现链端异常并已自动恢复（%s）", s.LastDetail)
		return s
	}
	s.Note = fmt.Sprintf("链端守护在跑（每 %d 秒一轮），最近一轮检测正常", s.IntervalSec)
	return s
}

// guardBrief 给 AI 看的简短说明（进快照）。
//
// 刻意写得可直接引用：模型看到"EnvKit 能自动恢复链端"才会在用户说
// "链挂了"时给出正确回答——**告知已开启自动恢复**，而不是让用户自己去重启。
func guardBrief() string {
	s := guardSnapshot()
	if s.Host == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(s.Note)
	// 能力告知：即使当前没开，也要让模型知道 EnvKit 有这个能力
	if s.AutoRecover {
		sb.WriteString("；EnvKit 具备链端自动恢复能力（chain_autorecover），一键检测时发现节点/WeBASE 宕机会自动尝试拉起")
	} else {
		sb.WriteString("；EnvKit 具备链端自动恢复能力，但 chain_autorecover 未开启（需在「程序配置 → 链端」勾选）")
	}
	if s.RecoverCount > 0 {
		sb.WriteString(fmt.Sprintf("；本次运行已自动恢复 %d 次", s.RecoverCount))
	}
	return sb.String()
}

// handleGuardStatus GET：守护状态。界面上"链端守护"卡片的数据源。
//
// 为什么单独一个接口而不塞进 /api/runtime/state：守护状态的变化频率
// 与环境状态不同（它每 30~300 秒才动一次），混在一起会让状态中心的
// 缓存频繁失效——而状态中心是首页轮询的热点。
func handleGuardStatus(w http.ResponseWriter, r *http.Request) {
	s := guardSnapshot()
	// 带上最近几次守护动作，让用户看到"它到底做了什么"而不只是"现在怎样"
	recent := auditQuery("all", actGuard, 8)
	acts := make([]map[string]any, 0, len(recent))
	for _, e := range recent {
		if !strings.Contains(e.Action, "recover") && !strings.Contains(e.Action, "unreachable") {
			continue
		}
		acts = append(acts, map[string]any{
			"ts": e.Ts, "action": e.Action, "target": e.Target,
			"result": e.Result, "detail": e.Detail, "verify": e.Verify,
		})
		if len(acts) >= 5 {
			break
		}
	}
	writeJSON(w, map[string]any{"status": s, "recent": acts})
}

func init() {
	aiToolRegistry["get_chain_guard"] = aiTool{
		Desc: "查询链端后台守护的状态：是否开启、检测间隔、最近一轮检测结果、" +
			"最近一次自动恢复有没有成功、本次运行已恢复几次、连续不可达几次。" +
			"用户问「链挂了会自动处理吗」「守护开着没」「它有没有重启过我的链」时用它，" +
			"不要凭猜测回答——守护是无人值守运行的，状态只有这里有",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Execute: func(map[string]any) (string, error) {
			s := guardSnapshot()
			if s.Host == "" {
				return "未配置链端地址，守护不运行", nil
			}
			out := s.Note
			out += fmt.Sprintf("\n开启=%v 自动恢复=%v 间隔=%ds 连续不可达=%d 已恢复次数=%d",
				s.Enabled, s.AutoRecover, s.IntervalSec, s.LastUnreachable, s.RecoverCount)
			if s.LastCheck != "" {
				out += fmt.Sprintf("\n最近检测=%s 动作=%s 结果=%s", s.LastCheck, s.LastAction, boolStr(s.LastOK, "正常", "异常"))
				if s.LastDetail != "" {
					out += "（" + s.LastDetail + "）"
				}
			}
			return out, nil
		},
	}
}
