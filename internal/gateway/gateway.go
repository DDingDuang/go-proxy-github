package gateway

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"go-proxy-github-cn/internal/config"
)

// Version 网关版本号
const Version = "1.0.0"

// Gateway GitHub 加速网关:
//
//	客户端(浏览器/git) --HTTP--> 网关 --带认证的HTTP代理--> GitHub
type Gateway struct {
	cfg         *config.Config
	log         *zap.Logger
	upstream    *http.Transport // 经由上游代理访问 GitHub 的 Transport
	proxyURL    *url.URL        // 上游代理地址
	proxyAuth   string          // 上游代理的 Proxy-Authorization 值(Basic)
	allowed     map[string]struct{}
	stats       *Stats       // 运行统计(管理面板)
	logs        *LogBuffer   // 内存日志缓冲(proxy/manage)
	store       *CheckStore  // SQLite 存储(repo_check 持久化, 可为 nil)
	proxyStatus *proxyStatus // 上游代理可用性状态
}

// New 创建网关
func New(cfg *config.Config, log *zap.Logger) (*Gateway, error) {
	transport, err := newUpstreamTransport(cfg)
	if err != nil {
		return nil, err
	}
	proxyURL, err := url.Parse(cfg.Proxy.UpstreamProxy)
	if err != nil {
		return nil, err
	}

	g := &Gateway{
		cfg:         cfg,
		log:         log,
		upstream:    transport,
		proxyURL:    proxyURL,
		allowed:     make(map[string]struct{}, len(cfg.Proxy.AllowedDomains)),
		stats:       NewStats(),
		logs:        NewLogBuffer(300),
		proxyStatus: newProxyStatus(),
	}
	// 项目检查记录持久化(SQLite); 未配置 db_path 时仅存内存
	if strings.TrimSpace(cfg.Server.DBPath) != "" {
		store, err := OpenCheckStore(cfg.Server.DBPath)
		if err != nil {
			log.Warn("初始化 SQLite 存储失败, 回退内存模式", zap.String("db_path", cfg.Server.DBPath), zap.Error(err))
		} else {
			g.store = store
			log.Info("项目检查记录持久化已启用", zap.String("db_path", cfg.Server.DBPath))
		}
	}
	// 预生成上游代理认证头
	if proxyURL.User != nil {
		g.proxyAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte(proxyURL.User.String()))
	}
	for _, d := range cfg.Proxy.AllowedDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			g.allowed[d] = struct{}{}
		}
	}
	// 启动后台代理健康检测
	g.startProxyMonitor()

	return g, nil
}

// Close 释放资源(关闭 SQLite 存储)
func (g *Gateway) Close() {
	if g.store != nil {
		_ = g.store.Close()
	}
}

