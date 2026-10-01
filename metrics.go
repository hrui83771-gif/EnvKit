// metrics.go —— /api/metrics（Prometheus 文本格式）+ 运行指标采集
//
// 为什么不用 prometheus/client_golang：单文件、零依赖是本工具的硬约束（v1.7 评审 P2）。
// 这里手写了 8 个左右核心指标：请求数、任务数/耗时/超时、子进程重试、链端自动恢复、
// 审计事件量、告警发送量 —— 覆盖"这个工具有没有人用、卡不卡、链端稳不稳、告警响不响"。
package main

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// runtimeVersion 返回编译本二进制的 Go 版本（如 go1.25.0），用于 envkit_build_info。
func runtimeVersion() string { return strings.TrimPrefix(runtime.Version(), "go") }

type metricsState struct {
	mu          sync.Mutex
	startedAt   time.Time
	httpTotal   map[string]int64   // path → count（不含 query）
	httpStatus  map[string]int64   // code → count
	taskTotal   map[string]int64   // "task\x00result" → count
	taskSeconds map[string]float64 // task → seconds sum
	retries     int64              // 子进程零输出秒退重试次数
	timeouts    int64              // 看门狗终止的任务数
	chainRecov  int64              // 链端自动恢复次数
	auditTotal  map[string]int64   // "actor\x00action\x00result" → count
	alertTotal  map[string]int64   // "channel\x00result" → count
}

var met = newMetrics()

func newMetrics() *metricsState {
	return &metricsState{
		startedAt:   time.Now(),
		httpTotal:   map[string]int64{},
		httpStatus:  map[string]int64{},
		taskTotal:   map[string]int64{},
		taskSeconds: map[string]float64{},
		auditTotal:  map[string]int64{},
		alertTotal:  map[string]int64{},
	}
}

// ---------- 埋点入口（各模块调用，锁内只做 map 自增，开销可忽略） ----------

func metHTTP(path string, code int) {
	met.mu.Lock()
	met.httpTotal[path]++
	met.httpStatus[strconv.Itoa(code)]++
	met.mu.Unlock()
}

// metTask 任务结束埋点。result: "ok" / "timeout"。
func metTask(name string, d time.Duration, result string) {
	met.mu.Lock()
	met.taskTotal[name+"\x00"+result]++
	if result == "timeout" {
		met.timeouts++
	}
	met.taskSeconds[name] += d.Seconds()
	met.mu.Unlock()
}

func metRetry() {
	met.mu.Lock()
	met.retries++
	met.mu.Unlock()
}

func metAudit(actor, action, result string) {
	met.mu.Lock()
	met.auditTotal[actor+"\x00"+action+"\x00"+result]++
	met.mu.Unlock()
}

func metAlert(channel, result string) {
	met.mu.Lock()
	met.alertTotal[channel+"\x00"+result]++
	met.mu.Unlock()
}

func metChainRecover() {
	met.mu.Lock()
	met.chainRecov++
	met.mu.Unlock()
}

// ---------- 渲染 ----------

func escLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

