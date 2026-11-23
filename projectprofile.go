package main

// projectprofile.go —— 项目运行画像（ProjectProfile）
//
// 以前"项目怎么跑"分散在三处：
//   - backendProjectKind 只回答 go / maven；
//   - projectBrief.PortHints 是前后端共享的一锅裸数字（最多 5 个）；
//   - svcPortCandidates 再把日志 URL、scan_ports、上面那锅数字和内置默认拼起来。
//
// 结果是：前端 Vite 的 5173 经常根本不在候选里；后端 8080 会被前端当成自己的；
// pom.xml 里的 17 也可能被当端口。这里把"项目应该怎么跑"收敛成一份按服务拆开的
// 画像：类型、端口线索（带来源与置信度）、健康路径、项目指纹。
//
// 画像只描述**项目文件里的事实与框架默认**。运行中的真实端口由 portobserver.go
// 观察；两边不混。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type projectServiceProfile struct {
	Service      string         `json:"service"`
	Dir          string         `json:"dir"`
	Kind         string         `json:"kind"`
	PortHints    []svcPortHint  `json:"port_hints"`
	HealthPaths  []string       `json:"health_paths"`
	Markers      []string       `json:"markers,omitempty"`
	ConfigFiles  []string       `json:"config_files,omitempty"`
	ProjectLabel string         `json:"project_label,omitempty"`
}

