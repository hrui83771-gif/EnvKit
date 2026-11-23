// configfile.go —— 配置加载、默认值合并、原子保存（DPAPI 加密密码字段）
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"envkit/internal/platform"
)

// ---------- 杂项 ----------

// loadConfigFile 按「正本 → .bak → .bak2 → .bak3」顺序找一个**能解析**的配置。
// 返回 (数据, 实际来源文件名, 错误)。JSON 解析失败也算不可用 —— 半截文件比没有文件更危险。
func loadConfigFile(path string) ([]byte, string, error) {
	var firstErr error
	for _, p := range []string{path, path + ".bak", path + ".bak2", path + ".bak3"} {
		b, err := os.ReadFile(p)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var probe Config
		if err := json.Unmarshal(b, &probe); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s 解析失败：%v", filepath.Base(p), err)
			}
			continue
		}
		return b, filepath.Base(p), nil
	}
	return nil, "", firstErr
}

func loadConfig() {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		path := filepath.Join(dir, "config.json")
		data, src, lerr := loadConfigFile(path)
		if lerr == nil {
			var rawCfg Config
			_ = json.Unmarshal(data, &rawCfg)
			// 迁移判断必须基于磁盘原文（内存中解密后必然是明文）
			plain := func(v string) bool { return v != "" && !strings.HasPrefix(v, platform.DPAPIPrefix) }
			needMigrate := plain(rawCfg.MySQL.Password) || plain(rawCfg.Projects.MySQLPass) || plain(rawCfg.Chain.SSHPassword) || (rawCfg.AI != nil && plain(rawCfg.AI.APIKey))
			_ = json.Unmarshal(data, &cfg)
			applyConfigDefaults()
			steps := migrateConfig(&cfg, rawCfg.SchemaVersion)
			normalizeConfig(&cfg)
			decryptConfig(&cfg)
			for _, s := range steps {
				warn(scSys, "配置", "已迁移配置：%s", s)
			}
			if src != "config.json" {
				// 回退成功：立刻按回退内容重写正本，避免"每次启动都回退"这种隐性状态
				warn(scSys, "配置", "config.json 不可用，已从 %s 恢复，并重写正本", src)
				saveExternalConfig(cfg)
				auditNow(actSys, "config_recover", "config.json", src, resOK, lerr.Error())
				return
			}
			if needMigrate {
				warn(scSys, "安全", "检测到明文密码，已升级为 DPAPI 加密存储")
				saveExternalConfig(cfg)
			} else if cfg.SchemaVersion != rawCfg.SchemaVersion {
				saveExternalConfig(cfg) // 版本已推进，落盘固化
			}
			return
		}
	}
	_ = json.Unmarshal(embeddedConfig, &cfg)
	applyConfigDefaults()
	_ = migrateConfig(&cfg, cfg.SchemaVersion)
	normalizeConfig(&cfg)
	// 模板里理论上没有密文，但仍要走一次解密：否则万一混入 dpapi: 前缀，
	// 会被当成字面量密码拿去连库/调 API，报出完全看不懂的错。
	decryptConfig(&cfg)
}

// applyConfigDefaults 把默认值落到全局配置：只在加载/迁移路径调用。
func applyConfigDefaults() { applyDefaultsTo(&cfg) }

