// loghub.go —— 实时日志 Hub（SSE 广播）+ 落盘轮转 + 分级日志函数
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------- 实时日志 Hub (SSE) ----------
type Hub struct {
	mu      sync.Mutex
	subs    []chan LogMsg
	history []LogMsg
}

func NewHub() *Hub { return &Hub{history: make([]LogMsg, 0, 1000)} }

func (h *Hub) Subscribe() chan LogMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan LogMsg, 256)
	h.subs = append(h.subs, ch)
	return ch
}

func (h *Hub) Unsubscribe(ch chan LogMsg) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, s := range h.subs {
		if s == ch {
			h.subs = append(h.subs[:i], h.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

func (h *Hub) Publish(m LogMsg) {
	h.mu.Lock()
	if len(h.history) < 3000 {
		h.history = append(h.history, m)
	}
	subs := append([]chan LogMsg{}, h.subs...)
	h.mu.Unlock()
	writeLogFile(m)
	for _, s := range subs {
		select {
		case s <- m:
		default:
		}
	}
}

// ---------- 日志落盘（按 src 分流 + 单服务配额，v3.0 T1.8 / plan §3.2.1） ----------
//
// 轮转策略（修复"只留一份 .old、第二次轮转直接覆盖"的问题）：
//
//	单文件超阈值触发轮转 → 现有 .1~.N 依次后退一位 → 当前文件成为 .1，最多保留 keep 份。
//
// 落盘行比前端行多三段：完整日期（跨天文件可定位）、来源进程、当前任务名。
//
// v3.0 T1.8 之前：业务前后端 stdout 与 EnvKit 自身日志混在同一文件，且只在
// **开文件时**核对一次大小——开完之后一个吵闹的后端能把文件撑到无限大。
// node1 日志 960MB 拖垮磁盘正是这个模式。现在按 src 分流、各自配额、
// 每次写入后核对实际字节数：
//
//	· src==""（EnvKit 自身）→ envkit-<day>.log，5MB/10 份（旧配额不变）；
//	· src="web"/"backend"（业务）→ envkit-<day>-<src>.log，各自 8MB/5 份。
type logSink struct {
	path string   // 当前文件路径
	f    *os.File // 当前句柄
	size int64    // 已写字节数（Observe 用实际值，不靠猜）
}

var (
	logFileMu  sync.Mutex
	logSinks   = map[string]*logSink{} // "day/src" → sink
	logLastDay string

	// logDirOverride 测试专用：非空时作为日志目录（生产取可执行文件所在目录）。
	logDirOverride string
)

// 日志配额：EnvKit 自身与业务服务分别限额。
// 业务侧单文件略宽（8MB）：前后端 stdout 单行更碎、重启更频繁；
// 归档份数更少（5 份）：业务日志的排查价值随时间的衰减比 EnvKit 自身快。
const (
	logKeep    = 10      // EnvKit 自身日志归档份数（不含当前文件）
	logMaxSize = 5 << 20 // EnvKit 自身日志单文件上限

	svcLogKeep    = 5       // 业务服务日志归档份数
	svcLogMaxSize = 8 << 20 // 业务服务日志单文件上限

	// logLineCap 轮转复验的容量上界里给"单行超长"留的余量。后端偶尔打一整行
	// JSON 能到几十 KB；1MB 以上的单行会触发复验 warn（只打印、不阻断）——
	// 宁可见到噪声，也不把上界悄悄放宽。
	logLineCap = 1 << 20
)

func logKeepFor(src string) int {
	if src == "" {
		return logKeep
	}
	return svcLogKeep
}

func logMaxFor(src string) int64 {
	if src == "" {
		return logMaxSize
	}
	return svcLogMaxSize
}

// logDir 日志目录：默认可执行文件所在目录；测试可覆盖。
func logDir() string {
	if logDirOverride != "" {
		return logDirOverride
	}
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// logSrcSuffix src 对应的文件名中缀（"" → EnvKit 自身，无中缀，旧名不破）。
func logSrcSuffix(src string) string {
	if src == "" {
		return ""
	}
	return "-" + src
}

// svcLogPath 某天某 src 的日志文件路径。
func svcLogPath(dir, day, src string) string {
	return filepath.Join(dir, "envkit-"+day+logSrcSuffix(src)+".log")
}

// rotateLog 把 path 轮转为 path.1，原有 .N 依次后退，超出 keep 的删除。
// 返回第一个遇到的错误但不中断后续搬运（能转多少转多少）——
// v3.0 T1.8：旧版全部 `_ =` 吞掉，重命名失败时轮转"看起来成功"，
// Verify 阶段永远发现不了，磁盘也就永远不释放。
func rotateLog(path string, keep int) error {
	var firstErr error
	for i := keep; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", path, i)
		if i == keep {
			// 最老的一份直接丢弃（删不掉 = 磁盘不释放，要可见）
			if err := os.Remove(src); err != nil && !os.IsNotExist(err) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.Rename(src, fmt.Sprintf("%s.%d", path, i+1)); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	if err := os.Rename(path, path+".1"); err != nil && !os.IsNotExist(err) && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// allDigits 是否非空纯数字（归档后缀 .N 的 N）。
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// logSinkDisk 统计某天某 src 的全部日志文件（当前 + 归档）份数与总字节数。
// 轮转复验与单测共用同一函数——保证"测的东西就是产品看的东西"。
// 只认三种形态：当前（.log / 无扩展名）与归档（.log.N）。
// "envkit-<day>-web.log" 相对 src="" 的前缀 rest 是 "-web.log"——不算进来：
// 复验张冠李戴（把业务文件的释放算到自身头上）比不复验更坏。
func logSinkDisk(dir, day, src string) (files int, bytes int64) {
	prefix := "envkit-" + day + logSrcSuffix(src)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		if rest != "" && rest != ".log" && !(strings.HasPrefix(rest, ".log.") && allDigits(rest[len(".log."):])) {
			continue
		}
		// 大小必须 os.Stat 现取，不能用 e.Info()：Windows 上 ReadDir 返回的
		// FileInfo 对"正开着写句柄的文件"是过期值——实测连续两次写入后仍读到
		// 上一轮的字节数，直到一次 os.Stat 刷新目录项才恢复（见 T1.8 单测
		// TestLogSinkDiskScopeIsolation 的踩坑记录）。轮转复验拿过期字节核对
		// "磁盘释放"，等于用假尺子量，还测不出来。
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
			files++
			bytes += fi.Size()
		}
	}
	return files, bytes
}

// rotateSink 轮转一个日志 sink。轮转本身按一次小型恢复事务执行
// （plan §3.2.1：Observe 大小 → Plan 归档 → Execute 轮换 → Verify 磁盘释放）：
//
//	Observe —— 轮转前当前文件的实际字节数（sink.size，写入时累计的真实值）
//	Plan    —— 归档链后退一位，最老一份越界即删（keep 份上限）
//	Execute —— 关旧句柄（Windows 上占着文件改不了名）→ 重命名链 → 开新当前文件
//	Verify  —— 重新 stat 磁盘上的实际字节：当前归零、份数不超限、该 src
//	           总占用 ≤ (keep+1) 份上限；不复验成功则把实际数字打印出来。
//
// 复验看实际字节数，不是"调用了 rotateLog 就算成功"。失败只打印不走 hub：
// Publish → writeLogFile 会重回本函数已持有的 logFileMu（不可重入），死锁。
func rotateSink(sk string, sink *logSink, dir, day, src string) {
	before := sink.size // Observe
	keep, maxSz := logKeepFor(src), logMaxFor(src)
	// Execute
	_ = sink.f.Close()
	rerr := rotateLog(sink.path, keep)
	f, err := os.OpenFile(sink.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		delete(logSinks, sk) // 句柄重建不了：丢掉，下次写入重来
		fmt.Fprintf(os.Stdout, "[loghub] 日志轮转失败：%s 轮转错误=%v 重开错误=%v\n", sink.path, rerr, err)
		return
	}
	sink.f, sink.size = f, 0
	// Verify：实际字节数
	files, total := logSinkDisk(dir, day, src)
	bound := int64(keep+1) * (maxSz + logLineCap)
	if rerr != nil || files > keep+1 || total > bound {
		fmt.Fprintf(os.Stdout, "[loghub] 日志轮转复验未过：%s 轮转前=%d 字节 现有份数=%d 总字节=%d（上限 份数≤%d 总字节≤%d）错误=%v\n",
			sink.path, before, files, total, keep+1, bound, rerr)
	}
}

func writeLogFile(m LogMsg) {
	dir := logDir()
	day := time.Now().Format("20060102")
	logFileMu.Lock()
	defer logFileMu.Unlock()
	if logLastDay != day {
		// 切天：关上一天的全部句柄（按天建的文件留着不关，fd 会跨天泄漏）
		for k, s := range logSinks {
			_ = s.f.Close()
			delete(logSinks, k)
		}
		logLastDay = day
	}
	sk := day + "/" + m.Src
	sink := logSinks[sk]
	if sink == nil {
		path := svcLogPath(dir, day, m.Src)
		// Observe：开新句柄前先核对磁盘上的实际大小（重启续写/跨进程追加场景）
		if fi, err := os.Stat(path); err == nil && fi.Size() > logMaxFor(m.Src) {
			if rerr := rotateLog(path, logKeepFor(m.Src)); rerr != nil {
				fmt.Fprintf(os.Stdout, "[loghub] 开文件时轮转失败：%s %v\n", path, rerr)
			}
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return // 目录只读等场景静默放弃
		}
		sz := int64(0)
		if fi, err := f.Stat(); err == nil {
			sz = fi.Size() // O_APPEND 下已有内容要从磁盘实际大小起算
		}
		sink = &logSink{path: path, f: f, size: sz}
		logSinks[sk] = sink
	}
	// 落盘格式（结构化）：日期 时间 [级别] [步骤] 正文 [来源] [任务]
	line := time.Now().Format("2006-01-02") + " " + m.Line
	if m.Src != "" {
		line += " [src=" + m.Src + "]"
	}
	if t := currentTask(); t != "" {
		line += " [task=" + t + "]"
	}
	n, _ := sink.f.WriteString(line + "\n")
	sink.size += int64(n)
	// 超阈值就地轮转：单个 src 再吵也炸不掉磁盘（不等切天/重启）
	if sink.size > logMaxFor(m.Src) {
		rotateSink(sk, sink, dir, day, m.Src)
	}
}

// parseLogDay 从日志文件名解析日期部分；不是 envkit 日志返回 ("", false)。
// 兼容全部形态：envkit-<day>.log / .log.N / .old / v3.0 起的 -<src> 分流中缀。
func parseLogDay(name string) (string, bool) {
	if !strings.HasPrefix(name, "envkit-") {
		return "", false
	}
	rest := strings.TrimPrefix(name, "envkit-")
	if i := strings.LastIndex(rest, ".log."); i >= 0 {
		rest = rest[:i+len(".log")] // 砍掉轮转后缀 .N，保留 .log 交给下面统一剥
	}
	rest = strings.TrimSuffix(rest, ".old")
	rest = strings.TrimSuffix(rest, ".log")
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		rest = rest[:i] // 砍掉 -<src> 分流中缀（web/backend）
	}
	if _, err := time.Parse("20060102", rest); err != nil {
		return "", false
	}
	return rest, true
}

// cleanupOldLogs 启动时清理 30 天前的日志文件（含轮转副本 .log.N、
// 历史 .old 与按 src 分流的 -web/-backend——v3.0 T1.8 前只认旧名，
// 分流文件会永远逃过清理）。
func cleanupOldLogs() {
	dir := logDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -30)
	removed := 0
	for _, e := range entries {
		name := e.Name()
		day, ok := parseLogDay(name)
		if !ok {
			continue
		}
		t, _ := time.Parse("20060102", day)
		if !t.Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, name)) == nil {
			removed++
		}
	}
	if removed > 0 {
		info(scSys, "日志", "已清理 %d 个超过 30 天的旧日志文件", removed)
	}
}