// isAllowed 判断 host 是否在白名单内(支持后缀匹配:
// 白名单含 github.com 时, api.github.com / raw.githubusercontent.com 均命中)
func (g *Gateway) isAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if _, ok := g.allowed[host]; ok {
		return true
	}
	for d := range g.allowed {
		if strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// connectAllowed 判断 CONNECT 隧道目标是否允许
func (g *Gateway) connectAllowed(target string) bool {
	if g.cfg.Proxy.ConnectAllowAny {
		return true
	}
	return g.isAllowed(hostOnly(target))
}

// Engine 构建 gin 管理面引擎(信息页 + 健康检查 + 反向代理路由)
func (g *Gateway) Engine() *gin.Engine {
	r := gin.New()
	_ = r.SetTrustedProxies(nil) // 不信任任何代理, 防止伪造客户端 IP
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.HandleMethodNotAllowed = false

	r.Use(gin.Recovery(), g.accessLog())

	r.GET("/", g.handleIndex)
	r.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	r.GET("/favicon.ico", func(c *gin.Context) {
		c.Status(http.StatusNoContent) // 避免浏览器请求图标时被当成代理请求
	})
	r.GET("/api/stats", g.handleStats)
	r.GET("/api/logs", g.handleLogs)
	r.POST("/api/repo/check", g.handleRepoCheck)
	r.POST("/api/proxy/check", g.handleProxyCheck)

	// 其余所有路径交给反向代理处理
	r.NoRoute(g.handleReverseProxy)
	return r
}

// accessLog zap 访问日志中间件
func (g *Gateway) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		c.Next()
		fields := []zap.Field{
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("query", c.Request.URL.RawQuery),
			zap.String("client", c.ClientIP()),
			zap.String("host", c.Request.Host),
			zap.Int("status", c.Writer.Status()),
			zap.Int("bytes", c.Writer.Size()),
			zap.Duration("latency", time.Since(start)),
		}
		if c.Writer.Status() >= 500 {
			g.log.Warn("request", fields...)
		} else {
			g.log.Info("request", fields...)
		}

		// 写入管理面板日志缓冲
		project := hostOnly(c.Request.Host)
		kind := "proxy"
		if target, ok := g.resolveTarget(c.Request); ok {
			project = extractProject(target)
		}
		switch {
		case path == "/api/repo/check":
			kind = "repo_check"
		case path == "/" || path == "/healthz" || strings.HasPrefix(path, "/api/"):
			kind = "manage"
		}
		// repo_check 日志由 handleRepoCheck 自行记录(带被检查的项目地址)
		if kind != "repo_check" {
			g.logs.Add(LogEntry{
				Time:      time.Now(),
				IP:        c.ClientIP(),
				Project:   project,
				Method:    c.Request.Method,
				Path:      path,
				Status:    c.Writer.Status(),
				LatencyMS: time.Since(start).Milliseconds(),
				Bytes:     int64(c.Writer.Size()),
				Kind:      kind,
			})
		}
	}
}

// handleStats 管理面板数据接口(配置摘要 + 实时统计 + 代理状态)
func (g *Gateway) handleStats(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"app":               "github-gateway",
		"version":           Version,
		"listen":            g.cfg.Server.Listen,
		"local_ips":         localIPv4s(),
		"upstream_proxy":    maskedProxy(g.cfg.Proxy.UpstreamProxy),
		"path_proxy":        g.cfg.Proxy.PathProxy,
		"connect_allow_any": g.cfg.Proxy.ConnectAllowAny,
		"allowed_domains":   g.cfg.Proxy.AllowedDomains,
		"stats":             g.stats.Snapshot(),
		"proxy_status":      g.proxyStatus.snapshot(),
	})
}

// handleProxyCheck 手动触发一次上游代理检测, 返回最新状态
func (g *Gateway) handleProxyCheck(c *gin.Context) {
	g.checkProxy()
	c.JSON(http.StatusOK, gin.H{"proxy_status": g.proxyStatus.snapshot()})
}

// handleLogs 管理面板访问日志接口。
// kind=repo_check 时从 SQLite 分页查询(持久化), 其他 kind 从内存缓冲查询(兼容)。
// 分页参数: page(从 1 开始), page_size(默认 20, 最大 200)
func (g *Gateway) handleLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	kind := c.Query("kind")

	if kind == "repo_check" && g.store != nil {
		entries, total, err := g.store.List(page, pageSize)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		pages := int((total + int64(pageSize) - 1) / int64(pageSize))
		if pages < 1 {
			pages = 1
		}
		c.JSON(http.StatusOK, gin.H{
			"logs":      entries,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
			"pages":     pages,
			"source":    "sqlite",
		})
		return
	}

	// 内存模式(未配置 db_path 或非 repo_check)
	limit := pageSize
	if limit < 1 || limit > 500 {
		limit = 100
	}
	all := g.logs.List(500)
	out := make([]LogEntry, 0, len(all))
	for _, l := range all {
		if kind != "" && l.Kind != kind {
			continue
		}
		out = append(out, l)
		if len(out) >= limit {
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{"logs": out, "total": int64(len(out)), "page": 1, "page_size": limit, "pages": 1, "source": "memory"})
}

// maskedProxy 隐藏上游代理地址中的密码
func maskedProxy(proxy string) string {
	u, err := url.Parse(proxy)
	if err != nil || u.User == nil {
		return proxy
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), "******")
	}
	return u.String()
}
