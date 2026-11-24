package main

// Phase 3/4 单测：只覆盖“计划生成”和“快照目录读写/校验”，
// 不碰真实数据库、不启动真实服务；真实链路由手工验收补。
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func applyTestContract() *EnvironmentContract {
	return &EnvironmentContract{
		Version: 1,
		Project: ContractProject{Name: "demo", Root: "."},
		Tools:   map[string]string{"java": ">=17"},
		Services: []Service{
			{ID: "web", Label: "前端", Dir: "web", Adapter: AdapterLocal,
				Probe: ProbeSpec{Kind: ProbeHTTP, URL: "http://127.0.0.1:65001/"}},
			{ID: "backend", Label: "后端", Dir: "server", Adapter: AdapterLocal,
				Probe: ProbeSpec{Kind: ProbeHTTP, URL: "http://127.0.0.1:65002/actuator/health"}},
			{ID: "chain", Label: "链节点", Adapter: AdapterSSHChain,
				Probe: ProbeSpec{Kind: ProbeComposite}},
		},
		Database: &DatabaseContract{Engine: "mysql", Host: "127.0.0.1", Port: 3306, Name: "demo_db", User: "root"},
	}
}

func applyDoctorReport() DoctorReport {
	return DoctorReport{
		ContractPath: `D:\demo\envkit.yaml`, ProjectRoot: `D:\demo`,
		Checks: []DoctorCheck{
			{ID: "database.exists", Group: "database", Title: "数据库存在", Status: doctorDrift, Actual: "不存在"},
			{ID: "database.tables", Group: "database", Title: "数据库表", Status: doctorDrift, Actual: "缺表"},
			{ID: "services.web.probe", Group: "services", Title: "前端服务", Status: doctorDrift, Actual: "端口未监听"},
			{ID: "services.chain.probe", Group: "services", Title: "链节点", Status: doctorDrift, Actual: "端口未监听"},
			{ID: "tools.java", Group: "tools", Title: "工具链 java", Status: doctorDrift, Actual: "11"},
			{ID: "database.schema", Group: "database", Title: "Schema 文件", Status: doctorDrift, Actual: "不存在"},
			{ID: "secrets.mysql_password", Group: "secrets", Title: "密钥", Status: doctorDrift, Actual: "未绑定"},
			{ID: "services.backend.probe", Group: "services", Title: "后端服务", Status: doctorOK, Actual: "通过"},
		},
	}
}

func TestBuildContractApplyPlanOnlyDriftAndBlocksUnsafe(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web"), 0755); err != nil {
		t.Fatal(err)
	}
	testCfg := Config{}
	testCfg.Projects.MySQLHost = "127.0.0.1"
	testCfg.Projects.MySQLUser = "root"
	testCfg.Projects.MySQLPass = "x"
	testCfg.Projects.WebScript = "dev"
	oldCfg := cfgSnapshot()
	cfgMu.Lock()
	cfg = testCfg
	cfgMu.Unlock()
	defer func() { cfgMu.Lock(); cfg = oldCfg; cfgMu.Unlock() }()

	contract := applyTestContract()
	rep := applyDoctorReport()
	rep.ProjectRoot = root
	plan := buildContractApplyPlan(cfgSnapshot(), contract, root, rep)
	if !plan.OK {
		t.Fatalf("plan not ok: %+v", plan)
	}
	if len(plan.Actions) != 7 {
		t.Fatalf("actions=%d, want 7 (每个 drift 都要有交代): %+v", len(plan.Actions), plan.Actions)
	}
	byID := map[string]ApplyAction{}
	for _, a := range plan.Actions {
		byID[a.ID] = a
	}
	if a := byID["apply.database.create"]; !a.Available || a.Risk != applyRiskSafe {
		t.Fatalf("db create = %+v", a)
	}
	if a := byID["apply.database.schema"]; a.Available || a.Risk != applyRiskBlocked || !strings.Contains(a.Reason, "schema") {
		t.Fatalf("schema import should be blocked without schema: %+v", a)
	}
	if a := byID["apply.services.web.start"]; !a.Available || a.NeedsConfirm || a.Risk != applyRiskSafe {
		t.Fatalf("web start = %+v", a)
	}
	if a := byID["apply.services.chain.start"]; a.Available || a.Risk != applyRiskBlocked {
		t.Fatalf("remote chain must not be startable: %+v", a)
	}
	if a := byID["apply.tools.java"]; a.Available || a.Risk != applyRiskBlocked {
		t.Fatalf("tool upgrade must be blocked: %+v", a)
	}
	if a := byID["apply.secrets.mysql_password"]; a.Available || a.Risk != applyRiskBlocked {
		t.Fatalf("secret must be blocked: %+v", a)
	}
}

