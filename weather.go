package main

// weather.go —— 桌宠天气查询（可选，默认关闭）。
//
// 为什么不直接让前端 fetch wttr.in：① 前端直连第三方域名会把控制台的
// 来源暴露给外部站点，也绕过了这里统一的外连出口；② 超时/错误格式
// 都要各写一遍；③ 关掉开关时前端不该有任何外连代码路径。
//
// 对外只有一个 GET /api/pet/weather：
//   - 未开启 → 200 {"enabled":false}（前端据此隐藏菜单项，不留死链）
//   - 已开启 → 拉 wttr.in 的 JSON，压成一句中文播报
//
// 网络策略：只允许 https，15 秒超时，失败返回可读原因而不是把上游
// 原始报文丢给用户。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// weatherUpstream 天气数据源（wttr.in，与原版桌宠一致）。
// 单独提出来便于测试替换。
var weatherUpstream = "https://wttr.in/"

// weatherTimeout 单次查询超时。wttr.in 偶尔 10 秒以上，给宽一点但不无限等。
const weatherTimeout = 15 * time.Second

// weatherCacheTTL 同一城市的播报缓存时长。
// 没有缓存时"右键菜单连点两下"就会连打两次 wttr.in —— 它有频率限制，
// 打太密会被 429/限流页拒绝；天气本来就 5 分钟不会变，缓存住最省事。
const weatherCacheTTL = 5 * time.Minute

// weatherResp 返回给前端的结构。
type weatherResp struct {
	Enabled bool   `json:"enabled"`
	Text    string `json:"text,omitempty"`     // 播报文案，如"汕头今天 26°，多云"
	City    string `json:"city,omitempty"`     // 实际查询的城市
	TempC   string `json:"temp_c,omitempty"`   // 当前温度
	Desc    string `json:"desc,omitempty"`     // 天气描述
	Error   string `json:"error,omitempty"`    // 失败原因（人话，英文界面下查不到译文时的保底）
	ErrCode string `json:"err_code,omitempty"` // 稳定错误键（不含数字），前端按它查词表翻译
}

// weatherCache 最近一次成功的播报（单城市、单条即可：右键菜单一次只查一个
// 城市，城市改了 key 就变，旧条目自然过期）。
var weatherCache = struct {
	mu   sync.Mutex
	key  string // upstream+city：测试会替换 upstream，把它算进 key 避免串味
	got  time.Time
	text string
	resp weatherResp
}{}

// weatherCacheGet 命中且未过期时返回 true。
func weatherCacheGet(key string) (string, weatherResp, bool) {
	weatherCache.mu.Lock()
	defer weatherCache.mu.Unlock()
	if weatherCache.key != key || weatherCache.got.IsZero() || time.Since(weatherCache.got) > weatherCacheTTL {
		return "", weatherResp{}, false
	}
	return weatherCache.text, weatherCache.resp, true
}

// weatherCachePut 只缓存成功结果：失败多半是瞬时网络问题，缓存住反而让
// 用户以为"天气功能坏了"，下一次点击就该重试。
func weatherCachePut(key, text string, resp weatherResp) {
	weatherCache.mu.Lock()
	weatherCache.key, weatherCache.got, weatherCache.text, weatherCache.resp = key, time.Now(), text, resp
	weatherCache.mu.Unlock()
}

// resetWeatherCache 清空缓存（供测试：换桩/换配置后不能吃到上一次的结果）。
func resetWeatherCache() {
	weatherCache.mu.Lock()
	weatherCache.key, weatherCache.got = "", time.Time{}
	weatherCache.mu.Unlock()
}

