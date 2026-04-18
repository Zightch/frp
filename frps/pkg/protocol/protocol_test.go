package protocol

import (
	"errors"
	"testing"
)

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
	var tokenID [16]byte
	copy(tokenID[:], []byte("1234567890abcdef"))

	body, err := MarshalAuthBegin(AuthBegin{
		TokenID:        tokenID,
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
	if got.TokenID != tokenID {
		t.Fatalf("unexpected token id: %x", got.TokenID)
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

	body, err := MarshalConfigPush(ConfigPush{
		ConfigVersion: 12,
		GeneratedAtMs: 34,
		Tunnels: []TunnelEntry{
			{
				TunnelID:    1,
				Protocol:    ProtocolTCP,
				TunnelFlags: TunnelFlagEnabled,
				RemoteStart: 20000,
				RemoteEnd:   20000,
				LocalHost:   host,
				LocalStart:  22,
				LocalEnd:    22,
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
	if got.Tunnels[0].LocalHost.String() != "127.0.0.1" {
		t.Fatalf("unexpected local host: %s", got.Tunnels[0].LocalHost.String())
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
