package main

// runtime_frontend_contract_test.go —— 前端要用的字段，后端必须真的给
//
// ## 为什么要这个文件
//
// 一次真实盘点发现：`/api/runtime/state`（唯一状态源）建了四个版本，
// **前端零消费**。追根因不是「前端没写」，而是：
//
// ```go
// // 旧形状：只保留 running/url/pid/since，Phase/Verified 等新字段走 /api/runtime/state
// "services": healthServicesCompat(st.Services),
// ```
//
// **兼容层把 `phase` 和 `verified` 丢掉了。**
// 前端拿不到这两个字段，就只能按 `running` 上色——
// 于是端口被别人的服务占着、进程在但没复验，也一样显示绿。
//
// ## 教训
//
// 「唯一状态源」建好了还不够，**必须有一条路径让前端真的能读到它的关键字段**。
// 否则它就只是一个"后端自己看着 nice"的内部结构。
// 断链点通常在**兼容层**——它为了"不破坏旧调用方"而丢掉新字段，
// 而"不破坏"与"可用"在这里恰好冲突。

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestHealthServicesExposesVerify 兼容层必须把复验结论透出。
//
// 这是**防复发**的核心断言：一旦有人为了"保持旧形状"把字段删掉，这里立刻红。
func TestHealthServicesExposesVerify(t *testing.T) {
	got := healthServicesCompat(map[string]ServiceState{
		"web": {
			Running: true, PID: 42, Port: 8080,
			Phase: phRunning, Verified: true,
		},
		"backend": {
			// 关键场景：进程在、端口在，但**端口是别人的服务** → 未通过复验
			Running: true, PID: 99, Port: 8888,
			Phase: phDegraded, Verified: false,
			LastVerify: "端口 8888 被 main.exe 占用，与预期进程不符，不能认定已就绪",
		},
	})

	w, ok := got["web"]
	if !ok {
		t.Fatal("应包含 web")
	}
	if !w.Verified {
		t.Error("web 已复验，Verified 应为 true")
	}
	if w.Phase != phRunning {
		t.Errorf("web phase = %q，期望 %q", w.Phase, phRunning)
	}

	b := got["backend"]
	if b.Verified {
		t.Error("backend 未通过复验，Verified 必须为 false —— 这是本测试存在的理由")
	}
	if b.Phase != phDegraded {
		t.Errorf("backend phase = %q，期望 %q", b.Phase, phDegraded)
	}
	// LastVerify 让人能在界面上直接显示「为什么没验过」，
	// 而不是只给一个颜色让用户猜
	if !strings.Contains(b.LastVerify, "占用") {
		t.Errorf("LastVerify 应保留具体原因，实际 %q", b.LastVerify)
	}
	// 旧字段一个都不能少（CDP 测试与既有前端依赖它们）
	if !w.Running || w.PID != 42 || w.URL != "http://127.0.0.1:8080" {
		t.Errorf("旧字段被破坏了：%+v", w)
	}
}

// TestHealthJSONIncludesVerifyFields JSON 层也要有这些字段。
//
// 上一条测的是 Go 结构体；这条测**真的序列化出去** ——
// 两者不是一回事：json tag 写错时结构体对、JSON 里就没有。
func TestHealthJSONIncludesVerifyFields(t *testing.T) {
	m := healthServicesCompat(map[string]ServiceState{
		"web": {Running: true, Port: 8080, Phase: phRunning, Verified: true},
	})
	b, err := json.Marshal(m["web"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, k := range []string{`"running"`, `"phase"`, `"verified"`, `"url"`} {
		if !strings.Contains(s, k) {
			t.Errorf("JSON 缺字段 %s：%s", k, s)
		}
	}
	// running 必须仍然存在且为 true —— 不能因为加了新字段就改了旧语义
	if !strings.Contains(s, `"running":true`) {
		t.Errorf("旧字段语义变了：%s", s)
	}
}

// TestHealthHandlerCarriesIssues /api/health 必须带 issues。
//
// 前端 renderIssues 依赖它，且**每条要带 action**——
// 只报问题不给下一步，等于把负担推回给用户。
func TestHealthHandlerCarriesIssues(t *testing.T) {
	runtimeInvalidateCache()
	st := currentRuntimeState()
	if st == nil {
		t.Fatal("状态源不应返回 nil")
	}
	// issues 不得为 nil：前端直接遍历，null 会让 map/forEach 报错
	if st.Issues == nil {
		t.Error("issues 不该为 nil")
	}
	for _, is := range st.Issues {
		if strings.TrimSpace(is.What) == "" {
			t.Error("issue 不能没有 what")
		}
		if strings.TrimSpace(is.Action) == "" {
			t.Errorf("issue %q 没有 action —— 只报问题不给下一步等于把负担推回给用户", is.What)
		}
	}
}

// TestHealthCheckPaneShowsActions 「本机体检」必须把 issues[].action 显示出来。
//
// 后端为每条问题都算好了 action，但设置页原来只写「N 个警告」——
// **信息在渲染层丢了**，用户知道有两个警告却不知道下一步点哪里。
// 这条测试直接盯 web/src 的两个位置：容器在模板里，action 在渲染函数里。
// 谁把其中一半删掉，这里立刻红（与 TestBuiltHTMLHasMissionTab 同一个思路）。
func TestHealthCheckPaneShowsActions(t *testing.T) {
	js := readFrontendJS(t)
	for _, want := range []string{
		"s_health_issues", // 渲染目标
		"i.action",        // 读的是 action，不是只数个数
		"建议：{0}",          // 与首页同一句「该怎么办」
	} {
		if !strings.Contains(js, want) {
			t.Errorf("前端脚本缺 %q —— 体检只报数量、把 action 丢了", want)
		}
	}
	tpl, err := os.ReadFile("web/src/index.tpl.html")
	if err != nil {
		t.Fatalf("读 web/src/index.tpl.html: %v", err)
	}
	if !strings.Contains(string(tpl), `id="s_health_issues"`) {
		t.Error("模板里没有 #s_health_issues —— 渲染函数写了也无处落地")
	}
}
