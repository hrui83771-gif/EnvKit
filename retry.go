package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 子进程"零输出秒退"统一重试 ----------
// 本机安全软件会间歇性零输出瞬杀 EnvKit 派生的子进程（go / npm / mysqldump / mysql）：
// 表现为 exit status 非零且 stderr 完全为空，手动执行同一命令却正常。
// 原先 execStreamed / execBackground / runMysqlOut / 备份 各自实现了一套重试，
// 语义已经开始分叉（次数、退避、是否 cmd /c 包装、是否只对"零输出"重试）。
// 这里统一为"一份策略 + 一个循环"，调用方只负责描述"怎么跑一次"。

// retryPolicy 描述一次"零输出秒退"重试策略。
type retryPolicy struct {
	Attempts int           // 总尝试次数（含首次），<1 视为 1
	Backoff  time.Duration // 每次重试前的等待
	WrapLast bool          // 最后一次尝试改用 cmd /c call 包装派生（绕开对父进程链的拦截）
	Scope    string        // 日志分区
	Tag      string        // 日志步骤名
	Label    string        // 日志主语（如 "mysqldump"），默认"子进程"
	AnyErr   bool          // true=任何错误都重试；false（默认）=只对"零输出"错误重试
	// Abort 重试前检查；返回 true 时立即放弃后续尝试并交回上次结果。
	// v3.0 T1.4：任务看门狗超时杀掉前台命令后必须靠它收手——
	// 否则"杀了还重试"等于无锁继续执行（任务锁已经释放）。
	Abort func() bool
}

// runRetry 按策略执行 attempt。attempt 每次拿到"是否包装"的标志，
// 返回 (是否产生了任何输出, 错误)；runRetry 返回最后一次尝试的结果。
// 默认只在"有错误且零输出"时重试——因为真实报错一定会往 stderr/stdout 写东西。
func runRetry(p retryPolicy, attempt func(wrap bool) (bool, error)) (bool, error) {
	if p.Attempts < 1 {
		p.Attempts = 1
	}
	if p.Label == "" {
		p.Label = "子进程"
	}
	if p.Backoff <= 0 {
		p.Backoff = time.Second
	}
	var (
		hasOut bool
		err    error
	)
	for i := 0; i < p.Attempts; i++ {
		if i > 0 {
			if p.Abort != nil && p.Abort() {
				return hasOut, err // 已被看门狗终止：不再重试（无锁执行比不执行更坏）
			}
			metRetry() // 重试率是"环境异常"的先行指标，进 /metrics
			// 重试前告知原因与等待时间，避免用户以为"卡住没反应"
			wrapNext := p.WrapLast && i == p.Attempts-1
			switch {
			case wrapNext:
				warn(p.Scope, p.Tag, "%s仍无输出，%s后改用 cmd /c 包装执行...", p.Label, p.Backoff)
			case hasOut:
				warn(p.Scope, p.Tag, "%s执行失败，%s后自动重试（第 %d/%d 次）...", p.Label, p.Backoff, i+1, p.Attempts)
			default:
				// v3.3：这里原先直接断言"疑似杀毒软件拦截"。那是错的——
				// 零输出也可能是一个坏掉的启动器（例如 npm.cmd 找不到自身的
				// npm-cli.js，报错写在一个没人读的管道里），把用户引向白名单
				// 排查等于让他去改一件本来就没错的事。只陈述看到的事实。
				warn(p.Scope, p.Tag, "%s未产生任何输出就退出（退出码 %v），%s后自动重试（第 %d/%d 次）...",
					p.Label, exitCodeText(err), p.Backoff, i+1, p.Attempts)
			}
			time.Sleep(p.Backoff)
		}
		wrap := p.WrapLast && i == p.Attempts-1
		hasOut, err = attempt(wrap)
		if err == nil {
			if wrap {
				info(p.Scope, p.Tag, "cmd /c 包装执行成功")
			}
			return hasOut, err
		}
		if !p.AnyErr && hasOut {
			return hasOut, err // 有输出 = 真实报错，原样交回调用方
		}
	}
	return hasOut, err
}

