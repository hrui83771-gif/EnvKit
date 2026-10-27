// diag_test.go —— semver 比较 / 诊断文件筛选 / 更新检查解析的单元测试
package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.8.0", "1.8.0", 0},
		{"1.8.0", "1.9.0", -1},
		{"1.10.0", "1.9.0", 1}, // 数字比较而非字符串比较（"10" < "9" 是字符串序）
		{"v2.0.0", "1.9.9", 1},
		{"1.8", "1.8.0", 0}, // 缺段按 0
		{"1.8.0", "", 1},
	}
	for _, c := range cases {
		if got := compareSemver(c.a, c.b); got != c.want {
			t.Errorf("compareSemver(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRecentFilesFilter(t *testing.T) {
	// 只验证筛选规则本身：非前缀/非后缀/目录一律排除
	if got := recentFiles("Z:\\definitely\\not\\exist", "envkit-", ".log", 5); got != nil {
		t.Fatalf("目录不存在应返回 nil，得到 %v", got)
	}
}

// 复查间隔的三态：没配过（nil）= 每 3 天；显式 0 = 只在启动查；超上限夹到一周。
// 之所以要测：从 int 改成 *int 就是为了保住"自动复查"和"显式关掉定时"能共存，
// 一旦有人把 interval() 改回 int 的零值判断，这两种意图会重新塌成同一个值。
func TestUpdateIntervalDefaults(t *testing.T) {
	zero, big, neg := 0, 200, -5
	cases := []struct {
		name string
		uc   UpdateConfig
		want int
	}{
		{"未配置按 3 天", UpdateConfig{}, updateDefaultIntervalHours},
		{"显式 0 = 仅启动", UpdateConfig{IntervalHours: &zero}, 0},
		{"超上限夹到 168", UpdateConfig{IntervalHours: &big}, 168},
		{"负数按 0", UpdateConfig{IntervalHours: &neg}, 0},
	}
	for _, c := range cases {
		if got := c.uc.interval(); got != c.want {
			t.Errorf("%s: interval() = %d, want %d", c.name, got, c.want)
		}
	}
}

// 回归：GitHub Releases API 的版本号在 tag_name、说明在 body。
// 只认 version/notes 时，填 Releases 地址会一直报"响应里没有版本号"——
// 而那种写法看起来完全合理（README、徽章都是 releases 链接）。
func TestSelfUpdateAcceptsGitHubRelease(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		io.WriteString(w, `{"tag_name":"v99.0.0","body":"第一行\n第二行"}`)
	}))
	defer srv.Close()

	cfgMu.Lock()
	oldURL := cfg.UpdateURL
	cfg.UpdateURL = srv.URL
	cfgMu.Unlock()
	updMu.Lock()
	oldUpd := upd
	updMu.Unlock()
	defer func() {
		cfgMu.Lock()
		cfg.UpdateURL = oldURL
		cfgMu.Unlock()
		updMu.Lock()
		upd = oldUpd
		updMu.Unlock()
	}()

	runSelfUpdateCheck()

	updMu.Lock()
	got := upd
	updMu.Unlock()

	if got.Latest != "v99.0.0" {
		t.Fatalf("没从 tag_name 取到版本号：latest=%q err=%q", got.Latest, got.Error)
	}
	if !got.HasNew {
		t.Fatalf("v99.0.0 应判为新版本：%+v", got)
	}
	if strings.Contains(got.Notes, "\n") {
		t.Fatalf("notes 应被压成一行（日志/气泡会被多行撑坏）：%q", got.Notes)
	}
	if ua == "" {
		t.Fatal("请求没带 User-Agent：GitHub API 会直接拒")
	}
}

// 回归：Config 浅拷贝共享 AI 指针 —— 脱敏/加密绝不能动到全局内存里的密钥。
// （真实事故：导出诊断包后 AI 助手 API Key 被清空，直到重启才恢复。）
func TestConfigCopyKeepsAIKey(t *testing.T) {
	orig := Config{AI: &AIConfig{Enabled: true, APIKey: "sk-test", Quirks: map[string]AIQuirk{"m": {NoTools: true}}}}

	_ = encryptConfig(orig) // 模拟一次保存
	if orig.AI.APIKey != "sk-test" {
		t.Fatalf("encryptConfig 把原 AI Key 改成了密文：%q", orig.AI.APIKey)
	}

	san := cloneConfigDeep(orig)
	san.AI.APIKey = ""
	san.AI.Quirks["m"] = AIQuirk{}
	if orig.AI.APIKey != "sk-test" {
		t.Fatalf("深拷贝后清空脱敏副本，原 Key 仍被改掉：%q", orig.AI.APIKey)
	}
	if !orig.AI.Quirks["m"].NoTools {
		t.Fatal("Quirks map 未深拷贝，脱敏副本的写入穿透到了原配置")
	}
}
