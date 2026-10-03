package main

// scope.go —— v2.3 N2：经验的适用范围、置信度与失效机制
//
// ## 为什么第一批只做这五项
//
// ROADMAP 定的第二批（Conflict / Correction / Retired）**刻意不做**：
// 它们需要真实运行数据来判断机制是否有效。在没验证"经验到底有没有用"
// 之前就上复杂机制，是拿未验证的假设去构建。
//
// 这一批的每一项都对应一个具体的失效场景：
//
//	Scope      换项目还用 → "上次这台机器上 npm run serve 就行"，
//	                       换个前端项目就完全错
//	Evidence   凭什么信它 → 结论必须能追到具体审计行，人才能核对
//	Succ/Fail  只记失败 → 同一做法后来成功了却还在被劝阻
//	Stale      环境变了 → IP 变了、依赖升了、端口改了，经验自动失效
//	Confidence 少样本当真理 → 2 次失败和 20 次失败的置信度不该相同
//
// ## 核心原则：置信度只影响"怎么用"，不影响"能不能用"
//
// 高置信度不是"绕过确认"的通行证。PolicyGate 根本不读这里任何字段
// （v2.2 已用单测钉死"Memory 不是权限"），本文件也不提供任何提档 API。
// 置信度只决定：这条要不要主动提示、排在第几位、要不要标注不确定性。
//
// ## 不做跨项目泛化
//
// 一个项目学到的"经验"默认**不适用于另一个项目**。这不是保守，
// 而是正确：启动脚本、端口、目录约定都是项目特定的。
// 少数真正通用的（如"备份前先停后端"）由用户显式标 `Scope: global`。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Scope 一条经验的适用范围。
//
// 存储形态刻意是**扁平字符串**而不是嵌套结构：它要进提示词、要能被
// 用户看懂、要能手工编辑。嵌套结构在用户编辑时极易出错，
// 而这里出错的后果是"经验匹配不上"——看起来像学习功能坏了。
type Scope struct {
	// Kind 作用域类型：project（默认）| global
	Kind string `json:"kind,omitempty"`
	// Project 项目指纹（Kind=project 时有效）：前端目录+后端目录的哈希
	Project string `json:"project,omitempty"`
	// Path 前端/后端目录的展示名（给人看）
	Frontend string `json:"frontend,omitempty"`
	Backend  string `json:"backend,omitempty"`
	// EnvFingerprint 环境指纹：Go/Node/MySQL 版本 + 端口配置
	Env string `json:"env,omitempty"`
	// Action 限定的动作（空=不限）。用于"这条只关于 start_service"
	Action string `json:"action,omitempty"`
	// Target 限定的目标（空=不限）。"这条只关于 web 不关于 backend"
	Target string `json:"target,omitempty"`
}

// scopeGlobal 全局作用域：用户显式标记"这条在任何项目都适用"。
const scopeGlobal = "global"

// projectFingerprint 取当前项目的稳定指纹。
//
// 为什么用目录路径而不是项目名：项目名会重复（多个 demo 都叫 farm），
// 而路径是本地唯一的。哈希是为了避免把完整路径塞进提示词。
func projectFingerprint() string {
	cfgMu.Lock()
	fe, be := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	cfgMu.Unlock()
	if fe == "" && be == "" {
		return ""
	}
	h := sha256.Sum256([]byte(filepath.Clean(fe) + "|" + filepath.Clean(be)))
	return hex.EncodeToString(h[:])[:12]
}

// envFingerprint 取环境指纹。
//
// 刻意**不包含**任何绝对路径与 IP：环境"变了"指的是"依赖版本与端口配置
// 变了"，不是"IP 变了"——后者会让每次虚拟机重启都让全部经验失效，
// 那样 Stale 就成了噪声源而不是信号源。
func envFingerprint() string {
	var parts []string
	parts = append(parts, "schema="+strconv.Itoa(cfg.SchemaVersion))
	parts = append(parts, "chainport="+strconv.Itoa(cfg.Chain.ChainPort))
	parts = append(parts, "dbport="+strconv.Itoa(cfg.Projects.MySQLPort))
	parts = append(parts, "scanports="+fmt.Sprint(cfg.Projects.ScanPorts))
	// 环境检测结果里的版本号（若已检测过）：依赖升级正是最该让经验失效的变更
	resultsMu.Lock()
	var vers []string
	for _, v := range results {
		if v.Version != "" {
			vers = append(vers, v.Name+":"+v.Version)
		}
	}
	resultsMu.Unlock()
	sort.Strings(vers) // 顺序不稳定会让指纹乱变
	parts = append(parts, strings.Join(vers, ","))
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:])[:12]
}