// ---------- 失败注入（故障演练 / 自测用） ----------
// 本机"零输出秒退"是概率事件，无法稳定复现三层兜底。这里提供一个显式开关：
//   set ENVKIT_INJECT_FAIL=3   → 接下来 3 次子进程执行会被替换成"exit 1 且零输出"
// 用于验证 execStreamed / execBackground / 备份 的重试与 cmd 包装确实生效；
// 不设该环境变量时这条路径完全不参与（零副作用）。

var (
	injectMu   sync.Mutex
	injectLeft = -1 // -1 = 尚未从环境变量初始化
)

// takeInjectFail 判断本次执行是否要注入失败（是则消耗一次额度）。
func takeInjectFail() bool {
	injectMu.Lock()
	defer injectMu.Unlock()
	if injectLeft < 0 {
		injectLeft = 0
		if v := strings.TrimSpace(os.Getenv("ENVKIT_INJECT_FAIL")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				injectLeft = n
			}
		}
	}
	if injectLeft > 0 {
		injectLeft--
		return true
	}
	return false
}

// resetInjectFail 重新从环境变量装载注入额度（测试用）。
func resetInjectFail() {
	injectMu.Lock()
	injectLeft = -1
	injectMu.Unlock()
}

// wrapArgs 按需把命令包成 `cmd /c call <name> ...`（绕开针对直接父进程链的行为拦截）。
func wrapArgs(wrap bool, name string, args []string) (string, []string) {
	if !wrap {
		return name, args
	}
	return "cmd", append([]string{"/c", "call", name}, args...)
}

// buildCmd 构造带超时的命令。设置了 ENVKIT_INJECT_FAIL 时返回"零输出秒退"的替身命令（见上）。
func buildCmd(ctx context.Context, wrap bool, name string, args ...string) *exec.Cmd {
	return cmdFor(func(n string, a ...string) *exec.Cmd { return exec.CommandContext(ctx, n, a...) },
		wrap, name, args)
}

// buildCmdDetached 构造不绑 context 的长驻命令（后台服务不能被超时杀掉，只由 Job Object 连带终止）。
func buildCmdDetached(wrap bool, name string, args ...string) *exec.Cmd {
	return cmdFor(exec.Command, wrap, name, args)
}

// cmdFor 统一处理"故障注入"与"cmd /c 包装"，两种构造方式共用，避免逻辑分叉。
func cmdFor(newCmd func(string, ...string) *exec.Cmd, wrap bool, name string, args []string) *exec.Cmd {
	if takeInjectFail() {
		warn(scSys, "注入", "故障注入：把 %s 替换为零输出秒退（ENVKIT_INJECT_FAIL）", name)
		return newCmd("cmd", "/c", "exit", "1")
	}
	n, a := wrapArgs(wrap, name, args)
	return newCmd(n, a...)
}

// fastExit 判定"零输出秒退"：有错误、没产生任何输出、且存活时间极短。
// 三个条件同时满足才算——真实报错会写 stderr，正常退出不会有错误。
func fastExit(err error, hasOut bool, started time.Time, window time.Duration) bool {
	return err != nil && !hasOut && time.Since(started) < window
}

// exitCodeText 从 exec 的错误里取出退出码，取不到就原样返回错误的简述。
// 日志里写"退出码 1"比写"exit status 1"更容易和进程自己的报错对上。
func exitCodeText(err error) string {
	if err == nil {
		return "0"
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() >= 0 {
		return strconv.Itoa(ee.ExitCode())
	}
	return err.Error()
}

// subprocRetry 从设置页「启动校验与重试」读到的策略（0 = 内置默认）。
//
// 只给"零输出秒退"这条路径用（execStreamed / runMysqlOut）。数据库备份
// （dbops.go 的 mysqldump）不跟这里走：那条策略 AnyErr=true，真实报错也会重试，
// 放大次数只会把"注定失败"拖得更久。
//
// 为什么能在这里取 cfgMu：两个调用点都在跑子进程，不持有任何全局锁；
// 若将来有人把它挪进持锁区，就是死锁 —— 见 DevConfig.LogMaxMB 上的锁序说明。
func subprocRetry() (attempts int, backoff time.Duration) {
	d := devSnap()
	return devOr(d.SubprocRetries, devDefaultSubprocRetries),
		time.Duration(devOr(d.SubprocBackoffSecs, devDefaultSubprocBackoff)) * time.Second
}
