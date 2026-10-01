// detect.go —— 环境检测：组件探测、版本读取、已装路径、语言代理与 MySQL 初始化
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ---------- 环境安装：检测 ----------
func runCheck() {
	setPhase("checking")
	info(scInstall, "检测", "开始检测...")
	for _, c := range cfg.Components {
		r := detectComponent(c)
		resultsMu.Lock()
		results[c.Name] = r
		resultsMu.Unlock()
		if r.Installed {
			ok(scInstall, "检测", "%s 已安装 · %s · %s", c.Name, r.Version, r.Location)
		} else {
			warn(scInstall, "检测", "%s 未安装", c.Name)
		}
	}
	ok(scInstall, "检测", "检测完成")
	setPhase("checked")
}

func detectComponent(c Component) CheckResult {
	toolPath := filepath.Join(componentBinDir(c), c.BinExe)
	if _, err := os.Stat(toolPath); err == nil {
		ver := getVersion(toolPath, c)
		return CheckResult{Name: c.Name, Installed: true, Version: ver, Location: toolPath}
	}
	if p, err := exec.LookPath(c.CheckCmd); err == nil {
		ver := getVersion(p, c)
		return CheckResult{Name: c.Name, Installed: true, Version: ver, Location: p}
	}
	return CheckResult{Name: c.Name, Installed: false}
}

func getVersion(exe string, c Component) string {
	if len(c.CheckArgs) == 0 {
		return ""
	}
	ctx, cancel := cmdContext(20 * time.Second)
	defer cancel()
	// 走统一的命令构造（便于故障注入开关覆盖组件探测这类轻量路径）
	out, err := buildCmd(ctx, false, exe, c.CheckArgs...).CombinedOutput()
	if err != nil {
		return ""
	}
	return firstLine(strings.TrimSpace(string(out)))
}

// ---------- 组件路径/检测 ----------
func componentInstallPath(c Component) string { return filepath.Join(cfg.InstallDir, c.ZipTop) }
func componentBinDir(c Component) string {
	if c.BinRel == "" {
		return componentInstallPath(c)
	}
	return filepath.Join(componentInstallPath(c), c.BinRel)
}

func isInstalled(c Component) (bool, string) {
	exePath := filepath.Join(componentBinDir(c), c.BinExe)
	if _, err := os.Stat(exePath); err == nil {
		return true, exePath
	}
	if _, err := exec.LookPath(c.CheckCmd); err == nil {
		return true, "系统已存在 " + c.CheckCmd
	}
	return false, ""
}

func configureEnv(c Component) {
	binDir := componentBinDir(c)
	if _, err := os.Stat(binDir); os.IsNotExist(err) {
		warn(scInstall, "环境", "%s 由系统提供，跳过 PATH", c.Name)
		return
	}
	if err := addToUserPath(binDir); err != nil {
		warn(scInstall, "环境", "PATH 设置失败：%v", err)
	} else {
		ok(scInstall, "环境", "PATH += %s", binDir)
	}
	for k, v := range c.ExtraEnv {
		v = strings.ReplaceAll(v, "<install>", cfg.InstallDir)
		if err := setEnvVar(k, v); err != nil {
			warn(scInstall, "环境", "%s 设置失败：%v", k, err)
		} else {
			ok(scInstall, "环境", "%s=%s", k, v)
		}
	}
}

func setupMySQL(c Component) {
	base := componentInstallPath(c)
	mysqldExe := filepath.Join(base, "bin", "mysqld.exe")
	if _, err := os.Stat(mysqldExe); os.IsNotExist(err) {
		warn(scInstall, "MySQL", "由系统提供，跳过初始化")
		return
	}
	dataDir := filepath.Join(base, "data")
	iniPath := filepath.Join(base, "my.ini")
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		ini := fmt.Sprintf("[mysqld]\nbasedir=%q\ndatadir=%q\nport=%d\n", base, dataDir, cfg.MySQL.Port)
		_ = os.WriteFile(iniPath, []byte(ini), 0644)
		info(scInstall, "MySQL", "初始化数据目录...")
		out, e := runIn(base, mysqldExe, "--defaults-file="+iniPath, "--initialize-insecure", "--console")
		if e != nil {
			warn(scInstall, "MySQL", "初始化输出：%s", firstLines(out, 500))
		} else {
			ok(scInstall, "MySQL", "数据目录已初始化")
		}
	} else {
		info(scInstall, "MySQL", "数据目录已存在，跳过")
	}
	info(scInstall, "MySQL", "注册并启动服务 %s", cfg.MySQL.ServiceName)
	_, _ = runIn(base, mysqldExe, "--install", cfg.MySQL.ServiceName, "--defaults-file="+iniPath)
	_ = exec.Command("net", "start", cfg.MySQL.ServiceName).Run()
	time.Sleep(3 * time.Second)
	pw := cfg.MySQL.Password
	sql := "ALTER USER 'root'@'localhost' IDENTIFIED BY '" + pw + "';"
	out, e := runIn(base, filepath.Join(base, "bin", "mysql.exe"), "-u", "root", "-e", sql)
	if e != nil {
		// 注意：这里绝不能把 sql 原样打进日志——它内含明文密码。
		// 只给出可自行复现的操作指引。
		fail(scInstall, "MySQL", "设置密码失败，请在 MySQL 控制台手动执行：ALTER USER 'root'@'localhost' IDENTIFIED BY '<你的密码>';（%s）",
			firstLines(out, 300))
	} else {
		ok(scInstall, "MySQL", "root 密码已设置（密码不会写入日志）")
	}
}

// truncateStr 截断长字符串（日志/弹窗展示用，避免一行刷屏）。
func truncateStr(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
func firstLines(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
