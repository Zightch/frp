package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"github.com/zightch/frp/frpc/internal/client"
	"github.com/zightch/frp/frpc/internal/config"
)

var version = "dev"

func main() {
	var (
		server       string
		clientID     string
		clientSecret string
		showVersion  bool
	)

	flag.StringVar(&server, "server", "", "frps control server address, for example 1.2.3.4:7000")
	flag.StringVar(&clientID, "client-id", "", "proxy group client ID")
	flag.StringVar(&clientSecret, "client-secret", "", "proxy group client secret")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println(version)
		return
	}

	cfg := config.Config{
		Server:       server,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "validate config: %v\n", err)
		os.Exit(1)
	}

	logger := newLogger()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	app := client.New(cfg, logger, version)
	if err := app.Run(ctx); err != nil {
		logger.Error("frpc exited with error", "error", err)
		os.Exit(1)
	}
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FRPC_LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
