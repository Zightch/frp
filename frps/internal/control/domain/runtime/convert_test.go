package runtime

import (
	"strings"
	"testing"

	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestConfigTunnelsFromDesiredPreservesBackendTLSSettings(t *testing.T) {
	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	input := []protocol.TunnelEntry{{
		TunnelName:                   "tls-api",
		TunnelID:                     3,
		Protocol:                     protocol.ProtocolTCP,
		TunnelFlags:                  protocol.TunnelFlagEnabled,
		RemoteStart:                  17443,
		RemoteEnd:                    17443,
		LocalHost:                    host,
		LocalStart:                   18443,
		LocalEnd:                     18443,
		BackendTLSMode:               protocol.TunnelTLSModeMTLS,
		BackendTLSLoadSystemCA:       true,
		BackendTLSInsecureSkipVerify: true,
		BackendTLSServerName:         "api.internal",
		BackendTLSCAPEM:              "ca-pem",
		BackendTLSClientCertPEM:      "client-cert",
		BackendTLSClientKeyPEM:       "client-key",
	}}

	desired := DesiredTunnelsFromConfig(input)
	if len(desired) != 1 {
		t.Fatalf("unexpected desired tunnel count: %d", len(desired))
	}
	roundtrip := ConfigTunnelsFromDesired(desired)
	if len(roundtrip) != 1 {
		t.Fatalf("unexpected roundtrip tunnel count: %d", len(roundtrip))
	}
	if !SameTunnelEntry(input[0], roundtrip[0]) {
		t.Fatalf("expected backend tls roundtrip to match: want=%#v got=%#v", input[0], roundtrip[0])
	}
}

func TestConfigTunnelsFromDesiredPreservesCustomCAOnlyTLS(t *testing.T) {
	input := ConfigTunnelsFromDesired([]controlsession.DesiredTunnelRuntime{{
		TunnelID:                     9,
		TunnelName:                   "tls-ca-only",
		Protocol:                     "tcp",
		Enabled:                      true,
		RemoteStart:                  16000,
		RemoteEnd:                    16000,
		LocalHost:                    "backend.internal",
		LocalStart:                   8080,
		LocalEnd:                     8080,
		BackendTLSMode:               protocol.TunnelTLSModeTLS,
		BackendTLSLoadSystemCA:       false,
		BackendTLSInsecureSkipVerify: false,
		BackendTLSServerName:         "backend.internal",
		BackendTLSCAPEM:              strings.Repeat("A", 32),
	}})

	if len(input) != 1 {
		t.Fatalf("unexpected tunnel count: %d", len(input))
	}
	if input[0].BackendTLSMode != protocol.TunnelTLSModeTLS || input[0].BackendTLSCAPEM == "" {
		t.Fatalf("expected backend tls fields to survive desired->config conversion: %#v", input[0])
	}
}
