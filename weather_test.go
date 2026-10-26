package main

// weather_test.go —— 桌宠天气的解析与开关语义。
//
// 为什么必须有这组测试：真实 wttr.in 的 lang_zh / weatherDesc 形状是
// **[{"value":"…"}]** 而不是字符串。第一版把它们声明成 string，
// go vet 与全量单测全绿（没有任何用例碰它），上线后每次查天气都返回
// "天气数据格式异常"——只有真的发一次请求才看得出来。
// 所以这里把**真实报文片段**当夹具钉死，并且不依赖外网。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	resetWeatherCache() // 新桩 = 新上游地址，别让上一个用例的缓存混进来
	old := weatherUpstream
	weatherUpstream = srv.URL + "/"
	t.Cleanup(func() { weatherUpstream = old; srv.Close() })
	return srv
}

func TestWeatherParsesRealWttrShape(t *testing.T) {
	withStubWeather(t, wttrRealBody, http.StatusOK)
	text, resp, err := fetchWeather(context.Background(), "汕头")
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

// TestPetWeatherUsesDefaultCityWhenBlank 用户没填城市时，端到端应查默认城市，
// 而不是把空串丢给上游按 IP 定位（那会落在机房所在地）。
// 这是真实路径：handlePetWeather → effectiveWeatherCity → fetchWeather。
func TestPetWeatherUsesDefaultCityWhenBlank(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, wttrRealBody)
	}))
	resetWeatherCache()
	old := weatherUpstream
	weatherUpstream = srv.URL + "/"
	t.Cleanup(func() { weatherUpstream = old; srv.Close(); resetWeatherCache() })

	withPetCfg(t, PetConfig{WeatherEnabled: true, WeatherCity: ""})
	rec := httptest.NewRecorder()
	handlePetWeather(rec, httptest.NewRequest(http.MethodGet, "/api/pet/weather", nil))
	var got weatherResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if !strings.HasPrefix(gotPath, "/"+defaultWeatherCity) {
		t.Fatalf("空城市应查默认城市 %q，实际请求路径 %q", defaultWeatherCity, gotPath)
	}
	if got.City != defaultWeatherCity {
		t.Fatalf("播报城市应是默认城市 %q，实际 %q", defaultWeatherCity, got.City)
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
	if _, _, err := fetchWeather(context.Background(), ""); err == nil {
		t.Fatal("没有 current_condition 时应报错，而不是返回空文案")
	} else if !strings.Contains(err.Error(), "天气") {
		t.Fatalf("错误原因要是人话：%v", err)
	}

	withStubWeather(t, `{}`, http.StatusInternalServerError)
	_, _, err := fetchWeather(context.Background(), "")
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

// ---------- handlePetConfig：*bool 合并语义 + 落盘 + 405 ----------
//
// 这组用例是 12393ac 提交说明里声称"有"、实际**没有**的那部分。
// 它钉的是一个反复踩过的坑：weather_enabled 若用 bool 而不是 *bool，
// "只改城市"的保存会把开关静默关掉（AI 段当年就是这么丢 Key 的）。

// withPetCfg 切换桌宠配置并把落盘重定向到临时目录（测试不能写真实 config.json）。
func withPetCfg(t *testing.T, pc PetConfig) {
	t.Helper()
	dir := t.TempDir()
	oldPath := configFilePath
	oldPet := cfg.Pet
	configFilePath = func() string { return filepath.Join(dir, "config.json") }
	cfg.Pet = pc
	t.Cleanup(func() {
		configFilePath = oldPath
		cfg.Pet = oldPet
		resetWeatherCache()
	})
}

func getPetCfg(t *testing.T) (*httptest.ResponseRecorder, PetConfig) {
	t.Helper()
	rec := httptest.NewRecorder()
	handlePetConfig(rec, httptest.NewRequest(http.MethodGet, "/api/pet/config", nil))
	var pc PetConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &pc); err != nil {
		t.Fatalf("GET /api/pet/config 响应不是 PetConfig JSON：%v", err)
	}
	return rec, pc
}

func postPetCfg(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pet/config", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handlePetConfig(rec, req)
	return rec
}

func TestPetConfigPostKeepsUnspecifiedFields(t *testing.T) {
	withPetCfg(t, PetConfig{WeatherEnabled: true, WeatherCity: "汕头"})

	// 只改城市 → 开关不能被静默关掉
	if rec := postPetCfg(t, `{"weather_city":"北京"}`); rec.Code != 200 {
		t.Fatalf("POST 失败：%d %s", rec.Code, rec.Body.String())
	}
	if pc := cfg.Pet; !pc.WeatherEnabled || pc.WeatherCity != "北京" {
		t.Fatalf("只改城市时开关应保持开启：enabled=%v city=%q", pc.WeatherEnabled, pc.WeatherCity)
	}

	// 只关开关 → 城市要留着（用户下次开还能看到自己填的）
	if rec := postPetCfg(t, `{"weather_enabled":false}`); rec.Code != 200 {
		t.Fatalf("POST 失败：%d %s", rec.Code, rec.Body.String())
	}
	if pc := cfg.Pet; pc.WeatherEnabled || pc.WeatherCity != "北京" {
		t.Fatalf("关开关时城市应保留：enabled=%v city=%q", pc.WeatherEnabled, pc.WeatherCity)
	}

	// 落盘再回读：状态不能只活在内存里
	_, pc := getPetCfg(t)
	if pc.WeatherEnabled || pc.WeatherCity != "北京" {
		t.Fatalf("落盘回读不一致：enabled=%v city=%q", pc.WeatherEnabled, pc.WeatherCity)
	}
}

