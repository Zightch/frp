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
	path := filepath.Join(t.TempDir(), "frps.json")
	content := []byte(`{
		"control_listen_addr":"127.0.0.1:7000",
		"management_listen_addr":"127.0.0.1:7500",
		"read_header_timeout":"3s",
		"shutdown_timeout":"8s",
		"database":{"type":"mysql","dsn":"user:pass@tcp(127.0.0.1:3306)/frps?parseTime=true"},
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
