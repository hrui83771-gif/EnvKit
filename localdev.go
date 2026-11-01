package main

// localdev.go —— 手机能"看见"和"摸到"这台电脑的三件本地能力：
//
//	截屏（只读） / 剪贴板（读 + 写） / 本地终端（执行，见 localterm_windows.go）
//
// ## 三条约束，本文件每个 handler 都遵守
//
//  1. **只在手机访问那个独立 mux 上注册**。主控制台一行不加 ——
//     主控制台已经有 90 多个端点，再往里塞三个"能执行"的，
//     就等于把 lanaccess.go 开头那套"表面积收敛"的论证作废。
//
//  2. **默认值按危险程度分级**（见下面三个 lanAllowXxx 函数）：
//     看屏幕＝只读，默认开；剪贴板＝能往电脑里写，默认关；终端＝能执行任意命令，默认关。
//     "出厂就能截屏"和"出厂就能敲命令"不是同一件事，不该用同一个默认值。
//
//  3. **每个动作都进审计**。手机上既没有确认卡也没有人能回头看屏幕，
//     审计日志是唯一的追责线索。高频动作（截屏刷新）按分钟节流，否则会把日志写满。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/atotto/clipboard"
	"github.com/kbinani/screenshot"
)

// ===== 能力开关 =====

// lanBoolOr 三态布尔：字段缺失（老配置里没有）→ 用缺省值。
//
// 为什么不是普通 bool：这个段是**整段可缺省**的（老配置里根本没有 lan 段），
// 用普通 bool 就无法区分"用户关了"和"这个字段还不存在"，
// 升级后会把用户已经打开的能力悄悄改成 false。
func lanBoolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// 三个默认值集中在此处。
// 散落到各个 handler 里的话，"改一个漏一个"一定会发生（本项目的 MEMORY 第 2 条）。
func lanAllowShotOn(c LanConfig) bool { return lanBoolOr(c.AllowShot, true) }
func lanAllowClipOn(c LanConfig) bool { return lanBoolOr(c.AllowClip, false) }
func lanAllowTermOn(c LanConfig) bool { return lanBoolOr(c.AllowTerm, false) }

// lanFeature 鉴权 + 能力开关，两件事都不通过就直接把响应写完。
//
// 顺序不能反：**先鉴权再判开关**。反过来的话，未配对的设备也能靠
// "403 的文案是「没打开」还是「未配对」"探出这台机器上开了哪些能力。
func lanFeature(w http.ResponseWriter, r *http.Request, on bool, feat string) (LanDevice, bool) {
	dev, ok := lanAuth(w, r)
	if !ok {
		return LanDevice{}, false
	}
	if !on {
		lanWriteJSON(w, http.StatusForbidden, map[string]any{
			"ok":       false,
			"disabled": true,
			"msg":      "电脑端没有打开「" + feat + "」",
		})
		return LanDevice{}, false
	}
	return dev, true
}

// lanAuditSlow 节流审计：gap 内最多写一条。
//
// 截屏按钮点一下就是一次请求，手机会自动刷新 —— 逐条记会让真正的操作
// （配对、启动、开终端）淹没在几百条 "lan_shot" 里，那不是留痕，是噪音。
func lanAuditSlow(last *atomic.Int64, gap time.Duration, action, target, id, msg string) {
	now := time.Now().Unix()
	prev := last.Load()
	if now-prev < int64(gap/time.Second) {
		return
	}
	if last.CompareAndSwap(prev, now) {
		auditNow(actUser, action, target, id, resOK, msg)
	}
}

// ===== 截屏 =====

const (
	lanShotDefaultW = 1280 // 手机屏宽也就 400 左右，1280 已经能看清字
	lanShotMinW     = 320  // 再小就什么都读不出来了，不如不传
	lanShotMaxW     = 2560 // 上限挡住 ?max=99999 让一次请求吃满 CPU
)

var lanShotAuditAt atomic.Int64

// lanShotWidth 解析 ?max=。非法值一律回落到缺省，不报错 ——
// 这只是个"看得清就行"的参数，为它返回 400 只会让页面显示一个碎图标。
func lanShotWidth(q string) int {
	n, err := strconv.Atoi(strings.TrimSpace(q))
	if err != nil || n <= 0 {
		return lanShotDefaultW
	}
	if n < lanShotMinW {
		return lanShotMinW
	}
	if n > lanShotMaxW {
		return lanShotMaxW
	}
	return n
}

