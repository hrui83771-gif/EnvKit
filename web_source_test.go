package main

// web_source_test.go —— 读**拼接后**的前端脚本，供各条契约测试共用。
//
// ## 为什么不再直接读 web/src/app.js
//
// v3.7.2 把 8818 行的 `web/src/app.js` 按面板拆成了 `web/src/js/*.js`。
// 若每条断言各写一遍"读哪个文件"，拆分点一挪测试就跟着红——
// 而它想钉的是**行为契约**（前端读的字段名、渲染用的文案），不是文件布局。
//
// ## 为什么排序拼接就够
//
// `tools/web_build.py` 的拼接顺序**就是**文件名排序（数字前缀决定顺序），
// 这里用同一个规则，两边不会分叉。拼接是空串连接：片段自带行尾换行，
// 多插一个换行会让断言里的 `...;\n...` 类字面量对不上。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFrontendJS 返回 web/src/js/*.js 按文件名排序、空串拼接后的源码。
func readFrontendJS(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("web", "src", "js", "*.js"))
	if err != nil {
		t.Fatalf("枚举 web/src/js/*.js：%v", err)
	}
	if len(files) == 0 {
		t.Fatal("web/src/js/ 里没有 js 片段——前端源码目录被挪走了？")
	}
	var b strings.Builder
	for _, f := range files {
		part, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s：%v", f, err)
		}
		b.Write(part)
	}
	return b.String()
}
