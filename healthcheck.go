package main

// healthcheck.go —— 全链路体检（v3.9 P0）
//
// 一句话：按顺序把已有检测全部跑一遍，回答「这套环境现在能不能交付」。
//
// 为什么要单独做：环境检测只看组件、链端校验只看链、诊断报告只是把已有快照
// 拼成 Markdown（没跑过就写"未检测"，而且不含服务/端口，更没有结论）。
// 三段各自都能跑，但没有人把它们合起来给一句人话。
//
// ===== 三条红线 =====
// 1. 只读。不启动服务、不建库、不自动恢复、不复位熔断。
//    不通过的项只给 Acts（点页面上已有的按钮），写操作照旧走 428 确认。
//    唯一例外是「检测本身会刷新自己的快照」（results / dbHealth / chainInfo），
//    这是用户主动发起的一次真实检测，不是状态篡改。
// 2. 绝不调用 verifyService。它会写 svcRecordVerify、累加连续失败次数、
//    影响熔断与守护策略——「体检一遍」不该改变被体检对象的状态。
//    这里只用 svcAlive / firstListeningPort / portOwner / httpProbe 这些只读探针。
// 3. 跳过 ≠ 失败，未知 ≠ 0。没配链端/数据库就整段跳过并写明原因；
//    配了却连不上才算失败。Skip 不计入 Deliverable 的否决。
//
// 一个口径：跑完调 runtimeInvalidateCache()，让 /api/state 的「当前待处理」
// 用同一套状态源重新算，不产生第二份真相。

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	hcPass = "pass"
	hcFail = "fail"
	hcSkip = "skip"
)

// 分段。前端按 group 分组渲染，文案由 t() 映射。
const (
	hcgEnv     = "env"
	hcgProject = "project"
	hcgDB      = "db"
	hcgChain   = "chain"
	hcgService = "service"
	hcgRuntime = "runtime"
)

// HCItem 一项检查。Detail 必须写「凭什么这么判」，不能只有一个 ✅。
type HCItem struct {
	ID     string   `json:"id"`
	Group  string   `json:"group"`
	Title  string   `json:"title"`
	Status string   `json:"status"` // pass | fail | skip
	Detail string   `json:"detail"`
	Acts   []string `json:"acts,omitempty"`
}

// HCReport 一次体检的完整结果。Status=idle/running/done。
type HCReport struct {
	Status      string   `json:"status"`
	Progress    string   `json:"progress"`
	Done        int      `json:"done"`
	Total       int      `json:"total"`
	StartedAt   string   `json:"started_at"`
	FinishedAt  string   `json:"finished_at"`
	Version     string   `json:"version"`
	Items       []HCItem `json:"items"`
	Pass        int      `json:"pass"`
	Fail        int      `json:"fail"`
	Skip        int      `json:"skip"`
	Deliverable bool     `json:"deliverable"`
	Conclusion  string   `json:"conclusion"`
	// Steps 是每项完成的真实留痕（不是动画编出来的）。前端按它回放阶段名，
	// 让几毫秒就跑完的本地项也看得见；Done 永不超过后端真实完成数。
	Steps []HCStep `json:"steps,omitempty"`
	// Restored：这份报告是从上次运行的历史里恢复的，不是刚跑完的。
	Restored bool `json:"restored,omitempty"`
}

// HCStep 一项完成时的留痕。AtMs 用绝对毫秒，前端只拿它算相邻两项的真实间隔。
type HCStep struct {
	Done  int    `json:"done"`
	Title string `json:"title"`
	AtMs  int64  `json:"at_ms"`
}

var (
	hcMu      sync.RWMutex
	hcCur     = HCReport{Status: "idle"}
	hcHistory []HCReport // 最近 3 份完整报告（最新在前），随 state.json 落盘
)

const hcHistoryMax = 3

// 总项数。编排与进度条共用，改项时只改这里。
const hcTotalItems = 22

func hcSnapshot() HCReport {
	hcMu.RLock()
	defer hcMu.RUnlock()
	r := hcCur
	r.Items = append([]HCItem(nil), hcCur.Items...)
	return r
}

