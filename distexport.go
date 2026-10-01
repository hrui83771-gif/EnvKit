// distexport.go —— 运行时一键导出分发包（zip 下载）
//
// 与 tools/make_dist.py 的分工：
//   - make_dist.py：发布渠道，重编 exe + 运行态 dist_check 验证；
//   - 这里：日常渠道，前端「程序配置 → 导出分发包」直接把自己打进 zip 下载，
//     无需重编——运行中的二进制本身就是干净构建（embed 的是 config.dist.json）。
//
// 导出前强制做泄漏扫描：把本机 config.json（内存正本）的敏感值到自身二进制里
// 逐个搜，命中即 409 拒绝——防止有人用带密钥的 config 重新编译过 exe 后随手分发。
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
)

//go:embed distassets
var distAssets embed.FS

// collectSecrets 从内存正本收集敏感值（含 dpapi: 前缀的原文与密文两种形态）。
// 必须深拷贝：浅拷贝写 AI/Quirks 会穿透全局（铁律见 configfile.go）。
func collectSecrets() []string {
	cfgMu.Lock()
	c := cloneConfigDeepAI(cfg)
	cfgMu.Unlock()
	var vals []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		vals = append(vals, v)
		if i := strings.Index(v, "dpapi:"); i >= 0 {
			vals = append(vals, v[i+len("dpapi:"):])
		}
	}
	add(c.Chain.SSHHost)
	add(c.Chain.SSHUser)
	add(c.Chain.SSHPassword)
	add(c.Chain.SSHKey)
	add(c.Chain.SSHHostKey)
	add(c.MySQL.Password)
	add(c.Projects.MySQLPass)
	add(c.Projects.FrontendDir)
	add(c.Projects.BackendDir)
	if c.AI != nil {
		add(c.AI.APIKey)
	}
	return vals
}

// scanSelfLeaks 返回敏感值在二进制中的命中项描述（通用词/短值跳过，避免误报）。
// 同时检查 UTF-8 与 UTF-16LE 两种编码形态（与 make_dist.py 的策略一致）。
func scanSelfLeaks(blob []byte) []string {
	common := map[string]bool{
		"root": true, "admin": true, "user": true, "localhost": true,
		"127.0.0.1": true, "0.0.0.0": true, "123456": true,
		"deepseek": true, "utf8mb4": true, "mysql": true,
	}
	var hits []string
	for _, v := range collectSecrets() {
		if common[strings.ToLower(v)] || len(v) < 5 || strings.HasPrefix(strings.ToLower(v), "dpapi:") {
			continue
		}
		if bytes.Contains(blob, []byte(v)) {
			hits = append(hits, v[:min(4, len(v))]+"****")
		} else {
			u16 := make([]byte, 0, len(v)*2)
			for _, ru := range v {
				lo, hi := utf16.EncodeRune(ru)
				if hi != 0 {
					u16 = append(u16, byte(lo&0xff), byte(lo>>8), byte(hi&0xff), byte(hi>>8))
				} else {
					u16 = append(u16, byte(lo&0xff), byte(lo>>8))
				}
			}
			if bytes.Contains(blob, u16) {
				hits = append(hits, v[:min(4, len(v))]+"****(utf16)")
			}
		}
	}
	return hits
}

func handleDistExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	exePath, err := os.Executable()
	if err != nil {
		http.Error(w, "无法定位自身 exe："+err.Error(), 500)
		return
	}
	exeBlob, err := os.ReadFile(exePath)
	if err != nil {
		http.Error(w, "读取自身 exe 失败："+err.Error(), 500)
		return
	}
	if hits := scanSelfLeaks(exeBlob); len(hits) > 0 {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.Error(w, fmt.Sprintf(
			"泄漏扫描：当前 exe 二进制中发现 %d 项本机敏感信息（%v…），疑似用带密钥的配置重新编译过，已拒绝导出。"+
				"请改用干净源码构建（make_dist.py + dist_check.py 验证通过）后再分发。", len(hits), hits), 409)
		return
	}

	readAsset := func(name string) string {
		b, err := distAssets.ReadFile("distassets/" + name)
		if err != nil {
			return ""
		}
		return string(b)
	}
	// bat 必须 ANSI(GBK) + CRLF，否则中文 cmd 下乱码（make_dist.py 同款约定）
	gbkCRLF := func(s string) []byte {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\n", "\r\n")
		out, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
		if err != nil {
			return []byte(s)
		}
		return out
	}

	sum := sha256.Sum256(exeBlob)
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for _, e := range []struct {
		name string
		data []byte
	}{
		{"EnvKit.exe", exeBlob},
		{"安装.bat", gbkCRLF(readAsset("install.bat.tmpl"))},
		{"卸载.bat", gbkCRLF(readAsset("uninstall.bat.tmpl"))},
		{"使用说明.md", []byte(readAsset("README.md"))},
		{"SHA256SUMS.txt", []byte("EnvKit.exe  sha256  " + hex.EncodeToString(sum[:]) + "\n")},
	} {
		f, err := zw.Create(e.name)
		if err == nil {
			_, err = f.Write(e.data)
		}
		if err != nil {
			http.Error(w, "打包失败："+err.Error(), 500)
			return
		}
	}
	if err := zw.Close(); err != nil {
		http.Error(w, "打包失败："+err.Error(), 500)
		return
	}
	name := "EnvKit-v" + appVersion + "-win64.zip"
	auditNow(actUser, "dist_export", name, fmt.Sprintf("exe=%.1fMB,leak_scan=0命中", float64(len(exeBlob))/1048576), resOK, "")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	_, _ = w.Write(buf.Bytes())
}
