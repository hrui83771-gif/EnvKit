package main

// doctor.go —— v4.0 Phase 2：契约只读诊断
//
// Doctor 只回答"契约要求 vs 当前实际"，不启动、不停止、不修复、不执行
// 契约里写的任意命令。每个检查项都带 expected/actual/evidence，方便用户判断
// 是环境真的不对，还是契约本身没声明。
//
// 四态沿用 envreq 的口径：
//   ok          实际满足契约
//   drift       实际不满足，需要处理
//   undeclared  契约没声明，不能算通过
//   unknown     探测不到/本阶段不探测，不能算通过
//   skip        该检查不适用

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	doctorOK         = "ok"
	doctorDrift      = "drift"
	doctorUndeclared = "undeclared"
	doctorUnknown    = "unknown"
	doctorSkip       = "skip"

	doctorStatusOK         = "ok"
	doctorStatusPartial    = "partial"
	doctorStatusDrift      = "drift"
	doctorStatusBlocked    = "blocked"
	doctorStatusNoContract = "no_contract"
)

type DoctorCheck struct {
	ID       string   `json:"id"`
	Group    string   `json:"group"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Expected string   `json:"expected,omitempty"`
	Actual   string   `json:"actual,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	Repair   string   `json:"repair,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

type DoctorSummary struct {
	OK         int `json:"ok"`
	Drift      int `json:"drift"`
	Undeclared int `json:"undeclared"`
	Unknown    int `json:"unknown"`
	Skip       int `json:"skip"`
}

type DoctorReport struct {
	OK           bool          `json:"ok"`
	Status       string        `json:"status"`
	ContractPath string        `json:"contract_path,omitempty"`
	ProjectRoot  string        `json:"project_root,omitempty"`
	Checks       []DoctorCheck `json:"checks"`
	Summary      DoctorSummary `json:"summary"`
	Conclusion   string        `json:"conclusion"`
	Error        string        `json:"error,omitempty"`
	CheckedAt    string        `json:"checked_at"`
}

// runContractDoctor 是 Doctor 的唯一入口。path 为空时按当前配置推导。
func runContractDoctor(c Config, path string) DoctorReport {
	rep := DoctorReport{
		Checks:    []DoctorCheck{},
		CheckedAt: time.Now().Format(time.RFC3339),
	}
	if strings.TrimSpace(path) == "" {
		var err error
		_, path, err = contractPathOf(c)
		if err != nil {
			rep.Status = doctorStatusBlocked
			rep.Error = err.Error()
			rep.Conclusion = "无法确定契约位置：" + err.Error()
			return rep
		}
	}
	rep.ContractPath = path
	if _, err := os.Stat(path); err != nil {
		rep.Status = doctorStatusNoContract
		rep.Conclusion = "未找到环境契约；请在项目接入向导里生成 envkit.yaml，或把已有契约放到项目根"
		doctorAdd(&rep, "contract", "contract.exists", "环境契约存在", doctorUndeclared,
			path, "未找到", "契约是 Doctor 的比对基准；没有契约就无法判断环境是否达标")
		doctorSummarize(&rep)
		return rep
	}
	contract, problems, err := loadContractFile(path)
	if err != nil {
		rep.Status = doctorStatusBlocked
		rep.Error = err.Error()
		rep.Conclusion = "契约解析失败：" + err.Error()
		doctorAdd(&rep, "contract", "contract.parse", "契约可解析", doctorDrift,
			"合法 YAML", "解析失败", err.Error())
		doctorSummarize(&rep)
		return rep
	}
	if len(problems) > 0 {
		rep.Status = doctorStatusBlocked
		rep.Error = strings.Join(problems, "；")
		rep.Conclusion = "契约自身有硬错误，先修契约再诊断环境"
		for i, p := range problems {
			doctorAdd(&rep, "contract", fmt.Sprintf("contract.rule.%d", i+1), "契约规则校验", doctorDrift,
				"合法契约", p, p)
		}
		doctorSummarize(&rep)
		return rep
	}
	doctorAdd(&rep, "contract", "contract.parse", "契约可解析", doctorOK,
		"合法 YAML", "已解析", path)

	baseRoot := filepath.Dir(path)
	projectRoot := baseRoot
	if strings.TrimSpace(contract.Project.Root) != "" && contract.Project.Root != "." {
		abs, err := contractAbsPath(baseRoot, contract.Project.Root)
		if err != nil {
			doctorAdd(&rep, "project", "project.root", "项目根路径", doctorDrift,
				"项目内相对路径", contract.Project.Root, err.Error())
		} else {
			projectRoot = abs
		}
	}
	rep.ProjectRoot = projectRoot
	doctorCheckProject(&rep, projectRoot)
	doctorCheckTools(&rep, contract.Tools)
	doctorCheckServices(&rep, contract.Services, projectRoot)
	doctorCheckDatabase(&rep, contract.Database, projectRoot)
	doctorCheckSecrets(&rep, contract.Secrets, projectRoot)
	doctorFinalize(&rep)
	return rep
}

func doctorAdd(rep *DoctorReport, group, id, title, status, expected, actual, detail string, evidence ...string) {
	if status == "" {
		status = doctorUnknown
	}
	rep.Checks = append(rep.Checks, DoctorCheck{
		ID: id, Group: group, Title: title, Status: status,
		Expected: expected, Actual: actual, Detail: detail, Evidence: evidence,
	})
}

func doctorCheckProject(rep *DoctorReport, root string) {
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		doctorAdd(rep, "project", "project.root", "项目根目录", doctorDrift,
			"目录存在", root, "项目根不存在或不是目录")
		return
	}
	doctorAdd(rep, "project", "project.root", "项目根目录", doctorOK,
		"目录存在", root, "项目根可读")
}

