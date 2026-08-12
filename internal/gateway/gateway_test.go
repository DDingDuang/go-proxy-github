package gateway

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"go-proxy-github-cn/internal/config"
)

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// startGateway 启动被测网关(复用生产代码路径)
func startGateway(t *testing.T, cfg *config.Config) *httptest.Server {
	t.Helper()
	gw, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	engine := gw.Engine()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			gw.HandleConnect(w, r)
			return
		}
		engine.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		srv.Close()
		gw.Close() // 释放 SQLite 等资源, 避免文件占用
	})
	return srv
}

// newMockUpstreamProxy 模拟一个要求用户名密码认证的上游 HTTP 代理。
// 支持 CONNECT 隧道与普通请求转发; 返回代理服务器与认证头断言函数。
func newMockUpstreamProxy(t *testing.T, user, pass string) (*httptest.Server, func() string) {
	t.Helper()
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	var mu sync.Mutex
	gotAuth := ""

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth := r.Header.Get("Proxy-Authorization")
		if auth != "" {
			gotAuth = auth
		}
		mu.Unlock()

		if auth == "" {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		if auth != expected {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		if r.Method == http.MethodConnect {
			dst, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				dst.Close()
				return
			}
			conn, buf, err := hj.Hijack()
			if err != nil {
				dst.Close()
				return
			}
			defer conn.Close()
			defer dst.Close()
			if _, err := buf.WriteString("HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
				return
			}
			if err := buf.Flush(); err != nil {
				return
			}
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(conn, dst); done <- struct{}{} }()
			go func() { _, _ = io.Copy(dst, buf); done <- struct{}{} }()
			<-done
			return
		}

		// 普通请求: 转发到绝对 URI
		if !r.URL.IsAbs() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		outReq, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		outReq.Header = r.Header.Clone()
		outReq.Header.Del("Proxy-Authorization")
		resp, err := http.DefaultTransport.RoundTrip(outReq)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	getAuth := func() string {
		mu.Lock()
		defer mu.Unlock()
		return gotAuth
	}
	return srv, getAuth
}

func withAuthProxyURL(proxyURL, user, pass string) string {
	u := mustURL(proxyURL)
	u.User = url.UserPassword(user, pass)
	return u.String()
}

func baseConfig() *config.Config {
	cfg := config.Default()
	cfg.Server.Listen = "127.0.0.1:0"
	cfg.Server.DBPath = "" // 测试默认内存模式, 避免污染项目目录
	return cfg
}

// TestPathProxyViaAuthenticatedUpstream 路径模式反向代理 + 上游代理认证
func TestPathProxyViaAuthenticatedUpstream(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s", r.URL.Path)
	}))
	defer target.Close()

	proxy, getAuth := newMockUpstreamProxy(t, "proxyuser", "proxypass")

	cfg := baseConfig()
	cfg.Proxy.UpstreamProxy = withAuthProxyURL(proxy.URL, "proxyuser", "proxypass")
	cfg.Proxy.PathProxy = true
	cfg.Proxy.AllowedDomains = []string{"127.0.0.1"}

	gwSrv := startGateway(t, cfg)

	reqURL := gwSrv.URL + "/http://" + strings.TrimPrefix(target.URL, "http://") + "/hello"
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET %s: %v", reqURL, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if string(body) != "hello /hello" {
		t.Fatalf("unexpected body: %q", body)
	}

	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxyuser:proxypass"))
	if got := getAuth(); got != expectedAuth {
		t.Fatalf("Proxy-Authorization = %q, want %q", got, expectedAuth)
	}
}

// TestConnectTunnelViaAuthenticatedUpstream CONNECT 隧道 + 上游代理认证
func TestConnectTunnelViaAuthenticatedUpstream(t *testing.T) {
	tlsTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "secure-ok")
	}))
	defer tlsTarget.Close()

	proxy, getAuth := newMockUpstreamProxy(t, "u", "p")

	cfg := baseConfig()
	cfg.Proxy.UpstreamProxy = withAuthProxyURL(proxy.URL, "u", "p")
	cfg.Proxy.AllowedDomains = []string{"127.0.0.1"}

	gwSrv := startGateway(t, cfg)

	// 客户端把网关当作 HTTP 代理, 访问 https 目标
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(mustURL(gwSrv.URL)),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // 测试用自签名证书
		},
	}
	resp, err := client.Get(tlsTarget.URL + "/secure")
	if err != nil {
		t.Fatalf("GET via tunnel: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "secure-ok" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}

	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p"))
	if got := getAuth(); got != expectedAuth {
		t.Fatalf("Proxy-Authorization = %q, want %q", got, expectedAuth)
	}
}

// TestHostBasedProxy 域名直连模式(Host 头指向网关)
func TestHostBasedProxy(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "host-mode %s", r.URL.Path)
	}))
	defer target.Close()

	proxy, _ := newMockUpstreamProxy(t, "u", "p")

	cfg := baseConfig()
	cfg.Proxy.UpstreamProxy = withAuthProxyURL(proxy.URL, "u", "p")
	cfg.Proxy.AllowedDomains = []string{"127.0.0.1"}
	cfg.Proxy.InsecureSkipVerify = true // 测试目标为自签名证书

	gwSrv := startGateway(t, cfg)

	req, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/hello", nil)
	req.Host = strings.TrimPrefix(target.URL, "https://") // 模拟 DNS/hosts 把域名指向网关
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "host-mode /hello" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

