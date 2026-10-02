// diag.go —— 一键诊断包（zip）+ 自版本检查
//
// 之前用户排查问题只能手工翻日志文件挨个发；现在 /api/diag 一个 zip 打包：
// 诊断报告（md）+ 脱敏配置 + 最近日志 + 审计流 + state.json。
// 注意：日志里可能含内网 IP / 绝对路径等环境指纹，导出前会提示用户自行斟酌再外发。
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const diagMaxFileBytes = 4 << 20 // 单个文件最多打包 4MB（日志按尾部截断，审计同理）

func handleDiag(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	dir := exeDir()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	addZipFile(zw, "report.md", buildDiagReport())
	// 操作流水摘要：诊断包里原本只有原始 jsonl，看的人得自己解析——这里给一份可读的单行摘要
	addZipFile(zw, "audit-recent.txt", strings.Join(auditSummary(300), "\n")+"\n")

	// 脱敏配置：密码/API Key 一律清空（诊断包是给"别人"看的）。
	// 必须深拷贝 AI：浅拷贝会把全局内存里的 API Key 一起清掉（AI 助手当场失联）。
	cfgMu.Lock()
	c := cloneConfigDeepAI(cfg)
	cfgMu.Unlock()
	c.MySQL.Password = ""
	c.Projects.MySQLPass = ""
	c.Chain.SSHPassword = ""
	if c.AI != nil {
		c.AI.APIKey = ""
	}
	if data, err := json.MarshalIndent(c, "", "  "); err == nil {
		addZipFile(zw, "config-sanitized.json", string(data))
	}

	// 日志与审计：按修改时间取最近几份，超限截尾
	for _, name := range recentFiles(dir, "envkit-", ".log", 6) {
		addZipFileTailed(zw, name, readTail(filepath.Join(dir, name), diagMaxFileBytes))
	}
	for _, name := range recentFiles(dir, "audit-", ".jsonl", 6) {
		addZipFileTailed(zw, name, readTail(filepath.Join(dir, name), diagMaxFileBytes))
	}

	if data, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		addZipFile(zw, "state.json", string(data))
	} else {
		addZipFile(zw, "state.json", "(无 state.json：尚未产生持久化状态)")
	}

	if err := zw.Close(); err != nil {
		http.Error(w, "打包失败: "+err.Error(), 500)
		return
	}
	name := "envkit-diag-" + time.Now().Format("20060102-150405") + ".zip"
	auditNow(actUser, "diag_export", name, "", resOK, "")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	_, _ = w.Write(buf.Bytes())
}

// recentFiles 返回 dir 下以 prefix 开头、suffix 结尾的文件名，按修改时间倒序（新的在前）。
func recentFiles(dir, prefix, suffix string, max int) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type fi struct {
		name string
		mod  time.Time
	}
	var out []fi
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		if info, err := e.Info(); err == nil {
			out = append(out, fi{name, info.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	if len(out) > max {
		out = out[:max]
	}
	names := make([]string, 0, len(out))
	for _, x := range out {
		names = append(names, x.name)
	}
	return names
}

// readTail 读文件尾部 max 字节；超限时在开头加一行截断说明。
func readTail(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	if st.Size() <= max {
		b, err := io.ReadAll(f)
		if err != nil {
			return ""
		}
		return string(b)
	}
	if _, err := f.Seek(-max, io.SeekEnd); err != nil {
		return ""
	}
	b := make([]byte, max)
	n, _ := io.ReadFull(f, b)
	return fmt.Sprintf("(文件超过 %d MB，仅保留末尾部分)\n%s", max>>20, string(b[:n]))
}

func addZipFile(zw *zip.Writer, name, content string) {
	f, err := zw.Create(name)
	if err != nil {
		return
	}
	_, _ = io.WriteString(f, content)
}

func addZipFileTailed(zw *zip.Writer, name, content string) { addZipFile(zw, name, content) }

// ---------- 自版本检查 ----------

type updateInfo struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Notes   string `json:"notes"`
	HasNew  bool   `json:"has_new"`
	Checked bool   `json:"checked"`
	Error   string `json:"error,omitempty"`
	At      string `json:"at"`
}

var (
	updMu sync.Mutex
	upd   = updateInfo{Current: appVersion}
)

// compareSemver 逐段比较点分版本号；解析失败的段按 0 处理。返回 -1/0/1。
func compareSemver(a, b string) int {
	as, bs := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		x, _ := strconv.Atoi(seg(as, i))
		y, _ := strconv.Atoi(seg(bs, i))
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

func seg(parts []string, i int) string {
	if i < len(parts) {
		return strings.TrimSpace(parts[i])
	}
	return "0"
}

// runSelfUpdateCheck 启动时执行一次；结果缓存到全局 upd，/api/self-update 直接读。
// 未配置 update_url 时视为功能关闭，不报错。
func runSelfUpdateCheck() {
	cfgMu.Lock()
	url := strings.TrimSpace(cfg.UpdateURL)
	cfgMu.Unlock()
	res := updateInfo{Current: appVersion, At: time.Now().Format("15:04:05")}
	if url == "" {
		res.Error = "未配置 update_url，跳过检查"
	} else {
		resp, err := httpShort.Get(url)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				var body struct {
					Version string `json:"version"`
					Notes   string `json:"notes"`
				}
				if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
					res.Error = "响应解析失败：" + err.Error()
				} else {
					res.Latest = strings.TrimSpace(body.Version)
					res.Notes = body.Notes
					res.HasNew = compareSemver(appVersion, res.Latest) < 0
				}
			} else {
				res.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
		} else {
			res.Error = err.Error()
		}
	}
	updMu.Lock()
	upd = res
	updMu.Unlock()
	switch {
	case res.HasNew:
		info(scSys, "版本", "发现新版本 %s（当前 %s）%s", res.Latest, appVersion, res.Notes)
	case res.Error != "" && !strings.Contains(res.Error, "跳过"):
		warn(scSys, "版本", "自更新检查失败：%s", res.Error)
	default:
		info(scSys, "版本", "已是最新版本 %s", appVersion)
	}
}

// handleSelfUpdate 返回缓存的检查结果；?refresh=1 同步重新检查一次。
func handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.URL.Query().Get("refresh") == "1" {
		runSelfUpdateCheck()
	}
	updMu.Lock()
	u := upd
	updMu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(u)
}