func hcPublish(r HCReport) {
	hcMu.Lock()
	hcCur = r
	hcMu.Unlock()
}

func hcBegin() HCReport {
	r := HCReport{
		Status: "running", Progress: "环境组件", Done: 0, Total: hcTotalItems,
		StartedAt: time.Now().Format("2006-01-02 15:04:05"),
		Version:   appVersion, Items: []HCItem{}, Steps: []HCStep{},
	}
	hcPublish(r)
	return r
}

// hcStep 每项跑完立刻更新进度：前端轮询 GET 就能显示阶段，不用改 SSE 管道。
// 留痕是给展示层用的：前 8 项都是本地文件检查，几毫秒就过完，
// 800ms 的轮询会把它们全跨过去，用户只看到进度条从 0 跳到 8。
func hcStep(r *HCReport, phase string) {
	r.Done++
	r.Progress = phase
	if r.Done > r.Total {
		r.Total = r.Done
	}
	r.Steps = append(r.Steps, HCStep{Done: r.Done, Title: phase, AtMs: time.Now().UnixMilli()})
	info(scSys, "体检", "[%d/%d] %s", r.Done, r.Total, phase)
	hcPublish(*r)
}

func hcAdd(r *HCReport, group, id, title, status, detail string, acts ...string) {
	// 跳过必须写原因：「没查」不许伪装成「查过没问题」。
	if status == hcSkip && strings.TrimSpace(detail) == "" {
		detail = "未说明原因（本次未核实）"
	}
	r.Items = append(r.Items, HCItem{
		ID: id, Group: group, Title: title, Status: status,
		Detail: detail, Acts: acts,
	})
	hcStep(r, title)
}

// ===== 结论：纯函数，单测直接喂 items =====

// hcSummarize 统计并给出交付结论。
// Deliverable 只由 Fail 决定：Skip 是「没核实/没配置」，不构成否决，
// 但结论里必须如实写出跳过数，不许把「没查」说成「查过没问题」。
func hcSummarize(items []HCItem) (pass, fail, skip int, deliverable bool, conclusion string) {
	var bad []string
	for _, it := range items {
		switch it.Status {
		case hcPass:
			pass++
		case hcFail:
			fail++
			bad = append(bad, it.Title)
		case hcSkip:
			skip++
		}
	}
	deliverable = fail == 0
	head := fmt.Sprintf("%d 项通过 / %d 项不通过 / %d 项跳过", pass, fail, skip)
	// 结论词由前端的大标题给，这里只写统计与下一步，不重复一遍「暂不能交付」。
	switch {
	case fail == 0 && skip == 0:
		conclusion = head + "。全部检查通过。"
	case fail == 0:
		conclusion = head + "。已检查的项目全部通过；跳过项未核实，不代表通过。"
	case len(bad) <= 3:
		conclusion = head + "。先处理：" + strings.Join(bad, "、") + "。"
	default:
		conclusion = head + "。先处理：" + strings.Join(bad[:3], "、") +
			fmt.Sprintf(" 等 %d 项。", len(bad))
	}
	return
}

func hcFinish(r *HCReport) {
	r.Pass, r.Fail, r.Skip, r.Deliverable, r.Conclusion = hcSummarize(r.Items)
	r.FinishedAt = time.Now().Format("2006-01-02 15:04:05")
	r.Progress = "完成"
	r.Status = "done"
	hcPublish(*r)
	// 同一口径：让 /api/state 用刚刷新的快照重算「当前待处理」
	runtimeInvalidateCache()
	if r.Deliverable {
		ok(scSys, "体检", "全链路体检完成：%s", r.Conclusion)
	} else {
		warn(scSys, "体检", "全链路体检完成：%s", r.Conclusion)
	}
	// 留最近 3 份完整报告并立刻落盘。存完整报告而不是只存计数：
	// 重启后界面要能把上一轮证据原样摆出来（标 Restored），还要能对比「少了哪几项」。
	hcMu.Lock()
	cp := *r
	cp.Steps = append([]HCStep(nil), r.Steps...)
	cp.Items = append([]HCItem(nil), r.Items...)
	hist := append([]HCReport{cp}, hcHistory...)
	if len(hist) > hcHistoryMax {
		hist = hist[:hcHistoryMax]
	}
	hcHistory = hist
	hcMu.Unlock()
	// 体检把环境/项目/库/链端/服务全量跑过一遍，本身就是一次复检；
	// 复用 setCfgStale 会多存一次盘，这里改状态后统一存一次。
	uiMu.Lock()
	cfgStaleState = false
	cfgStaleAt = ""
	uiMu.Unlock()
	savePersistedState()
}

