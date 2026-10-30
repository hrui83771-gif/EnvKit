// state.go —— 全局类型/状态、任务互斥锁与看门狗、正则缓存、命令超时上下文
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 日志分区
const (
	scInstall = "install"
	scConfig  = "config"
	scChain   = "chain"
	scStart   = "start"
	scSys     = "sys"
	// scEval 评测区（设置页「开发者配置 → 评测」）。
	// 单独一个 scope：评测一次能吐几百行，混进 start/config 面板会把
	// 用户正在看的启动日志直接冲走。
	scEval = "eval"
)

type Proxy struct {
	HTTP  string `json:"http"`
	HTTPS string `json:"https"`
}

type MySQLConfig struct {
	Password    string `json:"password"`
	Port        int    `json:"port"`
	ServiceName string `json:"service_name"`
}

type Projects struct {
	FrontendDir string   `json:"frontend_dir"`
	BackendDir  string   `json:"backend_dir"`
	MySQLHost   string   `json:"mysql_host"`
	MySQLPort   int      `json:"mysql_port"`
	MySQLUser   string   `json:"mysql_user"`
	MySQLPass   string   `json:"mysql_password"`
	DBName      string   `json:"db_name"`
	SQLFile     string   `json:"sql_file"`
	BackupDir   string   `json:"backup_dir"`  // 备份目录；为空则用 exe 同级的 backups/
	ScanPorts   []int    `json:"scan_ports"`  // 端口占用诊断的端口清单；空则用内置默认
	WebScripts  []string `json:"web_scripts"` // 用户收藏的前端启动脚本（npm run <script>）
	// WebScript 指定前端启动脚本（v2.1）。留空 = 由 package.json 自动推断。
	// 即便填写也会过安全规则：deploy/migrate/reset 这类脚本名一律拒绝。
	WebScript string `json:"web_script,omitempty"`
	// BackendFile 指定后端入口（相对后端目录，如 main.go、cmd/server/main.go）。
	// 留空 = 自动推断；推断不出时报错而不是盲跑一条必然失败的命令。
	BackendFile string `json:"backend_file,omitempty"`
}

type Component struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	URL       string            `json:"url"`
	ZipTop    string            `json:"zip_top"`
	BinRel    string            `json:"bin_rel"`
	BinExe    string            `json:"bin_exe"`
	CheckCmd  string            `json:"check_cmd"`
	CheckArgs []string          `json:"check_args"`
	CheckRe   string            `json:"check_regex"`
	Special   string            `json:"special"`
	ExtraEnv  map[string]string `json:"extra_env"`
	SHA256    string            `json:"sha256,omitempty"` // 可选：下载文件 SHA256 校验（留空跳过）
}

type Config struct {
	// SchemaVersion 配置结构版本，用于迁移与兼容判断（见 configschema.go）。
	// 手写 config.json 时可以不填，加载时会按 0 → 最新逐级迁移。
	SchemaVersion int         `json:"schema_version"`
	AppName       string      `json:"app_name"`
	AppTitle      string      `json:"app_title"`
	InstallDir    string      `json:"install_dir"`
	Proxy         Proxy       `json:"proxy"`
	GoProxy       string      `json:"goproxy"`
	GoSumDB       string      `json:"gosumdb"`
	GoCGO         string      `json:"go_cgo"`
	NpmRegistry   string      `json:"npm_registry"`
	MySQL         MySQLConfig `json:"mysql"`
	Projects      Projects    `json:"projects"`
	Chain         ChainConfig `json:"chain"`
	// Services 拓扑服务清单（v3.0 第二阶段 T2.1，plan §4.1）。
	// **空 = 未启用拓扑模型**，此时按旧 Projects/Chain 扁平字段推导出等价清单
	// （servicesFromLegacy），行为与 v3.0.1 完全一致。
	// 非空 = 守护循环与 AI 恢复都按这份清单走，适配器由每项的 adapter 指定。
	Services   []Service   `json:"services,omitempty"`
	Components []Component `json:"components"`
	AI         *AIConfig   `json:"ai,omitempty"`
	Pet        PetConfig   `json:"pet"`
	Alert      AlertConfig `json:"alerts"`
	// Mission 行动（Agent 闭环 Phase 1，2026-10-26 定案）：
	// 巡检/晨报类只读能力默认开，入站触发端点默认关。见 mission.go MissionConfig。
	Mission MissionConfig `json:"missions"`
	// Recovery 恢复事务的两层节制参数（v3.0 T1.5）。零值 = 内置默认
	//（窗口 10 分钟 / 12 次 / per-target 3 次）。见 txbudget.go。
	Recovery RecoveryConfig `json:"recovery,omitempty"`
	// Dev 开发者配置（设置页「开发者配置」tab，v3.4）。指针段：老配置里没有
	// "dev" 段时为 nil，所有读取点走 devSnap() + getter 兜底，不判空就崩的写法一律不允许。
	// 老页面的 configBody() 不回传 dev，靠 mergeFormConfig 的 nil 保护不被自动保存抹掉。
	Dev *DevConfig `json:"dev,omitempty"`
	// UpdateURL 自版本检查地址。两种都认：
	//   ① 静态 JSON：{"version":"x.y.z","notes":"…"}
	//   ② GitHub Releases API：/repos/{owner}/{repo}/releases/latest
	//      （版本取 tag_name、说明取 body —— 发 release 就等于发布了版本信息，不用手工维护 json）
	// 留空 = 关闭检查。
	UpdateURL string `json:"update_url"`
	// Update 自更新检查的行为开关（v3.4，设置页「界面与更新」卡）。
	// 指针段：老配置没有 "update" 段时为 nil → 按"开启 + 每 3 天复查一次"处理
	// （v3.4 里是"只在启动查一次"，节奏太稀，跟着"自动检查更新"一起改成了定时）。
	Update *UpdateConfig `json:"update,omitempty"`
	// Notify 全局通知开关（v3.4）。指针段：老配置没有 "notify" 段时为 nil
	// → 按"不静音"处理，与之前行为完全一致。
	//
	// 只管「EnvKit 主动推送」这一类（告警、链端不可达、崩溃断路器、版本提示）；
	// 用户自己点了按钮的回执（"已在运行，已为你打开页面"、"快捷方式已创建"）
	// 不受影响 —— 那是操作结果，静音掉等于让人以为按钮坏了。
	Notify *NotifyConfig `json:"notify,omitempty"`
	// TrayHidden 上次退出前的窗口状态：true = 启动后直接隐藏到托盘（后台运行）。
	// 由托盘菜单「隐藏窗口到托盘 / 显示控制台窗口」写入，下次启动沿用，
	// 这样"挂机跑"不用每次开机都手动收一次窗口。
	TrayHidden bool `json:"tray_hidden,omitempty"`
}

