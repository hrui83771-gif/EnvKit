// configschema.go —— 配置 schema 版本、迁移链、规范化与校验
//
// 为什么需要这个文件：config.json 一旦写坏（字段改名、目录不存在、端口越界、
// sha256 抄错一位、URL 少了 http://），问题会在几十分钟后才以"安装失败/连不上库"
// 的形式暴露，排查成本极高。这里把三类保护集中起来：
//  1. schema_version —— 结构变更可追溯，老配置有迁移路径，不再靠"字段为空就补默认"硬扛；
//  2. normalizeConfig —— 保存前把可自动修正的脏值修掉（结尾分隔符、空格、越界间隔）；
//  3. checkConfig —— 保存前拦截"必然导致后续失败"的硬错误（端口越界、URL 无 scheme、
//     非绝对路径、sha256 格式错误、守护开启却没填主机），并在界面上直接告诉用户改哪个框。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// configSchemaVersion 当前配置结构版本。
// 约定：任何"字段改名 / 语义变化 / 默认值变化"都必须 +1，并在 configMigrations 补一条迁移；
// 否则老配置文件会被静默按新语义解析（这类 bug 不会报错，只会算错）。
const configSchemaVersion = 1

// ---------- 迁移 ----------

type configMigration struct {
	from  int
	desc  string
	apply func(c *Config)
}

// configMigrations 迁移链：from 表示源版本，apply 负责把配置推进到 from+1。
// 新增迁移时只允许"追加"到列表末尾，不要改已有条目（历史配置可能还停在任意版本）。
var configMigrations = []configMigration{
	{
		from: 0,
		desc: "引入 schema_version；规整路径结尾分隔符、补齐 chain_host",
		apply: func(c *Config) {
			c.InstallDir = trimTrailingSep(c.InstallDir)
			c.Projects.FrontendDir = trimTrailingSep(c.Projects.FrontendDir)
			c.Projects.BackendDir = trimTrailingSep(c.Projects.BackendDir)
			c.Projects.BackupDir = trimTrailingSep(c.Projects.BackupDir)
			c.Projects.SQLFile = strings.TrimSpace(c.Projects.SQLFile)
			c.Chain.ChainDir = trimTrailingSep(c.Chain.ChainDir)
			c.Chain.WebaseDir = trimTrailingSep(c.Chain.WebaseDir)
			// 历史配置里 chain_host 可能为空（早期版本没有这个字段），补成 ssh_host
			if strings.TrimSpace(c.Chain.ChainHost) == "" {
				c.Chain.ChainHost = strings.TrimSpace(c.Chain.SSHHost)
			}
		},
	},
}

// migrateConfig 把配置从 from 版本逐级推到最新版本，返回执行过的迁移描述（供日志）。
// 若 from 比本程序还新（用户回退了版本），不做任何改动 —— 降级写回只会造成数据丢失。
func migrateConfig(c *Config, from int) []string {
	if from >= configSchemaVersion {
		c.SchemaVersion = from // 比本程序新：原样保留版本号，不假装自己懂它
		return nil
	}
	cur := from
	var done []string
	for _, m := range configMigrations {
		if m.from < cur {
			continue // 该迁移在当前版本之下，早已应用过
		}
		if m.from >= configSchemaVersion {
			break
		}
		m.apply(c)
		cur = m.from + 1
		done = append(done, fmt.Sprintf("v%d→v%d：%s", m.from, cur, m.desc))
	}
	c.SchemaVersion = configSchemaVersion
	if cur < configSchemaVersion {
		done = append(done, fmt.Sprintf("v%d→v%d：无显式迁移，仅标记版本", cur, configSchemaVersion))
	}
	return done
}

// ---------- 规范化 ----------

