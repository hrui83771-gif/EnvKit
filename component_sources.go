// component_sources.go —— 默认组件下载源的兼容升级
//
// config.dist.json 只影响“第一次生成配置”的用户；已经运行过旧版的机器，
// component 表已经写进 config.json，镜像撤掉旧版本后仍会一直 404/403。
// 这里只迁移**已知的旧默认 URL**，不动用户自己改过的源、也不动带自定义
// SHA256 的条目（那通常意味着用户自己固定了版本）。
package main

import "strings"

const (
	// Go：1.23.6 是旧内置默认版本，项目侧早已普遍要求更高版本。
	staleGo1236 = "https://golang.google.cn/dl/go1.23.6.windows-amd64.zip"
	go1274URL   = "https://golang.google.cn/dl/go1.27.4.windows-amd64.zip"

	// Node.js：22.22.0 是旧内置默认版本，升到 24 LTS，避免新项目 Vite/工具链提示版本过低。
	staleNode2222 = "https://registry.npmmirror.com/-/binary/node/v22.22.0/node-v22.22.0-win-x64.zip"
	node24223URL  = "https://registry.npmmirror.com/-/binary/node/v24.22.3/node-v24.22.3-win-x64.zip"
	node24223Top  = "node-v24.22.3-win-x64"

	staleJDK17URL = "https://mirrors.tuna.tsinghua.edu.cn/Adoptium/17/jdk/x64/windows/OpenJDK17U-jdk_x64_windows_hotspot_17.0.12_7.zip"
	staleMaven399 = "https://mirrors.tuna.tsinghua.edu.cn/apache/maven/maven-3/3.9.9/binaries/apache-maven-3.9.9-bin.zip"

	jdk17URL    = "https://mirrors.tuna.tsinghua.edu.cn/Adoptium/17/jdk/x64/windows/OpenJDK17U-jdk_x64_windows_hotspot_17.0.21.1_2.zip"
	jdk17ZipTop = "jdk-17.0.21.1+2"
	maven316URL = "https://mirrors.tuna.tsinghua.edu.cn/apache/maven/maven-3/3.9.16/binaries/apache-maven-3.9.16-bin.zip"
)

// upgradeStaleComponentSources 把旧版内置源迁移到镜像现存版本。
// 只在 URL 精确命中旧默认值时生效，避免覆盖用户自定义源。
func upgradeStaleComponentSources(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Components {
		comp := &c.Components[i]
		switch strings.ToLower(strings.TrimSpace(comp.Name)) {
		case "go":
			if strings.TrimSpace(comp.URL) != staleGo1236 {
				continue
			}
			comp.Version = "1.27.4"
			comp.URL = go1274URL
			comp.SHA256 = ""
		case "node", "node.js":
			if strings.TrimSpace(comp.URL) != staleNode2222 {
				continue
			}
			comp.Version = "24.22.3"
			comp.URL = node24223URL
			comp.ZipTop = node24223Top
			comp.SHA256 = ""
		case "jdk":
			if strings.TrimSpace(comp.URL) != staleJDK17URL {
				continue
			}
			comp.Version = "17.0.21.1"
			comp.URL = jdk17URL
			comp.ZipTop = jdk17ZipTop
			comp.SHA256 = "" // 旧哈希对应旧 zip，必须清掉，否则新包会校验失败
			if comp.ExtraEnv == nil {
				comp.ExtraEnv = map[string]string{}
			}
			comp.ExtraEnv["JAVA_HOME"] = "<install>/" + jdk17ZipTop
		case "maven":
			if strings.TrimSpace(comp.URL) != staleMaven399 {
				continue
			}
			comp.Version = "3.9.16"
			comp.URL = maven316URL
			comp.ZipTop = "apache-maven-3.9.16"
			comp.SHA256 = ""
		}
	}
}
