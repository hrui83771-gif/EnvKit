package main

// explore.go —— AI 探索层：让助手在"不知道项目是什么"的时候，能自己去查。
//
// 设计要点（v2.0 P1）：
//   1. 只读：三个工具都不改任何东西，因此不进确认闸门；但每次调用都落审计（actor=ai）。
//   2. 有边界：只能看已配置的项目目录及其共同父目录，越界一律拒绝。
//   3. 有配额：深度/条目/字节/命中数都有上限，避免在几万文件的仓库里拖死会话
//      （本机 web/ 实测 25842 个文件，绝大多数在 node_modules）。
//   4. 不可信：项目文件内容（尤其第三方依赖源码）可能藏诱导指令，一律包 <untrusted_data>，
//      并做轻量密钥脱敏，防止把真实密码喂给模型。

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// ---------- 配额 ----------
const (
	exploreMaxDepth      = 3          // 目录树最大深度
	exploreMaxEntries    = 200        // 单次列目录最大条目
	exploreMaxReadBytes  = 64 * 1024  // 单文件最大读取字节
	exploreMaxLines      = 400        // 单文件最大返回行数
	exploreMaxHits       = 50         // 搜索最大命中数
	exploreMaxScan       = 5000       // 单次搜索最大扫描文件数
	exploreMaxFileScan   = 512 * 1024 // 内容搜索时跳过大文件
	exploreMaxBriefFiles = 2000       // 生成项目画像时最大扫描文件数
)

// exploreSkipDirs 永远不进的目录：依赖产物、构建产物、版本库元数据。
var exploreSkipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true, "out": true,
	"backups": true, "target": true, "vendor": true, "bin": true, "obj": true,
	".idea": true, ".vscode": true, ".next": true, ".nuxt": true, ".cache": true,
	"coverage": true, "__pycache__": true, ".gradle": true, "logs": true,
}

// exploreSecretNames 含凭据的文件：即使落在项目目录内也拒绝读取。
var exploreSecretNames = map[string]bool{
	"config.json": true, "id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	".npmrc": true, ".pypirc": true, ".htpasswd": true, "credentials": true, ".netrc": true,
}

var exploreSecretSuffix = []string{".pem", ".pfx", ".key", ".p12", ".jks", ".keystore", ".asc"}

// secretAssignRe 识别代码里的密码/密钥赋值，用于输出前脱敏。
// 覆盖三种常见写法：JSON/YAML 的 "password": "x"、Go 的 password := "x"、普通赋值 pwd = "x"，
// 因此分隔符用 [:=]{1,2} 才能同时吃掉 ":"、"=" 和 ":="。
var secretAssignRe = regexp.MustCompile(`(?i)((?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)\s*[:=]{1,2}\s*)(["'])([^"']{3,})(["'])`)

// secretDSNRe 数据源串里的凭据（如 root:123456@tcp(127.0.0.1:3306)），值部分整段打码。
var secretDSNRe = regexp.MustCompile(`(?i)([a-z0-9_]+):([^:@\s"']{2,})@(tcp|unix|http|https)\(`)

// ---------- 路径沙箱 ----------

// projectRoots 探索根目录白名单：已配置的前后端目录本身；
// 两者同父时再把父目录加进来，让助手能看到项目全貌（例如同时含 server/ 与 web/ 的仓库）。
func projectRoots() []string {
	cfgMu.Lock()
	fe, be := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	cfgMu.Unlock()

	var roots []string
	add := func(p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		abs, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return
		}
		for _, e := range roots {
			if pathEqualCI(e, abs) {
				return
			}
		}
		roots = append(roots, abs)
	}
	add(fe)
	add(be)
	if fe != "" && be != "" {
		pf, pb := filepath.Dir(fe), filepath.Dir(be)
		if pf != "" && pathEqualCI(pf, pb) {
			add(pf)
		}
	}
	sort.Strings(roots)
	return roots
}

