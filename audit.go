package main

// audit.go —— 操作审计流（JSONL，只追加，与业务日志分离）
//
// 设计要点：
//  1. 业务日志回答"系统发生了什么"，审计回答"谁、何时、对什么、做了什么变更、结果如何"。
//     二者必须分文件：业务日志会被轮转覆盖，审计不能。
//  2. 只追加、不修改、不回滚；每天一个文件 audit-YYYYMMDD.jsonl，独立保留 30 天。
//  3. 写操作入口必须埋点；参数与详情统一脱敏（密码/API Key 抹成 ***）。
//  4. 审计失败（磁盘只读等）只告警，绝不阻断业务——审计是记录手段，不是控制手段。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AuditEntry 一条审计记录。
type AuditEntry struct {
	Ts     string `json:"ts"`               // 2006-01-02 15:04:05.000
	Actor  string `json:"actor"`            // user | ai | guard | system
	Action string `json:"action"`           // 动作名，如 db_backup / apply_sql / config_save
	Target string `json:"target"`           // 目标对象，如库名、服务名、文件路径
	Params string `json:"params"`           // 入参摘要（已脱敏）
	Result string `json:"result"`           // ok | fail | denied | started
	Detail string `json:"detail,omitempty"` // 结果补充（已脱敏）
	DurMs  int64  `json:"dur_ms,omitempty"` // 耗时
	Verify string `json:"verify,omitempty"` // 复验结论（v2.0 P2：执行之后有没有真的验过、验出什么）
}

// 动作主体
const (
	actUser  = "user"
	actAI    = "ai"
	actGuard = "guard"
	actSys   = "system"
)

// 结果取值
const (
	resOK     = "ok"
	resFail   = "fail"
	resDenied = "denied"
	resStart  = "started"
)

var (
	auditMu   sync.Mutex
	auditFile *os.File
	auditDay  string
)

// auditPath 当前审计文件路径（exeDir 定义在 av.go）。
func auditPath() string {
	return filepath.Join(exeDir(), "audit-"+time.Now().Format("20060102")+".jsonl")
}

// auditWrite 追加一条记录（内部持锁）。
func auditWrite(e AuditEntry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	day := time.Now().Format("20060102")
	if auditFile == nil || auditDay != day {
		if auditFile != nil {
			_ = auditFile.Close()
			auditFile = nil
		}
		f, err := os.OpenFile(auditPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			// 目录只读等场景：放弃落盘但留一条业务日志，避免静默丢失可观测性
			warn(scSys, "审计", "审计文件无法写入（%v），本次操作未留痕", err)
			return
		}
		auditFile, auditDay = f, day
	}
	_, _ = auditFile.WriteString(string(b) + "\n")
}

// auditNow 立即记录一条已完成的操作。
func auditNow(actor, action, target, params, result, detail string) {
	auditWrite(AuditEntry{
		Ts:     time.Now().Format("2006-01-02 15:04:05.000"),
		Actor:  actor,
		Action: action,
		Target: target,
		Params: auditSanitize(params),
		Result: result,
		Detail: auditSanitize(detail),
	})
}

// confirmReason 按界面语言返回二次确认文案（英文界面不能弹中文）。
func confirmReason(lang, zh, en string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "en") {
		return en
	}
	return zh
}

// auditStart 记录一条开始执行的操作，返回 finish 回调（自动计算耗时）。
// 用法：defer auditStart(actUser, "apply_sql", db, path)("ok", "")
func auditStart(actor, action, target, params string) func(result, detail string) {
	start := time.Now()
	return func(result, detail string) {
		auditWrite(AuditEntry{
			Ts:     time.Now().Format("2006-01-02 15:04:05.000"),
			Actor:  actor,
			Action: action,
			Target: target,
			Params: auditSanitize(params),
			Result: result,
			Detail: auditSanitize(detail),
			DurMs:  time.Since(start).Milliseconds(),
		})
		metAudit(actor, action, result)
	}
}

