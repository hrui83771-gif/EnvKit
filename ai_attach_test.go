package main

// ai_attach_test.go —— 图片附件的契约测试。
//
// 这里有两条是"守门"性质，不是功能测试：
//   - TestAIMsgJSONUnchangedWithoutImages：无图消息的 JSON 必须逐字节等于改造前。
//     一旦变了，所有请求的字节前缀都变，DeepSeek 的上下文缓存（按前缀命中）整体失效，
//     而且是**静默**失效 —— 只有账单变贵，没有任何报错能提醒你。
//   - TestAIAttachHydrateDropsOldImages：历史里每轮重发一张图 = 每轮白付 1024 token。
//     这条断言的是"更早的图会被换成文字占位"，不是"图片能发出去"。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tinyPNG 造一张真的 PNG（内容必须是真图：服务端按 http.DetectContentType 判定，
// 随便几个字节是过不了的 —— 这正是那条"不信前端声明"的判据在起作用）。
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 1, color.RGBA{G: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatalf("造图失败：%v", err)
	}
	return b.Bytes()
}

func withTempAttachDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := aiAttachDirFn
	aiAttachDirFn = func() string { return dir }
	t.Cleanup(func() { aiAttachDirFn = old })
	return dir
}

// ---------- 序列化契约 ----------