func pathEqualCI(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// normPath 统一比较形态：Windows 下路径不区分大小写。
func normPath(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// withinRoot 判定 p 是否在 root 之内（root 自身也算在内）。
func withinRoot(root, p string) bool {
	r, q := normPath(root), normPath(p)
	if r == q {
		return true
	}
	return strings.HasPrefix(q, r+string(filepath.Separator))
}

// aiSafePath 把模型给的路径解析成绝对路径，并确认它落在白名单根内。
// 解析失败、不存在于白名单内的路径一律返回 ok=false。
func aiSafePath(user string) (abs string, ok bool) {
	roots := projectRoots()
	if len(roots) == 0 {
		return "", false
	}
	cand := filepath.Clean(user)
	if !filepath.IsAbs(cand) {
		cand = filepath.Join(roots[0], cand)
	}
	abs, err := filepath.Abs(cand)
	if err != nil {
		return "", false
	}
	// 符号链接可能是"跳出根目录"的通道，能解析就必须解析后再判定
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = resolved
	}
	for _, r := range roots {
		if withinRoot(r, abs) {
			return abs, true
		}
	}
	return "", false
}

func exploreSkipDir(name string) bool {
	return exploreSkipDirs[strings.ToLower(name)]
}

// exploreSecretPath 是否属于"看不得"的文件：密钥、私钥、含本机实配的 config.json、dotenv。
func exploreSecretPath(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	if exploreSecretNames[base] {
		return true
	}
	if strings.HasPrefix(base, ".env") || strings.HasPrefix(base, "id_") {
		return true
	}
	for _, suf := range exploreSecretSuffix {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

// redactSecrets 输出前的轻量脱敏：把疑似密码/密钥的赋值值与数据源串里的口令换成 ***。
// 保守替换，只动紧跟在典型敏感键名后面的引号串，以及明确的 DSN 凭据段，避免误伤正常代码。
func redactSecrets(s string) string {
	s = secretAssignRe.ReplaceAllString(s, "$1$2***$4")
	return secretDSNRe.ReplaceAllString(s, "$1:***@$3(")
}

// untrusted 给工具结果套不可信定界符，配合系统提示词第 1 条。
func untrusted(body string) string {
	return "<untrusted_data>\n" + body + "\n</untrusted_data>"
}

// exploreNoRootMsg 未配置项目目录时的统一提示，模型据此知道该怎么补配置。
const exploreNoRootMsg = "尚未配置项目目录（程序配置里没有前端目录或后端目录），无法查看项目文件。" +
	"请先让用户在「程序配置」中选择项目目录，或直接告诉用户需要先配置。"

// aiExploreDenied 记录一次越界/敏感文件访问尝试（安全审计用）。
func aiExploreDenied(action, detail string) {
	auditNow(actAI, "explore_denied", action, "", resFail, firstLines(detail, 200))
}

// aiArgInt 读取整型参数，兼容不同来源的数值类型。
// （模型给的 JSON 数字会被解码成 float64，但意图兜底路径与单测可能直接给 int/字符串，
//
//	一律只认 float64 会静默忽略参数，是曾经真实踩到的坑。）
func aiArgInt(args map[string]any, key string, def, min, max int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	var n int
	switch x := v.(type) {
	case float64:
		n = int(x)
	case float32:
		n = int(x)
	case int:
		n = x
	case int64:
		n = int(x)
	case string:
		p, perr := strconv.Atoi(strings.TrimSpace(x))
		if perr != nil {
			return def
		}
		n = p
	default:
		return def
	}
	if min > 0 && n < min {
		n = min
	}
	if max > 0 && n > max {
		n = max
	}
	return n
}

// ---------- 工具注册 ----------
// 用 init 注册而非塞进 ai_tools.go 的大字面量：探索层保持自成一体的一个文件，
// 且与既有工具同走一套注册表/闸门/审计逻辑。

func init() {
	aiToolRegistry["list_project"] = aiTool{
		Desc: "列出项目目录结构（只读）。path 可用相对项目的子路径；depth 为层级深度（1-3）。用于判断项目类型、找入口文件、找配置文件的位置",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": "相对项目根目录的子路径，留空表示根目录"},
			"depth": map[string]any{"type": "integer", "description": "展开层级，默认 2，最大 3"},
		}, "required": []string{}},
		Execute: func(args map[string]any) (string, error) { return exploreList(args), nil },
	}

	aiToolRegistry["search_files"] = aiTool{
		Desc: "在项目内搜索文件（只读）。mode=name 按文件名 glob 匹配（如 *.vue、docker-compose.*）；mode=content 在文件内容里正则搜索，返回 文件:行号:内容。用于在报错信息里找来源文件、找配置项",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "glob（name 模式）或正则表达式（content 模式）"},
			"mode":    map[string]any{"type": "string", "description": "name 或 content，默认 name", "enum": []string{"name", "content"}},
			"limit":   map[string]any{"type": "integer", "description": "最大命中数，默认 30，上限 50"},
		}, "required": []string{"pattern"}},
		Execute: func(args map[string]any) (string, error) { return exploreSearch(args), nil },
	}

	aiToolRegistry["read_file"] = aiTool{
		Desc: "读取项目内指定文件的部分内容（只读）。建议先用 list_project / search_files 定位，再带 offset 精读。key/凭据类文件按安全策略拒绝读取",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "文件路径（相对项目根或绝对路径，必须在项目目录内）"},
			"offset": map[string]any{"type": "integer", "description": "起始行号（从 1 开始），默认 1"},
			"lines":  map[string]any{"type": "integer", "description": "读取行数，默认 200，上限 400"},
		}, "required": []string{"path"}},
		Execute: func(args map[string]any) (string, error) { return exploreRead(args), nil },
	}
}

