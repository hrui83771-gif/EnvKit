// projectsetup.go —— 项目接入向导的只读检查与配置落盘。
//
// 设计约束（对应「项目接入向导」的验收口径）：
//
//	· 检查只读：不启动服务、不安装依赖、不杀进程，只读目录/配置文件；
//	· 目录是唯一起点：前端/后端目录没填就不扫描；
//	· 数据库密码不出后端：检查响应只给 has_password / password_source，
//	  真正落盘由 apply 接口在服务端重新解析候选后完成；
//	· 只合并同一文件/同一配置段内的字段；跨文件只做「占位符精确解析」
//	  （例如 application-dev.yml 的 ${DB_PASSWORD} 去 application-local.yml 里
//	  找同名键），不做跨文件硬拼 host/name。
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- 响应结构 ----------

type setupPort struct {
	Port       int    `json:"port"`
	Source     string `json:"source"`
	Confidence int    `json:"confidence"`
	Evidence   string `json:"evidence,omitempty"`
}

type setupService struct {
	Provided     bool        `json:"provided"`
	Dir          string      `json:"dir,omitempty"`
	Valid        bool        `json:"valid"`
	Kind         string      `json:"kind"`
	Label        string      `json:"label,omitempty"`
	Launch       string      `json:"launch,omitempty"`
	LaunchSource string      `json:"launch_source,omitempty"`
	Ports        []setupPort `json:"ports"`
	PackageJSON  bool        `json:"package_json"`
	NodeModules  bool        `json:"node_modules"`
	ConfigFiles  []string    `json:"config_files,omitempty"`
	Markers      []string    `json:"markers,omitempty"`
}

type setupDB struct {
	ID             string   `json:"id"`
	Host           string   `json:"host,omitempty"`
	Port           int      `json:"port,omitempty"`
	Name           string   `json:"name,omitempty"`
	User           string   `json:"user,omitempty"`
	HasPassword    bool     `json:"has_password"`
	PasswordSource string   `json:"password_source,omitempty"`
	Unresolved     []string `json:"unresolved,omitempty"`
	Source         string   `json:"source"`
	Confidence     int      `json:"confidence"`
	Notes          []string `json:"notes,omitempty"`

	// 只存在于服务端内存：绝不带 json tag，也绝不写日志/审计/AI。
	password string
}

type setupTool struct {
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Version string `json:"version,omitempty"`
	Hint    string `json:"hint,omitempty"`
	Warn    string `json:"warn,omitempty"`
}

type setupPortOcc struct {
	Port    int    `json:"port"`
	State   string `json:"state"`
	PID     int    `json:"pid,omitempty"`
	Name    string `json:"name,omitempty"`
	Source  string `json:"source,omitempty"`
	Service string `json:"service,omitempty"`
}

type setupSQLFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Rel  string `json:"rel,omitempty"`
	Size int64  `json:"size,omitempty"`
}

type setupDatabase struct {
	Found      bool           `json:"found"`
	Files      []string       `json:"files"`
	Candidates []setupDB      `json:"candidates"`
	SQLFiles   []setupSQLFile `json:"sql_files,omitempty"`
}

type setupDBCheckResp struct {
	OK        bool   `json:"ok"`
	Connected bool   `json:"connected"`
	Exists    bool   `json:"exists"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
	CheckedAt string `json:"checked_at"`
}

type setupInspectResp struct {
	OK        bool           `json:"ok"`
	Root      string         `json:"root,omitempty"`
	Frontend  setupService   `json:"frontend"`
	Backend   setupService   `json:"backend"`
	Database  setupDatabase  `json:"database"`
	Ports     []setupPortOcc `json:"ports"`
	Tools     []setupTool    `json:"tools"`
	Warnings  []string       `json:"warnings"`
	CheckedAt string         `json:"checked_at"`
}

// ---------- HTTP ----------

func handleProjectInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		FrontendDir string `json:"frontend_dir"`
		BackendDir  string `json:"backend_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), 400)
		return
	}
	front, back := strings.TrimSpace(body.FrontendDir), strings.TrimSpace(body.BackendDir)
	if front == "" && back == "" {
		http.Error(w, "请至少选择前端或后端目录", 400)
		return
	}
	res := inspectProjectSetup(front, back)
	writeJSON(w, res)
}

