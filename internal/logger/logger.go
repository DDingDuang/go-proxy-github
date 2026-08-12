package logger

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"go-proxy-github-cn/internal/config"
)

// New 根据配置创建 zap 日志器(JSON 格式, 支持 stdout 或文件输出)
func New(cfg config.LogConfig) (*zap.Logger, error) {
	level := zapcore.InfoLevel
	switch strings.ToLower(strings.TrimSpace(cfg.Level)) {
	case "debug":
		level = zapcore.DebugLevel
	case "warn", "warning":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	case "info", "":
		level = zapcore.InfoLevel
	default:
		return nil, fmt.Errorf("无效的日志级别: %s", cfg.Level)
	}

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encCfg.EncodeLevel = zapcore.CapitalLevelEncoder
	encCfg.EncodeDuration = zapcore.StringDurationEncoder
	encoder := zapcore.NewJSONEncoder(encCfg)

	var ws zapcore.WriteSyncer
	switch strings.ToLower(strings.TrimSpace(cfg.Output)) {
	case "", "stdout":
		ws = zapcore.AddSync(os.Stdout)
	default:
		f, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("打开日志文件失败: %w", err)
		}
		ws = zapcore.AddSync(f)
	}

	core := zapcore.NewCore(encoder, ws, level)
	return zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel)), nil
}
