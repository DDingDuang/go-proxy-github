package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// repoDetail GitHub API 返回的仓库信息
type repoDetail struct {
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Stars         int    `json:"stargazers_count"`
	Forks         int    `json:"forks_count"`
	Language      string `json:"language"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// errRepoNotFound 仓库不存在
var errRepoNotFound = errors.New("repository not found")

// logRepoCheck 记录一次 Git 项目检查日志(带被检查的项目地址)
// 优先写入 SQLite(持久化), 未启用时回退内存缓冲
func (g *Gateway) logRepoCheck(c *gin.Context, project string, status int, latency time.Duration) {
	e := LogEntry{
		Time:      time.Now(),
		IP:        c.ClientIP(),
		Project:   project,
		Method:    http.MethodPost,
		Path:      "/api/repo/check",
		Status:    status,
		LatencyMS: latency.Milliseconds(),
		Kind:      "repo_check",
	}
	if g.store != nil {
		if err := g.store.Insert(e); err != nil {
			g.log.Warn("写入检查记录失败", zap.String("project", project), zap.Error(err))
		}
		return
	}
	g.logs.Add(e)
}

// repoCheckRequest 项目检查请求
type repoCheckRequest struct {
	URL string `json:"url"`
}

// handleRepoCheck 检查 GitHub 项目是否存在, 并返回 clone 命令。
// 优先查 GitHub API(可返回星标等详情), 遇速率限制等错误时降级为网页检查。
func (g *Gateway) handleRepoCheck(c *gin.Context) {
	start := time.Now()
	var req repoCheckRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		g.logRepoCheck(c, strings.TrimSpace(req.URL), http.StatusBadRequest, time.Since(start))
		c.JSON(http.StatusBadRequest, gin.H{"exists": nil, "error": "请输入 GitHub 项目地址"})
		return
	}

	owner, repo, ok := parseGitHubURL(req.URL)
	if !ok {
		g.logRepoCheck(c, strings.TrimSpace(req.URL), http.StatusBadRequest, time.Since(start))
		c.JSON(http.StatusBadRequest, gin.H{"exists": nil, "error": "无法解析 GitHub 项目地址, 示例: https://github.com/owner/repo"})
		return
	}

	// 1) GitHub API 查询
	detail, err := g.queryRepoAPI(owner, repo)
	if err == nil {
		g.logRepoCheck(c, owner+"/"+repo, http.StatusOK, time.Since(start))
		c.JSON(http.StatusOK, gin.H{
			"exists":         true,
			"full_name":      detail.FullName,
			"description":    detail.Description,
			"stars":          detail.Stars,
			"forks":          detail.Forks,
			"language":       detail.Language,
			"default_branch": detail.DefaultBranch,
			"private":        detail.Private,
			"clone_commands": g.cloneCommands(owner, repo),
		})
		return
	}
	if errors.Is(err, errRepoNotFound) {
		g.logRepoCheck(c, owner+"/"+repo, http.StatusNotFound, time.Since(start))
		c.JSON(http.StatusOK, gin.H{"exists": false, "full_name": owner + "/" + repo})
		return
	}

	// 2) 降级: 网页检查(GitHub API 速率受限等)
	g.log.Warn("GitHub API 查询受限, 降级网页检查", zap.String("repo", owner+"/"+repo), zap.Error(err))
	exists, werr := g.checkRepoViaWeb(owner, repo)
	if werr != nil {
		g.logRepoCheck(c, owner+"/"+repo, http.StatusBadGateway, time.Since(start))
		c.JSON(http.StatusBadGateway, gin.H{"exists": nil, "error": werr.Error()})
		return
	}
	resp := gin.H{
		"exists":    exists,
		"full_name": owner + "/" + repo,
		"note":      "GitHub API 速率受限, 已通过网页检查确认",
	}
	if exists {
		resp["clone_commands"] = g.cloneCommands(owner, repo)
	}
	st := http.StatusOK
	if !exists {
		st = http.StatusNotFound
	}
	g.logRepoCheck(c, owner+"/"+repo, st, time.Since(start))
	c.JSON(http.StatusOK, resp)
}

// queryRepoAPI 经上游代理查询 GitHub API 仓库信息
func (g *Gateway) queryRepoAPI(owner, repo string) (*repoDetail, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "github-gateway/"+Version)

	resp, err := g.upstream.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var d repoDetail
		if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
			return nil, err
		}
		return &d, nil
	case http.StatusNotFound:
		return nil, errRepoNotFound
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// checkRepoViaWeb 通过 github.com 网页检查仓库是否存在(200=存在, 404=不存在)
func (g *Gateway) checkRepoViaWeb(owner, repo string) (bool, error) {
	req, err := http.NewRequest(http.MethodGet, "https://github.com/"+owner+"/"+repo, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "github-gateway/"+Version)

	resp, err := g.upstream.RoundTrip(req)
	if err != nil {
		return false, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	_ = resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("网页检查失败: HTTP %d", resp.StatusCode)
	}
}

// parseGitHubURL 从各种形式的输入中解析出 owner/repo
func parseGitHubURL(raw string) (owner, repo string, ok bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")

	// ssh 形式: git@github.com:owner/repo 或 ssh://git@github.com/owner/repo
	if i := strings.Index(s, "github.com:"); i >= 0 {
		s = "github.com/" + s[i+len("github.com:"):]
	}
	for _, p := range []string{"https://", "http://", "ssh://git@", "git@", "ssh://"} {
		s = strings.TrimPrefix(s, p)
	}
	if !strings.HasPrefix(s, "github.com/") {
		return "", "", false
	}
	s = strings.TrimPrefix(s, "github.com/")
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	// 排除非仓库路径(如 /features、/search、/orgs/xxx 等)
	switch parts[0] {
	case "features", "search", "topics", "collections", "marketplace", "sponsors", "login", "signup", "orgs", "settings":
		return "", "", false
	}
	return parts[0], parts[1], true
}

// cloneCommands 生成通过网关克隆项目的命令(代理模式 + 路径模式)
func (g *Gateway) cloneCommands(owner, repo string) []string {
	addr := net.JoinHostPort(preferredIP(), listenPort(g.cfg.Server.Listen))
	gitURL := "https://github.com/" + owner + "/" + repo + ".git"
	return []string{
		// 方式一: git 走 HTTP 代理(最通用, 支持所有 GitHub 域名)
		fmt.Sprintf("git -c http.https://github.com.proxy=http://%s clone %s", addr, gitURL),
		// 方式二: 路径模式(不设代理, 网关直连回源)
		fmt.Sprintf("git clone http://%s/https://github.com/%s/%s.git", addr, owner, repo),
	}
}

// preferredIP 选择用于生成 clone 命令的 IP:
// 优先局域网网段(192.168.x / 10.x / 172.16-31.x), 跳过 Docker 等虚拟网卡常见的
// 172.17/172.18/172.19 段, 最后回退到第一个非回环地址或 127.0.0.1
func preferredIP() string {
	var fallback string
	for _, v := range localIPv4s() {
		ip := net.ParseIP(v).To4() // 注意: ParseIP 对 IPv4 返回 16 字节形式, 需 To4()
		if ip == nil || v == "127.0.0.1" {
			continue
		}
		if fallback == "" {
			fallback = v
		}
		switch {
		case ip[0] == 192 && ip[1] == 168:
			return v
		case ip[0] == 10:
			return v
		case ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 && ip[1] != 17 && ip[1] != 18 && ip[1] != 19:
			return v
		}
	}
	if fallback != "" {
		return fallback
	}
	return "127.0.0.1"
}

// localIPv4s 枚举本机非回环 IPv4 地址(用于生成局域网可访问的命令)
func localIPv4s() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []string
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				ips = append(ips, ip4.String())
			}
		}
	}
	return ips
}

// listenPort 从监听地址提取端口
func listenPort(listen string) string {
	if _, port, err := net.SplitHostPort(listen); err == nil {
		return port
	}
	return "8080"
}

// extractProject 从代理目标 URL 中提取"项目地址"用于日志展示,
// 如 https://github.com/owner/repo.git/info/refs → github.com/owner/repo
func extractProject(target *url.URL) string {
	host := target.Host
	segs := strings.Split(strings.Trim(target.Path, "/"), "/")
	if len(segs) >= 2 && segs[0] != "" && segs[1] != "" {
		return host + "/" + strings.TrimSuffix(segs[0], ".git") + "/" + strings.TrimSuffix(segs[1], ".git")
	}
	if target.Path != "" {
		return host + target.Path
	}
	return host
}
