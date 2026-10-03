package main

// envreq.go —— v2.3 N7：环境符合性检查（项目要求 vs 实际版本）
//
// ## 要补的缺口
//
// 现有环境检测只回答"装了什么版本"（"Go 1.23.6"、"Node 20.11.1"），
// 不回答"够不够用"。于是这类故障很难自查：
//
//   项目 go.mod 写 `go 1.24.0`，机器上是 1.23.6
//   → `go build` 报 "go.mod requires go >= 1.24.0"
//   → 用户看到的是一个来自编译器的、看不出所以然的错误
//
// 前端同理：package.json 的 `engines.node` 声明了 >=18，实际是 16，
// npm install 会警告但往往不拦，运行时才炸在某个语法错误上。
//
// **"执行不等于成功"在这里也成立**：装了 Go 不等于能编这个项目。
//
// ## 关键判断：没声明要求时必须说"项目未声明"，不能说"符合"
//
// 绝大多数项目不写 engines / 不写 go 指令的最低版本。此时如果说"符合"，
// 就是把"没检查到"当成"检查通过"——与 v2.0 修过的链端判据同源。
// 所以每个组件都有三态：符合 / 不符合 / **项目未声明**。
//
// ## 版本比较：只做"够不够"，不做"是否一致"
//
// 要求 ^1.24.0 而实际 1.25.0 是**完全正常**的（向后兼容）。
// 判成"不一致"会制造大量假警报，而用户会开始无视所有告警。
// 所以只判"实际 < 要求"这一种失败。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EnvReq 一个组件的项目要求与实际版本比对结果。
type EnvReq struct {
	Component string `json:"component"` // go / node
	Required  string `json:"required,omitempty"`
	Actual    string `json:"actual,omitempty"`
	// Status: ok（够用）| too_low（不够）| undeclared（项目未声明）| not_installed
	Status string `json:"status"`
	// Why 说清结论依据，给人看
	Why string `json:"why"`
	// Action 下一步该做什么（Status != ok 时有值）
	Action string `json:"action,omitempty"`
}

const (
	reqOK         = "ok"
	reqTooLow     = "too_low"
	reqUndeclared = "undeclared"
	reqNotInstall = "not_installed"
)

// goModGoRe 从 go.mod 里取 go 指令的版本。
var goModGoRe = regexp.MustCompile(`(?m)^\s*go\s+(\d+\.\d+(?:\.\d+)?)`)

// nodeEnginesRe 从 package.json 的 engines.node 取最低要求。
// 只取最低版本（^18.2.0 / >=18 / 18.x 都能解析出版本号），
// 上限（<19）刻意不判——上限极少写且容易误判。
var nodeEnginesRe = regexp.MustCompile(`(\d+)(?:\.(\d+))?(?:\.(\d+))?`)

// checkGoReq 比对 Go 版本与 go.mod 要求。
func checkGoReq(backendDir string, actual string) EnvReq {
	r := EnvReq{Component: "go", Actual: actual}
	if strings.TrimSpace(actual) == "" {
		r.Status = reqNotInstall
		r.Why = "未检测到 Go"
		r.Action = "在「环境安装」里安装 Go"
		return r
	}
	if strings.TrimSpace(backendDir) == "" {
		r.Status = reqUndeclared
		r.Why = "未配置后端目录，无法读取 go.mod"
		return r
	}
	b, err := os.ReadFile(filepath.Join(backendDir, "go.mod"))
	if err != nil {
		r.Status = reqUndeclared
		r.Why = "后端目录没有 go.mod（非 Go 项目，或 go.mod 在上层目录）"
		return r
	}
	m := goModGoRe.FindStringSubmatch(string(b))
	if m == nil {
		r.Status = reqUndeclared
		r.Why = "go.mod 未声明 go 指令版本（说明它不挑版本）"
		return r
	}
	r.Required = m[1]
	return compareReq(r, actual, "go.mod 声明 go "+r.Required)
}

// checkNodeReq 比对 Node 版本与 package.json engines 要求。
func checkNodeReq(frontendDir string, actual string) EnvReq {
	r := EnvReq{Component: "node", Actual: actual}
	if strings.TrimSpace(actual) == "" {
		r.Status = reqNotInstall
		r.Why = "未检测到 Node.js"
		r.Action = "在「环境安装」里安装 Node.js"
		return r
	}
	if strings.TrimSpace(frontendDir) == "" {
		r.Status = reqUndeclared
		r.Why = "未配置前端目录，无法读取 package.json"
		return r
	}
	b, err := os.ReadFile(filepath.Join(frontendDir, "package.json"))
	if err != nil {
		r.Status = reqUndeclared
		r.Why = "前端目录没有 package.json（非 Node 项目）"
		return r
	}
	engines := parseEnginesNode(string(b))
	if engines == "" {
		r.Status = reqUndeclared
		r.Why = "package.json 的 engines 未声明 node 版本（说明它不挑版本）"
		return r
	}
	r.Required = engines
	return compareReq(r, actual, "package.json 的 engines.node 要求 "+engines)
}