// TestBlockNonAllowedHost 白名单外域名(路径模式)应被拒绝
func TestBlockNonAllowedHost(t *testing.T) {
	proxy, _ := newMockUpstreamProxy(t, "u", "p")

	cfg := baseConfig()
	cfg.Proxy.UpstreamProxy = withAuthProxyURL(proxy.URL, "u", "p")
	cfg.Proxy.PathProxy = true
	cfg.Proxy.AllowedDomains = []string{"github.com"}

	gwSrv := startGateway(t, cfg)

	resp, err := http.Get(gwSrv.URL + "/https://evil.example.com/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s, want 403", resp.StatusCode, body)
	}
}

// TestBlockConnectNonAllowedHost 白名单外域名(CONNECT 隧道)应被拒绝
func TestBlockConnectNonAllowedHost(t *testing.T) {
	proxy, _ := newMockUpstreamProxy(t, "u", "p")

	cfg := baseConfig()
	cfg.Proxy.UpstreamProxy = withAuthProxyURL(proxy.URL, "u", "p")
	cfg.Proxy.AllowedDomains = []string{"github.com"}

	gwSrv := startGateway(t, cfg)

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustURL(gwSrv.URL)),
		},
	}
	if _, err := client.Get("https://evil.example.com/"); err == nil {
		t.Fatal("期望 CONNECT 白名单外目标报错, 实际成功")
	}
}

// TestStatsAPI 管理面板与统计接口
func TestStatsAPI(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s", r.URL.Path)
	}))
	defer target.Close()

	proxy, _ := newMockUpstreamProxy(t, "u", "p")

	cfg := baseConfig()
	cfg.Proxy.UpstreamProxy = withAuthProxyURL(proxy.URL, "u", "p")
	cfg.Proxy.PathProxy = true
	cfg.Proxy.AllowedDomains = []string{"127.0.0.1"}

	gwSrv := startGateway(t, cfg)

	// 制造一次真实请求
	reqURL := gwSrv.URL + "/http://" + strings.TrimPrefix(target.URL, "http://") + "/hello"
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET %s: %v", reqURL, err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	// 首页应返回管理面板 HTML
	pageResp, err := http.Get(gwSrv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	page, _ := io.ReadAll(pageResp.Body)
	pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK || !strings.Contains(string(page), "GitHub 加速网关") {
		t.Fatalf("管理面板内容异常: status=%d", pageResp.StatusCode)
	}

	// 统计接口应包含请求数与域名排行
	d := struct {
		Version string `json:"version"`
		Stats   struct {
			Requests int64 `json:"requests"`
			Hosts    []struct {
				Host string `json:"host"`
			} `json:"hosts"`
		} `json:"stats"`
	}{}
	statsResp, err := http.Get(gwSrv.URL + "/api/stats")
	if err != nil {
		t.Fatalf("GET /api/stats: %v", err)
	}
	body, _ := io.ReadAll(statsResp.Body)
	statsResp.Body.Close()
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("解析 /api/stats 失败: %v, body=%s", err, body)
	}
	if d.Stats.Requests < 1 {
		t.Fatalf("stats.requests = %d, want >= 1", d.Stats.Requests)
	}
	targetHost := strings.TrimPrefix(target.URL, "http://")
	found := false
	for _, h := range d.Stats.Hosts {
		if h.Host == targetHost {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("域名排行中未找到 %s, hosts=%+v", targetHost, d.Stats.Hosts)
	}
}