// trimTrailingSep 去掉路径结尾的分隔符，但保留 `C:\` 这类只有盘符根的形式。
func trimTrailingSep(p string) string {
	p = strings.TrimSpace(p)
	for len(p) > 3 && (strings.HasSuffix(p, `\`) || strings.HasSuffix(p, "/")) {
		p = p[:len(p)-1]
	}
	return p
}

// normalizeConfig 就地修正"可以自动改对"的脏值。只在保存/加载时调用，保证内存与磁盘一致。
func normalizeConfig(c *Config) {
	c.AppName = strings.TrimSpace(c.AppName)
	c.AppTitle = strings.TrimSpace(c.AppTitle)
	c.InstallDir = trimTrailingSep(c.InstallDir)
	c.GoProxy = strings.TrimSpace(c.GoProxy)
	c.GoSumDB = strings.TrimSpace(c.GoSumDB)
	c.GoCGO = strings.TrimSpace(c.GoCGO)
	c.NpmRegistry = strings.TrimSpace(c.NpmRegistry)
	// 代理地址容错：用户常贴 "127.0.0.1:7890"（无 scheme）
	c.Proxy.HTTP = normalizeProxy(c.Proxy.HTTP)
	c.Proxy.HTTPS = normalizeProxy(c.Proxy.HTTPS)

	p := &c.Projects
	p.MySQLHost = strings.TrimSpace(p.MySQLHost)
	p.MySQLUser = strings.TrimSpace(p.MySQLUser)
	p.DBName = strings.TrimSpace(p.DBName)
	p.FrontendDir = trimTrailingSep(p.FrontendDir)
	p.BackendDir = trimTrailingSep(p.BackendDir)
	p.BackupDir = trimTrailingSep(p.BackupDir)
	p.SQLFile = strings.TrimSpace(p.SQLFile)

	ch := &c.Chain
	ch.SSHHost = strings.TrimSpace(ch.SSHHost)
	ch.SSHUser = strings.TrimSpace(ch.SSHUser)
	ch.SSHKey = strings.TrimSpace(ch.SSHKey)
	ch.ChainHost = strings.TrimSpace(ch.ChainHost)
	ch.ChainDir = trimTrailingSep(ch.ChainDir)
	ch.WebaseDir = trimTrailingSep(ch.WebaseDir)
	ch.SSHHostKey = strings.TrimSpace(ch.SSHHostKey)
	// 守护间隔：填了值就必须 ≥30s，否则等于对远程主机做压力测试
	if ch.ChainGuardSecs != 0 && ch.ChainGuardSecs < 30 {
		ch.ChainGuardSecs = 30
	}

	if c.AI != nil {
		c.AI.BaseURL = strings.TrimRight(strings.TrimSpace(c.AI.BaseURL), "/")
		c.AI.APIKey = strings.TrimSpace(c.AI.APIKey)
		c.AI.Model = strings.TrimSpace(c.AI.Model)
		if c.AI.Temperature < 0 {
			c.AI.Temperature = 0
		}
		if c.AI.Temperature > 2 {
			c.AI.Temperature = 2
		}
	}

	for i := range c.Components {
		c.Components[i].URL = strings.TrimSpace(c.Components[i].URL)
		c.Components[i].SHA256 = strings.ToLower(strings.TrimSpace(c.Components[i].SHA256))
	}
}

// normalizeProxy 给裸 host:port 补上 http://（系统代理字段不接受无 scheme 的地址）。
func normalizeProxy(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.Contains(v, "://") {
		return v
	}
	return "http://" + v
}

// ---------- 校验 ----------

// configProblem 一条配置问题。Hard=true 表示"必然导致后续失败"，保存接口会拒绝；
// Hard=false 只是提示（目录还没建、SQL 还没选都属于正常中间态）。
type configProblem struct {
	Field string `json:"field"`
	Msg   string `json:"msg"`
	Hard  bool   `json:"hard"`
}

func (p configProblem) String() string { return p.Field + " → " + p.Msg }

var (
	reDBSchemaName = regexp.MustCompile(`^[A-Za-z0-9_$]+$`)
	reSHA256       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func hasURLScheme(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// checkConfig 返回全部配置问题（含硬/软两类）。调用方自行决定是拒绝保存还是仅告警。
func checkConfig(c Config) []configProblem {
	var ps []configProblem
	hard := func(field, format string, a ...any) {
		ps = append(ps, configProblem{Field: field, Msg: fmt.Sprintf(format, a...), Hard: true})
	}
	soft := func(field, format string, a ...any) {
		ps = append(ps, configProblem{Field: field, Msg: fmt.Sprintf(format, a...)})
	}

	// 端口：0 视为"未设置"（加载时会补默认值），越界一律硬错误
	chkPort := func(field string, v int) {
		if v != 0 && (v < 1 || v > 65535) {
			hard(field, "端口需在 1-65535，当前 %d", v)
		}
	}
	// 目录：非空时必须绝对路径（相对路径会让子进程的工作目录变成随机值）
	chkDir := func(field, p string) {
		if p == "" {
			return
		}
		if !filepath.IsAbs(p) {
			hard(field, "必须是绝对路径，当前「%s」", p)
			return
		}
		if fi, err := os.Stat(p); err != nil {
			soft(field, "目录当前不存在（安装/创建后即恢复）：%s", p)
		} else if !fi.IsDir() {
			hard(field, "该路径是一个文件，不是目录：%s", p)
		}
	}
	chkFile := func(field, p string) {
		if p == "" {
			return
		}
		if !filepath.IsAbs(p) {
			hard(field, "必须是绝对路径，当前「%s」", p)
			return
		}
		if _, err := os.Stat(p); err != nil {
			soft(field, "文件当前不存在：%s", p)
		}
	}
	// 远程目录（链端/WeBASE 在 SSH 服务器上，是 Linux 路径）：`/...` 即为绝对路径，
	// 不能用 Windows 的 filepath.IsAbs 判断（会把 /root/... 误判为相对路径）。
	chkRemoteDir := func(field, p string) {
		if p == "" {
			return
		}
		if !strings.HasPrefix(p, "/") && !filepath.IsAbs(p) {
			hard(field, "必须是绝对路径（远程 Linux 路径以 / 开头，本机路径带盘符），当前「%s」", p)
		}
	}
	chkURL := func(field, u string, required bool) {
		if u == "" {
			if required {
				hard(field, "不能为空")
			}
			return
		}
		if !hasURLScheme(u) {
			hard(field, "地址必须以 http:// 或 https:// 开头，当前「%s」", u)
		}
	}

	chkDir("安装目录", c.InstallDir)
	chkURL("npm 源", c.NpmRegistry, false)
	if strings.TrimSpace(c.GoProxy) == "" {
		hard("Go 代理", "不能为空（离线环境请填 direct）")
	}

	// MySQL
	if strings.TrimSpace(c.MySQL.ServiceName) == "" {
		soft("MySQL 服务名", "为空时无法重启已装好的 MySQL 服务")
	}
	if c.MySQL.Port != 0 && (c.MySQL.Port < 1 || c.MySQL.Port > 65535) {
		hard("MySQL 端口", "端口需在 1-65535，当前 %d", c.MySQL.Port)
	}

	// 项目
	if c.Projects.MySQLHost == "" {
		hard("数据库主机", "不能为空")
	}
	if c.Projects.MySQLUser == "" {
		hard("数据库用户", "不能为空")
	}
	chkPort("数据库端口", c.Projects.MySQLPort)
	if c.Projects.DBName != "" && !reDBSchemaName.MatchString(c.Projects.DBName) {
		hard("数据库名", "只允许字母/数字/下划线/美元符，当前「%s」", c.Projects.DBName)
	}
	chkDir("前端目录", c.Projects.FrontendDir)
	chkDir("后端目录", c.Projects.BackendDir)
	chkDir("备份目录", c.Projects.BackupDir)
	chkFile("SQL 文件", c.Projects.SQLFile)
	if c.Projects.BackupDir != "" && c.Projects.SQLFile != "" &&
		filepath.Dir(c.Projects.SQLFile) == c.Projects.BackupDir {
		soft("备份目录", "与 SQL 文件目录相同，备份产物会落进源码目录（建议改到独立目录）")
	}

	// 链端
	chkPort("SSH 端口", c.Chain.SSHPort)
	chkPort("链端 Channel 端口", c.Chain.ChainPort)
	chkPort("WeBASE-Front 端口", c.Chain.WebasePort)
	if c.Chain.GroupID < 0 {
		hard("群组 ID", "不能为负数")
	}
	chkRemoteDir("链端目录", c.Chain.ChainDir)
	chkRemoteDir("WeBASE 目录", c.Chain.WebaseDir)
	if c.Chain.ChainGuard || c.Chain.ChainAutoRecover {
		if strings.TrimSpace(c.Chain.SSHHost) == "" {
			hard("SSH 主机", "已开启链端守护/自动恢复，必须填写 SSH 主机")
		} else if strings.TrimSpace(c.Chain.SSHUser) == "" {
			hard("SSH 用户", "已开启链端守护/自动恢复，必须填写 SSH 用户")
		}
		if strings.TrimSpace(c.Chain.SSHPassword) == "" && strings.TrimSpace(c.Chain.SSHKey) == "" {
			hard("SSH 凭据", "已开启链端守护/自动恢复，必须填密码或私钥内容")
		}
	}
	if c.Chain.SSHHostKey != "" && !strings.HasPrefix(c.Chain.SSHHostKey, "SHA256:") {
		hard("SSH 主机指纹", "格式应为 SHA256:... ，当前「%s」", c.Chain.SSHHostKey)
	}

	// 组件
	for i, comp := range c.Components {
		name := comp.Name
		if name == "" {
			name = fmt.Sprintf("第 %d 项", i+1)
		}
		chkURL("组件「"+name+"」下载地址", comp.URL, false)
		if comp.SHA256 != "" && !reSHA256.MatchString(comp.SHA256) {
			hard("组件「"+name+"」SHA256", "需为 64 位十六进制，当前「%s」（写错会导致下载后校验永久失败）", comp.SHA256)
		}
	}

	// AI
	if c.AI != nil {
		chkURL("AI 接口地址", c.AI.BaseURL, false)
		if c.AI.Enabled {
			if c.AI.APIKey == "" {
				hard("AI API Key", "已启用 AI 助手，必须填写 API Key")
			}
			if c.AI.Model == "" {
				hard("AI 模型", "已启用 AI 助手，必须填写模型名")
			}
			if c.AI.BaseURL == "" {
				hard("AI 接口地址", "已启用 AI 助手，必须填写接口地址")
			}
		}
		if c.AI.MaxTokens < 0 {
			hard("AI 最大 Tokens", "不能为负数")
		}
	}
	return ps
}

// hardProblems 只挑出阻断级问题。
func hardProblems(ps []configProblem) []configProblem {
	var out []configProblem
	for _, p := range ps {
		if p.Hard {
			out = append(out, p)
		}
	}
	return out
}

// reportConfigProblems 把问题写进日志与审计流：硬错误用 warn，软提示用 info。
// 供"内部保存"路径使用 —— 内部保存绝不能因为校验失败就不写盘（那会丢用户数据）。
func reportConfigProblems(ps []configProblem) int {
	n := 0
	for _, p := range ps {
		if p.Hard {
			warn(scSys, "配置", "存在问题（已保存，请尽快修正）：%s", p.String())
			n++
		} else {
			info(scSys, "配置", "提示：%s", p.String())
		}
	}
	return n
}
