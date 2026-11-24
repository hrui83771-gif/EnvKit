package main

// contract_legacy.go —— 从旧 config.json / 项目文件生成 envkit.yaml
//
// 这不是"重新识别项目"，而是把 3.x 已经算出来的结果收口：
//   - 服务来自 projectsetup 的 setupService（启动命令、端口、健康路径）；
//   - 数据库来自 Projects；
//   - 工具约束来自 go.mod / package.json / pom.xml 的声明。
// 生成结果只是候选，必须由用户确认后才写项目文件。

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type contractBuild struct {
	Contract *EnvironmentContract `json:"contract"`
	Root     string               `json:"root"`
	Path     string               `json:"path"`
	Services []Service            `json:"-"`
	Warnings []string             `json:"warnings,omitempty"`
}

// buildContractFromConfig 生成契约候选；front/back 为空时用当前配置。
func buildContractFromConfig(c Config, front, back string) (*contractBuild, error) {
	if strings.TrimSpace(front) == "" {
		front = c.Projects.FrontendDir
	}
	if strings.TrimSpace(back) == "" {
		back = c.Projects.BackendDir
	}
	root, err := projectRootFromDirs(front, back)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	res := &contractBuild{
		Root: root,
		Path: filepath.Join(root, contractFileName),
		Contract: &EnvironmentContract{
			Version: contractVersion,
			Project: ContractProject{Name: filepath.Base(root), Root: "."},
			Tools:   map[string]string{},
		},
	}

	ins := inspectProjectSetup(front, back)
	if svc, ok := contractServiceFromSetup(ins.Frontend, "web", root); ok {
		res.Services = append(res.Services, svc)
	}
	if svc, ok := contractServiceFromSetup(ins.Backend, "backend", root); ok {
		res.Services = append(res.Services, svc)
	}

	// 数据库：config 是机器侧当前值；契约只记录期望，不写密码。
	if strings.TrimSpace(c.Projects.DBName) != "" {
		db := &DatabaseContract{
			Engine: "mysql",
			Host:   firstNonEmpty(c.Projects.MySQLHost, "127.0.0.1"),
			Port:   c.Projects.MySQLPort,
			Name:   c.Projects.DBName,
			User:   c.Projects.MySQLUser,
		}
		if db.Port == 0 {
			db.Port = 3306
		}
		if sqlFile := strings.TrimSpace(c.Projects.SQLFile); sqlFile != "" {
			abs, err := filepath.Abs(sqlFile)
			if err == nil {
				if rel, relErr := contractRelPath(root, abs); relErr == nil {
					db.Schema = rel
				} else {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("SQL 文件不在项目根内，未写入契约：%s", sqlFile))
				}
			}
		}
		if strings.TrimSpace(c.Projects.MySQLPass) != "" {
			res.Contract.Secrets = map[string]string{"mysql_password": "env:ENVKIT_MYSQL_PASSWORD"}
			res.Warnings = append(res.Warnings,
				"数据库密码仍只在本机 DPAPI 保存；契约记录 env:ENVKIT_MYSQL_PASSWORD 引用，不写明文")
		}
		res.Contract.Database = db
	}

	// 链端/WeBASE 是远程服务，沿用 topology 的推导；只取 chain/webase，
	// 避免把 web/backend/mysql 再复制一遍。
	for _, svc := range servicesFromLegacy(c) {
		if svc.ID == "chain" || svc.ID == "webase" {
			res.Services = append(res.Services, svc)
		}
	}

	// 依赖关系只表达"启动顺序上的期望"，不改变 3.x 的实际启动行为。
	for i := range res.Services {
		switch res.Services[i].ID {
		case "backend":
			if res.Contract.Database != nil {
				res.Services[i].DependsOn = []string{"mysql"}
			}
		case "web":
			if serviceInList(res.Services, "backend") {
				res.Services[i].DependsOn = []string{"backend"}
			}
		}
	}

	res.Contract.Services = res.Services
	res.Contract.Tools = contractToolsFromDirs(front, back)
	if len(res.Contract.Tools) == 0 {
		res.Contract.Tools = nil
	}
	return res, nil
}

