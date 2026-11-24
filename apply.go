// apply.go —— v4.0 Phase 3：契约 Apply（只修安全项）
//
// Apply 不是第二套 Doctor，而是 Doctor 的“可选修复层”：
//
//	· 计划只从 Doctor 的 drift 项生成，不扫描不存在的差异；
//	· 只执行白名单动作（建库、启动 web/backend、导入契约里的 schema）；
//	· 高风险动作（导入 schema）必须二次确认；
//	· 工具链只诊断，不升级/切换；其他本地服务属于按需扩展，默认关闭。
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
	OK          bool                `json:"ok"`
	Applied     []ApplyActionResult `json:"applied"`
	Skipped     []ApplyActionResult `json:"skipped,omitempty"`
	Transaction *ApplyTransaction   `json:"transaction,omitempty"`
	Doctor      DoctorReport        `json:"doctor"`
	Error       string              `json:"error,omitempty"`
	Finished    string              `json:"finished_at"`
}

// ApplyTransaction 执行前自动快照的凭据。回滚复用 Phase 4 的 checkpoint restore，
// 不新造第二套恢复路径。
type ApplyTransaction struct {
	CheckpointID      string         `json:"checkpoint_id"`
	Checkpoint        CheckpointView `json:"checkpoint"`
	DatabaseIncluded  bool           `json:"database_included"`
	RollbackAvailable bool           `json:"rollback_available"`
	RollbackNote      string         `json:"rollback_note,omitempty"`
	StartedAt         string         `json:"started_at"`
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
				a.Reason = "4.0 主干只自动启动 web/backend；其他本地服务属于按需扩展，默认关闭"
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
				Reason: "工具链只诊断，不自动升级/降级或切换版本；是否需要处理由用户决定",
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
		p.Conclusion = "只执行你勾选的白名单动作；执行前自动快照，导入 schema 属于高风险，需二次确认。"
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
	switch contractModeOf(c) {
	case contractModeOff:
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "环境契约为 off 模式；请先在设置里启用契约"})
		return
	case contractModeReadOnly:
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "当前是 readonly 模式，只能生成计划；切到 enforce 才允许执行写操作"})
		return
	case contractModeEnforce:
		// ok
	default:
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "未知契约模式，请检查配置"})
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

	// 环境事务：先落一份执行前快照，再动任何东西。快照失败 = 不执行，
	// 否则“回滚”只是口号，用户拿不到执行前的状态。
	includeDB, stripNote, err := planApplyBackup(plan.contract, chosen)
	if err != nil {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	c = cfgSnapshot()
	m, cpDir, err := createCheckpointSnapshot(c, plan.contract, plan.ContractPath, "Apply 前自动快照", includeDB)
	if err != nil {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "执行前快照失败，已中止：" + err.Error()})
		return
	}
	cp := checkpointView(m, dirSize(cpDir))
	rollbackNote := "配置和端口现场已快照；回滚走下方快照列表，恢复前会先备份当前状态。"
	if includeDB {
		rollbackNote = "配置和数据库已快照；回滚会先备份当前库，再恢复这份快照。"
	}
	if stripNote != "" {
		rollbackNote += " " + stripNote
	}
	run := ApplyRun{
		OK: true, Finished: time.Now().Format(time.RFC3339),
		Transaction: &ApplyTransaction{
			CheckpointID: m.ID, Checkpoint: cp, DatabaseIncluded: includeDB,
			RollbackAvailable: true, RollbackNote: rollbackNote,
			StartedAt: time.Now().Format(time.RFC3339),
		},
	}
	auditNow(actUser, "contract_apply_tx", m.ID, plan.ContractPath, resStart, "执行前自动快照")
	info(scConfig, "契约 Apply", "事务快照 %s 已创建（数据库=%v）", m.ID, includeDB)
	writeJSON(w, executeApplyChosen(plan, chosen, run.Transaction))
}

// executeApplyChosen 是事务执行体：一个动作失败就停，后面的记 skipped，
// 返回同一个 transaction 凭据供 UI 提供回滚入口。
func executeApplyChosen(plan ApplyPlan, chosen []ApplyAction, tx *ApplyTransaction) ApplyRun {
	run := ApplyRun{OK: true, Finished: time.Now().Format(time.RFC3339), Transaction: tx}
	for i, a := range chosen {
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
			// 事务语义：一个动作失败就停，后面的不带着半截状态继续跑。
			for _, rest := range chosen[i+1:] {
				run.Skipped = append(run.Skipped, ApplyActionResult{
					ID: rest.ID, Title: rest.Title, OK: false, Msg: "前序动作失败，未执行",
				})
			}
			if run.Transaction != nil {
				run.Transaction.RollbackNote += " 有动作失败，可回滚到执行前快照。"
			}
			break
		}
		run.Applied = append(run.Applied, res)
	}
	// 最后重新跑 Doctor：Apply 的验收不是“按钮执行完”，而是“差异是否变了”。
	run.Doctor = runContractDoctor(cfgSnapshot(), plan.ContractPath)
	if run.Transaction != nil {
		auditNow(actUser, "contract_apply_tx", run.Transaction.CheckpointID, plan.ContractPath, map[bool]string{true: resOK, false: resFail}[run.OK], run.Error)
	}
	return run
}

// planApplyBackup 决定事务快照是否包含数据库。
// 导入 schema 时库已存在 → 必须带库；库原本不存在 → 只能留配置快照，
// 并明确告诉用户“回滚不会删除新建库”，不能假装能撤销建库。
func planApplyBackup(contract *EnvironmentContract, chosen []ApplyAction) (bool, string, error) {
	if contract == nil || contract.Database == nil {
		return false, "", nil
	}
	for _, a := range chosen {
		if a.Op != "database.import_schema" {
			continue
		}
		p, err := doctorDBParams(contract.Database)
		if err != nil {
			return false, "", fmt.Errorf("数据库连接参数不可用，无法建立事务快照：%w", err)
		}
		exists, err := doctorDBExists(context.Background(), p, contract.Database.Name)
		if err != nil {
			return false, "", fmt.Errorf("无法确认数据库状态，已中止：%w", err)
		}
		if exists {
			return true, "", nil
		}
		return false, "目标库执行前不存在，快照只含配置；回滚不会删除本次新建的库。", nil
	}
	for _, a := range chosen {
		if a.Op == "database.create" {
			return false, "建库动作不在快照回滚范围内；回滚不会删除本次新建的库。", nil
		}
	}
	return false, "", nil
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