// PetConfig 桌宠配置（config.json 的 "pet" 段）。
//
// 天气是可选能力：开启后桌宠会按需请求 wttr.in 查询当前城市天气。
// **默认关闭**——EnvKit 其余功能都不出网，只有这一个例外，
// 所以必须由用户显式打开，不能在后台悄悄外连。
type PetConfig struct {
	// WeatherEnabled 是否允许桌宠查询天气（默认 false）。
	WeatherEnabled bool `json:"weather_enabled"`
	// WeatherCity 城市名（wttr.in 的 location 段，支持拼音或中文，留空 = 按 IP 猜）。
	// 建议显式填写："Shanghai" / "上海" 均可。未填时默认"上海"（见 defaultWeatherCity）。
	WeatherCity string `json:"weather_city,omitempty"`
}

// UpdateConfig 自更新检查的行为开关（config.json 的 "update" 段，v3.4）。
//
// 为什么单独一段而不是塞进 dev：这是面向普通用户的基础设置（更新检查要不要开、
// 多久查一次），不是"调错了也不至于坏"的开发者调参；两者混在一段，
// 以后谁改默认值都得先判断这条属于哪边。
//
// 和 update_url 的分工：URL 是"查哪里"，这里管"查不查、多久查、哪个版本别烦我"。
type UpdateConfig struct {
	// Enabled nil = 开。用指针是为了保住老行为：v3.4 之前只要配了
	// update_url 就会检查，不能因为升级后多了个默认 false 的布尔就悄悄关掉。
	Enabled *bool `json:"enabled,omitempty"`
	// IntervalHours 定时复查间隔（小时）。nil = 内置默认（每 3 天一次）；
	// 显式 0 = 只在启动时查一次（v3.4 的老行为，保留给"不想定时外连"的人）；
	// >0 = 每 N 小时再查。改这个不用重启，updateLoop 每分钟醒一次自己算。
	//
	// 为什么从 int 改成 *int：int 下"没配过"和"配成 0"都是 0，就没法让
	// "默认自动复查"和"显式关掉定时复查"共存 —— 老配置里没有这个字段，
	// 升级后应当变成自动复查，而不是沿用"只在启动查一次"。
	IntervalHours *int `json:"interval_hours,omitempty"`
	// IgnoreVersion 不再提示"有新版本"的版本号（精确匹配 latest）。
	// 只抑制提示，不抑制检查本身 —— 界面上仍然能看到最新版号。
	IgnoreVersion string `json:"ignore_version,omitempty"`
}

// updateDefaultIntervalHours 未配置时的复查间隔：3 天。
//
// 为什么是 3 天而不是 1 天：这个检查要外连 GitHub，天天查既没必要也吵；
// 而一周一次又容易"版本发出去好几天了没人知道"。上限仍由 handleUpdateConfig 卡在 168。
const updateDefaultIntervalHours = 72

// interval 生效的复查间隔（小时）：nil → 默认 72，负数按 0，上限 168。
func (u UpdateConfig) interval() int {
	if u.IntervalHours == nil {
		return updateDefaultIntervalHours
	}
	v := *u.IntervalHours
	if v < 0 {
		return 0
	}
	if v > 168 {
		return 168
	}
	return v
}

// on 是否允许检查（nil 视为开）。
func (u UpdateConfig) on() bool { return u.Enabled == nil || *u.Enabled }

