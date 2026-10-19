// topology.go —— 拓扑抽象模型（v3.0 第二阶段 T2.2，plan §4.1）
//
// 原则（用户原话）：**不为每种环境复制一套引擎**。核心事务、权限裁决、证据格式、
// 指标定义四件事不依赖具体拓扑；新拓扑只能通过配置 + 适配器接入。
//
// 这个文件只放**词汇与契约**，不放任何具体拓扑的实现：
//
//	Service    一个可监控可恢复的单元（配置侧，config.json 的 services 清单）
//	Probe      只采集，不动作
//	Recoverer  产出执行计划并执行
//	Verifier   独立复验，三态出口
//	Observation / Action / Plan / VerifySpec   三者之间传递的值类型
//
// 具体实现见 topo_local.go / topo_db.go / topo_chain.go（T2.3-T2.5）。
//
// 红线（plan §六，写在这里而不是只写在文档里）：
//
//	R1  Recoverer 不提供"执行任意字符串"入口。命令一律来自配置模板，
//	    再过 launch.go 式白名单∩黑名单。所以 Execute 的参数是 Plan（已过安全检查），
//	    不是 string。
//	R3  Verifier 返回三态 OpResult。Ok&&!Verified 与 Ok&&Verified 在任何一层
//	    都不得合并——合并就是把"执行了"当成"恢复了"。
package main

import (
	"fmt"
	"strings"
	"time"
)

// ---------- 配置侧：Service ----------

// Adapter 适配器标识。新增适配器时在这里加常量，**不要在核心文件里写
// `if adapter == "xxx"` 的分支**——那是 R5 禁的，见 T2.9 的静态检查。
const (
	AdapterLocal    = "local"     // 本机进程（web / backend）
	AdapterDB       = "db"        // 数据库（MySQL）
	AdapterSSHChain = "ssh_chain" // 远程链端 + WeBASE
)

// 风险等级（plan §4.5 risk_tier）。low=重启类可自动；high=数据类必须人工。
const (
	RiskLow  = "low"
	RiskHigh = "high"
)

// Service 一个可监控可恢复的单元（plan §4.1 的 Service）。
//
// 旧 Projects / Chain 扁平字段是第一批发配器的配置来源。迁移期两者并存：
// Services 为空时按旧字段推导出等价清单（servicesFromLegacy，T2.1），
// 保证老配置读进来后的行为一个字节都不变。
type Service struct {
	ID       string `json:"id"`        // 唯一标识：web / backend / mysql / chain / webase
	Label    string `json:"label"`     // 人类可读名（UI 与审计里显示）
	Adapter  string `json:"adapter"`   // local / db / ssh_chain
	RiskTier string `json:"risk_tier"` // low / high

	// Probe 健康检查。Kind 决定用哪种探针（port / http / command / composite）。
	Probe ProbeSpec `json:"probe"`

	// Start / Stop / Restart 命令模板。**模板，不是任意字符串**：
	// 只接受配置里写死的这几个字段，且执行前过安全规则（R1）。
	Start   string `json:"start,omitempty"`
	Stop    string `json:"stop,omitempty"`
	Restart string `json:"restart,omitempty"`

	// DependsOn 依赖的其他 service id。跨节点事件关联的依据（plan §4.3）：
	// 同一时间窗内多个 service 探针失败时，先查共同依赖是否也失败了。
	DependsOn []string `json:"depends_on,omitempty"`

	// Recovery 恢复策略引用（strategy.go 那张表的键）。空 = 无自动恢复策略，
	// 该 service 只在报告里出现，守护循环不碰它。
	Recovery string `json:"recovery,omitempty"`

	// Guard 守护循环驱动这个 service 时需要的拓扑无关参数。
	// **这三个字段存在的理由是"保住基线审计条目"**：守护巡检要写审计，
	// 而审计里的动作名与目标在 v3.0.1 基线里是固定值。把它们提成配置，
	// 核心循环就不需要知道"链端的审计叫 chain_autorecover"（R5）。
	Guard GuardSpec `json:"guard,omitempty"`

	// Verify 独立复验规范（plan §4.5 的 verify_spec）。
	Verify VerifySpec `json:"verify"`
}