// handleProjectDBTest 只读测试一组候选数据库：不写配置、不建库、不执行 SQL。
// 向导在用户勾选“建库/导入”之前先给一次真实连接结论，避免保存后才发现密码错。
func handleProjectDBTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		FrontendDir string `json:"frontend_dir"`
		BackendDir  string `json:"backend_dir"`
		DBID        string `json:"db_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), 400)
		return
	}
	front, back := strings.TrimSpace(body.FrontendDir), strings.TrimSpace(body.BackendDir)
	if front == "" && back == "" {
		http.Error(w, "请至少选择前端或后端目录", 400)
		return
	}
	_, candidates := scanDBCandidates(front, back)
	for _, c := range candidates {
		if c.ID != body.DBID {
			continue
		}
		out := probeSetupDBCandidate(c)
		out.OK = out.Connected
		out.CheckedAt = time.Now().Format(time.RFC3339)
		writeJSON(w, out)
		return
	}
	http.Error(w, "数据库配置已变化，请重新检查后再试", 409)
}

func probeSetupDBCandidate(c setupDB) setupDBCheckResp {
	if len(c.Unresolved) > 0 {
		for _, u := range c.Unresolved {
			if u == "password" {
				return setupDBCheckResp{Error: "密码是未解析的占位符，请先补齐数据库密码"}
			}
		}
	}
	if strings.TrimSpace(c.Name) == "" {
		return setupDBCheckResp{Error: "候选里没有数据库名"}
	}
	host := c.Host
	if host == "" {
		host = "localhost"
	}
	port := c.Port
	if port <= 0 {
		port = 3306
	}
	user := c.User
	if user == "" {
		user = "root"
	}
	version, err := runSetupMysql(c, host, port, user, "SELECT VERSION();")
	if err != nil {
		return setupDBCheckResp{Error: err.Error()}
	}
	out := setupDBCheckResp{Connected: true, Version: firstLine(strings.TrimSpace(version))}
	q := "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='" + strings.ReplaceAll(c.Name, "'", "''") + "';"
	n, err := runSetupMysql(c, host, port, user, q)
	if err != nil {
		out.Error = "连接成功，但查询库是否存在失败：" + err.Error()
		return out
	}
	out.Exists = strings.TrimSpace(n) == "1"
	return out
}

func runSetupMysql(c setupDB, host string, port int, user, sql string) (string, error) {
	exe := mysqlClient()
	if exe == "" {
		return "", fmt.Errorf("未找到 mysql 客户端，请先安装 MySQL")
	}
	ctx, cancel := cmdContext(15 * time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-h", host, "-P", strconv.Itoa(port), "-u", user, "-N", "-B", "-e", sql)
	cmd.Env = append(toolEnv(nil), "MYSQL_PWD="+c.password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := firstLines(strings.TrimSpace(string(out)), 300)
		if msg == "" {
			msg = err.Error()
		}
		return string(out), fmt.Errorf("连接失败：%s", msg)
	}
	return string(out), nil
}

type setupApplyReq struct {
	FrontendDir string `json:"frontend_dir"`
	BackendDir  string `json:"backend_dir"`
	UseDatabase bool   `json:"use_database"`
	DBID        string `json:"db_id"`
}

func handleProjectApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body setupApplyReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), 400)
		return
	}
	front, back := strings.TrimSpace(body.FrontendDir), strings.TrimSpace(body.BackendDir)
	if front == "" && back == "" {
		http.Error(w, "请至少选择前端或后端目录", 400)
		return
	}
	if front != "" {
		if fi, err := os.Stat(front); err != nil || !fi.IsDir() {
			http.Error(w, "前端目录不可用", 400)
			return
		}
	}
	if back != "" {
		if fi, err := os.Stat(back); err != nil || !fi.IsDir() {
			http.Error(w, "后端目录不可用", 400)
			return
		}
	}

	next := cfgSnapshot()
	next.Projects.FrontendDir = front
	next.Projects.BackendDir = back
	appliedDB := false
	if body.UseDatabase {
		ins := inspectProjectSetup(front, back)
		var picked *setupDB
		for i := range ins.Database.Candidates {
			if ins.Database.Candidates[i].ID == body.DBID {
				picked = &ins.Database.Candidates[i]
				break
			}
		}
		if picked == nil {
			http.Error(w, "数据库配置已变化，请重新检查后再保存", 409)
			return
		}
		if picked.Host != "" {
			next.Projects.MySQLHost = picked.Host
		}
		if picked.Port > 0 {
			next.Projects.MySQLPort = picked.Port
		}
		if picked.User != "" {
			next.Projects.MySQLUser = picked.User
		}
		if picked.Name != "" {
			next.Projects.DBName = picked.Name
		}
		// 只有真拿到密码才覆盖；占位符未解析时保留原配置，避免把已有密码抹空。
		if picked.password != "" {
			next.Projects.MySQLPass = picked.password
		}
		appliedDB = true
	}
	normalizeConfig(&next)
	if hard := hardProblems(checkConfig(next)); len(hard) > 0 {
		var msgs []string
		for _, p := range hard {
			msgs = append(msgs, p.String())
		}
		auditNow(actUser, "project_setup", "config.json", "", resDenied, strings.Join(msgs, "；"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "problems": hard})
		return
	}
	saved := cfgUpdate(func(g *Config) { g.Projects = next.Projects })
	saveExternalConfig(saved)
	setCfgStale(true)
	projectProfileInvalidate()
	runtimeInvalidateCache()
	auditNow(actUser, "project_setup", "config.json", "", resOK,
		fmt.Sprintf("frontend=%t backend=%t database=%t", front != "", back != "", appliedDB))
	writeJSON(w, map[string]any{
		"ok":               true,
		"frontend_dir":     front,
		"backend_dir":      back,
		"database_applied": appliedDB,
	})
}

// ---------- 检查 ----------

func inspectProjectSetup(frontDir, backDir string) setupInspectResp {
	prof := buildProjectProfile(frontDir, backDir)
	res := setupInspectResp{
		OK:        true,
		Frontend:  setupServiceFromProfile(prof.Frontend, "web"),
		Backend:   setupServiceFromProfile(prof.Backend, "backend"),
		Warnings:  []string{},
		CheckedAt: time.Now().Format(time.RFC3339),
	}

	dbFiles, candidates := scanDBCandidates(frontDir, backDir)
	res.Database = setupDatabase{Found: len(candidates) > 0, Files: dbFiles, Candidates: candidates}
	res.Database.SQLFiles = scanProjectSQLFiles(frontDir, backDir)

	// 端口占用：只查候选端口和常见端口，不扫全表；归属沿用运行时观察结论。
	portSet := map[int]bool{}
	for _, p := range res.Frontend.Ports {
		portSet[p.Port] = true
	}
	for _, p := range res.Backend.Ports {
		portSet[p.Port] = true
	}
	portSet[8080], portSet[5173] = true, true
	ports := make([]int, 0, len(portSet))
	for p := range portSet {
		if p > 0 {
			ports = append(ports, p)
		}
	}
	sort.Ints(ports)
	occ := scanPortsOnce(ports)
	annotatePortOwners(occ)
	for _, o := range occ {
		res.Ports = append(res.Ports, setupPortOcc{
			Port: o.Port, State: o.State, PID: o.PID, Name: o.Name,
			Source: o.Source, Service: o.Service,
		})
	}

	res.Tools = checkSetupTools(frontDir, backDir, res.Frontend, res.Backend)
	for _, t := range res.Tools {
		if t.Warn != "" {
			res.Warnings = append(res.Warnings, t.Warn)
		}
	}

	if res.Frontend.Provided && !res.Frontend.PackageJSON {
		res.Warnings = append(res.Warnings, "前端目录里没有找到 package.json")
	}
	if res.Backend.Provided && !res.Backend.Valid {
		res.Warnings = append(res.Warnings, "后端目录不可用")
	}
	if res.Backend.Provided && res.Backend.Valid && res.Backend.Kind == "go" &&
		!hasFile(filepath.Join(backDir, "go.mod")) && !hasFile(filepath.Join(backDir, "main.go")) {
		res.Warnings = append(res.Warnings, "后端目录没有找到 go.mod / main.go / pom.xml，类型可能识别不准")
	}
	for _, c := range res.Database.Candidates {
		if len(c.Unresolved) > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s 的 %s 引用了环境变量，未能解析", c.Source, strings.Join(c.Unresolved, "、")))
		}
	}
	return res
}

func setupServiceFromProfile(p projectServiceProfile, target string) setupService {
	provided := strings.TrimSpace(p.Dir) != ""
	valid := false
	if provided {
		if fi, err := os.Stat(p.Dir); err == nil && fi.IsDir() {
			valid = true
		}
	}
	s := setupService{
		Provided: provided, Dir: p.Dir, Valid: valid, Kind: p.Kind,
		Label: p.ProjectLabel, PackageJSON: hasFile(filepath.Join(p.Dir, "package.json")),
		NodeModules: hasFile(filepath.Join(p.Dir, "node_modules")),
		ConfigFiles: append([]string(nil), p.ConfigFiles...),
		Markers:     append([]string(nil), p.Markers...),
	}
	if !valid {
		s.Kind = "none"
		return s
	}
	hints := append([]svcPortHint(nil), p.PortHints...)
	sortPortHints(hints)
	seen := map[int]bool{}
	for _, h := range hints {
		if h.Port <= 0 || seen[h.Port] {
			continue
		}
		seen[h.Port] = true
		s.Ports = append(s.Ports, setupPort{Port: h.Port, Source: h.Source, Confidence: h.Confidence, Evidence: h.Evidence})
		if len(s.Ports) >= 5 {
			break
		}
	}
	if target == "web" {
		// 这里刻意不读全局 cfg.Projects.WebScript：检查的是用户刚选的目录，
		// 不是当前已保存项目。否则换个项目也会把全局脚本名当成它的启动方式。
		if script, _, source, _ := inferWebScript(p.Dir, ""); script != "" {
			s.Launch = "npm run " + script
			s.LaunchSource = source
		} else if len(readPackageScripts(p.Dir)) == 0 {
			s.Launch = "npm run serve"
			s.LaunchSource = "默认（未读到 package.json）"
		}
	} else if s.Kind == "maven" {
		s.Launch = "mvn spring-boot:run"
		s.LaunchSource = "pom.xml"
	} else if file, why, ok := launchBackendFileOrDefault(p.Dir); ok {
		s.Launch = "go run " + file
		s.LaunchSource = why
	}
	return s
}

func checkSetupTools(frontDir, backDir string, front, back setupService) []setupTool {
	var out []setupTool
	needFront := front.Provided && front.Valid
	needBack := back.Provided && back.Valid
	if needFront {
		node := findExe("node.exe", "node")
		t := setupTool{Name: "Node.js", Found: node != "", Hint: "前端启动需要 Node.js"}
		if node != "" {
			t.Version = runToolVersion(node, "--version")
		}
		out = append(out, t)
	}
	if needBack {
		switch back.Kind {
		case "maven":
			mvn := resolveMavenCmd(backDir)
			t := setupTool{Name: "Maven", Found: mvn != "", Hint: "Maven 项目需要 mvn 或 mvnw.cmd"}
			if mvn != "" {
				// mvnw.cmd 在部分机器上会再调 powershell；这里只做只读展示，
				// 不执行 wrapper，避免把控制台编码/环境问题变成一条乱码版本号。
				if strings.HasSuffix(strings.ToLower(mvn), ".cmd") || strings.HasSuffix(strings.ToLower(mvn), ".bat") {
					t.Version = filepath.Base(mvn)
				} else {
					t.Version = runToolVersion(mvn, "-v")
					if len(t.Version) > 80 {
						t.Version = t.Version[:80]
					}
				}
			}
			out = append(out, t)
			java, javaVer := javaForProject(backDir)
			jt := setupTool{Name: "JDK", Found: java != "", Hint: "Spring Boot 需要 JDK"}
			if java != "" {
				jt.Version = javaVer
				if req := mavenRequiredJava(backDir); req != "" && javaVer != "" && javaVerLess(javaVer, req) {
					jt.Warn = fmt.Sprintf("项目要求 Java %s，当前只找到 JDK %s，启动会失败", req, javaVer)
				} else if req != "" && javaVer != "" {
					// 当前 PATH/JAVA_HOME 可能是 JDK8，但启动时会自动切换到兼容 JDK。
					// 把这件事提前说清楚，避免用户以为检查结果在“假装通过”。
					cur := currentJavaPath()
					if cur != "" && !strings.EqualFold(filepath.Clean(cur), filepath.Clean(java)) {
						jt.Hint = "启动时会使用兼容 JDK：" + java
					}
				}
			}
			out = append(out, jt)
		case "go":
			g := findExe("go.exe", "go")
			t := setupTool{Name: "Go", Found: g != "", Hint: "Go 后端需要 go 工具链"}
			if g != "" {
				t.Version = strings.TrimSpace(strings.TrimPrefix(runToolVersion(g, "version"), "go version"))
			}
			out = append(out, t)
		}
	}
	return out
}

func runToolVersion(path string, args ...string) string {
	ctx, cancel := cmdContext(8 * time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// ---------- 数据库配置扫描 ----------

var (
	jdbcMySQLRe = regexp.MustCompile(`(?i)jdbc:mysql://([^:/\s"'<>]+)(?::(\d+))?/([^\s?"'&;]+)`)
	dsnMySQLRe  = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]+):([^@\s"'<>]+)@tcp\(([^:)\s]+)(?::(\d+))?\)/([A-Za-z0-9_.-]+)`)
	placeholder = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)(?::([^}]*))?\}$`)
	dockerPort  = regexp.MustCompile(`["']?(\d{2,5}):(\d{2,5})["']?`)
)

