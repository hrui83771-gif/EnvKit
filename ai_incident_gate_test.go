package main

// ai_incident_gate_test.go —— 事故语料回放门禁（eval/incidents/）
//
// ## 为什么要有这一层
//
// 11/4 连着两次「单测/CDP 全绿、用户先在界面上撞到」：
//   · 丢 docs：CDP 把整个 aiConsume 桩掉去验传参，被测的白名单重建根本不执行；
//   · 文档污染：意图单测只喂短句，CDP 桩掉 fetch 不走服务端预检，
//     真实 AI 对话又是直接构造 API 绕过前端发送。
//
// 三次都是**观测点不在真正出问题的那一层**。所以这里刻意做两件事：
//
//  1. 驱动 `handleAIChat` 而不是拼几条 aiMsg 直接调 aiRunLoop —— 请求体解析、
//     文档落盘、hydrate、预检、主循环、SSE 全走一遍；
//  2. 断言不只看 SSE，还包一层 aiUpstreamDo 抓**真正发给上游的请求字节**。
//     光看模型回答是空转：回答是录像里写死的，它证明不了材料有没有被拼进去。
//
// 上游用 ENVKIT_AI_REPLAY 完全离线回放，BaseURL 指向必然连不上的 127.0.0.1:1：
// 只要真的走了网络，用例立刻失败。零 token、零依赖、可重复。
//
// ## 与现有单测的分工
//
// 这里验的是「事故不复现」（黑盒）；各道闸本身的独立可观测性由
// ai_doc_mode_test.go 的 8 组变异守。两者不互相替代 —— 事故往往需要同时拆掉
// 两道纵深防御才会复现，单点变异抓不到它，这是上一轮实测过的。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- 语料结构（与 eval/incidents/cases.json 一一对应） ----------

type incidentSuite struct {
	Version int            `json:"version"`
	Note    string         `json:"note"`
	Cases   []incidentCase `json:"cases"`
}

type incidentDoc struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
}

type incidentToolSpec struct {
	Stub  string `json:"stub"`
	Watch bool   `json:"watch"`
}

type incidentAssert struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

type incidentMutation struct {
	File    string `json:"file"`
	Find    string `json:"find"`
	Replace string `json:"replace"`
	Note    string `json:"note"`
}

type incidentCase struct {
	ID        string                      `json:"id"`
	Incident  string                      `json:"incident"`
	Date      string                      `json:"date"`
	Fix       string                      `json:"fix"`
	Recording string                      `json:"recording"`
	UserText  string                      `json:"user_text"`
	Doc       *incidentDoc                `json:"doc"`
	Tools     map[string]incidentToolSpec `json:"tools"`
	Asserts   []incidentAssert            `json:"asserts"`
	Mutations []incidentMutation          `json:"mutations"`
}

func loadIncidentSuite(t *testing.T) incidentSuite {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("eval", "incidents", "cases.json"))
	if err != nil {
		t.Fatalf("读事故语料清单失败：%v", err)
	}
	var s incidentSuite
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("cases.json 解析失败：%v", err)
	}
	if s.Version != 1 || len(s.Cases) == 0 {
		t.Fatalf("cases.json 版本/条目异常：version=%d cases=%d", s.Version, len(s.Cases))
	}
	for _, c := range s.Cases {
		if c.ID == "" || c.Recording == "" || len(c.Asserts) == 0 {
			t.Fatalf("语料条目字段不全：%+v", c)
		}
		if len(c.Mutations) == 0 {
			t.Fatalf("语料 %s 没有配变异点——没有变异门禁的断言等于没有", c.ID)
		}
	}
	return s
}

// ---------- 观测：真正发给上游的请求字节 ----------

type capturedTool struct {
	calls   int
	results []string
}

func captureUpstreamRequests(t *testing.T) *[][]byte {
	t.Helper()
	bodies := &[][]byte{}
	orig := aiUpstreamDo
	aiUpstreamDo = func(c *http.Client, req *http.Request) (*http.Response, error) {
		if req.GetBody != nil {
			if rc, err := req.GetBody(); err == nil {
				b, _ := io.ReadAll(rc)
				_ = rc.Close()
				*bodies = append(*bodies, b)
			}
		}
		return orig(c, req)
	}
	t.Cleanup(func() { aiUpstreamDo = orig })
	return bodies
}

func installIncidentTools(t *testing.T, specs map[string]incidentToolSpec) map[string]*capturedTool {
	t.Helper()
	caps := map[string]*capturedTool{}
	for name, spec := range specs {
		name, spec := name, spec
		old, ok := aiToolRegistry[name]
		if !ok {
			t.Fatalf("语料要观测的工具 %s 没注册", name)
		}
		cap := &capturedTool{}
		caps[name] = cap
		replacement := old
		if spec.Stub != "" {
			replacement.Execute = func(map[string]any) (string, error) {
				cap.calls++
				cap.results = append(cap.results, spec.Stub)
				return spec.Stub, nil
			}
		} else {
			replacement.Execute = func(a map[string]any) (string, error) {
				res, err := old.Execute(a)
				cap.calls++
				cap.results = append(cap.results, res)
				return res, err
			}
		}
		aiToolRegistry[name] = replacement
		t.Cleanup(func() { aiToolRegistry[name] = old })
	}
	return caps
}

// ---------- 门禁主体 ----------

