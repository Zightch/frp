package client

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestNoteReconnectLogsOnlyOncePerReconnectCycle(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := New(testConfig(), logger, "test-client")

	client.noteReconnect()
	client.noteReconnect()

	got := buffer.String()
	if strings.Count(got, "重连 frps 中...") != 1 {
		t.Fatalf("expected one reconnect log, got %q", got)
	}
	if strings.Contains(got, "重连失败") {
		t.Fatalf("did not expect reconnect failure logs, got %q", got)
	}

	client.markSessionActive()
	client.noteReconnect()
	if strings.Count(buffer.String(), "重连 frps 中...") != 2 {
		t.Fatalf("expected reconnect cycle to log again after recovery, got %q", buffer.String())
	}
}

func TestLogConfigAppliedUsesCompactMessagesAndTLSDetails(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := New(testConfig(), logger, "test-client")

	client.logConfigApplied(protocol.ConfigPush{
		ConfigVersion: 7,
		Tunnels: []protocol.TunnelEntry{{
			TunnelID:               3,
			TunnelName:             "tls-api",
			Protocol:               protocol.ProtocolTCP,
			TunnelFlags:            protocol.TunnelFlagEnabled,
			RemoteStart:            17443,
			RemoteEnd:              17443,
			LocalHost:              mustHost(t, "127.0.0.1"),
			LocalStart:             18443,
			LocalEnd:               18443,
			BackendTLSMode:         protocol.TunnelTLSModeTLS,
			BackendTLSLoadSystemCA: true,
			BackendTLSServerName:   "api.internal",
		}},
	}, configReloadSummary{
		addedTunnels:    1,
		replacedTunnels: 0,
		removedTunnels:  0,
	})

	got := buffer.String()
	if !strings.Contains(got, "领取配置 隧道数量1 启用1 +1 ~0 -0") {
		t.Fatalf("expected compact config summary, got %q", got)
	}
	if strings.Contains(got, "version=7") {
		t.Fatalf("did not expect config version in log, got %q", got)
	}
	if !strings.Contains(got, "隧道[tls-api] 已启动 tcp 17443 -> 127.0.0.1:18443 tls=tls") {
		t.Fatalf("expected tunnel ready summary with tls mode, got %q", got)
	}
	if !strings.Contains(got, "ca=system") || !strings.Contains(got, "sni=api.internal") {
		t.Fatalf("expected backend tls details in log, got %q", got)
	}
}
