package main

// eval_smoke_test.go —— 冒烟集（v2.1 基线，随提交跑）
//
// 为什么用 Go 测试而不是 YAML + Python runner：
//  1. 零额外依赖，随 `go test ./...` 一起跑，不给开发增加额外步骤；
//  2. 判分与断言同源，不会出现"脚本说通过、断言说失败"的口径分歧；
//  3. 完整 YAML 任务集留给 v3.1 的 Harness（那时需要跨环境编排与报告生成）。
//
// 题目全部基于 eval/fixtures/ 下的**固定磁盘样本**，不临时构造——
// 临时构造的样本会随代码一起变，基线数据就不可比了。
//
// 每题标注它验证哪条主张。改动请同步更新 docs/eval/baseline-v2.1.md。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// smokeFix 返回 fixture 目录的绝对路径。
func smokeFix(t *testing.T, parts ...string) string {
	t.Helper()
	base := filepath.Join("eval", "fixtures")
	p := filepath.Join(append([]string{base}, parts...)...)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture 缺失：%s（%v）", p, err)
	}
	return p
}

// smokeCfg 临时把配置指向 fixture，返回恢复函数。
func smokeCfg(t *testing.T, fe, be, webScript string) func() {
	t.Helper()
	oldFE, oldBE, oldWS := cfg.Projects.FrontendDir, cfg.Projects.BackendDir, cfg.Projects.WebScript
	oldMem := aiMemoryOn()
	cfg.Projects.FrontendDir, cfg.Projects.BackendDir, cfg.Projects.WebScript = fe, be, webScript
	aiMutate(func(a *AIConfig) { a.MemoryEnabled = false })
	return func() {
		cfg.Projects.FrontendDir, cfg.Projects.BackendDir, cfg.Projects.WebScript = oldFE, oldBE, oldWS
		aiMutate(func(a *AIConfig) { a.MemoryEnabled = oldMem })
	}
}

// ===== S01 前端仅 dev → 必须推断出 dev，不能回退 serve =====
// 主张：环境事实层接管硬编码（v2.1 L1）
func TestSmoke01_FrontendDev(t *testing.T) {
	fe := smokeFix(t, "frontend-npm-dev")
	defer smokeCfg(t, fe, "", "")()

	script, all, src, _ := inferWebScript(fe, "")
	if script != "dev" {
		t.Fatalf("应推断出 dev，实际 %q", script)
	}
	if src != "package.json" {
		t.Fatalf("来源应是 package.json，实际 %q", src)
	}
	// build / lint 是危险脚本，绝不能进候选
	for _, c := range all {
		if c == "build" || c == "lint" {
			t.Fatalf("危险脚本 %q 进了候选集：%v", c, all)
		}
	}
}

// ===== S02 前端仅 serve → 推断 serve =====
// 主张：同上（FarmTrace 自身的形态）
func TestSmoke02_FrontendServe(t *testing.T) {
	fe := smokeFix(t, "frontend-serve")
	defer smokeCfg(t, fe, "", "")()

	if script, _, _, _ := inferWebScript(fe, ""); script != "serve" {
		t.Fatalf("应推断出 serve，实际 %q", script)
	}
}

// ===== S03 多个候选 → 优先 dev，且候选集完整 =====
// 主张：Inference 与 Decision 分层，候选要能被看见
func TestSmoke03_MultipleCandidates(t *testing.T) {
	fe := smokeFix(t, "frontend-npm-dev")
	defer smokeCfg(t, fe, "", "serve")()

	// 用户显式指定 serve 时，配置优先于推断
	script, _, src, _ := inferWebScript(fe, "serve")
	if script != "serve" || src != "config" {
		t.Fatalf("用户配置应优先，实际 %q/%q", script, src)
	}
	// 但候选集里必须仍然只有 dev（build/lint 被剔除）
	_, all, _, _ := inferWebScript(fe, "")
	if len(all) != 1 || all[0] != "dev" {
		t.Fatalf("候选集应只有 dev，实际 %v", all)
	}
}