// GuardSpec 守护循环的驱动参数（拓扑无关）。
type GuardSpec struct {
	// Group 资源组名（组锁用）。空 = 用 service id 当组名。
	// 守护与用户的手动操作按组互斥：动链端的动作才需要和链端任务互斥。
	Group string `json:"group,omitempty"`

	// AuditAction 审计动作名。空 = "<id>_autorecover"。
	// 显式配置的理由：链端基线里的动作名是 chain_autorecover / webase_autorecover，
	// 与"<id>_autorecover"约定一致；但将来若有服务的约定不同，改配置即可，
	// 不必在核心循环里加分支。
	AuditAction string `json:"audit_action,omitempty"`

	// AuditTarget 审计目标。空 = service id。
	// 链端基线的目标是 SSH 主机（用户认这个），所以旧字段推导时显式填上。
	AuditTarget string `json:"audit_target,omitempty"`

	// IntervalSecs 巡检间隔（秒，最小 30）。0 = 用全局默认。
	// 为什么是 per-service 而不是全局：不同 service 的合理巡检频率不同
	// （链节点 30s，数据库可以 5 分钟）。旧代码只有一个 ChainGuardSecs，
	// 那是因为它只守护一种东西。
	IntervalSecs int `json:"interval_secs,omitempty"`

	// Notify 恢复时是否弹桌面通知。
	Notify bool `json:"notify,omitempty"`
}

// auditActionOf 审计动作名：显式配置优先，否则 "<id>_autorecover"。
func (g GuardSpec) auditActionOf(svcID string) string {
	if a := strings.TrimSpace(g.AuditAction); a != "" {
		return a
	}
	return svcID + "_autorecover"
}

// groupOf 资源组名：显式配置优先，否则用 service id。
func (g GuardSpec) groupOf(svcID string) string {
	if g := strings.TrimSpace(g.Group); g != "" {
		return g
	}
	return svcID
}

// auditTargetOf 审计目标：显式配置优先，否则 service id。
func (g GuardSpec) auditTargetOf(svcID string) string {
	if t := strings.TrimSpace(g.AuditTarget); t != "" {
		return t
	}
	return svcID
}

// ProbeKind 探针种类。
const (
	ProbePort      = "port"      // TCP 端口是否在听
	ProbeHTTP      = "http"      // HTTP 探活
	ProbeCommand   = "command"   // 只读命令（如 mysqladmin ping）
	ProbeComposite = "composite" // 多个子探针全部通过才算健康
)

// ProbeSpec 健康检查规范。
type ProbeSpec struct {
	Kind    string      `json:"kind"`
	Port    int         `json:"port,omitempty"`    // kind=port
	URL     string      `json:"url,omitempty"`     // kind=http
	Command string      `json:"command,omitempty"` // kind=command（只读）
	Timeout int         `json:"timeout,omitempty"` // 秒，0=内置默认
	Parts   []ProbeSpec `json:"parts,omitempty"`   // kind=composite
}

// VerifySpec 独立复验标准（plan §4.5 verify_spec）。
//
// Evidence 列出"必须拿到哪些客观证据字段"才算复验通过——不用他类的判据冒充。
// 例如链端的复验要 [port, node_proc, block_height]，数据库备份的复验要
// [file, size, sha256]。这正是 v3.0 判据改造（T1.7）"读系统客观记录"的配置侧对应物。
type VerifySpec struct {
	// Target 复验结论记到哪个目标上。**空值会被补成 service id**
	// （v3.0.1 踩过：备份类 target 为空串，导致"有事件"被判成"没事件"）。
	Target string `json:"target,omitempty"`
	// Evidence 期望的客观证据字段清单。
	Evidence []string `json:"evidence,omitempty"`
	// WaitSecs 复验等待窗口，0=内置默认。
	WaitSecs int `json:"wait_secs,omitempty"`
}

// ---------- 代码侧：三个接口 ----------

// Probe 只采集，不动作。
//
// 这个接口**没有任何执行入口**——这是 R1 的前半句。采集端能发起进程的话，
// "探测"本身就变成了未授权的动作，而且它发生在所有闸门之前。
type Probe interface {
	Observe(svc Service) Observation
}

// Recoverer 恢复动作的规划与执行。
//
// R1：Plan 只产出**过完安全规则的命令**；Execute 收 Plan 而不是 string，
// 从类型上就不可能"执行任意字符串"。
type Recoverer interface {
	// Plan 把意图变成可核查的执行计划（含风险分级与前置检查）。
	// 计划不合法时返回 error，**不允许退化成一个"尽量试试"的计划**。
	Plan(svc Service, a Action) (Plan, error)
	// Execute 执行计划，返回三态结论。
	Execute(p Plan) OpResult
}

