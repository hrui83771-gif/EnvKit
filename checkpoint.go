// checkpoint.go —— v4.0 Phase 4：最小环境快照
//
// 快照只收“把环境恢复到能继续开发”所需要的四样东西：
//  1. 契约文件副本（只作对照，恢复时不静默覆盖项目仓库里的 envkit.yaml）
//  2. 本机配置（DPAPI 加密后落盘，密码不以明文出现在快照目录）
//  3. 可选的数据库 dump（走固定 mysqldump 参数）
//  4. 服务探针与端口占用现场
//
// 入口是契约：没有契约就没有“当前环境应该是什么”的参照，不做 OS 全盘快照。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const checkpointKeep = 20

var checkpointDirFn = func() string {
	return filepath.Join(exeDir(), "checkpoints")
}

func checkpointDir() string { return checkpointDirFn() }

type CheckpointDatabase struct {
	Included   bool   `json:"included"`
	Name       string `json:"name,omitempty"`
	File       string `json:"file,omitempty"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	TableCount int    `json:"table_count,omitempty"`
	Skipped    string `json:"skipped,omitempty"`
}

type CheckpointPort struct {
	Port  int    `json:"port"`
	State string `json:"state"`
	PID   int    `json:"pid,omitempty"`
	Name  string `json:"name,omitempty"`
}

type CheckpointService struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Probe    string `json:"probe,omitempty"`
	Port     int    `json:"port,omitempty"`
	Status   string `json:"status"`
	Evidence string `json:"evidence,omitempty"`
}

type CheckpointManifest struct {
	Version      int                 `json:"version"`
	ID           string              `json:"id"`
	CreatedAt    string              `json:"created_at"`
	Label        string              `json:"label,omitempty"`
	ContractPath string              `json:"contract_path,omitempty"`
	ProjectRoot  string              `json:"project_root,omitempty"`
	ContractFile string              `json:"contract_file,omitempty"`
	ContractSHA  string              `json:"contract_sha256,omitempty"`
	ConfigFile   string              `json:"config_file,omitempty"`
	ConfigSHA    string              `json:"config_sha256,omitempty"`
	Database     CheckpointDatabase  `json:"database"`
	Services     []CheckpointService `json:"services"`
	Ports        []CheckpointPort    `json:"ports"`
}

type CheckpointView struct {
	ID           string `json:"id"`
	CreatedAt    string `json:"created_at"`
	Label        string `json:"label,omitempty"`
	Size         int64  `json:"size"`
	ContractPath string `json:"contract_path,omitempty"`
	ProjectRoot  string `json:"project_root,omitempty"`
	DBIncluded   bool   `json:"db_included"`
	DBName       string `json:"db_name,omitempty"`
	DBSize       int64  `json:"db_size,omitempty"`
	TableCount   int    `json:"table_count,omitempty"`
	Services     int    `json:"services"`
	Running      int    `json:"running"`
}

type CheckpointRestoreRun struct {
	OK              bool                `json:"ok"`
	Steps           []ApplyActionResult `json:"steps"`
	Doctor          DoctorReport        `json:"doctor"`
	ContractChanged bool                `json:"contract_changed"`
	ContractCopy    string              `json:"contract_copy,omitempty"`
	ContractNote    string              `json:"contract_note"`
	Error           string              `json:"error,omitempty"`
	Finished        string              `json:"finished_at"`
}

func newCheckpointID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "cp-" + time.Now().Format("20060102-150405")
	}
	return "cp-" + time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func validCheckpointID(id string) bool {
	if len(id) < 6 || len(id) > 64 || !strings.HasPrefix(id, "cp-") {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return filepath.Base(id) == id && !strings.Contains(id, "..")
}

func checkpointPath(id string) (string, error) {
	if !validCheckpointID(id) {
		return "", fmt.Errorf("快照 ID 不合法")
	}
	return filepath.Join(checkpointDir(), id), nil
}

func checkpointManifestPath(dir string) string { return filepath.Join(dir, "manifest.json") }

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi != nil && !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return total
}

func snapshotServiceState(contract *EnvironmentContract) ([]CheckpointService, []CheckpointPort) {
	services := []CheckpointService{}
	ports := []CheckpointPort{}
	if contract == nil {
		return services, ports
	}
	for _, svc := range contract.Services {
		row := CheckpointService{ID: svc.ID, Label: svcLabelOrID(svc), Status: "skip"}
		if svc.Adapter != AdapterLocal {
			row.Status = "unknown"
			row.Evidence = "远程链端不在本阶段探测范围"
			services = append(services, row)
			continue
		}
		p := svc.Probe.Port
		if p <= 0 && svc.Probe.Kind == ProbeHTTP {
			p = doctorPortFromURL(svc.Probe.URL)
		}
		row.Port = p
		row.Probe = svc.Probe.Kind
		if p <= 0 {
			row.Status = "no_probe"
			row.Evidence = "契约没有声明端口探针"
			services = append(services, row)
			continue
		}
		occ := scanPortsOnce([]int{p})
		for _, o := range occ {
			ports = append(ports, CheckpointPort{Port: o.Port, State: o.State, PID: o.PID, Name: o.Name})
			if o.Port == p && o.State == "LISTENING" {
				row.Status = "running"
				row.Evidence = fmt.Sprintf("port=%d pid=%d name=%s", o.Port, o.PID, o.Name)
			}
		}
		if row.Status != "running" {
			row.Status = "stopped"
			row.Evidence = fmt.Sprintf("port=%d 未监听", p)
		}
		services = append(services, row)
	}
	return services, ports
}

// createCheckpointSnapshot 是所有快照入口的唯一实现：手动“立即快照”和
// Apply 执行前的事务快照都走这里，避免两套清单格式。
// 调用方负责先校验契约和数据库是否存在；这里只做落盘与校验。
func createCheckpointSnapshot(c Config, contract *EnvironmentContract, contractPath, label string, includeDB bool) (CheckpointManifest, string, error) {
	var m CheckpointManifest
	if contract == nil {
		return m, "", fmt.Errorf("契约为空，不能创建快照")
	}
	id := newCheckpointID()
	dir, _ := checkpointPath(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return m, "", fmt.Errorf("创建快照目录失败：%w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	contractBytes, err := os.ReadFile(contractPath)
	if err != nil {
		cleanup()
		return m, "", fmt.Errorf("读取契约失败：%w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, "contract.yaml"), contractBytes, 0644); err != nil {
		cleanup()
		return m, "", fmt.Errorf("保存契约副本失败：%w", err)
	}

	cfgBytes, err := json.MarshalIndent(encryptConfig(c), "", "  ")
	if err != nil {
		cleanup()
		return m, "", fmt.Errorf("序列化配置失败：%w", err)
	}
	configName := "config.json"
	if err := writeFileAtomic(filepath.Join(dir, configName), cfgBytes, 0600); err != nil {
		cleanup()
		return m, "", fmt.Errorf("保存配置快照失败：%w", err)
	}

	m = CheckpointManifest{
		Version: 1, ID: id, CreatedAt: time.Now().Format(time.RFC3339),
		Label: strings.TrimSpace(label), ContractPath: contractPath, ProjectRoot: filepath.Dir(contractPath),
		ContractFile: "contract.yaml", ContractSHA: fileSHA256Hex(filepath.Join(dir, "contract.yaml")),
		ConfigFile: configName, ConfigSHA: fileSHA256Hex(filepath.Join(dir, configName)),
	}
	if len(m.Label) > 80 {
		m.Label = m.Label[:80]
	}

	if includeDB {
		d := contract.Database
		if d == nil {
			cleanup()
			return m, "", fmt.Errorf("契约没有数据库声明，不能快照数据库")
		}
		p, err := doctorDBParams(d)
		if err != nil {
			cleanup()
			return m, "", fmt.Errorf("数据库连接参数不可用：%w", err)
		}
		exists, err := doctorDBExists(context.Background(), p, d.Name)
		if err != nil {
			cleanup()
			return m, "", fmt.Errorf("查询数据库失败：%w", err)
		}
		if !exists {
			cleanup()
			return m, "", fmt.Errorf("数据库 %s 不存在，无法快照", d.Name)
		}
		dbFile := d.Name + ".sql"
		size, sha, err := dumpDatabaseTo(p, d.Name, filepath.Join(dir, dbFile))
		if err != nil {
			cleanup()
			return m, "", fmt.Errorf("数据库快照失败：%w", err)
		}
		actual, _ := doctorTableNames(context.Background(), p, d.Name)
		m.Database = CheckpointDatabase{
			Included: true, Name: d.Name, File: dbFile, Size: size, SHA256: sha, TableCount: len(actual),
		}
	} else {
		m.Database = CheckpointDatabase{Included: false, Skipped: "未选择数据库"}
	}
	m.Services, m.Ports = snapshotServiceState(contract)

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		cleanup()
		return m, "", fmt.Errorf("生成清单失败：%w", err)
	}
	if err := writeFileAtomic(checkpointManifestPath(dir), data, 0644); err != nil {
		cleanup()
		return m, "", fmt.Errorf("写入快照清单失败：%w", err)
	}
	pruneCheckpoints(checkpointKeep)
	return m, dir, nil
}

func handleContractCheckpointCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Label    string `json:"label"`
		Database bool   `json:"database"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), 400)
		return
	}
	c := cfgSnapshot()
	_, contractPath, err := contractPathOf(c)
	if err != nil {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	contract, problems, err := loadContractFile(contractPath)
	if err != nil {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if len(problems) > 0 {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "契约自身有硬错误：" + strings.Join(problems, "；")})
		return
	}
	h, granted := beginTaskH("环境快照", true)
	if !granted {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "有任务正在执行，请稍候"})
		return
	}
	defer h.Done()

	// 先做只读预检：数据库不存在是用户可修正的冲突，应该在写目录前返回 409。
	if body.Database {
		d := contract.Database
		if d == nil {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "契约没有数据库声明，不能快照数据库"})
			return
		}
		p, err := doctorDBParams(d)
		if err != nil {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "数据库连接参数不可用：" + err.Error()})
			return
		}
		exists, err := doctorDBExists(context.Background(), p, d.Name)
		if err != nil {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "查询数据库失败：" + err.Error()})
			return
		}
		if !exists {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "数据库 " + d.Name + " 不存在，无法快照"})
			return
		}
	}

	m, dir, err := createCheckpointSnapshot(c, contract, contractPath, body.Label, body.Database)
	if err != nil {
		writeJSONStatus(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	auditNow(actUser, "checkpoint_create", m.ID, fmt.Sprintf("db=%v", m.Database.Included), resOK, "")
	info(scConfig, "快照", "已创建环境快照 %s（数据库=%v）", m.ID, m.Database.Included)
	writeJSON(w, map[string]any{"ok": true, "checkpoint": checkpointView(m, dirSize(dir))})
}

func checkpointView(m CheckpointManifest, size int64) CheckpointView {
	running := 0
	for _, s := range m.Services {
		if s.Status == "running" {
			running++
		}
	}
	return CheckpointView{
		ID: m.ID, CreatedAt: m.CreatedAt, Label: m.Label, Size: size,
		ContractPath: m.ContractPath, ProjectRoot: m.ProjectRoot,
		DBIncluded: m.Database.Included, DBName: m.Database.Name, DBSize: m.Database.Size,
		TableCount: m.Database.TableCount, Services: len(m.Services), Running: running,
	}
}

func readCheckpointManifest(dir string) (CheckpointManifest, error) {
	var m CheckpointManifest
	data, err := os.ReadFile(checkpointManifestPath(dir))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("快照清单损坏：%v", err)
	}
	if m.Version != 1 || m.ID == "" {
		return m, fmt.Errorf("不支持的快照清单")
	}
	return m, nil
}