func TestBuildContractApplyPlanSchemaImportNeedsConfirm(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "schema.sql"), []byte("CREATE TABLE t(id int);"), 0644); err != nil {
		t.Fatal(err)
	}
	contract := applyTestContract()
	contract.Database.Schema = "schema.sql"
	testCfg := Config{}
	testCfg.Projects.MySQLHost = "127.0.0.1"
	testCfg.Projects.MySQLUser = "root"
	testCfg.Projects.MySQLPass = "x"
	testCfg.Projects.WebScript = "dev"
	oldCfg := cfgSnapshot()
	cfgMu.Lock()
	cfg = testCfg
	cfgMu.Unlock()
	defer func() { cfgMu.Lock(); cfg = oldCfg; cfgMu.Unlock() }()
	rep := applyDoctorReport()
	rep.ProjectRoot = root
	plan := buildContractApplyPlan(cfgSnapshot(), contract, root, rep)
	for _, a := range plan.Actions {
		if a.ID == "apply.database.schema" {
			if !a.Available || a.Risk != applyRiskConfirm || !a.NeedsConfirm {
				t.Fatalf("schema action = %+v", a)
			}
			return
		}
	}
	t.Fatal("schema action missing")
}

func TestContractApplyPlanRefusesOffMode(t *testing.T) {
	c := Config{GoProxy: "direct", Projects: Projects{MySQLHost: "127.0.0.1", MySQLUser: "root"}}
	if _, err := planContractApply(c); err == nil || !strings.Contains(err.Error(), "off") {
		t.Fatalf("off mode should be refused, got %v", err)
	}
}

func TestCheckpointCreateListRestoreConfigOnly(t *testing.T) {
	dir := t.TempDir()
	oldCp, oldCfgPath, oldBackup := checkpointDirFn, configFilePath, backupDirFn
	checkpointDirFn = func() string { return filepath.Join(dir, "checkpoints") }
	configFilePath = func() string { return filepath.Join(dir, "config.json") }
	backupDirFn = func() string { return filepath.Join(dir, "backups") }
	defer func() {
		checkpointDirFn, configFilePath, backupDirFn = oldCp, oldCfgPath, oldBackup
	}()

	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(project, "web"), 0755); err != nil {
		t.Fatal(err)
	}
	contractText := `version: 1
project:
  name: demo
  root: "."
services:
  - id: web
    label: 前端
    adapter: local
    risk_tier: low
    dir: web
    probe:
      kind: http
      url: http://127.0.0.1:65001/
database:
  engine: mysql
  host: 127.0.0.1
  name: demo_db
  user: root
`
	if err := os.WriteFile(filepath.Join(project, "envkit.yaml"), []byte(contractText), 0644); err != nil {
		t.Fatal(err)
	}
	c := Config{
		SchemaVersion: configSchemaVersion,
		GoProxy:       "direct",
		Projects:      Projects{MySQLHost: "127.0.0.1", MySQLUser: "root", MySQLPass: "pw", DBName: "demo_db"},
		Contract:      &ContractConfig{Root: project, Path: "envkit.yaml", Mode: contractModeReadOnly},
	}
	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()

	body := strings.NewReader(`{"label":"测试快照","database":false}`)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/contract/checkpoint", body)
	handleContractCheckpointCreate(rr, req)
	if rr.Code != 200 {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		OK         bool           `json:"ok"`
		Checkpoint CheckpointView `json:"checkpoint"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || !validCheckpointID(created.Checkpoint.ID) {
		t.Fatalf("create response=%s", rr.Body.String())
	}
	cpDir := filepath.Join(checkpointDir(), created.Checkpoint.ID)
	if _, err := os.Stat(filepath.Join(cpDir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cpDir, "contract.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cpDir, "config.json")); err != nil {
		t.Fatal(err)
	}

	rr = httptest.NewRecorder()
	handleContractCheckpointList(rr, httptest.NewRequest(http.MethodGet, "/api/contract/checkpoints", nil))
	var listed struct {
		Checkpoints []CheckpointView `json:"checkpoints"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Checkpoints) != 1 || listed.Checkpoints[0].ID != created.Checkpoint.ID {
		t.Fatalf("list=%s", rr.Body.String())
	}

	// 未确认 → 428
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/contract/checkpoint/restore",
		strings.NewReader(`{"id":"`+created.Checkpoint.ID+`","confirm":false}`))
	handleContractCheckpointRestore(rr, req)
	if rr.Code != http.StatusPreconditionRequired {
		t.Fatalf("restore without confirm status=%d body=%s", rr.Code, rr.Body.String())
	}

	// 确认 → 配置恢复，契约文件不被覆盖
	beforeContract, _ := os.ReadFile(filepath.Join(project, "envkit.yaml"))
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/contract/checkpoint/restore",
		strings.NewReader(`{"id":"`+created.Checkpoint.ID+`","confirm":true}`))
	handleContractCheckpointRestore(rr, req)
	if rr.Code != 200 {
		t.Fatalf("restore status=%d body=%s", rr.Code, rr.Body.String())
	}
	var run CheckpointRestoreRun
	if err := json.Unmarshal(rr.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if !run.OK || len(run.Steps) == 0 || !run.Steps[0].OK {
		t.Fatalf("restore run=%s", rr.Body.String())
	}
	afterContract, _ := os.ReadFile(filepath.Join(project, "envkit.yaml"))
	if string(beforeContract) != string(afterContract) {
		t.Fatal("契约文件被快照覆盖了")
	}
	if fi, err := os.Stat(filepath.Join(dir, "config.json")); err != nil || fi.Size() == 0 {
		t.Fatalf("恢复后配置未落盘: %v", err)
	}
}

