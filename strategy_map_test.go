// strategy_map_test.go —— T2.9 第四表一致性的 Go 侧断言 + R5 拓扑独立性静态检查
package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ---------- 第四表：映射自洽 ----------

// TestInjectionToStrategyPointsAtRealStrategies 映射的每个值都必须是
// 真实注册的策略。指向不存在的策略 = 判据在测一个产品里没有的东西。
func TestInjectionToStrategyPointsAtRealStrategies(t *testing.T) {
	if len(injectionToStrategy) == 0 {
		t.Fatal("映射表不能为空")
	}
	for kind, strat := range injectionToStrategy {
		if !strategyKnown(strat) {
			t.Errorf("注入 %s 映射到未注册的策略 %q", kind, strat)
		}
	}
}

// TestNoInjectionIsBothMappedAndExempt 同一个类别不能既配了策略又被豁免。
// 两边都写 = 语义冲突，而冲突时没人知道该信哪边。
func TestNoInjectionIsBothMappedAndExempt(t *testing.T) {
	for kind := range injectionToStrategy {
		if _, exempt := injectionWithoutStrategy[kind]; exempt {
			t.Errorf("注入 %s 同时出现在映射表与豁免表里", kind)
		}
	}
}

// TestExemptionsHaveReasons 豁免必须带理由。
// "忘了配"和"刻意不配"在表里都表现为一行缺失——不带理由就分不出来，
// 于是新增自主恢复类别时漏配策略会被静默吞掉。
func TestExemptionsHaveReasons(t *testing.T) {
	for kind, why := range injectionWithoutStrategy {
		if strings.TrimSpace(why) == "" {
			t.Errorf("豁免 %s 没有理由", kind)
		}
		if len(why) < 8 {
			t.Errorf("豁免 %s 的理由太短（%q），说不清是设计还是遗漏", kind, why)
		}
	}
}

// TestStrategyToInjectionsReverseIndex 反向索引与正向映射一致。
func TestStrategyToInjectionsReverseIndex(t *testing.T) {
	rev := strategyToInjections()
	total := 0
	for _, ks := range rev {
		total += len(ks)
	}
	if total != len(injectionToStrategy) {
		t.Errorf("反向索引共 %d 条，正向映射 %d 条——不一致",
			total, len(injectionToStrategy))
	}
	for kind, strat := range injectionToStrategy {
		found := false
		for _, k := range rev[strat] {
			if k == kind {
				found = true
			}
		}
		if !found {
			t.Errorf("%s -> %s 在反向索引里找不到", kind, strat)
		}
	}
}

// TestStrategyMapIsRegexParseable **这条是给 Python 侧断言兜底的**。
//
// Python（tools/recovery_selftest.py）要读本表做跨语言四表断言，
// 但它读的是**源码文本**。一旦有人改写成别的格式（改行距、加注释行、
// 用 := 而不是 =），Python 的正则就会静默匹配到 0 条——
// 于是四表断言变成"永远通过"的空转。
//
// 所以这里用**与 Python 完全相同的正则**反解本文件，断言能还原出同一张表。
// Python 正则失配时，这条 Go 测试先红。
func TestStrategyMapIsRegexParseable(t *testing.T) {
	src, err := os.ReadFile("strategy_map.go")
	if err != nil {
		t.Fatalf("读不到 strategy_map.go：%v", err)
	}
	s := string(src)

	// 与 tools/recovery_selftest.py 中 STRATEGY_MAP_RE / STRATEGY_EXEMPT_RE 保持一致。
	// **`\r?` 不是可有可无的**：本机用 Python 脚本改 .go 文件时，
	// `pathlib.write_text` 会把 `\n` 写成 `\r\n`（os.linesep），
	// 而 `os.ReadFile` 不做归一化——于是正则解出 0 条，
	// 四表断言静默变成空转。加上 `\r?` 让检查对行尾结尾不敏感。
	// （踩过：2026-10-19 植入违规测试时 Go 侧报"解出 0 条"，Python 侧却全绿。）
	mapBlock := blockBetween(s, "var injectionToStrategy = map[string]string{", "}")
	if mapBlock == "" {
		t.Fatal("找不到 injectionToStrategy 代码块——Python 侧的正则也会失配")
	}
	reMap := regexp.MustCompile(`(?m)^\t"([a-z_]+)":\s+"([a-z_]+)",\r?$`)
	got := map[string]string{}
	for _, m := range reMap.FindAllStringSubmatch(mapBlock, -1) {
		got[m[1]] = m[2]
	}
	if len(got) != len(injectionToStrategy) {
		t.Fatalf("正则只解出 %d 条，实际 %d 条——Python 侧会静默少验",
			len(got), len(injectionToStrategy))
	}
	for k, v := range injectionToStrategy {
		if got[k] != v {
			t.Errorf("键 %s：正则解出 %q，实际 %q", k, got[k], v)
		}
	}

	exBlock := blockBetween(s, "var injectionWithoutStrategy = map[string]string{", "}")
	if exBlock == "" {
		t.Fatal("找不到 injectionWithoutStrategy 代码块")
	}
	reEx := regexp.MustCompile(`(?m)^\t"([a-z_]+)":\s+"([^"]+)",\r?$`)
	gotEx := map[string]string{}
	for _, m := range reEx.FindAllStringSubmatch(exBlock, -1) {
		gotEx[m[1]] = m[2]
	}
	if len(gotEx) != len(injectionWithoutStrategy) {
		t.Fatalf("豁免表正则只解出 %d 条，实际 %d 条", len(gotEx), len(injectionWithoutStrategy))
	}
	for k, v := range injectionWithoutStrategy {
		if gotEx[k] != v {
			t.Errorf("豁免键 %s：正则解出 %q，实际 %q", k, gotEx[k], v)
		}
	}
}

