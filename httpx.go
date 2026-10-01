package main

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---------- 共享 HTTP 客户端 ----------
// 原先 7 处各自 `&http.Client{...}`：每建一个 client 就新建一个 Transport（= 新建连接池），
// 于是每次请求都要重新 TCP + TLS 握手，且超时策略散落各处、难以统一调整。
// 现在统一为"一个共享 Transport（连接池共享）+ 按用途区分的超时 client"。
//
// 强制 HTTP/1.1（空 TLSNextProto = 禁用 h2 协商）：AI 流式对话会频繁提前关闭响应体
// （客户端关页面 / 心跳失败 / 单轮超时兜底），x/net/http2 的连接复用在这种场景下
// 会抛 "http2: response body closed" 且连接无法自愈，后续对话整段卡死。
// HTTP/1.1 提前关 body 只会丢弃该条连接，无此问题；对下载速度也无感。
var httpTransport = &http.Transport{
	Proxy:           proxyFromConfig, // 每请求实时读代理配置，改完无需重启
	MaxIdleConns:    64,
	IdleConnTimeout: 90 * time.Second,
	TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
}

// newHTTPClient 基于共享连接池构造 client；timeout<=0 表示不设全局超时（由 context 控制）。
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: httpTransport}
}

var (
	httpFast   = newHTTPClient(5 * time.Second)  // 本机/链端探测：要求快速失败
	httpCDN    = newHTTPClient(8 * time.Second)  // 外网版本检查 / CDN 探测：要求快速失败
	httpShort  = newHTTPClient(20 * time.Second) // 轻量 API（模型列表等）
	httpAPI    = newHTTPClient(60 * time.Second) // 非流式 LLM 调用（测试连接 / 工具轮）
	httpStream = newHTTPClient(0)                // SSE 流式：不设全局超时，由 ctx / 心跳 / 单轮计时器控制
	httpLong   = newHTTPClient(30 * time.Minute) // 大文件下载
)

// proxyFromConfig 每个请求实时读取当前代理配置（而非启动时快照），
// 用户改完代理配置后立即对新请求生效。
func proxyFromConfig(_ *http.Request) (*url.URL, error) {
	cfgMu.Lock()
	raw := strings.TrimSpace(cfg.Proxy.HTTPS)
	if raw == "" {
		raw = strings.TrimSpace(cfg.Proxy.HTTP)
	}
	cfgMu.Unlock()
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil // 配置写错时按直连处理，不阻断请求
	}
	return u, nil
}
