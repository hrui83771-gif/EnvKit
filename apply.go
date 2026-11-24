// apply.go —— v4.0 Phase 3：契约 Apply（只修安全项）
//
// Apply 不是第二套 Doctor，而是 Doctor 的“可选修复层”：
//
//	· 计划只从 Doctor 的 drift 项生成，不扫描不存在的差异；
//	· 只执行白名单动作（建库、启动 web/backend、导入契约里的 schema）；
//	· 高风险动作（导入 schema）必须二次确认；
//	· 不升级工具链、不改契约、不执行契约里的任意 start/stop 命令。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	applyRiskSafe    = "safe"
	applyRiskConfirm = "confirm"
	applyRiskBlocked = "blocked"
)

type ApplyAction struct {
	ID            string `json:"id"`
	Group         string `json:"group"`
	Title         string `json:"title"`
	Target        string `json:"target"`
	Risk          string `json:"risk"`
	Reason        string `json:"reason,omitempty"`
	Available     bool   `json:"available"`
	NeedsConfirm  bool   `json:"needs_confirm"`
	Op            string `json:"-"`
	Service       string `json:"-"`
	ExpectedState string `json:"expected_state,omitempty"`
}

type ApplyPlan struct {
	OK           bool          `json:"ok"`
	ContractPath string        `json:"contract_path,omitempty"`
	ProjectRoot  string        `json:"project_root,omitempty"`
	Mode         string        `json:"mode"`
	Actions      []ApplyAction `json:"actions"`
	Conclusion   string        `json:"conclusion,omitempty"`
	Error        string        `json:"error,omitempty"`
	GeneratedAt  string        `json:"generated_at"`

	contract *EnvironmentContract `json:"-"`
}

type ApplyActionResult struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	OK       bool     `json:"ok"`
	Msg      string   `json:"msg"`
	Evidence []string `json:"evidence,omitempty"`
	Artifact string   `json:"artifact,omitempty"`
}

type ApplyRun struct {
	OK       bool                `json:"ok"`
	Applied  []ApplyActionResult `json:"applied"`
	Doctor   DoctorReport        `json:"doctor"`
	Error    string              `json:"error,omitempty"`
	Finished string              `json:"finished_at"`
}

var contractApplyMu sync.Mutex