// defaultWeatherCity 天气城市的默认值。
//
// 放在这里而不是写死在 config.dist.json：用户已有的 config.json 里
// weather_city 是空串，改分发模板对他们无效——只有代码里的默认值
// 才能让"老配置 + 新版本"也吃到这个默认。
const defaultWeatherCity = "上海"

// RecoveryConfig 恢复事务的两层节制参数（v3.0 T1.5，修 G5/G6）。
// 两层分工见 txbudget.go：MaxActions 防"不同策略连环自动执行刷爆环境"，
// MaxAttempts 防"同一服务反复失败反复重启"。零值 = 内置默认
// （窗口 10 分钟 / 12 次 / per-target 3 次）。
// BreakerAfter/BreakerCooldownSecs 是崩溃断路器（v3.0 T1.9，plan §3.2.3）：
// 连续失败达限进熔断、冷静期后放行一次探针。零值 = 3 次 / 300 秒。
type RecoveryConfig struct {
	MaxActions          int `json:"max_actions,omitempty"`           // 窗口内恢复类动作上限，0 = 12
	WindowSecs          int `json:"window_secs,omitempty"`           // 预算窗口秒数，0 = 600
	MaxAttempts         int `json:"max_attempts,omitempty"`          // per-target 连续失败上限，0 = 3
	BreakerAfter        int `json:"breaker_after,omitempty"`         // 连续失败达限进熔断，0 = 3
	BreakerCooldownSecs int `json:"breaker_cooldown_secs,omitempty"` // 熔断冷静期秒数，0 = 300
}

// DevConfig 开发者配置（config.json 的 "dev" 段，设置页「开发者配置」tab）。
//
// 定位：装的全是"调错了不至于坏、且有内置默认兜底"的参数 ——
// 零值 = 内置默认（与 Recovery/Pet 段同一惯例），所以老配置里没有 dev 段也照常跑；
// 保存入口是独立的 /api/dev/config（handleDevConfig），老页面的整包表单保存
// 永远碰不到它（mergeFormConfig 的 nil 保护）。
// 本段不含任何密码 / APIKey：AI 密钥走 /api/ai/config + DPAPI，别往这里加。
type DevConfig struct {
	// —— AI 调参（滚动摘要 + 单轮超时，见 ai_history.go / ai_loop.go）——
	// SummaryAfterTokens 历史估算 token 超过该值才启用滚动摘要（0 = 默认 5000）。
	SummaryAfterTokens int `json:"summary_after_tokens,omitempty"`
	// SummaryKeepTokens 摘要后尾部原样保留的估算上限（0 = 默认 3000）。
	SummaryKeepTokens int `json:"summary_keep_tokens,omitempty"`
	// SummarizeKeepMsgs 摘要后至少原样保留的消息条数（0 = 默认 12）。
	SummarizeKeepMsgs int `json:"summarize_keep_msgs,omitempty"`
	// TurnTimeoutSecs 单轮上游最长等待秒数（0 = 默认 360 = 6 分钟）。
	// 调大容忍慢上游，调小更快发现假死 —— 但太小会把正常长推理判成超时。
	TurnTimeoutSecs int `json:"turn_timeout_secs,omitempty"`

	// —— 数据保留（天；0 = 内置默认）——
	// AuditRetentionDays 审计文件 audit-*.jsonl 的保留天数（0 = 30）。
	AuditRetentionDays int `json:"audit_retention_days,omitempty"`
	// TraceRetentionDays 轨迹文件 trace-*.jsonl 的保留天数（0 = 14）。
	// 轨迹比审计大得多、长期没有价值，所以默认比审计短。
	TraceRetentionDays int `json:"trace_retention_days,omitempty"`
	// ShadowRetentionDays 影子报告的保留天数（0 = 30，与审计同策略）。
	ShadowRetentionDays int `json:"shadow_retention_days,omitempty"`
	// MissionDeadlineDays 行动超期未收口自动收成 expired 的天数（0 = 30）。
	// 这只是防僵尸行动永远挂着的兜底，实际任务远用不到。
	MissionDeadlineDays int `json:"mission_deadline_days,omitempty"`
	// MissionKeepDays 终态行动的保留天数（0 = 30）。
	MissionKeepDays int `json:"mission_keep_days,omitempty"`

	// —— 控制台端口 ——
	// ServerPort 控制台起始端口（0 = 默认 18765，被占用则 findPort 顺延）。
	ServerPort int `json:"server_port,omitempty"`
	// PortScanLimit 端口顺延/单实例探测的上界（不含；0 = 默认 18815，
	// 即默认给 50 个顺延名额）。必须大于 ServerPort。
	PortScanLimit int `json:"port_scan_limit,omitempty"`

	// —— 日志配额（0 = 内置默认，见 loghub.go）——
	// 单机上日志是磁盘增长最快的来源。业务侧 stdout 更碎、重启更频繁，
	// 所以默认单文件给得比 EnvKit 自身宽、归档份数给得更少。
	// **运行时取值不走 devSnap()**：日志热路径持有 logMu，再取 cfgMu 会与
	// "持 cfgMu 时写日志"的路径形成锁序反转。改用 refreshLogQuota 写原子快照。
	LogMaxMB    int `json:"log_max_mb,omitempty"`     // 自身单文件上限（默认 5）
	SvcLogMaxMB int `json:"svc_log_max_mb,omitempty"` // 业务单文件上限（默认 8）
	LogKeep     int `json:"log_keep,omitempty"`       // 自身归档份数（默认 10）
	SvcLogKeep  int `json:"svc_log_keep,omitempty"`   // 业务归档份数（默认 5）

	// —— 启动校验等待窗口（秒；0 = 内置默认）——
	// 慢机器上首次 vite 编译可能超过 45 秒，窗口太短会把"还在编译"判成
	// "启动失败"，这是最伤的一类误判（把人引去修本来正常的东西）。
	VerifyWaitWebSecs     int `json:"verify_wait_web_secs,omitempty"`     // 默认 45
	VerifyWaitBackendSecs int `json:"verify_wait_backend_secs,omitempty"` // 默认 25

	// —— 子进程重试（"零输出秒退"，见 retry.go）——
	// 只影响命令执行与 mysql 查询；数据库备份（dbops.go）固定 2 次不跟这里走，
	// 因为那条路径 AnyErr=true（真报错也重试），放大次数只会拖长失败时间。
	SubprocRetries     int `json:"subproc_retries,omitempty"`      // 总尝试次数（默认 3）
	SubprocBackoffSecs int `json:"subproc_backoff_secs,omitempty"` // 每次重试前等待（默认 2）

	// —— AI 读取 ——
	// ExploreMaxKB 单文件读取上限（KB，默认 64）。调大可让 AI 读全大文件，
	// 但内容会整段进上下文，长文件更容易触发摘要。
	ExploreMaxKB int `json:"explore_max_kb,omitempty"`
}

