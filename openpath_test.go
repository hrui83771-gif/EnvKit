package main

// openpath_test.go —— 「打开目录」端点的边界。
//
// 这个端点会拉起资源管理器，测试里必须把真正的 opener 换掉：
// 真跑会闪窗、依赖桌面会话，CI 上必挂。规则本身（绝对路径 / 存在 / 是目录）
// 才是要钉的东西——它决定了"客户端能不能让服务端打开任意位置"。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestResolveOpenDir(t *testing.T) {
	dir := t.TempDir()
	// 带空白与末尾分隔符：应被 TrimSpace + Clean 规整，而不是原样返回
	if got, err := resolveOpenDir("  " + dir + string(os.PathSeparator) + "  "); err != nil {
		t.Fatalf("合法目录被拒：%v", err)
	} else if got != filepath.Clean(dir) {
		t.Errorf("未规整路径：%q，期望 %q", got, filepath.Clean(dir))
	}

	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("写临时文件：%v", err)
	}
	for _, c := range []struct {
		name string
		in   string
		want string
	}{
		{"空", "   ", "路径为空"},
		{"相对路径", "backups", "只接受绝对路径"},
		{"不存在", filepath.Join(dir, "nope"), "目录不存在"},
		{"是文件不是目录", file, "不是目录"},
	} {
		if _, err := resolveOpenDir(c.in); err == nil {
			t.Errorf("%s：应当被拒绝", c.name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：错误信息应含 %q，实际 %v", c.name, c.want, err)
		}
	}
}

func TestHandleOpenPath(t *testing.T) {
	dir := t.TempDir()
	var opened string
	old := openDirInExplorer
	openDirInExplorer = func(p string) error { opened = p; return nil }
	t.Cleanup(func() { openDirInExplorer = old })

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/open/path", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handleOpenPath(rec, req)
		return rec
	}

	rec := post(`{"path":` + strconv.Quote(dir) + `}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法目录应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if opened != filepath.Clean(dir) {
		t.Errorf("opener 收到 %q，期望 %q", opened, filepath.Clean(dir))
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["ok"] != true {
		t.Errorf("响应应含 ok=true：%s", rec.Body.String())
	}

	// 相对路径：400，且**绝不能**调用 opener
	opened = ""
	if rec := post(`{"path":"backups"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("相对路径应 400，实际 %d", rec.Code)
	}
	if opened != "" {
		t.Errorf("被拒绝的路径不该打开任何东西，实际打开了 %q", opened)
	}
	// 不存在：400
	if rec := post(`{"path":` + strconv.Quote(filepath.Join(dir, "nope")) + `}`); rec.Code != http.StatusBadRequest {
		t.Errorf("不存在应 400，实际 %d", rec.Code)
	}
	// GET：405（只允许 POST，避免一个链接就触发打开）
	req := httptest.NewRequest(http.MethodGet, "/api/open/path?path="+dir, nil)
	rec2 := httptest.NewRecorder()
	handleOpenPath(rec2, req)
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET 应 405，实际 %d", rec2.Code)
	}
}

// TestHandlePaths 界面拿到的必须是服务端解析后的真实目录。
func TestHandlePaths(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/paths", nil)
	rec := httptest.NewRecorder()
	handlePaths(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	for _, k := range []string{"exe_dir", "log_dir", "backup_dir"} {
		v := out[k]
		if v == "" {
			t.Errorf("%s 不应为空", k)
			continue
		}
		if !filepath.IsAbs(v) {
			t.Errorf("%s 应是绝对路径，实际 %q（前端拿它去 open/path，相对路径会被拒）", k, v)
		}
	}
}
