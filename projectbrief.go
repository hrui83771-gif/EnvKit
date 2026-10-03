package main

// projectbrief.go —— 项目画像：让 AI 不用调工具也对项目有基本认知。
//
// 每回合的环境快照里会带上这份摘要（见 aiHealthSnapshot 的 project 字段），
// 于是"这是什么项目、怎么跑起来"这类问题在第一轮就有了抓手，
// 而不是先反问用户一行再动手。摘要很小（只有结构化结论），且带 2 分钟缓存，
// 避免每轮都去扫几万文件的仓库。

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

// ---------- 画像结构 ----------

type projectBrief struct {
	Roots       []string          `json:"roots"`
	Kinds       []string          `json:"kinds"`        // go / node / vue / react / maven / python / rust / docker
	Packages    []string          `json:"packages"`     // go 模块名 / npm 包名
	Scripts     []string          `json:"scripts"`      // package.json 里的可用脚本（已裁剪）
	Entries     map[string]string `json:"entries"`      // 入口文件猜测：go_main / web_main
	Configs     []string          `json:"configs"`      // 值得进一步精读的配置文件
	PortHints   []int             `json:"port_hints"`   // 端口线索：来自配置文件里的 port 配置
	NodeModules bool              `json:"node_modules"` // 依赖是否已安装（未装则 npm run 必然失败）
	Note        string            `json:"note,omitempty"`
}

// ---------- 缓存 ----------
// key 变了（项目目录改了）立刻重建；否则 2 分钟内复用，避免每回合都做磁盘遍历。

var (
	briefMu    sync.Mutex
	briefCache string
	briefKey   string
	briefAt    time.Time
)

func aiProjectBriefJSON() string {
	roots := projectRoots()
	key := strings.Join(roots, "|")
	briefMu.Lock()
	defer briefMu.Unlock()
	if briefCache != "" && briefKey == key && time.Since(briefAt) < 2*time.Minute {
		return briefCache
	}
	b, _ := json.Marshal(buildProjectBrief(roots))
	briefCache, briefKey, briefAt = string(b), key, time.Now()
	return briefCache
}

// ---------- 画像生成 ----------

var (
	reModuleLine = regexp.MustCompile(`(?m)^module\s+(\S+)`)
	rePortNum    = regexp.MustCompile(`(?i)port\s*[:=]\s*["']?(\d{2,5})`)
)

// briefConfigFiles 值得读取示意图 port 线索的配置文件名。
var briefConfigFiles = map[string]bool{
	"vue.config.js": true, "vite.config.js": true, "vite.config.ts": true,
	"webpack.config.js": true, "nuxt.config.js": true, "next.config.js": true,
	"docker-compose.yml": true, "docker-compose.yaml": true,
	"application.yml": true, "application.yaml": true, "application.properties": true,
	"config.yaml": true, "config.yml": true, "Makefile": true,
}

