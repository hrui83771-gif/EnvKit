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
	errKindTimeout     = "timeout"      // 被看门狗超时终止
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
}

func opOK(action, target, msg string, evidence ...string) OpResult {
	return OpResult{Ok: true, Action: action, Target: target, Msg: msg, Evidence: nonEmpty(evidence)}
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
		if len(r.Evidence) > 0 {
			return r.Msg + "（" + strings.Join(r.Evidence, "；") + "）"
		}
		return r.Msg
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
