package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultControlListenAddr    = "0.0.0.0:7000"
	defaultManagementListenAddr = "127.0.0.1:7080"
	defaultReadHeaderTimeout    = "5s"
	defaultShutdownTimeout      = "10s"
	defaultLogLevel             = "info"
	defaultLogFormat            = "text"
	defaultDatabaseType         = "sqlite"
	defaultDatabasePath         = "./frps.db"
	defaultWebUIDistDir         = "../webui/dist"
)

type Config struct {
	ControlListenAddr    string         `json:"control_listen_addr"`
	ManagementListenAddr string         `json:"management_listen_addr"`
	ReadHeaderTimeout    string         `json:"read_header_timeout"`
	ShutdownTimeout      string         `json:"shutdown_timeout"`
	Database             DatabaseConfig `json:"database"`
	WebUI                WebUIConfig    `json:"webui"`
	Log                  LogConfig      `json:"log"`
}

type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

type DatabaseConfig struct {
	Type string `json:"type"`
	DSN  string `json:"dsn"`
	Path string `json:"path"`
}

type WebUIConfig struct {
	DistDir    string `json:"dist_dir"`
	PathPrefix string `json:"path_prefix"`
}

func Default() Config {
	return Config{
		ControlListenAddr:    defaultControlListenAddr,
		ManagementListenAddr: defaultManagementListenAddr,
		ReadHeaderTimeout:    defaultReadHeaderTimeout,
		ShutdownTimeout:      defaultShutdownTimeout,
		Database: DatabaseConfig{
			Type: defaultDatabaseType,
			Path: defaultDatabasePath,
		},
		WebUI: WebUIConfig{
			DistDir: defaultWebUIDistDir,
		},
		Log: LogConfig{
			Level:  defaultLogLevel,
			Format: defaultLogFormat,
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, cfg.Validate()
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve config file path: %w", err)
	}

	file, err := os.Open(absPath)
	if err != nil {
		return Config{}, fmt.Errorf("open config file: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config file: %w", err)
	}

	cfg.resolveRelativePaths(filepath.Dir(absPath))

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	c.ControlListenAddr = strings.TrimSpace(c.ControlListenAddr)
	c.ManagementListenAddr = strings.TrimSpace(c.ManagementListenAddr)
	c.ReadHeaderTimeout = strings.TrimSpace(c.ReadHeaderTimeout)
	c.ShutdownTimeout = strings.TrimSpace(c.ShutdownTimeout)
	c.Database.Type = strings.ToLower(strings.TrimSpace(c.Database.Type))
	c.Database.DSN = strings.TrimSpace(c.Database.DSN)
	c.Database.Path = strings.TrimSpace(c.Database.Path)
	c.WebUI.DistDir = strings.TrimSpace(c.WebUI.DistDir)
	c.WebUI.PathPrefix = strings.TrimSpace(c.WebUI.PathPrefix)
	c.Log.Level = strings.ToLower(strings.TrimSpace(c.Log.Level))
	c.Log.Format = strings.ToLower(strings.TrimSpace(c.Log.Format))

	if c.ControlListenAddr == "" {
		return fmt.Errorf("control_listen_addr is required")
	}
	if c.ManagementListenAddr == "" {
		return fmt.Errorf("management_listen_addr is required")
	}
	if err := validateListenAddr(c.ControlListenAddr); err != nil {
		return fmt.Errorf("control_listen_addr: %w", err)
	}
	if err := validateListenAddr(c.ManagementListenAddr); err != nil {
		return fmt.Errorf("management_listen_addr: %w", err)
	}
	if _, err := time.ParseDuration(c.ReadHeaderTimeout); err != nil {
		return fmt.Errorf("read_header_timeout: %w", err)
	}
	if _, err := time.ParseDuration(c.ShutdownTimeout); err != nil {
		return fmt.Errorf("shutdown_timeout: %w", err)
	}
	if err := c.Database.Validate(); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	if c.WebUI.DistDir == "" {
		return fmt.Errorf("webui.dist_dir is required")
	}
	pathPrefix, err := NormalizeWebUIPathPrefix(c.WebUI.PathPrefix)
	if err != nil {
		return fmt.Errorf("webui.path_prefix: %w", err)
	}
	c.WebUI.PathPrefix = pathPrefix
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level must be one of debug, info, warn, error")
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("log.format must be one of text, json")
	}

	return nil
}

func (c *Config) resolveRelativePaths(baseDir string) {
	if strings.ToLower(strings.TrimSpace(c.Database.Type)) != "mysql" {
		c.Database.Path = resolveRelativePath(baseDir, c.Database.Path)
	}
	c.WebUI.DistDir = resolveRelativePath(baseDir, c.WebUI.DistDir)
}

func resolveRelativePath(baseDir, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(baseDir, value))
}

func NormalizeWebUIPathPrefix(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return "", nil
	}
	if strings.Contains(value, `\`) {
		return "", fmt.Errorf("must use URL path separators '/'")
	}
	if strings.ContainsAny(value, "?#") {
		return "", fmt.Errorf("must not contain query or fragment")
	}

	normalized := value
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	normalized = path.Clean(normalized)
	if normalized == "." || normalized == "/" {
		return "", nil
	}
	if strings.HasPrefix(normalized, "/../") || normalized == "/.." {
		return "", fmt.Errorf("must stay within the URL root")
	}
	if strings.Contains(normalized, "//") {
		return "", fmt.Errorf("must not contain empty path segments")
	}
	switch {
	case normalized == "/api" || strings.HasPrefix(normalized, "/api/"):
		return "", fmt.Errorf("must not overlap reserved management api paths")
	case normalized == "/healthz" || strings.HasPrefix(normalized, "/healthz/"):
		return "", fmt.Errorf("must not overlap reserved health check paths")
	case normalized == "/readyz" || strings.HasPrefix(normalized, "/readyz/"):
		return "", fmt.Errorf("must not overlap reserved health check paths")
	}
	return normalized, nil
}

func (c Config) ReadHeaderTimeoutDuration() time.Duration {
	duration, _ := time.ParseDuration(c.ReadHeaderTimeout)
	return duration
}

func (c Config) ShutdownTimeoutDuration() time.Duration {
	duration, _ := time.ParseDuration(c.ShutdownTimeout)
	return duration
}

func (c *DatabaseConfig) Validate() error {
	switch c.Type {
	case "sqlite":
		if c.Path == "" && c.DSN == "" {
			return fmt.Errorf("type sqlite requires path or dsn")
		}
		if c.Path != "" && c.DSN != "" {
			return fmt.Errorf("type sqlite cannot set both path and dsn")
		}
	case "mysql":
		if c.DSN == "" {
			return fmt.Errorf("type mysql requires dsn")
		}
		if c.Path != "" && c.Path != defaultDatabasePath {
			return fmt.Errorf("type mysql cannot set path")
		}
		c.Path = ""
	case "":
		return fmt.Errorf("type is required")
	default:
		return fmt.Errorf("type must be one of sqlite, mysql")
	}

	return nil
}

func validateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("missing port")
	}
	if host == "" {
		return fmt.Errorf("missing host")
	}
	return nil
}
