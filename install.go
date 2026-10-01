// install.go —— 环境安装：下载（含 SHA256 校验与重试）、解压、组件安装、失败提示
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------- 环境安装：安装 ----------
func runInstall() {
	setPhase("installing")
	info(scInstall, "安装", "开始安装缺失组件...")
	anyFail := false
	for _, c := range cfg.Components {
		if err := installComponent(c); err != nil {
			anyFail = true
			fail(scInstall, "安装", "%s 失败：%s", c.Name, err.Error())
			if hint := errorHint(c, err); hint != "" {
				warn(scInstall, "提示", "%s", hint)
			}
			continue
		}
		ok(scInstall, "安装", "%s 完成", c.Name)
		resultsMu.Lock()
		results[c.Name] = CheckResult{Name: c.Name, Installed: true, Detail: "已就绪"}
		resultsMu.Unlock()
	}
	for _, c := range cfg.Components {
		configureLanguageProxy(c)
	}
	if anyFail {
		fail(scInstall, "安装", "存在失败项，请查看日志后重试")
		setPhase("done")
		return
	}
	ok(scInstall, "安装", "全部完成，请重开终端使环境变量生效")
	setPhase("done")
}

func installComponent(c Component) error {
	// MySQL 官方 CDN 只保留系列最新版：安装前动态解析当前可下载版本并同步配置，
	// 避免"官方一发新版，静态 URL 就 404"的问题。
	if c.Special == "mysql" {
		if nc := resolveMySQLComponent(c); nc.URL != c.URL {
			// 版本换了，旧的校验值对新包无意义，必须清空（否则会把正常的新版本误判为被篡改）
			nc.SHA256 = ""
			ok(scInstall, "安装", "MySQL CDN 当前可下载版本为 %s，已自动切换下载地址并更新配置", nc.Version)
			for i := range cfg.Components {
				if cfg.Components[i].Name == c.Name {
					cfg.Components[i] = nc
				}
			}
			saveExternalConfig(cfg)
			c = nc
		}
	}
	if ok2, _ := isInstalled(c); ok2 {
		ok(scInstall, "安装", "%s 已存在，跳过", c.Name)
		configureEnv(c)
		if c.Special == "mysql" {
			setupMySQL(c)
		}
		return nil
	}
	info(scInstall, "安装", "处理 %s %s", c.Name, c.Version)
	zipPath := filepath.Join(os.TempDir(), c.Name+".zip")
	dlErr := downloadWithRetry(c.URL, zipPath, c.Name, c.SHA256)
	if dlErr != nil && c.Special == "mysql" {
		// 被新版本顶掉等场景：改走官方 archives 镜像兜底
		arch := fmt.Sprintf("https://downloads.mysql.com/archives/get/p/23/file/mysql-%s-winx64.zip", c.Version)
		warn(scInstall, "安装", "主地址下载失败（%v），改走官方 archives 镜像...", dlErr)
		dlErr = downloadWithRetry(arch, zipPath, c.Name, "")
	}
	if dlErr != nil {
		return fmt.Errorf("下载失败: %w", dlErr)
	}
	// 供应链保护：没有官方校验值时，把首次下载的哈希记为基线（TOFU 思路）。
	// 之后同一组件再下载，只要哈希与基线不符就会被 downloadWithRetry 拒绝并删文件——
	// 这能挡住"CDN 被污染 / 镜像被换包"这类最危险的落地执行路径。
	if strings.TrimSpace(c.SHA256) == "" {
		if sum, err := fileSHA256(zipPath); err == nil && len(sum) == 64 {
			cfgMu.Lock()
			for i := range cfg.Components {
				if cfg.Components[i].Name == c.Name {
					cfg.Components[i].SHA256 = sum
				}
			}
			snapshot := cfg
			cfgMu.Unlock()
			saveExternalConfig(snapshot)
			c.SHA256 = sum
			warn(scInstall, "安全", "%s 未内置官方校验值：已把本次下载的 SHA256 记为基线（%s…）。"+
				"今后同版本下载若不一致将拒绝安装；建议把官方公布的 sha256 填进 config.json", c.Name, sum[:16])
		}
	}
	info(scInstall, "安装", "解压 -> %s", componentInstallPath(c))
	if err := unzip(zipPath, cfg.InstallDir); err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}
	_ = os.Remove(zipPath)
	configureEnv(c)
	if c.Special == "mysql" {
		setupMySQL(c)
	}
	return nil
}

