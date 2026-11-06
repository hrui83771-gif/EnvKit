module github.com/getlantern/systray

go 1.13

// EnvKit 本地最小 fork（上游 v1.2.2，完整源码 + LICENSE 一并保留）。
// 只改了 systray_windows.go 里标记 ENVKIT 的几处：托盘图标添加失败时等任务栏
// 就绪并有限重试、把失败回调给宿主；其余平台文件与上游逐字节相同。
//
// 上游 go.mod 里的 lxn/walk、skratchdot/open-golang、govaluate 只给 example/
// 用，主包并不需要；这里去掉，避免 GOPROXY=off 构建时被无关模块卡住。
// 留下的两个依赖由主模块 envkit 的 go.mod 提供（MVS 取较新版本）。

require (
	github.com/getlantern/golog v0.0.0-20190830074920-4ef2e798c2d7
	golang.org/x/sys v0.48.0
)
