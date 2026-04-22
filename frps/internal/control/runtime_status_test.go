package control

import (
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestServerEnsureTunnelListenersRecordsAndClearsRuntimeIssueOnBindFailure(t *testing.T) {
	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	remotePort := freeTCPPort(t)
	blocker, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(remotePort)))
	if err != nil {
		t.Fatalf("listen blocker: %v", err)
	}

	server := NewServer(
		Options{
			WriteTimeout: time.Second,
			Network: staticSnapshotReader{
				snapshot: system.Snapshot{
					AvailableIPs: []system.IPAddress{
						{Addr: "127.0.0.1", Family: system.FamilyIPv4},
					},
				},
			},
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	failedSession := newTestSessionState(GroupRuntime{
		ID:          1,
		Name:        "group-a",
		EffectiveIP: "127.0.0.1",
	}, ConfigSnapshot{
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
	})

	err = server.ensureTunnelListeners(serverConn, slog.New(slog.NewTextHandler(io.Discard, nil)), failedSession)
	if err == nil {
		t.Fatal("expected blocked listener bind to fail")
	}
	issues := server.TunnelRuntimeIssues()
	if reason := issues[7]; !strings.Contains(reason, "listener 127.0.0.1") {
		t.Fatalf("unexpected recorded runtime issue: %#v", issues)
	}

	_ = blocker.Close()

	successSession := newTestSessionState(GroupRuntime{
		ID:          2,
		Name:        "group-a",
		EffectiveIP: "127.0.0.1",
	}, ConfigSnapshot{
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
	})
	defer server.shutdownSession(successSession)

	if err := server.ensureTunnelListeners(serverConn, slog.New(slog.NewTextHandler(io.Discard, nil)), successSession); err != nil {
		t.Fatalf("ensure tunnel listeners after unblocking: %v", err)
	}
	if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
		t.Fatalf("expected runtime issues to clear after successful listener start: %#v", issues)
	}
}
