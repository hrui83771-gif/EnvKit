package main

import (
	"strings"

	"envkit/internal/platform"
)

// bannerLines 生成启动信息块。做成纯函数（颜色开关由参数传入），
// 便于单测断言「版本/地址/状态」三要素齐全，且不依赖真控制台。
//
// 为什么重写横幅：原来那圈 '====' 纯白字混在一堆 INFO 日志里，
// 用户第一眼分不清「这是状态说明」还是「又一条日志」。新版本用
// 固定标签列 + 分级配色，让三件最要紧的事一眼可见：
//
//	版本（这是哪个 EnvKit）、地址（去哪里操作）、状态（窗口能不能关）。
func bannerLines(openURL, version string, color bool) []string {
	pad := func(s string) string {
		n := len([]rune(s))
		for n < 4 {
			s += "　" // 全角空格：等宽字体下补一列中文宽度
			n++
		}
		return s
	}
	line := func(label, level, value string) string {
		return "  " + pad(label) + "  " + platform.Colorize(level, value, color)
	}
	rule := strings.Repeat("─", 52)
	if color {
		rule = platform.Colorize("INFO", rule, true)
	}
	return []string{
		"",
		"  " + platform.Colorize("OK", "EnvKit 环境助手  v"+version, color),
		"  " + rule,
		line("服务状态", "OK", "运行中 · 请保持此窗口开启（关闭会导致网页不可访问）"),
		line("控制台", "INFO", openURL+"  ← 浏览器没自动打开时，复制这个地址"),
		line("退出方式", "HINT", "Ctrl+C  或  托盘右键「退出 EnvKit」  或  网页「退出程序」"),
		line("托盘操作", "HINT", "打开控制台 / 程序配置 / 打开日志目录 / 隐藏窗口到托盘"),
		"  " + rule,
		"",
	}
}