// 开发者段的内置默认值：与界面上「恢复默认」的目标值是同一份数字，
// 改这里就等于改默认 —— 不要在别处再写第二份字面量。
const (
	// 摘要触发阈值：历史估算过这么多 token 才压一次。
	//
	// 5000 → 10000（2026-10-30 用户反馈"压缩频率有点高"）。原因是压缩后水位并不低：
	// keepTokens 3000 + 摘要本身 400 字 + 之后每轮工具结果动辄几百上千 token，
	// 旧阈值下压完只剩约 2000 token 余量，一两轮就又触发 —— 每压一次都要先等一次
	// 前置 LLM 调用（十几秒），体感就是"老在压缩"。
	// 现在压完水位约 3400/10000 ≈ 34%，余量够走 4~6 轮工具；模型上下文（64k+）远没到边。
	devDefaultSummaryAfterTokens = 10000
	devDefaultSummaryKeepTokens  = 3000
	devDefaultSummarizeKeepMsgs  = 12
	devDefaultTurnTimeoutSecs    = 360
	devDefaultAuditRetentionDays = 30
	devDefaultTraceRetentionDays = 14
	devDefaultShadowRetentionDay = 30
	devDefaultMissionDeadlineDay = 30
	devDefaultMissionKeepDay     = 30
	devDefaultServerPort         = 18765
	devDefaultPortScanSpan       = 50 // 从起始端口起的顺延名额数（上界 = 起始 + 50）

	// 日志配额（loghub.go 的同名常量直接引用这里，避免两份默认值各说各话）
	devDefaultLogMaxMB    = 5
	devDefaultSvcLogMaxMB = 8
	devDefaultLogKeep     = 10
	devDefaultSvcLogKeep  = 5

	// 启动校验等待窗口（verify.go 的 verifyWaitWeb()/verifyWaitBackend() 用它）
	devDefaultVerifyWaitWeb     = 45
	devDefaultVerifyWaitBackend = 25

	// 子进程"零输出秒退"重试（retry.go 的 subprocRetry() 用它）
	devDefaultSubprocRetries = 3
	devDefaultSubprocBackoff = 2 // 秒

	// AI 单文件读取上限（explore.go 用它）
	devDefaultExploreMaxKB = 64
)