func scanDBCandidates(frontDir, backDir string) ([]string, []setupDB) {
	files := collectDBFiles(frontDir, backDir)
	secrets := collectSecretValues(files)
	var out []setupDB
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || len(b) == 0 || len(b) > 512*1024 {
			continue
		}
		c, ok := parseDBFile(f, dbDisplayPath(f, frontDir, backDir), string(b))
		if !ok {
			continue
		}
		resolveDBPlaceholders(&c, secrets)
		key := strings.ToLower(c.Source + "|" + c.Host + "|" + c.Name + "|" + c.User)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
		if len(out) >= 6 {
			break
		}
	}
	// 先按「URL/DSN > docker-compose > .env > 普通 yaml」的置信度排序，再按来源稳定排。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Source < out[j].Source
	})
	display := make([]string, 0, len(files))
	for i, f := range files {
		if i >= 12 {
			break
		}
		display = append(display, dbDisplayPath(f, frontDir, backDir))
	}
	return display, out
}

// scanProjectSQLFiles 找项目里可直接导入的 .sql（最多 12 个，深度 3）。
// 不读内容：向导只负责让用户选文件，执行仍由现有 init-db / apply-sql 流程做。
func scanProjectSQLFiles(frontDir, backDir string) []setupSQLFile {
	seen := map[string]bool{}
	var out []setupSQLFile
	skip := map[string]bool{
		"node_modules": true, ".git": true, "target": true, "dist": true,
		"build": true, ".idea": true, "vendor": true, "__pycache__": true,
	}
	roots := []string{frontDir, backDir}
	// 典型前后端分仓（project/web + project/server）的 SQL 常在项目根，
	// 不在任何一个子目录里。只在两边同父、且父不是盘符根时向上多扫一层。
	if strings.TrimSpace(frontDir) != "" && strings.TrimSpace(backDir) != "" {
		pf, pb := filepath.Dir(filepath.Clean(frontDir)), filepath.Dir(filepath.Clean(backDir))
		if strings.EqualFold(pf, pb) && pf != filepath.Dir(pf) {
			roots = append(roots, pf)
		}
	}
	seenRoot := map[string]bool{}
	for _, root := range roots {
		root = filepath.Clean(root)
		if strings.TrimSpace(root) == "" || root == "." || seenRoot[strings.ToLower(root)] {
			continue
		}
		seenRoot[strings.ToLower(root)] = true
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				if skip[strings.ToLower(info.Name())] {
					return filepath.SkipDir
				}
				rel, _ := filepath.Rel(root, path)
				if rel != "." && strings.Count(rel, string(filepath.Separator)) >= 3 {
					return filepath.SkipDir
				}
				return nil
			}
			if len(out) >= 12 || !strings.EqualFold(filepath.Ext(info.Name()), ".sql") {
				return nil
			}
			if info.Size() <= 0 || info.Size() > 64*1024*1024 {
				return nil
			}
			p := filepath.Clean(path)
			if seen[strings.ToLower(p)] {
				return nil
			}
			seen[strings.ToLower(p)] = true
			rel, _ := filepath.Rel(root, p)
			out = append(out, setupSQLFile{
				Path: p, Name: info.Name(), Rel: filepath.ToSlash(rel), Size: info.Size(),
			})
			return nil
		})
		if len(out) >= 12 {
			break
		}
	}
	priority := func(name string) int {
		switch strings.ToLower(name) {
		case "schema.sql":
			return 0
		case "init.sql", "init_db.sql", "initdb.sql":
			return 1
		case "database.sql", "db.sql":
			return 2
		case "data.sql":
			return 3
		}
		return 9
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := priority(out[i].Name), priority(out[j].Name)
		if pi != pj {
			return pi < pj
		}
		return out[i].Rel < out[j].Rel
	})
	return out
}