// Verifier 独立复验。
//
// R3：三态出口。Ok&&!Verified 表示"动作执行了但环境尚未被证明恢复"，
// 它与 Ok&&Verified 在 UI 徽章、审计、判据三处各自独立存在。
type Verifier interface {
	Verify(svc Service, spec VerifySpec) OpResult
}

// ---------- 值类型 ----------

// Action 一次恢复动作的**意图**，不是命令。
// 命令从哪来：Service 的 start/stop/restart 模板 + 适配器的 Plan。
type Action struct {
	Kind string `json:"kind"` // start / stop / restart
	Why  string `json:"why"`  // 触发原因（哪个探针的哪个失败态）
}

// Plan 一次恢复动作的执行计划。可核查（ROADMAP §3.1）：审计与影子模式
// 记录的都是这个结构，而不是一句"重启了服务"。
type Plan struct {
	Service  string   `json:"service"`
	Action   string   `json:"action"`
	Command  string   `json:"command"` // 过完安全规则的命令（含参数），供审计与展示
	Args     []string `json:"args,omitempty"`
	RiskTier string   `json:"risk_tier"` // 来自 Service.RiskTier
	// Precheck 前置检查项（策略元数据 precheck）：组锁可获？预算余量？
	// 断路器未熔断？依赖健康？任一项不过即不执行。
	Precheck []string `json:"precheck,omitempty"`
	// Shadow true = 影子事务：走完 Observe→Plan 就停，不 Execute（plan §4.4）。
	Shadow bool `json:"shadow,omitempty"`
}

// Observation 一次探针采集到的**事实**。
// 只放客观字段，不放结论——结论由上层判定，这样同一个 Observation
// 能被恢复事务和报告消费，且两者的口径不会漂移。
type Observation struct {
	Service  string    `json:"service"`
	Healthy  bool      `json:"healthy"`
	Evidence []string  `json:"evidence,omitempty"`
	ErrKind  string    `json:"err_kind,omitempty"`
	At       time.Time `json:"at"`
}

// ---------- 旧字段等价推导（迁移期，T2.1） ----------

