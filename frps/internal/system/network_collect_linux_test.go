//go:build linux

package system

import "testing"

func TestNewPlatformCollectorLinux(t *testing.T) {
	t.Parallel()

	collector := newPlatformCollector()
	if collector == nil {
		t.Fatal("expected linux platform collector")
	}
	if collector.Platform() != "linux" {
		t.Fatalf("unexpected linux collector platform: %q", collector.Platform())
	}
}
