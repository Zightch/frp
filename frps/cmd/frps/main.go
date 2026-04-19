package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/zightch/frp/frps/internal/app"
	"github.com/zightch/frp/frps/internal/config"
	"github.com/zightch/frp/frps/internal/logging"
)

var version = "dev"

func main() {
	configPath, err := resolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve config path: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config %s: %v\n", configPath, err)
		os.Exit(1)
	}

	logger, err := logging.New(cfg.Log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create logger: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	application := app.New(cfg, logger, version)
	if err := application.Run(ctx); err != nil {
		logger.Error("frps exited with error", "error", err)
		os.Exit(1)
	}
}

func resolveConfigPath() (string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return configPathForWorkingDir(workingDir), nil
}

func configPathForWorkingDir(workingDir string) string {
	return filepath.Join(workingDir, "data", "config.json")
}
