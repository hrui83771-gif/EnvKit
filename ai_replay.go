package main

// ai_replay.go —— 上游响应的**录像 / 回放**（v3.7.3）
//
// ## 要解决的是什么
//
// AI 主循环的性质是"状态机 + 上游流"：一次回合里工具调用、复验、确认、收尾
// 全部由**上游吐回来的字节**驱动。而线上的上游行为，我们能用测试覆盖到的部分
// 一直是"测试作者手写的那几帧 SSE"——
//
//	ai_confirm_flow_test.go 里的帧是内联硬编码的 JSON 字符串。
//
// 手写帧只覆盖**写的人想到的形态**。真实模型给回来的东西里，下面这些形态
// 在现有测试里一条都没有：
//
//   - arguments 被拆成多个 chunk 分片到达（我们的累积逻辑靠 += 拼接）
//   - usage 挂在**最后一帧**而不是中间某帧（也有厂商只在末尾单独发一帧 choices 为空）
//   - reasoning_content 与 content 交错
//   - 并行 tool_calls（index 0/1 同时进行）
//   - [DONE] 之后仍可能有残留字节
//
// 这些不是理论风险：它们是**模型/厂商升级时最先变的东西**，而变了以后
// 手上没有任何一条回归用例能告诉我们"改坏了"。
//
// ## 做法
//
// 在**所有到模型上游的 HTTP 出口**上加一个可替换的 seam（aiUpstreamDo），
// 两个环境变量控制它：
//
//	ENVKIT_AI_RECORD=<文件路径>   真实请求照发，同时把响应体原样落盘
//	ENVKIT_AI_REPLAY=<文件路径>   完全不发网络请求，按顺序回放落盘的响应体
//
// 录下来的东西是**原始字节**（未解析的 SSE 文本），所以回放驱动的是与线上
// 完全相同的解析路径 —— 它验证的是"主循环怎么读这些字节"，不是"我们怎么
// 想象主循环"。
//
// ## 为什么用环境变量而不是配置项
//
// 录像会**把模型输出写到磁盘**，这是调试/dev 场景才该发生的事。
// 环境变量的默认值是"关闭"，一次性的、不跨会话、不写进用户配置文件，
// 比在设置页加一个开关更符合"默认安全"。
//
// ## 不记什么
//
// 只记响应体 + 请求的**结构特征**（模型名、消息数、工具数、是否流式）。
// 不记 Authorization、不记 API Key、不记请求体的完整内容。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// aiExchange 一次上游往返的录像条目（一行 JSONL）。
//
// Body 存**原始响应体文本**：SSE 是文本协议，原样存才能保证回放走的是
// 同一条解析路径。用 string 而不是 base64，是为了出问题时能直接看文件。
type aiExchange struct {
	V      int    `json:"v"`                // 格式版本，便于以后无损演进
	Seq    int    `json:"seq"`              // 第几次往返（1 起）
	At     string `json:"at,omitempty"`     // 录制时刻，仅供人看
	Status int    `json:"status"`           // HTTP 状态码
	Stream bool   `json:"stream,omitempty"` // 请求体里 stream=true
	Model  string `json:"model,omitempty"`
	NMsgs  int    `json:"n_msgs,omitempty"`
	NTools int    `json:"n_tools,omitempty"`
	Body   string `json:"body"`
	// Trunc 表示响应体超过 aiRecordMaxBody 被截断 —— 截断过的 fixture
	// 回放出来会和录制时不一致，所以必须显式标出来，不能静默。
	Trunc bool `json:"trunc,omitempty"`
}

const (
	// aiRecordMaxBody 单条录像体的上限。轨迹/回答再长也远小于此；
	// 设上限是为了防止某个模型抽风输出几十 MB 把磁盘写满。
	aiRecordMaxBody = 4 << 20 // 4 MiB
	aiExchangeVer   = 1
)

