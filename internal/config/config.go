package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 支持 yaml 中 "30s"/"1m" 等字符串形式的时长类型
type Duration time.Duration

// UnmarshalYAML 实现 yaml.v3 的时长解析
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// D 返回标准 time.Duration
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config 网关总配置
type Config struct {
	Server ServerConfig `yaml:"server"`
	Proxy  ProxyConfig  `yaml:"proxy"`
	Log    LogConfig    `yaml:"log"`
}

// ServerConfig 网关监听配置
type ServerConfig struct {
	// Listen 网关监听地址, 如 "0.0.0.0:8080"
	Listen string `yaml:"listen"`
	// ReadHeaderTimeout 读取请求头超时
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	// DBPath Git 项目检查记录存储路径(SQLite), 留空则仅存内存(重启丢失)
	DBPath string `yaml:"db_path"`
}

// ProxyConfig 代理配置
type ProxyConfig struct {
	// UpstreamProxy 上游 HTTP 代理地址(带用户名密码),
	// 如 "http://user:password@proxy.example.com:8080"
	UpstreamProxy string `yaml:"upstream_proxy"`
	// AllowedDomains 允许加速的 GitHub 域名白名单(后缀匹配,
	// 填 github.com 会自动匹配 api.github.com、raw.githubusercontent.com 等)
	AllowedDomains []string `yaml:"allowed_domains"`
	// PathProxy 是否启用路径模式代理, 如 http://网关:8080/https://github.com/xxx
	PathProxy bool `yaml:"path_proxy"`
	// ConnectAllowAny 是否允许 CONNECT 隧道任意域名
	// (默认 false: 仅白名单域名, 防止被当作开放代理滥用)
	ConnectAllowAny bool `yaml:"connect_allow_any"`
	// InsecureSkipVerify 是否跳过上游 TLS 证书校验(仅调试用)
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`

	// DialTimeout 与上游代理建立 TCP 连接超时
	DialTimeout Duration `yaml:"dial_timeout"`
	// TLSTimeout 上游 TLS 握手超时
	TLSTimeout Duration `yaml:"tls_timeout"`
	// ResponseTimeout 等待上游响应头超时
	ResponseTimeout Duration `yaml:"response_timeout"`
	// IdleConnTimeout 空闲连接保留时间
	IdleConnTimeout Duration `yaml:"idle_conn_timeout"`
}

// LogConfig 日志配置
type LogConfig struct {
	// Level 日志级别: debug | info | warn | error
	Level string `yaml:"level"`
	// Output 输出目标: stdout 或日志文件路径
	Output string `yaml:"output"`
}

// DefaultDomains 默认允许加速的 GitHub 相关域名
func DefaultDomains() []string {
	return []string{
		"github.com",
		"githubusercontent.com",
		"github.io",
		"githubassets.com",
		"githubapp.com",
		"github.dev",
		"githubcopilot.com",
		"github.blog",
		"githubstatus.com",
		"ghcr.io",
		"ssh.github.com",
	}
}

// Default 返回默认配置
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Listen:            "0.0.0.0:8080",
			ReadHeaderTimeout: Duration(10 * time.Second),
			DBPath:            "data/checks.db",
		},
		Proxy: ProxyConfig{
			UpstreamProxy:   "",
			AllowedDomains:  DefaultDomains(),
			PathProxy:       true,
			ConnectAllowAny: false,
			DialTimeout:     Duration(10 * time.Second),
			TLSTimeout:      Duration(10 * time.Second),
			ResponseTimeout: Duration(60 * time.Second),
			IdleConnTimeout: Duration(90 * time.Second),
		},
		Log: LogConfig{Level: "info", Output: "stdout"},
	}
}

// Load 从文件加载配置, 支持环境变量覆盖
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}

	// 环境变量覆盖(便于容器化部署)
	if v := os.Getenv("GATEWAY_LISTEN"); v != "" {
		cfg.Server.Listen = v
	}
	if v := os.Getenv("GATEWAY_UPSTREAM_PROXY"); v != "" {
		cfg.Proxy.UpstreamProxy = v
	}
	if v := os.Getenv("GATEWAY_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate 校验配置
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Server.Listen) == "" {
		return errors.New("server.listen 不能为空")
	}
	if strings.TrimSpace(c.Proxy.UpstreamProxy) == "" {
		return errors.New("proxy.upstream_proxy 不能为空, 例如 http://user:pass@proxy.example.com:8080")
	}
	u, err := url.Parse(c.Proxy.UpstreamProxy)
	if err != nil {
		return fmt.Errorf("proxy.upstream_proxy 解析失败: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("proxy.upstream_proxy 仅支持 http/https 协议")
	}
	if u.Host == "" {
		return errors.New("proxy.upstream_proxy 缺少主机地址")
	}
	return nil
}
