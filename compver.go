package main

// 组件版本管理：查询官方最新版本 + 一键升级 + 演练模式。
// 升级原理：把组件 URL / zip_top / version 中的旧版本号替换为最新版本号，
// 复用既有 下载→解压→配置 管线装出**新目录**（与旧目录并存，可回滚），
// 成功后才更新 config.json（DPAPI 加密保存）。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// httpc 兼容旧名：共享的轻量 API client（连接池与全局共用，见 httpx.go）
var httpc = httpShort

var verRe = regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?)`)

// versionFromURL 从下载 URL 文件名里提取版本号（go1.23.6…/node-v22.22.0…/mysql-8.0.40…）。
func versionFromURL(url string) string {
	base := url
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	if m := verRe.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	return ""
}

// verCmp 比较版本号：-1/0/1；非法段按 0 处理。
func verCmp(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		fmt.Sscanf(getPart(pa, i), "%d", &x)
		fmt.Sscanf(getPart(pb, i), "%d", &y)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func getPart(parts []string, i int) string {
	if i < len(parts) {
		return parts[i]
	}
	return "0"
}

// ---------- 官方最新版本查询（带 30 分钟缓存） ----------
type latestEntry struct {
	v  string
	at time.Time
}

var (
	latestMu    sync.Mutex
	latestCache = map[string]latestEntry{}
)

const latestTTL = 30 * time.Minute

func fetchLatestFor(name string) string {
	latestMu.Lock()
	if e, ok := latestCache[name]; ok && time.Since(e.at) < latestTTL {
		latestMu.Unlock()
		return e.v
	}
	latestMu.Unlock()
	var v string
	switch name {
	case "Go":
		v = fetchLatestGo()
	case "Node.js":
		v = fetchLatestNode()
	case "MySQL":
		v = fetchLatestMySQL()
	}
	if v != "" {
		latestMu.Lock()
		latestCache[name] = latestEntry{v, time.Now()}
		latestMu.Unlock()
	}
	return v
}

func httpGetBody(url string) (string, error) {
	resp, err := httpc.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func fetchLatestGo() string {
	// golang.google.cn 国内可达，返回稳定版列表，首个 stable 即最新
	body, err := httpGetBody("https://golang.google.cn/dl/?mode=json")
	if err != nil {
		return ""
	}
	var arr []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := json.Unmarshal([]byte(body), &arr); err != nil {
		return ""
	}
	for _, e := range arr {
		if e.Stable {
			return strings.TrimPrefix(e.Version, "go")
		}
	}
	return ""
}

func fetchLatestNode() string {
	// npmmirror 二进制镜像目录列表，正则直接抽数量最多的 22.x.y（与配置大版本一致）
	body, err := httpGetBody("https://registry.npmmirror.com/-/binary/node/")
	if err != nil {
		return ""
	}
	re := reOf(`v(22\.\d+\.\d+)`)
	best := ""
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		if verCmp(m[1], best) > 0 {
			best = m[1]
		}
	}
	return best
}

func fetchLatestMySQL() string {
	// MySQL 官方 CDN 只保留每个系列的最新版：从高版本号向下探测，第一个 206/200 即最新
	for minor := 60; minor >= 40; minor-- {
		u := fmt.Sprintf("https://cdn.mysql.com/Downloads/MySQL-8.0/mysql-8.0.%d-winx64.zip", minor)
		resp, err := httpCDN.Head(u)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == 200 || resp.StatusCode == 206 {
			return fmt.Sprintf("8.0.%d", minor)
		}
	}
	return ""
}

// ---------- 接口：版本对比 ----------
type compVerInfo struct {
	Name           string `json:"name"`
	ConfigVer      string `json:"configVer"`
	InstalledVer   string `json:"installedVer"`
	InstalledKnown bool   `json:"installedKnown"`
	ToolDir        bool   `json:"toolDir"`
	Latest         string `json:"latest"`
}

func handleComponentVersions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	resultsMu.Lock()
	snap := map[string]CheckResult{}
	for k, v := range results {
		snap[k] = v
	}
	resultsMu.Unlock()
	out := make([]compVerInfo, 0, len(cfg.Components))
	for _, c := range cfg.Components {
		info := compVerInfo{Name: c.Name, ConfigVer: c.Version, Latest: fetchLatestFor(c.Name)}
		cr, ok := snap[c.Name]
		if !ok {
			// 检测缓存缺失（页面刚打开就点了检查更新）：就地快速探测
			cr = detectComponent(c)
			resultsMu.Lock()
			results[c.Name] = cr
			resultsMu.Unlock()
		}
		if cr.Installed {
			info.InstalledKnown = true
			// 提取纯版本号：取最后一个数字段（MySQL 输出含路径 "Server 8.0in"，首个匹配会是 8.0）
			if ms := verRe.FindAllStringSubmatch(cr.Version, -1); len(ms) > 0 {
				info.InstalledVer = ms[len(ms)-1][1]
			} else {
				info.InstalledVer = firstLine(strings.TrimSpace(cr.Version))
			}
		}
		if _, err := os.Stat(componentBinDir(c)); err == nil {
			info.ToolDir = true
		}
		out = append(out, info)
	}
	_ = json.NewEncoder(w).Encode(out)
}

// ---------- 接口：一键升级 ----------
func handleComponentUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	idx := -1
	for i, c := range cfg.Components {
		if c.Name == body.Name {
			idx = i
			break
		}
	}
	if idx < 0 {
		http.Error(w, "未知组件："+body.Name, 400)
		return
	}
	target := cfg.Components[idx] // 值拷贝
	h, granted := beginTaskH("升级 "+target.Name, true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "component_upgrade", target.Name, target.Version)
		audited := false
		defer func() {
			if !audited {
				fin(resFail, "未完成")
			}
		}()
		sc := scInstall
		latest := fetchLatestFor(target.Name)
		if latest == "" {
			fail(sc, "升级", "无法获取 %s 官方最新版本（网络或镜像不可达）", target.Name)
			return
		}
		if verCmp(latest, target.Version) <= 0 {
			ok(sc, "升级", "%s 已是最新（%s）", target.Name, target.Version)
			return
		}
		newTop := strings.ReplaceAll(target.ZipTop, target.Version, latest)
		newURL := strings.ReplaceAll(target.URL, target.Version, latest)
		if newTop == target.ZipTop && newURL == target.URL {
			fail(sc, "升级", "URL 中未找到可替换的版本号（%s）", target.Version)
			return
		}
		// 先验证新地址有效，再动任何配置
		resp, err := httpc.Head(newURL)
		if err != nil {
			fail(sc, "升级", "新版本地址不可达：%s（%v）", newURL, err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			fail(sc, "升级", "新版本地址返回 HTTP %d：%s", resp.StatusCode, newURL)
			return
		}
		nc := target
		nc.Version, nc.URL, nc.ZipTop = latest, newURL, newTop
		nc.SHA256 = "" // 换版本后旧校验值不再适用，安装时会重新记录基线
		warn(sc, "升级", "%s：准备升级 %s → %s", target.Name, target.Version, latest)
		zipPath := filepath.Join(os.TempDir(), target.Name+"-upgrade.zip")
		if err := downloadWithRetry(nc.URL, zipPath, nc.Name+"-upgrade", nc.SHA256); err != nil {
			fail(sc, "升级", "下载失败：%v", err)
			return
		}
		info(sc, "升级", "解压 -> %s", componentInstallPath(nc))
		if err := unzip(zipPath, cfg.InstallDir); err != nil {
			fail(sc, "升级", "解压失败：%v", err)
			return
		}
		_ = os.Remove(zipPath)
		configureEnv(nc)
		configureLanguageProxy(nc)
		if nc.Special == "mysql" {
			setupMySQL(nc)
		}
		// 验证新版本可执行
		exe := filepath.Join(componentBinDir(nc), nc.BinExe)
		if _, err := os.Stat(exe); err != nil {
			fail(sc, "升级", "验证失败：未找到 %s", exe)
			return
		}
		if out, verr := runIn(filepath.Dir(exe), exe, nc.CheckArgs...); verr != nil || !reOf(nc.CheckRe).MatchString(out) {
			warn(sc, "升级", "版本验证输出异常：%s", firstLines(strings.TrimSpace(out), 200))
		}
		// 全部成功后才落配置（含 DPAPI 加密、原子写）
		cfg.Components[idx] = nc
		saveExternalConfig(cfg)
		audited = true
		fin(resOK, fmt.Sprintf("%s → %s", target.Version, latest))
		ok(sc, "升级", "%s 升级完成：%s → %s", target.Name, target.Version, latest)
		if target.ZipTop != nc.ZipTop {
			old := componentInstallPath(target)
			if _, err := os.Stat(old); err == nil {
				warn(sc, "升级", "旧版本目录保留未删（可手动删除）：%s", old)
			}
		}
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

// resolveMySQLComponent MySQL 官方 CDN 只保留每个系列的最新版，静态 URL 会随版本更新失效。
// 安装/演练前动态解析：探测 CDN 当前可下载的版本并改写 URL/zip_top/version。
// 探测失败（网络问题）时保持原配置；被新版本顶掉的旧版可走 archives 兜底。
func resolveMySQLComponent(c Component) Component {
	latest := fetchLatestMySQL()
	if latest == "" || latest == c.Version {
		return c
	}
	nc := c
	nc.Version = latest
	nc.URL = fmt.Sprintf("https://cdn.mysql.com/Downloads/MySQL-8.0/mysql-%s-winx64.zip", latest)
	nc.ZipTop = fmt.Sprintf("mysql-%s-winx64", latest)
	return nc
}

// ---------- 接口：演练模式（只读模拟安装流程） ----------
func handleInstallDryRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	h, granted := beginTaskH("演练安装", false) // 只读模拟
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		auditNow(actUser, "install_dryrun", cfg.InstallDir, "", resOK, "")
		info(scInstall, "演练", "=== 演练模式：只读模拟，不下载、不安装、不改环境变量 ===")
		for _, c := range cfg.Components {
			if c.Special == "mysql" {
				c = resolveMySQLComponent(c)
			}
			if ok2, loc := isInstalled(c); ok2 {
				info(scInstall, "演练", "%s：已安装（%s）→ 将跳过", c.Name, loc)
				continue
			}
			status := "不可达"
			if resp, err := httpc.Head(c.URL); err == nil {
				resp.Body.Close()
				status = fmt.Sprintf("HTTP %d 可用", resp.StatusCode)
			}
			info(scInstall, "演练", "%s %s：未安装 → 下载 %s（%s）", c.Name, c.Version, c.URL, status)
			info(scInstall, "演练", "%s：解压至 %s，PATH += %s", c.Name, componentInstallPath(c), componentBinDir(c))
			for k, v := range c.ExtraEnv {
				info(scInstall, "演练", "%s：环境变量 %s=%s", c.Name, k, strings.ReplaceAll(v, "<install>", cfg.InstallDir))
			}
			if c.Special == "mysql" {
				info(scInstall, "演练", "%s：将初始化 data 目录并注册 Windows 服务（需管理员）", c.Name)
			}
		}
		ok(scInstall, "演练", "演练完成：以上为真实安装将执行的完整步骤")
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}
