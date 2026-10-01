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
	// UpdateURL 自版本检查地址（返回 {"version":"x.y.z","notes":"..."} 的 JSON）。
	// 留空 = 关闭检查。无公开发布渠道时保持为空即可。
	UpdateURL string `json:"update_url"`
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

type TaskHandle struct {
	ID        int
	Name      string
	Exclusive bool
	Since     time.Time
	Timeout   time.Duration

	killMu sync.Mutex
	kill   func()
	done   bool
}

// SetKill 注册"超时即终止"回调（只对短命前台命令注册，长驻服务不注册）。
func (h *TaskHandle) SetKill(fn func()) {
	if h == nil {
		return
	}
	h.killMu.Lock()
	h.kill = fn
	h.killMu.Unlock()
}

// Done 结束任务并释放锁；首次调用时记录任务耗时指标。
func (h *TaskHandle) Done() {
	if h == nil {
		return
	}
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
	auditNow(actSys, "task_timeout", h.Name, "", resFail,
		fmt.Sprintf("运行超过 %v 被看门狗终止", h.Timeout))
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

// beginTaskH 申请任务。exclusive=true 为写操作（互斥一切），false 为只读（可并发）。
func beginTaskH(name string, exclusive bool) (*TaskHandle, bool) {
	busyMu.Lock()
	defer busyMu.Unlock()
	for _, t := range tasks {
		if t.Exclusive || exclusive {
			return nil, false // 有独占任务，或本次是独占任务且已有任何任务在跑
		}
	}
	nextTask++
	h := &TaskHandle{ID: nextTask, Name: name, Exclusive: exclusive, Since: time.Now(), Timeout: taskWatchdogLimit}
	tasks[h.ID] = h
	return h, true
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