// devOr 取正数，否则回落默认 —— 0 与负数都视为"没设"。
func devOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// resolved 返回"填好默认值的副本"：给设置页回显用 —— 用户看到的应该是
// 生效值（数字），而不是一排空输入框。
func (d DevConfig) resolved() DevConfig {
	d.SummaryAfterTokens = devOr(d.SummaryAfterTokens, devDefaultSummaryAfterTokens)
	d.SummaryKeepTokens = devOr(d.SummaryKeepTokens, devDefaultSummaryKeepTokens)
	d.SummarizeKeepMsgs = devOr(d.SummarizeKeepMsgs, devDefaultSummarizeKeepMsgs)
	d.TurnTimeoutSecs = devOr(d.TurnTimeoutSecs, devDefaultTurnTimeoutSecs)
	d.AuditRetentionDays = devOr(d.AuditRetentionDays, devDefaultAuditRetentionDays)
	d.TraceRetentionDays = devOr(d.TraceRetentionDays, devDefaultTraceRetentionDays)
	d.ShadowRetentionDays = devOr(d.ShadowRetentionDays, devDefaultShadowRetentionDay)
	d.MissionDeadlineDays = devOr(d.MissionDeadlineDays, devDefaultMissionDeadlineDay)
	d.MissionKeepDays = devOr(d.MissionKeepDays, devDefaultMissionKeepDay)
	d.ServerPort = devOr(d.ServerPort, devDefaultServerPort)
	d.PortScanLimit = devOr(d.PortScanLimit, d.ServerPort+devDefaultPortScanSpan)
	d.LogMaxMB = devOr(d.LogMaxMB, devDefaultLogMaxMB)
	d.SvcLogMaxMB = devOr(d.SvcLogMaxMB, devDefaultSvcLogMaxMB)
	d.LogKeep = devOr(d.LogKeep, devDefaultLogKeep)
	d.SvcLogKeep = devOr(d.SvcLogKeep, devDefaultSvcLogKeep)
	d.VerifyWaitWebSecs = devOr(d.VerifyWaitWebSecs, devDefaultVerifyWaitWeb)
	d.VerifyWaitBackendSecs = devOr(d.VerifyWaitBackendSecs, devDefaultVerifyWaitBackend)
	d.SubprocRetries = devOr(d.SubprocRetries, devDefaultSubprocRetries)
	d.SubprocBackoffSecs = devOr(d.SubprocBackoffSecs, devDefaultSubprocBackoff)
	d.ExploreMaxKB = devOr(d.ExploreMaxKB, devDefaultExploreMaxKB)
	return d
}

// 读取侧兜底（手写 config.json 把某项改成 0/负数时不至于读出 0）。
// 与 resolved() 分工：resolved 给界面回显，这些给运行时取值。
func (d DevConfig) summaryAfterTokens() int {
	return devOr(d.SummaryAfterTokens, devDefaultSummaryAfterTokens)
}
func (d DevConfig) summaryKeepTokens() int {
	return devOr(d.SummaryKeepTokens, devDefaultSummaryKeepTokens)
}
func (d DevConfig) summarizeKeepMsgs() int {
	return devOr(d.SummarizeKeepMsgs, devDefaultSummarizeKeepMsgs)
}
func (d DevConfig) turnTimeout() time.Duration {
	return time.Duration(devOr(d.TurnTimeoutSecs, devDefaultTurnTimeoutSecs)) * time.Second
}
func (d DevConfig) auditRetentionDays() int {
	return devOr(d.AuditRetentionDays, devDefaultAuditRetentionDays)
}
func (d DevConfig) traceRetentionDays() int {
	return devOr(d.TraceRetentionDays, devDefaultTraceRetentionDays)
}
func (d DevConfig) shadowRetentionDays() int {
	return devOr(d.ShadowRetentionDays, devDefaultShadowRetentionDay)
}
func (d DevConfig) missionDeadline() time.Duration {
	return time.Duration(devOr(d.MissionDeadlineDays, devDefaultMissionDeadlineDay)) * 24 * time.Hour
}
func (d DevConfig) missionKeep() time.Duration {
	return time.Duration(devOr(d.MissionKeepDays, devDefaultMissionKeepDay)) * 24 * time.Hour
}
func (d DevConfig) serverPort() int {
	p := devOr(d.ServerPort, devDefaultServerPort)
	if p > 65535 {
		return devDefaultServerPort
	}
	return p
}
func (d DevConfig) portScanLimit() int {
	start := d.serverPort()
	if d.PortScanLimit > start {
		return d.PortScanLimit
	}
	return start + devDefaultPortScanSpan
}

// logQuotaOf 日志配额（字节数）。**只允许 refreshLogQuota 调用**：
// 写日志的热路径不许碰 cfgMu —— 别处存在"持着 cfgMu 写日志"的路径，
// 谁要是在 logMu 下再取 cfgMu，就是锁序反转，偶发死锁且极难复现。
func (d DevConfig) logQuotaOf() logQuota {
	return logQuota{
		keep:    devOr(d.LogKeep, devDefaultLogKeep),
		svcKeep: devOr(d.SvcLogKeep, devDefaultSvcLogKeep),
		max:     int64(devOr(d.LogMaxMB, devDefaultLogMaxMB)) << 20,
		svcMax:  int64(devOr(d.SvcLogMaxMB, devDefaultSvcLogMaxMB)) << 20,
	}
}

// devSnap 开发者段快照：所有"常量 → 配置"的运行时读取点都走这里。
// cfgMu 只在取值这一瞬持有、不嵌套任何其它锁；调用点也不得持有
// cfgMu / aiCfgMu / msMu（T1 锁序审计逐一确认过：不存在反向嵌套路径）。
func devSnap() DevConfig {
	cfgMu.Lock()
	var d DevConfig
	if cfg.Dev != nil {
		d = *cfg.Dev
	}
	cfgMu.Unlock()
	return d
}