// aiUpstreamDo 是**所有到模型上游的 HTTP 出口**（流式与非流式共用）。
//
// 抽成变量是为了给录像/回放留一个不碰业务代码的插口。生产路径的行为
// 与直接 `c.Do(req)` 逐字节等价：没设环境变量时这里只是多一次 if。
var aiUpstreamDo = func(c *http.Client, req *http.Request) (*http.Response, error) {
	if resp, ok := aiReplayNext(req); ok {
		return resp, nil
	}
	resp, err := c.Do(req)
	if err != nil {
		return resp, err
	}
	return aiRecordWrap(resp, req), nil
}

// ===== 录像 =====

var (
	aiRecMu   sync.Mutex
	aiRecSeq  int
	aiRecPath string
	aiRecInit bool
)

func aiRecordOn() bool {
	return strings.TrimSpace(os.Getenv("ENVKIT_AI_RECORD")) != ""
}

// aiRecordWrap 给响应体套一层"边读边抄"的包装：不解析、不改写，
// 只是把上游吐出来的字节原样留一份，在 EOF / Close 时落盘。
//
// 为什么必须在**读取过程中**抄而不是先读完再抄：流式响应的 body 是一次性的，
// 读走就没了；而主循环是边读边推给前端的，我们不能替它读完。
func aiRecordWrap(resp *http.Response, req *http.Request) *http.Response {
	if resp == nil || resp.Body == nil || !aiRecordOn() {
		return resp
	}
	h := aiExchange{V: aiExchangeVer, Status: resp.StatusCode}
	// At 用与轨迹一致的格式，方便人在两个文件之间对时间
	h.At = nowStamp()
	if f := aiReqFacts(req); f != nil {
		h.Stream = f.Stream
		h.Model = f.Model
		h.NMsgs = f.NMsgs
		h.NTools = f.NTools
	}
	resp.Body = &aiRecBody{rc: resp.Body, hdr: h}
	return resp
}