// ---------- 脱敏 ----------
// auditSanitize 把配置中的密钥从任意字符串里抹掉，避免"审计记录了密码"这种反效果。
func auditSanitize(s string) string {
	if s == "" {
		return ""
	}
	cfgMu.Lock()
	secrets := []string{
		cfg.MySQL.Password,
		cfg.Projects.MySQLPass,
		cfg.Chain.SSHPassword,
	}
	if cfg.AI != nil {
		secrets = append(secrets, cfg.AI.APIKey)
	}
	cfgMu.Unlock()
	for _, sec := range secrets {
		if len(sec) >= 3 {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	if len(s) > 500 {
		s = s[:500] + "…(截断)"
	}
	return s
}

// readAuditFile 读一份审计文件（每行一条 JSON）。坏行跳过——审计文件可能被人为编辑过，
// 读不出来也不能让查看页整个打不开。
func readAuditFile(p string) []AuditEntry {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	out := make([]AuditEntry, 0)
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var e AuditEntry
		if json.Unmarshal([]byte(ln), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// auditDir 审计目录的来源。抽成变量是为了让单测在临时目录里造数据，
// 而不是往用户真实的审计文件里写测试记录。
var auditDir = exeDir

// auditDays 列出有审计记录的日期，倒序（新在前）。
func auditDays() []string {
	entries, err := os.ReadDir(auditDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "audit-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if d := strings.TrimSuffix(strings.TrimPrefix(name, "audit-"), ".jsonl"); len(d) == 8 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// auditQuery 按天与主体查审计，返回最新的 n 条（新在前）。
// day 为空或 "all" 时跨天合并——默认只读今天的话，刚启动的当天往往空空如也。
func auditQuery(day, actor string, n int) []AuditEntry {
	if n <= 0 {
		n = 200
	}
	days := auditDays()
	if day != "" && day != "all" {
		days = []string{day}
	}
	// 先按日期正序（旧→新）拼接，这样"取尾部 n 条"才等于"取最新的 n 条"
	for i, j := 0, len(days)-1; i < j; i, j = i+1, j-1 {
		days[i], days[j] = days[j], days[i]
	}
	var all []AuditEntry
	dir := auditDir()
	for _, d := range days {
		all = append(all, readAuditFile(filepath.Join(dir, "audit-"+d+".jsonl"))...)
	}
	if actor != "" {
		kept := make([]AuditEntry, 0, len(all))
		for _, e := range all {
			if strings.EqualFold(e.Actor, actor) {
				kept = append(kept, e)
			}
		}
		all = kept
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	// 反转成新在前——页面从上往下看符合直觉
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if all == nil {
		all = []AuditEntry{}
	}
	return all
}

// auditTail 读取最近 n 条审计记录（供诊断报告用）。
func auditTail(n int) []AuditEntry {
	if n <= 0 {
		n = 50
	}
	all := readAuditFile(auditPath())
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// handleAudit 审计查看接口。只读，不落审计——否则"看一眼"也会把审计刷爆。
func handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	writeJSON(w, map[string]any{
		"ok":      true,
		"days":    auditDays(),
		"entries": auditQuery(r.URL.Query().Get("day"), strings.TrimSpace(r.URL.Query().Get("actor")), n),
	})
}

// cleanupOldAudits 清理超过 30 天的审计文件（与业务日志分开保留策略）。
func cleanupOldAudits() {
	dir := exeDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -30)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "audit-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		datePart := strings.TrimSuffix(strings.TrimPrefix(name, "audit-"), ".jsonl")
		t, perr := time.Parse("20060102", datePart)
		if perr != nil || !t.Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// auditSummary 给诊断报告用：最近审计条目的单行摘要。
func auditSummary(n int) []string {
	es := auditTail(n)
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, fmt.Sprintf("%s [%s] %s %s → %s%s",
			e.Ts, e.Actor, e.Action, e.Target, e.Result,
			func() string {
				if e.Detail != "" {
					return "（" + e.Detail + "）"
				}
				return ""
			}()))
	}
	return out
}