// servicesFromLegacy 由旧 Projects / Chain 扁平字段推导出等价的服务清单。
//
// 为什么需要它：老配置没有 services 字段。**不能因为升级了版本就要求用户
// 重填一遍配置**，也不能悄悄换一套语义——那样"链端守护"会静默失灵，
// 而用户看到的是"升级后链端不管了"，没有任何报错。
//
// 双向等价（T2.1 验收点）：
//
//	legacy → services：本函数；
//	services → legacy：每项都能映射回旧字段（探针端口/命令模板/复验目标）。
//
// 推导规则与旧代码逐条对齐：
//
//	· web/backend 用 LocalProc 适配器，探针是候选端口（svcPortsFn 的配置来源）；
//	· mysql 用 DB 适配器，探针是只读命令；
//	· chain/webase 用 SSHChain 适配器，探针是远程 TCP 端口，且 **webase 依赖 chain**
//	  （这正是 plan §4.3 跨节点事件关联要用的 depends_on：WeBASE 起不来先看链在不在）。
//
// **只有 chain/webase 挂 Recovery 策略**，这不是遗漏而是刻意的：
// startGuardLoop（main.go:163 调用）今天只守护链端，web/backend/mysql
// 从不被自动恢复。给它们挂上策略 = 升级即扩大无人值守自动恢复的作用面，
// 而这正是 plan §7.1 豁免风险里点名的那件事。要扩，等 T2.6 显式做并重跑基线。
func servicesFromLegacy(c Config) []Service {
	var svcs []Service

	// web：前端。留空端口 = 由项目画像/scan_ports 推断（与旧行为一致，
	// 不在推导期替用户猜一个端口写死）。
	if strings.TrimSpace(c.Projects.FrontendDir) != "" {
		svcs = append(svcs, Service{
			ID:       "web",
			Label:    "前端服务",
			Adapter:  AdapterLocal,
			RiskTier: RiskLow,
			Probe:    ProbeSpec{Kind: ProbePort, Port: 0, Timeout: 45},
			Verify:   VerifySpec{Target: "web", Evidence: []string{"pid", "port"}, WaitSecs: 45},
		})
	}
	// backend：后端。
	if strings.TrimSpace(c.Projects.BackendDir) != "" {
		svcs = append(svcs, Service{
			ID:       "backend",
			Label:    "后端服务",
			Adapter:  AdapterLocal,
			RiskTier: RiskLow,
			Probe:    ProbeSpec{Kind: ProbePort, Port: 0, Timeout: 45},
			Verify:   VerifySpec{Target: "backend", Evidence: []string{"pid", "port", "http_code"}, WaitSecs: 45},
		})
	}
	// mysql：数据库。high 档——数据类动作必须人工（plan §4.5）。
	if strings.TrimSpace(c.Projects.DBName) != "" {
		svcs = append(svcs, Service{
			ID:       "mysql",
			Label:    "MySQL 数据库",
			Adapter:  AdapterDB,
			RiskTier: RiskHigh,
			Probe:    ProbeSpec{Kind: ProbeCommand, Command: "mysqladmin ping", Timeout: 10},
			Verify:   VerifySpec{Target: "db", Evidence: []string{"version", "db_exists"}, WaitSecs: 60},
		})
	}
	// chain / webase：远程链端。**只在配了 SSH 主机时推导**——
	// 与 startGuardLoop 的开头判断一致（没有 SSH 主机就没有链端可守护）。
	if strings.TrimSpace(c.Chain.SSHHost) != "" {
		guard := GuardSpec{
			Group:        grpChain,
			AuditTarget:  c.Chain.SSHHost,
			IntervalSecs: c.Chain.ChainGuardSecs,
			Notify:       c.Chain.ChainNotify,
		}
		svcs = append(svcs, Service{
			ID:       "chain",
			Label:    "链节点",
			Adapter:  AdapterSSHChain,
			RiskTier: RiskLow,
			Probe:    ProbeSpec{Kind: ProbePort, Port: c.Chain.ChainPort, Timeout: 10},
			Recovery: "restart_chain",
			Guard:    guard,
			Verify: VerifySpec{
				Target:   "chain",
				Evidence: []string{"port", "node_proc", "block_height"},
				WaitSecs: 60,
			},
		})
		svcs = append(svcs, Service{
			ID:        "webase",
			Label:     "WeBASE-Front",
			Adapter:   AdapterSSHChain,
			RiskTier:  RiskLow,
			Probe:     ProbeSpec{Kind: ProbePort, Port: c.Chain.WebasePort, Timeout: 10},
			Recovery:  "restart_chain",
			Guard:     guard, // 同一组：与旧循环一致，整轮持链端组锁
			Verify:    VerifySpec{Target: "webase", Evidence: []string{"port"}, WaitSecs: 30},
			DependsOn: []string{"chain"}, // WeBASE 依赖链节点：链全宕时不各自恢复
		})
	}
	return svcs
}

// effectiveServices 生效的服务清单：配了用配的，没配按旧字段推导。
// **推导结果也过 validateServices**——旧字段本身可能写坏（端口越界等），
// 不能因为"是老配置"就跳过校验。
func effectiveServices(c Config) ([]Service, []string) {
	svcs := c.Services
	if len(svcs) == 0 {
		svcs = servicesFromLegacy(c)
	}
	return svcs, validateServices(svcs)
}

// ---------- 纯函数：校验与查表（无 IO，可单测） ----------

// knownAdapter 适配器是否已注册。
func knownAdapter(a string) bool {
	switch a {
	case AdapterLocal, AdapterDB, AdapterSSHChain:
		return true
	}
	return false
}

// knownRiskTier 风险等级是否合法。
func knownRiskTier(t string) bool {
	return t == RiskLow || t == RiskHigh
}

// knownProbeKind 探针种类是否合法。
func knownProbeKind(k string) bool {
	switch k {
	case ProbePort, ProbeHTTP, ProbeCommand, ProbeComposite:
		return true
	}
	return false
}

// serviceByID 在清单里按 id 找 service。找不到返回零值 + false。
// **不静默返回第一个**——找不到就当找不到，让调用方去报错。
func serviceByID(svcs []Service, id string) (Service, bool) {
	for _, s := range svcs {
		if s.ID == id {
			return s, true
		}
	}
	return Service{}, false
}

// verifyTargetOf 复验目标：显式填了用显式的，空则补成 service id。
// 这就是 v3.0.1 那个补口（备份类 target 为空 → "有事件"被判成"没事件"）
// 的配置侧根除：**在模型层保证 target 永不为空**，而不是在每个调用点补。
func verifyTargetOf(svc Service) string {
	if t := strings.TrimSpace(svc.Verify.Target); t != "" {
		return t
	}
	return svc.ID
}

