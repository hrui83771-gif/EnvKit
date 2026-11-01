// 手机端只读 API（状态 / 日志 / 启停 / 对话） —— 从 lanaccess.go 拆出（纯搬家，行为不变）。
// 手机端的安全边界是「独立 mux + 路由少」，拆文件只移动函数，
// 不新增任何路由。

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func lanStateGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := lanAuth(w, r); !ok {
		return
	}
	// v3.6.2：状态一律取自 runtimeService（唯一状态源），**不再自己查 progState**。
	//
	// 旧实现按 {"web","backend"} 去查 progState，可 progState 里的键是
	// "web-start"/"backend-start"（多一个 -start 后缀，是"启动任务"而不是"服务"），
	// 于是 state[kv.key] 永远是空 —— 手机端**永远显示未启动**，用户说"点了没反应"。
	//
	// 更深一层的问题是口径：桌面端显示的状态由 runtimeService 算出五态
	// （starting/running/degraded/failed/stopped），手机端自己另算一套，
	// 两边迟早对不上。手机端拿到的必须是同一个口径。
	type svc struct {
		Key      string `json:"key"`
		Name     string `json:"name"`
		Phase    string `json:"phase"`
		Running  bool   `json:"running"`
		Verified bool   `json:"verified"`
		Msg      string `json:"msg"`
	}
	out := struct {
		OK       bool     `json:"ok"`
		Services []svc    `json:"services"`
		Stats    any      `json:"stats"`
		Version  string   `json:"version"`
		Logs     []string `json:"logs"`
		Port     int      `json:"port"`
		// v3.6.2：这台手机现在能用哪些本地能力。
		// 页面据此显示/隐藏整块卡片 —— 不给"点了才告诉你没开"的体验。
		Caps map[string]bool `json:"caps"`
	}{OK: true, Version: appVersion, Logs: []string{}}

	// 每次查状态都重算而不缓存：开关可能刚在电脑上被改掉，
	// 手机这边 3 秒轮询一次，改完一轮就能看到。
	if c := lanSnap(); c.on() {
		out.Caps = map[string]bool{
			"shot": lanAllowShotOn(c),
			"clip": lanAllowClipOn(c),
			"term": lanAllowTermOn(c) && conptyAvailable(),
			// 链端终端与本地终端同一个开关；额外要求链端 SSH 已配置，
			// 没配就不显示入口 —— 摆一个点进去必然报错的按钮不算"功能"。
			"ssh": lanAllowTermOn(c) && strings.TrimSpace(chainCfg().SSHHost) != "",
		}
	}

	for _, kv := range []struct{ key, name string }{{"web", "前端"}, {"backend", "后端"}} {
		st := runtimeService(kv.key)
		msg := st.LastVerify
		if msg == "" {
			msg = st.LastErr
		}
		out.Services = append(out.Services, svc{
			Key: kv.key, Name: kv.name,
			Phase: string(st.Phase), Running: st.Running, Verified: st.Verified, Msg: msg,
		})
	}
	out.Stats = procStatsSnapshotForLan()
	out.Port = listenPortCur
	lanWriteJSON(w, http.StatusOK, out)
}

// lanSSHWS 手机连链端 Linux 的 SSH 终端。
//
// ## 为什么和本地终端共用一个开关
//
// 用户会直觉认为"只开链端终端"比"开本机终端"安全 —— 事实相反：
// 链端那台机器上通常有 sudo，而手机一旦能登上去，本机这边的所有闸
// （写工具要确认、破坏性操作 428、AI 只读）全都不再是障碍。
// 所以两者是同一档权限，由一个开关统管，不给"看起来更安全"的错觉。
//
// 与桌面的 handleChainTermWS 走**同一份实现**（chainTermWS），
// 只换 upgrader：桌面那份只认回环 Origin，手机 Origin 是局域网地址。
func lanSSHWS(w http.ResponseWriter, r *http.Request) {
	dev, ok := lanFeature(w, r, lanAllowTermOn(lanSnap()), "终端")
	if !ok {
		return
	}
	if c := chainCfg(); strings.TrimSpace(c.SSHHost) == "" {
		http.Error(w, "链端 SSH 未配置：请先在电脑的「程序配置 → 链端」里填写主机与账号",
			http.StatusBadRequest)
		return
	}
	info(scSys, "手机访问", "%s 打开链端终端（%s）", dev.Name, chainCfg().SSHHost)
	auditNow(actUser, "lan_ssh", "chain", dev.ID, resOK, "手机打开链端终端："+dev.Name)
	chainTermWS(w, r, &lanWSUpgrader)
}

// lanLogsGet 手机端实时日志（只读）。
//
// 走 aiGetLogs 而不是另写一份切片：日志的"最近 N 行 + scope 前缀"只有一种口径，
// 两处各自实现迟早对不上（手机看到的行和 AI 看到的行不是同一批，排查时会互相打脸）。
func lanLogsGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := lanAuth(w, r); !ok {
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	switch scope {
	case "", "all", scInstall, scConfig, scStart, scChain, scSys, scEval:
	default:
		lanWriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "未知日志分区"})
		return
	}
	n := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil {
		n = v
	}
	if n < 20 {
		n = 20
	}
	if n > 500 {
		n = 500
	}
	lanWriteJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"scope": scope,
		"lines": aiGetLogs(scope, n),
	})
}

