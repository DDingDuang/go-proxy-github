package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"go.uber.org/zap"

	"go-proxy-github-cn/internal/config"
	"go-proxy-github-cn/internal/gateway"
	"go-proxy-github-cn/internal/logger"
)

// proxyHandler 统一入口: CONNECT 隧道请求直接处理, 其余交给 gin 引擎
type proxyHandler struct {
	engine http.Handler
	gw     *gateway.Gateway
}

func (h proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		h.gw.HandleConnect(w, r)
		return
	}
	h.engine.ServeHTTP(w, r)
}

func main() {
	var cfgPath string
	var showVersion bool
	flag.StringVar(&cfgPath, "config", "config.yaml", "配置文件路径")
	flag.BoolVar(&showVersion, "version", false, "打印版本与平台信息后退出")
	flag.Parse()

	// -version: 供 run.sh 探测二进制与当前平台是否匹配
	if showVersion {
		fmt.Printf("github-gateway %s (%s/%s)\n", gateway.Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置加载失败:", err)
		os.Exit(1)
	}

	zlog, err := logger.New(cfg.Log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "日志初始化失败:", err)
		os.Exit(1)
	}
	defer func() { _ = zlog.Sync() }()

	gw, err := gateway.New(cfg, zlog)
	if err != nil {
		zlog.Fatal("网关初始化失败", zap.Error(err))
	}

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           proxyHandler{engine: gw.Engine(), gw: gw},
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.D(),
		// 注意: 隧道(长连接)场景下不设置 Read/WriteTimeout,
		// 否则 git clone 等长连接会被服务器强制断开
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	zlog.Info("GitHub 加速网关已启动",
		zap.String("version", gateway.Version),
		zap.String("listen", cfg.Server.Listen),
		zap.String("upstream_proxy", cfg.Proxy.UpstreamProxy),
		zap.Bool("path_proxy", cfg.Proxy.PathProxy),
		zap.Bool("connect_allow_any", cfg.Proxy.ConnectAllowAny),
		zap.Int("allowed_domains", len(cfg.Proxy.AllowedDomains)),
	)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			zlog.Error("服务异常退出", zap.Error(err))
			os.Exit(1)
		}
	case sig := <-quit:
		zlog.Info("收到退出信号, 正在关闭", zap.String("signal", sig.String()))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			zlog.Warn("关闭服务出错", zap.Error(err))
		}
		zlog.Info("服务已退出")
	}
}
