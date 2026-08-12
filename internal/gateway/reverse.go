package gateway

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// handleReverseProxy 处理普通 HTTP 反向代理请求, 支持三种形态:
//
//  1. HTTP 代理模式(绝对 URI): 客户端把网关配成系统/浏览器代理后,
//     http 请求形如 GET http://github.com/xxx
//  2. 域名直连模式(Host 头): 通过 DNS/hosts 把 GitHub 域名指向网关,
//     请求形如 GET /xxx, Host: github.com
//  3. 路径模式(ghproxy 风格): 请求形如 GET /https://github.com/xxx
//     或 GET /github.com/xxx(配合 git 的 url.*.insteadOf 使用)
func (g *Gateway) handleReverseProxy(c *gin.Context) {
	target, ok := g.resolveTarget(c.Request)
	if !ok {
		g.log.Warn("请求被拒绝",
			zap.String("method", c.Request.Method),
			zap.String("host", c.Request.Host),
			zap.String("uri", c.Request.RequestURI),
			zap.String("client", c.ClientIP()),
		)
		c.JSON(http.StatusForbidden, gin.H{
			"error":   "目标域名不在白名单内, 请求被拒绝",
			"allowed": g.cfg.Proxy.AllowedDomains,
		})
		g.stats.RecordRequest(hostOnly(c.Request.Host), http.StatusForbidden, int(c.Writer.Size()))
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = target.Path
			req.URL.RawPath = ""
			req.URL.RawQuery = target.RawQuery
			req.Host = target.Host
			req.Header.Set("X-Forwarded-For", c.ClientIP())
			req.Header.Set("X-Forwarded-Proto", "http")
			// 不把客户端带来的代理认证头转发给 GitHub
			req.Header.Del("Proxy-Authorization")
			req.Header.Del("Proxy-Connection")
		},
		Transport:     g.upstream,
		FlushInterval: -1, // 立即刷新, 适配 git 智能 HTTP 的流式响应
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			g.log.Warn("上游请求失败",
				zap.String("target", target.String()),
				zap.Error(err),
			)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("bad gateway: " + err.Error() + "\n"))
		},
		ModifyResponse: func(resp *http.Response) error {
			g.log.Debug("上游响应",
				zap.String("target", target.String()),
				zap.Int("status", resp.StatusCode),
				zap.String("content_type", resp.Header.Get("Content-Type")),
			)
			return nil
		},
	}
	proxy.ServeHTTP(c.Writer, c.Request)
	g.stats.RecordRequest(target.Host, c.Writer.Status(), int(c.Writer.Size()))
}

// resolveTarget 解析请求对应的 GitHub 目标地址
func (g *Gateway) resolveTarget(r *http.Request) (*url.URL, bool) {
	// 1) HTTP 代理模式: 绝对 URI 形式
	if r.URL.IsAbs() {
		host := strings.ToLower(r.URL.Hostname())
		if !g.isAllowed(host) {
			return nil, false
		}
		return &url.URL{Scheme: r.URL.Scheme, Host: r.URL.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}, true
	}

	// 2) 路径模式(ghproxy 风格): /https://github.com/xxx 或 /github.com/xxx
	// 注意: 需在 Host 域名匹配之前判断, 否则 Host 为网关自身地址时会被误判
	if g.cfg.Proxy.PathProxy {
		p := strings.TrimPrefix(r.URL.EscapedPath(), "/")
		lower := strings.ToLower(p)
		if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
			u, err := url.Parse(p)
			if err == nil && u.Host != "" && g.isAllowed(u.Hostname()) {
				u.RawQuery = r.URL.RawQuery
				return u, true
			}
		} else if first, rest, ok := strings.Cut(p, "/"); ok {
			if g.isAllowed(first) {
				return &url.URL{Scheme: "https", Host: first, Path: "/" + rest, RawQuery: r.URL.RawQuery}, true
			}
		}
	}

	// 3) 域名直连模式: Host 头为 GitHub 域名(需把域名 DNS/hosts 指向网关)
	if host := strings.ToLower(hostOnly(r.Host)); g.isAllowed(host) {
		return &url.URL{Scheme: "https", Host: r.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}, true
	}
	return nil, false
}

// hostOnly 去掉主机名中的端口号, 兼容 IPv6
func hostOnly(hostport string) string {
	h := strings.ToLower(strings.TrimSpace(hostport))
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		if strings.HasPrefix(h, "[") {
			if j := strings.IndexByte(h, ']'); j > 0 && j+1 < len(h) && h[j+1] == ':' {
				return h[:j+1]
			}
			return h
		}
		return h[:i]
	}
	return h
}