func doctorCheckTools(rep *DoctorReport, tools map[string]string) {
	if len(tools) == 0 {
		doctorAdd(rep, "tools", "tools.declared", "工具链版本", doctorUndeclared,
			"至少声明 go/node/java 之一", "契约未声明工具版本", "未声明的版本要求不能算通过")
		return
	}
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		constraint := strings.TrimSpace(tools[name])
		actual := doctorToolActual(name)
		id := "tools." + name
		if actual == "" {
			doctorAdd(rep, "tools", id, "工具链 "+name, doctorDrift,
				constraint, "未检测到", "契约声明了版本，但 PATH 里没有可用的 "+name)
			continue
		}
		r := EnvReq{Component: name, Required: constraint, Actual: actual}
		r = compareReq(r, actual, "契约声明 "+name+" "+constraint)
		status := doctorOK
		switch r.Status {
		case reqTooLow, reqNotInstall:
			status = doctorDrift
		case reqUndeclared:
			status = doctorUnknown
		}
		doctorAdd(rep, "tools", id, "工具链 "+name, status,
			constraint, actual, r.Why, r.Action)
	}
}

func doctorToolActual(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "go":
		return probeVersionOf("go")
	case "node", "nodejs", "node.js":
		return probeVersionOf("node")
	case "java", "jdk":
		return probeJavaVersion()
	}
	return ""
}

