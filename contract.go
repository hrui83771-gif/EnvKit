package main

// contract.go —— v4.0 环境契约模型（Phase 1）
//
// 契约不是 config.json 的替代品，而是项目级期望状态：
//   - envkit.yaml 跟着项目走，进 Git，描述"项目需要什么"；
//   - config.json 跟着机器走，留在本机，描述"这台机器怎么满足它"；
//   - 契约里不出现绝对路径、DPAPI 密文或明文密码。
//
// Phase 1 只读：解析、校验、生成、给 API/UI 预览；不改变启动、停止、安装行为。
// 服务段直接复用 topology.go 的 Service，避免再造一套 ContractService 与现有
// Probe/DependsOn/Verify 漂移。

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	contractVersion      = 1
	contractFileName     = "envkit.yaml"
	contractModeOff      = "off"
	contractModeReadOnly = "readonly"
	contractModeEnforce  = "enforce"
)

// EnvironmentContract 是 envkit.yaml 的内存模型。
// 字段顺序按"人读契约"的顺序排：项目 → 工具 → 服务 → 数据库 → 密钥引用。
type EnvironmentContract struct {
	Version  int               `yaml:"version" json:"version"`
	Project  ContractProject   `yaml:"project" json:"project"`
	Tools    map[string]string `yaml:"tools,omitempty" json:"tools,omitempty"`
	Services []Service         `yaml:"services,omitempty" json:"services,omitempty"`
	Database *DatabaseContract `yaml:"database,omitempty" json:"database,omitempty"`
	Secrets  map[string]string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
}

// ContractProject 项目身份。Root 是相对路径（通常为 "."），契约文件放在 root 下。
type ContractProject struct {
	Name string `yaml:"name" json:"name"`
	Root string `yaml:"root" json:"root"`
}

// DatabaseContract 数据库期望状态。不存密码，只存密钥引用。
type DatabaseContract struct {
	Engine         string   `yaml:"engine" json:"engine"`
	Host           string   `yaml:"host,omitempty" json:"host,omitempty"`
	Port           int      `yaml:"port,omitempty" json:"port,omitempty"`
	Name           string   `yaml:"name" json:"name"`
	User           string   `yaml:"user,omitempty" json:"user,omitempty"`
	Schema         string   `yaml:"schema,omitempty" json:"schema,omitempty"`
	Seed           string   `yaml:"seed,omitempty" json:"seed,omitempty"`
	RequiredTables []string `yaml:"required_tables,omitempty" json:"required_tables,omitempty"`
}

// ContractConfig 是 config.json 的 "contract" 段：本机侧只记录位置与开关。
// Root 是本机绝对路径（机器相关，留在 config）；Path 是相对 root 的契约文件。
type ContractConfig struct {
	Root string `json:"root,omitempty"`
	Path string `json:"path,omitempty"`
	Mode string `json:"mode,omitempty"` // off / readonly / enforce
}

func contractModeValid(m string) bool {
	switch m {
	case contractModeOff, contractModeReadOnly, contractModeEnforce:
		return true
	}
	return false
}

func contractModeOf(c Config) string {
	if c.Contract == nil || strings.TrimSpace(c.Contract.Mode) == "" {
		return contractModeOff
	}
	return strings.ToLower(strings.TrimSpace(c.Contract.Mode))
}

// parseContractYAML 解析并校验一份契约。KnownFields(true) 保证拼错字段不会
// 被静默忽略——契约是"验收标准"，字段名写错比报错更危险。
func parseContractYAML(data []byte) (*EnvironmentContract, []string, error) {
	var c EnvironmentContract
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, nil, fmt.Errorf("解析 envkit.yaml 失败：%w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("envkit.yaml 只能包含一个 YAML 文档：%w", err)
	}
	normalizeContract(&c)
	problems := validateContract(&c)
	return &c, problems, nil
}

func loadContractFile(path string) (*EnvironmentContract, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return parseContractYAML(data)
}

