package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/control"
)

type blockingControlRepository struct {
	group          control.GroupRuntime
	loadAllStarted chan struct{}
	allowLoadAll   chan struct{}
}

func (r *blockingControlRepository) LoadGroupRuntimeByClientID(_ context.Context, _ [16]byte) (control.GroupRuntime, error) {
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