func configureLanguageProxy(c Component) {
	switch c.Name {
	case "Go":
		exe := findExe("go.exe", "go")
		if exe == "" {
			return
		}
		if _, err := runIn(filepath.Dir(exe), exe, "env", "-w", "GOPROXY="+cfg.GoProxy); err != nil {
			warn(scInstall, "代理", "GOPROXY 设置失败：%v", err)
		} else {
			ok(scInstall, "代理", "GOPROXY=%s", cfg.GoProxy)
		}
	case "Node.js":
		exe := findExe("npm.cmd", "npm")
		if exe == "" {
			return
		}
		if _, err := runIn(filepath.Dir(exe), exe, "config", "set", "registry", cfg.NpmRegistry); err != nil {
			warn(scInstall, "代理", "npm registry 设置失败：%v", err)
		} else {
			ok(scInstall, "代理", "npm registry=%s", cfg.NpmRegistry)
		}
	}
}

func errorHint(c Component, err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "404"):
		return "下载源 404，请在『配置』里更换镜像地址"
	case strings.Contains(s, "no such host"), strings.Contains(s, "timeout"),
		strings.Contains(s, "TLS handshake"), strings.Contains(s, "Client.Timeout"):
		return "网络不可达，请检查网络或填写下载代理"
	case strings.Contains(s, "VCRUNTIME140"):
		return "缺少 VC++ 2019 运行库，请先安装后重试"
	case strings.Contains(s, "拒绝访问"), strings.Contains(s, "Access is denied"),
		strings.Contains(s, "OpenService"), strings.Contains(s, "StartService"):
		return "权限不足，请以管理员身份运行 EnvKit.exe"
	default:
		return "请把日志发我排查"
	}
}

// ---------- 下载/解压 ----------
// fileSHA256 计算文件 SHA256（十六进制小写）。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// downloadWithRetry 下载并可选 SHA256 校验，失败自动重试（共 3 次机会）。
func downloadWithRetry(rawURL, dest, label, sha256sum string) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			warn(scInstall, "下载", "[%s] 第 %d 次重试...", label, attempt)
			time.Sleep(2 * time.Second)
		}
		if err := download(rawURL, dest, label); err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(sha256sum) != "" {
			sum, err := fileSHA256(dest)
			if err != nil {
				lastErr = fmt.Errorf("计算 SHA256 失败：%v", err)
				continue
			}
			if !strings.EqualFold(sum, strings.TrimSpace(sha256sum)) {
				_ = os.Remove(dest)
				lastErr = fmt.Errorf("SHA256 校验不符（期望 %s，实际 %s），已删除损坏文件", sha256sum, sum)
				warn(scInstall, "下载", "[%s] %v", label, lastErr)
				continue
			}
			ok(scInstall, "下载", "[%s] SHA256 校验通过", label)
		}
		return nil
	}
	return lastErr
}

func download(rawURL, dest, label string) error {
	resp, err := httpLong.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d（源：%s）", resp.StatusCode, rawURL)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	total := resp.ContentLength
	buf := make([]byte, 64*1024)
	written := int64(0)
	lastPct := -1
	lastMB := int64(0)
	for {
		n, re := resp.Body.Read(buf)
		if n > 0 {
			f.Write(buf[:n])
			written += int64(n)
			if total > 0 {
				pct := int(written * 100 / total)
				if pct >= lastPct+10 {
					lastPct = pct
					info(scInstall, "下载", "[%s] %d%%（%.0f / %.0f MB）", label, pct, float64(written)/1048576, float64(total)/1048576)
				}
			} else if written >= lastMB+20971520 { // 总长未知：每 20MB 汇报一次
				lastMB = written
				info(scInstall, "下载", "[%s] 已下载 %.0f MB", label, float64(written)/1048576)
			}
		}
		if re == io.EOF {
			break
		}
		if re != nil {
			return re
		}
	}
	ok(scInstall, "下载", "[%s] 完成", label)
	return nil
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	dest = filepath.Clean(dest)
	count := 0
	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(fpath, dest+string(os.PathSeparator)) && fpath != dest {
			return fmt.Errorf("zip 内非法路径: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, 0755)
			continue
		}
		os.MkdirAll(filepath.Dir(fpath), 0755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
		count++
	}
	ok(scInstall, "安装", "解压 %d 个文件", count)
	return nil
}
