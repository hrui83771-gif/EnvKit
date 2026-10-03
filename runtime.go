package main

// runtime.go —— v2.2 M11：统一状态源
//
// ## 为什么要有这个
//
// 前端现在有三套轮询：`pollHealth`（2s）/ `pollState`（3s）/ `pollProg`（3s），
// 各自算各自的。问题不在于"多次请求"，而在于**同一件事被算了三遍**：
//
//   - `deps.frontend`（node_modules 是否存在）由 health 算，前端又自己推断一次
//   - `services` 的 running 来自 svcState，但"跑了多久""有没有验过"没人算
//   - **"现在哪里有问题"根本没有统一答案**——三个接口都不给这个
//
// 规划里 v3.0 要做「项目状态中心」，而状态中心的前提是**有一个唯一的状态源**。
// 没有它，UI 只能把三个接口的结果拼起来自己判断——那就是"每页自己算"。
//
// ## 与既有接口的关系
//
// `/api/runtime/state` 是**唯一计算源**；`/api/health` 改为从它取数，
// 响应格式与字段名保持不变（前端与既有测试都不受影响）。
// 这样计算逻辑只有一份，同时可以逐页迁移，零回归。
//
// ## 性能：慢变与快变分开
//
// 这个接口会被高频轮询，不能每次都做重活：
//
//	慢变（30s 缓存）：env 组件版本、node_modules、项目画像、启动方式
//	快变（实时）：  服务存活、数据库连通、链端状态
//
// 磁盘遍历与 netstat 都在慢变那侧，缓存命中时整次调用只是读几个内存变量。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ===== 类型 =====

// ServicePhase 服务生命周期阶段。
type ServicePhase string

const (
	phStopped  ServicePhase = "stopped"
	phStarting ServicePhase = "starting"
	phRunning  ServicePhase = "running"
	phDegraded ServicePhase = "degraded" // 进程在但未通过复验
	phFailed   ServicePhase = "failed"   // 崩溃或复验失败
)

// ServiceState 服务完整状态。布尔值表达不了"启动中/降级/反复重启"，
// 而状态中心与 AI 都需要这些信息。
type ServiceState struct {
	Phase       ServicePhase `json:"phase"`
	Running     bool         `json:"running"`
	PID         int          `json:"pid"`
	Port        int          `json:"port,omitempty"`
	Since       string       `json:"since,omitempty"`
	UptimeSec   int64        `json:"uptime_sec,omitempty"`
	Restarts    int          `json:"restarts,omitempty"` // 连续失败次数
	Backoff     string       `json:"backoff,omitempty"`  // 建议退避与原因
	LastErr     string       `json:"last_err,omitempty"` // 最近一次失败的结论
	LastErrKind string       `json:"last_err_kind,omitempty"`
	LastErrAt   string       `json:"last_err_at,omitempty"`
	LastVerify  string       `json:"last_verify,omitempty"` // 最近一次复验结论
	Verified    bool         `json:"verified"`
	VerifyAt    string       `json:"verify_at,omitempty"`
	Crashes     []CrashEvent `json:"crashes,omitempty"` // 崩溃历史（新在前）
}

// Issue 状态中心要显示的核心：**哪里有问题、该怎么办**。
//
// 只报"有问题"而不给下一步，等于把负担推回给用户——
// 而这正是 EnvKit 存在的意义（它本来就是帮人干这件事的）。
type Issue struct {
	Level  string `json:"level"`  // error | warn | info
	Scope  string `json:"scope"`  // env | project | service | db | chain
	What   string `json:"what"`   // 问题
	Action string `json:"action"` // 该怎么办
}

// RuntimeState 是唯一状态源。
type RuntimeState struct {
	ObservedAt string                  `json:"observed_at"`
	Admin      bool                    `json:"admin"`
	Busy       string                  `json:"busy"`
	Phase      string                  `json:"phase"`
	Env        EnvInfo                 `json:"env"`
	Project    ProjInfo                `json:"project"`
	Services   map[string]ServiceState `json:"services"`
	DB         DBHealth                `json:"db"`
	Chain      ChainInfo               `json:"chain"`
	Deps       map[string]bool         `json:"deps"`
	Issues     []Issue                 `json:"issues"`
}

// EnvInfo 环境侧。
type EnvInfo struct {
	HasResults bool              `json:"has_results"`
	Ready      bool              `json:"ready"`
	Components map[string]string `json:"components,omitempty"` // 组件 → 版本
	Missing    []string          `json:"missing,omitempty"`
}