// aiReqFacts 从请求体里取**结构特征**（不取内容）。
// 依赖 req.GetBody —— 两个调用点都用 strings.NewReader 构造，因此该字段非 nil。
func aiReqFacts(req *http.Request) *aiRequestBody {
	if req == nil || req.GetBody == nil {
		return nil
	}
	rc, err := req.GetBody()
	if err != nil || rc == nil {
		return nil
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return nil
	}
	var p struct {
		Model    string            `json:"model"`
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	return &aiRequestBody{Model: p.Model, Stream: p.Stream, NMsgs: len(p.Messages), NTools: len(p.Tools)}
}

type aiRequestBody struct {
	Model  string
	Stream bool
	NMsgs  int
	NTools int
}

// aiRecBody 把响应体抄进内存，在流结束时写一条 JSONL。
//
// flush 用 sync.Once：正常路径 EOF 与 defer Close 都会到，且**必须只落一条**。
type aiRecBody struct {
	rc     io.ReadCloser
	buf    bytes.Buffer
	hdr    aiExchange
	once   sync.Once
	trunc  bool
	closed bool
}

func (b *aiRecBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 && !b.trunc {
		if b.buf.Len()+n > aiRecordMaxBody {
			b.trunc = true
		} else {
			b.buf.Write(p[:n])
		}
	}
	if err == io.EOF {
		b.flush()
	}
	return n, err
}

func (b *aiRecBody) Close() error {
	err := b.rc.Close()
	b.flush() // 提前 Close（客户端断开/心跳失败）也要留下已读到的部分
	return err
}

func (b *aiRecBody) flush() {
	b.once.Do(func() {
		e := b.hdr
		e.Body = b.buf.String()
		e.Trunc = b.trunc
		aiRecordAppend(e)
	})
}

func aiRecordAppend(e aiExchange) {
	path := strings.TrimSpace(os.Getenv("ENVKIT_AI_RECORD"))
	if path == "" {
		return
	}
	aiRecMu.Lock()
	defer aiRecMu.Unlock()
	if !aiRecInit || aiRecPath != path {
		aiRecPath, aiRecInit, aiRecSeq = path, true, 0
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	aiRecSeq++
	e.V = aiExchangeVer
	e.Seq = aiRecSeq
	if e.At == "" {
		e.At = nowStamp()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		warn(scSys, "AI", "录像文件无法写入（%v）", err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// ===== 回放 =====

var (
	aiRepMu     sync.Mutex
	aiRepPath   string
	aiRepLines  []aiExchange
	aiRepIdx    int
	aiRepLoaded bool
	aiRepErr    error
)

// aiReplayReset 清掉回放状态，让下一次调用重新读环境变量指定的文件。
// 只给测试用：一次 `go test` 里要跑多个 fixture 时必须换文件。
func aiReplayReset() {
	aiRepMu.Lock()
	aiRepPath, aiRepLines, aiRepIdx, aiRepLoaded, aiRepErr = "", nil, 0, false, nil
	aiRepMu.Unlock()
}

// aiReplayNext 取下线上下一次往返的录制响应。
// ok=false 表示当前没开回放（调用方照常走网络）。
func aiReplayNext(req *http.Request) (*http.Response, bool) {
	path := strings.TrimSpace(os.Getenv("ENVKIT_AI_REPLAY"))
	if path == "" {
		return nil, false
	}
	aiRepMu.Lock()
	defer aiRepMu.Unlock()
	if !aiRepLoaded || aiRepPath != path {
		aiRepPath, aiRepIdx, aiRepErr = path, 0, nil
		aiRepLines, aiRepErr = aiReplayLoad(path)
		aiRepLoaded = true
	}
	if aiRepErr != nil {
		return aiReplayErrResp(aiRepErr), true
	}
	if aiRepIdx >= len(aiRepLines) {
		// **录像用完必须报错，不能返回空响应。**
		// 返回一个 200 + 空体的话，主循环会当成"模型returned空内容"继续往下跑，
		// 测试最后失败在一个完全无关的断言上 —— 排查成本极高。
		return aiReplayErrResp(fmt.Errorf(
			"回放已用完：本回合第 %d 次上游请求没有对应录像（fixture 只有 %d 条）。"+
				"请先用 ENVKIT_AI_RECORD 重新录制，或检查代码是否多发了请求",
			aiRepIdx+1, len(aiRepLines))), true
	}
	e := aiRepLines[aiRepIdx]
	aiRepIdx++
	return aiReplayResp(e), true
}

// aiReplayLoad 读 JSONL fixture。空行跳过；解析失败的行**直接报错**——
// 静默跳过会让"少录了一条"表现为"模型突然不调工具了"，是最难查的那种假象。
func aiReplayLoad(path string) ([]aiExchange, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读录像失败：%w", err)
	}
	var out []aiExchange
	for i, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var e aiExchange
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			return nil, fmt.Errorf("录像第 %d 行解析失败：%w", i+1, err)
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, errors.New("录像文件是空的")
	}
	return out, nil
}

func aiReplayResp(e aiExchange) *http.Response {
	st := e.Status
	if st == 0 {
		st = 200
	}
	body := e.Body
	h := http.Header{}
	// 流式与非流式都给 SSE 的类型：主循环不看这个头，但 SSE 更贴近录制时的真实形态
	if e.Stream || strings.HasPrefix(strings.TrimSpace(body), "data:") {
		h.Set("Content-Type", "text/event-stream")
	} else {
		h.Set("Content-Type", "application/json")
	}
	return &http.Response{
		StatusCode: st,
		Status:     fmt.Sprintf("%d %s", st, http.StatusText(st)),
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// aiReplayErrResp 把回放层的故障伪装成一条 HTTP 响应交给主循环，
// 让它走**既有的错误展示路径**（用户/测试看到的是正常的红字，而不是 panic）。
func aiReplayErrResp(err error) *http.Response {
	return &http.Response{
		StatusCode: 400,
		Status:     "400 Bad Request",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":` + mustJSON(err.Error()) + `}}`)),
	}
}

// nowStamp 与轨迹用同一个时间格式，方便人在 trace-*.jsonl 与录像文件之间对时间。
func nowStamp() string { return time.Now().Format("2006-01-02 15:04:05.000") }
