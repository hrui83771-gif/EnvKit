// 冒烟集 fixture：cmd 布局的 Go 后端入口。
// 用来验证"后端入口不再写死 main.go"——写死时这个项目必然启动失败。
package main

import "fmt"

func main() {
	fmt.Println("smoke backend")
}