// ---------- list_project ----------

func exploreList(args map[string]any) string {
	roots := projectRoots()
	if len(roots) == 0 {
		return exploreNoRootMsg
	}
	sub, _ := args["path"].(string)
	depth := aiArgInt(args, "depth", 2, 1, exploreMaxDepth)

	base, ok := aiSafePath(sub)
	if !ok {
		aiExploreDenied("list_project", sub)
		return "拒绝访问：路径不在已配置的项目目录内（" + strings.Join(roots, " | ") + "）。如需查看请先让用户把该目录配置为前端/后端目录。"
	}
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		return "目录不存在或不可读：" + base
	}

	var lines []string
	count := 0
	trunc := false
	exploreWalkTree(base, "", 1, depth, &lines, &count, &trunc)

	head := "项目根目录：" + strings.Join(roots, " | ") + "\n查看位置：" + base + "\n\n"
	body := strings.Join(lines, "\n")
	if body == "" {
		body = "（该目录为空或全部内容被安全/忽略策略过滤）"
	}
	auditNow(actAI, "explore_list", base, fmt.Sprintf("depth=%d", depth), resOK, fmt.Sprintf("%d 条", count))
	return untrusted(head + body)
}

func exploreWalkTree(dir, prefix string, depth, maxDepth int, out *[]string, count *int, trunc *bool) {
	if *trunc {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		*out = append(*out, prefix+"(读取失败："+firstLines(err.Error(), 120)+")")
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if *trunc {
			return
		}
		name := e.Name()
		if e.IsDir() {
			if exploreSkipDir(name) {
				continue
			}
			*out = append(*out, prefix+name+"/")
		} else {
			*out = append(*out, prefix+name)
		}
		*count++
		if *count >= exploreMaxEntries {
			*trunc = true
			*out = append(*out, fmt.Sprintf("(已达 %d 条展示上限，结果被截断：请指定更深的 path 或用 search_files 精确定位)", exploreMaxEntries))
			return
		}
		if e.IsDir() && depth < maxDepth {
			exploreWalkTree(filepath.Join(dir, name), prefix+"  ", depth+1, maxDepth, out, count, trunc)
		}
	}
}

// ---------- search_files ----------

func exploreSearch(args map[string]any) string {
	roots := projectRoots()
	if len(roots) == 0 {
		return exploreNoRootMsg
	}
	pattern, _ := args["pattern"].(string)
	if strings.TrimSpace(pattern) == "" {
		return "缺少 pattern 参数。"
	}
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "name"
	}
	limit := aiArgInt(args, "limit", 30, 1, exploreMaxHits)

	var hits []string
	scanned := 0
	capped := false
	pat := strings.ToLower(pattern)

	var scan func(dir string, depth int)
	scan = func(dir string, depth int) {
		if len(hits) >= limit || scanned >= exploreMaxScan || depth > exploreMaxDepth {
			capped = capped || scanned >= exploreMaxScan
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if len(hits) >= limit || scanned >= exploreMaxScan {
				capped = true
				return
			}
			name := e.Name()
			full := filepath.Join(dir, name)
			if e.IsDir() {
				if exploreSkipDir(name) {
					continue
				}
				scan(full, depth+1)
				continue
			}
			if exploreSecretPath(full) {
				continue
			}
			scanned++
			switch mode {
			case "content":
				if fi, err2 := e.Info(); err2 != nil || fi.Size() > exploreMaxFileScan {
					continue
				}
				hits = append(hits, grepFile(full, pattern, limit-len(hits))...)
			default:
				if ok, _ := filepath.Match(pat, strings.ToLower(name)); ok {
					hits = append(hits, relOrAbs(full))
				}
			}
		}
	}
	for _, r := range roots {
		scan(r, 1)
	}

	if len(hits) == 0 {
		auditNow(actAI, "explore_search", pattern, mode, resOK, "0 命中")
		return untrusted("未找到匹配项（pattern=" + pattern + "，mode=" + mode + "）。可换个关键词，或先用 list_project 看看目录结构。")
	}
	tail := ""
	if capped || scanned >= exploreMaxScan {
		tail = fmt.Sprintf("\n(已达扫描上限：文件 %d 个 / 命中 %d 条，结果可能不完整——建议缩小范围或改用 content 模式配合精确正则)", scanned, len(hits))
	}
	auditNow(actAI, "explore_search", pattern, mode, resOK, fmt.Sprintf("%d 命中/扫描 %d 文件", len(hits), scanned))
	return untrusted(fmt.Sprintf("命中 %d 条（扫描 %d 个文件）：\n%s%s", len(hits), scanned, redactSecrets(strings.Join(hits, "\n")), tail))
}