type CheckResult struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Location  string `json:"location"`
	Detail    string `json:"detail"`
}

type ProgStatus struct {
	Running bool   `json:"running"`
	Ok      bool   `json:"ok"`
	Msg     string `json:"msg"`
	At      string `json:"at"`
}

type LogMsg struct {
	Scope string `json:"scope"`
	Src   string `json:"src"` // 进程来源：web / backend（用于启动页分栏）
	Line  string `json:"line"`
}

type DBHealth struct {
	Connected bool   `json:"connected"`
	DBExists  bool   `json:"dbExists"`
	Version   string `json:"version"`
	Msg       string `json:"msg"`
	At        string `json:"at"`
}

type SvcInfo struct {
	Running bool   `json:"running"`
	URL     string `json:"url"`
	PID     int    `json:"pid"`
	Since   string `json:"since"`

	// v2.7：把状态源的复验结论一并带出来。
	//
	// 此前 /api/health 的 services 只回 running/url/pid/since，
	// **phase 与 verified 被兼容层丢掉了** ——于是前端只能按 running 上色，
	// 端口被别人的服务占着、进程在但没复验也一样显示绿。
	// 那等于把「执行不等于成功」在视觉上抹掉。
	//
	// 追加而不替换：旧字段原样保留，既有 CDP 测试与外部调用零影响。
	Phase    ServicePhase `json:"phase,omitempty"`
	Verified bool         `json:"verified"`
	// LastVerify 是人类可读的复验结论，用于在卡上直接显示
	// 「端口被其他进程占用」这类具体原因，而不是只显示一个颜色。
	LastVerify string `json:"last_verify,omitempty"`
	Restarts   int    `json:"restarts,omitempty"`
	// v3.0 T1.9：断路器熔断态（plan §3.2.3）。熔断中 = 自动重启被拒绝，
	// /api/health 兼容层也带出来，旧前端不破、新前端可显示。
	BreakerTripped    bool `json:"breaker_tripped,omitempty"`
	BreakerRemainSecs int  `json:"breaker_remain_secs,omitempty"`
}

var (
	cfg       Config
	cfgMu     sync.Mutex
	results   = map[string]CheckResult{}
	resultsMu sync.Mutex
	phase     = "idle" // idle | checking | checked | installing | done
	phaseMu   sync.Mutex
	hub       = NewHub()

	progMu        sync.Mutex
	progState     = map[string]ProgStatus{}
	procMu        sync.Mutex
	activeProcs   = map[string]*exec.Cmd{}
	stoppingProcs = map[string]bool{} // 主动停止中的服务：监控 goroutine Wait 返回后不得覆盖"已停止"（防竞态误报崩溃）

	dbMu     sync.Mutex
	dbHealth = DBHealth{Msg: "未检测"}

	svcMu    sync.Mutex
	svcState = map[string]*SvcInfo{"web": {Running: false}, "backend": {Running: false}}

	isAdminFlag = false
)

// ---------- 任务模型 ----------
// 旧模型是"全局单锁 + 一个字符串"，问题：卡死任务只能放锁不能收尸，
// 且只读操作（检测、诊断）也被写操作一起挡住。
// 新模型：句柄 + 读写分类——
//   · 独占任务（写操作）：同一时刻只允许一个，存在时连只读任务也拒绝；
//   · 共享任务（只读操作）：可并发，但遇到独占任务时拒绝；
//   · 每个任务可注册 kill 回调，看门狗超时时真正终止子进程，而不是只放锁。
//
// v3.0 T1.2：再加**资源组**维度。全局锁的粒度太粗——"启动前端"会把
// "启动后端"一起挡掉，而两者本可并行；stop 服务则完全不走锁，
// 与并发的 start 竞态（G2）。资源组规则：
//   · 任务声明自己动的资源（web / backend / chain / db，见下方常量）；
//   · 同组内：写任务互斥一切，读任务之间可并行；
//   · 不同组之间互不冲突（start web 与 start backend 并行跑）；
//   · 不声明组 = 全局任务（安装/升级/清杀软这类维护动作，与所有任务冲突）；
//   · 一次动作可持多组（stop all = web+backend），**成组原子获取**，
//     避免"拿着 web 等 backend"与别人"拿着 backend 等 web"对撮成死锁。

type TaskHandle struct {
	ID        int
	Name      string
	Exclusive bool
	Since     time.Time
	Timeout   time.Duration
	// Groups 本次任务持有的资源组；空 = 全局任务（与所有任务冲突）。
	Groups []string

	killMu sync.Mutex
	kill   func()
	done   bool

	// 操作结果：任务无论成功失败都写入，超时/watchdog 终止时写入 timeout 结果。
	// 用途一是审计溯源，二是让未来基于句柄读取结果的代码有唯一真源。
	resMu     sync.Mutex
	result    OpResult
	resSet    bool
	resClosed bool // 句柄已关闭（Done/terminate）：迟到的 SetResult 不再覆盖结论
	// TimeoutFact 超时核对钩子（v3.0 T1.4）：看门狗杀掉子进程后，由任务
	// 注册的核对函数给出"实际状态"事实（已退出 / 仍在运行未复验）。
	// 不猜——超时之后进程可能已经起来了，直接记"超时失败"是谎报；
	// 而"可能起来了"这种第三种措辞不允许存在。
	TimeoutFact func() string
}

