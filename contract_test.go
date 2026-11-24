package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseContractValid(t *testing.T) {
	src := `
version: 1
project:
  name: tcm-inventory
  root: .
tools:
  java: ">=17"
  node: ">=20"
services:
  - id: backend
    label: 后端服务
    adapter: local
    risk_tier: low
    dir: server
    start: mvn spring-boot:run
    probe:
      kind: http
      port: 8080
      url: http://127.0.0.1:8080/actuator/health
      timeout: 45
    verify:
      target: backend
      evidence: [pid, port, http_code]
database:
  engine: mysql
  host: 127.0.0.1
  port: 3306
  name: tcm_inventory
  user: root
  schema: server/src/main/resources/db/schema.sql
secrets:
  mysql_password: env:ENVKIT_MYSQL_PASSWORD
`
	c, problems, err := parseContractYAML([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("合法契约不应有问题：%v", problems)
	}
	if c.Version != 1 || c.Project.Name != "tcm-inventory" || c.Project.Root != "." {
		t.Fatalf("项目字段解析错误：%+v", c.Project)
	}
	if len(c.Services) != 1 || c.Services[0].ID != "backend" || c.Services[0].Dir != "server" {
		t.Fatalf("服务字段解析错误：%+v", c.Services)
	}
	if c.Database == nil || c.Database.Name != "tcm_inventory" {
		t.Fatalf("数据库字段解析错误：%+v", c.Database)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	again, problems, err := parseContractYAML(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("round-trip 后有问题：%v", problems)
	}
	if len(again.Services) != 1 || again.Services[0].Probe.URL == "" {
		t.Fatalf("round-trip 丢了服务字段：%+v", again.Services)
	}
}

func TestContractRejectsUnsafePathsAndSecrets(t *testing.T) {
	src := `
version: 1
project:
  name: x
  root: .
services:
  - id: backend
    label: 后端
    adapter: local
    risk_tier: low
    dir: C:\Users\me\project
    probe:
      kind: port
      port: 8080
    verify:
      target: backend
database:
  engine: mysql
  name: x
  schema: ..\outside.sql
secrets:
  mysql_password: root123
`
	_, problems, err := parseContractYAML([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"必须是项目内的相对路径", "不能把明文密码写进契约"} {
		if !strings.Contains(joined, want) {
			t.Errorf("问题里缺少 %q：\n%s", want, joined)
		}
	}
}

func TestContractRejectsUnknownField(t *testing.T) {
	_, _, err := parseContractYAML([]byte("version: 1\nproject:\n  name: x\n  root: .\nunknown_field: true\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("未知字段必须报错，得到 %v", err)
	}
}

func TestContractWriteAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, contractFileName)
	c := &EnvironmentContract{
		Version: contractVersion,
		Project: ContractProject{Name: "x", Root: "."},
		Services: []Service{{
			ID: "web", Label: "前端", Adapter: AdapterLocal, RiskTier: RiskLow,
			Dir: "web", Start: "npm run dev",
			Probe:  ProbeSpec{Kind: ProbePort, Port: 5173},
			Verify: VerifySpec{Target: "web", Evidence: []string{"pid", "port"}},
		}},
	}
	if err := writeContractFile(path, c); err != nil {
		t.Fatal(err)
	}
	got, problems, err := loadContractFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("写回后校验失败：%v", problems)
	}
	if len(got.Services) != 1 || got.Services[0].Start != "npm run dev" {
		t.Fatalf("读回不完整：%+v", got.Services)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatal("原子写留下了 .tmp")
	}
}

func TestBuildContractFromConfig(t *testing.T) {
	root := t.TempDir()
	front := filepath.Join(root, "web")
	back := filepath.Join(root, "server")
	writeFile(t, front, "package.json", `{"name":"web","scripts":{"dev":"vite"},"dependencies":{"vite":"latest"},"engines":{"node":">=20.0.0"}}`)
	writeFile(t, front, "vite.config.js", "export default { server: { port: 5173 } }\n")
	writeFile(t, back, "pom.xml", `<project><properties><java.version>17</java.version></properties><dependencies><dependency><artifactId>spring-boot-starter-web</artifactId></dependency></dependencies></project>`)
	writeFile(t, back, "application.yml", "server:\n  port: 8080\nspring:\n  datasource:\n    url: jdbc:mysql://localhost:3306/tcm_inventory\n    username: root\n")
	outsideSQL := filepath.Join(t.TempDir(), "blood.sql")
	writeFile(t, filepath.Dir(outsideSQL), filepath.Base(outsideSQL), "CREATE TABLE t (id int);")

	c := Config{Projects: Projects{
		FrontendDir: front, BackendDir: back,
		MySQLHost: "localhost", MySQLPort: 3306, MySQLUser: "root",
		DBName: "tcm_inventory", MySQLPass: "secret", SQLFile: outsideSQL,
	}}
	build, err := buildContractFromConfig(c, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if build.Contract.Project.Root != "." || build.Contract.Project.Name != filepath.Base(root) {
		t.Fatalf("项目根错误：%+v", build.Contract.Project)
	}
	if len(build.Contract.Services) < 2 {
		t.Fatalf("应生成前端和后端服务：%+v", build.Contract.Services)
	}
	var web, backend *Service
	for i := range build.Contract.Services {
		switch build.Contract.Services[i].ID {
		case "web":
			web = &build.Contract.Services[i]
		case "backend":
			backend = &build.Contract.Services[i]
		}
	}
	if web == nil || web.Dir != "web" || web.Start != "npm run dev" || web.Probe.Port != 5173 {
		t.Fatalf("前端服务不对：%+v", web)
	}
	if backend == nil || backend.Dir != "server" || backend.Probe.Port != 8080 {
		t.Fatalf("后端服务不对：%+v", backend)
	}
	if !strings.Contains(backend.Probe.URL, "/actuator/health") {
		t.Fatalf("Spring 后端应使用 actuator 健康路径：%+v", backend.Probe)
	}
	if build.Contract.Tools["node"] != ">=20" || build.Contract.Tools["java"] != ">=17" {
		t.Fatalf("工具约束不对：%+v", build.Contract.Tools)
	}
	if build.Contract.Database == nil || build.Contract.Database.Name != "tcm_inventory" {
		t.Fatalf("数据库契约不对：%+v", build.Contract.Database)
	}
	if build.Contract.Database.Schema != "" {
		t.Fatalf("项目根外的 SQL 不应写进契约：%q", build.Contract.Database.Schema)
	}
	if len(build.Warnings) == 0 || !strings.Contains(strings.Join(build.Warnings, "\n"), "SQL 文件不在项目根内") {
		t.Fatalf("应给出 SQL 路径警告：%v", build.Warnings)
	}
	if _, ok := build.Contract.Secrets["mysql_password"]; !ok {
		t.Fatalf("有密码时应生成密钥引用：%+v", build.Contract.Secrets)
	}
}

func TestProjectRootFromDirs(t *testing.T) {
	root := filepath.Clean(`D:\BCGD\2027\tcm-inventory`)
	front := filepath.Join(root, "web")
	back := filepath.Join(root, "server")
	got, err := projectRootFromDirs(front, back)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) != root {
		t.Fatalf("root=%s，期望 %s", got, root)
	}
	if got, err := projectRootFromDirs("", back); err != nil || got != back {
		t.Fatalf("单目录应以该目录为根：%q %v", got, err)
	}
}

func TestContractConfigMigrationAndClone(t *testing.T) {
	c := Config{SchemaVersion: 1, Contract: &ContractConfig{Root: `D:\x`, Path: "envkit.yaml", Mode: "readonly"}}
	steps := migrateConfig(&c, 1)
	if c.SchemaVersion != configSchemaVersion || len(steps) == 0 {
		t.Fatalf("迁移未执行：%d %v", c.SchemaVersion, steps)
	}
	if c.Contract != nil {
		t.Fatal("v1→v2 迁移应把契约段置空，保持老配置行为不变")
	}

	orig := Config{Contract: &ContractConfig{Root: `D:\x`, Path: "envkit.yaml", Mode: "readonly"}}
	clone := cloneConfigDeep(orig)
	clone.Contract.Mode = "enforce"
	if orig.Contract.Mode != "readonly" {
		t.Fatal("cloneConfigDeep 没有隔离契约指针")
	}
}