// wttrJ1 只取 wttr.in 的 j1 格式里需要的字段。
//
// 注意 lang_zh 与 weatherDesc 的形状都是 **[{"value":"…"}]**，不是字符串。
// 早先把它当 string 收，json.Unmarshal 直接失败 → 每次查天气都报
// "天气数据格式异常"，而错的其实是结构体。改结构体前先看真实报文。
type wttrJ1 struct {
	CurrentCondition []struct {
		TempC       string      `json:"temp_C"`
		LangZh      []wttrValue `json:"lang_zh"`
		WeatherDesc []wttrValue `json:"weatherDesc"`
		Humidity    string      `json:"humidity"`
	} `json:"current_condition"`
	NearestArea []struct {
		AreaName []wttrValue `json:"areaName"`
		Region   []wttrValue `json:"region"`
	} `json:"nearest_area"`
}

// wttrValue wttr.in 的通用单值包装。
type wttrValue struct {
	Value string `json:"value"`
}

// firstValue 取包装数组里的第一段文本。
func firstValue(vs []wttrValue) string {
	if len(vs) == 0 {
		return ""
	}
	return strings.TrimSpace(vs[0].Value)
}

// weatherZh 把 wttr.in 的英文天气描述译成中文。
//
// 为什么要自己译：wttr.in 虽然接受 lang=zh，但实测 lang_zh 字段返回的
// 仍然是英文（与 weatherDesc 逐字相同，见 _wttr4 的取证），也就是说它
// **没有**中文数据。既要在界面上说中文，就只能在本机查表。
// 覆盖 worldweatheronline 的常用代码（113 晴、116 多云、119 阴、12x 雨、
// 2xx 雷/雪、3xx 雨夹雪/霰），查不到时原样返回英文而不是留空。
var weatherZh = map[string]string{
	"sunny": "晴", "clear": "晴",
	"partly cloudy": "多云", "cloudy": "多云", "overcast": "阴",
	"mist": "薄雾", "fog": "雾", "freezing fog": "冻雾",
	"patchy rain nearby": "局部有雨", "patchy rain possible": "可能有雨",
	"light drizzle": "毛毛雨", "light rain": "小雨", "moderate rain": "中雨",
	"heavy rain": "大雨", "moderate or heavy rain shower": "阵雨",
	"light rain shower": "小阵雨", "torrential rain shower": "暴雨",
	"patchy light rain": "零星小雨", "patchy light drizzle": "零星毛毛雨",
	"thundery outbreaks possible": "可能有雷阵雨", "thundery outbreaks in nearby": "附近有雷阵雨",
	"moderate or heavy rain with thunder": "雷雨", "patchy light rain with thunder": "局部雷阵雨",
	"patchy snow possible": "可能有雪", "light snow": "小雪", "moderate snow": "中雪",
	"heavy snow": "大雪", "blizzard": "暴雪", "patchy light snow": "零星小雪",
	"light snow showers": "小阵雪", "moderate or heavy snow showers": "阵雪",
	"light sleet": "小雨夹雪", "moderate or heavy sleet": "雨夹雪",
	"patchy sleet possible": "可能有雨夹雪", "ice pellets": "冰粒",
	"freezing drizzle": "冻毛毛雨", "freezing rain": "冻雨",
	"patchy freezing drizzle possible": "可能有冻毛毛雨",
}

// translateWeather 把英文描述换成中文；大小写与尾随空白都先归一。
func translateWeather(en string) string {
	k := strings.ToLower(strings.TrimSpace(en))
	if zh, ok := weatherZh[k]; ok {
		return zh
	}
	return strings.TrimSpace(en)
}

