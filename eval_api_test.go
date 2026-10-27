// eval_api_test.go —— 设置页评测区的后端契约测试。
//
// 这里钉的是**安全边界与参数口径**，不是跑通一次评测（那要真实模型与沙箱，
// 由 tools/ 下的脚本自己管）。三类断言：
//  1. 哪几档必须确认（写错一个就等于给破坏性评测开了后门）；
//  2. 参数怎么拼（--allow-writes 只能是显式 opt-in、--json-out 只给门禁）；
//  3. 前置条件不满足时必须禁止启动（端口顺延是最容易被忽略的那条）。
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvalKindsSafetySplit(t *testing.T) {
	// 判据：ReadOnly=false 的档必须走 428 确认。
	// 破坏性档漏标 ReadOnly 等于把"注入故障 + 烧额度"变成一次点击。
	want := map[string]bool{
		"smoke":    true,
		"gate":     true,
		"recovery": false,
		"tasks":    false,
	}
	if len(evalKindList) != len(want) {
		t.Fatalf("档位数量变化（%d）：四档是设置页文案的契约，加档必须同步改界面", len(evalKindList))
	}
	for _, k := range evalKindList {
		exp, ok := want[k.Key]
		if !ok {
			t.Fatalf("出现未登记的档位 %q", k.Key)
		}
		if k.ReadOnly != exp {
			t.Errorf("%s 的 ReadOnly=%v，应为 %v（破坏性档必须过确认）", k.Key, k.ReadOnly, exp)
		}
		if k.Name == "" || k.Script == "" || k.Cost == "" {
			t.Errorf("%s 缺少展示用字段（Name/Script/Cost 都要进界面）", k.Key)
		}
		if k.Timeout < time.Minute {
			t.Errorf("%s 超时 %s 过短：门禁都要十几秒，任务评测要几十分钟", k.Key, k.Timeout)
		}
	}
}

func TestEvalKindLookup(t *testing.T) {
	if _, ok := evalKindByKey("gate"); !ok {
		t.Fatal("gate 必须能查到")
	}
	if _, ok := evalKindByKey("nope"); ok {
		t.Fatal("未知档位不该查得到（handler 靠这个回 400）")
	}
	if _, ok := evalKindByKey(""); ok {
		t.Fatal("空串不该查得到")
	}
}

func TestEvalBuildArgsGateGetsJSONOut(t *testing.T) {
	k, _ := evalKindByKey("gate")
	out := filepath.Join("tmp", "g.json")
	got := evalBuildArgs(k, evalRunReq{Repeat: 2}, out)
	if !contains(got, "--json-out") || !contains(got, out) {
		t.Fatalf("门禁必须带 --json-out（看板数据只认它算出来的结论），实际 %v", got)
	}
	if !contains(got, "--repeat") || !contains(got, "2") {
		t.Fatalf("repeat 要透传，实际 %v", got)
	}
}

func TestEvalBuildArgsAllowWritesIsOptIn(t *testing.T) {
	k, _ := evalKindByKey("recovery")
	// 默认不带：--allow-writes 会自动批准确认卡，必须是用户显式选择
	if got := evalBuildArgs(k, evalRunReq{}, ""); contains(got, "--allow-writes") {
		t.Fatalf("没传 allow_writes 时不许自动批准，实际 %v", got)
	}
	if got := evalBuildArgs(k, evalRunReq{AllowWrites: true, Only: "kill_service"}, ""); !contains(got, "--allow-writes") {
		t.Fatalf("显式 allow_writes 时要带上，实际 %v", got)
	}
	// only 要透传，且不能被拆成两个参数
	if got := evalBuildArgs(k, evalRunReq{Only: "kill_service"}, ""); !contains(got, "kill_service") {
		t.Fatalf("only 要透传，实际 %v", got)
	}
}

func TestEvalBuildArgsTasksHasNoAllowWrites(t *testing.T) {
	// 串档防护：--allow-writes 只对 recovery 有意义，
	// 误传给 judge_tasks 会被它当未知参数直接退出（评测白跑一次）。
	k, _ := evalKindByKey("tasks")
	got := evalBuildArgs(k, evalRunReq{AllowWrites: true}, "")
	if contains(got, "--allow-writes") {
		t.Fatalf("tasks 不该收到 --allow-writes，实际 %v", got)
	}
}

func TestEvalOnlyPatternRejectsInjection(t *testing.T) {
	good := []string{"T01", "T01,T02", "kill_service", "kill_service,hold_port"}
	for _, s := range good {
		if !onlyRe.MatchString(s) {
			t.Errorf("合法参数被拒：%q", s)
		}
	}
	bad := []string{"T01; rm -rf /", "a b", "T01|T02", "T01 --repeat 99", "../../etc", "T01\nT02"}
	for _, s := range bad {
		if onlyRe.MatchString(s) {
			t.Errorf("非法参数被放行：%q", s)
		}
	}
}

