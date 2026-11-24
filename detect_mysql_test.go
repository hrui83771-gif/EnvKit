package main

import "testing"

// 系统安装的 MySQL 不在 PATH 时，组件检测也必须能通过服务/安装目录找到它。
// CI 没装 MySQL 时跳过；本机有 MySQL80 服务时这条会真正验证回退路径。
func TestDetectComponentFallsBackToSystemMySQL(t *testing.T) {
	if mysqlServiceExe() == "" && mysqlClientFromInstall() == "" {
		t.Skip("本机没有可定位的系统 MySQL")
	}
	c := Component{
		Name:      "MySQL",
		BinExe:    "mysqld.exe",
		CheckCmd:  "mysqld",
		CheckArgs: []string{"--version"},
		Special:   "mysql",
	}
	r := detectComponent(c)
	if !r.Installed {
		t.Fatalf("系统 MySQL 已存在但检测为未安装: %+v", r)
	}
	if r.Location == "" {
		t.Fatalf("检测到安装但位置为空: %+v", r)
	}
}
