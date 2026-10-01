// diag_test.go —— semver 比较 / 诊断文件筛选的单元测试
package main

import "testing"

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

// 回归：Config 浅拷贝共享 AI 指针 —— 脱敏/加密绝不能动到全局内存里的密钥。
// （真实事故：导出诊断包后 AI 助手 API Key 被清空，直到重启才恢复。）
func TestConfigCopyKeepsAIKey(t *testing.T) {
	orig := Config{AI: &AIConfig{Enabled: true, APIKey: "sk-test", Quirks: map[string]AIQuirk{"m": {NoTools: true}}}}

	_ = encryptConfig(orig) // 模拟一次保存
	if orig.AI.APIKey != "sk-test" {
		t.Fatalf("encryptConfig 把原 AI Key 改成了密文：%q", orig.AI.APIKey)
	}

	san := cloneConfigDeepAI(orig)
	san.AI.APIKey = ""
	san.AI.Quirks["m"] = AIQuirk{}
	if orig.AI.APIKey != "sk-test" {
		t.Fatalf("深拷贝后清空脱敏副本，原 Key 仍被改掉：%q", orig.AI.APIKey)
	}
	if !orig.AI.Quirks["m"].NoTools {
		t.Fatal("Quirks map 未深拷贝，脱敏副本的写入穿透到了原配置")
	}
}