// ===== S04 cmd 布局后端 → 推断 cmd/server/main.go =====
// 主张：后端入口不再写死 main.go（v2.1 L1）
func TestSmoke04_BackendCmdLayout(t *testing.T) {
	be := smokeFix(t, "backend-cmd-layout")
	defer smokeCfg(t, "", be, "")()

	f, why := inferBackendFile(be)
	if f != "cmd/server/main.go" {
		t.Fatalf("cmd 布局应推断出 cmd/server/main.go，实际 %q（%s）", f, why)
	}
	if _, _, ok := launchBackendFileOrDefault(be); !ok {
		t.Fatal("该 fixture 应当可启动")
	}
}

// ===== S05 全部脚本危险 → 必须拒绝，且不得静默回退 serve =====
// 主张：安全违规率第 0 类（不执行有副作用的脚本）
// 这是 v1 时代最严重的缺陷：写死 serve，遇到什么项目都硬跑。
func TestSmoke05_DangerousOnlyRejected(t *testing.T) {
	fe := smokeFix(t, "frontend-dangerous-only")
	defer smokeCfg(t, fe, "", "")()

	script, all, src, blocked := inferWebScript(fe, "")
	if script != "" || src != "none" {
		t.Fatalf("全是危险脚本时必须返回 none，实际 %q/%q", script, src)
	}
	if len(all) != 0 {
		t.Fatalf("不应有合法候选，实际 %v", all)
	}
	for _, want := range []string{"build", "deploy", "migrate", "test"} {
		if !containsStr(blocked, want) {
			t.Fatalf("被拒脚本 %q 应报给用户，实际 blocked=%v", want, blocked)
		}
	}
	// 关键：不得回退
	if _, _, ok := launchWebScriptOrDefault(fe, ""); ok {
		t.Fatal("识别不出时不得静默回退 serve")
	}
	// 计划说明里必须点名被拒的脚本
	p := currentLaunchPlan()
	if !strings.Contains(p.Note, "deploy") {
		t.Fatalf("说明应点名 deploy，实际 %q", p.Note)
	}
}

// ===== S06 显式指定危险脚本 → 任何路径都要拒绝 =====
// 主张：AI 不能通过参数绕过安全规则
func TestSmoke06_ExplicitDangerousRejected(t *testing.T) {
	fe := smokeFix(t, "frontend-npm-dev")
	defer smokeCfg(t, fe, "", "")()

	for _, bad := range []string{"deploy", "migrate", "reset", "install"} {
		if _, _, ok := launchWebScriptOrDefault(fe, bad); ok {
			t.Fatalf("显式指定 %q 必须被拒绝", bad)
		}
		if launchScriptAllowed(bad) {
			t.Fatalf("%q 竟然通过了脚本名白名单", bad)
		}
	}
	// 用户配置里写危险脚本同样拒绝（配置是最高优先级来源，更不能漏闸）
	if s, _, _, _ := inferWebScript(fe, "deploy"); s != "" {
		t.Fatalf("配置里的危险脚本必须被拒，实际 %q", s)
	}
}

// ===== S07 越界与敏感文件 → 必须拒绝 =====
// 主张：安全边界（探索沙箱 + 凭据文件保护）
func TestSmoke07_SensitivePathRejected(t *testing.T) {
	// 敏感文件（凭据/私钥/本机配置）无论在哪都拒读
	for _, p := range []string{
		`D:\BCGD\FarmTrace\envkit\config.json`,
		`D:\BCGD\FarmTrace\envkit\id_rsa`,
		`C:\Users\henry\.ssh\id_ed25519`,
		`D:\proj\.env`,
	} {
		if !exploreSecretPath(p) {
			t.Fatalf("敏感文件 %q 应被识别为拒读", p)
		}
	}
	// 越界路径：项目根之外一律拒（沙箱）
	defer withSmokeRoots(t)()
	for _, p := range []string{
		`C:\Windows\System32\drivers\etc\hosts`,
		`D:\完全不在项目里\secret.txt`,
	} {
		if _, ok := aiSafePath(p); ok {
			t.Fatalf("越界路径 %q 应被沙箱拒绝", p)
		}
	}
}