// applyDefaultsTo 把默认值落到给定配置。
//
// 为什么要参数化：导入配置时原来调的是 applyConfigDefaults()，它写的是全局 cfg——
// 结果"给导入文件补默认值"变成了"无锁改正在运行的内存配置"（还让随后的
// 空密码保留逻辑拿到已经补过默认值的字段）。这里参数名沿用 cfg 遮蔽全局变量，
// 函数体里的 cfg.X 全部落在实参上，语义从"改全局"变成"改这份配置"。
func applyDefaultsTo(cfg *Config) {
	if cfg.InstallDir == "" {
		cfg.InstallDir = `C:\EnvKit\tools`
	}
	if cfg.GoProxy == "" {
		cfg.GoProxy = "https://goproxy.cn,https://goproxy.io,direct"
	}
	if cfg.NpmRegistry == "" {
		cfg.NpmRegistry = "https://mirrors.cloud.tencent.com/npm/"
	}
	if cfg.Projects.MySQLHost == "" {
		cfg.Projects.MySQLHost = "127.0.0.1"
	}
	if cfg.Projects.MySQLPort == 0 {
		cfg.Projects.MySQLPort = 3306
	}
	if cfg.Projects.MySQLUser == "" {
		cfg.Projects.MySQLUser = "root"
	}
	if cfg.Projects.MySQLPass == "" {
		cfg.Projects.MySQLPass = "123456"
	}
	if cfg.Chain.SSHPort == 0 {
		cfg.Chain.SSHPort = 22
	}
	if cfg.Chain.ChainPort == 0 {
		cfg.Chain.ChainPort = 20200
	}
	if cfg.Chain.WebasePort == 0 {
		cfg.Chain.WebasePort = 5002
	}
	if cfg.Chain.GroupID == 0 {
		cfg.Chain.GroupID = 1
	}
	if cfg.Chain.ChainHost == "" {
		cfg.Chain.ChainHost = cfg.Chain.SSHHost
	}
	if cfg.Chain.ChainStart == "" {
		cfg.Chain.ChainStart = "bash start_all.sh" // FISCO-BCOS v2 节点目录默认启动脚本
	}
	if cfg.Chain.WebaseStart == "" {
		cfg.Chain.WebaseStart = "bash start.sh" // WeBASE-Front 默认启动脚本
	}
	if cfg.Chain.ChainGuardSecs == 0 {
		cfg.Chain.ChainGuardSecs = 60 // 后台守护默认 60s 一轮
	}
	// AI 助手默认值
	if cfg.AI == nil {
		cfg.AI = &AIConfig{}
	}
	if cfg.AI.Provider == "" {
		cfg.AI.Provider = "deepseek"
	}
	if cfg.AI.BaseURL == "" {
		cfg.AI.BaseURL = "https://api.deepseek.com"
	}
	if cfg.AI.Model == "" {
		cfg.AI.Model = "deepseek-chat"
	}
	if cfg.AI.Temperature == 0 {
		cfg.AI.Temperature = 0.3
	}
	if cfg.AI.MaxTokens == 0 {
		cfg.AI.MaxTokens = 2048
	}
	// chain_autorecover 默认 false（不自动重启，避免误伤）；用户开启后检测发现宕机即自动拉起
	// 行动（Phase 1）：巡检/晨报默认开——**代码内默认值**，理由同 defaultWeatherCity：
	// 老配置里没有 missions 段，改分发模板对已有用户无效。
	// Enabled 是指针：nil = 没设 = 开（见 MissionConfig.on）。
	if cfg.Mission.PatrolIntervalMin == 0 {
		cfg.Mission.PatrolIntervalMin = 60
	}
	if cfg.Mission.BriefingAt == "" {
		cfg.Mission.BriefingAt = "08:30"
	}
	// 开发者段（设置页「开发者配置」，v3.4）：老配置里没有 "dev" 段，这里补指针并
	// 把默认值落实到内存 —— 这样 GET /api/config 与设置页回显的都是真实数字，
	// 而不是一排空输入框。读取侧各 getter 仍带兜底（手写 0 也不会读出 0）。
	if cfg.Dev == nil {
		cfg.Dev = &DevConfig{}
	}
	dv := cfg.Dev.resolved()
	cfg.Dev = &dv
}

// mergeFormConfig 把"页面表单提交的配置"合并到当前配置上：
// 表单只包含界面上存在的字段，缺失的整段沿用原值 —— 否则一次自动保存就会把
// AI 配置（Base URL / API Key / 模型，另有独立入口保存）或组件表（安装页生成）抹掉。
func mergeFormConfig(form, cur Config) Config {
	if form.AI == nil && cur.AI != nil {
		cp := *cur.AI // 深拷贝：避免表单配置与全局配置共享同一个 AI 结构体
		form.AI = &cp
	}
	// 桌宠段（天气开关/城市）只有 /api/pet/config 一个出入口，程序配置页
	// 既不显示也不提交它 —— 不无条件保留的话，改一下"安装目录"触发自动保存，
	// 就会把用户已开启的天气设置抹成零值（configBody() 同理不回传 pet）。
	// 用无条件保留而不是"零值才保留"：表单里没有任何途径表达"把桌宠关掉"，
	// 判断零值反而会误判成"表单想清空"。
	form.Pet = cur.Pet
	if len(form.Components) == 0 {
		form.Components = cur.Components
	}
	if form.SchemaVersion == 0 {
		form.SchemaVersion = cur.SchemaVersion // 表单不提交版本号，别让它被"降级"成 0
	}
	if form.Alert.Webhook == "" && form.Alert.CooldownSecs == 0 {
		form.Alert = cur.Alert // 界面暂无告警配置区，防止表单保存把 webhook 抹掉
	}
	// 行动段（Phase 1）只有 API 没有配置页 UI，同 Pet/Alert 无条件保留——
	// 否则程序配置页一次自动保存就把巡检开关/入站开关抹成零值。
	form.Mission = cur.Mission
	// 开发者段（v3.4 设置页）走独立出入口 /api/dev/config：老页面的 configBody()
	// 不回传 dev，指针为空就沿用原值 —— 否则老页一次自动保存就把新页的调参抹回零值。
	// 用指针判空而不是无条件保留：将来若整包导出/导入带上了 dev 段，自然放行。
	if form.Dev == nil {
		form.Dev = cur.Dev
	}
	// MCP 段（v3.8）走独立出入口 /api/mcp/config，同 Dev：老页面 configBody()
	// 不回传 mcp，指针为空就沿用原值 —— 否则一次自动保存就把 MCP 开关抹回关闭。
	if form.MCP == nil {
		form.MCP = cur.MCP
	}
	if strings.TrimSpace(form.UpdateURL) == "" {
		form.UpdateURL = cur.UpdateURL // 同上：表单没有该字段，别让保存把它抹成空
	}
	if strings.TrimSpace(form.AppName) == "" {
		form.AppName = cur.AppName
	}
	if strings.TrimSpace(form.AppTitle) == "" {
		form.AppTitle = cur.AppTitle
	}
	if len(form.Chain.SSHHost) == 0 && len(form.Chain.SSHUser) == 0 && len(form.Chain.ChainHost) == 0 &&
		len(form.Chain.ChainDir) == 0 && len(form.Chain.WebaseDir) == 0 {
		form.Chain = cur.Chain // 表单完全没有链端字段时保留（例如第三方客户端只提交部分字段）
	}
	return form
}