func contractServiceFromSetup(s setupService, target, root string) (Service, bool) {
	if !s.Provided || !s.Valid || strings.TrimSpace(s.Dir) == "" {
		return Service{}, false
	}
	rel, err := contractRelPath(root, s.Dir)
	if err != nil {
		return Service{}, false
	}
	port := 0
	if len(s.Ports) > 0 {
		port = s.Ports[0].Port
	}
	svc := Service{
		ID:       target,
		Label:    contractServiceLabel(target),
		Adapter:  AdapterLocal,
		RiskTier: RiskLow,
		Dir:      rel,
		Start:    strings.TrimSpace(s.Launch),
		Probe:    ProbeSpec{Kind: ProbePort, Port: port, Timeout: 45},
		Verify:   VerifySpec{Target: target},
	}
	if target == "backend" {
		svc.Verify.Evidence = []string{"pid", "port", "http_code"}
	} else {
		svc.Verify.Evidence = []string{"pid", "port"}
	}
	// 有端口时优先用 HTTP 探针；health_path 来自项目画像（Spring 会带
	// /actuator/health，前端通常是 /）。没有端口就保留 port 探针，Doctor
	// 会把它判成"未声明"，而不是猜一个端口。
	if port > 0 {
		healthPath := "/"
		if target == "backend" && backendIsSpring(s.Dir) {
			healthPath = "/actuator/health"
		}
		svc.Probe = ProbeSpec{
			Kind:    ProbeHTTP,
			Port:    port,
			URL:     "http://127.0.0.1:" + strconv.Itoa(port) + healthPath,
			Timeout: 45,
		}
	}
	return svc, true
}

func contractServiceLabel(target string) string {
	if target == "web" {
		return "前端服务"
	}
	return "后端服务"
}

func backendIsSpring(dir string) bool {
	if hasFile(filepath.Join(dir, "pom.xml")) {
		return true
	}
	if b, err := os.ReadFile(filepath.Join(dir, "pom.xml")); err == nil {
		return strings.Contains(string(b), "spring-boot")
	}
	return false
}

func serviceInList(svcs []Service, id string) bool {
	for _, s := range svcs {
		if s.ID == id {
			return true
		}
	}
	return false
}

// contractToolsFromDirs 只记录项目自己声明的最低版本；没声明就不写。
// 这正是 envreq 的"未声明 ≠ 符合"口径：契约不替项目猜版本。
func contractToolsFromDirs(front, back string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(back) != "" {
		if b, err := os.ReadFile(filepath.Join(back, "go.mod")); err == nil {
			if m := goModGoRe.FindStringSubmatch(string(b)); len(m) > 1 {
				out["go"] = ">=" + m[1]
			}
		}
		if b, err := os.ReadFile(filepath.Join(back, "pom.xml")); err == nil {
			if v := pomJavaDeclared(string(b)); v != "" {
				v = javaModernForm(normalizeJavaVer(v))
				out["java"] = ">=" + trimVersionZeros(v)
			}
		}
	}
	if strings.TrimSpace(front) != "" {
		if b, err := os.ReadFile(filepath.Join(front, "package.json")); err == nil {
			if v := parseEnginesNode(string(b)); v != "" {
				out["node"] = ">=" + trimVersionZeros(v)
			}
		}
	}
	return out
}

// trimVersionZeros 把 20.0.0 / 20.0 收敛成 20；只用于人类可读的约束，
// 不参与版本比较。Doctor 仍然用 compareReq 做真实比较。
func trimVersionZeros(v string) string {
	v = strings.TrimSpace(v)
	for strings.HasSuffix(v, ".0") {
		v = strings.TrimSuffix(v, ".0")
	}
	return v
}