// ===== 编排：全部只读 =====

func runHealthCheck() {
	r := hcBegin()
	fin := auditStart(actUser, "health_check", "", "")
	defer func() {
		if rec := recover(); rec != nil {
			// 某一项探针 panic 不能让整份报告停在 running（前端会永远转圈）
			hcAdd(&r, hcgRuntime, "hc.panic", "体检自身异常", hcFail,
				fmt.Sprintf("编排被中断：%v；已完成的 %d 项结果保留", rec, r.Done))
			hcFinish(&r)
			fin(resFail, fmt.Sprintf("%v", rec))
			return
		}
		fin(resOK, r.Conclusion)
	}()

	hcCheckEnv(&r)
	hcCheckProject(&r)
	hcCheckDB(&r)
	hcCheckChain(&r)
	hcCheckService(&r)
	hcCheckRuntime(&r)
	hcFinish(&r)
}

// ---- A. 环境（3 项）----
func hcCheckEnv(r *HCReport) {
	if len(cfg.Components) == 0 {
		hcAdd(r, hcgEnv, "env.components", "组件齐全", hcSkip, "没有配置要检测的组件，跳过")
	} else {
		runCheck() // 复用环境检测：会刷新 results 快照（检测本身的产物）
		env := runtimeStateSlow()
		if !env.HasResults {
			hcAdd(r, hcgEnv, "env.components", "组件齐全", hcSkip, "检测没返回任何结果，未核实", "check")
		} else if len(env.Missing) > 0 {
			hcAdd(r, hcgEnv, "env.components", "组件齐全", hcFail,
				"缺少："+strings.Join(env.Missing, "、"), "check", "install")
		} else {
			hcAdd(r, hcgEnv, "env.components", "组件齐全", hcPass,
				fmt.Sprintf("%d 个组件都已探测到", len(cfg.Components)))
		}
	}

	// 装了不等于够用；但探测不到版本只算「未核实」（runtime.go 同一判据）
	var tooLow []string
	unknown := 0
	for _, q := range checkAllEnvReq() {
		switch q.Status {
		case reqTooLow:
			tooLow = append(tooLow, q.Component+"："+q.Why)
		case reqMismatch:
			// config.toml 链地址与链端配置不一致：后端会连错节点，属客观不满足
			tooLow = append(tooLow, q.Component+"："+q.Why)
		case reqNotInstall, reqUndeclared:
			unknown++
		}
	}
	switch {
	case len(tooLow) > 0:
		hcAdd(r, hcgEnv, "env.version", "版本符合项目要求", hcFail,
			strings.Join(tooLow, "；"), "goto:install")
	case unknown > 0:
		hcAdd(r, hcgEnv, "env.version", "版本符合项目要求", hcSkip,
			fmt.Sprintf("%d 项版本/要求探测不到，未核实", unknown), "check")
	default:
		hcAdd(r, hcgEnv, "env.version", "版本符合项目要求", hcPass, "已配置的要求全部满足")
	}

	if isAdminFlag {
		hcAdd(r, hcgEnv, "env.admin", "管理员权限", hcPass, "当前以管理员身份运行")
	} else {
		hcAdd(r, hcgEnv, "env.admin", "管理员权限", hcSkip,
			"非管理员：目录白名单等能力可能受限，但日常检测/启动不需要，不判失败")
	}
}