// TestParseGitHubURL 项目地址解析
func TestParseGitHubURL(t *testing.T) {
	cases := []struct {
		in     string
		owner  string
		repo   string
		wantOK bool
	}{
		{"https://github.com/octocat/Hello-World", "octocat", "Hello-World", true},
		{"https://github.com/octocat/Hello-World.git", "octocat", "Hello-World", true},
		{"https://github.com/octocat/Hello-World/tree/main", "octocat", "Hello-World", true},
		{"http://github.com/octocat/Hello-World", "octocat", "Hello-World", true},
		{"git@github.com:octocat/Hello-World.git", "octocat", "Hello-World", true},
		{"ssh://git@github.com/octocat/Hello-World.git", "octocat", "Hello-World", true},
		{"github.com/octocat/Hello-World", "octocat", "Hello-World", true},
		{"https://github.com/octocat/Hello-World/", "octocat", "Hello-World", true},
		{"https://github.com/features", "", "", false},
		{"https://gitlab.com/foo/bar", "", "", false},
		{"", "", "", false},
		{"not a url", "", "", false},
	}
	for _, c := range cases {
		owner, repo, ok := parseGitHubURL(c.in)
		if ok != c.wantOK || owner != c.owner || repo != c.repo {
			t.Errorf("parseGitHubURL(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, owner, repo, ok, c.owner, c.repo, c.wantOK)
		}
	}
}

// TestLogBuffer 日志缓冲顺序与裁剪
func TestLogBuffer(t *testing.T) {
	b := NewLogBuffer(5)
	for i := 0; i < 8; i++ {
		b.Add(LogEntry{Path: fmt.Sprintf("/p%d", i), Status: 200})
	}
	list := b.List(10)
	if len(list) != 5 {
		t.Fatalf("List 长度 = %d, want 5", len(list))
	}
	if list[0].Path != "/p7" || list[4].Path != "/p3" {
		t.Fatalf("顺序错误: %+v", list)
	}
	if got := len(b.List(2)); got != 2 {
		t.Fatalf("limit 未生效: %d", got)
	}
}

// TestLogsAPI 访问日志接口
func TestLogsAPI(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer target.Close()

	proxy, _ := newMockUpstreamProxy(t, "u", "p")

	cfg := baseConfig()
	cfg.Proxy.UpstreamProxy = withAuthProxyURL(proxy.URL, "u", "p")
	cfg.Proxy.PathProxy = true
	cfg.Proxy.AllowedDomains = []string{"127.0.0.1"}

	gwSrv := startGateway(t, cfg)

	// 制造一次代理请求
	resp, err := http.Get(gwSrv.URL + "/http://" + strings.TrimPrefix(target.URL, "http://") + "/hello")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	// 日志应包含: 项目地址 + 状态码
	resp2, err := http.Get(gwSrv.URL + "/api/logs")
	if err != nil {
		t.Fatalf("GET /api/logs: %v", err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	var d struct {
		Logs []struct {
			Project string `json:"project"`
			Status  int    `json:"status"`
			IP      string `json:"ip"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("解析 /api/logs 失败: %v", err)
	}
	found := false
	for _, l := range d.Logs {
		if l.Status == 200 && l.Project != "" && l.IP != "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("日志未包含代理请求记录: %s", body)
	}
}

// TestCheckStore SQLite 持久化 + 分页
func TestCheckStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "checks.db")
	store, err := OpenCheckStore(dbPath)
	if err != nil {
		t.Fatalf("OpenCheckStore: %v", err)
	}
	defer store.Close()

	// 写入 25 条记录
	for i := 0; i < 25; i++ {
		e := LogEntry{
			Time:      time.Now().Add(-time.Duration(i) * time.Second),
			IP:        fmt.Sprintf("192.168.0.%d", i%10+1),
			Project:   fmt.Sprintf("owner/repo-%02d", i),
			Method:    http.MethodPost,
			Status:    200,
			LatencyMS: int64(100 + i),
			Kind:      "repo_check",
		}
		if err := store.Insert(e); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	// 第 1 页: 20 条, 最新在前(倒序)
	page1, total, err := store.List(1, 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 25 {
		t.Fatalf("total = %d, want 25", total)
	}
	if len(page1) != 20 {
		t.Fatalf("page1 len = %d, want 20", len(page1))
	}
	if page1[0].Project != "owner/repo-24" {
		t.Fatalf("第 1 条应为最新的 repo-24, got %s", page1[0].Project)
	}

	// 第 2 页: 5 条
	page2, _, err := store.List(2, 20)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2) != 5 {
		t.Fatalf("page2 len = %d, want 5", len(page2))
	}
	if page2[0].Project != "owner/repo-04" {
		t.Fatalf("第 2 页第 1 条应为 repo-04, got %s", page2[0].Project)
	}

	// 重新打开数据库, 验证持久化
	store.Close()
	store2, err := OpenCheckStore(dbPath)
	if err != nil {
		t.Fatalf("重新打开: %v", err)
	}
	defer store2.Close()
	entries, total2, err := store2.List(1, 100)
	if err != nil {
		t.Fatalf("重新打开后 List: %v", err)
	}
	if total2 != 25 || len(entries) != 25 {
		t.Fatalf("持久化后 total=%d len=%d, want 25/25", total2, len(entries))
	}
}

// TestLogsAPIPagination SQLite 模式下日志分页接口
func TestLogsAPIPagination(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer target.Close()

	proxy, _ := newMockUpstreamProxy(t, "u", "p")

	cfg := baseConfig()
	cfg.Proxy.UpstreamProxy = withAuthProxyURL(proxy.URL, "u", "p")
	cfg.Proxy.PathProxy = true
	cfg.Proxy.AllowedDomains = []string{"127.0.0.1"}
	cfg.Server.DBPath = filepath.Join(t.TempDir(), "checks.db") // 启用 SQLite

	gwSrv := startGateway(t, cfg)

	// 制造 3 次检查(其中 1 次为非法输入)
	for _, url := range []string{
		"https://github.com/octocat/Hello-World",
		"https://github.com/octocat/Hello-World",
		"not-a-url",
	} {
		body := fmt.Sprintf(`{"url":"%s"}`, url)
		resp, err := http.Post(gwSrv.URL+"/api/repo/check", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST repo/check: %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	// 分页查询
	resp, err := http.Get(gwSrv.URL + "/api/logs?kind=repo_check&page=1&page_size=2")
	if err != nil {
		t.Fatalf("GET logs: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var d struct {
		Logs     []LogEntry `json:"logs"`
		Total    int64      `json:"total"`
		Page     int        `json:"page"`
		PageSize int        `json:"page_size"`
		Pages    int        `json:"pages"`
		Source   string     `json:"source"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("解析失败: %v, body=%s", err, body)
	}
	if d.Source != "sqlite" {
		t.Fatalf("source = %s, want sqlite", d.Source)
	}
	if d.Total != 3 || d.Pages != 2 || len(d.Logs) != 2 {
		t.Fatalf("total=%d pages=%d len=%d, want 3/2/2", d.Total, d.Pages, len(d.Logs))
	}
	if d.Logs[0].Project == "" {
		t.Fatalf("日志缺少项目地址: %+v", d.Logs[0])
	}

	// 第 2 页: 1 条
	resp2, err := http.Get(gwSrv.URL + "/api/logs?kind=repo_check&page=2&page_size=2")
	if err != nil {
		t.Fatalf("GET logs page2: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	var d2 struct {
		Logs []LogEntry `json:"logs"`
	}
	if err := json.Unmarshal(body2, &d2); err != nil {
		t.Fatalf("解析 page2 失败: %v", err)
	}
	if len(d2.Logs) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(d2.Logs))
	}
}

// TestProxyRecheck 手动触发代理检测接口
func TestProxyRecheck(t *testing.T) {
	proxy, _ := newMockUpstreamProxy(t, "u", "p")

	cfg := baseConfig()
	cfg.Proxy.UpstreamProxy = withAuthProxyURL(proxy.URL, "u", "p")
	cfg.Proxy.AllowedDomains = []string{"127.0.0.1"}

	gwSrv := startGateway(t, cfg)

	resp, err := http.Post(gwSrv.URL+"/api/proxy/check", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/proxy/check: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var d struct {
		ProxyStatus struct {
			OK        bool   `json:"ok"`
			LatencyMS int64  `json:"latency_ms"`
			CheckedAt string `json:"checked_at"`
		} `json:"proxy_status"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("解析失败: %v, body=%s", err, body)
	}
	if !d.ProxyStatus.OK {
		t.Fatalf("代理检测应为可用, got %s", body)
	}
	if d.ProxyStatus.CheckedAt == "" {
		t.Fatalf("缺少检测时间: %s", body)
	}
}

// TestAbsoluteFormProxy HTTP 代理模式的绝对 URI 请求
func TestAbsoluteFormProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "abs %s", r.URL.Path)
	}))
	defer target.Close()

	proxy, _ := newMockUpstreamProxy(t, "u", "p")

	cfg := baseConfig()
	cfg.Proxy.UpstreamProxy = withAuthProxyURL(proxy.URL, "u", "p")
	cfg.Proxy.AllowedDomains = []string{"127.0.0.1"}

	gwSrv := startGateway(t, cfg)

	// 模拟客户端把网关配成 HTTP 代理后, 浏览器发送的绝对 URI 请求
	req, _ := http.NewRequest(http.MethodGet, "http://"+strings.TrimPrefix(target.URL, "http://")+"/abs", nil)
	req.URL.Scheme = "http"
	proxyReq, _ := http.NewRequest(http.MethodGet, gwSrv.URL, nil)
	proxyReq.URL = req.URL // 直接以绝对 URI 形式请求网关

	resp, err := http.DefaultTransport.RoundTrip(proxyReq)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "abs /abs" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}