func collectDBFiles(frontDir, backDir string) []string {
	set := map[string]bool{}
	add := func(p string) {
		p = filepath.Clean(p)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() <= 512*1024 {
			set[strings.ToLower(p)] = true
		}
	}
	addGlob := func(dir, pattern string) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		ms, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, m := range ms {
			add(m)
		}
	}
	for _, dir := range []string{frontDir, backDir} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, n := range []string{".env", ".env.local", ".env.development", ".env.production", "config.yaml", "config.yml", "database.yaml", "database.yml", "docker-compose.yml", "docker-compose.yaml", "main.go", "db.go", "database.go", "config.go"} {
			add(filepath.Join(dir, n))
		}
		for _, pat := range []string{"application*.yml", "application*.yaml", "application*.properties", "docker-compose*.yml", "docker-compose*.yaml"} {
			addGlob(dir, pat)
		}
	}
	if backDir != "" {
		addGlob(filepath.Join(backDir, "src", "main", "resources"), "application*.yml")
		addGlob(filepath.Join(backDir, "src", "main", "resources"), "application*.yaml")
		addGlob(filepath.Join(backDir, "src", "main", "resources"), "application*.properties")
	}
	// 浅层遍历：只到 3 层，跳过依赖/构建目录，防止在 node_modules 里翻几万文件。
	for _, root := range []string{frontDir, backDir} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := strings.ToLower(d.Name())
				if name == "node_modules" || name == ".git" || name == "target" || name == "dist" || name == "build" || name == ".idea" || name == "vendor" || name == "__pycache__" {
					return filepath.SkipDir
				}
				rel, _ := filepath.Rel(root, path)
				if rel != "." && strings.Count(rel, string(filepath.Separator)) >= 3 {
					return filepath.SkipDir
				}
				return nil
			}
			if len(set) >= 60 {
				return filepath.SkipAll
			}
			if isDBFileName(d.Name()) {
				if fi, e := d.Info(); e == nil && fi.Size() <= 512*1024 {
					set[strings.ToLower(path)] = true
				}
			}
			return nil
		})
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func isDBFileName(name string) bool {
	n := strings.ToLower(name)
	if strings.HasPrefix(n, ".env") {
		return true
	}
	if strings.HasPrefix(n, "application") {
		return strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".properties")
	}
	if strings.HasPrefix(n, "docker-compose") {
		return strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml")
	}
	switch n {
	case "config.yaml", "config.yml", "database.yaml", "database.yml", "db.yaml", "db.yml",
		"main.go", "db.go", "database.go", "config.go", "mysql.go":
		return true
	}
	return false
}