// rotateConfigBackup 保留最近 3 版历史配置：.bak（上一版）/.bak2/.bak3。
// 旧实现只留一份 .bak —— 连续两次异常保存就会把"唯一的好版本"顶掉。
func rotateConfigBackup(path string) {
	if _, err := os.Stat(path); err != nil {
		return // 还没有正本，无需轮转
	}
	_ = os.Remove(path + ".bak3")
	_ = os.Rename(path+".bak2", path+".bak3")
	_ = os.Rename(path+".bak", path+".bak2")
	_ = os.Rename(path, path+".bak")
}

// saveExternalConfig 规范化 + 校验告警 + 原子写 + 保留 3 版备份 + 密码字段 DPAPI 加密
// （内存中始终保持明文）。
//
// 注意：内部保存路径（AI 学习参数、组件版本变更等）**不因校验失败而跳过写盘** ——
// 那会丢掉用户数据；硬错误只记日志与审计，由界面的保存接口负责拦截。
// configFilePath 定位外部配置文件。抽成变量供单测替换——
// 否则任何碰配置的测试都会写坏用户真实的 config.json。
var configFilePath = func() string {
	exe, err := os.Executable()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(filepath.Dir(exe), "config.json")
}

func saveExternalConfig(c Config) {
	// 落盘前先深拷贝：normalizeConfig 会就地改 Components 等切片元素，
	// 若调用方传的是全局配置的值拷贝，那等于无锁改全局底层数组。
	c = cloneConfigDeep(c)
	path := configFilePath()
	normalizeConfig(&c)
	c.SchemaVersion = configSchemaVersion
	if n := reportConfigProblems(checkConfig(c)); n > 0 {
		auditNow(actSys, "config_save_invalid", "config.json", "", resFail, fmt.Sprintf("%d 项硬错误", n))
	}
	data, err := json.MarshalIndent(encryptConfig(c), "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	rotateConfigBackup(path)
	if err := os.Rename(tmp, path); err != nil {
		return
	}
	// 告诉 configwatch：这份内容是我们自己写下去的，别当成"用户在外部改配置"再重载一遍。
	// 必须用 data（加密后的原始字节）而不是内存里的 cfg —— DPAPI 每次加密同一明文
	// 都会产出不同密文，拿内存值和磁盘字节比哈希永远不相等。
	cfgMarkDisk(data)
}

func protectIfPlain(v string) string {
	if v == "" || strings.HasPrefix(v, platform.DPAPIPrefix) {
		return v
	}
	if e := platform.DPAPIProtect(v); e != "" {
		return e
	}
	return v // 加密失败保底存原文
}

func decryptField(v string) string {
	if !strings.HasPrefix(v, platform.DPAPIPrefix) {
		return v
	}
	if p, err := platform.DPAPIUnprotect(v); err == nil {
		return p
	}
	return "" // 换机/换用户解不开 → 置空，等用户重填
}

// cloneConfigDeep 值拷贝 Config，并对**所有引用类型字段**做深拷贝：
// 指针段（AI / Dev / Lan / MCP / Update / Notify）、切片（Projects 的端口清单与
// 收藏脚本、Services / Components）、map（AI.Quirks、Component.ExtraEnv）。
//
// 为什么必须全：Config 是值类型但内部到处是指针与切片。直接 `c := cfg` 后
// 改 c.AI.APIKey 会把全局内存密钥一起改掉（真实事故：导出诊断包脱敏后 AI
// 助手当场失联）；更隐蔽的是 normalizeConfig 会就地改 c.Components[i].URL ——
// 浅拷贝下那是在**无锁改全局配置的底层数组**，并发的 GET /api/config 会读到
// 半截状态。新增引用字段时同步这里与 TestCloneConfigDeepIsolated。
func cloneConfigDeep(c Config) Config {
	c.Projects.ScanPorts = cloneIntSlice(c.Projects.ScanPorts)
	c.Projects.WebScripts = cloneStringSlice(c.Projects.WebScripts)

	if c.Services != nil {
		out := make([]Service, len(c.Services))
		for i, s := range c.Services {
			s.DependsOn = cloneStringSlice(s.DependsOn)
			s.Probe = cloneProbeSpec(s.Probe)
			s.Verify.Evidence = cloneStringSlice(s.Verify.Evidence)
			out[i] = s
		}
		c.Services = out
	}
	if c.Components != nil {
		out := make([]Component, len(c.Components))
		for i, comp := range c.Components {
			comp.CheckArgs = cloneStringSlice(comp.CheckArgs)
			comp.ExtraEnv = cloneStringMap(comp.ExtraEnv)
			out[i] = comp
		}
		c.Components = out
	}

	if c.AI != nil {
		ai := *c.AI
		if ai.Quirks != nil {
			q := make(map[string]AIQuirk, len(ai.Quirks))
			for k, v := range ai.Quirks {
				q[k] = v
			}
			ai.Quirks = q
		}
		c.AI = &ai
	}
	if c.Dev != nil {
		dv := *c.Dev
		c.Dev = &dv
	}
	if c.Lan != nil {
		l := *c.Lan
		l.Enabled = cloneBoolPtr(l.Enabled)
		l.Port = cloneIntPtr(l.Port)
		l.AllowShot = cloneBoolPtr(l.AllowShot)
		l.AllowClip = cloneBoolPtr(l.AllowClip)
		l.AllowTerm = cloneBoolPtr(l.AllowTerm)
		l.RequireOTP = cloneBoolPtr(l.RequireOTP)
		if l.Devices != nil {
			l.Devices = append([]LanDevice(nil), l.Devices...)
		}
		c.Lan = &l
	}
	if c.MCP != nil {
		m := *c.MCP
		m.Enabled = cloneBoolPtr(m.Enabled)
		c.MCP = &m
	}
	if c.Update != nil {
		u := *c.Update
		u.Enabled = cloneBoolPtr(u.Enabled)
		u.IntervalHours = cloneIntPtr(u.IntervalHours)
		c.Update = &u
	}
	if c.Notify != nil {
		n := *c.Notify
		c.Notify = &n
	}
	c.Mission.Enabled = cloneBoolPtr(c.Mission.Enabled)
	return c
}

func cloneStringSlice(s []string) []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s...)
}

