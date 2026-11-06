module github.com/getlantern/systray

go 1.26.0

// EnvKit 本地最小 fork（上游 v1.2.2，完整源码 + LICENSE 一并保留）。
// 只改了 systray_windows.go 里标记 ENVKIT 的几处：托盘图标添加失败时等通知区域
// 就绪、换图标 ID / 用 NIM_MODIFY 接管残留条目、把失败与恢复回调给宿主。
// 其余平台文件与上游逐字节相同（可用 cmp 核）。
//
// 上游自带的 systray_windows_test.go 调的是旧版 addOrUpdateMenuItem 签名，
// 在 v1.2.2 里本来就编译不过；改名为 .upstream 保留原文，不参与构建。
// 我们自己的 addicon_recovery_windows_test.go 是恢复逻辑的确定性单元测试，
// 独立模块不被主模块 `go test ./...` 覆盖，CI 里有单独一步执行。
//
// 上游 go.mod 里的 lxn/walk、skratchdot/open-golang、govaluate 只给 example/
// 用，主包并不需要；这里去掉，避免 GOPROXY=off 构建时被无关模块卡住。
// 下面 indirect 由 `go mod tidy`（GOPROXY=off，取本机模块缓存）补全，
// 用于让 fork 能独立跑 go test；MVS 取主模块 envkit 的较新版本。

require (
	github.com/getlantern/golog v0.0.0-20190830074920-4ef2e798c2d7
	golang.org/x/sys v0.48.0
)

require (
	github.com/getlantern/context v0.0.0-20190109183933-c447772a6520 // indirect
	github.com/getlantern/errors v0.0.0-20190325191628-abdb3e3e36f7 // indirect
	github.com/getlantern/hex v0.0.0-20190417191902-c6586a6fe0b7 // indirect
	github.com/getlantern/hidden v0.0.0-20190325191715-f02dbb02be55 // indirect
	github.com/getlantern/ops v0.0.0-20190325191751-d70cb0d6f85f // indirect
	github.com/go-stack/stack v1.8.0 // indirect
	github.com/oxtoacart/bpool v0.0.0-20190530202638-03653db5a59c // indirect
)