func TestIncidentGate(t *testing.T) {
	suite := loadIncidentSuite(t)
	for _, c := range suite.Cases {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			runIncidentCase(t, c)
		})
	}
}

func runIncidentCase(t *testing.T, c incidentCase) {
	withTempDocDir(t)
	// 这两个 helper 返回的是**显式 undo**（内部没有注册 t.Cleanup），
	// 必须 defer 调用；写成 withAIMissionEnv(t)() 会当场把隔离撤掉。
	undoEnv := withAIMissionEnv(t)
	defer undoEnv()

	// BaseURL 故意指向必然连不上的端口：真走了网络这条用例就必须失败。
	oldAI := cfg.AI
	cfg.AI = &AIConfig{Enabled: true, Provider: "test",
		BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "incident-replay"}
	oldRO := aiReadOnly.Load()
	aiReadOnly.Store(false)
	t.Cleanup(func() {
		cfg.AI = oldAI
		aiReadOnly.Store(oldRO)
	})

	// 组装请求体：文档正文先真落盘，请求里只带引用（与前端发送形状一致）。
	docID := ""
	msg := map[string]any{"role": "user", "content": c.UserText}
	if c.Doc != nil {
		text, err := os.ReadFile(filepath.Join("eval", "incidents", c.Doc.File))
		if err != nil {
			t.Fatalf("读语料文档 %s 失败：%v", c.Doc.File, err)
		}
		docID = mustStoreDoc(t, string(text))
		msg["docs"] = []map[string]any{{
			"id": docID, "name": c.Doc.Name, "kind": c.Doc.Kind,
			"chars": len([]rune(string(text))),
		}}
	}
	bodyBytes, _ := json.Marshal(map[string]any{
		"messages": []any{msg}, "lang": "zh",
	})

	// 录像里的 {{DOC_ID}} 用真实落盘 id 渲染（id 是随机的，静态文件写不了）。
	recSrc, err := os.ReadFile(filepath.Join("eval", "incidents", c.Recording))
	if err != nil {
		t.Fatalf("读录像 %s 失败：%v", c.Recording, err)
	}
	recText := strings.ReplaceAll(string(recSrc), "{{DOC_ID}}", docID)
	recPath := filepath.Join(t.TempDir(), "replay.jsonl")
	if err := os.WriteFile(recPath, []byte(recText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENVKIT_AI_REPLAY", recPath)
	t.Setenv("ENVKIT_AI_RECORD", "")
	aiReplayReset()

	reqBodies := captureUpstreamRequests(t)
	caps := installIncidentTools(t, c.Tools)

	req := httptest.NewRequest(http.MethodPost, "/api/ai/chat", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Content-Type", "application/json")
	// 写操作确认卡会原地等 10 分钟（aiConfirmWaitMax）。ctx 到点后 aiAwaitConfirm
	// 会干净退出，于是「被改坏」的那一版能快速变红，而不是把测试挂死。
	ctx, cancel := context.WithTimeout(req.Context(), 4*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handleAIChat(rec, req)

	sse := rec.Body.String()
	upstream := strings.Join(func() []string {
		out := make([]string, 0, len(*reqBodies))
		for _, b := range *reqBodies {
			out = append(out, string(b))
		}
		return out
	}(), "\n")

	if upstream == "" {
		// 只能用 Errorf、不能用 Fatalf：写操作预检命中时会原地等确认、根本不发上游请求，
		// 那正是「事故复现」的形态。Fatal 会在这里提前终止，后面的 sse_absent
		// confirm_request 断言一次都不执行 —— 变异看起来红了，其实是空转假验证。
		t.Errorf("没有捕获到任何上游请求体 —— 请求体观测点失效。SSE 尾部：%s", tailOf(sse, 400))
	}

	for _, a := range c.Asserts {
		if !checkIncidentAssert(a, sse, upstream, caps) {
			t.Errorf("[%s] 断言失败 %s(%q)%s\n  SSE 尾部: %s\n  请求体片段: %s",
				c.ID, a.Kind, a.Value, noteOf(a.Note), tailOf(sse, 700), firstLines(upstream, 700))
		}
	}
}

func noteOf(s string) string {
	if s == "" {
		return ""
	}
	return " —— " + s
}

func checkIncidentAssert(a incidentAssert, sse, upstream string, caps map[string]*capturedTool) bool {
	switch a.Kind {
	case "sse_present":
		return strings.Contains(sse, a.Value)
	case "sse_absent":
		return !strings.Contains(sse, a.Value)
	case "req_present":
		return upstream != "" && strings.Contains(upstream, a.Value)
	case "req_absent":
		// 空串里当然「不含」，但不能拿它当证据：没抓到请求体时 req_absent 会永远为真。
		return upstream != "" && !strings.Contains(upstream, a.Value)
	case "no_error":
		return !strings.Contains(sse, `"type":"error"`)
	case "tool_called":
		cap := caps[a.Value]
		return cap != nil && cap.calls > 0
	case "tool_result_contains":
		// 工具真读盘的结果，不是录像里写死的模型措辞。
		for _, cap := range caps {
			for _, r := range cap.results {
				if strings.Contains(r, a.Value) {
					return true
				}
			}
		}
		return false
	default:
		panic(fmt.Sprintf("未知断言类型 %q（cases.json 写错了）", a.Kind))
	}
}