// parseEnginesNode 从 package.json 文本里取 engines.node 的最低版本。
// 刻意不引 JSON 解析：只匹配这一段，容错更好，且省一次反序列化失败的处理。
func parseEnginesNode(src string) string {
	i := strings.Index(src, "\"engines\"")
	if i < 0 {
		return ""
	}
	rest := src[i+9:]
	k := strings.Index(rest, "\"node\"")
	if k < 0 {
		return ""
	}
	rest = rest[k+6:]
	c := strings.Index(rest, ":")
	if c < 0 {
		return ""
	}
	rest = rest[c+1:]
	// 取第一个引号串
	q1 := strings.Index(rest, "\"")
	if q1 < 0 {
		return ""
	}
	rest = rest[q1+1:]
	q2 := strings.Index(rest, "\"")
	if q2 < 0 {
		return ""
	}
	spec := rest[:q2]
	// 取版本号：^18.2.0 → 18.2.0；>=18 → 18；18.x → 18
	m := nodeEnginesRe.FindStringSubmatch(spec)
	if m == nil {
		return ""
	}
	v := m[1]
	if m[2] != "" {
		v += "." + m[2]
	}
	if m[3] != "" {
		v += "." + m[3]
	}
	return v
}

// compareReq 判"实际版本是否够用"。只判低不判高。
func compareReq(r EnvReq, actual, why string) EnvReq {
	av, aok := verTriple(actual)
	rv, rok := verTriple(r.Required)
	if !aok || !rok {
		r.Status = reqUndeclared
		r.Why = why + "，但无法解析版本号（实际读到 " + actual + "）"
		return r
	}
	if verLess(av, rv) {
		r.Status = reqTooLow
		r.Why = fmt.Sprintf("%s，实际 %s —— **版本不够，编译或运行会失败**", why, verStr(av))
		r.Action = fmt.Sprintf("在「环境安装」把 %s 升到 %s 或更高（当前 %s）",
			r.Component, r.Required, verStr(av))
		return r
	}
	// 实际 >= 要求：完全正常，包括"比要求新"（向后兼容）
	r.Status = reqOK
	r.Why = fmt.Sprintf("%s，实际 %s（够用）", why, verStr(av))
	return r
}