func TestEvalBlockersStopOnPortMismatch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "recovery_sandbox.py"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldCfg, oldPort := cfg, listenPortCur
	t.Cleanup(func() { cfg, listenPortCur = oldCfg, oldPort })
	// cfg.AI 是指针：不新建就会 nil dereference（evalAISnapshot 已判空，这里给真值）
	cfg.AI = &AIConfig{Enabled: true, APIKey: "test-key", Model: "test-model"}

	k, _ := evalKindByKey("recovery")
	fakePy := `C:\fake\python.exe`

	listenPortCur = 18765
	if b := evalBlockers(k, root, nil, fakePy); len(b) != 0 {
		t.Fatalf("端口一致且前置齐全时不该有 blocker，实际 %v", b)
	}

	listenPortCur = 18766
	b := evalBlockers(k, root, nil, fakePy)
	if len(b) == 0 {
		t.Fatal("端口顺延后必须挡住评测：脚本硬编码 18765，连错实例会一直卡在取 token")
	}
	if !strings.Contains(strings.Join(b, "|"), "18765") {
		t.Errorf("blocker 要点出脚本期望的 18765，实际 %v", b)
	}
}

func TestEvalBlockersMissingScriptAndPython(t *testing.T) {
	root := t.TempDir() // 空目录：脚本不存在
	k, _ := evalKindByKey("smoke")
	b := evalBlockers(k, root, nil, "") // 同时不给 python
	joined := strings.Join(b, "|")
	if !strings.Contains(joined, "recovery_selftest.py") {
		t.Errorf("缺脚本要指名道姓，实际 %v", b)
	}
	if !strings.Contains(joined, "Python 3") {
		t.Errorf("缺 python 要说清缺什么，实际 %v", b)
	}

	// root 定位失败时直接短路：连评测脚本都不在，别的检查没有意义
	short := evalBlockers(k, "", errors.New("未找到 tools/eval_gate.py"), "")
	if len(short) != 1 || !strings.Contains(short[0], "eval_gate.py") {
		t.Errorf("root 失败时应只报这一条，实际 %v", short)
	}
}

func TestEvalBlockersRequireAIKey(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "tools"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "tools", "judge_tasks.py"), []byte("\n"), 0o644)

	oldCfg, oldPort := cfg, listenPortCur
	t.Cleanup(func() { cfg, listenPortCur = oldCfg, oldPort })
	listenPortCur = 18765
	k, _ := evalKindByKey("tasks")

	cfg.AI = &AIConfig{Enabled: false, APIKey: ""}
	if b := evalBlockers(k, root, nil, `C:\fake\python.exe`); !strings.Contains(strings.Join(b, "|"), "API Key") {
		t.Errorf("没配 key 时任务评测必须挡住（否则每轮都报错却已经烧了发现成本）实际 %v", b)
	}
	cfg.AI = &AIConfig{Enabled: true, APIKey: "k"}
	if b := evalBlockers(k, root, nil, `C:\fake\python.exe`); len(b) != 0 {
		t.Errorf("配好 key 后不该有 blocker，实际 %v", b)
	}
	// ai 段整体缺失（老配置）时不能崩：这是指针字段最容易被忽略的一种状态
	cfg.AI = nil
	if b := evalBlockers(k, root, nil, `C:\fake\python.exe`); !strings.Contains(strings.Join(b, "|"), "API Key") {
		t.Errorf("ai 段缺失时应要求配 key 而不是 panic，实际 %v", b)
	}
}

func TestEvalTakeGateJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gate.json")
	if err := os.WriteFile(p, []byte(`{"ok":true,"problems":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := evalTakeGateJSON(p)
	if len(raw) == 0 || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("合法 JSON 应原样透传，实际 %q", string(raw))
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("读完必须删掉临时文件：留在系统临时目录里会随每次门禁累积")
	}
}

func TestEvalTakeGateJSONRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("not json at all"), 0o644)
	if raw := evalTakeGateJSON(bad); raw != nil {
		t.Errorf("非法 JSON 必须当作没有结论，不许塞给前端，实际 %q", string(raw))
	}
	// 文件不存在（旧版脚本没有 --json-out）：同样是"没有结论"，不是错误
	if raw := evalTakeGateJSON(filepath.Join(dir, "missing.json")); raw != nil {
		t.Errorf("文件不存在时应返回 nil，实际 %q", string(raw))
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
