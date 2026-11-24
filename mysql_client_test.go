package main

// mysql_client_test.go —— 真实 AI 实测暴露的问题回归：
// 用户机器上 MySQL80 服务在跑、mysql.exe 在 Program Files，但不在 PATH、
// 也没有被 EnvKit 安装到组件目录，于是 db_check 报"未找到 mysql 客户端"，
// 模型据此断言"MySQL 完全没有安装"。修复必须能从服务 ImagePath 反推客户端。

import "testing"

func TestParseMysqlServiceExe(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			in:   "HKEY_LOCAL_MACHINE\\SYSTEM\\CurrentControlSet\\Services\\MySQL80\r\n    ImagePath    REG_EXPAND_SZ    \"C:\\Program Files\\MySQL\\MySQL Server 8.0\\bin\\mysqld.exe\" --defaults-file=\"C:\\ProgramData\\MySQL\\MySQL Server 8.0\\my.ini\" MySQL80\r\n",
			want: `C:\Program Files\MySQL\MySQL Server 8.0\bin\mysqld.exe`,
		},
		{
			in:   `    ImagePath    REG_SZ    "D:\mysql\bin\mysqld.exe" --defaults-file=D:\mysql\my.ini`,
			want: `D:\mysql\bin\mysqld.exe`,
		},
		{
			in:   "    ImagePath    REG_SZ    C:\\mysql\\bin\\mysqld.exe --defaults-file=C:\\mysql\\my.ini",
			want: `C:\mysql\bin\mysqld.exe`,
		},
		{
			// 真实 MySQL80 服务常把 ImagePath 写成 ...\mysqld（省略 .exe）
			in:   `    ImagePath    REG_SZ    "C:\Program Files\MySQL\MySQL Server 8.0\bin\mysqld" --defaults-file="C:\ProgramData\MySQL\MySQL Server 8.0\my.ini" MySQL80`,
			want: `C:\Program Files\MySQL\MySQL Server 8.0\bin\mysqld.exe`,
		},
		{in: "no image path here", want: ""},
	}
	for _, c := range cases {
		if got := parseMysqlServiceExe(c.in); got != c.want {
			t.Errorf("parseMysqlServiceExe(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