// ---- B. 项目（5 项）----
func hcCheckProject(r *HCReport) {
	p := runtimeProject()
	front := strings.TrimSpace(p.FrontendDir)
	if front == "" {
		hcAdd(r, hcgProject, "proj.frontend_dir", "前端目录", hcFail, "未配置前端目录", "focus:fe_dir")
	} else if fi, err := os.Stat(front); err != nil || !fi.IsDir() {
		hcAdd(r, hcgProject, "proj.frontend_dir", "前端目录", hcFail,
			"目录不存在或不可读："+front, "focus:fe_dir")
	} else if _, err := os.Stat(filepath.Join(front, "package.json")); err != nil {
		hcAdd(r, hcgProject, "proj.frontend_dir", "前端目录", hcFail,
			"目录里没有 package.json："+front, "focus:fe_dir")
	} else {
		hcAdd(r, hcgProject, "proj.frontend_dir", "前端目录", hcPass, front)
	}

	switch {
	case p.WebDepsUnknown:
		hcAdd(r, hcgProject, "proj.web_deps", "前端依赖", hcSkip, "前端目录未配置或不可读，未核实")
	case !p.WebDepsInstalled:
		hcAdd(r, hcgProject, "proj.web_deps", "前端依赖", hcFail,
			"node_modules 缺失，npm run 必然失败", "deps:web")
	default:
		hcAdd(r, hcgProject, "proj.web_deps", "前端依赖", hcPass, "node_modules 已存在")
	}

	if p.WebSource == "none" {
		hcAdd(r, hcgProject, "proj.web_script", "前端启动脚本", hcFail,
			"识别不出启动脚本，需手动填写", "focus:cfg_web_script")
	} else {
		hcAdd(r, hcgProject, "proj.web_script", "前端启动脚本", hcPass,
			p.Web+"（来源："+p.WebSource+"）")
	}

	back := strings.TrimSpace(p.BackendDir)
	switch {
	case back == "":
		hcAdd(r, hcgProject, "proj.backend", "后端目录与入口", hcSkip, "未配置后端目录，跳过")
	case p.Backend == "":
		hcAdd(r, hcgProject, "proj.backend", "后端目录与入口", hcFail,
			"识别不出后端入口，需手动填写", "focus:cfg_backend_file")
	default:
		hcAdd(r, hcgProject, "proj.backend", "后端目录与入口", hcPass, back+" / "+p.Backend)
	}

	sqlFile := strings.TrimSpace(cfg.Projects.SQLFile)
	if sqlFile == "" {
		hcAdd(r, hcgProject, "proj.sql", "SQL 建表文件", hcSkip, "未配置 SQL 文件，跳过")
	} else if _, err := os.Stat(sqlFile); err != nil {
		hcAdd(r, hcgProject, "proj.sql", "SQL 建表文件", hcFail, "文件不存在："+sqlFile, "focus:sql_file")
	} else {
		hcAdd(r, hcgProject, "proj.sql", "SQL 建表文件", hcPass, sqlFile)
	}
}

