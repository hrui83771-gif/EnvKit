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
	Components    []Component `json:"components"`
	AI            *AIConfig   `json:"ai,omitempty"`
	Alert         AlertConfig `json:"alerts"`
	// Recovery 恢复事务的两层节制参数（v3.0 T1.5）。零值 = 内置默认
	//（窗口 10 分钟 / 12 次 / per-target 3 次）。见 txbudget.go。
	Recovery RecoveryConfig `json:"recovery,omitempty"`
	// UpdateURL 自版本检查地址（返回 {"version":"x.y.z","notes":"..."} 的 JSON）。
	// 留空 = 关闭检查。无公开发布渠道时保持为空即可。
	UpdateURL string `json:"update_url"`
}

// RecoveryConfig 恢复事务的两层节制参数（v3.0 T1.5，修 G5/G6）。
// 两层分工见 txbudget.go：MaxActions 防"不同策略连环自动执行刷爆环境"，
// MaxAttempts 防"同一服务反复失败反复重启"。零值 = 内置默认
// （窗口 10 分钟 / 12 次 / per-target 3 次）。
type RecoveryConfig struct {
	MaxActions  int `json:"max_actions,omitempty"`  // 窗口内恢复类动作上限，0 = 12
	WindowSecs  int `json:"window_secs,omitempty"`  // 预算窗口秒数，0 = 600
	MaxAttempts int `json:"max_attempts,omitempty"` // per-target 连续失败上限，0 = 3
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
