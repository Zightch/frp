package system

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestSnapshotFromDiscoveredInterfacesNormalizesAndDeduplicates(t *testing.T) {
	t.Parallel()

	capturedAt := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	snapshot := snapshotFromDiscoveredInterfaces("linux", capturedAt, []discoveredInterface{
		{
			Name:  "eth1",
			Index: 2,
			Addrs: []net.Addr{
				&net.IPNet{IP: net.ParseIP("fe80::1")},
				&net.IPNet{IP: net.ParseIP("10.0.0.8")},
				&net.IPAddr{IP: net.ParseIP("10.0.0.8")},
			},
		},
		{
			Name:  "lo",
			Index: 1,
			Addrs: []net.Addr{
				&net.IPNet{IP: net.ParseIP("::1")},
				&net.IPNet{IP: net.ParseIP("127.0.0.1")},
			},
		},
	})

	if snapshot.Platform != "linux" {
		t.Fatalf("unexpected platform: %q", snapshot.Platform)
	}
	if !snapshot.CapturedAt.Equal(capturedAt) {
		t.Fatalf("unexpected capturedAt: %v", snapshot.CapturedAt)
	}
	if len(snapshot.Interfaces) != 2 {
		t.Fatalf("unexpected interface count: %d", len(snapshot.Interfaces))
	}
	if snapshot.Interfaces[0].Name != "lo" || snapshot.Interfaces[1].Name != "eth1" {
		t.Fatalf("unexpected interface order: %#v", snapshot.Interfaces)
	}
	if len(snapshot.Interfaces[1].IPs) != 2 {
		t.Fatalf("unexpected eth1 ip count: %#v", snapshot.Interfaces[1].IPs)
	}
	if got, want := snapshot.Interfaces[1].IPs[0], (IPAddress{Addr: "10.0.0.8", Family: FamilyIPv4}); got != want {
		t.Fatalf("unexpected first eth1 ip: got %#v want %#v", got, want)
	}
	if got, want := snapshot.Interfaces[1].IPs[1], (IPAddress{Addr: "fe80::1", Family: FamilyIPv6}); got != want {
		t.Fatalf("unexpected second eth1 ip: got %#v want %#v", got, want)
	}
	if got := snapshot.AvailableIPStrings(); len(got) != 4 {
		t.Fatalf("unexpected available ip list: %#v", got)
	}
}

func TestNetworkSnapshotServicePollsAndKeepsLastGoodSnapshot(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := testSnapshot("windows", time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC), "10.0.0.1")
	second := testSnapshot("windows", time.Date(2026, 4, 21, 12, 0, 1, 0, time.UTC), "10.0.0.2", "2001:db8::1")

	service := NewNetworkSnapshotService(Options{
		PollInterval: 10 * time.Millisecond,
		Collector: &fakeCollector{
			platform: "windows",
			results: []fakeCollectResult{
				{snapshot: first},
				{snapshot: second},
				{err: errors.New("temporary collector failure")},
				{err: errors.New("temporary collector failure")},
			},
		},
	}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := service.Start(ctx); err != nil {
		t.Fatalf("start service: %v", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		if err := service.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown service: %v", err)
		}
	}()

	if !service.Current().HasIP("10.0.0.1") {
		t.Fatalf("expected initial snapshot to be visible: %#v", service.Current())
	}

	waitFor(t, time.Second, func() bool {
		return service.Current().HasIP("10.0.0.2")
	})

	time.Sleep(40 * time.Millisecond)

	current := service.Current()
	if !current.HasIP("10.0.0.2") || !current.HasIP("2001:db8::1") {
		t.Fatalf("expected latest successful snapshot to be retained: %#v", current)
	}
	if current.HasIP("10.0.0.1") {
		t.Fatalf("expected previous snapshot to be replaced: %#v", current)
	}

	clone := service.Current()
	clone.Interfaces[0].IPs[0].Addr = "mutated"
	if service.Current().Interfaces[0].IPs[0].Addr == "mutated" {
		t.Fatal("expected Current to return a defensive copy")
	}
}

type fakeCollectResult struct {
	snapshot Snapshot
	err      error
}

type fakeCollector struct {
	platform string
	results  []fakeCollectResult
}

func (c *fakeCollector) Platform() string {
	return c.platform
}

func (c *fakeCollector) Collect() (Snapshot, error) {
	if len(c.results) == 0 {
		return Snapshot{}, errors.New("unexpected extra collect")
	}

	result := c.results[0]
	c.results = c.results[1:]
	if result.err != nil {
		return Snapshot{}, result.err
	}
	return cloneSnapshot(result.snapshot), nil
}

func testSnapshot(platform string, capturedAt time.Time, addrs ...string) Snapshot {
	ips := make([]IPAddress, 0, len(addrs))
	for _, addr := range addrs {
		family := FamilyIPv6
		if net.ParseIP(addr).To4() != nil {
			family = FamilyIPv4
		}
		ips = append(ips, IPAddress{
			Addr:   addr,
			Family: family,
		})
	}
	sortIPAddresses(ips)

	return Snapshot{
		Platform:   platform,
		CapturedAt: capturedAt,
		Interfaces: []NetworkInterface{
			{
				Name:  "eth0",
				Index: 1,
				IPs:   append([]IPAddress(nil), ips...),
			},
		},
		AvailableIPs: append([]IPAddress(nil), ips...),
	}
}

func waitFor(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("condition was not met before timeout")
}