// currentScope 取当前上下文的作用域。
func currentScope(action string) Scope {
	cfgMu.Lock()
	fe, be := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	cfgMu.Unlock()
	return Scope{
		Kind:     "project",
		Project:  projectFingerprint(),
		Frontend: filepath.Base(filepath.Clean(fe)),
		Backend:  filepath.Base(filepath.Clean(be)),
		Env:      envFingerprint(),
		Action:   action,
	}
}

// scopeApplies 判断一条经验在当前上下文是否仍然适用。
//
// 这是 Stale 的执行点。四种不适用：
//  1. 项目指纹变了        → 换项目/改目录
//  2. 环境指纹变了        → 依赖版本或端口配置变了
//  3. 动作不匹配          → 这条只关于 start_service，别的动作不算
//  4. 已标记为 global     → 跳过项目与环境检查
//
// **返回不适用时必须给理由**：界面上要显示"为什么这条不适用了"，
// 静默消失会让用户以为记忆功能坏了。
func scopeApplies(sc Scope, action string) (bool, string) {
	if sc.Kind == scopeGlobal {
		// global 也要检查动作限定——"这条只关于备份"仍然有意义
		if sc.Action != "" && sc.Action != action {
			return false, "只适用于 " + sc.Action + "，当前动作是 " + action
		}
		return true, ""
	}
	if sc.Action != "" && action != "" && sc.Action != action {
		return false, "只适用于 " + sc.Action
	}
	if sc.Project == "" {
		// 老数据（无作用域）视为仍然适用，但不能拿它做跨项目建议
		return true, ""
	}
	if cur := projectFingerprint(); cur != "" && cur != sc.Project {
		return false, "项目已变（" + firstLines(sc.Frontend+"/"+sc.Backend, 30) + " → " +
			currentScopeDirLabel() + "）"
	}
	if sc.Env != "" {
		if cur := envFingerprint(); cur != sc.Env {
			return false, "环境已变（依赖版本或端口配置调整过）"
		}
	}
	return true, ""
}

func currentScopeDirLabel() string {
	cfgMu.Lock()
	fe, be := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	cfgMu.Unlock()
	f, b := filepath.Base(filepath.Clean(fe)), filepath.Base(filepath.Clean(be))
	if f == "." {
		f = ""
	}
	if b == "." {
		b = ""
	}
	if f == "" && b == "" {
		return "(未配置)"
	}
	if f == "" {
		return b
	}
	if b == "" {
		return f
	}
	return f + "/" + b
}

// ===== 置信度 =====

// Confidence 依据成功/失败计数算出的置信度。
//
// 刻意用简单的成功/失败模型，不上概率公式：要解释得清、能在界面上
// 显示成"高/中/低"、能被用户质疑。复杂的贝叶斯在这个数据量下
// 只会给出看起来精确、实际由先验主导的数字。
//
// 数据来源刻意只用**同一条经验自身的成败**（ok/fail 计数），
// 不用"相关操作的成功率"——那会把不相关的信号混进来。
func Confidence(succ, fail int) float64 {
	total := succ + fail
	if total == 0 {
		return 0
	}
	// Laplace 平滑：1 次成功不该有 100% 置信度
	return float64(succ+1) / float64(total+2)
}

// confidenceLabel 给界面用的一档表述。
func confidenceLabel(c float64) string {
	switch {
	case c >= 0.8:
		return "高"
	case c >= 0.55:
		return "中"
	case c > 0:
		return "低"
	}
	return "无"
}

// 置信度对使用的约束：低于 lsConfMin 的经验**只作参考不主动提示**。
//
// 注意这不是安全策略——它只影响"要不要主动说"，不影响动作能否执行。
// 真正的档位判定在 PolicyGate，与这里完全无关（v2.2 已钉死）。
const lsConfMin = 0.55

