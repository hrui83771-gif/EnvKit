package main

// localdev_test.go —— 手机端三件本地能力的"判据"部分。
//
// 这里只测**纯函数**和**开关缺省值**，不测"能不能真的截到一张图"：
// 后者依赖显示器、锁屏、远程桌面，在 CI/无人值守环境里空转，
// 而且真出问题时它照样是绿的（"本机恰好能截"不等于逻辑对）。
//
// 三条最容易被改坏、改坏了又不会报错的判据：
//   1. 缩放不能把图放大、不能把边界算错（错了只是画面变形，没有异常）；
//   2. 三个开关的缺省值（错了 = 出厂就给人开了终端）；
//   3. 验证码只能向前用一次（错了 = 截获的码可以重放 30 秒）。

import (
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// ---------- 1. 缩放 ----------

func solidImage(w, h int, c color.RGBA) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetRGBA(x, y, c)
		}
	}
	return m
}

func TestLanScaleDownGeometry(t *testing.T) {
	src := solidImage(400, 200, color.RGBA{10, 120, 200, 255})

	cases := []struct {
		name       string
		maxW       int
		wantW      int
		wantH      int
		sameObject bool // true = 应当原样返回，不做任何缩放
	}{
		{"缩到一半", 200, 200, 100, false},
		{"缩到 1/4", 100, 100, 50, false},
		{"恰好等于宽度", 400, 400, 200, true},
		{"比原图还宽", 800, 400, 200, true},
		{"maxW=0 视为不缩放", 0, 400, 200, true},
		{"极度压缩也不许高为 0", 5, 5, 2, false},
	}
	for _, c := range cases {
		got := lanScaleDown(src, c.maxW)
		b := got.Bounds()
		if b.Dx() != c.wantW || b.Dy() != c.wantH {
			t.Errorf("%s：得到 %dx%d，期望 %dx%d", c.name, b.Dx(), b.Dy(), c.wantW, c.wantH)
		}
		if c.sameObject && got != image.Image(src) {
			t.Errorf("%s：不该新建图像（白算一次，还会丢掉原图的调色板信息）", c.name)
		}
	}
}

// 纯色图缩放后必须还是同一个颜色。这条抓的是"通道错位"：
// 把 Pix 的 RGBA 顺序写反（比如 R 和 B 互换），尺寸断言照样全绿，只有颜色不对。
func TestLanScaleDownKeepsColor(t *testing.T) {
	src := solidImage(40, 40, color.RGBA{200, 30, 90, 255})
	got := lanScaleDown(src, 10)
	c := got.(*image.RGBA).RGBAAt(5, 5)
	if c.R != 200 || c.G != 30 || c.B != 90 || c.A != 255 {
		t.Fatalf("颜色被改了：%v", c)
	}
}

// 非零原点的图（image.Rect(100,100,140,140)）不能被当成 0 原点去索引，
// 否则缩放结果是错的但尺寸是对的。
func TestLanScaleDownHandlesNonZeroOrigin(t *testing.T) {
	src := image.NewRGBA(image.Rect(100, 100, 140, 140))
	for y := 100; y < 140; y++ {
		for x := 100; x < 140; x++ {
			src.SetRGBA(x, y, color.RGBA{250, 250, 250, 255})
		}
	}
	got := lanScaleDown(src, 10)
	if b := got.Bounds(); b.Dx() != 10 || b.Dy() != 10 {
		t.Fatalf("尺寸错：%v", b)
	}
	if c := got.(*image.RGBA).RGBAAt(3, 3); c.R != 250 {
		t.Fatalf("非零原点图取到了背景色（%v）—— 说明按绝对坐标索引了", c)
	}
}

// ---------- 2. 开关缺省值 ----------

func TestLanScaleWidthParam(t *testing.T) {
	cases := map[string]int{
		"":       lanShotDefaultW,
		"0":      lanShotDefaultW,
		"-100":   lanShotDefaultW,
		"abc":    lanShotDefaultW,
		"  ":     lanShotDefaultW,
		"100":    lanShotMinW, // 太小没意义，抬到下限
		"1280":   1280,
		"999999": lanShotMaxW,
		" 640 ":  640,
		"2560":   lanShotMaxW,
		"2561":   lanShotMaxW,
	}
	for q, want := range cases {
		if got := lanShotWidth(q); got != want {
			t.Errorf("lanShotWidth(%q)=%d，期望 %d", q, got, want)
		}
	}
}

// 出厂默认：看屏幕开（只读）、剪贴板关、终端关。
// 这三行错了的后果不对等 —— 看屏幕错了只是多给一个只读视图，
// 终端错了等于出厂就给了手机一个远程 shell，所以必须钉住。
func TestLanCapabilityDefaults(t *testing.T) {
	var empty LanConfig // 老配置里根本没有这三个字段
	if !lanAllowShotOn(empty) {
		t.Error("看屏幕应当默认开（只读能力）")
	}
	if lanAllowClipOn(empty) {
		t.Error("剪贴板应当默认关（能往电脑里写）")
	}
	if lanAllowTermOn(empty) {
		t.Error("终端应当默认关（能执行任意命令）")
	}

	// 用户显式关掉/打开时，必须听用户的
	off := LanConfig{AllowShot: boolp(false), AllowClip: boolp(true), AllowTerm: boolp(true)}
	if lanAllowShotOn(off) || !lanAllowClipOn(off) || !lanAllowTermOn(off) {
		t.Error("显式设置的值没有生效（缺省值盖掉了用户选择）")
	}
}

// ---------- 3. 二次验证 ----------

func TestLanOTPAcceptThenRejectReplay(t *testing.T) {
	secret, err := lanNewOTPSecret()
	if err != nil {
		t.Fatalf("生成密钥失败：%v", err)
	}
	if len(secret) < 16 {
		t.Fatalf("密钥太短：%q", secret)
	}
	lanOTPResetStep()
	t.Cleanup(lanOTPResetStep)

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("生成验证码失败：%v", err)
	}
	ok, replay := lanOTPCode(secret, code)
	if !ok || replay {
		t.Fatalf("刚生成的验证码应当通过，得到 ok=%v replay=%v", ok, replay)
	}
	// 同一个码再来一次：必须判重放，而不是再放进去一次
	ok, replay = lanOTPCode(secret, code)
	if ok || !replay {
		t.Fatalf("同一个验证码被用了第二次（ok=%v replay=%v）—— 防重放没起作用", ok, replay)
	}
}

func TestLanOTPRejectsMalformed(t *testing.T) {
	secret, _ := lanNewOTPSecret()
	lanOTPResetStep()
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "12 34 56"} {
		if ok, _ := lanOTPCode(secret, bad); ok {
			t.Errorf("畸形验证码 %q 被放过了", bad)
		}
	}
	if ok, _ := lanOTPCode("", "123456"); ok {
		t.Error("密钥为空时不该通过任何验证码")
	}
}

func TestLanOTPURLCarriesSecret(t *testing.T) {
	secret, _ := lanNewOTPSecret()
	u := lanOTPURL(secret)
	if !strings.HasPrefix(u, "otpauth://totp/") {
		t.Fatalf("不是 otpauth 地址：%q", u)
	}
	if !strings.Contains(u, "secret="+secret) {
		t.Fatalf("地址里没有密钥（口令器会扫出一个不能用的条目）：%q", u)
	}
	if !strings.Contains(u, "issuer=EnvKit") {
		t.Errorf("缺少 issuer，口令器里会显示成一串乱码：%q", u)
	}
	if lanOTPURL("") != "" {
		t.Error("没有密钥时应当返回空串，而不是一个残缺的 otpauth 地址")
	}
}