// lanScaleDown 等比缩到宽度不超过 maxW；本来就不宽则原样返回。
//
// 抽成独立函数只为可测：边界（宽为 0 的怪图、1px、恰好等于 maxW）用手造的图
// 断言最快，真去调整显示器分辨率既慢又不可重复。
//
// ## 为什么自己做重采样，不引 golang.org/x/image/draw
//
// 试过了：x/image v0.47.0 的 go.mod 把 golang.org/x/text 顶到 v0.43.0、
// x/sys 顶到 v0.49.0 —— 而本项目锁的是 text v0.37.0（那个版本是跟着已发布
// 的 exe 一起验过的）。为了一个"缩个图"的功能去动四个基础库的版本，
// 收益和风险完全不成比例。盒式重采样就三十行，还能顺手写成可测的纯函数。
//
// 用面积平均而不是最近邻：0.6 倍最近邻会把细笔画拆成断续的点，
// 手机上就变成"看得见但读不出"，那等于白截。
func lanScaleDown(src image.Image, maxW int) image.Image {
	if src == nil {
		return nil
	}
	b := src.Bounds()
	if maxW <= 0 || b.Dx() <= maxW {
		return src
	}
	h := b.Dy() * maxW / b.Dx()
	if h < 1 {
		h = 1
	}

	// 先归一成"原点在 (0,0) 的 *image.RGBA"：下面按 Pix 直读，
	// 避免每像素一次接口动态派发（1080p 走 At() 要 200ms+，手机上连点两下「刷新」就明显卡）。
	// 顺带把"边界不从 0 开始"的图也归一掉，否则下面的相对坐标会错位。
	srcPix, ok := src.(*image.RGBA)
	if !ok || srcPix.Bounds().Min != (image.Point{}) {
		srcPix = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(srcPix, srcPix.Bounds(), src, b.Min, draw.Src)
	}

	dst := image.NewRGBA(image.Rect(0, 0, maxW, h))
	for y := 0; y < h; y++ {
		y0 := y * b.Dy() / h
		y1 := (y + 1) * b.Dy() / h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < maxW; x++ {
			x0 := x * b.Dx() / maxW
			x1 := (x + 1) * b.Dx() / maxW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint32
			for sy := y0; sy < y1; sy++ {
				base := sy*srcPix.Stride + x0*4
				for sx := x0; sx < x1; sx++ {
					r += uint32(srcPix.Pix[base])
					g += uint32(srcPix.Pix[base+1])
					bl += uint32(srcPix.Pix[base+2])
					a += uint32(srcPix.Pix[base+3])
					base += 4
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			o := y*dst.Stride + x*4
			dst.Pix[o] = uint8(r / n)
			dst.Pix[o+1] = uint8(g / n)
			dst.Pix[o+2] = uint8(bl / n)
			dst.Pix[o+3] = uint8(a / n)
		}
	}
	return dst
}

// lanCaptureDisplay 抓第 n 块屏（0 = 主屏）。
//
// 库在某些显示状态下会 panic（显示器刚断开、会话锁屏、远程桌面切走），
// 这里兜一层：手机端要的是一句"取不到"，不是整个进程跟着崩。
func lanCaptureDisplay(n int) (img image.Image, err error) {
	defer func() {
		if v := recover(); v != nil {
			img, err = nil, fmt.Errorf("截屏失败：%v", v)
		}
	}()
	if screenshot.NumActiveDisplays() <= n {
		return nil, errors.New("没有可截取的屏幕（锁屏或远程桌面切换中取不到）")
	}
	return screenshot.CaptureDisplay(n)
}

func lanShotGet(w http.ResponseWriter, r *http.Request) {
	dev, ok := lanFeature(w, r, lanAllowShotOn(lanSnap()), "看屏幕")
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	img, err := lanCaptureDisplay(0)
	if err != nil {
		lanWriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	img = lanScaleDown(img, lanShotWidth(r.URL.Query().Get("max")))

	// 先编码到内存再一次性写出：直接 png.Encode(w, …) 一旦中途失败，
	// 客户端拿到的是一张"头部完好但数据截断"的图 —— 页面显示为残缺图片，
	// 排查时会误以为是截屏本身坏了。
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		lanWriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "编码失败：" + err.Error()})
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "no-store") // 桌面上可能正开着密码框，缓存下来很危险
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	_, _ = w.Write(buf.Bytes())

	lanAuditSlow(&lanShotAuditAt, time.Minute, "lan_shot", "display0", dev.ID, "手机查看了电脑屏幕："+dev.Name)
}

// ===== 剪贴板 =====

// lanClipMaxBytes 单次剪贴板上限。手机上复制一个文件路径/一段 SQL 远用不到 64KB；
// 上限存在的意义是别让一次请求把整个内存撑起来。
const lanClipMaxBytes = 64 << 10

var lanClipAuditAt atomic.Int64

// lanClipAny 读写分发。GET/POST 合成一个 handler 是为了让 lanMux 上
// "剪贴板"只占一条路由 —— 这条通道的语义就是"读一次 / 写一次"。
func lanClipAny(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		lanClipPost(w, r)
		return
	}
	lanClipGet(w, r)
}

func lanClipGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := lanFeature(w, r, lanAllowClipOn(lanSnap()), "剪贴板"); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	txt, err := clipboard.ReadAll()
	if err != nil {
		lanWriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "msg": "读取剪贴板失败：" + err.Error()})
		return
	}
	if len(txt) > lanClipMaxBytes {
		txt = txt[:lanClipMaxBytes] // 按字节截断会切坏多字节字符，但这是"取一份给人看"，可接受
	}
	lanWriteJSON(w, http.StatusOK, map[string]any{"ok": true, "text": txt, "len": len(txt)})
}

func lanClipPost(w http.ResponseWriter, r *http.Request) {
	dev, ok := lanFeature(w, r, lanAllowClipOn(lanSnap()), "剪贴板")
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	// 多留一倍余量给 JSON 转义（引号、换行会膨胀），超了就直接拒。
	if err := json.NewDecoder(io.LimitReader(r.Body, lanClipMaxBytes*2)).Decode(&body); err != nil {
		lanWriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数错误"})
		return
	}
	if len(body.Text) > lanClipMaxBytes {
		lanWriteJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "msg": fmt.Sprintf("最多 %d KB", lanClipMaxBytes>>10),
		})
		return
	}
	if err := clipboard.WriteAll(body.Text); err != nil {
		lanWriteJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "msg": "写入剪贴板失败：" + err.Error()})
		return
	}
	lanAuditSlow(&lanClipAuditAt, 3*time.Second, "lan_clip", "clipboard", dev.ID,
		fmt.Sprintf("手机写入了电脑剪贴板（%d 字节）：%s", len(body.Text), dev.Name))
	lanWriteJSON(w, http.StatusOK, map[string]any{"ok": true, "len": len(body.Text)})
}