func buildProjectBrief(roots []string) projectBrief {
	b := projectBrief{Roots: roots, Entries: map[string]string{}}
	if len(roots) == 0 {
		b.Note = "未配置项目目录"
		return b
	}
	kinds := map[string]bool{}
	var configs []string
	scanned := 0

	// 扫描点 = 项目根 + 一级子目录：真实仓库常常是"根目录下放 server/ 与 web/"，
	// 只扫根目录会把两个子项目的 go.mod / package.json 全漏掉。
	scanRoots := make([]string, 0, len(roots)*2)
	seen := map[string]bool{}
	addRoot := func(p string) {
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			return
		}
		k := normPath(p)
		if seen[k] {
			return
		}
		seen[k] = true
		scanRoots = append(scanRoots, p)
	}
	for _, root := range roots {
		addRoot(root)
		if entries, err := os.ReadDir(root); err == nil {
			for _, e := range entries {
				if e.IsDir() && !exploreSkipDir(e.Name()) {
					addRoot(filepath.Join(root, e.Name()))
				}
			}
		}
	}

	// 标志性文件判定
	for _, root := range scanRoots {
		if st, err := os.Stat(filepath.Join(root, "node_modules")); err == nil && st.IsDir() {
			b.NodeModules = true
		}
		if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
			kinds["go"] = true
			if m := reModuleLine.FindStringSubmatch(string(data)); len(m) > 1 {
				b.Packages = append(b.Packages, m[1])
			}
			configs = append(configs, "go.mod")
			if hasFile(filepath.Join(root, "main.go")) {
				b.Entries["go_main"] = relOrAbs(filepath.Join(root, "main.go"))
			}
		}
		if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
			name, scripts, tags := parsePackageJSON(data)
			kinds["node"] = true
			for _, t := range tags {
				kinds[t] = true
			}
			if name != "" {
				b.Packages = append(b.Packages, name)
			}
			b.Scripts = append(b.Scripts, scripts...)
			configs = append(configs, "package.json")
			for _, c := range []string{"src/main.js", "src/main.ts", "src/main.tsx"} {
				if hasFile(filepath.Join(root, c)) {
					b.Entries["web_main"] = relOrAbs(filepath.Join(root, c))
					break
				}
			}
			if !b.NodeModules {
				b.Note = "node_modules 不存在/不在统计范围：npm run 大概率会失败，需先 npm install"
			}
		}
		for f, kind := range map[string]string{
			"pom.xml": "maven", "build.gradle": "gradle", "requirements.txt": "python",
			"pyproject.toml": "python", "Cargo.toml": "rust",
			"Makefile": "make", "docker-compose.yml": "docker", "docker-compose.yaml": "docker",
		} {
			if hasFile(filepath.Join(root, f)) {
				kinds[kind] = true
			}
		}
	}

	// 浅层扫描（≤2 层）：找 cmd/*/main.go、配置文件，不进依赖/构建目录
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if scanned >= exploreMaxBriefFiles || depth > 2 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if scanned >= exploreMaxBriefFiles {
				return
			}
			name := e.Name()
			full := filepath.Join(dir, name)
			if e.IsDir() {
				if exploreSkipDir(name) {
					continue
				}
				walk(full, depth+1)
				continue
			}
			scanned++
			if exploreSecretPath(full) {
				continue
			}
			if name == "main.go" && b.Entries["go_main"] == "" {
				b.Entries["go_main"] = relOrAbs(full)
			}
			if briefConfigFiles[name] {
				configs = append(configs, relOrAbs(full))
				for _, p := range portHintsIn(full) {
					if !containsInt(b.PortHints, p) && len(b.PortHints) < 5 {
						b.PortHints = append(b.PortHints, p)
					}
				}
			}
		}
	}
	for _, r := range roots {
		walk(r, 1)
	}

	b.Kinds = sortedKinds(kinds)
	b.Scripts = dedupTrim(b.Scripts, 8)
	b.Packages = dedupTrim(b.Packages, 5)
	b.Configs = dedupTrim(configs, 12)
	sort.Ints(b.PortHints)
	return b
}

func hasFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// parsePackageJSON 抽取包名、脚本清单与框架标签（不看依赖版本号，保持摘要精简）。
func parsePackageJSON(data []byte) (name string, scripts []string, tags []string) {
	var pkg struct {
		Name         string            `json:"name"`
		Scripts      map[string]string `json:"scripts"`
		Dependencies map[string]any    `json:"dependencies"`
		DevDeps      map[string]any    `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", nil, nil
	}
	name = pkg.Name
	keys := make([]string, 0, len(pkg.Scripts))
	for k := range pkg.Scripts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := pkg.Scripts[k]
		if len(s) > 80 {
			s = s[:80] + "…"
		}
		scripts = append(scripts, k+": "+s)
	}
	has := func(name string) bool {
		_, a := pkg.Dependencies[name]
		_, b := pkg.DevDeps[name]
		return a || b
	}
	for dep, tag := range map[string]string{
		"vue": "vue", "react": "react", "vite": "vite", "next": "next",
		"nuxt": "nuxt", "electron": "electron", "@angular/core": "angular",
	} {
		if has(dep) {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return name, scripts, tags
}

// portHintsIn 从配置文件里抓 port 线索（可能来自注释或示例，故叫 hints 不叫 facts）。
func portHintsIn(path string) []int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() > 256*1024 {
		return nil
	}
	var builder strings.Builder
	buf := make([]byte, 64*1024)
	if n, _ := f.Read(buf); n > 0 {
		builder.Write(buf[:n])
	}
	var out []int
	for _, m := range rePortNum.FindAllStringSubmatch(builder.String(), -1) {
		if len(m) > 1 {
			if v, err := strconv.Atoi(m[1]); err == nil && v > 0 && v < 65536 {
				out = append(out, v)
			}
		}
	}
	return out
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func sortedKinds(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupTrim(in []string, max int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}

// ---------- 工具注册 ----------

func init() {
	aiToolRegistry["get_project_brief"] = aiTool{
		Desc:   "读取项目画像：根目录、语言与框架、包名、可用启动脚本、入口文件、配置文件清单、端口线索、依赖是否已安装（只读）。想知道「这是什么项目、要怎么跑起来」时先调它，比层层 list 更快",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Execute: func(map[string]any) (string, error) {
			roots := projectRoots()
			if len(roots) == 0 {
				return exploreNoRootMsg, nil
			}
			auditNow(actAI, "explore_brief", strings.Join(roots, "|"), "", resOK, "")
			return untrusted(aiProjectBriefJSON()), nil
		},
	}
}
