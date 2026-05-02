package client

import (
	"testing"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestSameTunnelExecutionDetectsTunnelNameAndBackendTLSChanges(t *testing.T) {
	base := protocol.TunnelEntry{
		TunnelName:                   "web",
		TunnelID:                     7,
		Protocol:                     protocol.ProtocolTCP,
		TunnelFlags:                  protocol.TunnelFlagEnabled,
		RemoteStart:                  20000,
		RemoteEnd:                    20000,
		LocalHost:                    mustHost(t, "127.0.0.1"),
		LocalStart:                   8080,
		LocalEnd:                     8080,
		BackendTLSMode:               protocol.TunnelTLSModeTLS,
		BackendTLSLoadSystemCA:       true,
		BackendTLSServerName:         "backend.internal",
		BackendTLSClientCertPEM:      "",
		BackendTLSClientKeyPEM:       "",
		BackendTLSInsecureSkipVerify: false,
	}

	same := base
	if !sameTunnelExecution(base, same) {
		t.Fatal("expected identical tunnel execution to match")
	}

	renamed := base
	renamed.TunnelName = "api"
	if sameTunnelExecution(base, renamed) {
		t.Fatal("expected tunnel name change to invalidate execution equality")
	}

	tlsChanged := base
	tlsChanged.BackendTLSInsecureSkipVerify = true
	if sameTunnelExecution(base, tlsChanged) {
		t.Fatal("expected backend tls change to invalidate execution equality")
	}
}
