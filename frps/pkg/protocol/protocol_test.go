package protocol

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestChallengeResponseStable(t *testing.T) {
	var secretHash [32]byte
	for i := range secretHash {
		secretHash[i] = byte(i)
	}

	var nonce [16]byte
	copy(nonce[:], []byte("nonce-1234567890"))

	got := ChallengeResponse(secretHash, nonce)
	want := [32]byte{
		0xea, 0x7b, 0x82, 0xcb, 0x92, 0x6b, 0xc5, 0x6f,
		0x20, 0x45, 0x61, 0xab, 0xb7, 0x82, 0xa4, 0x3c,
		0x00, 0xb3, 0xdf, 0xbd, 0x53, 0x1d, 0x60, 0xba,
		0x42, 0x7a, 0x1d, 0x5b, 0x7f, 0x4d, 0x36, 0x14,
	}
	if got != want {
		t.Fatalf("unexpected challenge response: %x", got)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	original := Frame{
		Type:      TypeHeartbeatPing,
		RequestID: 42,
		Body:      []byte{1, 2, 3},
	}

	wire, err := original.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	got, err := ParseFrame(wire)
	if err != nil {
		t.Fatalf("parse frame: %v", err)
	}

	if got.Version != Version {
		t.Fatalf("unexpected version: %d", got.Version)
	}
	if got.Type != original.Type {
		t.Fatalf("unexpected type: %v", got.Type)
	}
	if got.RequestID != original.RequestID {
		t.Fatalf("unexpected request id: %d", got.RequestID)
	}
	if string(got.Body) != string(original.Body) {
		t.Fatalf("unexpected body: %v", got.Body)
	}
}

func TestParseFrameRejectsInvalidFlags(t *testing.T) {
	wire, err := Frame{
		Type:  TypeAuthBegin,
		Flags: 1,
	}.MarshalBinary()
	if err == nil {
		t.Fatal("expected marshal to reject invalid flags")
	}

	protocolErr := AsProtocolError(err)
	if protocolErr == nil || protocolErr.Code != ErrorCodeProtocolInvalidFlags {
		t.Fatalf("unexpected error: %v", err)
	}

	wire = []byte{
		Version, byte(TypeAuthBegin), 0, 1,
		0, 0, 0, 1,
		0, 0, 0, 0,
	}
	_, err = ParseFrame(wire)
	protocolErr = AsProtocolError(err)
	if protocolErr == nil || protocolErr.Code != ErrorCodeProtocolInvalidFlags {
		t.Fatalf("expected invalid flags error, got %v", err)
	}
}

func TestAuthBeginRoundTrip(t *testing.T) {
	var clientID [16]byte
	copy(clientID[:], []byte("1234567890abcdef"))

	body, err := MarshalAuthBegin(AuthBegin{
		ClientID:       clientID,
		ClientVersion:  "dev",
		Hostname:       "node-1",
		OS:             OSLinux,
		Arch:           ArchAMD64,
		CapabilityBits: 7,
	})
	if err != nil {
		t.Fatalf("marshal auth begin: %v", err)
	}

	got, err := UnmarshalAuthBegin(body)
	if err != nil {
		t.Fatalf("unmarshal auth begin: %v", err)
	}
	if got.ClientID != clientID {
		t.Fatalf("unexpected client id: %x", got.ClientID)
	}
	if got.ClientVersion != "dev" || got.Hostname != "node-1" {
		t.Fatalf("unexpected client fields: %#v", got)
	}
	if got.OS != OSLinux || got.Arch != ArchAMD64 || got.CapabilityBits != 7 {
		t.Fatalf("unexpected runtime fields: %#v", got)
	}
}

func TestConfigPushRoundTrip(t *testing.T) {
	host, err := ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	caPEM := "-----BEGIN CERTIFICATE-----\n" + strings.Repeat("A", 70000) + "\n-----END CERTIFICATE-----\n"
	body, err := MarshalConfigPush(ConfigPush{
		ConfigVersion: 12,
		GeneratedAtMs: 34,
		Tunnels: []TunnelEntry{
			{
				TunnelName:                   "backend-api",
				TunnelID:                     1,
				Protocol:                     ProtocolTCP,
				TunnelFlags:                  TunnelFlagEnabled,
				ListenTLSMode:                TunnelTLSModeTLS,
				RemoteStart:                  20000,
				RemoteEnd:                    20000,
				LocalHost:                    host,
				LocalStart:                   22,
				LocalEnd:                     22,
				Revision:                     5678,
				BackendTLSMode:               TunnelTLSModeMTLS,
				BackendTLSLoadSystemCA:       true,
				BackendTLSInsecureSkipVerify: true,
				BackendTLSServerName:         "backend.internal",
				BackendTLSCAPEM:              caPEM,
				BackendTLSClientCertPEM:      "client-cert",
				BackendTLSClientKeyPEM:       "client-key",
				RatePolicy: TunnelRatePolicy{
					PolicyID:    77,
					Mode:        RatePolicyModeShared,
					DownlinkBPS: 10_000_000,
					UplinkBPS:   5_000_000,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal config push: %v", err)
	}

	got, err := UnmarshalConfigPush(body)
	if err != nil {
		t.Fatalf("unmarshal config push: %v", err)
	}
	if got.ConfigVersion != 12 || got.GeneratedAtMs != 34 {
		t.Fatalf("unexpected config metadata: %#v", got)
	}
	if len(got.Tunnels) != 1 {
		t.Fatalf("unexpected tunnel count: %d", len(got.Tunnels))
	}
	tunnel := got.Tunnels[0]
	if tunnel.LocalHost.String() != "127.0.0.1" {
		t.Fatalf("unexpected local host: %s", tunnel.LocalHost.String())
	}
	if tunnel.TunnelName != "backend-api" {
		t.Fatalf("unexpected tunnel name: %q", tunnel.TunnelName)
	}
	if tunnel.Revision != 5678 || tunnel.ListenTLSMode != TunnelTLSModeTLS || tunnel.BackendTLSMode != TunnelTLSModeMTLS {
		t.Fatalf("unexpected tunnel metadata: %#v", tunnel)
	}
	if !tunnel.BackendTLSLoadSystemCA || !tunnel.BackendTLSInsecureSkipVerify {
		t.Fatalf("unexpected backend tls flags: %#v", tunnel)
	}
	if tunnel.BackendTLSServerName != "backend.internal" {
		t.Fatalf("unexpected backend server name: %q", tunnel.BackendTLSServerName)
	}
	if tunnel.BackendTLSCAPEM != caPEM || tunnel.BackendTLSClientCertPEM != "client-cert" || tunnel.BackendTLSClientKeyPEM != "client-key" {
		t.Fatalf("unexpected backend tls materials: %#v", tunnel)
	}
	if tunnel.RatePolicy != (TunnelRatePolicy{}) {
		t.Fatalf("expected config.push wire tunnel to omit rate policy fields, got %#v", tunnel.RatePolicy)
	}
}

func TestStreamMessagesRoundTrip(t *testing.T) {
	openBody, err := MarshalStreamOpen(StreamOpen{
		TunnelID:   7,
		RemotePort: 20000,
		ClientAddr: SockAddr{
			IP:   net.ParseIP("203.0.113.10").To4(),
			Port: 54321,
		},
		OpenedAtMs: 1234,
	})
	if err != nil {
		t.Fatalf("marshal stream.open: %v", err)
	}

	open, err := UnmarshalStreamOpen(openBody)
	if err != nil {
		t.Fatalf("unmarshal stream.open: %v", err)
	}
	if open.TunnelID != 7 || open.RemotePort != 20000 || open.ClientAddr.Port != 54321 || open.OpenedAtMs != 1234 {
		t.Fatalf("unexpected stream.open: %#v", open)
	}
	if open.ClientAddr.IP.String() != "203.0.113.10" {
		t.Fatalf("unexpected client ip: %s", open.ClientAddr.IP.String())
	}

	openedBody, err := MarshalStreamOpened(StreamOpened{
		Status:    StatusError,
		ErrorCode: ErrorCodeStreamLocalDialFailed,
		Message:   "dial failed",
	})
	if err != nil {
		t.Fatalf("marshal stream.opened: %v", err)
	}

	opened, err := UnmarshalStreamOpened(openedBody)
	if err != nil {
		t.Fatalf("unmarshal stream.opened: %v", err)
	}
	if opened.Status != StatusError || opened.ErrorCode != ErrorCodeStreamLocalDialFailed || opened.Message != "dial failed" {
		t.Fatalf("unexpected stream.opened: %#v", opened)
	}

	closeBody, err := MarshalStreamClose(StreamClose{
		ReasonCode: CloseReasonEOF,
		Initiator:  InitiatorFRPC,
		Message:    "done",
	})
	if err != nil {
		t.Fatalf("marshal stream.close: %v", err)
	}

	closeMessage, err := UnmarshalStreamClose(closeBody)
	if err != nil {
		t.Fatalf("unmarshal stream.close: %v", err)
	}
	if closeMessage.ReasonCode != CloseReasonEOF || closeMessage.Initiator != InitiatorFRPC || closeMessage.Message != "done" {
		t.Fatalf("unexpected stream.close: %#v", closeMessage)
	}
}

func TestUDPMessagesRoundTrip(t *testing.T) {
	openBody, err := MarshalUDPOpen(UDPOpen{
		TunnelID:   8,
		RemotePort: 21000,
		ClientAddr: SockAddr{
			IP:   net.ParseIP("2001:db8::10"),
			Port: 45678,
		},
		IdleTimeoutMs: 30000,
	})
	if err != nil {
		t.Fatalf("marshal udp.open: %v", err)
	}

	open, err := UnmarshalUDPOpen(openBody)
	if err != nil {
		t.Fatalf("unmarshal udp.open: %v", err)
	}
	if open.TunnelID != 8 || open.RemotePort != 21000 || open.ClientAddr.Port != 45678 || open.IdleTimeoutMs != 30000 {
		t.Fatalf("unexpected udp.open: %#v", open)
	}
	if open.ClientAddr.IP.String() != "2001:db8::10" {
		t.Fatalf("unexpected client ip: %s", open.ClientAddr.IP.String())
	}

	closeBody, err := MarshalUDPClose(UDPClose{
		ReasonCode: CloseReasonIdleTimeout,
		Initiator:  InitiatorFRPS,
		Message:    "idle timeout",
	})
	if err != nil {
		t.Fatalf("marshal udp.close: %v", err)
	}

	closeMessage, err := UnmarshalUDPClose(closeBody)
	if err != nil {
		t.Fatalf("unmarshal udp.close: %v", err)
	}
	if closeMessage.ReasonCode != CloseReasonIdleTimeout || closeMessage.Initiator != InitiatorFRPS || closeMessage.Message != "idle timeout" {
		t.Fatalf("unexpected udp.close: %#v", closeMessage)
	}
}

func TestErrorBodyRejectsInvalidBool(t *testing.T) {
	body := []byte{0x04, 0x4d, 0x02, 0x00, 0x00}
	_, err := UnmarshalErrorBody(body)
	if err == nil {
		t.Fatal("expected invalid bool error")
	}
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.Code != ErrorCodeProtocolBadBody {
		t.Fatalf("unexpected error: %v", err)
	}
}
