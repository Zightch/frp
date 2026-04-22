package control

import (
	"context"
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

	blockedPort := freeTCPPort(t)
	healthyPort := freeTCPPortExcept(t, blockedPort)
	blocker, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(blockedPort)))
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

	session := newTestSessionState(GroupRuntime{
		ID:          1,
		Name:        "group-a",
		EffectiveIP: "127.0.0.1",
	}, ConfigSnapshot{
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(blockedPort),
				RemoteEnd:   uint16(blockedPort),
				LocalHost:   host,
				LocalStart:  22,
				LocalEnd:    22,
			},
			{
				TunnelID:    8,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(healthyPort),
				RemoteEnd:   uint16(healthyPort),
				LocalHost:   host,
				LocalStart:  23,
				LocalEnd:    23,
			},
		},
	})
	defer server.shutdownSession(session)

	if err := server.ensureTunnelListeners(serverConn, slog.New(slog.NewTextHandler(io.Discard, nil)), session); err != nil {
		t.Fatalf("ensure tunnel listeners with partial bind failure: %v", err)
	}
	issues := server.TunnelRuntimeIssues()
	if reason := issues[7]; !strings.Contains(reason, "端口冲突") {
		t.Fatalf("unexpected recorded runtime issue: %#v", issues)
	}
	if reason := issues[8]; reason != "" {
		t.Fatalf("unexpected runtime issue for healthy tunnel: %#v", issues)
	}
	if len(session.listeners[7]) != 0 {
		t.Fatalf("unexpected listeners started for blocked tunnel: %#v", session.listeners[7])
	}
	if len(session.listeners[8]) != 1 {
		t.Fatalf("expected healthy tunnel listener to start, got %#v", session.listeners[8])
	}

	_ = blocker.Close()

	if err := server.ensureTunnelListeners(serverConn, slog.New(slog.NewTextHandler(io.Discard, nil)), session); err != nil {
		t.Fatalf("ensure tunnel listeners after unblocking: %v", err)
	}
	if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
		t.Fatalf("expected runtime issues to clear after successful listener start: %#v", issues)
	}
	if len(session.listeners[7]) != 1 {
		t.Fatalf("expected recovered tunnel listener to start, got %#v", session.listeners[7])
	}
	if len(session.listeners[8]) != 1 {
		t.Fatalf("expected healthy tunnel listener to remain active, got %#v", session.listeners[8])
	}
}

func TestServerEnsureTunnelListenersDetectsRuntimeConflictWithActiveGroup(t *testing.T) {
	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	remotePort := freeTCPPort(t)
	healthyPort := freeTCPPortExcept(t, remotePort)
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

	otherSession := newTestSessionState(GroupRuntime{
		ID:          2,
		Name:        "group-b",
		EffectiveIP: system.AnyIPv4,
	}, ConfigSnapshot{
		Version: 1,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    8,
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
	otherClientConn, otherServerConn := net.Pipe()
	defer otherClientConn.Close()
	defer otherServerConn.Close()
	defer server.shutdownSession(otherSession)
	if err := server.ensureTunnelListeners(otherServerConn, slog.New(slog.NewTextHandler(io.Discard, nil)), otherSession); err != nil {
		t.Fatalf("start active group listeners: %v", err)
	}
	server.registerActiveSession(otherServerConn, otherSession)
	defer server.unregisterActiveSession(otherSession)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	currentSession := newTestSessionState(GroupRuntime{
		ID:          1,
		Name:        "group-a",
		EffectiveIP: "127.0.0.1",
	}, ConfigSnapshot{
		Version: 1,
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
			{
				TunnelID:    9,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(healthyPort),
				RemoteEnd:   uint16(healthyPort),
				LocalHost:   host,
				LocalStart:  23,
				LocalEnd:    23,
			},
		},
	})
	defer server.shutdownSession(currentSession)

	if err := server.ensureTunnelListeners(serverConn, slog.New(slog.NewTextHandler(io.Discard, nil)), currentSession); err != nil {
		t.Fatalf("ensure tunnel listeners with active-group conflict: %v", err)
	}

	issues := server.TunnelRuntimeIssues()
	reason := issues[7]
	if !strings.Contains(reason, `分组"group-b"`) || !strings.Contains(reason, "无法启动监听") {
		t.Fatalf("unexpected recorded runtime issue: %#v", issues)
	}
	if reason := issues[9]; reason != "" {
		t.Fatalf("unexpected runtime issue for healthy tunnel: %#v", issues)
	}
	if len(currentSession.listeners[7]) != 0 {
		t.Fatalf("unexpected listeners started for conflicted tunnel: %#v", currentSession.listeners[7])
	}
	if len(currentSession.listeners[9]) != 1 {
		t.Fatalf("expected healthy tunnel listener to start, got %#v", currentSession.listeners[9])
	}
}

func TestServerScanNonListeningTunnelRuntimeIssuesRecordsAndClearsRecoveredBindFailure(t *testing.T) {
	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	remotePort := freeTCPPort(t)
	blocker, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(remotePort)))
	if err != nil {
		t.Fatalf("listen blocker: %v", err)
	}
	defer blocker.Close()

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: "127.0.0.1",
			Snapshot: ConfigSnapshot{
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
	}

	server := NewServer(
		Options{
			Repository: repo,
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

	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues with blocker: %v", err)
	}
	if reason := server.TunnelRuntimeIssues()[7]; !strings.Contains(reason, "端口冲突") {
		t.Fatalf("unexpected recorded runtime issue after blocker: %#v", server.TunnelRuntimeIssues())
	}

	_ = blocker.Close()

	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues after unblocking: %v", err)
	}
	if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
		t.Fatalf("expected runtime issues to clear after port recovery: %#v", issues)
	}
}

func TestServerScanNonListeningTunnelRuntimeIssuesRecordsEffectiveIPFailureForEnabledTunnels(t *testing.T) {
	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: "127.0.0.2",
			Snapshot: ConfigSnapshot{
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: 21001,
						RemoteEnd:   21001,
						LocalHost:   host,
						LocalStart:  22,
						LocalEnd:    22,
					},
					{
						TunnelID:    8,
						Protocol:    protocol.ProtocolUDP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: 22001,
						RemoteEnd:   22001,
						LocalHost:   host,
						LocalStart:  53,
						LocalEnd:    53,
					},
				},
			},
		},
	}

	server := NewServer(
		Options{
			Repository: repo,
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

	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues with missing effective_ip: %v", err)
	}

	issues := server.TunnelRuntimeIssues()
	for _, tunnelID := range []int64{7, 8} {
		reason := issues[tunnelID]
		if !strings.Contains(reason, "当前不存在于本机") || !strings.Contains(reason, "无法启动监听") {
			t.Fatalf("unexpected recorded effective_ip runtime issue: %#v", issues)
		}
	}
}