func dbDisplayPath(path, frontDir, backDir string) string {
	for _, root := range []string{backDir, frontDir} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if rel, err := filepath.Rel(root, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}

func parseDBFile(path, display, text string) (setupDB, bool) {
	c := setupDB{Source: display, Confidence: 60, Port: 3306}
	notes := []string{}

	// 1) JDBC URL：字段最全、最可信。
	if m := jdbcMySQLRe.FindStringSubmatch(text); m != nil {
		c.Host = strings.TrimSpace(m[1])
		if m[2] != "" {
			c.Port, _ = strconv.Atoi(m[2])
		}
		c.Name = strings.TrimSpace(m[3])
		c.Confidence = 92
		// URL 上的 user/password 查询参数（少见，但 Spring 也允许）
		if q := firstKV(text, "user", "username"); q != "" {
			c.User = cleanVal(q)
		}
		if q := firstKV(text, "password", "passwd", "pwd"); q != "" {
			c.password = cleanVal(q)
		}
	} else if m := dsnMySQLRe.FindStringSubmatch(text); m != nil {
		c.User = strings.TrimSpace(m[1])
		c.password = strings.TrimSpace(m[2])
		c.Host = strings.TrimSpace(m[3])
		if m[4] != "" {
			c.Port, _ = strconv.Atoi(m[4])
		}
		c.Name = strings.TrimSpace(m[5])
		c.Confidence = 88
	} else {
		// 2) 只看数据库配置段，避免把 server.port / 别的 host 当数据库端口。
		sec := dbSection(text)
		if sec == "" {
			sec = text
		}
		c.Host = firstKV(sec, "db_host", "db-host", "mysql_host", "mysql-host", "datasource.host", "database.host", "host", "DB_HOST", "MYSQL_HOST", "DATABASE_HOST")
		c.User = firstKV(sec, "db_user", "db-user", "mysql_user", "mysql-user", "datasource.username", "datasource.user", "database.user", "username", "user", "DB_USER", "DB_USERNAME", "MYSQL_USER", "DATABASE_USER")
		c.password = firstKV(sec, "db_password", "db-password", "mysql_password", "mysql-password", "datasource.password", "database.password", "password", "passwd", "pwd", "DB_PASSWORD", "DB_PASS", "MYSQL_PASSWORD", "MYSQL_ROOT_PASSWORD", "DATABASE_PASSWORD")
		c.Name = firstKV(sec, "db_name", "db-name", "mysql_database", "datasource.database", "database.name", "database", "db", "name", "schema", "DB_NAME", "DB_DATABASE", "MYSQL_DATABASE", "DATABASE_NAME")
		if p := firstKV(sec, "db_port", "db-port", "mysql_port", "mysql-port", "datasource.port", "database.port", "port", "DB_PORT", "MYSQL_PORT", "MYSQL_TCP_PORT"); p != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(p, `"'`))); err == nil {
				c.Port = n
			}
		}
		// docker-compose：容器端口映射（3306:3306）
		if strings.HasPrefix(strings.ToLower(filepath.Base(path)), "docker-compose") {
			c.Confidence = 82
			if ms := dockerPort.FindStringSubmatch(text); ms != nil && (ms[1] == "3306" || ms[2] == "3306") {
				c.Port = 3306
				notes = append(notes, "端口来自 docker-compose 映射")
			}
			if c.User == "" && c.password != "" {
				c.User = "root"
				notes = append(notes, "docker-compose 只声明了 root 密码")
			}
			if c.Host == "" {
				c.Host = "127.0.0.1"
			}
		} else if strings.HasPrefix(strings.ToLower(filepath.Base(path)), ".env") {
			c.Confidence = 85
			if c.Host == "" && (c.Name != "" || c.User != "") {
				c.Host = "127.0.0.1"
			}
		} else if strings.HasPrefix(strings.ToLower(filepath.Base(path)), "application") {
			c.Confidence = 78
		}
	}

	if c.Host == "" && c.Name == "" && c.User == "" {
		return setupDB{}, false
	}
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Port <= 0 {
		c.Port = 3306
	}
	c.Host = cleanVal(c.Host)
	c.Name = cleanVal(c.Name)
	c.User = cleanVal(c.User)
	c.password = cleanVal(c.password)
	if c.password != "" {
		c.Confidence += 4
	}
	if c.User != "" || c.Name != "" {
		c.Confidence += 2
	}
	if c.Confidence > 96 {
		c.Confidence = 96
	}
	if c.password != "" && !strings.Contains(c.password, "${") {
		c.HasPassword = true
		c.PasswordSource = display
	}
	c.Notes = notes
	c.ID = dbCandidateID(path, c)
	return c, true
}