// svcPortHint 一条端口线索。Confidence 越高越可信：
//
//	100 用户显式配置（scan_ports 不算，见下）
//	 95 EnvKit 自己启动后日志里的真实 URL（在 verify.go 追加）
//	 85 项目配置文件里明确声明
//	 75 package.json 脚本参数
//	 60 用户 scan_ports（用于观察，不用来阻断启动）
//	 50 框架默认
//
// scan_ports 之所以不设高：它是"端口诊断列表"，用户可能只是把 8080/3306 放进来
// 排查别的服务，不等于本项目的服务端口。
type svcPortHint struct {
	Port       int    `json:"port"`
	Source     string `json:"source"`
	Confidence int    `json:"confidence"`
	Service    string `json:"service,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
}

type projectProfile struct {
	Frontend projectServiceProfile `json:"frontend"`
	Backend  projectServiceProfile `json:"backend"`
	Key      string                `json:"key"`
}

var (
	profileMu    sync.Mutex
	profileCache *projectProfile
	profileKey   string
	profileAt    time.Time
	profileTTL   = 2 * time.Minute
)

func projectProfileInvalidate() {
	profileMu.Lock()
	profileCache, profileKey, profileAt = nil, "", time.Time{}
	profileMu.Unlock()
}

// projectProfile 返回当前项目画像。调用方只读，不得修改返回的切片。
func currentProjectProfile() *projectProfile {
	c := cfgSnapshot()
	key := normPath(c.Projects.FrontendDir) + "|" + normPath(c.Projects.BackendDir)
	profileMu.Lock()
	defer profileMu.Unlock()
	if profileCache != nil && profileKey == key && time.Since(profileAt) < profileTTL {
		return profileCache
	}
	p := buildProjectProfile(c.Projects.FrontendDir, c.Projects.BackendDir)
	profileCache, profileKey, profileAt = p, key, time.Now()
	return p
}

func (p *projectProfile) service(target string) projectServiceProfile {
	if p == nil {
		return projectServiceProfile{}
	}
	if target == "web" {
		return p.Frontend
	}
	return p.Backend
}

func projectServiceDir(target string) string {
	p := currentProjectProfile()
	return p.service(target).Dir
}

// servicePortHints 返回某服务按置信度降序的端口线索（不含运行时日志 URL）。
func servicePortHints(target string) []svcPortHint {
	p := currentProjectProfile()
	hints := append([]svcPortHint(nil), p.service(target).PortHints...)
	sortPortHints(hints)
	return hints
}

func sortPortHints(h []svcPortHint) {
	sort.SliceStable(h, func(i, j int) bool {
		if h[i].Confidence == h[j].Confidence {
			return h[i].Port < h[j].Port
		}
		return h[i].Confidence > h[j].Confidence
	})
}

func buildProjectProfile(frontDir, backDir string) *projectProfile {
	p := &projectProfile{Key: normPath(frontDir) + "|" + normPath(backDir)}
	p.Frontend = buildFrontendProfile(frontDir)
	p.Backend = buildBackendProfile(backDir)
	return p
}

// ---------- 前端 ----------

func buildFrontendProfile(dir string) projectServiceProfile {
	p := projectServiceProfile{Service: "web", Dir: strings.TrimSpace(dir), Kind: "none"}
	if p.Dir == "" {
		return p
	}
	if fi, err := os.Stat(p.Dir); err != nil || !fi.IsDir() {
		return p
	}
	p.Kind = detectFrontendKind(p.Dir)
	p.ProjectLabel = npmPackageName(p.Dir)

	files := existingFiles(
		filepath.Join(p.Dir, "vite.config.js"),
		filepath.Join(p.Dir, "vite.config.ts"),
		filepath.Join(p.Dir, "vite.config.mjs"),
		filepath.Join(p.Dir, "vite.config.mts"),
		filepath.Join(p.Dir, "vue.config.js"),
		filepath.Join(p.Dir, "vue.config.ts"),
		filepath.Join(p.Dir, "next.config.js"),
		filepath.Join(p.Dir, "next.config.mjs"),
		filepath.Join(p.Dir, "nuxt.config.js"),
		filepath.Join(p.Dir, "nuxt.config.ts"),
		filepath.Join(p.Dir, "webpack.config.js"),
		filepath.Join(p.Dir, "package.json"),
		filepath.Join(p.Dir, ".env"),
		filepath.Join(p.Dir, ".env.local"),
		filepath.Join(p.Dir, ".env.development"),
		filepath.Join(p.Dir, ".env.development.local"),
	)
	for _, f := range files {
		p.ConfigFiles = append(p.ConfigFiles, filepath.Base(f))
		addFrontendPortHints(&p, f)
	}
	addDefaultPorts(&p, frontendDefaultPorts(p.Kind), "框架默认端口")
	p.HealthPaths = []string{"/"}
	addProjectMarkers(&p, p.Dir, p.ProjectLabel)
	return p
}

func detectFrontendKind(dir string) string {
	deps := packageDeps(dir)
	files := func(names ...string) bool {
		for _, n := range names {
			if hasFile(filepath.Join(dir, n)) {
				return true
			}
		}
		return false
	}
	switch {
	case deps["vite"] != "" || files("vite.config.js", "vite.config.ts", "vite.config.mjs", "vite.config.mts"):
		return "vite"
	case deps["next"] != "" || files("next.config.js", "next.config.mjs"):
		return "next"
	case deps["nuxt"] != "" || files("nuxt.config.js", "nuxt.config.ts"):
		return "nuxt"
	case deps["@vue/cli-service"] != "" || files("vue.config.js", "vue.config.ts"):
		return "vue-cli"
	case deps["react-scripts"] != "":
		return "cra"
	case hasFile(filepath.Join(dir, "package.json")):
		return "node"
	default:
		return "none"
	}
}

func frontendDefaultPorts(kind string) []int {
	switch kind {
	case "vite":
		return []int{5173, 4173}
	case "vue-cli":
		return []int{8080}
	case "next", "nuxt", "cra":
		return []int{3000}
	case "node":
		return []int{3000, 8080}
	default:
		return nil
	}
}

func addFrontendPortHints(p *projectServiceProfile, path string) {
	src := readTextLimited(path, 64*1024)
	if src == "" {
		return
	}
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "package.json":
		if ports := portsFromPackageScripts(src); len(ports) > 0 {
			addHints(p, ports, filepath.Base(path), 75, "package.json 启动脚本参数")
		}
		return
	case ".env", ".env.local", ".env.development", ".env.development.local":
		if ports := portsFromEnv(src); len(ports) > 0 {
			addHints(p, ports, filepath.Base(path), 85, "环境变量里的端口")
		}
		return
	}
	// vite/vue/next/nuxt/webpack 配置：优先找 server/devServer 后的 port。
	if ports := portsFromFrontendConfig(src); len(ports) > 0 {
		addHints(p, ports, filepath.Base(path), 85, "前端配置里的 devServer/server.port")
	}
}

// ---------- 后端 ----------

func buildBackendProfile(dir string) projectServiceProfile {
	p := projectServiceProfile{Service: "backend", Dir: strings.TrimSpace(dir), Kind: "none"}
	if p.Dir == "" {
		return p
	}
	if fi, err := os.Stat(p.Dir); err != nil || !fi.IsDir() {
		return p
	}
	p.Kind = backendProjectKind(p.Dir)
	p.ProjectLabel = mavenArtifact(p.Dir)
	p.HealthPaths = []string{"/"}

	files := existingFiles(
		filepath.Join(p.Dir, "application.yml"),
		filepath.Join(p.Dir, "application.yaml"),
		filepath.Join(p.Dir, "application.properties"),
		filepath.Join(p.Dir, "src", "main", "resources", "application.yml"),
		filepath.Join(p.Dir, "src", "main", "resources", "application.yaml"),
		filepath.Join(p.Dir, "src", "main", "resources", "application.properties"),
		filepath.Join(p.Dir, "pom.xml"),
		filepath.Join(p.Dir, "config.yaml"),
		filepath.Join(p.Dir, "config.yml"),
		filepath.Join(p.Dir, ".env"),
		filepath.Join(p.Dir, "Makefile"),
		filepath.Join(p.Dir, "docker-compose.yml"),
		filepath.Join(p.Dir, "docker-compose.yaml"),
	)
	// Spring Boot 的 profile 配置：application-dev.yml 等也要看。
	if ents, err := os.ReadDir(filepath.Join(p.Dir, "src", "main", "resources")); err == nil {
		for _, e := range ents {
			name := strings.ToLower(e.Name())
			if e.IsDir() || !strings.HasPrefix(name, "application-") {
				continue
			}
			if strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".properties") {
				files = append(files, filepath.Join(p.Dir, "src", "main", "resources", e.Name()))
			}
		}
	}
	seen := map[string]bool{}
	for _, f := range files {
		k := normPath(f)
		if seen[k] {
			continue
		}
		seen[k] = true
		p.ConfigFiles = append(p.ConfigFiles, filepath.Base(f))
		addBackendPortHints(&p, f)
	}
	addDefaultPorts(&p, backendDefaultPorts(p.Kind), "框架默认端口")
	if p.Kind == "maven" {
		p.HealthPaths = []string{"/actuator/health", "/"}
	}
	addProjectMarkers(&p, p.Dir, p.ProjectLabel)
	return p
}

func backendDefaultPorts(kind string) []int {
	switch kind {
	case "maven":
		return []int{8080}
	case "go":
		return []int{8080, 8888}
	default:
		return []int{8080}
	}
}

func addBackendPortHints(p *projectServiceProfile, path string) {
	src := readTextLimited(path, 64*1024)
	if src == "" {
		return
	}
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasPrefix(base, "application") && (strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")):
		if port := yamlServerPort(src); port > 0 {
			addHints(p, []int{port}, filepath.Base(path), 85, "Spring server.port")
		}
		return
	case strings.HasPrefix(base, "application") && strings.HasSuffix(base, ".properties"):
		if port := propertiesServerPort(src); port > 0 {
			addHints(p, []int{port}, filepath.Base(path), 85, "Spring server.port")
		}
		return
	case base == ".env":
		if ports := portsFromEnv(src); len(ports) > 0 {
			addHints(p, ports, filepath.Base(path), 85, "环境变量里的端口")
		}
		return
	case base == "docker-compose.yml" || base == "docker-compose.yaml":
		if ports := portsFromCompose(src); len(ports) > 0 {
			addHints(p, ports, filepath.Base(path), 70, "docker-compose 端口映射")
		}
		return
	}
	// Go 的 config.yaml / Makefile：只接受明确 port: 的写法，避免把 random 数字当端口。
	if base == "config.yaml" || base == "config.yml" || base == "makefile" {
		if ports := portsFromGenericConfig(src); len(ports) > 0 {
			addHints(p, ports, filepath.Base(path), 65, "项目配置里的端口")
		}
	}
}

// ---------- 解析 ----------

func yamlServerPort(src string) int {
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "server:") || strings.HasPrefix(t, "#") {
			continue
		}
		// 同一行 server.port: 8080 的少见写法
		if p := parsePortAfterColon(strings.TrimPrefix(t, "server:")); p > 0 {
			return p
		}
		baseIndent := len(ln) - len(strings.TrimLeft(ln, " \t"))
		for j := i + 1; j < len(lines) && j <= i+30; j++ {
			s := lines[j]
			trim := strings.TrimSpace(s)
			if trim == "" || strings.HasPrefix(trim, "#") {
				continue
			}
			indent := len(s) - len(strings.TrimLeft(s, " \t"))
			if indent <= baseIndent {
				break
			}
			if strings.HasPrefix(trim, "port:") {
				if p := parsePortAfterColon(trim); p > 0 {
					return p
				}
			}
		}
	}
	// 兼容 server.port: 8080 / server.port=8080
	re := regexp.MustCompile(`(?i)\bserver\s*\.\s*port\s*[:=]\s*["']?(\d{2,5})`)
	if m := re.FindStringSubmatch(src); len(m) > 1 {
		if p, _ := strconv.Atoi(m[1]); p > 0 {
			return p
		}
	}
	return 0
}

func propertiesServerPort(src string) int {
	re := regexp.MustCompile(`(?im)^\s*server\s*\.\s*port\s*=\s*(\d{2,5})\s*$`)
	if m := re.FindStringSubmatch(src); len(m) > 1 {
		p, _ := strconv.Atoi(m[1])
		return p
	}
	return 0
}

func portsFromFrontendConfig(src string) []int {
	lines := strings.Split(src, "\n")
	var out []int
	for i, ln := range lines {
		t := strings.ToLower(strings.TrimSpace(ln))
		if !(strings.Contains(t, "server:") || strings.Contains(t, "devserver:") ||
			strings.Contains(t, "server =") || strings.Contains(t, "server=")) {
			continue
		}
		for j := i + 1; j < len(lines) && j <= i+40; j++ {
			s := strings.TrimSpace(lines[j])
			lower := strings.ToLower(s)
			if strings.HasPrefix(s, "}") && !strings.Contains(lower, "port") {
				break
			}
			if strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/*") {
				continue
			}
			// 只接受 "port: 5173" / "port = 5173" 这种明确的键，避免 proxy 里的端口串。
			if strings.HasPrefix(lower, "port:") || strings.HasPrefix(lower, "port ") || strings.HasPrefix(lower, "port=") {
				if p := parsePortAfterColon(s); p > 0 {
					out = append(out, p)
				}
			}
		}
	}
	if len(out) == 0 {
		// 同一行或压缩写法：server: { ... port: 5173 }
		re := regexp.MustCompile(`(?is)(?:server|devServer)\s*:\s*\{[^{}]{0,800}?port\s*[:=]\s*["']?(\d{2,5})`)
		if m := re.FindStringSubmatch(src); len(m) > 1 {
			if p, _ := strconv.Atoi(m[1]); p > 0 {
				out = append(out, p)
			}
		}
	}
	return dedupPorts(out)
}

func portsFromPackageScripts(src string) []int {
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal([]byte(src), &pkg) != nil {
		return nil
	}
	var out []int
	for _, s := range pkg.Scripts {
		out = append(out, portsFromCommand(s)...)
	}
	return dedupPorts(out)
}

func portsFromCommand(s string) []int {
	var out []int
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)--port(?:=|\s+)(\d{2,5})`),
		regexp.MustCompile(`(?i)(?:^|\s)-p\s*(\d{2,5})(?:\s|$)`),
	} {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if len(m) > 1 {
				if p, _ := strconv.Atoi(m[1]); p > 0 {
					out = append(out, p)
				}
			}
		}
	}
	return dedupPorts(out)
}

func portsFromEnv(src string) []int {
	re := regexp.MustCompile(`(?im)^\s*(?:PORT|VITE_PORT|SERVER_PORT|NUXT_PORT|NEXT_PORT|APP_PORT)\s*=\s*["']?(\d{2,5})`)
	var out []int
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if len(m) > 1 {
			if p, _ := strconv.Atoi(m[1]); p > 0 {
				out = append(out, p)
			}
		}
	}
	return dedupPorts(out)
}

func portsFromCompose(src string) []int {
	re := regexp.MustCompile(`(?m)^\s*-\s*["']?(\d{2,5}):(\d{2,5})["']?\s*$`)
	var out []int
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if len(m) > 2 {
			if p, _ := strconv.Atoi(m[2]); p > 0 {
				out = append(out, p)
			}
		}
	}
	return dedupPorts(out)
}

func portsFromGenericConfig(src string) []int {
	re := regexp.MustCompile(`(?im)^\s*port\s*[:=]\s*["']?(\d{2,5})`)
	var out []int
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if len(m) > 1 {
			if p, _ := strconv.Atoi(m[1]); p > 0 {
				out = append(out, p)
			}
		}
	}
	return dedupPorts(out)
}

func parsePortAfterColon(s string) int {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		i = strings.IndexByte(s, '=')
	}
	if i < 0 {
		return 0
	}
	v := strings.TrimSpace(s[i+1:])
	if c := strings.IndexAny(v, " \t,#;}"); c >= 0 {
		v = v[:c]
	}
	v = strings.Trim(v, `"'`)
	p, err := strconv.Atoi(v)
	if err != nil || p < 1 || p > 65535 {
		return 0
	}
	return p
}

func addHints(p *projectServiceProfile, ports []int, source string, confidence int, evidence string) {
	for _, port := range ports {
		if !isServicePort(port) {
			continue
		}
		existing := -1
		for i := range p.PortHints {
			if p.PortHints[i].Port == port {
				existing = i
				break
			}
		}
		if existing >= 0 {
			if confidence > p.PortHints[existing].Confidence {
				p.PortHints[existing] = svcPortHint{
					Port: port, Source: source, Confidence: confidence,
					Service: p.Service, Evidence: evidence,
				}
			}
			continue
		}
		p.PortHints = append(p.PortHints, svcPortHint{
			Port: port, Source: source, Confidence: confidence,
			Service: p.Service, Evidence: evidence,
		})
	}
}

func addDefaultPorts(p *projectServiceProfile, ports []int, evidence string) {
	for _, port := range ports {
		addHints(p, []int{port}, "framework-default", 50, evidence)
	}
}

func isServicePort(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	// 这些端口通常是数据库/链端/中间件，不是前后端 dev server。
	switch port {
	case 3306, 6379, 27017, 20200, 5002, 8545, 8546:
		return false
	}
	return true
}

func dedupPorts(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, p := range in {
		if p <= 0 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// ---------- 项目元信息 ----------

func packageDeps(dir string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return out
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return out
	}
	for k := range pkg.Dependencies {
		out[k] = "dep"
	}
	for k := range pkg.DevDependencies {
		if _, ok := out[k]; !ok {
			out[k] = "dev"
		}
	}
	return out
}

func npmPackageName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return ""
	}
	return strings.TrimSpace(pkg.Name)
}

func mavenArtifact(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "pom.xml"))
	if err != nil {
		return ""
	}
	a, _ := extractPomMeta(string(data))
	return a
}

func addProjectMarkers(p *projectServiceProfile, dir, label string) {
	if dir != "" {
		p.Markers = append(p.Markers, normPath(dir))
	}
	if label != "" {
		p.Markers = append(p.Markers, strings.TrimSpace(label))
	}
}

func readTextLimited(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() || fi.Size() > max {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func existingFiles(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if hasFile(p) {
			out = append(out, p)
		}
	}
	return out
}