// ProjInfo 项目侧。
type ProjInfo struct {
	FrontendDir string   `json:"frontend_dir"`
	BackendDir  string   `json:"backend_dir"`
	Kinds       []string `json:"kinds,omitempty"`
	Web         string   `json:"web_script,omitempty"`    // 推断/配置的启动脚本
	WebSource   string   `json:"web_source,omitempty"`    // config | package.json | none
	Backend     string   `json:"backend_entry,omitempty"` // 推断/配置的入口
	// WebDepsInstalled 前端依赖是否就绪。**在 runtimeProject 里查一次**，
	// 不在 runtimeIssues 里再查——后者每轮轮询都会多花一次磁盘遍历。
	WebDepsInstalled bool `json:"web_deps_installed"`
	// WebDepsUnknown 目录未配置或不可读，此时不该报"依赖缺失"（会误报）
	WebDepsUnknown bool `json:"web_deps_unknown"`
}

// ===== 慢变部分缓存 =====

var (
	rtMu      sync.Mutex
	rtSlow    *RuntimeState
	rtSlowAt  time.Time
	rtSlowFor = 30 * time.Second
)

const (
	rtIssError = "error"
	rtIssWarn  = "warn"
	rtIssInfo  = "info"
)

// runtimeStateSlow 采集慢变部分（磁盘 / 组件 / 画像 / 启动方式）。
func runtimeStateSlow() *EnvInfo {
	resultsMu.Lock()
	n := len(results)
	comps := make(map[string]string, len(results))
	var missing []string
	for k, v := range results {
		if v.Version != "" {
			comps[k] = v.Version
		}
		if !v.Installed {
			missing = append(missing, k)
		}
	}
	ready := n > 0
	for _, v := range results {
		if !v.Installed {
			ready = false
		}
	}
	resultsMu.Unlock()
	sort.Strings(missing)
	return &EnvInfo{HasResults: n > 0, Ready: ready, Components: comps, Missing: missing}
}

// runtimeProject 采集项目侧（含启动方式推断）。
func runtimeProject() ProjInfo {
	p := ProjInfo{
		FrontendDir: cfg.Projects.FrontendDir,
		BackendDir:  cfg.Projects.BackendDir,
	}
	var brief projectBrief
	if raw := aiProjectBriefJSON(); raw != "" {
		if json.Unmarshal([]byte(raw), &brief) == nil {
			p.Kinds = brief.Kinds
		}
	}
	lp := currentLaunchPlan()
	p.Web, p.WebSource, p.Backend = lp.WebScript, lp.WebSource, lp.BackendFile
	// 依赖检查只在这里做一次，issues 复用结果
	if strings.TrimSpace(p.FrontendDir) == "" {
		p.WebDepsUnknown = true
	} else if fi, err := os.Stat(p.FrontendDir); err != nil || !fi.IsDir() {
		p.WebDepsUnknown = true
	} else {
		p.WebDepsInstalled = dirHasNodeModules(p.FrontendDir)
	}
	return p
}

// runtimeService 采集单个服务状态。
func runtimeService(target string) ServiceState {
	svcMu.Lock()
	var si SvcInfo
	started := ""
	if s := svcState[target]; s != nil {
		si = *s
		started = s.Since
	}
	progMu.Lock()
	starting := false
	if ps, ok := progState[target+"-start"]; ok {
		starting = ps.Running
	}
	progMu.Unlock()
	svcMu.Unlock()

	st := ServiceState{
		Running: si.Running,
		PID:     si.PID,
		Since:   started,
		Port:    portFromURL(si.URL),
	}
	if started != "" {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", started, time.Local); err == nil {
			st.UptimeSec = int64(time.Since(t).Seconds())
		}
	}
	if st.Port > 0 {
		if prev, ok := svcPortOf(target); ok {
			st.Restarts = 0 // 占位：重启计数在 v2.2 状态机补齐
			_ = prev
		}
	}
	st.Phase = phStopped
	switch {
	case starting:
		st.Phase = phStarting
	case !si.Running:
		st.Phase = phStopped
	default:
		st.Phase = phRunning
	}
	// 复验结论与崩溃历史来自统一注册表（svcstate.go）。
	//
	// 判定用 svcVerifyRec.Verified 布尔，**不做字符串匹配**——早先写成
	// strings.Contains(v, "已复验通过")，一旦 AI 换个说法就误判成"没验过"，
	// 状态灯会莫名变黄。结论文本是给人看的，布尔才是给机器判断的。
	rec := svcSnapshot(target)
	if rec.Conclusion != "" || rec.ErrKind != "" {
		st.LastVerify = rec.Conclusion
		st.VerifyAt = rec.VerifyAt
		st.Verified = rec.Verified
	}
	st.Restarts = rec.Restarts
	st.Backoff = rec.Backoff
	st.LastErr = rec.LastErr
	st.LastErrAt = rec.LastErrAt
	// 崩溃次数 > 0 且当前没在跑 → failed，而不是单纯的 stopped：
	// "停着"和"崩了停着"对用户是完全不同的两件事。
	if rec.Restarts > 0 && !si.Running && st.Phase == phStopped {
		st.Phase = phFailed
	}
	if st.Phase == phRunning && !st.Verified {
		st.Phase = phDegraded
	}
	return st
}