func cleanVal(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	s = strings.TrimSuffix(s, ",")
	return strings.TrimSpace(s)
}

func dbCandidateID(path string, c setupDB) string {
	key := strings.ToLower(filepath.Clean(path)) + "|" + c.Host + "|" + strconv.Itoa(c.Port) + "|" + c.Name + "|" + c.User
	h := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", h)[:10]
}

func dbSection(text string) string {
	lower := strings.ToLower(text)
	for _, marker := range []string{"datasource:", "database:", "mysql:", "db:"} {
		idx := strings.Index(lower, marker)
		if idx < 0 {
			continue
		}
		start := idx
		// 从 marker 后逐行找第一个顶格 key（非注释、非空），作为段尾。
		lines := strings.Split(text[start:], "\n")
		end := len(text) - start
		pos := len(lines[0]) + 1
		for i := 1; i < len(lines); i++ {
			ln := lines[i]
			if pos >= end {
				break
			}
			trim := strings.TrimSpace(ln)
			if trim == "" || strings.HasPrefix(trim, "#") {
				pos += len(ln) + 1
				continue
			}
			if len(ln) == len(strings.TrimLeft(ln, " \t")) && strings.HasSuffix(trim, ":") {
				end = pos
				break
			}
			pos += len(ln) + 1
		}
		return text[start:end]
	}
	return ""
}

