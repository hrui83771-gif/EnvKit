// topology_registry.go —— 适配器注册表（T2.3-T2.5）
//
// 为什么单独一个文件而不是放进 topology.go：**抽象层不得知道实现的存在**。
// topology.go 只放词汇与契约（TestTopologyFileHasNoAdapterImplementations
// 会读源码核对）；注册表是"接线"，天然要引用全部三个实现，
// 把它混进抽象层，那条断言就只能放水。
//
// 新增拓扑 = 在这里加一行 + 一个适配器实现（topo_*.go），
// **不需要在事务/裁决/证据/指标任何核心文件里加分支**（R5）。
package main

// adapterFor 按名字取适配器。三个内置适配器见 topo_local.go / topo_db.go /
// topo_chain.go。
//
// 返回的三个接口由同一个实现体提供——这是有意的：一个适配器本来就同时
// 会采集、会恢复、会复验（它对自己管的那个服务全权负责）。
// 强行拆成三个类型只会让"谁负责什么"变模糊。
func adapterFor(name string) (Probe, Recoverer, Verifier, bool) {
	switch name {
	case AdapterLocal:
		a := LocalProcAdapter{}
		return a, a, a, true
	case AdapterDB:
		a := DBAdapter{}
		return a, a, a, true
	case AdapterSSHChain:
		a := SSHChainAdapter{}
		return a, a, a, true
	}
	return nil, nil, nil, false
}