// buildContractApplyPlan 把 Doctor 的 drift 项翻译成可执行/不可执行的动作。
// 纯函数：不给 handler 之外的调用方开“绕过 doctor”的后门。
func buildContractApplyPlan(c Config, contract *EnvironmentContract, root string, rep DoctorReport) ApplyPlan {
	p := ApplyPlan{
		OK:           contract != nil,
		ContractPath: rep.ContractPath,
		ProjectRoot:  rep.ProjectRoot,
		Mode:         contractModeOf(c),
		Actions:      []ApplyAction{},
		GeneratedAt:  time.Now().Format(time.RFC3339),
		contract:     contract,
	}
	if contract == nil {
		p.Error = "没有可解析的环境契约"
		return p
	}
	if root == "" {
		root = rep.ProjectRoot
	}
	add := func(a ApplyAction) {
		a.NeedsConfirm = a.Risk == applyRiskConfirm
		p.Actions = append(p.Actions, a)
	}

	for _, chk := range rep.Checks {
		if chk.Status != doctorDrift {
			continue
		}
		switch {
		case chk.ID == "database.exists":
			d := contract.Database
			if d == nil {
				continue
			}
			a := ApplyAction{
				ID: "apply.database.create", Group: "database",
				Title:  "创建数据库 " + d.Name,
				Target: d.Name, Risk: applyRiskSafe, Op: "database.create",
			}
			if _, err := doctorDBParams(d); err != nil {
				a.Available = false
				a.Risk = applyRiskBlocked
				a.Reason = "数据库连接参数不可用：" + err.Error()
			} else {
				a.Available = true
				a.ExpectedState = "数据库存在"
			}
			add(a)
		case chk.ID == "database.tables":
			d := contract.Database
			if d == nil {
				continue
			}
			a := ApplyAction{
				ID: "apply.database.schema", Group: "database",
				Title:  "导入契约 schema 到 " + d.Name,
				Target: d.Name, Risk: applyRiskConfirm, Op: "database.import_schema",
			}
			if strings.TrimSpace(d.Schema) == "" {
				a.Risk = applyRiskBlocked
				a.Reason = "契约没有声明 schema 路径；不能凭空推断表结构"
			} else if abs, err := contractAbsPath(root, d.Schema); err != nil {
				a.Risk = applyRiskBlocked
				a.Reason = "schema 路径不合法：" + err.Error()
			} else if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
				a.Risk = applyRiskBlocked
				a.Reason = "schema 文件不存在：" + abs
			} else if _, err := doctorDBParams(d); err != nil {
				a.Risk = applyRiskBlocked
				a.Reason = "数据库连接参数不可用：" + err.Error()
			} else {
				a.Available = true
				a.ExpectedState = "表清单补齐"
			}
			add(a)
		case strings.HasPrefix(chk.ID, "services.") && strings.HasSuffix(chk.ID, ".probe"):
			svcID := strings.TrimSuffix(strings.TrimPrefix(chk.ID, "services."), ".probe")
			svc, ok := contractService(contract, svcID)
			if !ok {
				continue
			}
			a := ApplyAction{
				ID: "apply.services." + svc.ID + ".start", Group: "services",
				Title: "启动 " + svcLabelOrID(svc), Target: svc.ID, Risk: applyRiskSafe, Op: "service.start", Service: svc.ID,
			}
			switch {
			case svc.Adapter != AdapterLocal:
				a.Risk = applyRiskBlocked
				a.Reason = "Phase 3 只启动 local 服务，远程链端不执行契约命令"
			case svc.ID != "web" && svc.ID != "backend":
				a.Risk = applyRiskBlocked
				a.Reason = "Phase 3 只自动启动 web/backend；其他服务留给 Phase 5 环境事务"
			case !serviceProbeWasDown(svc):
				a.Risk = applyRiskBlocked
				a.Reason = "端口已有监听，但健康检查不通过；不自动重启身份不明的实例"
			default:
				dir, err := serviceAbsDir(root, svc)
				if err != nil || !isDir(dir) {
					a.Risk = applyRiskBlocked
					a.Reason = "服务目录不可用：" + dir
				} else if svc.ID == "web" && webScriptOf(cfgSnapshot(), svc) == "" {
					a.Risk = applyRiskBlocked
					a.Reason = "认不出前端启动脚本；请先在程序配置里填写 web_script"
				} else {
					a.Available = true
					a.ExpectedState = "服务探针通过"
				}
			}
			add(a)
		case strings.HasPrefix(chk.ID, "tools."):
			add(ApplyAction{
				ID: "apply." + chk.ID, Group: "tools",
				Title: chk.Title, Target: chk.Actual, Risk: applyRiskBlocked,
				Reason: "Phase 3 不自动升级/降级工具链；版本变更需要用户自己选定安装包",
			})
		case chk.ID == "database.schema":
			add(ApplyAction{
				ID: "apply.database.schema.missing", Group: "database",
				Title: "补 schema 文件", Target: chk.Actual, Risk: applyRiskBlocked,
				Reason: "契约指向的 schema 不存在；先把文件放进项目，Doctor 才能据此修表",
			})
		case strings.HasPrefix(chk.ID, "secrets."):
			add(ApplyAction{
				ID: "apply." + chk.ID, Group: "secrets",
				Title: chk.Title, Target: chk.Actual, Risk: applyRiskBlocked,
				Reason: "密钥由本机绑定/环境变量提供；Phase 3 不代替你填密码",
			})
		default:
			add(ApplyAction{
				ID: "apply." + chk.ID, Group: chk.Group,
				Title: chk.Title, Target: chk.Actual, Risk: applyRiskBlocked,
				Reason: "该项不在 Phase 3 的安全自动修复范围内",
			})
		}
	}
	if len(p.Actions) == 0 {
		p.Conclusion = "没有需要 Apply 的 drift 项。"
	} else {
		p.Conclusion = "只执行你勾选的白名单动作；导入 schema 属于高风险，需二次确认。"
	}
	return p
}

