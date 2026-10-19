// correlate.go —— 跨节点事件关联（v3.0 第二阶段 T2.7，plan §4.3）
//
// 问题：四节点全宕时，如果每个 service 各发起一个恢复事务，会把环境刷爆；
// 而且后三个必然失败——它们的前置正是第一个要修的东西。
// 用户看到的是通知刷屏，而真正的故障原因没人去看。
//
// 已有先例（plan §4.3 点名）：守护"地址失效期间不做恢复"——
// **共同原因（SSH 不可达）不按独立故障处理**。本文件把这个思想推广成
// 一般的关联器。
//
// 设计要点：**压制下游恢复这个决定本身要可审计**。
// 只压制不记录，等于把"我们决定不救"藏起来——而出问题时，
// 最该查的恰恰是这些没发生的动作。
package main

import (
	"fmt"
	"time"
)

// CorrelateResult 一轮探针失败的关联结论。
type CorrelateResult struct {
	// Upstream 共同原因所在的上游 service id。空 = 没有可归因的上游
	// （含"上游是主机/网络这类没有 service 实体的情况"）。
	Upstream string `json:"upstream,omitempty"`
	// Affected 受影响的下游 service id（它们不该各自恢复）。
	Affected []string `json:"affected,omitempty"`
	// Suppressed 是否应当压制所有恢复（主机级共同原因）。
	Suppressed bool `json:"suppressed"`
	// Reason 人话原因，进审计与报告。
	Reason string `json:"reason"`
}

// correlate 把一轮里失败的 service 归因。
//
// svcs 全部 service；failed 是本轮探针失败的（id → 探针结论）。
//
// 三条规则，按优先级：
//
//  1. **主机级共同原因**：≥2 个 service 报 unreachable / auth_failed。
//     这不是"两个服务各自坏了"，是"到这台主机的路出了问题"。
//     对它发起任何恢复都无效（恢复命令本身就靠这条路送达）。
//  2. **依赖归因**：A 依赖 B 且两者都失败 → B 是上游，A 是受影响的下游。
//     对 B 走一个恢复事务，A 只记录"受影响"。
//  3. 都不命中 → 各自独立处理，不压制。
func correlate(svcs []Service, failed map[string]Observation) CorrelateResult {
	if len(failed) == 0 {
		return CorrelateResult{Reason: "无失败"}
	}

	// ---- 规则 1：主机级共同原因 ----
	hostish := 0
	for _, obs := range failed {
		if obs.ErrKind == "unreachable" || obs.ErrKind == "auth_failed" {
			hostish++
		}
	}
	if hostish >= 2 {
		ids := make([]string, 0, len(failed))
		for id := range failed {
			ids = append(ids, id)
		}
		return CorrelateResult{
			Suppressed: true,
			Affected:   ids,
			Reason: fmt.Sprintf(
				"%d 个 service 同时报不可达/认证失败，判定为主机或网络级共同原因，"+
					"本轮不发起任何恢复（恢复命令依赖同一条通道）", hostish),
		}
	}

	// ---- 规则 2：依赖归因 ----
	dependents := reverseDeps(svcs)
	var res CorrelateResult
	for id := range failed {
		down := dependents[id]
		if len(down) == 0 {
			continue
		}
		// 下游里有没有也失败的？有 → id 是上游
		var affected []string
		for _, d := range down {
			if _, bad := failed[d]; bad {
				affected = append(affected, d)
			}
		}
		if len(affected) == 0 {
			continue
		}
		if res.Upstream == "" {
			res.Upstream = id
			res.Affected = affected
			res.Reason = fmt.Sprintf("上游 %s 与下游 %v 同时失败，判定为一次上游故障",
				id, affected)
			continue
		}
		// 多个上游都中招：把它们都算进受影响，取第一个当主上游
		res.Affected = append(res.Affected, id)
	}
	if res.Upstream != "" {
		return res
	}

	// ---- 规则 3：各自独立 ----
	ids := make([]string, 0, len(failed))
	for id := range failed {
		ids = append(ids, id)
	}
	return CorrelateResult{Reason: fmt.Sprintf("%d 个 service 独立失败，无共同原因", len(ids))}
}

// reverseDeps 反向依赖表：service id → 依赖它的 service id 列表。
// 用 DependsOn 建索引，这样"谁是上游"是配置说了算，不是代码里写死的。
func reverseDeps(svcs []Service) map[string][]string {
	out := map[string][]string{}
	for _, s := range svcs {
		for _, dep := range s.DependsOn {
			out[dep] = append(out[dep], s.ID)
		}
	}
	return out
}

// correlationWindow 时间窗（plan §4.3）。
//
// 为什么需要窗：探针是快照，两个 service 在不同轮次失败，未必有共同原因。
// 只有**同一时间窗内**的失败才做关联——否则会把相隔十分钟的两次独立故障
// 误判成一次，从而错误地压制掉第二次的恢复。
type correlationWindow struct {
	width   time.Duration
	entries map[string]Observation
	since   time.Time
}

// newCorrelationWindow 建时间窗。width 至少一个巡检间隔。
func newCorrelationWindow(width time.Duration) *correlationWindow {
	if width <= 0 {
		width = 5 * time.Minute
	}
	return &correlationWindow{width: width, entries: map[string]Observation{},
		since: time.Now()}
}

// add 记录一次失败；返回是否仍在窗内。
// 超出窗宽时先清空再记——**过期条目必须丢弃**，否则窗会无限累积，
// 而基于陈旧数据的关联比不关联更坏（它会压制掉本该发生的恢复）。
func (w *correlationWindow) add(obs Observation) bool {
	if time.Since(w.since) > w.width {
		w.entries = map[string]Observation{}
		w.since = time.Now()
	}
	w.entries[obs.Service] = obs
	return true
}

// failed 窗内当前失败的集合。
func (w *correlationWindow) failed() map[string]Observation {
	out := map[string]Observation{}
	for k, v := range w.entries {
		if !v.Healthy {
			out[k] = v
		}
	}
	return out
}

// reset 开一个新窗（每轮巡检结束或恢复成功后调用）。
func (w *correlationWindow) reset() {
	w.entries = map[string]Observation{}
	w.since = time.Now()
}