func listCheckpoints() []CheckpointView {
	out := []CheckpointView{}
	entries, err := os.ReadDir(checkpointDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || !validCheckpointID(e.Name()) {
			continue
		}
		m, err := readCheckpointManifest(filepath.Join(checkpointDir(), e.Name()))
		if err != nil {
			continue
		}
		m.ID = e.Name()
		out = append(out, checkpointView(m, dirSize(filepath.Join(checkpointDir(), e.Name()))))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func pruneCheckpoints(keep int) {
	entries, err := os.ReadDir(checkpointDir())
	if err != nil {
		return
	}
	ids := []string{}
	for _, e := range entries {
		if e.IsDir() && validCheckpointID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) <= keep {
		return
	}
	sort.Strings(ids)
	for _, id := range ids[:len(ids)-keep] {
		_ = os.RemoveAll(filepath.Join(checkpointDir(), id))
	}
}

func handleContractCheckpointList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	writeJSON(w, map[string]any{"checkpoints": listCheckpoints(), "dir": checkpointDir(), "keep": checkpointKeep})
}

func handleContractCheckpointRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		ID      string `json:"id"`
		Confirm bool   `json:"confirm"`
		Lang    string `json:"lang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), 400)
		return
	}
	dir, err := checkpointPath(strings.TrimSpace(body.ID))
	if err != nil {
		writeJSONStatus(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	m, err := readCheckpointManifest(dir)
	if err != nil {
		writeJSONStatus(w, 404, map[string]any{"ok": false, "error": "快照不可读：" + err.Error()})
		return
	}
	// 先做只读预检：任何一项不合法都不进入写阶段。
	var restored Config
	configPath := filepath.Join(dir, filepath.Base(m.ConfigFile))
	if m.ConfigFile != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "快照配置不可读：" + err.Error()})
			return
		}
		if m.ConfigSHA != "" && fileSHA256Hex(configPath) != m.ConfigSHA {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "配置快照校验和不匹配，拒绝恢复"})
			return
		}
		if err := json.Unmarshal(data, &restored); err != nil {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "快照配置不是有效 JSON：" + err.Error()})
			return
		}
		decryptConfig(&restored)
		normalizeConfig(&restored)
		migrateConfig(&restored, restored.SchemaVersion)
		if hard := hardProblems(checkConfig(restored)); len(hard) > 0 {
			msgs := []string{}
			for _, p := range hard {
				msgs = append(msgs, p.String())
			}
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "快照配置未通过硬校验：" + strings.Join(msgs, "；")})
			return
		}
	}
	var d *DatabaseContract
	if m.Database.Included {
		dbPath := filepath.Join(dir, filepath.Base(m.Database.File))
		fi, err := os.Stat(dbPath)
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "快照数据库文件不可用"})
			return
		}
		if m.Database.SHA256 != "" && fileSHA256Hex(dbPath) != m.Database.SHA256 {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "数据库快照校验和不匹配，拒绝恢复"})
			return
		}
		// 连接参数以当前本机配置为准；库身份优先用当前契约，缺失再读快照副本。
		if _, currentPath, err := contractPathOf(cfgSnapshot()); err == nil {
			if cur, _, err := loadContractFile(currentPath); err == nil {
				d = cur.Database
			}
		}
		if d == nil {
			if snap, _, err := loadContractFile(filepath.Join(dir, filepath.Base(m.ContractFile))); err == nil {
				d = snap.Database
			}
		}
		if d == nil {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "找不到数据库连接声明，无法恢复数据库"})
			return
		}
		if _, err := doctorDBParams(d); err != nil {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "数据库连接参数不可用：" + err.Error()})
			return
		}
	}
	if !body.Confirm {
		reason := "即将恢复快照 " + m.ID + "：配置" + boolWord(m.ConfigFile != "") + "、数据库" + boolWord(m.Database.Included) + "。当前配置和数据库都会先留备份。"
		if strings.EqualFold(body.Lang, "en") {
			reason = "About to restore checkpoint " + m.ID + ": settings=" + boolWord(m.ConfigFile != "") + ", database=" + boolWord(m.Database.Included) + ". Current settings and database are backed up first."
		}
		needConfirm(w, "checkpoint_restore", m.ID, reason, true)
		return
	}
	h, granted := beginTaskH("快照恢复", true)
	if !granted {
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "有任务正在执行，请稍候"})
		return
	}
	defer h.Done()

	run := CheckpointRestoreRun{OK: true, Finished: time.Now().Format(time.RFC3339), ContractNote: "契约文件未自动覆盖"}
	// 1) 数据库：先备份当前库，再导入快照。
	if m.Database.Included {
		step, _ := restoreCheckpointDatabase(m, d, dir)
		run.Steps = append(run.Steps, step)
		if !step.OK {
			run.OK = false
			run.Error = step.Msg
			auditNow(actUser, "checkpoint_restore", m.ID, "database", resFail, step.Msg)
		}
	}
	// 2) 配置：复用配置保存的轮转与热更新路径。
	if run.OK && m.ConfigFile != "" {
		step := ApplyActionResult{ID: "restore.config", Title: "恢复本机配置"}
		if err := applyRestoredConfig(restored); err != nil {
			step.OK = false
			step.Msg = err.Error()
			run.OK = false
			run.Error = step.Msg
		} else {
			step.OK = true
			step.Msg = "配置已恢复；当前配置已轮转为 .bak"
			step.Evidence = []string{"config=" + configFilePath()}
		}
		run.Steps = append(run.Steps, step)
	}
	if !run.OK {
		run.Error = strings.TrimSpace(run.Error)
	} else {
		info(scConfig, "快照", "已恢复环境快照 %s", m.ID)
	}
	// 3) 契约副本只做对照，不写回项目仓库。
	run.ContractCopy = filepath.Join(dir, filepath.Base(m.ContractFile))
	if _, path, err := contractPathOf(cfgSnapshot()); err == nil {
		same := m.ContractSHA != "" && fileSHA256Hex(path) == m.ContractSHA
		run.ContractChanged = !same
		if !same {
			run.ContractNote = "当前 envkit.yaml 与快照不同；未覆盖，快照副本在 " + run.ContractCopy
		}
	}
	run.Doctor = runContractDoctor(cfgSnapshot(), "")
	auditNow(actUser, "checkpoint_restore", m.ID, run.ContractNote, resOK, run.Error)
	writeJSON(w, run)
}

func boolWord(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func restoreCheckpointDatabase(m CheckpointManifest, d *DatabaseContract, dir string) (ApplyActionResult, error) {
	step := ApplyActionResult{ID: "restore.database", Title: "恢复数据库 " + m.Database.Name}
	dbPath := filepath.Join(dir, filepath.Base(m.Database.File))
	if m.Database.SHA256 != "" && fileSHA256Hex(dbPath) != m.Database.SHA256 {
		step.Msg = "数据库快照校验和不匹配，拒绝恢复"
		return step, fmt.Errorf("%s", step.Msg)
	}
	p, err := doctorDBParams(d)
	if err != nil {
		step.Msg = err.Error()
		return step, err
	}
	exists, err := doctorDBExists(context.Background(), p, m.Database.Name)
	if err != nil {
		step.Msg = "查询数据库失败：" + err.Error()
		return step, err
	}
	var backup string
	if exists {
		backup = filepath.Join(backupDir(), fmt.Sprintf("%s-restore-%s.sql", m.Database.Name, time.Now().Format("20060102-150405")))
		size, sha, err := dumpDatabaseTo(p, m.Database.Name, backup)
		if err != nil {
			step.Msg = "恢复前备份当前数据库失败，已中止：" + err.Error()
			return step, err
		}
		step.Evidence = append(step.Evidence, fmt.Sprintf("backup=%s (%d bytes sha256=%s)", backup, size, sha))
	} else {
		if res := applyCreateDatabase(&DatabaseContract{Name: m.Database.Name, Host: d.Host, Port: d.Port, User: d.User}); res.OK {
			step.Evidence = append(step.Evidence, "已创建缺失的目标库")
		} else {
			step.Msg = res.Msg
			return step, fmt.Errorf("%s", res.Msg)
		}
	}
	// 快照里的空库 dump 只有头部、没有 CREATE TABLE。直接把它导入现有库，
	// 当前库里新加的表不会被删除，恢复就会“看起来成功、实际没回到执行前”。
	// 先清掉当前库里的表/视图，再导入快照；失败时用刚做的备份回填。
	recoverBackup := func() bool {
		if backup == "" {
			return false
		}
		if err := dropDatabaseObjects(p, m.Database.Name); err != nil {
			return false
		}
		return applySQLFileWithParams(backup, p, m.Database.Name, false) == nil
	}
	if err := dropDatabaseObjects(p, m.Database.Name); err != nil {
		step.Msg = "清理当前库对象失败，已中止：" + firstLines(err.Error(), 400)
		step.Artifact = backup
		return step, err
	}
	if err := applySQLFileWithParams(dbPath, p, m.Database.Name, true); err != nil {
		step.Msg = "导入快照数据库失败：" + firstLines(err.Error(), 400)
		if recoverBackup() {
			step.Msg += "；已回滚到恢复前备份"
		}
		step.Artifact = backup
		return step, err
	}
	actual, err := doctorTableNames(context.Background(), p, m.Database.Name)
	if err == nil && len(actual) != m.Database.TableCount {
		step.Msg = fmt.Sprintf("表数量不一致：快照 %d，恢复后 %d", m.Database.TableCount, len(actual))
		if recoverBackup() {
			step.Msg += "；已回滚到恢复前备份"
		}
		step.Artifact = backup
		return step, fmt.Errorf("%s", step.Msg)
	}
	step.OK = true
	step.Msg = "数据库已恢复"
	step.Artifact = backup
	return step, nil
}

// dropDatabaseObjects 清空目标库里的视图和表，让随后的 SQL 导入等价于“替换整个库内容”。
// 顺序必须先是视图再是表：视图依赖表，留着会阻塞表删除。
func dropDatabaseObjects(p mysqlConnParams, dbName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	q := fmt.Sprintf("SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s';",
		strings.ReplaceAll(dbName, "'", "''"))
	_, rows, err := mysqlReadOnlyQuery(ctx, p, "", q)
	if err != nil {
		return err
	}
	var views, tables []string
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(row[1]), "VIEW") {
			views = append(views, row[0])
		} else {
			tables = append(tables, row[0])
		}
	}
	if sql, err := dropObjectsSQL("VIEW", views); err != nil {
		return err
	} else if sql != "" {
		if err := mysqlExec(p, dbName, sql); err != nil {
			return err
		}
	}
	if sql, err := dropObjectsSQL("TABLE", tables); err != nil {
		return err
	} else if sql != "" {
		if err := mysqlExec(p, dbName, sql); err != nil {
			return err
		}
	}
	return nil
}

func dropObjectsSQL(kind string, names []string) (string, error) {
	if len(names) == 0 {
		return "", nil
	}
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		q, err := quoteIdent(n)
		if err != nil {
			return "", err
		}
		quoted = append(quoted, q)
	}
	return "DROP " + kind + " IF EXISTS " + strings.Join(quoted, ", ") + ";", nil
}

// applyRestoredConfig 与 handleConfigRestore 同一条安全路径：硬校验 → 覆盖内存 →
// saveExternalConfig 轮转 → 失效缓存。区别只是配置来自快照目录而不是固定槽位。
func applyRestoredConfig(c Config) error {
	if hard := hardProblems(checkConfig(c)); len(hard) > 0 {
		msgs := []string{}
		for _, p := range hard {
			msgs = append(msgs, p.String())
		}
		return fmt.Errorf("快照配置未通过硬校验：%s", strings.Join(msgs, "；"))
	}
	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()
	saveExternalConfig(c)
	if fi, err := os.Stat(configFilePath()); err != nil || fi.Size() == 0 {
		return fmt.Errorf("配置写入后未落盘：%v", err)
	}
	projectBriefInvalidate()
	runtimeInvalidateCache()
	setCfgStale(true)
	return nil
}

func handleContractCheckpointDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		ID      string `json:"id"`
		Confirm bool   `json:"confirm"`
		Lang    string `json:"lang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), 400)
		return
	}
	dir, err := checkpointPath(strings.TrimSpace(body.ID))
	if err != nil {
		writeJSONStatus(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, err := os.Stat(dir); err != nil {
		writeJSONStatus(w, 404, map[string]any{"ok": false, "error": "快照不存在"})
		return
	}
	if !body.Confirm {
		needConfirm(w, "checkpoint_delete", body.ID, confirmReason(body.Lang,
			"即将删除快照 "+body.ID+"。删除后不能恢复；当前环境不受影响。",
			"About to delete checkpoint "+body.ID+". This cannot be undone; the current environment is not affected."), false)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		writeJSONStatus(w, 500, map[string]any{"ok": false, "error": "删除失败：" + err.Error()})
		return
	}
	auditNow(actUser, "checkpoint_delete", body.ID, "", resOK, "")
	writeJSON(w, map[string]any{"ok": true})
}