// lanBriefGet 手机端的"统计 + 最近一份晨报"。
//
// 只读**已落盘**的报告，不现场生成：生成会写文件、统计磁盘、还可能触发轮转，
// 一个"看一眼"的请求不该有副作用。没有报告就如实说没有 —— 现编一份
// 会让用户以为系统每天早上都在出报告，其实只是他刚点了一下。
func lanBriefGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := lanAuth(w, r); !ok {
		return
	}
	out := map[string]any{"ok": true}
	// 统计：与桌面晨报同源（briefWorkStats 只读审计与轨迹），不自己另算
	// 否则"手机上的数"和"报告里的数"会不一致。
	wk := briefWorkStats(time.Now())
	out["stats"] = map[string]any{
		"ops":         wk.Ops,
		"ok":          wk.OK,
		"fail":        wk.Fail,
		"denied":      wk.Denied,
		"started":     wk.Started,
		"unverified":  wk.Unverified,
		"verify":      wk.VerifyCount,
		"verify_ok":   wk.VerifyOK,
		"top_actions": wk.Actions,
	}
	dir := briefingDirFn()
	name, at, text := lanBriefLatest(dir)
	out["report"] = name
	out["report_at"] = at
	out["text"] = text
	lanWriteJSON(w, http.StatusOK, out)
}

// lanBriefLatest 取目录里最新的一份晨报（按文件名 = 按时间倒序）。
// 返回文件名、生成时间（文件名里的 2006-01-02-1504）与正文。
func lanBriefLatest(dir string) (string, string, string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", ""
	}
	var best string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".md") || len(n) != len("2006-01-02-1504.md") {
			continue
		}
		if n > best {
			best = n
		}
	}
	if best == "" {
		return "", "", ""
	}
	b, err := os.ReadFile(filepath.Join(dir, best))
	if err != nil {
		return "", "", ""
	}
	text := string(b)
	// 手机屏幕上没人会滚三万字：截断并明说截断，比让页面卡住好。
	const lanBriefMax = 40000
	if len(text) > lanBriefMax {
		text = text[:lanBriefMax] + "\n\n（报告过长，已在手机端截断；完整内容见电脑上的「报告」页）"
	}
	at := strings.TrimSuffix(best, ".md")
	if ts, err := time.ParseInLocation("2006-01-02-1504", at, time.Local); err == nil {
		at = ts.Format("2006-01-02 15:04")
	}
	return best, at, text
}

// lanStartPost 启动前端 / 后端 / 两个都起（由 key 决定）。
//
// ## 复用桌面 handler，但不阻塞手机
//
// 复验必须走桌面的那一条路（handleWebStart / handleBackendStart 内部含
// "启动后当场复验"，自己拼一套必然会漏掉复验，那会让 ever_verified 变成假判据
// —— 这个坑 program.go 里记过）。
//
// 但**不能同步等它跑完**：handleWebStart 会等就绪复验（前端最长 45 秒），
// 手机浏览器那边的请求会一直挂着 —— 用户看到的是"点了没反应"，
// 而且有些手机浏览器/代理会直接超时掐掉请求。
//
// 所以：**动作照做，响应立刻回**。手机拿到"已受理"后靠 /api/lan/state 轮询看进展，
// 页面上显示"启动中，请耐心等待…"，直到服务端给出结论。
func lanStartPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	dev, ok := lanAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body)
	key := strings.TrimSpace(body.Key)
	if key != "web" && key != "backend" && key != "all" {
		lanWriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "key 只能是 web / backend / all"})
		return
	}
	info(scSys, "手机访问", "%s 从手机发起启动：%s", dev.Name, key)
	auditNow(actUser, "lan_start", key, dev.ID, resOK, "来自手机："+dev.Name)

	// 后台执行桌面那套逻辑，输出丢弃（桌面的响应是写给浏览器看的，手机只看 /state）。
	go lanStartDispatch(key)
	lanWriteJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"accepted": true,
		"msg":      "已受理，正在启动（进度看下面的服务状态）",
	})
}

// lanStart 分派表。抽成变量只为可测：分派错时页面照样回"已受理"，
// 用户只会看到某个服务一直"未启动"，唯一能提前抓到的地方是断言这张表
// （见 TestLanStartDispatchByKey）。
var (
	lanStartWeb     = handleWebStart
	lanStartBackend = handleBackendStart
)

