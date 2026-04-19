package main

import (
	"path/filepath"
	"testing"
)

func TestConfigPathForExecutable(t *testing.T) {
	t.Parallel()

	executablePath := filepath.Join("C:\\", "runtime", "frps", "frps.exe")
	want := filepath.Join("C:\\", "runtime", "frps", "data", "config.json")

	if got := configPathForExecutable(executablePath); got != want {
		t.Fatalf("unexpected config path: got %q want %q", got, want)
	}
}