func contractService(c *EnvironmentContract, id string) (Service, bool) {
	if c == nil {
		return Service{}, false
	}
	for _, s := range c.Services {
		if s.ID == id {
			return s, true
		}
	}
	return Service{}, false
}

func serviceAbsDir(root string, svc Service) (string, error) {
	d := strings.TrimSpace(svc.Dir)
	if d == "" || d == "." {
		return root, nil
	}
	return contractAbsPath(root, d)
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// serviceProbeWasDown 只在“端口没有监听”时建议启动。
// 端口在听但 HTTP 5xx 是另一类问题：盲目重启可能打断别人的实例。
func serviceProbeWasDown(svc Service) bool {
	if svc.Probe.Kind != ProbeHTTP && svc.Probe.Kind != ProbePort {
		return false
	}
	port := svc.Probe.Port
	if port <= 0 && svc.Probe.Kind == ProbeHTTP {
		port = doctorPortFromURL(svc.Probe.URL)
	}
	if port <= 0 {
		return false
	}
	occ := scanPortsOnce([]int{port})
	return len(occ) == 0 || occ[0].State != "LISTENING"
}

// webScriptOf 只从前端服务声明或本机配置里取一个**合法脚本名**。
// 不从任意 shell 字符串里直接执行。
func webScriptOf(c Config, svc Service) string {
	if s := strings.TrimSpace(c.Projects.WebScript); s != "" && validScriptName(s) {
		return s
	}
	start := strings.TrimSpace(svc.Start)
	for _, p := range []string{"npm run ", "npm run-script ", "pnpm run ", "yarn "} {
		if strings.HasPrefix(start, p) {
			s := strings.TrimSpace(strings.TrimPrefix(start, p))
			if validScriptName(s) {
				return s
			}
		}
	}
	if start == "npm start" {
		return "start"
	}
	return ""
}

func planContractApply(c Config) (ApplyPlan, error) {
	if contractModeOf(c) == contractModeOff {
		return ApplyPlan{}, fmt.Errorf("环境契约为 off 模式；请先在设置里启用 readonly")
	}
	_, path, err := contractPathOf(c)
	if err != nil {
		return ApplyPlan{}, err
	}
	contract, problems, err := loadContractFile(path)
	if err != nil {
		return ApplyPlan{}, err
	}
	if len(problems) > 0 {
		return ApplyPlan{}, fmt.Errorf("契约自身有硬错误：%s", strings.Join(problems, "；"))
	}
	rep := runContractDoctor(c, path)
	root := rep.ProjectRoot
	if root == "" {
		root = filepath.Dir(path)
	}
	return buildContractApplyPlan(c, contract, root, rep), nil
}

func handleContractApplyPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	plan, err := planContractApply(cfgSnapshot())
	if err != nil {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, plan)
}

func handleContractApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Selected []string `json:"selected"`
		Confirm  bool     `json:"confirm"`
		Lang     string   `json:"lang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), 400)
		return
	}
	c := cfgSnapshot()
	if contractModeOf(c) == contractModeOff {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "环境契约为 off 模式；请先在设置里启用 readonly"})
		return
	}
	plan, err := planContractApply(c)
	if err != nil {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if len(body.Selected) == 0 {
		writeJSONStatus(w, 400, map[string]any{"ok": false, "error": "请至少选择一个修复动作"})
		return
	}
	selected := map[string]bool{}
	for _, id := range body.Selected {
		selected[strings.TrimSpace(id)] = true
	}
	var chosen []ApplyAction
	for _, a := range plan.Actions {
		if !selected[a.ID] {
			continue
		}
		if !a.Available || a.Risk == applyRiskBlocked {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": a.Title + "：该动作不可执行（" + a.Reason + "）"})
			return
		}
		chosen = append(chosen, a)
	}
	if len(chosen) == 0 {
		writeJSONStatus(w, 400, map[string]any{"ok": false, "error": "选中的 ID 不属于当前 Apply 计划"})
		return
	}
	needConfirm := false
	var high []string
	for _, a := range chosen {
		if a.NeedsConfirm {
			needConfirm = true
			high = append(high, a.Title)
		}
	}
	if needConfirm && !body.Confirm {
		reason := "即将执行高风险动作：" + strings.Join(high, "、") + "。导入 schema 可能覆盖现有表或数据，执行前会自动备份。"
		if strings.EqualFold(body.Lang, "en") {
			reason = "About to run high-risk actions: " + strings.Join(high, ", ") + ". Importing schema may overwrite tables/data; a backup is taken first."
		}
		writeJSONStatus(w, 428, map[string]any{
			"needConfirm": true, "action": "contract_apply", "target": plan.ContractPath,
			"reason": reason, "highrisk": true,
		})
		return
	}
	if !contractApplyMu.TryLock() {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "已有 Apply 正在执行，请稍候"})
		return
	}
	defer contractApplyMu.Unlock()

	run := ApplyRun{OK: true, Finished: time.Now().Format(time.RFC3339)}
	for _, a := range chosen {
		var res ApplyActionResult
		switch a.Op {
		case "database.create":
			res = applyCreateDatabase(plan.contract.Database)
		case "database.import_schema":
			res = applyImportSchema(plan.contract.Database, plan.ProjectRoot)
		case "service.start":
			res = applyStartService(plan.contract, plan.ProjectRoot, a.Service)
		default:
			res = ApplyActionResult{ID: a.ID, Title: a.Title, OK: false, Msg: "未知动作"}
		}
		res.ID, res.Title = a.ID, a.Title
		if res.OK {
			auditNow(actUser, "contract_apply", a.Target, a.ID, resOK, res.Msg)
		} else {
			auditNow(actUser, "contract_apply", a.Target, a.ID, resFail, res.Msg)
			run.OK = false
			if run.Error == "" {
				run.Error = res.Msg
			}
		}
		run.Applied = append(run.Applied, res)
	}
	// 最后重新跑 Doctor：Apply 的验收不是“按钮执行完”，而是“差异是否变了”。
	run.Doctor = runContractDoctor(cfgSnapshot(), plan.ContractPath)
	writeJSON(w, run)
}

func applyCreateDatabase(d *DatabaseContract) ApplyActionResult {
	if d == nil {
		return ApplyActionResult{OK: false, Msg: "契约没有数据库声明"}
	}
	p, err := doctorDBParams(d)
	if err != nil {
		return ApplyActionResult{OK: false, Msg: err.Error()}
	}
	exists, err := doctorDBExists(context.Background(), p, d.Name)
	if err != nil {
		return ApplyActionResult{OK: false, Msg: "查询数据库失败：" + err.Error()}
	}
	if exists {
		return ApplyActionResult{OK: true, Msg: "数据库已存在，无需创建", Evidence: []string{"database=" + d.Name}}
	}
	ident, err := quoteIdent(d.Name)
	if err != nil {
		return ApplyActionResult{OK: false, Msg: err.Error()}
	}
	sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;", ident)
	if err := mysqlExec(p, "", sql); err != nil {
		return ApplyActionResult{OK: false, Msg: "建库失败：" + firstLines(err.Error(), 300)}
	}
	exists, err = doctorDBExists(context.Background(), p, d.Name)
	if err != nil || !exists {
		return ApplyActionResult{OK: false, Msg: "建库后复核失败：" + fmt.Sprint(err)}
	}
	info(scConfig, "契约 Apply", "已创建数据库 %s", d.Name)
	return ApplyActionResult{OK: true, Msg: "已创建数据库 " + d.Name, Evidence: []string{"database=" + d.Name, "verified=true"}}
}

func applyImportSchema(d *DatabaseContract, root string) ApplyActionResult {
	if d == nil || strings.TrimSpace(d.Schema) == "" {
		return ApplyActionResult{OK: false, Msg: "契约没有声明 schema"}
	}
	abs, err := contractAbsPath(root, d.Schema)
	if err != nil {
		return ApplyActionResult{OK: false, Msg: err.Error()}
	}
	if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
		return ApplyActionResult{OK: false, Msg: "schema 文件不可用：" + abs}
	}
	p, err := doctorDBParams(d)
	if err != nil {
		return ApplyActionResult{OK: false, Msg: err.Error()}
	}
	exists, err := doctorDBExists(context.Background(), p, d.Name)
	if err != nil {
		return ApplyActionResult{OK: false, Msg: "查询数据库失败：" + err.Error()}
	}
	if !exists {
		if res := applyCreateDatabase(d); !res.OK {
			return res
		}
	}
	var backup string
	var evidence []string
	if exists {
		backup = filepath.Join(backupDir(), fmt.Sprintf("%s-apply-%s.sql", d.Name, time.Now().Format("20060102-150405")))
		size, sha, err := dumpDatabaseTo(p, d.Name, backup)
		if err != nil {
			return ApplyActionResult{OK: false, Msg: "导入前备份失败，已中止：" + err.Error()}
		}
		evidence = append(evidence, fmt.Sprintf("backup=%s (%d bytes sha256=%s)", backup, size, sha))
	}
	if err := applySQLFileWithParams(abs, p, d.Name, true); err != nil {
		return ApplyActionResult{OK: false, Msg: "导入 schema 失败：" + firstLines(err.Error(), 400), Evidence: evidence, Artifact: backup}
	}
	doctorCheckSchemaTables := []string{}
	if names := tableNamesFromSchema(abs); len(names) > 0 {
		actual, err := doctorTableNames(context.Background(), p, d.Name)
		if err == nil {
			for _, n := range names {
				if !actual[n] {
					doctorCheckSchemaTables = append(doctorCheckSchemaTables, n)
				}
			}
		}
	}
	if len(doctorCheckSchemaTables) > 0 {
		return ApplyActionResult{OK: false, Msg: "导入完成，但缺少表：" + strings.Join(doctorCheckSchemaTables, ", "), Evidence: evidence, Artifact: backup}
	}
	info(scConfig, "契约 Apply", "已导入 schema：%s -> %s", abs, d.Name)
	return ApplyActionResult{OK: true, Msg: "已导入 schema 到 " + d.Name, Evidence: evidence, Artifact: backup}
}

func applyStartService(contract *EnvironmentContract, root, serviceID string) ApplyActionResult {
	svc, ok := contractService(contract, serviceID)
	if !ok {
		return ApplyActionResult{OK: false, Msg: "契约里没有服务 " + serviceID}
	}
	dir, err := serviceAbsDir(root, svc)
	if err != nil || !isDir(dir) {
		return ApplyActionResult{OK: false, Msg: "服务目录不可用：" + dir}
	}
	switch serviceID {
	case "web":
		script := webScriptOf(cfgSnapshot(), svc)
		if script == "" {
			return ApplyActionResult{OK: false, Msg: "认不出前端启动脚本"}
		}
		res := webStartTask(dir, script, actUser)
		if !res.Ok {
			return ApplyActionResult{OK: false, Msg: res.Msg, Evidence: res.Evidence}
		}
		verified := verifyService("web", verifyWaitWeb())
		if !verified.Ok {
			return ApplyActionResult{OK: false, Msg: res.Msg + "；" + verified.Msg, Evidence: verified.Evidence}
		}
		return ApplyActionResult{OK: true, Msg: res.Msg + "；" + verified.Msg, Evidence: verified.Evidence}
	case "backend":
		res := backendStartTask(dir, actUser)
		if !res.Ok {
			return ApplyActionResult{OK: false, Msg: res.Msg, Evidence: res.Evidence}
		}
		verified := verifyService("backend", verifyWaitBackend())
		if !verified.Ok {
			return ApplyActionResult{OK: false, Msg: res.Msg + "；" + verified.Msg, Evidence: verified.Evidence}
		}
		return ApplyActionResult{OK: true, Msg: res.Msg + "；" + verified.Msg, Evidence: verified.Evidence}
	}
	return ApplyActionResult{OK: false, Msg: "Phase 3 只启动 web/backend"}
}