func doctorCheckServices(rep *DoctorReport, svcs []Service, root string) {
	if len(svcs) == 0 {
		doctorAdd(rep, "services", "services.declared", "服务清单", doctorUndeclared,
			"至少声明一个服务", "契约未声明服务", "没有服务清单，Doctor 只能检查工具和数据库")
		return
	}
	for i, svc := range svcs {
		id := "services." + svc.ID
		if svc.ID == "" {
			id = fmt.Sprintf("services[%d]", i)
		}
		if svc.Adapter == AdapterSSHChain {
			doctorAdd(rep, "services", id, "服务 "+svcLabelOrID(svc), doctorUnknown,
				"远程链端探针", "Phase 2 不探测远程链端", "远程 SSH/链端检查留到后续阶段；不把未探测当成通过")
			continue
		}
		if strings.TrimSpace(svc.Dir) != "" && svc.Dir != "." {
			abs, err := contractAbsPath(root, svc.Dir)
			if err != nil {
				doctorAdd(rep, "services", id+".dir", "服务目录 "+svcLabelOrID(svc), doctorDrift,
					"项目内目录", svc.Dir, err.Error())
			} else if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
				doctorAdd(rep, "services", id+".dir", "服务目录 "+svcLabelOrID(svc), doctorDrift,
					"目录存在", abs, "服务目录不存在或不是目录")
			} else {
				doctorAdd(rep, "services", id+".dir", "服务目录 "+svcLabelOrID(svc), doctorOK,
					"目录存在", abs, "目录可读")
			}
		}
		if strings.TrimSpace(svc.Start) == "" {
			doctorAdd(rep, "services", id+".start", "启动命令 "+svcLabelOrID(svc), doctorUndeclared,
				"契约声明启动命令", "未声明", "契约没有启动命令，后续事务无法启动这个服务")
		}
		doctorCheckServiceProbe(rep, svc, id)
	}
}

func svcLabelOrID(svc Service) string {
	if strings.TrimSpace(svc.Label) != "" {
		return svc.Label
	}
	return svc.ID
}

func doctorCheckServiceProbe(rep *DoctorReport, svc Service, id string) {
	probe := svc.Probe
	switch probe.Kind {
	case ProbeHTTP:
		port := probe.Port
		if port <= 0 {
			port = doctorPortFromURL(probe.URL)
		}
		if port <= 0 {
			doctorAdd(rep, "services", id+".probe", "健康检查 "+svcLabelOrID(svc), doctorUnknown,
				"HTTP 探针", probe.URL, "契约声明了 HTTP 探针，但没有可用端口")
			return
		}
		occ := scanPortsOnce([]int{port})
		if len(occ) == 0 || occ[0].State != "LISTENING" {
			doctorAdd(rep, "services", id+".probe", "健康检查 "+svcLabelOrID(svc), doctorDrift,
				probe.URL, "端口未监听", fmt.Sprintf("端口 %d 没有监听者；服务可能未启动", port))
			return
		}
		http := httpProbeAt("127.0.0.1", port)
		code := httpStatusCode(http)
		if http == "" {
			doctorAdd(rep, "services", id+".probe", "健康检查 "+svcLabelOrID(svc), doctorDrift,
				probe.URL, "HTTP 未响应", fmt.Sprintf("端口 %d 在监听（PID %d %s），但 HTTP 没有响应", port, occ[0].PID, occ[0].Name))
			return
		}
		if code >= 500 {
			doctorAdd(rep, "services", id+".probe", "健康检查 "+svcLabelOrID(svc), doctorDrift,
				probe.URL, http, fmt.Sprintf("端口 %d 在监听，但服务端自报 HTTP %d", port, code),
				fmt.Sprintf("pid=%d", occ[0].PID), fmt.Sprintf("process=%s", occ[0].Name))
			return
		}
		doctorAdd(rep, "services", id+".probe", "健康检查 "+svcLabelOrID(svc), doctorOK,
			probe.URL, http, fmt.Sprintf("端口 %d 在监听，HTTP 可用", port),
			fmt.Sprintf("pid=%d", occ[0].PID), fmt.Sprintf("process=%s", occ[0].Name))
	case ProbePort:
		port := probe.Port
		if port <= 0 {
			doctorAdd(rep, "services", id+".probe", "端口探针 "+svcLabelOrID(svc), doctorUnknown,
				"端口探针", "未声明端口", "契约声明了端口探针，但没有端口")
			return
		}
		occ := scanPortsOnce([]int{port})
		if len(occ) == 0 || occ[0].State != "LISTENING" {
			doctorAdd(rep, "services", id+".probe", "端口探针 "+svcLabelOrID(svc), doctorDrift,
				fmt.Sprintf("端口 %d", port), "未监听", "端口没有监听者；服务可能未启动")
			return
		}
		doctorAdd(rep, "services", id+".probe", "端口探针 "+svcLabelOrID(svc), doctorOK,
			fmt.Sprintf("端口 %d", port), "LISTENING", "",
			fmt.Sprintf("pid=%d", occ[0].PID), fmt.Sprintf("process=%s", occ[0].Name))
	case ProbeCommand:
		doctorAdd(rep, "services", id+".probe", "命令探针 "+svcLabelOrID(svc), doctorUnknown,
			"只读命令探针", probe.Command, "Phase 2 不执行契约里的命令探针；避免把诊断变成任意命令执行")
	case ProbeComposite:
		doctorAdd(rep, "services", id+".probe", "组合探针 "+svcLabelOrID(svc), doctorUnknown,
			"组合探针", "composite", "Phase 2 暂不递归执行组合探针")
	default:
		doctorAdd(rep, "services", id+".probe", "健康检查 "+svcLabelOrID(svc), doctorUnknown,
			"port/http/command/composite", probe.Kind, "契约声明了未知探针类型")
	}
}