// ===== issues 计算 =====

// runtimeIssues 从各部分状态推导"哪里有问题、该怎么办"。
//
// 原则：**每条 issue 都必须带可执行的下一步**。只说"有问题"而不说怎么办，
// 等于把负担推回给用户——而这正是 EnvKit 存在的意义。
func runtimeIssues(env *EnvInfo, proj ProjInfo, svcs map[string]ServiceState, db DBHealth, chain ChainInfo) []Issue {
	var out []Issue
	add := func(level, scope, what, action string) {
		out = append(out, Issue{Level: level, Scope: scope, What: what, Action: action})
	}

	// ---- 环境 ----
	if !env.HasResults {
		add(rtIssInfo, "env", "尚未检测环境", "先在「环境安装」跑一次检测")
	}
	for _, m := range env.Missing {
		add(rtIssError, "env", "缺少组件："+m, "在「环境安装」里安装 "+m)
	}
	// v2.3 N7：环境符合性。**装了不等于够用**——项目可能要更高版本。
	//
	// 只在**确实探测到版本且不够**时报警。探测不到（Go 不在 PATH ——
	// EnvKit 自带的 SDK 就不在）属于"没检查到"，报成"未安装"或"不够"
	// 都是误导：前者让用户去装一个他已经有的东西，后者是凭空断言。
	// 这与 v2.0 修过的链端判据同源。
	if env.HasResults {
		for _, r := range checkAllEnvReq() {
			if r.Status != reqTooLow {
				continue
			}
			add(rtIssError, "env", r.Component+" 版本不满足项目要求："+r.Why, r.Action)
		}
	}

	// ---- 项目 ----
	if strings.TrimSpace(proj.FrontendDir) == "" {
		add(rtIssWarn, "project", "未配置前端目录", "在「程序配置」选择前端目录")
	} else if !proj.WebDepsUnknown && !proj.WebDepsInstalled {
		add(rtIssError, "project", "前端依赖未安装（node_modules 缺失）", "先执行 npm install，否则 npm run 必然失败")
	}
	if strings.TrimSpace(proj.BackendDir) == "" {
		add(rtIssWarn, "project", "未配置后端目录", "在「程序配置」选择后端目录")
	}
	// 启动方式识别不出来是最容易让人卡住的一类问题，必须单列并给手动入口
	if proj.WebSource == "none" {
		msg := "识别不出前端启动脚本"
		if len(proj.Kinds) > 0 {
			msg += "（项目类型：" + strings.Join(proj.Kinds, "、") + "）"
		}
		add(rtIssWarn, "project", msg,
			"在「程序配置 → 启动方式」手动填写前端启动脚本（dev / serve / start 这类）")
	}
	if proj.FrontendDir != "" && proj.Backend == "" && strings.TrimSpace(proj.BackendDir) != "" {
		add(rtIssWarn, "project", "识别不出后端入口", "在「程序配置 → 启动方式」手动填写后端入口（相对后端目录，如 main.go）")
	}

	// ---- 服务 ----
	for _, key := range []string{"backend", "web"} {
		s, ok := svcs[key]
		if !ok {
			continue
		}
		name := "后端"
		if key == "web" {
			name = "前端"
		}
		switch s.Phase {
		case phDegraded:
			add(rtIssWarn, "service", name+"进程在运行但未通过复验",
				"点「复验」确认服务真的可用；未复验前不要当作已就绪")
		case phFailed:
			// 反复崩溃是最容易让人盲目重启的场景：把次数与建议一起给出来。
			act := "查看「程序启动」日志最后几行"
			switch {
			case s.Restarts >= 3:
				act = "已连续失败 " + itoa(s.Restarts) + " 次，别再盲目重启——" + s.Backoff
			case s.LastErr != "":
				act += "：" + firstLines(s.LastErr, 60)
			}
			what := name + "启动失败"
			if s.Restarts > 1 {
				what += "（连续 " + itoa(s.Restarts) + " 次）"
			}
			if s.LastErr != "" {
				what += "：" + firstLines(s.LastErr, 80)
			}
			add(rtIssError, "service", what, act)
		}
	}

	// ---- 数据库 ----
	if strings.TrimSpace(cfg.Projects.DBName) != "" {
		switch {
		case !db.Connected:
			add(rtIssError, "db", "数据库未连接", "检查 MySQL 服务是否运行、账号密码是否正确")
		case !db.DBExists:
			add(rtIssWarn, "db", "数据库不存在："+db.Msg, "在「程序配置」执行建库")
		}
	}

	// ---- 链端 ----
	// 链端不可达只是 warn 不是 error：绝大多数情况是"本机虚拟机没开"，
	// 报成 error 会让人以为链坏了。
	if chain.Checked {
		if !chain.SSHOK && !chain.Port20200 {
			add(rtIssWarn, "chain", "链端不可达（SSH 与 20200 端口均无响应）",
				"确认链端已启动；本机虚拟机场景请确认端口转发与 chain_host 配置")
		} else if !chain.Port20200 {
			add(rtIssWarn, "chain", "链节点端口未监听（20200）", "在「链端校验」查看详情")
		}
		if !chain.WebaseOK {
			add(rtIssWarn, "chain", "WeBASE-Front 未响应（5002）", "确认 WeBASE-Front 已启动")
		}
	}

	sort.Slice(out, func(i, j int) bool {
		rank := map[string]int{rtIssError: 0, rtIssWarn: 1, rtIssInfo: 2}
		return rank[out[i].Level] < rank[out[j].Level]
	})
	if out == nil {
		out = []Issue{}
	}
	return out
}

