package app

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/config"
	"github.com/zightch/frp/frps/internal/control"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestAppRunDelaysManagementAPIUntilInitialRuntimeScanCompletes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	controlPort := freeTCPPort(t)
	managementPort := freeTCPPortExcept(t, controlPort)
	remotePort := freeTCPPortExcept(t, controlPort, managementPort)

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	tokenHash := sha256.Sum256([]byte("startup-scan-token"))
	repo := &blockingControlRepository{
		group: control.GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: system.AnyIPv4,
			TokenHash:   tokenHash,
			Snapshot: control.ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 1,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: uint16(remotePort),
						RemoteEnd:   uint16(remotePort),
						LocalHost:   host,
						LocalStart:  22,
						LocalEnd:    22,
					},
				},
			},
		},
		loadAllStarted: make(chan struct{}, 1),
		allowLoadAll:   make(chan struct{}),
	}

	originalNewControlServer := newControlServer
	newControlServer = func(options control.Options, logger *slog.Logger, version string) *control.Server {
		options.Repository = repo
		options.Store = nil
		options.RuntimeScanPoll = time.Hour
		return control.NewServer(options, logger, version)
	}
	defer func() {
		newControlServer = originalNewControlServer
	}()

	application := New(config.Config{
		ControlListenAddr:    net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort)),
		ManagementListenAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(managementPort)),
		ReadHeaderTimeout:    "1s",
		ShutdownTimeout:      "1s",
		Database: config.DatabaseConfig{
			Type: "sqlite",
			Path: filepath.Join(t.TempDir(), "frps.sqlite"),
		},
		WebUI: config.WebUIConfig{
			DistDir: filepath.Join(t.TempDir(), "missing-webui"),
		},
	}, logger, "test-server")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- application.Run(ctx)
	}()

	select {
	case <-repo.loadAllStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("initial runtime scan did not start")
	}

	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(managementPort)), 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("expected management api to remain closed until initial runtime scan completes")
	}

	close(repo.allowLoadAll)

	waitForListeningPort(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(managementPort)))

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("app run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("app run did not exit")
	}
}

type blockingControlRepository struct {
	group          control.GroupRuntime
	loadAllStarted chan struct{}
	allowLoadAll   chan struct{}
}

func (r *blockingControlRepository) LoadGroupRuntime(_ context.Context, _ [16]byte) (control.GroupRuntime, error) {
	return r.group, nil
}

func (r *blockingControlRepository) LoadGroupRuntimeByID(_ context.Context, _ int64) (control.GroupRuntime, error) {
	return r.group, nil
}

func (r *blockingControlRepository) ListGroupRuntimes(ctx context.Context) ([]control.GroupRuntime, error) {
	if r.loadAllStarted != nil {
		select {
		case r.loadAllStarted <- struct{}{}:
		default:
		}
	}
	if r.allowLoadAll != nil {
		select {
		case <-r.allowLoadAll:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []control.GroupRuntime{r.group}, nil
}

func freeTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free tcp port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func freeTCPPortExcept(t *testing.T, excluded ...int) int {
	t.Helper()

	blocked := make(map[int]struct{}, len(excluded))
	for _, port := range excluded {
		blocked[port] = struct{}{}
	}

	for attempt := 0; attempt < 100; attempt++ {
		port := freeTCPPort(t)
		if _, ok := blocked[port]; ok {
			continue
		}
		return port
	}

	t.Fatal("failed to allocate distinct tcp port")
	return 0
}

func waitForListeningPort(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for listening port %s: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
