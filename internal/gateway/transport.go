package gateway

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"time"

	"go-proxy-github-cn/internal/config"
)

// newUpstreamTransport 构建经由上游代理(带用户名密码)访问 GitHub 的 Transport。
// Go 标准库会自动为代理 URL 中的 userinfo 生成 Proxy-Authorization: Basic 请求头,
// 因此无需手工处理认证。
func newUpstreamTransport(cfg *config.Config) (*http.Transport, error) {
	proxyURL, err := url.Parse(cfg.Proxy.UpstreamProxy)
	if err != nil {
		return nil, err
	}
	return &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout:   cfg.Proxy.DialTimeout.D(),
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       cfg.Proxy.IdleConnTimeout.D(),
		TLSHandshakeTimeout:   cfg.Proxy.TLSTimeout.D(),
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: cfg.Proxy.ResponseTimeout.D(),
		// #nosec G402 -- 仅当配置显式开启 insecure_skip_verify 时跳过校验
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Proxy.InsecureSkipVerify},
	}, nil
}