func TestAIMsgJSONUnchangedWithoutImages(t *testing.T) {
	cases := []struct {
		msg  aiMsg
		want string
	}{
		{aiMsg{Role: "user", Content: "hi"}, `{"role":"user","content":"hi"}`},
		{aiMsg{Role: "system", Content: "sys"}, `{"role":"system","content":"sys"}`},
		{aiMsg{Role: "tool", Content: "r", ToolCallID: "c1"}, `{"role":"tool","content":"r","tool_call_id":"c1"}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.msg)
		if err != nil {
			t.Fatalf("序列化失败：%v", err)
		}
		if string(b) != c.want {
			t.Fatalf("无图消息的 JSON 变了（会让上游前缀缓存整体失效）\n实际：%s\n期望：%s", b, c.want)
		}
	}
	// assistant 仍然始终带 reasoning_content（思考型模型要求）
	b, _ := json.Marshal(aiMsg{Role: "assistant", Content: "ok"})
	if !strings.Contains(string(b), `"reasoning_content":""`) {
		t.Fatalf("assistant 消息必须始终带 reasoning_content，实际：%s", b)
	}
}

func TestAIMsgWithImagesUsesContentBlocks(t *testing.T) {
	m := aiMsg{Role: "user", Content: "这张图哪里报错？", Images: []aiImage{
		{ID: strings.Repeat("a", 32), Name: "a.png", MIME: "image/png", DataURL: "data:image/png;base64,AAA"},
	}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	var got struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("带图消息的 content 应该是块数组，实际 %s（%v）", b, err)
	}
	if got.Role != "user" || len(got.Content) != 2 {
		t.Fatalf("期望 role=user + 2 块，实际 %s", b)
	}
	// 顺序：先文字后图片。反过来的话模型会先"看到图"再读到问题，容易答偏。
	if got.Content[0].Type != "text" || got.Content[0].Text == "" {
		t.Fatalf("第一块必须是文字块，实际：%s", b)
	}
	if got.Content[1].Type != "image_url" || got.Content[1].ImageURL.URL == "" {
		t.Fatalf("第二块必须是 image_url，实际：%s", b)
	}
	// 自定义键不得泄漏给上游：部分 OpenAI 兼容实现见到未知键直接 400
	if strings.Contains(string(b), `"images"`) {
		t.Fatalf("images 字段不许发给上游，实际：%s", b)
	}
}

// 图片读盘失败（DataURL 空）时不能产生空 image 块：上游会 400，而且用户看不懂。
func TestAIMultiPartSkipsUnloadedImages(t *testing.T) {
	m := aiMsg{Role: "user", Content: "看图", Images: []aiImage{{ID: strings.Repeat("b", 32)}}}
	b, _ := json.Marshal(m)
	if strings.Contains(string(b), "image_url") {
		t.Fatalf("没有 DataURL 的图不该出现 image_url 块：%s", b)
	}
	if !strings.Contains(string(b), `"text":"看图"`) {
		t.Fatalf("文字块必须保留：%s", b)
	}
}

// ---------- 上传 / 读取 ----------

func TestAIAttachRoundTrip(t *testing.T) {
	withTempAttachDir(t)
	raw := tinyPNG(t)

	body, _ := json.Marshal(map[string]any{
		"name": "shot.png", "mime": "image/png",
		"content": "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw),
	})
	rec := httptest.NewRecorder()
	handleAIAttach(rec, httptest.NewRequest(http.MethodPost, "/api/ai/attach", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("上传应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID   string `json:"id"`
		MIME string `json:"mime"`
		Size int    `json:"bytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("返回不是 JSON：%s", rec.Body.String())
	}
	if !aiAttachIDOK(out.ID) || out.MIME != "image/png" || out.Size != len(raw) {
		t.Fatalf("返回字段不对：%+v", out)
	}

	// 读回来要字节一致
	rec2 := httptest.NewRecorder()
	handleAIAttach(rec2, httptest.NewRequest(http.MethodGet, "/api/ai/attach?id="+out.ID, nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("读取应成功，实际 %d", rec2.Code)
	}
	if !bytes.Equal(rec2.Body.Bytes(), raw) {
		t.Fatal("读回的字节与上传的不一致")
	}
	if ct := rec2.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type 应为 image/png，实际 %q", ct)
	}
	// hydrate 也要能读成 data URL
	u, err := aiAttachLoad(aiImage{ID: out.ID, Name: "shot.png"})
	if err != nil || !strings.HasPrefix(u, "data:image/png;base64,") {
		t.Fatalf("aiAttachLoad 失败：%q %v", u, err)
	}
}

// 判据是"文件内容是不是图片"，不是"前端说的 MIME 是不是图片"。
func TestAIAttachRejectsNonImageBytes(t *testing.T) {
	withTempAttachDir(t)
	body, _ := json.Marshal(map[string]any{
		"name": "evil.png", "mime": "image/png",
		"content": base64.StdEncoding.EncodeToString([]byte("<?php echo 1; ?>")),
	})
	rec := httptest.NewRecorder()
	handleAIAttach(rec, httptest.NewRequest(http.MethodPost, "/api/ai/attach", bytes.NewReader(body)))
	if rec.Code == http.StatusOK {
		t.Fatal("文件内容不是图片，必须拒绝（否则这端点就是个任意文件落盘通道）")
	}
	ents, _ := os.ReadDir(aiAttachDir())
	for _, e := range ents {
		if aiAttachIDOK(e.Name()) {
			t.Fatalf("被拒的内容不该落盘，却留下了 %s", e.Name())
		}
	}
}

// ID 直接当文件名用 → 形状必须白名单，否则就是目录穿越。
func TestAIAttachRejectsBadID(t *testing.T) {
	withTempAttachDir(t)
	for _, id := range []string{"../config.json", "..\\config.json", "", strings.Repeat("a", 31), strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		rec := httptest.NewRecorder()
		handleAIAttach(rec, httptest.NewRequest(http.MethodGet, "/api/ai/attach?id="+id, nil))
		if rec.Code == http.StatusOK {
			t.Fatalf("非法 ID %q 不该被接受", id)
		}
	}
}

// ---------- 历史注入 ----------

func TestAIAttachHydrateDropsOldImages(t *testing.T) {
	dir := withTempAttachDir(t)
	mk := func(id string) aiImage {
		if err := os.WriteFile(filepath.Join(dir, id), tinyPNG(t), 0o644); err != nil {
			t.Fatalf("写附件失败：%v", err)
		}
		return aiImage{ID: id, Name: id[:4] + ".png"}
	}
	var msgs []aiMsg
	for i := 0; i < aiAttachKeepMsgs+1; i++ {
		id := strings.Repeat(string(rune('a'+i)), 32)
		msgs = append(msgs, aiMsg{Role: "user", Content: "看图", Images: []aiImage{mk(id)}})
	}
	out := aiAttachHydrate(msgs)
	// 最早那条：图不再发送，但**必须留下文字占位**——否则模型以为用户没给过图
	if len(out[0].Images) != 0 {
		t.Fatalf("超过保留条数的旧图不该再发，实际还剩 %d 张", len(out[0].Images))
	}
	if !strings.Contains(out[0].Content, "原本附有图片") {
		t.Fatalf("旧图降级后必须留占位文字，实际：%q", out[0].Content)
	}
	// 最近 aiAttachKeepMsgs 条：图照发
	for i := 1; i < len(out); i++ {
		if len(out[i].Images) != 1 || !strings.HasPrefix(out[i].Images[0].DataURL, "data:image/png;base64,") {
			t.Fatalf("第 %d 条（最近范围内）应携带图片，实际 %+v", i, out[i].Images)
		}
	}
}

// 文件被清理/手动删掉：降级成文字并记 WARN，绝不静默当成"没这张图"。
func TestAIAttachHydrateMissingFileDegrades(t *testing.T) {
	withTempAttachDir(t)
	msgs := []aiMsg{{Role: "user", Content: "看这张", Images: []aiImage{{ID: strings.Repeat("c", 32), Name: "gone.png"}}}}
	out := aiAttachHydrate(msgs)
	if len(out[0].Images) != 0 {
		t.Fatal("读不到文件时不应继续引用它")
	}
	if !strings.Contains(out[0].Content, "gone.png") {
		t.Fatalf("占位文字里要写清是哪张图没了，实际：%q", out[0].Content)
	}
}

func TestIsImageUnsupported(t *testing.T) {
	yes := []string{
		"This model does not support image",
		"image input is not supported",
		"unsupported image format",
		"该模型不支持图片",
	}
	no := []string{
		"rate limit exceeded",
		"image file too large", // 是大小问题，不是能力问题，不能误导用户去换模型
		"invalid api key",
	}
	for _, s := range yes {
		if !aiIsImageUnsupported(s) {
			t.Fatalf("应判为「模型不吃图片」：%q", s)
		}
	}
	for _, s := range no {
		if aiIsImageUnsupported(s) {
			t.Fatalf("不该判为「模型不吃图片」：%q", s)
		}
	}
}

// 整条链的一次性冒烟：请求 JSON → aiMsg.Images → 读盘 → 上游 payload。
// 分段单测各自绿、拼起来断掉，是这次真实发生过的事（前端白名单重建丢了 images），
// 所以留一条端到端的形状断言。
func TestImageLinkThroughRequestDecode(t *testing.T) {
	dir := withTempAttachDir(t)
	id := strings.Repeat("d", 32)
	if err := os.WriteFile(filepath.Join(dir, id), tinyPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := `{"messages":[{"role":"user","content":"看图","images":[{"id":"` + id + `","name":"s.png","mime":"image/png"}]}]}`
	var req aiChatReq
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("decode 失败：%v", err)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Images) != 1 {
		t.Fatalf("images 没解出来：%+v", req.Messages)
	}
	hyd := aiAttachHydrate(req.Messages)
	if len(hyd[0].Images) != 1 || hyd[0].Images[0].DataURL == "" {
		t.Fatalf("hydrate 没填 DataURL：%+v", hyd[0].Images)
	}
	p := aiPayload(hyd, nil, AIQuirk{}, -1, false)
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), "image_url") {
		t.Fatalf("最终 payload 里没有 image_url：%s", b)
	}
}

