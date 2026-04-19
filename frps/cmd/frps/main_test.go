package main

import (
	"path/filepath"
	"testing"
)

func TestConfigPathForWorkingDir(t *testing.T) {
	t.Parallel()

	workingDir := filepath.Join("C:\\", "runtime", "frps")
	want := filepath.Join("C:\\", "runtime", "frps", "data", "config.json")

	if got := configPathForWorkingDir(workingDir); got != want {
		t.Fatalf("unexpected config path: got %q want %q", got, want)
	}
}
