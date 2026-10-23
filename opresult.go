package main

// opresult.go —— 操作结果契约。
//
// 背景：以前任务函数（webStartTask / backendStartTask / dbBackupTask）内部失败只会
// 写 UI 日志和审计，外层一律 return nil，导致调用方（尤其是 AI 工具）永远以为成功了，
// AI 于是对用户谎报"已经启动"。
// 契约要求：任何操作性任务都必须返回一个带证据的结果，而不是只返回一个 error 或 nil。
//   · Ok=false 时必须给出 ErrKind（失败归类），让 AI 能说"为什么失败、下一步做什么"；
//   · Ok=true 时必须给出 Evidence（可复核的客观值），证明"环境真的恢复了"，而不只是"命令发出了"。

import (
	"errors"
	"fmt"
	"strings"
)

// 失败归类：给 AI 与 UI 用的稳定枚举，便于给出针对性下一步。
const (
	errKindBusy        = "busy"         // 有任务正在执行
	errKindBadConfig   = "bad_config"   // 缺少必要配置（如未指定项目目录）
	errKindMissingDep  = "missing_dep"  // 依赖缺失（找不到 go / npm / mysqldump）
	errKindBuildFail   = "build_fail"   // 构建失败（如 go build 报错）
	errKindSpawnFail   = "spawn_fail"   // 进程派生失败（含被杀软拦截的零输出秒退）
	errKindEmptyOutput = "empty_output" // 命令成功但产物为空/残缺
	errKindVerifyFail  = "verify_fail"  // 事后复验未通过
	// errKindRestoreFail 备份无法真正还原（v2.3 N3）。
	// 与 verify_fail 分开：文件可能完全没坏、校验和也一致，
	// 但导进去才发现用不了——"文件是好的"和"能还原"是两件事，
	// 混在一类里用户会以为是文件损坏，而真正原因是缺了触发器之类。
	errKindRestoreFail = "restore_fail"
	errKindTimeout     = "timeout" // 被看门狗超时终止
	errKindUnknown     = "unknown"
)

// OpResult 一次操作的完整结果：成功与否 + 失败归类 + 证据链。
type OpResult struct {
	Ok       bool     `json:"ok"`
	Action   string   `json:"action"`             // start_service / db_backup / ...
	Target   string   `json:"target"`             // web / backend / 库名
	Msg      string   `json:"msg"`                // 一句话结论
	Evidence []string `json:"evidence"`           // 证据：端口、HTTP 码、文件路径、字节数、sha256…
	ErrKind  string   `json:"err_kind,omitempty"` // 仅失败时有值
	// Verified 是否"已通过环境复验"（v2.0 P2）。三态语义必须分清：
	//   · Ok=false            → 动作本身失败，环境没变；
	//   · Ok=true, Verified=false → 动作已执行，但现实环境尚未被证明恢复（不许说"已启动/已备份好"）；
	//   · Ok=true, Verified=true  → 已用客观证据确认环境真的恢复了（可以据此向用户确认完成）。
	Verified bool `json:"verified"`
	// Artifact 本次操作的产物路径（备份文件等），供后续复验直接取用。
	Artifact string `json:"artifact,omitempty"`
}

// opOK 动作已执行但尚未复验（诚实态：不许被当成"环境已恢复"）。
func opOK(action, target, msg string, evidence ...string) OpResult {
	return OpResult{Ok: true, Action: action, Target: target, Msg: msg, Evidence: nonEmpty(evidence)}
}

// opVerified 动作已执行且已通过环境复验（可以据此向用户确认完成）。
func opVerified(action, target, msg string, evidence ...string) OpResult {
	r := opOK(action, target, msg, evidence...)
	r.Verified = true
	return r
}

// withArtifact 附带产物路径（链式写法：opVerified(...).withArtifact(path)）。
func (r OpResult) withArtifact(path string) OpResult {
	r.Artifact = path
	return r
}

func opFail(action, target, kind, msg string, evidence ...string) OpResult {
	if kind == "" {
		kind = errKindUnknown
	}
	return OpResult{Ok: false, Action: action, Target: target, Msg: msg, Evidence: nonEmpty(evidence), ErrKind: kind}
}

// Err 兼容还想要 error 的调用方（HTTP handlers 里继续沿用 409 之类的处理）。
func (r OpResult) Err() error {
	if r.Ok {
		return nil
	}
	return errors.New(r.String())
}

// String 单行/多行的人读结论：给 AI 工具返回值、日志与确认卡片用。
func (r OpResult) String() string {
	if r.Ok {
		// 未复验必须显式标出来：这个字符串会进 AI 工具返回值，
		// 模型看到「未复验」才不会把"命令发出去了"说成"环境已恢复"。
		head := "已复验通过："
		if !r.Verified {
			head = "未复验："
		}
		if len(r.Evidence) > 0 {
			return head + r.Msg + "（" + strings.Join(r.Evidence, "；") + "）"
		}
		return head + r.Msg
	}
	s := "失败"
	if r.ErrKind != "" {
		s += "[" + r.ErrKind + "]"
	}
	s += "：" + r.Msg
	if len(r.Evidence) > 0 {
		s += "（" + strings.Join(r.Evidence, "；") + "）"
	}
	return s
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---------- OpResult → AI 可读文本 ----------
// 写工具的结果一律走这两个函数：成功给结论 + 证据，失败给归类 + 证据。
// 目的是让 AI 能如实告诉用户"失败在哪、下一步做什么"，而不是笼统地说"已执行"。

// aiOpEvidence 证据链拼接；没有证据时明确说"无"，避免 AI 脑补。
func aiOpEvidence(r OpResult) string {
	if len(r.Evidence) == 0 {
		return "无"
	}
	return strings.Join(r.Evidence, "；")
}

// aiOpErr 把失败结果转成工具错误；错误文本会回传给模型，所以它本身就是给 AI 的提示。
func aiOpErr(what string, r OpResult) error {
	return fmt.Errorf("%s失败[%s]：%s\n证据：%s", what, r.ErrKind, r.Msg, aiOpEvidence(r))
}

// rrRes OpResult → 审计结果值。
// v3.0 T1.6 会把它升级成三态（resUnverified）；当前先二值：
// 执行了记 ok，没执行记 fail——**未复验不在此处放大成失败**，
// 复验结论由 auditVerify 那条记录单独说（两条记录各管一件事）。
func rrRes(r OpResult) string {
	if r.Ok {
		return resOK
	}
	return resFail
}

// aiOpResultText 把一个 OpResult 翻译成给模型的结论文本，三态语义必须分开说：
// 失败给 err_kind 与下一步；已复验给证据并允许确认完成；未复验要堵死"那就当它成功了"的脑补。
func aiOpResultText(what string, r OpResult) string {
	switch {
	case !r.Ok:
		return fmt.Sprintf("%s失败[%s]：%s\n证据：%s\n请如实转述失败原因并给出下一步。", what, r.ErrKind, r.Msg, aiOpEvidence(r))
	case r.Verified:
		return fmt.Sprintf("%s成功且已复验通过：%s\n证据：%s", what, r.Msg, aiOpEvidence(r))
	default:
		return fmt.Sprintf("%s已执行但未通过复验：%s\n证据：%s\n必须如实告诉用户「已执行，还没确认成功」，禁止说已经好了。",
			what, r.Msg, aiOpEvidence(r))
	}
}