// confidenceGate 决定一条经验是否值得主动提示。
func confidenceGate(c float64) (bool, string) {
	if c >= 0.8 {
		return true, ""
	}
	if c >= lsConfMin {
		return true, "置信度中等（" + confidenceLabel(c) + "），仅供参考"
	}
	return false, "置信度低（" + confidenceLabel(c) + "），不主动提示"
}

// ===== 成败计数 =====

// outcomeStat 经验的成败统计。
//
// 关键点：**成功也计数**。v2.0 的经验只统计失败，于是"这个做法不行"
// 一旦被写进经验就永远不会撤销——哪怕后来证明它其实是通的。
type outcomeStat struct {
	Succ int `json:"succ,omitempty"`
	Fail int `json:"fail,omitempty"`
}

// outcomeFile 统计文件路径。抽成变量供单测替换。
var outcomeFile = func() string { return filepath.Join(exeDir(), "lesson-outcomes.json") }

var outcomeMu sync.Mutex

// outcomeKey 统计的键：经验 ID + 作用域指纹。
//
// **必须带作用域**：同一条"端口被占"经验，在项目 A 上失败 5 次、
// 在项目 B 上成功 5 次，两个上下文必须分开统计，否则互相抵消。
func outcomeKey(id string) string {
	return id + "|" + projectFingerprint()
}

// recordOutcome 记一次成败。action 非空时才有意义（经验是按动作触发的）。
func recordOutcome(id, action, result string) {
	if id == "" || action == "" {
		return
	}
	if result != resOK && result != resFail {
		return // denied / started 不计入成败：它们不是"做了但没成"
	}
	outcomeMu.Lock()
	defer outcomeMu.Unlock()
	m := loadOutcomes()
	k := outcomeKey(id)
	st := m[k]
	if result == resOK {
		st.Succ++
	} else {
		st.Fail++
	}
	m[k] = st
	saveOutcomes(m)
}

// loadOutcomes 读统计。坏行跳过。
func loadOutcomes() map[string]outcomeStat {
	b, err := os.ReadFile(outcomeFile())
	if err != nil {
		return map[string]outcomeStat{}
	}
	m := map[string]outcomeStat{}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var e struct {
			K string      `json:"k"`
			V outcomeStat `json:"v"`
		}
		if json.Unmarshal([]byte(ln), &e) == nil && e.K != "" {
			m[e.K] = e.V
		}
	}
	return m
}

func saveOutcomes(m map[string]outcomeStat) {
	var sb strings.Builder
	for k, v := range m {
		sb.WriteString(mustJSON(struct {
			K string      `json:"k"`
			V outcomeStat `json:"v"`
		}{k, v}))
		sb.WriteString("\n")
	}
	// 限制条数：键里带项目指纹，目录改动会产生新键，不限制会无限增长
	if lines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n"); len(lines) > 500 {
		sb.Reset()
		sb.WriteString(strings.Join(lines[len(lines)-500:], "\n"))
	}
	if err := os.WriteFile(outcomeFile(), []byte(sb.String()), 0600); err != nil {
		warn(scSys, "记忆", "经验成败统计写入失败：%v", err)
	}
}

// outcomeOf 取某条经验在当前作用域下的成败统计。
func outcomeOf(id string) outcomeStat {
	outcomeMu.Lock()
	defer outcomeMu.Unlock()
	return loadOutcomes()[outcomeKey(id)]
}

// scopeSummary 给人看的作用域摘要。
func scopeSummary(sc Scope) string {
	if sc.Kind == scopeGlobal {
		if sc.Action != "" {
			return "全局 · 仅 " + sc.Action
		}
		return "全局"
	}
	var parts []string
	if p := strings.Trim(sc.Frontend+"/"+sc.Backend, "/"); p != "" {
		parts = append(parts, p)
	}
	if sc.Action != "" {
		parts = append(parts, sc.Action)
	}
	if len(parts) == 0 {
		return "本项目"
	}
	return strings.Join(parts, " · ")
}

// scopeStamp 给用户手写记忆用的默认值说明。
func scopeStamp() string {
	return fmt.Sprintf("当前项目指纹 %s / 环境 %s", projectFingerprint(), envFingerprint())
}
