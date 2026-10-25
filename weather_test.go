package main

// weather_test.go —— 桌宠天气的解析与开关语义。
//
// 为什么必须有这组测试：真实 wttr.in 的 lang_zh / weatherDesc 形状是
// **[{"value":"…"}]** 而不是字符串。第一版把它们声明成 string，
// go vet 与全量单测全绿（没有任何用例碰它），上线后每次查天气都返回
// "天气数据格式异常"——只有真的发一次请求才看得出来。
// 所以这里把**真实报文片段**当夹具钉死，并且不依赖外网。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wttrRealBody 摘自真实响应（汕头，2026-10-25），只裁掉无关字段。
const wttrRealBody = `{
  "current_condition": [{
    "temp_C": "25",
    "humidity": "67",
    "lang_xx": [{"value": "Overcast "}],
    "lang_zh": [{"value": "Overcast "}],
    "weatherCode": "122",
    "weatherDesc": [{"value": "Overcast "}]
  }],
  "nearest_area": [{
    "areaName": [{"value": "Xilu"}],
    "country": [{"value": "China"}],
    "region": [{"value": "Guangdong"}]
  }]
}`

// withStubWeather 把上游指到本地桩，返回时恢复。
func withStubWeather(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	old := weatherUpstream
	weatherUpstream = srv.URL + "/"
	t.Cleanup(func() { weatherUpstream = old; srv.Close() })
	return srv
}

func TestWeatherParsesRealWttrShape(t *testing.T) {
	withStubWeather(t, wttrRealBody, http.StatusOK)
	text, resp, err := fetchWeather("汕头")
	if err != nil {
		t.Fatalf("解析真实报文失败：%v（lang_zh/weatherDesc 是数组不是字符串）", err)
	}
	if resp.TempC != "25" {
		t.Fatalf("温度取错：%q", resp.TempC)
	}
	// 用户填了城市就用城市名：wttr.in 的 areaName 是街道级（"Xilu"），
	// 直接播报 "Xilu今天…" 很怪
	if resp.City != "汕头" {
		t.Fatalf("有城市参数时应以它为准，实际 %q", resp.City)
	}
	// wttr.in 没有中文数据，描述必须经本地词表翻过来
	if resp.Desc != "阴" {
		t.Fatalf("Overcast 应译为「阴」，实际 %q", resp.Desc)
	}
	if !strings.Contains(text, "25°") || !strings.Contains(text, "阴") {
		t.Fatalf("播报文案不完整：%q", text)
	}
	if strings.Contains(text, "Xilu") {
		t.Fatalf("不该拿街道名当播报地名：%q", text)
	}
	if strings.Contains(strings.ToLower(text), "overcast") {
		t.Fatalf("文案里不该残留英文：%q", text)
	}
}

// TestWeatherFallsBackToArea 没填城市（按 IP 猜测）时才用上游给的地名。
func TestWeatherFallsBackToArea(t *testing.T) {
	withStubWeather(t, wttrRealBody, http.StatusOK)
	text, resp, err := fetchWeather("")
	if err != nil {
		t.Fatalf("空城市应能正常解析：%v", err)
	}
	if resp.City != "Xilu" {
		t.Fatalf("空城市时应退回 nearest_area，实际 %q", resp.City)
	}
	if !strings.Contains(text, "Xilu今天") {
		t.Fatalf("播报应带地名：%q", text)
	}
}

func TestWeatherTranslateCoversCommonCodes(t *testing.T) {
	cases := map[string]string{
		"Sunny ":                              "晴",
		"Partly cloudy":                       "多云",
		"Overcast ":                           "阴",
		"Patchy rain nearby":                  "局部有雨",
		"Moderate rain":                       "中雨",
		"Light snow":                          "小雪",
		"Moderate or heavy rain with thunder": "雷雨",
	}
	for en, want := range cases {
		if got := translateWeather(en); got != want {
			t.Errorf("translateWeather(%q) = %q, want %q", en, got, want)
		}
	}
	// 查不到时保留英文，而不是留空（留空会让文案变成"今天 25°，"）
	if got := translateWeather("Some brand new code"); got != "Some brand new code" {
		t.Errorf("未知描述应原样返回，实际 %q", got)
	}
}

func TestWeatherUpstreamErrorIsReadable(t *testing.T) {
	withStubWeather(t, `{"not":"weather"}`, http.StatusOK)
	if _, _, err := fetchWeather(""); err == nil {
		t.Fatal("没有 current_condition 时应报错，而不是返回空文案")
	} else if !strings.Contains(err.Error(), "天气") {
		t.Fatalf("错误原因要是人话：%v", err)
	}

	withStubWeather(t, `{}`, http.StatusInternalServerError)
	_, _, err := fetchWeather("")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("上游 500 应带状态码：%v", err)
	}
}

// TestPetWeatherDisabledIsSilent 未开启时不能外连：上游指向一个必然失败的
// 地址，只要 handler 还返回 enabled:false 就说明它压根没发请求。
func TestPetWeatherDisabledIsSilent(t *testing.T) {
	oldUp, oldCfg := weatherUpstream, cfg.Pet
	t.Cleanup(func() {
		weatherUpstream = oldUp
		cfgMu.Lock()
		cfg.Pet = oldCfg
		cfgMu.Unlock()
	})
	weatherUpstream = "http://127.0.0.1:1/" // 必然拒绝
	cfgMu.Lock()
	cfg.Pet = PetConfig{WeatherEnabled: false, WeatherCity: "汕头"}
	cfgMu.Unlock()

	rec := httptest.NewRecorder()
	handlePetWeather(rec, httptest.NewRequest(http.MethodGet, "/api/pet/weather", nil))
	var got weatherResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if got.Enabled {
		t.Fatal("默认关闭时不应返回 enabled=true")
	}
	if got.Error != "" {
		t.Fatalf("关闭状态不该去请求上游，却带着错误回来：%q", got.Error)
	}
}
