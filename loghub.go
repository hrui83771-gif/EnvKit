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

// ---------- 日志落盘 ----------
// 轮转策略（修复"只留一份 .old、第二次轮转直接覆盖"的问题）：
//
//	单文件 5MB 触发轮转 → 现有 .1~.9 依次后退一位 → 当前文件成为 .1，最多保留 10 份。
//
// 落盘行比前端行多三段：完整日期（跨天文件可定位）、来源进程、当前任务名。
var (
	logFileMu  sync.Mutex
	logFile    *os.File
	logFileDay string
)

// logKeep 轮转保留份数（不含当前文件）。
const logKeep = 10

// logMaxSize 单文件上限。
const logMaxSize = 5 << 20

// rotateLog 把 path 轮转为 path.1，原有 .N 依次后退，超出 logKeep 的删除。
func rotateLog(path string) {
	for i := logKeep; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", path, i)
		if i == logKeep {
			_ = os.Remove(src) // 最老的一份直接丢弃
			continue
		}
		_ = os.Rename(src, fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
}

// cleanupOldLogs 启动时清理 30 天前的日志文件（含轮转副本 .log.N）。
func cleanupOldLogs() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -30)
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "envkit-") {
			continue
		}
		datePart := strings.TrimPrefix(name, "envkit-")
		// 去掉可能的轮转后缀 .N（N 为数字），只保留日期部分用于比较
		if idx := strings.LastIndex(datePart, ".log."); idx >= 0 {
			datePart = datePart[:idx]
		}
		datePart = strings.TrimSuffix(datePart, ".log")
		datePart = strings.TrimSuffix(datePart, ".old")
		t, perr := time.Parse("20060102", datePart)
		if perr != nil || !t.Before(cutoff) {
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

func writeLogFile(m LogMsg) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	day := time.Now().Format("20060102")
	logFileMu.Lock()
	defer logFileMu.Unlock()
	if logFile == nil || logFileDay != day {
		if logFile != nil {
			_ = logFile.Close()
			logFile = nil
		}
		path := filepath.Join(filepath.Dir(exe), "envkit-"+day+".log")
		if fi, err := os.Stat(path); err == nil && fi.Size() > logMaxSize {
			rotateLog(path)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return // 目录只读等场景静默放弃
		}
		logFile, logFileDay = f, day
	}
	// 落盘格式（结构化）：日期 时间 [级别] [步骤] 正文 [来源] [任务]
	line := time.Now().Format("2006-01-02") + " " + m.Line
	if m.Src != "" {
		line += " [src=" + m.Src + "]"
	}
	if t := currentTask(); t != "" {
		line += " [task=" + t + "]"
	}
	_, _ = logFile.WriteString(line + "\n")
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