// blockBetween 取 start 之后、end 之前的文本。找不到 start 返回空串。
func blockBetween(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// ---------- R5：拓扑独立性静态检查 ----------

// coreTopologyFreeFiles 恢复事务核心文件（plan §六 R5：
// 事务 / 裁决 / 证据 / 指标四类）。这些文件**不允许出现拓扑名字**——
// 出现即意味着核心逻辑开始依赖具体拓扑，新拓扑就接不进来了。
//
// 列表是白名单式的：要往这里加文件，先问"它是不是四类之一"。
// 适配器实现（topo_local/topo_db/topo_chain）与拓扑模型（topology.go）
// 刻意不在列——拓扑相关代码总得有个家，那就是这几个文件。
var coreTopologyFreeFiles = []string{
	"state.go",     // 事务：组锁、看门狗、状态
	"txbudget.go",  // 事务：预算与熔断
	"policy.go",    // 裁决：PolicyGate
	"verify.go",    // 证据：三态复验
	"opresult.go",  // 证据：三态载体
	"audit.go",     // 证据：审计流
	"guard.go",     // 事务：拓扑无关巡检循环（T2.6）
	"correlate.go", // 指标/关联：跨节点事件关联（T2.7）
}

// topologySymbols 拓扑相关的标识符。核心文件引用其中任何一个，
// 就说明它在按拓扑分支行事。
//
// **刻意不含 `adapterFor`**：注册表查找正是核心循环保持拓扑无关的**机制**——
// 循环通过它拿到适配器，而不是自己 `if adapter == "ssh_chain"`。
// 把它列进禁用名单会让 R5 永不可满足（循环总得从某处拿到适配器），
// 而一条永不可满足的规则等于没有规则，还会诱导人把检查改松。
//
// **也不含 verifyChain / chainReach / svcPortsFn / dbBackupTask 等实现函数名**：
// 那些是既有实现的本体，plan §4.1 说它们"退化为第一批适配器"——
// 在适配器完全收编之前，它们就在原来的文件里。把它们列为违规，
// 等于断言一件对当前代码为假的事（踩过：2026-10-19 扩充过一次，
// 结果 8 处误报，其中 verify.go 4 处、opresult.go 1 处）。
var topologySymbols = []string{
	"LocalProcAdapter", "DBAdapter", "SSHChainAdapter",
	"AdapterLocal", "AdapterDB", "AdapterSSHChain",
	"sshChainTargets", "localProcTargets",
}

// topologyBranchPatterns 真正的"按拓扑分支"模式。
// 符号名单管"引用了具体适配器"，这里管"拿拓扑名当判断条件"——
// 后者哪怕不引用适配器类型也成立（比如 `if svc.Adapter == "db"`）。
var topologyBranchPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\.Adapter\s*==`),
	regexp.MustCompile(`adapter\s*==\s*"`),
	regexp.MustCompile(`switch\s+\w*[Aa]dapter`),
}

// TestCoreFilesHaveNoTopologyDependency R5 的客观检查。
// 与"代码里写了注释说我不依赖拓扑"相比，这条读的是事实。
func TestCoreFilesHaveNoTopologyDependency(t *testing.T) {
	for _, f := range coreTopologyFreeFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			// correlate.go 在 T2.7 才建；文件不存在时跳过而不是失败——
			// 但要用 t.Logf 说出来，免得"跳过"被当成"通过"。
			if os.IsNotExist(err) {
				t.Logf("核心文件 %s 尚不存在（T2.7 建成后纳入检查）", f)
				continue
			}
			t.Fatalf("读不到 %s：%v", f, err)
		}
		s := string(src)
		for _, sym := range topologySymbols {
			if strings.Contains(s, sym) {
				t.Errorf("%s 出现拓扑符号 %s —— 恢复事务核心不得依赖具体拓扑（R5）。"+
					"拓扑相关代码应放在 topo_*.go / topology.go", f, sym)
			}
		}
		for _, rx := range topologyBranchPatterns {
			if m := rx.FindString(s); m != "" {
				t.Errorf("%s 出现按拓扑分支的写法 %q —— 新拓扑只能经配置与适配器接入，"+
					"不允许在核心里判 adapter（R5 / plan §4.2）", f, m)
			}
		}
	}
}

// TestTopologyFileHasNoAdapterImplementations 抽象层不得含具体适配器实现。
// topology.go 只放词汇与契约；出现 LocalProcAdapter 之类就说明实现漏进了抽象层，
// 于是"新增适配器"会变成"改抽象层"——那正是 R5 要防的。
func TestTopologyFileHasNoAdapterImplementations(t *testing.T) {
	src, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("读不到 topology.go：%v", err)
	}
	s := string(src)
	for _, sym := range []string{"LocalProcAdapter", "DBAdapter", "SSHChainAdapter",
		"opStartWeb", "opStartBackend", "dbBackupTask", "runChainStart", "verifyChain"} {
		if strings.Contains(s, sym) {
			t.Errorf("topology.go 出现具体实现符号 %s —— 抽象层只放词汇与契约", sym)
		}
	}
}