func TestPetConfigGetReturnsEffectiveCity(t *testing.T) {
	withPetCfg(t, PetConfig{WeatherEnabled: false, WeatherCity: ""})
	_, pc := getPetCfg(t)
	if pc.WeatherCity != defaultWeatherCity {
		t.Fatalf("城市留空时 GET 应回报生效值 %q，实际 %q", defaultWeatherCity, pc.WeatherCity)
	}
}

func TestPetConfigRejectsOtherMethods(t *testing.T) {
	withPetCfg(t, PetConfig{})
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		handlePetConfig(rec, httptest.NewRequest(m, "/api/pet/config", nil))
		if rec.Code != 405 {
			t.Fatalf("%s 应 405，实际 %d", m, rec.Code)
		}
	}
}

func TestPetConfigBadJSONIs400(t *testing.T) {
	withPetCfg(t, PetConfig{WeatherEnabled: true, WeatherCity: "汕头"})
	if rec := postPetCfg(t, `{不是 JSON`); rec.Code != 400 {
		t.Fatalf("坏 JSON 应 400，实际 %d", rec.Code)
	}
	// 坏请求不能顺手把配置改了
	if cfg.Pet.WeatherCity != "汕头" {
		t.Fatalf("坏请求不应改动配置： %+v", cfg.Pet)
	}
}

// ---------- handlePetWeather：方法检查 + 缓存 ----------

func TestPetWeatherRejectsNonGET(t *testing.T) {
	withPetCfg(t, PetConfig{WeatherEnabled: true, WeatherCity: "汕头"})
	rec := httptest.NewRecorder()
	handlePetWeather(rec, httptest.NewRequest(http.MethodPost, "/api/pet/weather", nil))
	if rec.Code != 405 {
		t.Fatalf("POST /api/pet/weather 应 405（别拿写方法去触发外连），实际 %d", rec.Code)
	}
}

// TestPetWeatherCachesSuccessfulResult 连点两次只打一次上游：
// wttr.in 有频率限制，而天气 5 分钟内不会变。
func TestPetWeatherCachesSuccessfulResult(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, wttrRealBody)
	}))
	resetWeatherCache()
	old := weatherUpstream
	weatherUpstream = srv.URL + "/"
	t.Cleanup(func() { weatherUpstream = old; srv.Close(); resetWeatherCache() })

	withPetCfg(t, PetConfig{WeatherEnabled: true, WeatherCity: "汕头"})
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		handlePetWeather(rec, httptest.NewRequest(http.MethodGet, "/api/pet/weather", nil))
		var got weatherResp
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("响应不是 JSON：%v", err)
		}
		if !got.Enabled || got.TempC != "25" {
			t.Fatalf("第 %d 次查询结果不对：%+v", i+1, got)
		}
	}
	if hits != 1 {
		t.Fatalf("两次查询应命中缓存只打一次上游，实际打了 %d 次", hits)
	}
}

// TestPetWeatherDoesNotCacheFailure 失败不能缓存：下一次点击就该重试。
func TestPetWeatherDoesNotCacheFailure(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, wttrRealBody)
	}))
	resetWeatherCache()
	old := weatherUpstream
	weatherUpstream = srv.URL + "/"
	t.Cleanup(func() { weatherUpstream = old; srv.Close(); resetWeatherCache() })

	withPetCfg(t, PetConfig{WeatherEnabled: true, WeatherCity: "汕头"})
	query := func() weatherResp {
		rec := httptest.NewRecorder()
		handlePetWeather(rec, httptest.NewRequest(http.MethodGet, "/api/pet/weather", nil))
		var got weatherResp
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("响应不是 JSON：%v", err)
		}
		return got
	}
	if got := query(); got.Error == "" {
		t.Fatalf("第一次应报错（上游 500），实际 %+v", got)
	}
	if got := query(); !got.Enabled || got.TempC != "25" {
		t.Fatalf("第二次应重试成功，实际 %+v", got)
	}
	if hits != 2 {
		t.Fatalf("失败不该被缓存，应两次都打上游，实际 %d 次", hits)
	}
}

// TestFetchWeatherHonorsContextCancel 客户端取消时不该傻等满超时。
func TestFetchWeatherHonorsContextCancel(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // 桩一直不返回，只有 ctx 取消能让请求结束
	}))
	t.Cleanup(func() { close(block); srv.Close() })
	old := weatherUpstream
	weatherUpstream = srv.URL + "/"
	t.Cleanup(func() { weatherUpstream = old })

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	_, _, err := fetchWeather(ctx, "汕头")
	if err == nil {
		t.Fatal("ctx 取消后应报错")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("ctx 取消后应立刻返回，实际等了 %v", elapsed)
	}
}
