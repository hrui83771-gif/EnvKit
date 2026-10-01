// configschema_test.go —— schema 迁移、规范化、校验的单元测试
package main

import (
	"os"
	"strings"
	"testing"
)

func writeFileForTest(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func TestMigrateConfigV0ToV1(t *testing.T) {
	c := Config{
		InstallDir: `C:\EnvKit\tools\`,
		Chain: ChainConfig{
			SSHHost:  "1.2.3.4",
			ChainDir: "/root/fisco/",
		},
	}
	steps := migrateConfig(&c, 0)
	if c.SchemaVersion != configSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", c.SchemaVersion, configSchemaVersion)
	}
	if len(steps) == 0 {
		t.Fatal("期望至少一条迁移记录")
	}
	if c.InstallDir != `C:\EnvKit\tools` {
		t.Fatalf("结尾分隔符未规整：%q", c.InstallDir)
	}
	if c.Chain.ChainDir != "/root/fisco" {
		t.Fatalf("链端目录结尾分隔符未规整：%q", c.Chain.ChainDir)
	}
	if c.Chain.ChainHost != "1.2.3.4" {
		t.Fatalf("chain_host 未从 ssh_host 补齐：%q", c.Chain.ChainHost)
	}
}

func TestMigrateConfigAlreadyCurrent(t *testing.T) {
	c := Config{SchemaVersion: configSchemaVersion, InstallDir: "relative\\path\\"}
	if steps := migrateConfig(&c, c.SchemaVersion); len(steps) != 0 {
		t.Fatalf("当前版本不应触发迁移，得到 %v", steps)
	}
	if c.SchemaVersion != configSchemaVersion {
		t.Fatalf("版本被改动：%d", c.SchemaVersion)
	}
}

func TestMigrateConfigFromNewer(t *testing.T) {
	// 用户回退版本后配置比程序新：不允许降级写回
	c := Config{SchemaVersion: 99}
	if steps := migrateConfig(&c, 99); len(steps) != 0 {
		t.Fatalf("未来版本不应执行迁移：%v", steps)
	}
	if c.SchemaVersion != 99 {
		t.Fatalf("未来版本号被改动：%d", c.SchemaVersion)
	}
}

func TestNormalizeConfig(t *testing.T) {
	c := Config{
		Proxy:    Proxy{HTTP: "127.0.0.1:7890"},
		Chain:    ChainConfig{SSHHost: " host ", ChainGuardSecs: 5},
		AI:       &AIConfig{BaseURL: "https://api.x.com/", Temperature: 5, APIKey: " k "},
		Projects: Projects{FrontendDir: `D:\proj\web\`},
	}
	normalizeConfig(&c)
	if c.Proxy.HTTP != "http://127.0.0.1:7890" {
		t.Fatalf("代理未补 scheme：%q", c.Proxy.HTTP)
	}
	if c.Chain.SSHHost != "host" {
		t.Fatalf("ssh_host 未去空格：%q", c.Chain.SSHHost)
	}
	if c.Chain.ChainGuardSecs != 30 {
		t.Fatalf("守护间隔未夹到下限：%d", c.Chain.ChainGuardSecs)
	}
	if c.AI.BaseURL != "https://api.x.com" {
		t.Fatalf("BaseURL 结尾斜杠未去：%q", c.AI.BaseURL)
	}
	if c.AI.Temperature != 2 {
		t.Fatalf("温度未夹到上限：%v", c.AI.Temperature)
	}
	if c.AI.APIKey != "k" {
		t.Fatalf("api_key 未去空格：%q", c.AI.APIKey)
	}
	if c.Projects.FrontendDir != `D:\proj\web` {
		t.Fatalf("前端目录未规整：%q", c.Projects.FrontendDir)
	}
}

func TestCheckConfigHard(t *testing.T) {
	c := Config{
		InstallDir: "relative\\dir", // 硬：非绝对路径
		GoProxy:    "https://goproxy.cn,direct",
		Projects: Projects{
			MySQLHost: "127.0.0.1",
			MySQLUser: "root",
			MySQLPort: 70000, // 硬：端口越界
			DBName:    "bad name",
		},
		NpmRegistry: "mirrors.tencent.com/npm/", // 硬：无 scheme
		Components: []Component{
			{Name: "go", URL: "https://go.dev/dl/go.zip", SHA256: "abc"}, // 硬：sha256 格式
		},
		Chain: ChainConfig{ChainGuard: true, SSHHost: "1.2.3.4"}, // 硬：守护开启没填用户/凭据
	}
	hard := hardProblems(checkConfig(c))
	if len(hard) == 0 {
		t.Fatal("期望发现硬错误")
	}
	joined := ""
	for _, p := range hard {
		joined += p.Field + ";"
	}
	// 注：SSH 主机填了所以只触发"用户/凭据"两条；主机为空的分支由 TestCheckConfigHard 的
	// ChainGuard 分支设计覆盖（凭据与用户检查同源）。
	for _, want := range []string{"安装目录", "数据库端口", "数据库名", "npm 源", "SHA256", "SSH 用户", "SSH 凭据"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少硬错误：%s（实际 %s）", want, joined)
		}
	}
}

func TestCheckConfigSoftDirNotExists(t *testing.T) {
	dir := t.TempDir()
	c := Config{
		GoProxy:  "direct",
		Projects: Projects{MySQLHost: "127.0.0.1", MySQLUser: "root", FrontendDir: dir},
	}
	hard := hardProblems(checkConfig(c))
	if len(hard) != 0 {
		t.Fatalf("合法配置不应有硬错误：%v", hard)
	}
	// 不存在的目录 → 只有软提示
	c2 := c
	c2.Projects.FrontendDir = `Z:\no\such\dir`
	soft := checkConfig(c2)
	found := false
	for _, p := range soft {
		if !p.Hard && strings.Contains(p.Field, "前端目录") {
			found = true
		}
		if p.Hard {
			t.Fatalf("不存在的目录不应是硬错误：%v", p)
		}
	}
	if !found {
		t.Fatal("缺少对不存在目录的软提示")
	}
}

func TestCheckConfigGuardOK(t *testing.T) {
	c := Config{
		GoProxy:  "direct",
		Projects: Projects{MySQLHost: "127.0.0.1", MySQLUser: "root"},
		Chain: ChainConfig{
			ChainGuard:     true,
			SSHHost:        "1.2.3.4",
			SSHUser:        "root",
			SSHPassword:    "x",
			ChainGuardSecs: 60,
			ChainDir:       "/root/fisco/nodes/127.0.0.1", // 远程 Linux 路径必须被判为绝对路径
			WebaseDir:      "/root/webase-front",
		},
	}
	if hard := hardProblems(checkConfig(c)); len(hard) != 0 {
		t.Fatalf("守护配置齐全时不应有硬错误：%v", hard)
	}
}

func TestLoadConfigFileFallback(t *testing.T) {
	// 正本损坏 → 回退 .bak
	dir := t.TempDir()
	base := dir + "\\config.json"
	_ = writeFileForTest(base, "{ broken json")
	_ = writeFileForTest(base+".bak", `{"schema_version":1,"app_name":"from-bak"}`)
	data, src, err := loadConfigFile(base)
	if err != nil {
		t.Fatalf("回退失败：%v", err)
	}
	if src != "config.json.bak" {
		t.Fatalf("来源 = %s, want config.json.bak", src)
	}
	if !strings.Contains(string(data), "from-bak") {
		t.Fatal("回退读到的内容不对")
	}
	// 全部损坏 → 报错
	_ = writeFileForTest(base+".bak", "nope")
	if _, _, err := loadConfigFile(base); err == nil {
		t.Fatal("全部候选都坏时应返回错误")
	}
}