// 复验结论与崩溃记录统一放在 svcstate.go 的服务注册表（svcRecordVerify / svcLastVerify 等）。

// ===== 采集与接口 =====

// currentRuntimeState 汇总完整状态（慢变部分带 30s 缓存）。
func currentRuntimeState() *RuntimeState {
	rtMu.Lock()
	defer rtMu.Unlock()
	now := time.Now()
	if rtSlow != nil && now.Sub(rtSlowAt) < rtSlowFor {
		// 慢变命中：只补快变部分
		cur := *rtSlow
		cur.Services = runtimeServices()
		cur.DB = runtimeDB()
		cur.Chain = runtimeChain()
		cur.Busy = currentTask()
		cur.Issues = runtimeIssues(&cur.Env, cur.Project, cur.Services, cur.DB, cur.Chain)
		cur.ObservedAt = now.Format("2006-01-02 15:04:05")
		return &cur
	}

	env := runtimeStateSlow()
	proj := runtimeProject()
	svcs := runtimeServices()
	db := runtimeDB()
	chain := runtimeChain()
	phaseMu.Lock()
	ph := phase
	phaseMu.Unlock()

	st := &RuntimeState{
		ObservedAt: now.Format("2006-01-02 15:04:05"),
		Admin:      isAdminFlag,
		Busy:       currentTask(),
		Phase:      ph,
		Env:        *env,
		Project:    proj,
		Services:   svcs,
		DB:         db,
		Chain:      chain,
		Deps: map[string]bool{
			"frontend": proj.FrontendDir != "" && dirHasNodeModules(proj.FrontendDir),
			"backend":  progOk("backend-tidy"),
		},
	}
	st.Issues = runtimeIssues(env, proj, svcs, db, chain)
	rtSlow, rtSlowAt = st, now
	return st
}

func runtimeServices() map[string]ServiceState {
	return map[string]ServiceState{
		"web":     runtimeService("web"),
		"backend": runtimeService("backend"),
	}
}

func runtimeDB() DBHealth {
	dbMu.Lock()
	defer dbMu.Unlock()
	return dbHealth
}

func runtimeChain() ChainInfo {
	chainMu.Lock()
	ch := chainInfo
	chainMu.Unlock()
	ch.WebaseURL = webaseBaseURL()
	return ch
}

// runtimeInvalidateCache 配置/项目目录变化后清缓存，慢变部分重新采集。
func runtimeInvalidateCache() {
	rtMu.Lock()
	rtSlow, rtSlowAt = nil, time.Time{}
	rtMu.Unlock()
}

// handleRuntimeState GET：唯一状态源。
func handleRuntimeState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "state": currentRuntimeState()})
}

// itoa 极简整数转字符串（状态中心文案里要用，避免为此引入 strconv 的完整依赖）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// runtimeIssueSummary 给 AI 看的简短状态摘要（注入快照用）。
func runtimeIssueSummary(s *RuntimeState) string {
	if s == nil || len(s.Issues) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("当前环境有 ")
	sb.WriteString(fmt.Sprint(len(s.Issues)))
	sb.WriteString(" 个待处理问题：\n")
	for _, is := range s.Issues {
		sb.WriteString("- [" + is.Level + "] " + is.What)
		if is.Action != "" {
			sb.WriteString(" → " + is.Action)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