type metricLine struct {
	name   string
	help   string
	typ    string // counter / gauge
	labels []string
	value  string
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	var lines []metricLine
	add := func(name, help, typ string, labels []string, value any) {
		var v string
		switch x := value.(type) {
		case int:
			v = strconv.Itoa(x)
		case int64:
			v = strconv.FormatInt(x, 10)
		case float64:
			v = strconv.FormatFloat(x, 'g', -1, 64)
		default:
			return
		}
		lines = append(lines, metricLine{name: name, help: help, typ: typ, labels: labels, value: v})
	}
	lb := func(kv ...string) string {
		if len(kv) == 0 {
			return ""
		}
		parts := make([]string, 0, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			parts = append(parts, escLabel(kv[i])+`="`+escLabel(kv[i+1])+`"`)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}

	met.mu.Lock()
	uptime := time.Since(met.startedAt).Seconds()
	httpTotal := cloneMap(met.httpTotal)
	httpStatus := cloneMap(met.httpStatus)
	taskTotal := cloneMap(met.taskTotal)
	taskSeconds := cloneMap(met.taskSeconds)
	retries, timeouts, chainRecov := met.retries, met.timeouts, met.chainRecov
	auditTotal := cloneMap(met.auditTotal)
	alertTotal := cloneMap(met.alertTotal)
	met.mu.Unlock()

	busyMu.Lock()
	activeTasks := len(tasks)
	busyMu.Unlock()

	add("envkit_up", "本服务是否存活", "gauge", []string{lb()}, 1)
	add("envkit_uptime_seconds", "自启动以来的秒数", "gauge", []string{lb()}, uptime)
	add("envkit_build_info", "版本信息", "gauge",
		[]string{lb("version", appVersion, "go", runtimeVersion())}, 1)

	paths := sortedKeys(httpTotal)
	for _, p := range paths {
		add("envkit_http_requests_total", "HTTP 请求总数", "counter",
			[]string{lb("path", p)}, httpTotal[p])
	}
	codes := sortedKeys(httpStatus)
	for _, c := range codes {
		add("envkit_http_status_total", "HTTP 响应状态码计数", "counter",
			[]string{lb("code", c)}, httpStatus[c])
	}
	tasksSorted := make([]string, 0, len(taskTotal))
	for k := range taskTotal {
		tasksSorted = append(tasksSorted, k)
	}
	sort.Strings(tasksSorted)
	for _, k := range tasksSorted {
		parts := strings.SplitN(k, "\x00", 2)
		name, result := parts[0], ""
		if len(parts) == 2 {
			result = parts[1]
		}
		add("envkit_task_total", "任务执行计数", "counter",
			[]string{lb("task", name, "result", result)}, taskTotal[k])
	}
	for name, secs := range taskSeconds {
		add("envkit_task_seconds_sum", "任务累计耗时（秒）", "gauge",
			[]string{lb("task", name)}, secs)
	}
	add("envkit_task_active", "当前进行中的任务数", "gauge", []string{lb()}, activeTasks)
	add("envkit_task_retries_total", "子进程零输出秒退重试总数", "counter", []string{lb()}, retries)
	add("envkit_task_timeouts_total", "看门狗终止的任务总数", "counter", []string{lb()}, timeouts)
	add("envkit_chain_recoveries_total", "链端自动恢复总数", "counter", []string{lb()}, chainRecov)

	auditKeys := sortedKeys(auditTotal)
	for _, k := range auditKeys {
		parts := strings.SplitN(k, "\x00", 3)
		actor, action, result := "", "", ""
		if len(parts) > 0 {
			actor = parts[0]
		}
		if len(parts) > 1 {
			action = parts[1]
		}
		if len(parts) > 2 {
			result = parts[2]
		}
		add("envkit_audit_events_total", "审计事件计数", "counter",
			[]string{lb("actor", actor, "action", action, "result", result)}, auditTotal[k])
	}
	alertKeys := sortedKeys(alertTotal)
	for _, k := range alertKeys {
		parts := strings.SplitN(k, "\x00", 2)
		channel, result := "", ""
		if len(parts) > 0 {
			channel = parts[0]
		}
		if len(parts) > 1 {
			result = parts[1]
		}
		add("envkit_alert_total", "告警发送计数", "counter",
			[]string{lb("channel", channel, "result", result)}, alertTotal[k])
	}

	// 链端状态 gauge（只读快照，不发起任何网络请求）
	chainMu.Lock()
	ci := chainInfo
	chainMu.Unlock()
	if ci.Checked {
		nodeUp := 0
		if ci.Port20200 {
			nodeUp = 1
		}
		add("envkit_chain_node_up", "链节点 20200 端口是否存活（最近一次检测）", "gauge", []string{lb()}, nodeUp)
		add("envkit_chain_webase_up", "WeBASE-Front 5002 是否存活（最近一次检测）", "gauge", []string{lb()}, boolGauge(ci.Port5002))
		if n, err := strconv.ParseInt(strings.TrimSpace(ci.BlockNumber), 10, 64); err == nil {
			add("envkit_chain_block_number", "链区块高度（最近一次检测）", "gauge", []string{lb()}, n)
		}
		add("envkit_chain_latency_ms", "节点端口探测耗时（毫秒，最近一次检测）", "gauge", []string{lb()}, ci.Lat20200)
	}

	// 磁盘剩余空间（exe 所在盘）—— CentOS 上 node1 日志撑爆磁盘的教训
	if free, ok := diskFreeBytes(); ok {
		add("envkit_disk_free_bytes", "exe 所在分区剩余空间（字节）", "gauge", []string{lb()}, free)
	}

	var b strings.Builder
	b.WriteString("# EnvKit metrics\n")
	last := ""
	for _, l := range lines {
		if l.name != last {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", l.name, l.help, l.name, l.typ)
			last = l.name
		}
		fmt.Fprintf(&b, "%s%s %s\n", l.name, strings.Join(l.labels, ""), l.value)
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func boolGauge(v bool) int {
	if v {
		return 1
	}
	return 0
}

func cloneMap[V any](m map[string]V) map[string]V {
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sortedKeys(m map[string]int64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// metricsMW 统计每个请求的路径与状态码（放在 recoverMW 外、securityMW 内）。
func metricsMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		metHTTP(r.URL.Path, rec.status)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