// writeContractFile 原子写契约。先写同目录 tmp，再 rename；失败不动原文件。
func writeContractFile(path string, c *EnvironmentContract) error {
	if c == nil {
		return errors.New("契约为空")
	}
	normalizeContract(c)
	if problems := validateContract(c); len(problems) > 0 {
		return fmt.Errorf("契约校验失败：%s", strings.Join(problems, "；"))
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func normalizeContract(c *EnvironmentContract) {
	if c == nil {
		return
	}
	c.Project.Name = strings.TrimSpace(c.Project.Name)
	c.Project.Root = filepath.Clean(strings.TrimSpace(c.Project.Root))
	if c.Project.Root == "" {
		c.Project.Root = "."
	}
	if c.Tools != nil {
		out := make(map[string]string, len(c.Tools))
		for k, v := range c.Tools {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k == "" {
				continue
			}
			out[strings.ToLower(k)] = v
		}
		c.Tools = out
	}
	for i := range c.Services {
		c.Services[i].ID = strings.TrimSpace(c.Services[i].ID)
		c.Services[i].Label = strings.TrimSpace(c.Services[i].Label)
		c.Services[i].Dir = filepath.Clean(strings.TrimSpace(c.Services[i].Dir))
		if c.Services[i].Dir == "" {
			c.Services[i].Dir = "."
		}
		c.Services[i].Start = strings.TrimSpace(c.Services[i].Start)
		c.Services[i].Stop = strings.TrimSpace(c.Services[i].Stop)
		c.Services[i].Restart = strings.TrimSpace(c.Services[i].Restart)
		c.Services[i].Probe.Kind = strings.ToLower(strings.TrimSpace(c.Services[i].Probe.Kind))
		c.Services[i].Verify.Target = strings.TrimSpace(c.Services[i].Verify.Target)
		c.Services[i].Verify.Evidence = cleanStrings(c.Services[i].Verify.Evidence)
		c.Services[i].DependsOn = cleanStrings(c.Services[i].DependsOn)
		c.Services[i].Probe = normalizeProbeSpec(c.Services[i].Probe)
	}
	if c.Database != nil {
		d := c.Database
		d.Engine = strings.ToLower(strings.TrimSpace(d.Engine))
		d.Host = strings.TrimSpace(d.Host)
		d.Name = strings.TrimSpace(d.Name)
		d.User = strings.TrimSpace(d.User)
		d.Schema = filepath.Clean(strings.TrimSpace(d.Schema))
		if d.Schema == "." {
			d.Schema = ""
		}
		d.Seed = filepath.Clean(strings.TrimSpace(d.Seed))
		if d.Seed == "." {
			d.Seed = ""
		}
		d.RequiredTables = cleanStrings(d.RequiredTables)
	}
	if c.Secrets != nil {
		out := make(map[string]string, len(c.Secrets))
		for k, v := range c.Secrets {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k == "" {
				continue
			}
			out[k] = v
		}
		c.Secrets = out
	}
}

func normalizeProbeSpec(p ProbeSpec) ProbeSpec {
	for i := range p.Parts {
		p.Parts[i] = normalizeProbeSpec(p.Parts[i])
	}
	p.Kind = strings.ToLower(strings.TrimSpace(p.Kind))
	p.URL = strings.TrimSpace(p.URL)
	p.Command = strings.TrimSpace(p.Command)
	return p
}

func cleanStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateContract 返回给人看的硬错误。Phase 1 不判断文件是否存在，只判断
// 契约自身是否自洽、路径是否能安全地落在项目根内。
func validateContract(c *EnvironmentContract) []string {
	if c == nil {
		return []string{"契约为空"}
	}
	var bad []string
	add := func(format string, a ...any) { bad = append(bad, fmt.Sprintf(format, a...)) }

	if c.Version <= 0 {
		add("version 不能为空，当前契约版本应为 %d", contractVersion)
	} else if c.Version != contractVersion {
		add("version=%d 暂不支持（当前支持 %d）", c.Version, contractVersion)
	}
	if strings.TrimSpace(c.Project.Name) == "" {
		add("project.name 不能为空")
	}
	if p := strings.TrimSpace(c.Project.Root); p == "" {
		add("project.root 不能为空（契约文件所在目录通常写 .）")
	} else if unsafeContractPath(p) {
		add("project.root 必须是项目内的相对路径，当前「%s」", p)
	}

	for name, constraint := range c.Tools {
		if strings.TrimSpace(name) == "" {
			add("tools 中存在空组件名")
			continue
		}
		if strings.TrimSpace(constraint) == "" {
			add("tools.%s 的版本约束不能为空（例如 >=17 或 20.x）", name)
		}
	}

	for i, s := range c.Services {
		for _, m := range validateService(s) {
			add("services[%d]：%s", i, m)
		}
		if s.Adapter == AdapterLocal || s.Adapter == AdapterDB {
			if unsafeContractPath(s.Dir) {
				add("services[%d].dir 必须是项目内的相对路径，当前「%s」", i, s.Dir)
			}
		}
	}
	if dup := duplicateServiceIDs(c.Services); len(dup) > 0 {
		add("services 中存在重复 id：%s", strings.Join(dup, "、"))
	}

	if c.Database != nil {
		d := c.Database
		if d.Engine == "" {
			add("database.engine 不能为空（Phase 1 只支持 mysql）")
		} else if d.Engine != "mysql" {
			add("database.engine=%s 暂不支持（Phase 1 只支持 mysql）", d.Engine)
		}
		if d.Name == "" {
			add("database.name 不能为空")
		}
		if d.Port != 0 && (d.Port < 1 || d.Port > 65535) {
			add("database.port 需在 1-65535，当前 %d", d.Port)
		}
		if unsafeContractPath(d.Schema) {
			add("database.schema 必须是项目内的相对路径，当前「%s」", d.Schema)
		}
		if unsafeContractPath(d.Seed) {
			add("database.seed 必须是项目内的相对路径，当前「%s」", d.Seed)
		}
		for _, t := range d.RequiredTables {
			if strings.TrimSpace(t) == "" {
				add("database.required_tables 不能包含空表名")
			}
		}
	}

	for name, ref := range c.Secrets {
		if strings.TrimSpace(name) == "" {
			add("secrets 中存在空名称")
			continue
		}
		if !validSecretRef(ref) {
			add("secrets.%s 必须使用 env:/file:/local: 引用，不能把明文密码写进契约（当前「%s」）", name, ref)
		}
	}
	return bad
}

func duplicateServiceIDs(svcs []Service) []string {
	seen := map[string]bool{}
	var dup []string
	for _, s := range svcs {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			continue
		}
		if seen[id] {
			dup = append(dup, id)
		}
		seen[id] = true
	}
	return dup
}

var reSecretRef = regexp.MustCompile(`^(env|file|local):[^\s]+$`)

func validSecretRef(ref string) bool {
	return reSecretRef.MatchString(strings.TrimSpace(ref))
}

// unsafeContractPath 拒绝绝对路径、盘符、.. 逃逸。契约里的路径必须相对项目根。
func unsafeContractPath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return true
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return true
	}
	// 统一按 / 和 \ 两种分隔符检查，避免在 Windows 上漏掉 "..\x"。
	for _, sep := range []string{"/", "\\"} {
		if strings.HasPrefix(clean, ".."+sep) {
			return true
		}
	}
	return false
}