// handlePetConfig 读写桌宠配置（目前只有天气）。
//
//	GET  → {"weather_enabled":bool,"weather_city":"…"}
//	POST → {"weather_enabled":bool?,"weather_city":string?}，只改带了的字段
//
// 为什么不复用 POST /api/config：那个接口是「整个表单替换 + 硬校验」，
// 用它保存一个开关会把表单里其它字段一起提交，且 AI 段要靠 mergeFormConfig
// 兜底。桌宠开关跟程序配置页没有语义关系，独立出入口更安全。
// WeatherEnabled 同样用指针：零值 false 无法区分"用户要关"和"没传这个字段"，
// 用 bool 会让任何一次只改城市的保存把天气静默关掉（AI 段踩过同样的坑）。
func handlePetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfgMu.Lock()
		pc := cfg.Pet
		pc.WeatherCity = effectiveWeatherCity(pc.WeatherCity)
		cfgMu.Unlock()
		writeJSON(w, pc)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var body struct {
		WeatherEnabled *bool   `json:"weather_enabled"`
		WeatherCity    *string `json:"weather_city"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}

	cfgMu.Lock()
	before := cfg.Pet
	if body.WeatherEnabled != nil {
		cfg.Pet.WeatherEnabled = *body.WeatherEnabled
	}
	if body.WeatherCity != nil {
		// 城市名允许留空（= 回到默认城市）；去掉首尾空白并限长，
		// 防止把一整段文本塞进 URL 路径。
		c := strings.TrimSpace(*body.WeatherCity)
		if r := []rune(c); len(r) > 32 {
			c = string(r[:32])
		}
		cfg.Pet.WeatherCity = c
	}
	after := cfg.Pet
	// 回报生效值而不是原始值：用户清空输入框时，界面上该显示
	// "上海"（实际查询用的地方），而不是一片空白。
	after.WeatherCity = effectiveWeatherCity(after.WeatherCity)
	saved := cfg
	cfgMu.Unlock()

	if saved.Pet != before {
		saveExternalConfig(saved)
		auditNow(actUser, "pet_config", "config.json", "pet",
			resOK, fmt.Sprintf("天气=%v 城市=%q", after.WeatherEnabled, after.WeatherCity))
	}
	writeJSON(w, after)
}

func handlePetWeather(w http.ResponseWriter, r *http.Request) {
	// 与 handlePetConfig 同一条规矩：只有 GET。少了这条检查，浏览器误发 POST
	// 也会真的去打 wttr.in —— 一个查询接口不该被任何写方法触发。
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	cfgMu.Lock()
	pc := cfg.Pet
	cfgMu.Unlock()

	if !pc.WeatherEnabled {
		writeJSON(w, weatherResp{Enabled: false})
		return
	}

	city := effectiveWeatherCity(pc.WeatherCity)
	key := weatherUpstream + "|" + city
	if text, resp, ok := weatherCacheGet(key); ok {
		resp.Enabled = true
		resp.Text = text
		writeJSON(w, resp)
		return
	}

	// 用请求自带的 context：客户端（右键菜单点开又关掉）取消时，
	// 出网请求跟着停，不会白等满 15 秒占着上游连接。
	text, resp, err := fetchWeather(r.Context(), city)
	if err != nil {
		writeJSON(w, weatherResp{Enabled: true, City: city, Error: err.Error(), ErrCode: errKeyOf(err)})
		return
	}
	weatherCachePut(key, text, resp)
	resp.Enabled = true
	resp.Text = text
	writeJSON(w, resp)
}

// errKeyOf 把 fetchWeather 的错误翻译成稳定键（不含数字/动态部分）。
// 前端用它查 I18N_EN —— "天气服务返回 500" 这种带数字的原话没法直接当键。
func errKeyOf(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "查询超时"):
		return "查询超时"
	case strings.Contains(s, "连不上天气服务"):
		return "连不上天气服务，请检查网络或代理"
	case strings.Contains(s, "天气服务返回"):
		return "天气服务返回非 200"
	case strings.Contains(s, "构造请求失败"):
		return "构造请求失败"
	case strings.Contains(s, "读取天气数据失败"):
		return "读取天气数据失败"
	case strings.Contains(s, "天气数据格式异常"):
		return "天气数据格式异常"
	case strings.Contains(s, "天气数据不完整"):
		return "天气数据不完整"
	case strings.Contains(s, "没拿到天气数据"):
		return "没拿到天气数据"
	}
	return s
}

// effectiveWeatherCity 决定这次查哪里：用户填了就按用户的，没填就用默认城市。
//
// 为什么不"留空就让上游按 IP 猜"：wttr.in 的 IP 定位经常落在机房所在地，
// 对国内用户基本是错的，而且结果不可复现。给一个有名字的默认值，
// 至少用户一眼能看出"这是默认值，可以改"。
func effectiveWeatherCity(c string) string {
	if c = strings.TrimSpace(c); c != "" {
		return c
	}
	return defaultWeatherCity
}

// fetchWeather 拉 wttr.in 并压成一句中文。city 为空时由上游按 IP 定位
// （正常路径不会走到：调用方已用 effectiveWeatherCity 兜了默认城市）。
// ctx 来自 HTTP 请求：客户端取消时上游请求立即停止（原来用 context.Background()，
// 关掉菜单后 Go 侧还会傻等满 15 秒）。
func fetchWeather(ctx context.Context, city string) (string, weatherResp, error) {
	var out weatherResp

	target := weatherUpstream
	if city != "" {
		// 城市名可能含中文/空格，必须转义，否则会被拼进路径导致 400
		target += url.PathEscape(city)
	}
	target += "?format=j1&lang=zh"

	// 把请求 context 套在超时里：客户端断开 → ctx 取消 → 请求立刻停；
	// 客户端一直等着 → 到 15 秒自动放弃。
	cctx, cancel := context.WithTimeout(ctx, weatherTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		return "", out, fmt.Errorf("构造请求失败")
	}
	// wttr.in 对非浏览器 UA 会返回纯文本降级页，这里明确要 JSON
	req.Header.Set("User-Agent", "curl/8.0")
	req.Header.Set("Accept", "application/json")

	res, err := httpShort.Do(req)
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return "", out, fmt.Errorf("查询超时（%d 秒）", int(weatherTimeout.Seconds()))
		}
		return "", out, fmt.Errorf("连不上天气服务，请检查网络或代理")
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", out, fmt.Errorf("天气服务返回 %d", res.StatusCode)
	}

	// 限制读取体积：正常响应几十 KB，超过 2 MB 一定是异常内容
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return "", out, fmt.Errorf("读取天气数据失败")
	}

	var j wttrJ1
	if err := json.Unmarshal(body, &j); err != nil {
		return "", out, fmt.Errorf("天气数据格式异常")
	}
	if len(j.CurrentCondition) == 0 {
		return "", out, fmt.Errorf("没拿到天气数据")
	}

	cur := j.CurrentCondition[0]
	// lang_zh 实测只有英文（wttr.in 并没有中文数据），所以一律走本地词表。
	// 先用 weatherDesc（最稳），lang_zh 作后备——两者内容目前相同。
	rawDesc := firstValue(cur.WeatherDesc)
	if rawDesc == "" {
		rawDesc = firstValue(cur.LangZh)
	}
	desc := translateWeather(rawDesc)
	// wttr.in 的 areaName 是**街道/区**级（汕头返回 "Xilu"），直接播报
	// "Xilu今天…" 很怪。优先用用户自己填的城市名；没填才退回
	// 「区 · 省」这种能认出来的写法，最后才用纯 areaName。
	area, region := "", ""
	if len(j.NearestArea) > 0 {
		area = firstValue(j.NearestArea[0].AreaName)
		region = firstValue(j.NearestArea[0].Region)
	}
	place := city
	if place == "" {
		place = area
	}
	if place == "" {
		place = region
	}

	out.City = place
	out.TempC = cur.TempC
	out.Desc = desc

	// 拼播报文案：地名 + 温度 + 天气；缺少的部分跳过而不是留空档
	var b strings.Builder
	if place != "" {
		b.WriteString(place)
	}
	b.WriteString("今天")
	if cur.TempC != "" {
		b.WriteString(" ")
		b.WriteString(cur.TempC)
		b.WriteString("°")
	}
	if desc != "" {
		if cur.TempC != "" {
			b.WriteString("，")
		}
		b.WriteString(desc)
	}
	text := strings.TrimSpace(b.String())
	if text == "" || text == "今天" {
		return "", out, fmt.Errorf("天气数据不完整")
	}
	return text, out, nil
}