func firstKV(text string, keys ...string) string {
	for _, k := range keys {
		re := regexp.MustCompile(`(?im)^[ \t-]*` + regexp.QuoteMeta(k) + `[ \t]*[:=][ \t]*(?:"([^"]*)"|'([^']*)'|([^\s#;]+))`)
		m := re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		for _, g := range m[1:] {
			if strings.TrimSpace(g) != "" {
				return strings.TrimSpace(g)
			}
		}
	}
	return ""
}

type secretValue struct {
	value  string
	source string
}

func collectSecretValues(files []string) map[string]secretValue {
	out := map[string]secretValue{}
	for _, f := range files {
		base := strings.ToLower(filepath.Base(f))
		if !strings.HasPrefix(base, ".env") && !strings.HasPrefix(base, "application") && !strings.HasSuffix(base, ".properties") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil || len(b) > 512*1024 {
			continue
		}
		text := string(b)
		envRe := regexp.MustCompile(`(?m)^[ \t]*(?:export[ \t]+)?([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*(.+)$`)
		yamlRe := regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)[ \t]*[:=][ \t]*(?:"([^"]*)"|'([^']*)'|([^\s#;]+))`)
		for _, re := range []*regexp.Regexp{envRe, yamlRe} {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				name := strings.TrimSpace(m[1])
				if !secretNameRelevant(name) {
					continue
				}
				val := ""
				for _, g := range m[2:] {
					if strings.TrimSpace(g) != "" {
						val = strings.TrimSpace(g)
						break
					}
				}
				val = cleanVal(val)
				if val == "" || strings.Contains(val, "${") || len(val) > 1024 {
					continue
				}
				if _, ok := out[name]; !ok {
					out[name] = secretValue{value: val, source: filepath.Base(f)}
				}
			}
		}
	}
	return out
}

