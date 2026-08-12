package gateway

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// HandleConnect 处理 CONNECT 隧道请求(https 代理模式):
//
//	客户端 --CONNECT github.com:443--> 网关 --CONNECT(带认证)--> 上游代理 --> GitHub
//
// 之后网关在客户端与上游代理之间双向透传字节流, TLS 由客户端与 GitHub 端到端协商。
func (g *Gateway) HandleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if target == "" {
		target = r.RequestURI
	}

	// 白名单校验
	if !g.connectAllowed(target) {
		g.log.Warn("CONNECT 被拒绝",
			zap.String("target", target),
			zap.String("client", remoteIP(r)),
		)
		g.stats.RecordRequest(hostOnly(target), http.StatusForbidden, 0)
		g.logs.Add(LogEntry{Time: time.Now(), IP: remoteIP(r), Project: hostOnly(target), Method: http.MethodConnect, Path: target, Status: http.StatusForbidden, Kind: "proxy"})
		http.Error(w, fmt.Sprintf("CONNECT %s forbidden: 目标不在白名单内", target), http.StatusForbidden)
		return
	}

	// 1. 连接上游代理
	upConn, err := net.DialTimeout("tcp", g.proxyURL.Host, g.cfg.Proxy.DialTimeout.D())
	if err != nil {
		g.log.Error("连接上游代理失败", zap.String("proxy", g.proxyURL.Host), zap.Error(err))
		g.stats.RecordRequest(hostOnly(target), http.StatusBadGateway, 0)
		g.logs.Add(LogEntry{Time: time.Now(), IP: remoteIP(r), Project: hostOnly(target), Method: http.MethodConnect, Path: target, Status: http.StatusBadGateway, Kind: "proxy"})
		http.Error(w, fmt.Sprintf("无法连接上游代理: %v", err), http.StatusBadGateway)
		return
	}
	defer upConn.Close()

	// 上游代理为 https 时先做 TLS 握手
	if g.proxyURL.Scheme == "https" {
		tlsConn := tls.Client(upConn, &tls.Config{
			ServerName:         g.proxyURL.Hostname(),
			InsecureSkipVerify: g.cfg.Proxy.InsecureSkipVerify, // #nosec G402
		})
		_ = tlsConn.SetDeadline(time.Now().Add(g.cfg.Proxy.DialTimeout.D()))
		if err := tlsConn.Handshake(); err != nil {
			g.log.Error("与上游代理 TLS 握手失败", zap.String("proxy", g.proxyURL.Host), zap.Error(err))
			http.Error(w, "与上游代理 TLS 握手失败", http.StatusBadGateway)
			return
		}
		_ = tlsConn.SetDeadline(time.Time{})
		upConn = tlsConn
	}

	// 2. 向上游代理发送 CONNECT(带 Proxy-Authorization)
	authLine := ""
	if g.proxyAuth != "" {
		authLine = fmt.Sprintf("Proxy-Authorization: %s\r\n", g.proxyAuth)
	}
	reqLine := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", target, target, authLine)
	if _, err := io.WriteString(upConn, reqLine); err != nil {
		g.log.Error("发送 CONNECT 到上游代理失败", zap.Error(err))
		http.Error(w, "发送 CONNECT 请求失败", http.StatusBadGateway)
		return
	}

	// 3. 解析上游代理响应(手动解析, 避免 http.ReadResponse 对 CONNECT 的特殊处理)
	br := bufio.NewReader(upConn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		g.log.Error("读取上游代理响应失败", zap.Error(err))
		http.Error(w, "读取上游代理响应失败", http.StatusBadGateway)
		return
	}
	parts := strings.SplitN(strings.TrimRight(statusLine, "\r\n"), " ", 3)
	if len(parts) < 2 {
		http.Error(w, "上游代理响应格式错误", http.StatusBadGateway)
		return
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		http.Error(w, "上游代理响应状态码解析失败", http.StatusBadGateway)
		return
	}
	// 读取并丢弃剩余响应头
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			http.Error(w, "读取上游代理响应头失败", http.StatusBadGateway)
			return
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	if code != http.StatusOK {
		g.log.Warn("上游代理拒绝 CONNECT",
			zap.Int("status", code),
			zap.String("target", target),
			zap.String("proxy", g.proxyURL.Host),
		)
		g.stats.RecordRequest(hostOnly(target), http.StatusBadGateway, 0)
		g.logs.Add(LogEntry{Time: time.Now(), IP: remoteIP(r), Project: hostOnly(target), Method: http.MethodConnect, Path: target, Status: http.StatusBadGateway, Kind: "proxy"})
		http.Error(w, fmt.Sprintf("上游代理拒绝 CONNECT: %s", strings.TrimSpace(statusLine)), http.StatusBadGateway)
		return
	}

	// 4. 劫持客户端连接, 双向透传
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "当前连接不支持隧道", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		g.log.Error("劫持客户端连接失败", zap.Error(err))
		return
	}
	defer clientConn.Close()

	g.stats.BeginConnect(target)

	g.log.Info("CONNECT 隧道建立",
		zap.String("target", target),
		zap.String("proxy", g.proxyURL.Host),
		zap.String("client", remoteIP(r)),
	)

	g.logs.Add(LogEntry{
		Time:    time.Now(),
		IP:      remoteIP(r),
		Project: hostOnly(target),
		Method:  http.MethodConnect,
		Path:    target,
		Status:  http.StatusOK,
		Kind:    "proxy",
	})

	// 5. 告知客户端隧道已建立
	if _, err := clientBuf.WriteString("HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	if err := clientBuf.Flush(); err != nil {
		return
	}

	// 双向转发:
	//   上游 → 客户端: 用 br 读取, 不丢失已缓冲的数据
	//   客户端 → 上游: 用 clientBuf 读取, 冲刷劫持时可能缓冲的数据
	down := &countingWriter{w: clientConn} // 上游 → 客户端(下行)
	up := &countingWriter{w: upConn}       // 客户端 → 上游(上行)
	done := make(chan struct{}, 2)
	go func() {
		defer upConn.Close()
		_, _ = io.Copy(down, br)
		done <- struct{}{}
	}()
	go func() {
		defer clientConn.Close()
		_, _ = io.Copy(up, clientBuf)
		done <- struct{}{}
	}()
	<-done
	g.stats.EndConnect(target, up.n, down.n, false)
}

// countingWriter 统计写入字节数的 io.Writer 包装器
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// remoteIP 从 RemoteAddr 提取客户端 IP
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