// 资源组名。恢复事务按组互斥：组内写任务串行，组间并行。
const (
	grpWeb     = "web"
	grpBackend = "backend"
	grpChain   = "chain"
	grpDB      = "db"
)

// SetKill 注册"超时即终止"回调（只对短命前台命令注册，长驻服务不注册）。
func (h *TaskHandle) SetKill(fn func()) {
	if h == nil {
		return
	}
	h.killMu.Lock()
	h.kill = fn
	h.killMu.Unlock()
}

// SetResult 记录本次任务的最终结果（后写覆盖前写，取最后一次有效结论）。
// 例外：句柄已关闭（正常完成或被看门狗终止）之后迟到的写入一律忽略——
// 否则被超时杀掉的任务会在 opStartXxx 返回后又把 timeout 结论覆盖成
// "build_fail"，审计与调用方从此以为它只是编译失败。
func (h *TaskHandle) SetResult(r OpResult) {
	if h == nil {
		return
	}
	h.resMu.Lock()
	if h.resClosed {
		h.resMu.Unlock()
		return
	}
	h.result, h.resSet = r, true
	h.resMu.Unlock()
}

// Result 取任务结果；set 为 false 表示任务尚未写入任何结论。
func (h *TaskHandle) Result() (r OpResult, set bool) {
	if h == nil {
		return OpResult{}, false
	}
	h.resMu.Lock()
	defer h.resMu.Unlock()
	return h.result, h.resSet
}

// Done 结束任务并释放锁；首次调用时记录任务耗时指标。
func (h *TaskHandle) Done() {
	if h == nil {
		return
	}
	h.resMu.Lock()
	h.resClosed = true
	h.resMu.Unlock()
	h.killMu.Lock()
	first := !h.done
	h.kill, h.done = nil, true
	h.killMu.Unlock()
	busyMu.Lock()
	if _, ok := tasks[h.ID]; ok {
		delete(tasks, h.ID)
	}
	busyMu.Unlock()
	if first {
		metTask(h.Name, time.Since(h.Since), "ok")
	}
}

// terminate 超时处理：先杀子进程，再放锁，并留审计与告警。
func (h *TaskHandle) terminate() {
	if h == nil {
		return
	}
	h.killMu.Lock()
	fn := h.kill
	h.kill = nil
	first := !h.done
	h.done = true // 预标记：随后 Done() 不再重复记 "ok" 指标
	h.killMu.Unlock()
	if first {
		metTask(h.Name, time.Since(h.Since), "timeout")
	}
	if fn != nil {
		fn()
		warn(scSys, "任务", "任务「%s」超时，已终止其子进程", h.Name)
	} else {
		warn(scSys, "任务", "任务「%s」超时，已释放任务锁（该任务未注册可终止的子进程，请手动检查）", h.Name)
	}
	// v3.0 T1.4：超时后强制核对实际状态。"以为超时其实成功了"
	// 在旧实现里只能靠下一次复验偶然发现。核对结论只允许两种事实：
	// 已退出，或仍在运行未复验——不存在"可能起来了"这第三种。
	fact := ""
	if h.TimeoutFact != nil {
		fact = h.TimeoutFact()
	}
	detail := fmt.Sprintf("运行超过 %v 被看门狗终止", h.Timeout)
	timeoutMsg := fmt.Sprintf("任务「%s」运行超过 %v 被看门狗终止", h.Name, h.Timeout)
	if fact != "" {
		detail += "；超时核对：" + fact
		timeoutMsg += "（核对：" + fact + "）"
	}
	auditNow(actSys, "task_timeout", h.Name, "", resFail, detail)
	// 超时也是一种"未完成"：写进句柄结果，避免调用方把被杀掉的任务当成成功。
	h.SetResult(opFail("task", h.Name, errKindTimeout, timeoutMsg))
	alertDispatch("EnvKit 任务超时",
		fmt.Sprintf("任务「%s」运行超过 %v 被看门狗终止，请检查对应子进程。", h.Name, h.Timeout))
	h.Done()
}

var (
	busyMu   sync.Mutex
	tasks    = map[int]*TaskHandle{}
	nextTask int
)

// taskWatchdogLimit 单个任务的默认最长存活时间。
const taskWatchdogLimit = 15 * time.Minute