// ---------- 24 小时自动清理 ----------
//
// 这组用例守的不是"GC 能被调用"，而是**判据的方向**。TTL 从 30 天收到 24 小时后，
// 一旦 deadline 写反（Add 而不是 Add(-)），删的就是"刚刚拖进来"的图 ——
// 用户看到的是"图传上去了，模型说没收到"，而且每次都要重新拖，症状极难归因。
// 所以两个方向各断言一次，外加"非附件文件不许碰"。

func writeAttach(t *testing.T, dir, id string, age time.Duration) {
	t.Helper()
	p := filepath.Join(dir, id)
	if err := os.WriteFile(p, tinyPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-age)
	if err := os.Chtimes(p, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func TestAttachGCDeletesExpiredKeepsFresh(t *testing.T) {
	dir := withTempAttachDir(t)
	expired := strings.Repeat("a", 32) // 25 小时前：该删
	border := strings.Repeat("b", 32)  // 23 小时前：还在保留期内
	fresh := strings.Repeat("c", 32)   // 刚刚：绝不能删
	writeAttach(t, dir, expired, 25*time.Hour)
	writeAttach(t, dir, border, 23*time.Hour)
	writeAttach(t, dir, fresh, 0)

	aiAttachGC()

	if _, err := os.Stat(filepath.Join(dir, expired)); !os.IsNotExist(err) {
		t.Fatalf("超过 24 小时的附件必须被删掉，实际还在（err=%v）", err)
	}
	for name, id := range map[string]string{"23 小时前的": border, "刚写入的": fresh} {
		if _, err := os.Stat(filepath.Join(dir, id)); err != nil {
			t.Fatalf("%s附件被误删了：%v", name, err)
		}
	}
}

func TestAttachGCLeavesForeignFilesAlone(t *testing.T) {
	dir := withTempAttachDir(t)
	// 这个目录将来可能加 index / 说明文件；GC 只认 32 位小写 hex 的附件名。
	// 不做这条限制，它就是个"每天删一次别人东西"的定时炸弹。
	foreign := filepath.Join(dir, "notes.txt")
	sub := filepath.Join(dir, "sub")
	if err := os.WriteFile(foreign, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(foreign, old, old); err != nil {
		t.Fatal(err)
	}

	aiAttachGC()

	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("非附件文件被删了：%v", err)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Fatalf("子目录被删了：%v", err)
	}
}

func TestAttachGCCapsTotalCount(t *testing.T) {
	dir := withTempAttachDir(t)
	// 全部落在保留期内，只能靠张数上限兜底：防"一天里狂传"把盘写爆。
	// 造 maxCount+3 张，最旧的 3 张必须被删，最新的必须留着。
	n := aiAttachMaxCount + 3
	base := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%032x", i)
		p := filepath.Join(dir, id)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		ts := base.Add(time.Duration(i) * time.Second) // i 越大越新
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	aiAttachGC()

	left := 0
	for _, e := range mustReadDir(t, dir) {
		if aiAttachIDOK(e.Name()) {
			left++
		}
	}
	if left != aiAttachMaxCount {
		t.Fatalf("清理后应剩 %d 张，实际 %d 张", aiAttachMaxCount, left)
	}
	for i := 0; i < 3; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%032x", i))); !os.IsNotExist(err) {
			t.Fatalf("最旧的 3 张（i=%d）应被删掉", i)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%032x", n-1))); err != nil {
		t.Fatalf("最新的那张不该被删：%v", err)
	}
}

func TestAttachGCIsIdempotent(t *testing.T) {
	dir := withTempAttachDir(t)
	writeAttach(t, dir, strings.Repeat("e", 32), 0)
	aiAttachGC()
	aiAttachGC() // 再跑一遍不能把剩下的删掉，也不能报错
	if _, err := os.Stat(filepath.Join(dir, strings.Repeat("e", 32))); err != nil {
		t.Fatalf("第二遍清理误删了保留期内的文件：%v", err)
	}
}

// 源码契约：清理逻辑写好了但没在启动时接上，等于没写。
// ticker 那一小时在单测里等不了，所以只能断言"main 里确实调了"——
// 这类"调用被删掉"的回退，靠测函数本身是抓不到的。
func TestAttachGCIsWiredIntoMain(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读 main.go 失败：%v", err)
	}
	if !strings.Contains(string(src), "aiAttachStartGC()") {
		t.Fatal("main.go 里没有调用 aiAttachStartGC()：图片附件不会自动清理了")
	}
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录失败：%v", err)
	}
	return ents
}