// contractAbsPath 把契约里的相对路径落到某个 project root 下，并再次确认
// 结果没有逃出 root。Phase 2 的 Doctor 会用它检查 schema/service 目录。
func contractAbsPath(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" || rel == "." {
		return filepath.Clean(root), nil
	}
	if unsafeContractPath(rel) {
		return "", fmt.Errorf("路径必须是项目内相对路径：%s", rel)
	}
	root = filepath.Clean(root)
	abs := filepath.Clean(filepath.Join(root, rel))
	r, err := filepath.Rel(root, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径逃出项目根：%s", rel)
	}
	return abs, nil
}

// contractPathOf 返回 config 指向的契约文件位置；未配置时按项目目录推导。
func contractPathOf(c Config) (root, path string, err error) {
	if c.Contract != nil {
		root = strings.TrimSpace(c.Contract.Root)
		path = strings.TrimSpace(c.Contract.Path)
		if root != "" {
			if path == "" {
				path = contractFileName
			}
			if unsafeContractPath(path) {
				return "", "", fmt.Errorf("契约路径必须是项目内相对路径：%s", path)
			}
			return filepath.Clean(root), filepath.Join(root, path), nil
		}
	}
	root, err = projectRootFromDirs(c.Projects.FrontendDir, c.Projects.BackendDir)
	if err != nil {
		return "", "", err
	}
	return root, filepath.Join(root, contractFileName), nil
}

// projectRootFromDirs 从前后端目录推导项目根：
//   - 两个目录都在同一父目录下：取共同父目录；
//   - 一个目录是另一个的祖先：取祖先；
//   - 只给了一个目录：以该目录为根（单服务项目）。
func projectRootFromDirs(front, back string) (string, error) {
	front = strings.TrimSpace(front)
	back = strings.TrimSpace(back)
	if front == "" && back == "" {
		return "", errors.New("前后端目录都为空，无法确定项目根")
	}
	if front == "" {
		return filepath.Clean(back), nil
	}
	if back == "" {
		return filepath.Clean(front), nil
	}
	f, err := filepath.Abs(front)
	if err != nil {
		return "", err
	}
	b, err := filepath.Abs(back)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(f, b); err == nil && !escapes(rel) {
		return filepath.Clean(f), nil
	}
	if rel, err := filepath.Rel(b, f); err == nil && !escapes(rel) {
		return filepath.Clean(b), nil
	}
	// 兄弟目录：逐段取共同前缀。Windows 盘符按大小写不敏感比较。
	vol := filepath.VolumeName(f)
	f = strings.TrimPrefix(f, vol)
	b = strings.TrimPrefix(b, vol)
	fs := splitPath(f)
	bs := splitPath(b)
	i := 0
	for i < len(fs) && i < len(bs) && strings.EqualFold(fs[i], bs[i]) {
		i++
	}
	if i == 0 {
		return "", errors.New("前后端目录不在同一个盘/卷，无法生成项目级契约")
	}
	root := vol + string(filepath.Separator) + filepath.Join(fs[:i]...)
	return filepath.Clean(root), nil
}

func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, `..\`)
}

func splitPath(p string) []string {
	p = strings.Trim(p, `/\`)
	if p == "" {
		return nil
	}
	var out []string
	for _, s := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func contractRelPath(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if escapes(rel) {
		return "", fmt.Errorf("路径不在项目根内：%s", abs)
	}
	return filepath.ToSlash(rel), nil
}