func cloneIntSlice(s []int) []int {
	if s == nil {
		return nil
	}
	return append([]int(nil), s...)
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// cloneProbeSpec 递归复制 composite 探针的 Parts（ProbeSpec 自引用）。
func cloneProbeSpec(p ProbeSpec) ProbeSpec {
	if len(p.Parts) > 0 {
		parts := make([]ProbeSpec, len(p.Parts))
		for i, sub := range p.Parts {
			parts[i] = cloneProbeSpec(sub)
		}
		p.Parts = parts
	}
	return p
}

func encryptConfig(c Config) Config {
	c = cloneConfigDeep(c)
	c.MySQL.Password = protectIfPlain(c.MySQL.Password)
	c.Projects.MySQLPass = protectIfPlain(c.Projects.MySQLPass)
	c.Chain.SSHPassword = protectIfPlain(c.Chain.SSHPassword)
	if c.AI != nil {
		c.AI.APIKey = protectIfPlain(c.AI.APIKey)
	}
	return c
}

func decryptConfig(c *Config) {
	c.MySQL.Password = decryptField(c.MySQL.Password)
	c.Projects.MySQLPass = decryptField(c.Projects.MySQLPass)
	c.Chain.SSHPassword = decryptField(c.Chain.SSHPassword)
	if c.AI != nil {
		c.AI.APIKey = decryptField(c.AI.APIKey)
	}
}