func TestCheckpointRestoreRejectsTamperedDatabase(t *testing.T) {
	dir := t.TempDir()
	oldCp, oldCfgPath := checkpointDirFn, configFilePath
	checkpointDirFn = func() string { return filepath.Join(dir, "checkpoints") }
	configFilePath = func() string { return filepath.Join(dir, "config.json") }
	defer func() { checkpointDirFn, configFilePath = oldCp, oldCfgPath }()

	project := filepath.Join(dir, "p")
	_ = os.MkdirAll(project, 0755)
	_ = os.WriteFile(filepath.Join(project, "envkit.yaml"), []byte("version: 1\nproject:\n  name: demo\n  root: \".\"\ndatabase:\n  engine: mysql\n  name: demo_db\n  user: root\n"), 0644)
	cfgMu.Lock()
	cfg = Config{
		SchemaVersion: configSchemaVersion, GoProxy: "direct",
		Projects: Projects{MySQLHost: "127.0.0.1", MySQLUser: "root", MySQLPass: "pw"},
		Contract: &ContractConfig{Root: project, Path: "envkit.yaml", Mode: contractModeReadOnly},
	}
	cfgMu.Unlock()

	id := "cp-20261124-120000-abc123"
	cpDir := filepath.Join(checkpointDir(), id)
	_ = os.MkdirAll(cpDir, 0755)
	cfgBytes, _ := json.Marshal(encryptConfig(cfgSnapshot()))
	_ = os.WriteFile(filepath.Join(cpDir, "config.json"), cfgBytes, 0600)
	_ = os.WriteFile(filepath.Join(cpDir, "contract.yaml"), []byte("version: 1\n"), 0644)
	_ = os.WriteFile(filepath.Join(cpDir, "demo_db.sql"), []byte("-- dump"), 0644)
	m := CheckpointManifest{
		Version: 1, ID: id, CreatedAt: "2026-11-24T12:00:00+08:00",
		ContractFile: "contract.yaml", ContractSHA: "x",
		ConfigFile: "config.json", ConfigSHA: fileSHA256Hex(filepath.Join(cpDir, "config.json")),
		Database: CheckpointDatabase{Included: true, Name: "demo_db", File: "demo_db.sql", Size: 7, SHA256: "deadbeef"},
	}
	data, _ := json.Marshal(m)
	_ = os.WriteFile(filepath.Join(cpDir, "manifest.json"), data, 0644)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/contract/checkpoint/restore",
		strings.NewReader(`{"id":"`+id+`","confirm":true}`))
	handleContractCheckpointRestore(rr, req)
	if rr.Code != 409 || !strings.Contains(rr.Body.String(), "校验和") {
		t.Fatalf("tampered db status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCheckpointIDRejectsTraversal(t *testing.T) {
	for _, id := range []string{"../x", "cp-../../x", `cp-..\x`, "cp-2026/../../x"} {
		if validCheckpointID(id) {
			t.Fatalf("id %q should be rejected", id)
		}
	}
}