func secretNameRelevant(name string) bool {
	n := strings.ToUpper(name)
	return strings.HasPrefix(n, "DB_") || strings.HasPrefix(n, "MYSQL_") ||
		strings.HasPrefix(n, "DATABASE_") || strings.HasPrefix(n, "SPRING_DATASOURCE_") ||
		strings.HasSuffix(n, "_PASSWORD") || n == "DB_PASS"
}

func resolveDBPlaceholders(c *setupDB, secrets map[string]secretValue) {
	// 用户名：${DB_USERNAME:root} 取默认值；${DB_USERNAME} 再看本地 secret。
	if strings.Contains(c.User, "${") {
		if v, varName, ok := expandPlaceholder(c.User); ok {
			c.User = v
		} else if v, ok := secrets[varName]; ok {
			c.User = v.value
			c.Notes = append(c.Notes, fmt.Sprintf("用户名通过 ${%s} 从 %s 解析", varName, v.source))
		} else if v := strings.TrimSpace(os.Getenv(varName)); v != "" {
			c.User = v
			c.Notes = append(c.Notes, fmt.Sprintf("用户名通过 ${%s} 从环境变量解析", varName))
		} else {
			c.Unresolved = append(c.Unresolved, "username")
			c.User = ""
		}
	}
	if strings.Contains(c.password, "${") {
		if v, varName, ok := expandPlaceholder(c.password); ok {
			c.password = v
			c.HasPassword = true
			c.PasswordSource = c.Source
			c.Notes = append(c.Notes, "密码使用了占位符默认值")
		} else if v, ok := secrets[varName]; ok {
			c.password = v.value
			c.HasPassword = true
			c.PasswordSource = v.source
			c.Notes = append(c.Notes, fmt.Sprintf("密码通过 ${%s} 从 %s 解析", varName, v.source))
		} else if v := strings.TrimSpace(os.Getenv(varName)); v != "" {
			c.password = v
			c.HasPassword = true
			c.PasswordSource = "环境变量"
			c.Notes = append(c.Notes, fmt.Sprintf("密码通过 ${%s} 从环境变量解析", varName))
		} else {
			c.password = ""
			c.Unresolved = append(c.Unresolved, "password")
		}
	} else if c.password != "" {
		c.HasPassword = true
		if c.PasswordSource == "" {
			c.PasswordSource = c.Source
		}
	}
}

// expandPlaceholder 仅处理完整占位符 `${VAR:default}`；有非空默认值时返回默认值。
func expandPlaceholder(s string) (string, string, bool) {
	m := placeholder.FindStringSubmatch(cleanVal(s))
	if m == nil {
		return s, "", false
	}
	if len(m) > 2 && strings.TrimSpace(m[2]) != "" {
		return strings.TrimSpace(m[2]), m[1], true
	}
	return "", m[1], false
}