// ---- C. 数据库（3 项）----
func hcCheckDB(r *HCReport) {
	dbName := strings.TrimSpace(cfg.Projects.DBName)
	if dbName == "" {
		for _, id := range []string{"db.connect", "db.exists", "db.tables"} {
			hcAdd(r, hcgDB, id, map[string]string{
				"db.connect": "MySQL 可连接", "db.exists": "目标库存在", "db.tables": "库内已有数据表",
			}[id], hcSkip, "未配置目标数据库，跳过")
		}
		return
	}
	h, err := dbTest() // 只读探测；不写 dbHealth，避免「体检」悄悄改写状态源
	if err != nil || !h.Connected {
		why := "连接失败"
		if err != nil {
			why = firstLines(err.Error(), 120)
		}
		hcAdd(r, hcgDB, "db.connect", "MySQL 可连接", hcFail, why, "dbtest")
		hcAdd(r, hcgDB, "db.exists", "目标库存在", hcSkip, "连不上数据库，无法核实")
		hcAdd(r, hcgDB, "db.tables", "库内已有数据表", hcSkip, "连不上数据库，无法核实")
		return
	}
	hcAdd(r, hcgDB, "db.connect", "MySQL 可连接", hcPass,
		fmt.Sprintf("%s:%d · MySQL %s", cfg.Projects.MySQLHost, cfg.Projects.MySQLPort, h.Version))
	if !h.DBExists {
		hcAdd(r, hcgDB, "db.exists", "目标库存在", hcFail, "目标库 "+dbName+" 不存在", "createdb")
		hcAdd(r, hcgDB, "db.tables", "库内已有数据表", hcSkip, "目标库不存在，无法核实")
		return
	}
	hcAdd(r, hcgDB, "db.exists", "目标库存在", hcPass, dbName)

	// 弱判据：只问「库里有没有表」。严格「SQL 里的表是否齐」属 P1，
	// 在没有可靠解析（USE / 前缀 / 注释）之前不做差集，否则会制造误报。
	q := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s';",
		strings.ReplaceAll(dbName, "'", "''"))
	p := cfg.Projects
	out, e := runMysqlOut("-h", p.MySQLHost, "-P", fmt.Sprint(p.MySQLPort), "-u", p.MySQLUser,
		"-N", "-B", "-e", q)
	if e != nil {
		hcAdd(r, hcgDB, "db.tables", "库内已有数据表", hcSkip, "表数量查不到，未核实："+firstLines(e.Error(), 80))
		return
	}
	n := strings.TrimSpace(firstLine(strings.TrimSpace(out)))
	if n == "" || n == "0" || n == "NULL" {
		hcAdd(r, hcgDB, "db.tables", "库内已有数据表", hcFail,
			"目标库是空库（0 张表），业务页面会查不到数据", "createdb")
	} else {
		hcAdd(r, hcgDB, "db.tables", "库内已有数据表", hcPass, "库内 "+n+" 张表")
	}
}

// ---- D. 链端（5 项）----
func hcCheckChain(r *HCReport) {
	c := chainCfg()
	host := strings.TrimSpace(c.ChainHost)
	if host == "" {
		host = strings.TrimSpace(c.SSHHost)
	}
	if host == "" {
		for _, id := range []string{"chain.reach", "chain.node", "chain.webase", "chain.info", "chain.consensus"} {
			hcAdd(r, hcgChain, id, map[string]string{
				"chain.reach": "链端主机可达", "chain.node": "链节点存活", "chain.webase": "WeBASE-Front 可用",
				"chain.info": "链信息可读", "chain.consensus": "共识在推进",
			}[id], hcSkip, "未配置链端主机，跳过（本机纯前端项目属正常）")
		}
		return
	}

	// allowRecover=false：体检只读，绝不自动拉起节点或 WeBASE
	doChainCheckWithRecover(false)
	ci := runtimeChain()
	if ci.Reach != "" && ci.Reach != "ok" {
		hcAdd(r, hcgChain, "chain.reach", "链端主机可达", hcFail,
			host+" 不可达（"+ci.Reach+"）："+firstLines(ci.ReachMsg, 140), "goto:chain")
	} else {
		hcAdd(r, hcgChain, "chain.reach", "链端主机可达", hcPass, host)
	}

	if ci.Port20200 || ci.NodeProcs > 0 {
		hcAdd(r, hcgChain, "chain.node", "链节点存活", hcPass,
			fmt.Sprintf("节点存活判定=%v · fisco-bcos 进程数=%d", ci.Port20200, ci.NodeProcs))
	} else if ci.NodeProcs < 0 {
		hcAdd(r, hcgChain, "chain.node", "链节点存活", hcSkip, "SSH 不可用，进程数未核实；端口探测仅供参考")
	} else {
		hcAdd(r, hcgChain, "chain.node", "链节点存活", hcFail,
			"节点端口未监听且进程数为 0", "goto:chain")
	}

	if ci.Port5002 {
		hcAdd(r, hcgChain, "chain.webase", "WeBASE-Front 可用", hcPass,
			fmt.Sprintf("%d 端口可达（%dms）", c.WebasePort, ci.Lat5002))
	} else {
		hcAdd(r, hcgChain, "chain.webase", "WeBASE-Front 可用", hcFail,
			fmt.Sprintf("%d 端口不通", c.WebasePort), "goto:chain")
	}

	if ci.WebaseOK && ci.BlockNumber != "" {
		hcAdd(r, hcgChain, "chain.info", "链信息可读", hcPass,
			fmt.Sprintf("区块高度 %s · 交易数 %s · 客户端 %s", ci.BlockNumber, ci.TxCount, ci.ClientVer))
	} else if !ci.Port5002 {
		hcAdd(r, hcgChain, "chain.info", "链信息可读", hcSkip, "WeBASE 不通，未读取链信息")
	} else {
		hcAdd(r, hcgChain, "chain.info", "链信息可读", hcFail,
			"WeBASE 可达但取不到区块高度，可能未连接节点", "goto:chain")
	}

	if !ci.Port5002 {
		hcAdd(r, hcgChain, "chain.consensus", "共识在推进", hcSkip, "WeBASE 不通，无法采样共识")
		return
	}
	vr := verifyChain(verifyChainGrowth) // 只读：返回 OpResult，不写服务注册表
	if vr.Verified {
		hcAdd(r, hcgChain, "chain.consensus", "共识在推进", hcPass,
			fmt.Sprintf("%v · %s", vr.Ok, firstLines(vr.Msg, 120)))
	} else {
		// 采不到 view 不等于共识停了（verify.go 注释：空闲链可能不出块）
		hcAdd(r, hcgChain, "chain.consensus", "共识在推进", hcSkip,
			"共识活性未核实："+firstLines(vr.Msg, 140))
	}
}

