package gateway

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// proxyStatus 上游代理可用性状态(后台定期检测, 供管理面板展示)
type proxyStatus struct {
	mu        sync.RWMutex
	ok        bool
	latencyMS int64
	errorMsg  string
	checkedAt time.Time
	checking  sync.Mutex // 防止并发重复检测
}

func newProxyStatus() *proxyStatus {
	return &proxyStatus{}
}

// snapshot 返回状态快照(JSON)
func (p *proxyStatus) snapshot() map[string]any {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return map[string]any{
		"ok":         p.ok,
		"latency_ms": p.latencyMS,
		"error":      p.errorMsg,
		"checked_at": p.checkedAt,
	}
}

func (p *proxyStatus) set(ok bool, latencyMS int64, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ok = ok
	p.latencyMS = latencyMS
	p.errorMsg = errMsg
	p.checkedAt = time.Now()
}

// startProxyMonitor 启动后台代理健康检测(启动即检, 之后每 60s 一次)
func (g *Gateway) startProxyMonitor() {
	go func() {
		g.checkProxy()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			g.checkProxy()
		}
	}()
}

// checkProxy 通过上游代理请求 github.com, 验证代理是否可用并记录延迟
func (g *Gateway) checkProxy() {
	g.proxyStatus.checking.Lock() // 手动触发与后台定时检测串行, 避免并发
	defer g.proxyStatus.checking.Unlock()

	start := time.Now()
	req, err := http.NewRequest(http.MethodGet, "https://github.com/", nil)
	if err != nil {
		g.proxyStatus.set(false, 0, err.Error())
		return
	}
	req.Header.Set("User-Agent", "github-gateway/"+Version)

	resp, err := g.upstream.RoundTrip(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		g.proxyStatus.set(false, latency, err.Error())
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	_ = resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		g.proxyStatus.set(true, latency, "")
	} else {
		g.proxyStatus.set(false, latency, fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
}
