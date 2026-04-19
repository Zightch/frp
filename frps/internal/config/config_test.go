package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frps.json")
	content := []byte(`{"control_listen_addr":"0.0.0.0:7000","unknown":true}`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestLoadAppliesCustomConfig(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "frps.json")
	content := []byte(`{
		"control_listen_addr":"127.0.0.1:7000",
		"management_listen_addr":"127.0.0.1:7500",
		"read_header_timeout":"3s",
		"shutdown_timeout":"8s",
		"database":{"type":"mysql","dsn":"user:pass@tcp(127.0.0.1:3306)/frps?parseTime=true"},
		"webui":{"dist_dir":"./webui-dist"},
		"log":{"level":"debug","format":"json"}
	}`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Log.Level != "debug" {
		t.Fatalf("unexpected log level: %s", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Fatalf("unexpected log format: %s", cfg.Log.Format)
	}
	if cfg.Database.Type != "mysql" {
		t.Fatalf("unexpected database type: %s", cfg.Database.Type)
	}
	if cfg.Database.DSN == "" {
		t.Fatal("expected mysql dsn to be loaded")
	}
	wantDistDir := filepath.Join(tempDir, "webui-dist")
	if cfg.WebUI.DistDir != wantDistDir {
		t.Fatalf("unexpected webui dist dir: got %q want %q", cfg.WebUI.DistDir, wantDistDir)
	}
}

func TestLoadRejectsInvalidDatabaseConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frps.json")
	content := []byte(`{
		"database":{"type":"mysql"}
	}`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid database config error")
	}
}

func TestLoadResolvesSQLitePathRelativeToConfigFile(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "frps.json")
	content := []byte(`{
		"database":{"type":"sqlite","path":"../data/frps.sqlite"},
		"webui":{"dist_dir":"../webui/dist"}
	}`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	wantDatabasePath := filepath.Clean(filepath.Join(tempDir, "..", "data", "frps.sqlite"))
	if cfg.Database.Path != wantDatabasePath {
		t.Fatalf("unexpected sqlite path: got %q want %q", cfg.Database.Path, wantDatabasePath)
	}

	wantDistDir := filepath.Clean(filepath.Join(tempDir, "..", "webui", "dist"))
	if cfg.WebUI.DistDir != wantDistDir {
		t.Fatalf("unexpected webui dist dir: got %q want %q", cfg.WebUI.DistDir, wantDistDir)
	}
}
