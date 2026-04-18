package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/zightch/frp/frps/internal/config"
)

func New(cfg config.LogConfig) (*slog.Logger, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	options := &slog.HandlerOptions{Level: level}
	var handler slog.Handler

	switch strings.ToLower(cfg.Format) {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, options)
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, options)
	default:
		return nil, fmt.Errorf("unsupported log format %q", cfg.Format)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger, nil
}

func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", level)
	}
}
