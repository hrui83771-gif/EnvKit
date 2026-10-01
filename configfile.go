// configfile.go —— 配置加载、默认值合并、原子保存（DPAPI 加密密码字段）
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
			plain := func(v string) bool { return v != "" && !strings.HasPrefix(v, dpapiPrefix) }
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

func applyConfigDefaults() {
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
}

// mergeFormConfig 把"页面表单提交的配置"合并到当前配置上：
// 表单只包含界面上存在的字段，缺失的整段沿用原值 —— 否则一次自动保存就会把
// AI 配置（Base URL / API Key / 模型，另有独立入口保存）或组件表（安装页生成）抹掉。
func mergeFormConfig(form, cur Config) Config {
	if form.AI == nil && cur.AI != nil {
		cp := *cur.AI // 深拷贝：避免表单配置与全局配置共享同一个 AI 结构体
		form.AI = &cp
	}
	if len(form.Components) == 0 {
		form.Components = cur.Components
	}
	if form.SchemaVersion == 0 {
		form.SchemaVersion = cur.SchemaVersion // 表单不提交版本号，别让它被"降级"成 0
	}
	if form.Alert.Webhook == "" && form.Alert.CooldownSecs == 0 {
		form.Alert = cur.Alert // 界面暂无告警配置区，防止表单保存把 webhook 抹掉
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
func saveExternalConfig(c Config) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	path := filepath.Join(filepath.Dir(exe), "config.json")
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
	_ = os.Rename(tmp, path)
}

func protectIfPlain(v string) string {
	if v == "" || strings.HasPrefix(v, dpapiPrefix) {
		return v
	}
	if e := dpapiProtect(v); e != "" {
		return e
	}
	return v // 加密失败保底存原文
}

func decryptField(v string) string {
	if !strings.HasPrefix(v, dpapiPrefix) {
		return v
	}
	if p, err := dpapiUnprotect(v); err == nil {
		return p
	}
	return "" // 换机/换用户解不开 → 置空，等用户重填
}

// cloneConfigDeepAI 值拷贝 Config，并对 AI 指针做深拷贝（含 Quirks map）。
// Config 是值类型但 AI 是指针 —— 直接 `c := cfg` 后改 c.AI.APIKey 会把全局
// 内存密钥一起改掉（真实事故：导出诊断包脱敏后 AI 助手当场失联）。
func cloneConfigDeepAI(c Config) Config {
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
	return c
}

func encryptConfig(c Config) Config {
	c = cloneConfigDeepAI(c)
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