// verTriple 解析 "1.24.0" / "v20.11.1" / "20" / ">=18" 成三元组。
//
// 刻意接受**单段**版本号：项目声明 ">=18" 是极常见的写法
// （`"engines":{"node":">=18"}`），而早期版本只认两段以上——
// 结果所有这类项目的符合性检查都退化成 undeclared，等于功能失效
// 却不报任何错。这个 bug 是 TestNodeReqOK 抓出来的。
func verTriple(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return [3]int{}, false
	}
	// 剥掉语义前缀。刻意逐个剥离而不是 strings.TrimLeft：
	// TrimLeft 用 cutset 会把 "go1.23.6" 的 'g'/'o' 也当噪声剥掉。
	if len(s) > 2 && s[:2] == "go" {
		s = s[2:]
	}
	s = strings.TrimLeft(s, ">=^~ vV")
	s = strings.TrimSpace(s)
	if s == "" {
		return [3]int{}, false
	}
	// 去掉可能的范围后缀（18.x → 18；>=18 <20 → 18）
	if i := strings.IndexAny(s, " \t<"); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, ".x"), ".*")
	parts := strings.Split(s, ".")
	var out [3]int
	for i, p := range parts {
		if p == "" {
			continue // "1..2" 这种畸形写法当 0 段处理
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// verLess 判断 a < b（逐段比较，缺的段当 0）。
func verLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func verStr(v [3]int) string {
	if v[2] == 0 && v[1] != 0 {
		return fmt.Sprintf("%d.%d", v[0], v[1])
	}
	if v[1] == 0 && v[2] == 0 {
		return fmt.Sprintf("%d", v[0])
	}
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

func init() {
	aiToolRegistry["check_env_req"] = aiTool{
		Desc: "检查**当前环境是否满足项目的版本要求**（读 go.mod 的 go 指令与 package.json 的 engines.node）。" +
			"装了组件不等于够用——项目可能要求更高版本，这时编译会直接失败。" +
			"用户报「编译报错说版本不对」「装完还是跑不了」时用它。" +
			"三种结果要分清：ok=够用、too_low=**版本不够，需要处理**、undeclared=项目没声明要求" +
			"（不是问题，但也没有保障）",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Execute: func(map[string]any) (string, error) {
			reqs := checkAllEnvReq()
			var sb strings.Builder
			sb.WriteString(envReqSummary(reqs) + "\n")
			for _, r := range reqs {
				line := r.Component + "：要求 " + orNone(r.Required) + " / 实际 " + orNone(r.Actual) + " → " + r.Status
				if r.Action != "" {
					line += "；下一步：" + r.Action
				}
				sb.WriteString(line + "\n")
			}
			return strings.TrimRight(sb.String(), "\n"), nil
		},
	}
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（未声明）"
	}
	return s
}

// checkAllEnvReq 查全部组件。返回里只有"不够用"和"没装"的才需要用户动手。
func checkAllEnvReq() []EnvReq {
	goVer, nodeVer := installedVersions()
	return []EnvReq{
		checkGoReq(cfg.Projects.BackendDir, goVer),
		checkNodeReq(cfg.Projects.FrontendDir, nodeVer),
	}
}

// installedVersions 取实际装了的版本。
//
// 优先读已有检测结果；没跑过检测就**只做只读探测**（不安装任何东西）——
// 环境符合性检查是回答"够不够用"，不是"顺手把环境装好"。
func installedVersions() (goVer, nodeVer string) {
	resultsMu.Lock()
	has := len(results) > 0
	if has {
		for _, v := range results {
			switch strings.ToLower(v.Name) {
			case "go", "golang":
				goVer = v.Version
			case "node", "node.js", "nodejs":
				nodeVer = strings.TrimPrefix(v.Version, "v")
			}
		}
	}
	resultsMu.Unlock()
	if has {
		return goVer, nodeVer
	}
	// 现场探测：先走组件定义（能复用项目配置的安装路径），
	// 失败再直接问 PATH —— 因为 cfg.Components 在没装过 EnvKit 的机器上
	// 可能就是默认模板甚至空，此时不该直接报"未安装"。
	if len(cfg.Components) > 0 {
		for _, c := range cfg.Components {
			if c.CheckCmd == "" {
				continue
			}
			r := detectComponent(c)
			if !r.Installed {
				continue
			}
			switch strings.ToLower(c.Name) {
			case "go", "golang":
				if goVer == "" {
					goVer = r.Version
				}
			case "node", "node.js", "nodejs":
				if nodeVer == "" {
					nodeVer = strings.TrimPrefix(r.Version, "v")
				}
			}
		}
	}
	if goVer == "" {
		goVer = probeVersionOf("go")
	}
	if nodeVer == "" {
		nodeVer = probeVersionOf("node")
	}
	return goVer, nodeVer
}

// probeVersionOf 直接问 PATH 里的可执行文件版本号。只读，不改任何东西。
//
// 为什么必须有这条兜底：EnvKit 首次运行时 cfg.Components 可能还没填
// （用户没走过安装向导），此时若直接返回"未安装"，用户会看到
// "go 版本不满足要求：未安装" —— 而他明明装了 Go。
func probeVersionOf(exe string) string {
	p := findExe(exe+".exe", exe)
	if p == "" {
		return ""
	}
	out := runVersionCmd(p, "--version")
	if out == "" {
		// node 的 --version 极老版本不支持，退回 -v
		out = runVersionCmd(p, "-v")
	}
	if out == "" {
		return ""
	}
	// 只取第一行：`go version go1.23.6 windows/amd64` / `v20.11.1`
	line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if v, ok := verTriple(line); ok {
		return verStr(v)
	}
	return ""
}

// runVersionCmd 跑一次版本查询。查不到返回空串（不是错误——版本探测失败不该报故障）。
func runVersionCmd(exe, arg string) string {
	ctx, cancel := cmdContext(20 * time.Second)
	defer cancel()
	b, err := buildCmd(ctx, false, exe, arg).CombinedOutput()
	if err != nil && len(b) == 0 {
		return ""
	}
	return string(b)
}

// envReqSummary 给 AI / 界面用的一句话结论。
//
// 只在"确实不够用"时才说话——满屏"符合"等于没说，
// 而真正需要注意的那一条会被淹没。
func envReqSummary(reqs []EnvReq) string {
	var bad []string
	var undeclared int
	for _, r := range reqs {
		switch r.Status {
		case reqTooLow, reqNotInstall:
			bad = append(bad, r.Component+": "+r.Why)
		case reqUndeclared:
			undeclared++
		}
	}
	if len(bad) == 0 {
		if undeclared > 0 {
			return fmt.Sprintf("环境符合性：已声明要求的组件都够用；另有 %d 个项目未声明版本要求（不构成问题，但也没有保障）", undeclared)
		}
		return "环境符合性：所有组件都满足项目声明的版本要求"
	}
	return "环境符合性有问题 —— " + strings.Join(bad, "；")
}

// envReqBadBrief 只返回"确实不够用"的项，供快照注入。
//
// 刻意不返回"符合"与"项目未声明"：前者是废话，后者是"没检查到"而非问题。
// 把它们混进上下文会稀释真正需要注意的那一条——这是提示词工程的通用陷阱。
func envReqBadBrief() string {
	var bad []string
	for _, r := range checkAllEnvReq() {
		switch r.Status {
		case reqTooLow:
			bad = append(bad, fmt.Sprintf("%s：要求 %s，实际 %s（不够，编译或运行会失败）",
				r.Component, r.Required, r.Actual))
		case reqNotInstall:
			bad = append(bad, fmt.Sprintf("%s：未安装，而项目需要它", r.Component))
		}
	}
	if len(bad) == 0 {
		return ""
	}
	return "**版本不够，动手之前先解决** —— " + strings.Join(bad, "；")
}