// ---- E. 服务（4 项）----
func hcCheckService(r *HCReport) {
	for _, target := range []string{"backend", "web"} {
		label := "后端"
		if target == "web" {
			label = "前端"
		}
		alive, pid := svcAlive(target)
		ports := svcPortsFn(target)
		port := firstListeningPort(ports)
		if !alive && port == 0 {
			hcAdd(r, hcgService, "svc."+target, label+"服务可用", hcFail,
				"进程未运行，且候选端口 "+intsToStr(ports)+" 均未监听", "start:"+target)
			continue
		}
		owner := ""
		if port > 0 {
			owner = portOwner(port)
		}
		if port > 0 && owner != "" && !svcOwnerAcceptable(owner, svcExpectProcs(target)) {
			hcAdd(r, hcgService, "svc."+target, label+"服务可用", hcFail,
				fmt.Sprintf("端口 %d 被 %s 占用，不是本服务", port, owner), "ports")
			continue
		}
		code := 0
		if port > 0 {
			code = httpStatusCode(httpProbe(port))
		}
		detail := fmt.Sprintf("进程存活=%v(PID %d) · 监听端口=%d · 占用者=%s · HTTP=%d",
			alive, pid, port, nonEmptyStr(owner, "未知"), code)
		if code >= 500 {
			hcAdd(r, hcgService, "svc."+target, label+"服务可用", hcFail,
				detail+"；服务端自报 5xx，进程在跑也不能算可用", "restart:"+target)
		} else if alive && port > 0 && code > 0 && code < 500 {
			hcAdd(r, hcgService, "svc."+target, label+"服务可用", hcPass, detail)
		} else if alive && port > 0 {
			hcAdd(r, hcgService, "svc."+target, label+"服务可用", hcSkip,
				detail+"；HTTP 未响应，未核实是否可用")
		} else {
			hcAdd(r, hcgService, "svc."+target, label+"服务可用", hcSkip, detail+"；证据不足，未核实")
		}
	}

	// 端口占用：区分「本服务占着」和「别人占着」，不把正常监听判成冲突
	occ := scanPortsOnce(defaultScanPorts)
	var foreign []string
	for _, o := range occ {
		if o.State == "LISTENING" && !svcOwnerAcceptable(o.Name, svcExpectProcs("backend")) &&
			!svcOwnerAcceptable(o.Name, svcExpectProcs("web")) {
			foreign = append(foreign, fmt.Sprintf("%d→%s(PID %d)", o.Port, o.Name, o.PID))
		}
	}
	if len(foreign) > 0 {
		sort.Strings(foreign)
		hcAdd(r, hcgService, "svc.ports", "端口无外来占用", hcFail,
			"疑似非本项目进程占着："+strings.Join(foreign, "、"), "ports")
	} else if len(occ) == 0 {
		hcAdd(r, hcgService, "svc.ports", "端口无外来占用", hcSkip, "端口表未取到，未核实", "ports")
	} else {
		hcAdd(r, hcgService, "svc.ports", "端口无外来占用", hcPass,
			fmt.Sprintf("%d 个常用端口无外来占用", len(occ)))
	}

	// 熔断 / 连续失败是「现在能不能交付」的直接否决项
	svcs := runtimeServices()
	var bad []string
	for key, s := range svcs {
		if s.BreakerTripped {
			bad = append(bad, key+" 处于熔断态")
		}
		if s.Restarts >= 3 {
			bad = append(bad, fmt.Sprintf("%s 连续失败 %d 次", key, s.Restarts))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		hcAdd(r, hcgService, "svc.breaker", "无熔断与崩溃循环", hcFail,
			strings.Join(bad, "；"), "breaker:backend")
	} else {
		hcAdd(r, hcgService, "svc.breaker", "无熔断与崩溃循环", hcPass, "没有熔断，也没有连续失败达限")
	}
}

// ---- F. 运行期（2 项）----
func hcCheckRuntime(r *HCReport) {
	// 口径必须诚实：hub.history 只有行文本没有时间戳，所以是「最近 N 行」
	// 而不是「最近 5 分钟」。带 [WARN]/[HINT]/[FAIL] 的行只信标签。
	hub.mu.Lock()
	hist := append([]LogMsg{}, hub.history...)
	hub.mu.Unlock()
	const window = 200
	if len(hist) > window {
		hist = hist[len(hist)-window:]
	}
	var errs []string
	for _, m := range hist {
		if strings.Contains(m.Line, "[FAIL]") || strings.Contains(strings.ToLower(m.Line), "panic:") {
			errs = append(errs, firstLines(m.Line, 100))
		}
	}
	if len(errs) > 0 {
		n := len(errs)
		if n > 3 {
			errs = errs[len(errs)-3:]
		}
		hcAdd(r, hcgRuntime, "rt.logs", "最近日志无错误", hcFail,
			fmt.Sprintf("最近 %d 行里 %d 条 [FAIL]/panic；末尾：", window, n)+strings.Join(errs, " | "), "ai")
	} else {
		hcAdd(r, hcgRuntime, "rt.logs", "最近日志无错误", hcPass,
			fmt.Sprintf("最近 %d 行没有 [FAIL]/panic 标记", window))
	}

	// 审计结论用结构化 Result，不靠中文措辞猜
	lines := auditSummary(100)
	failOps := 0
	for _, l := range lines {
		if strings.Contains(l, "→ "+resFail) {
			failOps++
		}
	}
	if len(lines) == 0 {
		hcAdd(r, hcgRuntime, "rt.audit", "最近操作无失败", hcSkip, "没有审计记录，未核实")
	} else if failOps > 0 {
		hcAdd(r, hcgRuntime, "rt.audit", "最近操作无失败", hcFail,
			fmt.Sprintf("最近 %d 条审计里有 %d 条失败", len(lines), failOps), "ai")
	} else {
		hcAdd(r, hcgRuntime, "rt.audit", "最近操作无失败", hcPass,
			fmt.Sprintf("最近 %d 条审计无失败记录", len(lines)))
	}
}

func nonEmptyStr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// ===== HTTP =====

// hcHistoryView 是给界面看的历史摘要：只给计数与失败项标题，不重复整份报告。
type hcHistoryView struct {
	FinishedAt  string   `json:"finished_at"`
	Pass        int      `json:"pass"`
	Fail        int      `json:"fail"`
	Skip        int      `json:"skip"`
	Deliverable bool     `json:"deliverable"`
	FailIDs     []string `json:"fail_ids"`
	FailTitles  []string `json:"fail_titles"`
}

// handleUIState 把「结论的时效性」交给前端：刷新/重启后照样知道
// 配置改过没复检、上一轮体检是什么结果。只读端点。
func handleUIState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stale, staleAt := getCfgStale()
	hcMu.RLock()
	views := make([]hcHistoryView, 0, len(hcHistory))
	for _, h := range hcHistory {
		v := hcHistoryView{
			FinishedAt: h.FinishedAt, Pass: h.Pass, Fail: h.Fail,
			Skip: h.Skip, Deliverable: h.Deliverable,
			FailIDs: []string{}, FailTitles: []string{},
		}
		for _, it := range h.Items {
			if it.Status == hcFail {
				v.FailIDs = append(v.FailIDs, it.ID)
				v.FailTitles = append(v.FailTitles, it.Title)
			}
		}
		views = append(views, v)
	}
	hcMu.RUnlock()
	writeJSON(w, map[string]any{
		"cfg_stale": stale, "cfg_stale_at": staleAt, "hc_history": views,
	})
}

func handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, hcSnapshot())
	case http.MethodPost:
		hcMu.Lock()
		if hcCur.Status == "running" {
			hcMu.Unlock()
			http.Error(w, "体检正在进行中", http.StatusConflict)
			return
		}
		hcMu.Unlock()
		// 只读任务：与同组写任务互斥，不与其它只读冲突。
		h, granted := beginTaskHG("全链路体检", []string{grpDB, grpChain}, false)
		if !granted {
			http.Error(w, "有任务正在执行，请稍候："+busyReason("全链路体检"), http.StatusConflict)
			return
		}
		go func() {
			defer h.Done()
			runHealthCheck()
		}()
		writeJSON(w, map[string]any{"started": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// hcMarkdown 导出文本。结论在最上面，证据在下面；跳过项必须写原因。
func hcMarkdown(rep HCReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# EnvKit 全链路体检\n\n")
	fmt.Fprintf(&b, "- 结论：**%s**\n", map[bool]string{true: "现在可以交付", false: "暂不能交付"}[rep.Deliverable])
	fmt.Fprintf(&b, "- %s\n", rep.Conclusion)
	fmt.Fprintf(&b, "- 开始：%s · 结束：%s · 版本：%s\n\n", rep.StartedAt, rep.FinishedAt, rep.Version)
	groupTitle := map[string]string{
		hcgEnv: "环境与组件", hcgProject: "项目配置", hcgDB: "数据库",
		hcgChain: "链端", hcgService: "服务与端口", hcgRuntime: "运行期",
	}
	mark := map[string]string{hcPass: "通过", hcFail: "不通过", hcSkip: "跳过"}
	for _, g := range []string{hcgEnv, hcgProject, hcgDB, hcgChain, hcgService, hcgRuntime} {
		fmt.Fprintf(&b, "## %s\n\n", groupTitle[g])
		for _, it := range rep.Items {
			if it.Group != g {
				continue
			}
			fmt.Fprintf(&b, "- [%s] **%s** — %s\n", mark[it.Status], it.Title, it.Detail)
		}
		b.WriteString("\n")
	}
	b.WriteString("> 本次体检为只读：未启动/重启服务、未建库、未自动恢复。跳过项未核实，不代表通过。\n")
	return b.String()
}

func handleHealthCheckExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rep := hcSnapshot()
	if rep.Status != "done" {
		http.Error(w, "还没有完成的体检报告，请先运行一次全链路体检", http.StatusConflict)
		return
	}
	name := "envkit-healthcheck-" + time.Now().Format("20060102-150405") + ".md"
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	_, _ = w.Write([]byte(hcMarkdown(rep)))
}
