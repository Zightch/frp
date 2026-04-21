//go:build windows

package system

import "testing"

func TestNewPlatformCollectorWindows(t *testing.T) {
	t.Parallel()

	collector := newPlatformCollector()
	if collector == nil {
		t.Fatal("expected windows platform collector")
	}
	if collector.Platform() != "windows" {
		t.Fatalf("unexpected windows collector platform: %q", collector.Platform())
	}
}