// lanStartDispatch 按 key 起对应的服务。
//
// **旧实现校验了 key 却一律调 handleWebStart** —— 手机上点「后端」和「全部启动」
// 起来的都是前端：接口回"已受理"，后端一直显示未启动，看起来像服务本身的问题。
// 三个取值必须对应三种动作，这不是"多一个分支"，是这条 API 的全部语义。
//
// all 的顺序与 AI 的 start_service(all) 一致（先后端再前端）：前端起来后要连后端，
// 反过来会先看到一段连接失败。但这里**不**像 AI 那样把两组锁合成一次原子获取 ——
// 手机这条请求的响应已经先回了，两条腿各自持锁、各自复验即可；
// 跨组原子锁会把一次「全部启动」变成最长 90 秒独占两组资源，代价大于收益。
func lanStartDispatch(key string) {
	switch key {
	case "backend":
		lanStartBackend(lanDiscardWriter{}, lanBgStartRequest())
	case "all":
		lanStartBackend(lanDiscardWriter{}, lanBgStartRequest())
		lanStartWeb(lanDiscardWriter{}, lanBgStartRequest())
	default: // "web"（取值已由 handler 校验，不会有别的）
		lanStartWeb(lanDiscardWriter{}, lanBgStartRequest())
	}
}

// lanBgStartRequest 给后台任务造一份独立请求。
//
// 不能复用 lanStartPost 的 r：handler 一返回它的 context 就被取消、body 也读完了。
// 桌面那两个 handler 只读可选的 dir/script（都允许缺省），所以一份空 JSON 就够。
func lanBgStartRequest() *http.Request {
	r := &http.Request{
		Method: http.MethodPost,
		Header: http.Header{},
		Body:   io.NopCloser(strings.NewReader("{}")),
	}
	return r.WithContext(context.Background())
}

// lanDiscardWriter 丢弃后台任务的响应输出。
//
// 后台跑的 handleWebStart 会往 w 写 JSON；手机端不需要那份响应
// （它看的是 /api/lan/state），但也不能让它写到一个真正的 ResponseWriter 上 ——
// 那会 panic（HeaderMap 里没有的东西）。
type lanDiscardWriter struct{}

func (lanDiscardWriter) Header() http.Header         { return http.Header{} }
func (lanDiscardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (lanDiscardWriter) WriteHeader(int)             {}

func lanStopPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	dev, ok := lanAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body)
	key := strings.TrimSpace(body.Key)
	if key != "web" && key != "backend" {
		key = ""
	}
	info(scSys, "手机访问", "%s 从手机发起停止：%s", dev.Name, firstNonEmpty(key, "全部"))
	auditNow(actUser, "lan_stop", firstNonEmpty(key, "all"), dev.ID, resOK, "来自手机："+dev.Name)

	b, _ := json.Marshal(map[string]string{"key": key})
	r.Body = io.NopCloser(strings.NewReader(string(b)))
	handleStop(w, r)
}

// lanChat 手机上的 AI 对话。
//
// 与桌面走同一个主循环（流式、工具、历史都复用），区别只有一处：
// **只读模式**——模型只看得见非写工具，执行层再拦一次写工具。
// 想让 AI 帮你重启服务？那是「启动/停止」两个按钮的事，两件事分开，
// 模型说错话也弄不坏东西。
func lanChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if _, ok := lanAuth(w, r); !ok {
		return
	}
	ctx := context.WithValue(r.Context(), lanCtxKey{}, true)
	handleAIChat(w, r.WithContext(ctx))
}

type lanCtxKey struct{}

// lanMarked 判断这次请求来自手机端（只读）。
func lanMarked(r *http.Request) bool {
	v, _ := r.Context().Value(lanCtxKey{}).(bool)
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// procStatsSnapshotForLan 手机端要的资源画像：只留"谁在跑、吃多少"。
//
// 不直接复用 procStatsSnapshot()：那里每个进程带 60 点 Hist 和 PID，
// 手机 2 秒轮询一次纯属浪费流量，而 PID 也不该给一个只读客户端。
func procStatsSnapshotForLan() any {
	s := procStatsSnapshot()
	if s == nil {
		return nil
	}
	type row struct {
		Key     string  `json:"key"`
		Label   string  `json:"label"`
		Alive   bool    `json:"alive"`
		CPUPct  float64 `json:"cpu_pct"`
		RSSMB   float64 `json:"rss_mb"`
		Uptime  int64   `json:"uptime_sec,omitempty"`
		Growing bool    `json:"growing"`
	}
	out := struct {
		TS   int64 `json:"ts"`
		CPUs int   `json:"cpus"`
		Rows []row `json:"rows"`
	}{TS: s.TS, CPUs: s.CPUs}
	for _, p := range s.Procs {
		if p.Key == "self" || p.Key == "web" || p.Key == "backend" {
			out.Rows = append(out.Rows, row{
				Key: p.Key, Label: p.Label, Alive: p.Alive,
				CPUPct: p.CPUPct, RSSMB: p.RSSMB, Uptime: p.Uptime, Growing: p.Growing,
			})
		}
	}
	return out
}

// ===== 桌面侧管理端点 =====
//
// 挂在**主控制台** mux 上（受 authMW + 本机令牌保护），不是 LAN mux 上。
// 手机端永远看不到自己的开关和配对码 —— 那两样只能在电脑上看。