// validateService 单个 service 的硬错误。返回空切片 = 合法。
// 消息直接给用户看，所以要说清改哪个字段、当前值是什么。
func validateService(s Service) []string {
	var bad []string
	add := func(format string, a ...any) {
		bad = append(bad, fmt.Sprintf(format, a...))
	}
	if strings.TrimSpace(s.ID) == "" {
		add("services[].id 不能为空（用来在审计与报告里标识这个服务）")
	}
	if strings.TrimSpace(s.Label) == "" {
		add("services[].label 不能为空（id=%s）", s.ID)
	}
	if !knownAdapter(s.Adapter) {
		add("services[].adapter 取值必须是 %s/%s/%s，当前「%s」（id=%s）",
			AdapterLocal, AdapterDB, AdapterSSHChain, s.Adapter, s.ID)
	}
	if !knownRiskTier(s.RiskTier) {
		add("services[].risk_tier 取值必须是 %s 或 %s，当前「%s」（id=%s）",
			RiskLow, RiskHigh, s.RiskTier, s.ID)
	}
	if !knownProbeKind(s.Probe.Kind) {
		add("services[].probe.kind 取值必须是 %s/%s/%s/%s，当前「%s」（id=%s）",
			ProbePort, ProbeHTTP, ProbeCommand, ProbeComposite, s.Probe.Kind, s.ID)
	}
	switch s.Probe.Kind {
	case ProbePort:
		// **0 = 未设置**，与 configschema.go 的 chkPort 约定一致：
		// web/backend 的候选端口由项目画像/scan_ports 推断，不是配置里写死的。
		// 把"未设置"判成"非法"会让所有老配置在升级后报错——
		// 那是误判，比漏判更坏（用户会去改本来正确的东西）。
		if s.Probe.Port != 0 && (s.Probe.Port < 1 || s.Probe.Port > 65535) {
			add("services[].probe.port 需在 1-65535（0 = 未设置，由项目画像推断），当前 %d（id=%s）",
				s.Probe.Port, s.ID)
		}
	case ProbeHTTP:
		if !hasURLScheme(s.Probe.URL) {
			add("services[].probe.url 必须以 http:// 或 https:// 开头，当前「%s」（id=%s）",
				s.Probe.URL, s.ID)
		}
	case ProbeCommand:
		if strings.TrimSpace(s.Probe.Command) == "" {
			add("services[].probe.command 不能为空（id=%s）", s.ID)
		}
	case ProbeComposite:
		if len(s.Probe.Parts) == 0 {
			add("services[].probe.parts 不能为空（kind=%s，id=%s）",
				ProbeComposite, s.ID)
		}
	}
	// 引用完整性：DependsOn / Recovery 指向的东西必须存在，
	// 否则"依赖健康检查"会静默变成"永远健康"。
	for _, d := range s.DependsOn {
		if strings.TrimSpace(d) == "" {
			add("services[].depends_on 不能有空条目（id=%s）", s.ID)
		}
	}
	if s.Recovery != "" && !strategyKnown(s.Recovery) {
		add("services[].recovery 引用了未注册的策略「%s」（id=%s）", s.Recovery, s.ID)
	}
	return bad
}

// validateServices 整份清单：逐项校验 + id 唯一性 + depends_on 指向存在。
// 唯一性必须查：重复 id 会让 serviceByID 永远返回第一个，
// 于是第二个 service 的探针/恢复/复验全部静默丢失。
func validateServices(svcs []Service) []string {
	var bad []string
	seen := map[string]bool{}
	for i := range svcs {
		for _, m := range validateService(svcs[i]) {
			bad = append(bad, m)
		}
		id := strings.TrimSpace(svcs[i].ID)
		if id == "" {
			continue
		}
		if seen[id] {
			bad = append(bad, fmt.Sprintf("services[].id 重复：「%s」（id 必须唯一，否则第二个同名服务的探针与恢复会静默丢失）", id))
		}
		seen[id] = true
	}
	for i := range svcs {
		for _, d := range svcs[i].DependsOn {
			d = strings.TrimSpace(d)
			if d == "" || d == svcs[i].ID {
				continue
			}
			if !seen[d] {
				bad = append(bad, fmt.Sprintf("services[%s].depends_on 指向不存在的 service「%s」", svcs[i].ID, d))
			}
		}
	}
	return bad
}
