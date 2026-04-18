package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/zightch/frp/frps/internal/app"
	"github.com/zightch/frp/frps/internal/config"
	"github.com/zightch/frp/frps/internal/logging"
)

var version = "dev"

func main() {
	var (
		configPath  string
		showVersion bool
	)

	flag.StringVar(&configPath, "config", "", "path to frps JSON config file")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
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