// grepFile 在单个文件里按正则搜索，返回 path:行号:内容（已脱敏）。
func grepFile(path, pattern string, max int) []string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return []string{"(正则编译失败：" + err.Error() + ")"}
	}
	return grepFileRe(path, re, max)
}

func grepFileRe(path string, re *regexp.Regexp, max int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, exploreMaxFileScan))
	if err != nil {
		return nil
	}
	if isBinaryLike(data) {
		return nil
	}
	var out []string
	lines := strings.Split(string(data), "\n")
	for i, ln := range lines {
		if len(out) >= max {
			break
		}
		if re.MatchString(ln) {
			out = append(out, fmt.Sprintf("%s:%d: %s", relOrAbs(path), i+1, strings.TrimRight(ln, "\r")))
		}
	}
	return out
}

// isBinaryLike 粗判二进制：含 NUL 字节即跳过（避免把 exe/图片塞进上下文）。
func isBinaryLike(b []byte) bool {
	n := len(b)
	if n > 4096 {
		n = 4096
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

// relOrAbs 相对项目根展示，缩短上下文占用。
func relOrAbs(p string) string {
	roots := projectRoots()
	for _, r := range roots {
		if withinRoot(r, p) {
			if rel, err := filepath.Rel(r, p); err == nil && !strings.HasPrefix(rel, "..") {
				return filepath.ToSlash(rel)
			}
		}
	}
	return p
}

// ---------- read_file ----------

func exploreRead(args map[string]any) string {
	roots := projectRoots()
	if len(roots) == 0 {
		return exploreNoRootMsg
	}
	p, _ := args["path"].(string)
	if strings.TrimSpace(p) == "" {
		return "缺少 path 参数。请先用 list_project / search_files 定位文件。"
	}
	abs, ok := aiSafePath(p)
	if !ok {
		aiExploreDenied("read_file", p)
		return "拒绝读取：路径不在已配置的项目目录内（" + strings.Join(roots, " | ") + "）。"
	}
	if exploreSecretPath(abs) {
		aiExploreDenied("read_file", abs)
		return "拒绝读取：该文件属于凭据/密钥类文件（" + filepath.Base(abs) + "），按安全策略不向模型暴露。"
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "文件不存在或不可读：" + abs
	}
	if fi.IsDir() {
		return "这是一个目录，不是文件。请用 list_project 查看目录内容：" + abs
	}
	if isBinaryLikeHeader(abs) {
		return "该文件疑似二进制（非文本），已跳过读取：" + abs
	}

	offset := aiArgInt(args, "offset", 1, 1, 0)
	nLines := aiArgInt(args, "lines", 200, 1, exploreMaxLines)

	f, err := os.Open(abs)
	if err != nil {
		return "打开失败：" + err.Error()
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, exploreMaxReadBytes))
	if err != nil {
		return "读取失败：" + err.Error()
	}
	text := string(data)
	lines := strings.Split(text, "\n")
	note := fmt.Sprintf("文件：%s（共 %d 行", relOrAbs(abs), len(lines))
	if fi.Size() > exploreMaxReadBytes {
		note += fmt.Sprintf("，源文件 %.1f KB，本次仅载入前 %d KB", float64(fi.Size())/1024, exploreMaxReadBytes/1024)
	}
	start := offset - 1
	if start > len(lines) {
		start = len(lines)
	}
	end := start + nLines
	if end > len(lines) {
		end = len(lines)
	}
	body := strings.Join(lines[start:end], "\n")
	note += fmt.Sprintf("，当前显示第 %d-%d 行）\n\n", start+1, end)
	auditNow(actAI, "explore_read", relOrAbs(abs), fmt.Sprintf("offset=%d lines=%d", offset, nLines), resOK, "")
	return untrusted(note + redactSecrets(body))
}

// isBinaryLikeHeader 读前 512 字节判断是否为二进制。
func isBinaryLikeHeader(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return isBinaryLike(buf[:n])
}
