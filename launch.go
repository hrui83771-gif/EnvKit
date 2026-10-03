package main

// launch.go —— v2.1 L1/L2：启动方式推断
//
// 解决的问题：AI 启前端写死 `npm run serve`、后端写死 `go run main.go`，
// 换个项目就全废。而推断所需的数据（package.json 的 scripts、Go 入口）
// projectBrief 早就算出来了——问题一直是"算出来了但没接上"。
//
// 风险边界（动手前想清楚，改了会连带失效）：
//
//  1. **危险的不是 npm，是"允许调用方自由填字符串"。**
//     `npm run <name>` 本身不会执行任意命令——npm 只在 package.json 的
//     scripts 里找同名条目，找不到就报错退出。所以真正的攻击面是
//     package.json 里那些 `deploy` / `migrate` / `reset` 脚本：
//     一旦 AI 能自由填，它可能去执行"发布脚本"而不是"启动服务"。
//     因此候选集必须是**白名单 ∩ 非黑名单**，而不是黑名单了事。
//
//  2. **推断错是允许的，谎报不允许。**
//     猜错 → 复验立刻发现 → AI 道歉并列出候选；
//     跳过复验直接报成功 → 这才是 v2.0 要杜绝的灾难。
//     所以本文件只负责"给出候选与理由"，不承担也不允许代替验证层。
//
//  3. **用户配置永远优先于推断。** 推断是兜底，不是判据。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LaunchPlan 一次启动方式推断的结果。
type LaunchPlan struct {
	WebScript   string   `json:"web_script,omitempty"`   // 建议的 npm 脚本（已剔除危险项）
	WebAll      []string `json:"web_all,omitempty"`      // 全部候选（供 AI 与界面展示）
	WebSource   string   `json:"web_source,omitempty"`   // config | package.json | none
	BackendFile string   `json:"backend_file,omitempty"` // 相对后端目录的入口，如 main.go
	BackendWhy  string   `json:"backend_why,omitempty"`  // 入口判定依据（给人和 AI 看）
	Blocked     []string `json:"blocked,omitempty"`      // 被安全规则剔除的脚本名
	Note        string   `json:"note,omitempty"`         // 需要人介入的说明
}

// ---------- 脚本名规则 ----------

// launchSafeScripts 白名单：只承认这些"一看就是启动服务"的脚本名。
// 刻意保守——宁可报"识别不了"让用户自己填，也不要赌一个没见过的名字是安全的。
// 匹配方式为**精确相等**（可含冒号子命令，如 dev:serve），不做前缀/包含匹配。
var launchSafeScripts = map[string]bool{
	"serve": true, "dev": true, "start": true, "watch": true,
	"dev:serve": true, "dev:start": true, "serve:dev": true,
	"start:dev": true, "develop": true, "dev:client": true, "start:app": true,
}

// launchDangerScripts 黑名单：命中即剔除，即使用户把它加进白名单也不放行。
// 语义是"这个脚本名本身就意味着副作用或破坏性操作"，不是"我不喜欢它"。
var launchDangerScripts = []string{
	"deploy", "release", "publish", "reset", "migrate", "migration",
	"drop", "truncate", "seed", "clear", "clean", "install", "ci",
	"build", "test", "lint", "e2e", "coverage", "prod", "production",
	"docker", "compose", "push", "pull", "backup", "restore", "kill", "dev:deploy",
}

// launchEntryInDir 校验用户手填的后端入口确实存在于后端目录内。
// 除了"文件存在"，还要挡越界：入口是相对路径，`..\..\windows\system32\x.exe`
// 这种必须拦住——否则配置就成了任意路径执行入口。
func launchEntryInDir(backendDir, entry string) bool {
	dir := strings.TrimSpace(backendDir)
	e := strings.TrimSpace(entry)
	if dir == "" || e == "" {
		return false
	}
	// 归一化后必须仍落在后端目录内
	base, err1 := filepath.Abs(dir)
	full, err2 := filepath.Abs(filepath.Join(dir, filepath.FromSlash(e)))
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	fi, err := os.Stat(full)
	return err == nil && !fi.IsDir()
}