// taskGroupsConflict 两组资源 + 读写属性是否冲突。
// 规则（与 TaskHandle 注释一致）：
//
//	· 两个只读永不冲突；
//	· 全局任务（groups 为空）与任何任务冲突；
//	· 否则按组名相交判定。
func taskGroupsConflict(a []string, aWrite bool, b []string, bWrite bool) bool {
	if !aWrite && !bWrite {
		return false
	}
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// beginTaskHG 按资源组申请任务。groups 为空 = 全局任务（旧语义）。
// 多组一次性原子判定：要么全拿到，要么全不拿——不存在"拿到一半"的中间态。
func beginTaskHG(name string, groups []string, exclusive bool) (*TaskHandle, bool) {
	busyMu.Lock()
	defer busyMu.Unlock()
	for _, t := range tasks {
		if taskGroupsConflict(t.Groups, t.Exclusive, groups, exclusive) {
			return nil, false
		}
	}
	nextTask++
	h := &TaskHandle{
		ID: nextTask, Name: name, Exclusive: exclusive,
		Groups: append([]string(nil), groups...),
		Since:  time.Now(), Timeout: taskWatchdogLimit,
	}
	tasks[h.ID] = h
	return h, true
}

// beginTaskH 申请任务（全局锁语义：与所有任务互斥）。
// 保留旧签名——安装/升级/白名单这类维护动作本就该全局互斥，
// 逐处改成组锁反而稀释了它们的保护。
func beginTaskH(name string, exclusive bool) (*TaskHandle, bool) {
	return beginTaskHG(name, nil, exclusive)
}

// taskGroupsOf 任务持有的组（副本）；全局任务返回空。
func (h *TaskHandle) taskGroupsOf() []string {
	if h == nil {
		return nil
	}
	return append([]string(nil), h.Groups...)
}

// busyReason 锁被拒时给人看的原因：当前谁在占着什么。
// 只说"有任务正在执行"等于没说——用户需要知道该等哪个。
func busyReason(want string) string {
	names := activeTaskNames()
	if len(names) == 0 {
		return "有任务正在执行，请稍候"
	}
	return "有任务正在执行（" + strings.Join(names, " / ") + "），请稍候"
}

// beginTask 写操作任务（互斥）。
func beginTask(name string) bool {
	_, ok := beginTaskH(name, true)
	return ok
}

// beginTaskRO 只读任务（可与其它只读任务并发）。
func beginTaskRO(name string) bool {
	_, ok := beginTaskH(name, false)
	return ok
}

func startTaskWatchdog() {
	go func() {
		for {
			time.Sleep(15 * time.Second)
			now := time.Now()
			busyMu.Lock()
			var overdue []*TaskHandle
			for _, t := range tasks {
				if now.Sub(t.Since) > t.Timeout {
					overdue = append(overdue, t)
				}
			}
			busyMu.Unlock()
			for _, t := range overdue {
				t.terminate()
			}
		}
	}()
}

var reCache sync.Map // pattern -> *regexp.Regexp

// reOf 返回缓存编译的正则：aiMatchIntent / aiLooksLikeMenu 等在每轮对话里会反复匹配，
// 直接 regexp.MustCompile 会重复编译同一个模式（十几次/轮），这里缓存一次即可。
// 模式写错时不再 panic（那会打断整条请求），退化为"永不匹配"并留一条日志。
func reOf(pattern string) *regexp.Regexp {
	if v, ok := reCache.Load(pattern); ok {
		return v.(*regexp.Regexp)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		warn(scSys, "正则", "模式编译失败（已按不匹配处理）：%v", err)
		re = regexp.MustCompile(`a^`)
	}
	reCache.Store(pattern, re)
	return re
}

// cmdContext 给外部命令加超时（d<=0 表示不设超时），避免个别命令挂起导致任务锁被永久占用
func cmdContext(d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), d)
}

// currentTask 返回当前正在跑的任务名（多个用 " / " 连接，无任务为空串）。
func currentTask() string {
	busyMu.Lock()
	defer busyMu.Unlock()
	if len(tasks) == 0 {
		return ""
	}
	names := make([]string, 0, len(tasks))
	for _, t := range tasks {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return strings.Join(names, " / ")
}

// activeTaskNames 当前任务名列表（诊断报告用）。
func activeTaskNames() []string {
	busyMu.Lock()
	defer busyMu.Unlock()
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Name)
	}
	return out
}

// ---------- 主流程 ----------
var apiToken string // 本机 API 令牌：启动时随机生成注入页面，防浏览器 CSRF

// 令牌治理：令牌不再"永不过期"。TTL 到期后自动轮换，旧令牌立即失效，
// 页面收到 403 会自行刷新并拿到新令牌。这样即使令牌曾经通过浏览器历史、
// Referer 或截图外泄，可滥用窗口也被限制在 TTL 之内。
const apiTokenTTL = 12 * time.Hour

var (
	tokenMu     sync.Mutex
	tokenIssued time.Time
)

// newToken 生成一个新的随机令牌。
func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// currentToken 返回当前有效令牌；超过 TTL 就轮换。
// serveIndex 与 authMW 都必须通过它取值，不能直接读 apiToken 变量。
func currentToken() string {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if tokenIssued.IsZero() {
		tokenIssued = time.Now()
	}
	if time.Since(tokenIssued) > apiTokenTTL {
		apiToken = newToken()
		tokenIssued = time.Now()
		warn(scSys, "安全", "API 令牌已超过 %v，已自动轮换（旧令牌失效，页面会自动刷新）", apiTokenTTL)
	}
	return apiToken
}