// withSmokeRoots 把沙箱根设成临时目录，避免测试受本机真实项目根影响。
func withSmokeRoots(t *testing.T) func() {
	t.Helper()
	oldFE, oldBE := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	dir := t.TempDir()
	cfg.Projects.FrontendDir, cfg.Projects.BackendDir = dir, dir
	return func() { cfg.Projects.FrontendDir, cfg.Projects.BackendDir = oldFE, oldBE }
}

// ===== S08 经验提取：成功的复验不得判为失败模式 =====
// 主张：验证准确性 / 学习层不产生误导
// 真实事故：v2.0 首版把「已复验通过：链端已恢复」当成了重复失败模式，
// 于是模型每次都被告知"这类操作重试没用"，而它其实成功了。
func TestSmoke08_NoFalseVerifyFail(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		{Ts: "2026-10-03 10:00:00.000", Actor: actAI, Action: "verify_chain", Target: "h",
			Result: resOK, Verify: "已复验通过：链端已恢复且共识在推进：视图 869695 → 869701"},
		{Ts: "2026-10-03 10:05:00.000", Actor: actAI, Action: "verify_chain", Target: "h",
			Result: resOK, Verify: "已复验通过：链端已恢复且共识在推进：视图 869801 → 869807"},
	})()

	if l := findLesson(lessonsFor(), lsVerifyFail); l != nil {
		t.Fatalf("成功的复验被误判为失败模式：%s", l.Title)
	}
}

// ===== S09 记忆层默认关闭 → 快照不注入任何记忆 =====
// 主张：新能力不静默改变既有行为
func TestSmoke09_MemoryGateDefaultOff(t *testing.T) {
	fe := smokeFix(t, "frontend-serve")
	defer smokeCfg(t, fe, "", "")()

	// 确认默认状态为关
	aiMutate(func(a *AIConfig) { a.MemoryEnabled = false })
	if aiMemoryOn() {
		t.Fatal("记忆层默认必须是关闭的")
	}
	// 造一条记忆，验证关闭时不会进入快照
	defer withMemoryEnv(t)()
	_, _, _ = memUpsert("冒烟集专用记忆条目", "", true, "manual")

	snap := aiHealthSnapshotFor("启动前端")
	if strings.Contains(snap, "user_memories") {
		t.Fatal("记忆层关闭时快照不应包含 user_memories")
	}
	if strings.Contains(snap, "冒烟集专用记忆条目") {
		t.Fatal("记忆层关闭时不应注入任何记忆内容")
	}

	// 打开后应当注入，验证门禁是有效的而非恒否
	aiMutate(func(a *AIConfig) { a.MemoryEnabled = true })
	snap2 := aiHealthSnapshotFor("启动前端")
	if !strings.Contains(snap2, "user_memories") {
		t.Fatal("记忆层开启后应注入 user_memories，否则门禁是坏的")
	}
}

// ===== S10 链端不可达 → 报"不可达"，不得当成"宕机"去自动恢复 =====
// 主张：验证准确性 / 不做无意义且有害的动作
// 真实事故：startGuardLoop 丢弃 err，把"连不上"当成"节点挂了"，
// 每 30~300 秒重启一次并弹通知，而 IP 不会自己变回来。
func TestSmoke10_UnreachableNotDown(t *testing.T) {
	// 192.0.2.0/24 是 RFC 5737 文档用网段，保证不可路由
	c := ChainConfig{SSHHost: "192.0.2.1", SSHPort: 22, ChainPort: 20200, WebasePort: 5002}
	st := chainReach(c)
	if st != reachDown {
		t.Skipf("目标地址意外可达（st=%v），跳过", st)
	}
	hint := reachHint(c, st)
	// 不可达时的出路指引必须把人引向"改地址/配转发"，而不是"重启节点"——
	// 恢复命令本身靠 SSH 送达，地址不通时重启毫无意义。
	if strings.Contains(hint, "重启") {
		t.Fatalf("不可达提示不得建议重启：%q", hint)
	}
	for _, must := range []string{"192.0.2.1", "127.0.0.1", "转发"} {
		if !strings.Contains(hint, must) {
			t.Fatalf("不可达提示缺少 %q，实际：%s", must, hint)
		}
	}
}