func doctorPortFromURL(raw string) int {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	if u.Port() != "" {
		p, _ := strconv.Atoi(u.Port())
		return p
	}
	if u.Scheme == "https" {
		return 443
	}
	if u.Scheme == "http" {
		return 80
	}
	return 0
}

func doctorCheckDatabase(rep *DoctorReport, d *DatabaseContract, root string) {
	if d == nil {
		doctorAdd(rep, "database", "database.declared", "数据库契约", doctorUndeclared,
			"声明 engine/name", "未声明数据库", "未声明不构成失败，但 Doctor 无法检查数据库层")
		return
	}
	if d.Engine != "mysql" {
		doctorAdd(rep, "database", "database.engine", "数据库引擎", doctorUnknown,
			"mysql", d.Engine, "Phase 2 只支持 MySQL 诊断")
		return
	}
	if strings.TrimSpace(d.Name) == "" {
		doctorAdd(rep, "database", "database.name", "数据库名", doctorDrift,
			"非空库名", "空", "契约声明了数据库，但库名为空")
		return
	}

	params, err := doctorDBParams(d)
	if err != nil {
		doctorAdd(rep, "database", "database.connect", "数据库连接", doctorDrift,
			"可连接 "+d.Name, "未配置", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cols, rows, err := mysqlReadOnlyQuery(ctx, params, "", "SELECT VERSION();")
	if err != nil || len(rows) == 0 || len(rows[0]) == 0 {
		msg := "连接失败"
		if err != nil {
			msg = err.Error()
		}
		doctorAdd(rep, "database", "database.connect", "数据库连接", doctorDrift,
			"连接 "+d.Name, "失败", firstLines(msg, 300))
		return
	}
	version := strings.TrimSpace(rows[0][0])
	doctorAdd(rep, "database", "database.connect", "数据库连接", doctorOK,
		"可连接 "+d.Name, version, "MySQL 版本 "+version, "columns="+strings.Join(cols, ","))

	exists, err := doctorDBExists(ctx, params, d.Name)
	if err != nil {
		doctorAdd(rep, "database", "database.exists", "数据库存在", doctorUnknown,
			d.Name, "查询失败", firstLines(err.Error(), 300))
	} else if !exists {
		doctorAdd(rep, "database", "database.exists", "数据库存在", doctorDrift,
			d.Name, "不存在", "契约声明的库不存在；Phase 3 才会执行建库")
	} else {
		doctorAdd(rep, "database", "database.exists", "数据库存在", doctorOK,
			d.Name, "存在", "")
	}

	doctorCheckSchema(rep, d, root)
	if exists {
		doctorCheckTables(rep, d, params, ctx)
	}
}

func doctorDBParams(d *DatabaseContract) (mysqlConnParams, error) {
	p := cfgSnapshot().Projects
	host := strings.TrimSpace(d.Host)
	if host == "" {
		host = strings.TrimSpace(p.MySQLHost)
	}
	port := d.Port
	if port <= 0 {
		port = p.MySQLPort
	}
	if port <= 0 {
		port = 3306
	}
	user := strings.TrimSpace(d.User)
	if user == "" {
		user = strings.TrimSpace(p.MySQLUser)
	}
	if host == "" || user == "" {
		return mysqlConnParams{}, fmt.Errorf("数据库主机/用户未配置")
	}
	// 密码始终来自本机配置，不从契约读取。契约只存 secret 引用。
	pass := p.MySQLPass
	if strings.TrimSpace(pass) == "" {
		return mysqlConnParams{}, fmt.Errorf("本机没有数据库密码绑定（secrets.mysql_password）")
	}
	return mysqlConnParams{Host: host, Port: port, User: user, Pass: pass, DB: d.Name}, nil
}

func doctorDBExists(ctx context.Context, p mysqlConnParams, name string) (bool, error) {
	q := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='%s';",
		strings.ReplaceAll(name, "'", "''"))
	_, rows, err := mysqlReadOnlyQuery(ctx, p, "", q)
	if err != nil {
		return false, err
	}
	return len(rows) > 0 && len(rows[0]) > 0 && strings.TrimSpace(rows[0][0]) == "1", nil
}

func doctorCheckSchema(rep *DoctorReport, d *DatabaseContract, root string) {
	if strings.TrimSpace(d.Schema) == "" {
		doctorAdd(rep, "database", "database.schema", "Schema 文件", doctorUndeclared,
			"契约声明 schema 路径", "未声明", "未声明 schema，Doctor 不推断表结构")
		return
	}
	abs, err := contractAbsPath(root, d.Schema)
	if err != nil {
		doctorAdd(rep, "database", "database.schema", "Schema 文件", doctorDrift,
			d.Schema, "路径逃逸", err.Error())
		return
	}
	if _, err := os.Stat(abs); err != nil {
		doctorAdd(rep, "database", "database.schema", "Schema 文件", doctorDrift,
			d.Schema, "文件不存在", abs)
		return
	}
	doctorAdd(rep, "database", "database.schema", "Schema 文件", doctorOK,
		d.Schema, "文件存在", abs)
}

func doctorCheckTables(rep *DoctorReport, d *DatabaseContract, p mysqlConnParams, ctx context.Context) {
	required := append([]string(nil), d.RequiredTables...)
	if len(required) == 0 && strings.TrimSpace(d.Schema) != "" {
		if abs, err := contractAbsPath(rep.ProjectRoot, d.Schema); err == nil {
			required = tableNamesFromSchema(abs)
		}
	}
	if len(required) == 0 {
		doctorAdd(rep, "database", "database.tables", "数据库表", doctorUndeclared,
			"required_tables 或可解析的 schema", "未声明", "没有表清单，不能判断库结构是否完整")
		return
	}
	actual, err := doctorTableNames(ctx, p, d.Name)
	if err != nil {
		doctorAdd(rep, "database", "database.tables", "数据库表", doctorUnknown,
			strings.Join(required, ", "), "查询失败", firstLines(err.Error(), 300))
		return
	}
	var missing []string
	for _, t := range required {
		if !actual[t] {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		doctorAdd(rep, "database", "database.tables", "数据库表", doctorDrift,
			strings.Join(required, ", "), "缺少 "+strings.Join(missing, ", "),
			"库存在，但缺少契约声明的表")
		return
	}
	doctorAdd(rep, "database", "database.tables", "数据库表", doctorOK,
		strings.Join(required, ", "), "全部存在", "")
}

func doctorTableNames(ctx context.Context, p mysqlConnParams, db string) (map[string]bool, error) {
	q := fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s';",
		strings.ReplaceAll(db, "'", "''"))
	_, rows, err := mysqlReadOnlyQuery(ctx, p, "", q)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, row := range rows {
		if len(row) > 0 {
			out[row[0]] = true
		}
	}
	return out, nil
}

var reCreateTable = regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[\x60"\[]?([A-Za-z0-9_]+)`)

func tableNamesFromSchema(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range reCreateTable.FindAllStringSubmatch(string(b), -1) {
		if len(m) > 1 && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

func doctorCheckSecrets(rep *DoctorReport, secrets map[string]string, root string) {
	if len(secrets) == 0 {
		doctorAdd(rep, "secrets", "secrets.declared", "密钥引用", doctorUndeclared,
			"env:/file:/local: 引用", "未声明", "未声明密钥引用，数据库密码来源无法核对")
		return
	}
	names := make([]string, 0, len(secrets))
	for k := range secrets {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := strings.TrimSpace(secrets[name])
		id := "secrets." + name
		switch {
		case strings.HasPrefix(ref, "env:"):
			envName := strings.TrimSpace(strings.TrimPrefix(ref, "env:"))
			if os.Getenv(envName) == "" {
				doctorAdd(rep, "secrets", id, "密钥 "+name, doctorDrift,
					ref, "环境变量为空", "契约要求环境变量 "+envName+"，当前未设置")
			} else {
				doctorAdd(rep, "secrets", id, "密钥 "+name, doctorOK,
					ref, "已设置", "环境变量存在（值不记录）")
			}
		case strings.HasPrefix(ref, "file:"):
			p := strings.TrimSpace(strings.TrimPrefix(ref, "file:"))
			abs, err := contractAbsPath(root, p)
			if err != nil {
				doctorAdd(rep, "secrets", id, "密钥 "+name, doctorDrift,
					ref, "路径非法", err.Error())
			} else if _, err := os.Stat(abs); err != nil {
				doctorAdd(rep, "secrets", id, "密钥 "+name, doctorDrift,
					ref, "文件不存在", abs)
			} else {
				doctorAdd(rep, "secrets", id, "密钥 "+name, doctorOK,
					ref, "文件存在", abs)
			}
		case strings.HasPrefix(ref, "local:"):
			if name == "mysql_password" {
				if strings.TrimSpace(cfgSnapshot().Projects.MySQLPass) == "" {
					doctorAdd(rep, "secrets", id, "密钥 "+name, doctorDrift,
						ref, "本机未绑定", "本机 config 里没有 mysql_password，请用向导保存数据库密码")
				} else {
					doctorAdd(rep, "secrets", id, "密钥 "+name, doctorOK,
						ref, "本机已绑定", "密码由本机 DPAPI 保存，不读出、不记录")
				}
			} else {
				doctorAdd(rep, "secrets", id, "密钥 "+name, doctorUnknown,
					ref, "本机绑定", "Phase 2 只核对 mysql_password 的本机绑定")
			}
		default:
			doctorAdd(rep, "secrets", id, "密钥 "+name, doctorDrift,
				"env:/file:/local:", ref, "非法密钥引用")
		}
	}
}

func doctorSummarize(rep *DoctorReport) {
	rep.Summary = DoctorSummary{}
	for _, c := range rep.Checks {
		switch c.Status {
		case doctorOK:
			rep.Summary.OK++
		case doctorDrift:
			rep.Summary.Drift++
		case doctorUndeclared:
			rep.Summary.Undeclared++
		case doctorUnknown:
			rep.Summary.Unknown++
		case doctorSkip:
			rep.Summary.Skip++
		}
	}
}

func doctorFinalize(rep *DoctorReport) {
	doctorSummarize(rep)
	switch {
	case rep.Summary.Drift > 0:
		rep.Status = doctorStatusDrift
		rep.Conclusion = fmt.Sprintf("发现 %d 项与契约不一致；Doctor 没有修改任何东西。", rep.Summary.Drift)
	case rep.Summary.Unknown > 0 || rep.Summary.Undeclared > 0:
		rep.Status = doctorStatusPartial
		rep.Conclusion = fmt.Sprintf("已声明项通过；另有 %d 项未声明、%d 项无法核实。",
			rep.Summary.Undeclared, rep.Summary.Unknown)
	default:
		rep.Status = doctorStatusOK
		rep.Conclusion = "契约声明的项目全部与实际一致。"
	}
	rep.OK = rep.Status == doctorStatusOK
}