func logf(scope, level, step, format string, args ...interface{}) {
	logfSrc(scope, "", level, step, format, args...)
}

func logfSrc(scope, src, level, step, format string, args ...interface{}) {
	ts := time.Now().Format("15:04:05")
	line := fmt.Sprintf("%s [%s]", ts, level)
	if step != "" {
		line += " [" + step + "]"
	}
	line += " " + fmt.Sprintf(format, args...)
	hub.Publish(LogMsg{Scope: scope, Src: src, Line: line})
	fmt.Println("[" + scope + "] " + line)
}

func info(scope, step, f string, a ...interface{}) { logf(scope, "INFO", step, f, a...) }
func ok(scope, step, f string, a ...interface{})   { logf(scope, "OK", step, f, a...) }
func warn(scope, step, f string, a ...interface{}) { logf(scope, "WARN", step, f, a...) }
func fail(scope, step, f string, a ...interface{}) { logf(scope, "FAIL", step, f, a...) }

func infoS(scope, src, step, f string, a ...interface{}) { logfSrc(scope, src, "INFO", step, f, a...) }
func okS(scope, src, step, f string, a ...interface{})   { logfSrc(scope, src, "OK", step, f, a...) }
func warnS(scope, src, step, f string, a ...interface{}) { logfSrc(scope, src, "WARN", step, f, a...) }
func failS(scope, src, step, f string, a ...interface{}) { logfSrc(scope, src, "FAIL", step, f, a...) }
