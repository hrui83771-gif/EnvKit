package main

// 首次引导的「静态契约」门禁。这些是配置/红线级断言，纯文本、零网络，
// 由 go test ./... 覆盖（CI 一定跑），不依赖 Chrome/CDP。
//
// 防的是三类回归：
//  1. 演示页偷偷改成调真实接口 / 执行任意代码 —— 权限闸门必须一行不动；
//  2. 5 个 vendor 产物只下载了但没 embed、没路由、没声明许可 —— 页面会静默降级；
//  3. 首次 8 屏选集被改坏、自动化环境保护被误删 —— 后者会让 33 个既有
//     CDP 回归脚本被全屏 overlay 挡住点击。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readSrc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v", path, err)
	}
	return string(b)
}

func TestOnboardDemoHasNoSideEffects(t *testing.T) {
	s := readSrc(t, "web/src/js/90-onboard.js")

	forbidden := []string{
		"fetch(", "XMLHttpRequest", "new WebSocket", "sendBeacon",
		"document.write", "new Function", "eval(",
	}
	for _, f := range forbidden {
		if strings.Contains(s, f) {
			t.Errorf("演示脚本里出现了 %q：引导页只能玩内置静态数据，不许碰接口/动态执行", f)
		}
	}
	if regexp.MustCompile(`['"` + "`" + `]/api/`).MatchString(s) {
		t.Error("演示脚本里出现了 /api/ 路径：任何接口调用都会把演示变成真实操作")
	}
	// 唯一允许的前端写入是「已看过」标记
	if n := strings.Count(s, "localStorage.setItem"); n != 1 {
		t.Errorf("localStorage.setItem 出现 %d 次，应当恰好 1 次（只有 mark()）", n)
	}
	// 首次完整播 8 屏（v5 起不再有"首次精选 5 屏"），八屏必须按顺序全在
	m := regexp.MustCompile(`FIRST_RUN_SLIDES\s*=\s*\[([^\]]+)\]`).FindStringSubmatch(s)
	if len(m) != 2 {
		t.Fatal("找不到 FIRST_RUN_SLIDES 定义")
	}
	parts := strings.Split(strings.ReplaceAll(m[1], " ", ""), ",")
	if len(parts) != 8 {
		t.Errorf("首次应播 8 屏，实际 %d 屏：%v", len(parts), parts)
	}
	want := []string{"0", "1", "2", "3", "4", "5", "6", "7"}
	for i, w := range want {
		if i < len(parts) && parts[i] != w {
			t.Errorf("首次第 %d 屏应为 %s，实际 %s（完整：%v）", i+1, w, parts[i], parts)
		}
	}
}

func TestOnboardVendorChain(t *testing.T) {
	vendors := []string{
		"swiper-bundle.min.js", "swiper-bundle.min.css",
		"anime.umd.min.js", "driver.js.iife.js", "driver.css",
	}
	mainGo := readSrc(t, "main.go")
	tpl := readSrc(t, "web/src/index.tpl.html")
	notice := readSrc(t, "THIRD-PARTY-NOTICE.md")

	for _, name := range vendors {
		if _, err := os.Stat("web/vendor/" + name); err != nil {
			t.Errorf("vendor 产物缺失：web/vendor/%s", name)
		}
		if !strings.Contains(mainGo, "//go:embed web/vendor/"+name) {
			t.Errorf("main.go 缺 //go:embed：%s", name)
		}
		if !strings.Contains(mainGo, `"/vendor/`+name+`"`) {
			t.Errorf("main.go 缺 /vendor/ 路由：%s", name)
		}
		if !strings.Contains(tpl, `/vendor/`+name) {
			t.Errorf("index.tpl.html 没引用：%s", name)
		}
	}
	for _, v := range []string{"12.2.0", "4.5.0", "1.10.2"} {
		if !strings.Contains(notice, v) {
			t.Errorf("THIRD-PARTY-NOTICE.md 缺版本 %s（许可声明要能对上产物）", v)
		}
	}
	// 零 CDN：head 里不许出现任何非同域的 script/link
	for _, bad := range []string{"https://", "http://"} {
		for _, line := range strings.Split(tpl, "\n") {
			if (strings.Contains(line, "<script") || strings.Contains(line, "<link")) && strings.Contains(line, bad) {
				t.Errorf("发现外链前端资源（违反零 CDN）：%s", strings.TrimSpace(line))
			}
		}
	}
}

func TestOnboardAvatarWaitNotDeadCode(t *testing.T) {
	css := readSrc(t, "web/src/app.css")
	core := readSrc(t, "web/src/js/00-core.js")
	ops := readSrc(t, "web/src/js/40-ops.js")
	ob := readSrc(t, "web/src/js/90-onboard.js")

	if !strings.Contains(css, "@keyframes ekWaitPulse") || !strings.Contains(css, ".ek-wait{") {
		t.Error("app.css 缺 .ek-wait 呼吸环样式：等待态会变成没样式的空 class")
	}
	if !strings.Contains(core, "window.EKAvatarWait") {
		t.Error("00-core.js 缺 EKAvatarWait 实现")
	}
	if !strings.Contains(ops, "EKAvatarWait(") || !strings.Contains(ops, "finally { endCfgWait(); }") {
		t.Error("头像等待态没有接到真实等待上，或没有 finally 保证收尾（会永远转）")
	}
	// 只许一套实现：wait() 必须代理到 EKAvatarWait，不许退回没样式的 ob-waiting
	if strings.Contains(ob, "ob-waiting") || !strings.Contains(ob, "EKAvatarWait") {
		t.Error("EKOnboard.wait 没有复用 EKAvatarWait，或又退回空转 class")
	}
}

func TestOnboardSlidesAndAutomationGuard(t *testing.T) {
	tpl := readSrc(t, "web/src/index.tpl.html")
	for i := 0; i < 8; i++ {
		tag := `data-idx="` + string(rune('0'+i)) + `"`
		if !strings.Contains(tpl, tag) {
			t.Errorf("缺第 %d 屏的 %s", i+1, tag)
		}
	}
	for _, id := range []string{"ob-brand", "ob-swiper", "ob-dots", "about-replay", "ob-cta-tour"} {
		if !strings.Contains(tpl, `id="`+id+`"`) {
			t.Errorf("缺引导关键节点 #%s", id)
		}
	}
	// 自动化环境保护：headless CDP 回归脚本不能被全屏 overlay 挡住
	ops := readSrc(t, "web/src/js/40-ops.js")
	if !strings.Contains(ops, "HeadlessChrome") || !strings.Contains(ops, "navigator.webdriver") {
		t.Error("40-ops.js 的自动化环境保护被删了：既有 CDP 回归会被首次引导挡住点击")
	}
	if !strings.Contains(ops, "EKOnboard.open('first')") {
		t.Error("40-ops.js 缺首次自动播放接线")
	}
}