// launchScriptInCandidates 判断脚本是否在当前项目的合法候选集内。
// 供 AI 工具做二次校验：模型可能忽略提示词自己编一个名字，
// 这时必须在派进程之前拦下来，并把候选集告诉它。
func launchScriptInCandidates(name string) bool {
	p := currentLaunchPlan()
	for _, s := range p.WebAll {
		if strings.EqualFold(s, strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// launchScriptAllowed 判定一个脚本名能否作为"启动服务"的候选。
// 白名单为准、黑名单兜底：两者同时命中时**拒绝**（黑名单优先级更高）。
func launchScriptAllowed(name string) bool {
	n := strings.TrimSpace(name)
	if n == "" {
		return false
	}
	for _, d := range launchDangerScripts {
		if strings.EqualFold(n, d) {
			return false
		}
	}
	return launchSafeScripts[strings.ToLower(n)]
}

// ---------- 推断 ----------

// inferWebScript 推断前端启动脚本。
// 优先用用户配置；否则从 package.json 的 scripts 里筛安全候选。
// 返回 (推荐脚本, 全部候选, 来源, 被剔除的)。
func inferWebScript(frontendDir string, configured string) (string, []string, string, []string) {
	if s := strings.TrimSpace(configured); s != "" {
		// 用户配置也要过安全规则：配置是"用户填的"，但用户也可能手滑填了 migrate
		if !launchScriptAllowed(s) {
			return "", nil, "config", nil
		}
		return s, []string{s}, "config", nil
	}
	scripts := readPackageScripts(frontendDir)
	if len(scripts) == 0 {
		return "", nil, "none", nil
	}
	var ok, blocked []string
	for _, s := range scripts {
		if launchScriptAllowed(s) {
			ok = append(ok, s)
		} else if launchScriptExplicitlyRisky(s) {
			blocked = append(blocked, s)
		}
	}
	sort.Strings(ok)
	sort.Strings(blocked)
	if len(ok) == 0 {
		return "", nil, "none", blocked
	}
	// 推荐项的排序：dev 优先于 serve（开发期最常用），其次 serve/start/watch。
	// 不做"只取第一个"——多个候选都合法时，让 AI 和用户都能看到。
	return launchPreferred(ok), ok, "package.json", blocked
}

// launchPreferred 在合法候选里挑一个推荐值。
// 顺序即优先级：dev > serve > start > watch > 其它（按字典序）。
var launchPrefOrder = []string{"dev", "serve", "start", "watch"}

func launchPreferred(cands []string) string {
	lower := map[string]string{}
	for _, c := range cands {
		lower[strings.ToLower(c)] = c
	}
	for _, p := range launchPrefOrder {
		if v, ok := lower[p]; ok {
			return v
		}
	}
	return cands[0]
}

// launchScriptExplicitlyRisky 判断"被剔除"是否值得告诉用户。
// 只有命中黑名单的才报；仅仅是"不认识的名字"不算，那属于正常项目。
func launchScriptExplicitlyRisky(name string) bool {
	for _, d := range launchDangerScripts {
		if strings.EqualFold(strings.TrimSpace(name), d) {
			return true
		}
	}
	return false
}

// readPackageScripts 读前端目录下的 package.json scripts，**只取脚本名**。
// 读失败或格式不对一律返回空——推断失败必须走"拒绝并说明"，不能猜。
//
// 注意：parsePackageJSON 返回的是给人和 AI 看的可读形态 "名字: 命令"，
// 这里必须剥掉命令部分。分隔符用 ": "（冒号+空格）而不是 ":"，
// 因为脚本名本身可以含冒号（dev:serve / start:dev 这类很常见）。
func readPackageScripts(dir string) []string {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	_, raw, _ := parsePackageJSON(data)
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		name := s
		if i := strings.Index(s, ": "); i > 0 {
			name = s[:i]
		}
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// inferBackendFile 推断后端入口（相对后端目录）。
//
// 现状是写死 `go run main.go`，对 FarmTrace（server/main.go）恰好有效，
// 但对 cmd/server/main.go 布局就失败。这里补上：
//   - 后端目录下有 main.go → main.go（绝大多数项目）
//   - 没有但有 cmd/<x>/main.go → cmd/<x>/main.go（并在理由里说明）
//   - 都没有 → 空，交由调用方明确报错，而不是盲跑一条必然失败的命令
func inferBackendFile(backendDir string) (string, string) {
	// 用户配置优先于一切推断（档 2 的兜底：推断错时用户可以教它）
	if f := strings.TrimSpace(cfg.Projects.BackendFile); f != "" {
		if launchEntryInDir(backendDir, f) {
			return f, "由你在「程序配置」中指定"
		}
		return "", "配置的后端入口在后端目录下不存在：" + f
	}
	if strings.TrimSpace(backendDir) == "" {
		return "", "未配置后端目录"
	}
	if hasFile(filepath.Join(backendDir, "main.go")) {
		return "main.go", "后端目录下的 main.go"
	}
	if hasFile(filepath.Join(backendDir, "go.mod")) {
		// 有 go.mod 却没有 main.go：典型的 cmd/ 布局或多个子模块
		if f := findGoEntry(backendDir, 0); f != "" {
			return f, "按 cmd/*/main.go 布局推断（后端目录根下没有 main.go）"
		}
		return "", "后端目录有 go.mod 但找不到 main.go（可能在子模块内，请手动指定）"
	}
	if f := findGoEntry(backendDir, 0); f != "" {
		return f, "扫描到的 main.go"
	}
	return "", "后端目录下未找到 main.go"
}

// findGoEntry 在有限深度内找 main.go（cmd/x/main.go、server/main.go 这类）。
// 深度限制为 3 层——再深就是"在代码里搜索"，该用探索工具而不是启动任务。
func findGoEntry(dir string, depth int) string {
	if depth > 3 {
		return ""
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	// 先看本层（cmd/x/main.go 里的 x/main.go 要两层）
	var dirs []string
	for _, e := range ents {
		if e.IsDir() {
			name := e.Name()
			// 跳过噪音目录，否则在带 node_modules 的仓库里会走很久
			if name == "node_modules" || name == ".git" || name == "vendor" ||
				name == "dist" || name == "build" || strings.HasPrefix(name, ".") {
				continue
			}
			dirs = append(dirs, name)
		}
	}
	if f := findGoEntry(filepath.Join(dir, "cmd"), depth); f != "" {
		return "cmd/" + f
	}
	for _, d := range dirs {
		sub := filepath.Join(dir, d)
		if hasFile(filepath.Join(sub, "main.go")) {
			return filepath.ToSlash(d) + "/main.go"
		}
	}
	return ""
}

// ---------- 对外入口 ----------

// currentLaunchPlan 产出当前生效的启动方式推断（AI 快照与界面共用）。
// 每次调用都重算画像目录的 package.json —— 配置可能刚被改，缓存 2 分钟会给出过期结论。
func currentLaunchPlan() LaunchPlan {
	p := LaunchPlan{}
	p.WebScript, p.WebAll, p.WebSource, p.Blocked = inferWebScript(
		cfg.Projects.FrontendDir, cfg.Projects.WebScript)
	p.BackendFile, p.BackendWhy = inferBackendFile(cfg.Projects.BackendDir)
	switch {
	case p.WebSource == "config":
		p.Note = "前端启动脚本由你在「程序配置」中指定"
	case p.WebSource == "package.json":
		p.Note = "前端启动脚本由 package.json 推断；启动后请以复验结果为准"
	case p.WebSource == "none":
		if len(p.Blocked) > 0 {
			p.Note = "package.json 里的脚本全部带副作用或破坏性语义（" +
				strings.Join(p.Blocked, "、") + "），已拒绝自动启动；请在「程序配置」手动指定启动脚本"
		} else {
			p.Note = "未能从 package.json 识别出可用的启动脚本（如 dev / serve / start）；请在「程序配置」手动指定"
		}
	}
	if p.BackendFile == "" {
		p.Note = strings.TrimSpace(p.Note + "；后端入口也未识别：" + p.BackendWhy)
	}
	return p
}

// launchWebScriptOrDefault 给启动任务用：返回应当执行的脚本名与来源说明。
// 识别不出时返回空串 + 明确的失败理由——绝不静默回退到 "serve"，
// 那正是 v1 时代"换个项目就启动不了"的根源。
func launchWebScriptOrDefault(dir, requested string) (script, source string, ok bool) {
	if s := strings.TrimSpace(requested); s != "" {
		// 调用方显式指定：仍要过安全规则，防止 AI 或接口传入危险脚本名
		if !launchScriptAllowed(s) {
			return "", "", false
		}
		return s, "调用方指定", true
	}
	// 调用方没给：先看用户配置，再看 package.json
	if s, _, src, _ := inferWebScript(dir, cfg.Projects.WebScript); s != "" {
		return s, src, true
	}
	// 最后兜底：历史行为。仅当 package.json 根本读不到（非"有但危险"）时才用 serve，
	// 避免打断"用户还没装依赖、但确实想跑 serve"的既有流程。
	scripts := readPackageScripts(dir)
	if len(scripts) == 0 {
		return "serve", "默认（未读到 package.json）", true
	}
	return "", "", false
}

// launchBackendFileOrDefault 同理，失败时给出可执行的下一步。
func launchBackendFileOrDefault(dir string) (file, why string, ok bool) {
	if f, w := inferBackendFile(dir); f != "" {
		return f, w, true
	}
	return "", "", false
}

// launchPlanBrief 注入提示词用的紧凑形态。
func launchPlanBrief(p LaunchPlan) string {
	if p.WebScript == "" && p.BackendFile == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("前端：")
	if p.WebScript != "" {
		sb.WriteString(fmt.Sprintf("npm run %s（来源：%s）", p.WebScript, launchSourceCN(p.WebSource)))
		if len(p.WebAll) > 1 {
			sb.WriteString("，其他合法候选：" + strings.Join(withoutFirst(p.WebAll, p.WebScript), "、"))
		}
	} else {
		sb.WriteString("未识别（不可自动启动）")
	}
	sb.WriteString("；后端：")
	if p.BackendFile != "" {
		sb.WriteString("go run " + p.BackendFile)
	} else {
		sb.WriteString("未识别（不可自动启动）")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func launchSourceCN(s string) string {
	switch s {
	case "config":
		return "用户配置"
	case "package.json":
		return "package.json 推断"
	case "none":
		return "未识别"
	default:
		return s
	}
}

func withoutFirst(v []string, first string) []string {
	var out []string
	for _, s := range v {
		if s != first {
			out = append(out, s)
		}
	}
	return out
}
