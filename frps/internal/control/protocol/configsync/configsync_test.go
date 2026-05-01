package configsync

import (
	"errors"
	"strings"
	"testing"

	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestBuildPushFrame(t *testing.T) {
	snapshot := controldomainruntime.ConfigSnapshot{
		Version:       7,
		GeneratedAtMs: 11,
		Tunnels: []protocol.TunnelEntry{{
			TunnelID:    3,
			Protocol:    protocol.ProtocolTCP,
			TunnelFlags: protocol.TunnelFlagEnabled,
			RemoteStart: 7000,
			RemoteEnd:   7000,
			LocalHost:   mustParseHost(t, "127.0.0.1"),
			LocalStart:  80,
			LocalEnd:    80,
			RatePolicy: protocol.TunnelRatePolicy{
				PolicyID:    9,
				Mode:        protocol.RatePolicyModeIndependent,
				DownlinkBPS: 10_000_000,
				UplinkBPS:   5_000_000,
			},
		}},
	}

	frame, err := BuildPushFrame(42, snapshot)
	if err != nil {
		t.Fatalf("build push frame: %v", err)
	}
	if frame.Type != protocol.TypeConfigPush || frame.RequestID != 42 || frame.StreamID != 0 {
		t.Fatalf("unexpected frame header: %#v", frame)
	}
	push, err := protocol.UnmarshalConfigPush(frame.Body)
	if err != nil {
		t.Fatalf("unmarshal push body: %v", err)
	}
	if push.ConfigVersion != snapshot.Version || push.GeneratedAtMs != snapshot.GeneratedAtMs || len(push.Tunnels) != 1 || push.Tunnels[0].TunnelID != 3 {
		t.Fatalf("unexpected push body: %#v", push)
	}
	if push.Tunnels[0].RatePolicy.PolicyID != 9 || push.Tunnels[0].RatePolicy.Mode != protocol.RatePolicyModeIndependent {
		t.Fatalf("unexpected push rate policy identity: %#v", push.Tunnels[0].RatePolicy)
	}
	if push.Tunnels[0].RatePolicy.DownlinkBPS != 10_000_000 || push.Tunnels[0].RatePolicy.UplinkBPS != 5_000_000 {
		t.Fatalf("unexpected push rate policy rates: %#v", push.Tunnels[0].RatePolicy)
	}
}

func TestDecodeAckFrameValidatesAck(t *testing.T) {
	validBody := mustMarshalConfigAck(t, protocol.ConfigAck{
		ConfigVersion: 9,
		Status:        protocol.StatusOK,
	})
	statusErrorBody := mustMarshalConfigAck(t, protocol.ConfigAck{
		ConfigVersion: 9,
		Status:        protocol.StatusError,
		Message:       " client reject ",
	})
	unsupportedStatusBody := mustMarshalConfigAck(t, protocol.ConfigAck{
		ConfigVersion: 9,
		Status:        99,
	})
	versionMismatchBody := mustMarshalConfigAck(t, protocol.ConfigAck{
		ConfigVersion: 10,
		Status:        protocol.StatusOK,
	})

	tests := []struct {
		name        string
		frame       protocol.Frame
		state       AckState
		wantCode    uint16
		wantMessage string
	}{
		{
			name: "zero request id",
			frame: protocol.Frame{
				Type: protocol.TypeConfigAck,
				Body: validBody,
			},
			state:       AckState{PendingRequestID: 42, ExpectedVersion: 9},
			wantCode:    protocol.ErrorCodeProtocolBadBody,
			wantMessage: "config.ack requestId must be non-zero",
		},
		{
			name: "non-zero stream id",
			frame: protocol.Frame{
				Type:      protocol.TypeConfigAck,
				RequestID: 42,
				StreamID:  3,
				Body:      validBody,
			},
			state:       AckState{PendingRequestID: 42, ExpectedVersion: 9},
			wantCode:    protocol.ErrorCodeProtocolBadBody,
			wantMessage: "config.ack streamId must be zero",
		},
		{
			name: "unexpected request id",
			frame: protocol.Frame{
				Type:      protocol.TypeConfigAck,
				RequestID: 43,
				Body:      validBody,
			},
			state:       AckState{PendingRequestID: 42, ExpectedVersion: 9},
			wantCode:    protocol.ErrorCodeProtocolBadBody,
			wantMessage: "unexpected config.ack requestId 43",
		},
		{
			name: "version mismatch",
			frame: protocol.Frame{
				Type:      protocol.TypeConfigAck,
				RequestID: 42,
				Body:      versionMismatchBody,
			},
			state:       AckState{PendingRequestID: 42, ExpectedVersion: 9},
			wantCode:    protocol.ErrorCodeProtocolBadBody,
			wantMessage: "config.ack version mismatch: got 10 want 9",
		},
		{
			name: "client status error",
			frame: protocol.Frame{
				Type:      protocol.TypeConfigAck,
				RequestID: 42,
				Body:      statusErrorBody,
			},
			state:       AckState{PendingRequestID: 42, ExpectedVersion: 9},
			wantCode:    protocol.ErrorCodeConfigApplyFailed,
			wantMessage: "client rejected config version 9: client reject",
		},
		{
			name: "unsupported status",
			frame: protocol.Frame{
				Type:      protocol.TypeConfigAck,
				RequestID: 42,
				Body:      unsupportedStatusBody,
			},
			state:       AckState{PendingRequestID: 42, ExpectedVersion: 9},
			wantCode:    protocol.ErrorCodeProtocolBadBody,
			wantMessage: "unsupported config.ack status 99",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeAckFrame(tt.frame, tt.state)
			if err == nil {
				t.Fatal("expected error")
			}
			got := protocolError(t, err)
			if got.Code != tt.wantCode || !strings.Contains(got.Message, tt.wantMessage) {
				t.Fatalf("unexpected error: code=%d message=%q", got.Code, got.Message)
			}
		})
	}

	ack, err := DecodeAckFrame(protocol.Frame{
		Type:      protocol.TypeConfigAck,
		RequestID: 42,
		Body:      validBody,
	}, AckState{PendingRequestID: 42, ExpectedVersion: 9})
	if err != nil {
		t.Fatalf("decode valid ack: %v", err)
	}
	if ack.ConfigVersion != 9 || ack.Status != protocol.StatusOK {
		t.Fatalf("unexpected ack: %#v", ack)
	}
}

func TestMapAcceptError(t *testing.T) {
	ack := protocol.ConfigAck{ConfigVersion: 8}

	mapped, ok := MapAcceptError(ErrUnexpectedAck, 42, ack, 9)
	if !ok {
		t.Fatal("expected unexpected ack mapping")
	}
	if got := protocolError(t, mapped); got.Code != protocol.ErrorCodeProtocolBadBody || got.Message != "unexpected config.ack requestId 42" {
		t.Fatalf("unexpected mapped error: %#v", got)
	}

	mapped, ok = MapAcceptError(ErrVersionMismatch, 42, ack, 9)
	if !ok {
		t.Fatal("expected version mismatch mapping")
	}
	if got := protocolError(t, mapped); got.Code != protocol.ErrorCodeProtocolBadBody || got.Message != "config.ack version mismatch: got 8 want 9" {
		t.Fatalf("unexpected mapped error: %#v", got)
	}

	if mapped, ok := MapAcceptError(errors.New("boom"), 42, ack, 9); ok || mapped != nil {
		t.Fatalf("unexpected unknown mapping: mapped=%v ok=%v", mapped, ok)
	}
}

func TestValidateInitialRuntimeRecordsIssuesAndRejectsStartup(t *testing.T) {
	resolveErr := errors.New("not local")
	var recorded []uint32

	err := ValidateInitialRuntime(InitialRuntimeValidationOptions{
		Group: controldomainruntime.GroupRuntime{ID: 5, EffectiveIP: "192.0.2.10"},
		Snapshot: controldomainruntime.ConfigSnapshot{
			Version: 12,
			Tunnels: []protocol.TunnelEntry{
				{TunnelID: 1, TunnelFlags: protocol.TunnelFlagEnabled},
				{TunnelID: 2},
			},
		},
		ResolveEffectiveIP: func(controldomainruntime.GroupRuntime) (string, error) {
			return "", resolveErr
		},
		RecordRuntimeIssue: func(tunnelID uint32, configVersion uint64, reason string) {
			if configVersion != 12 || reason != "runtime reason" {
				t.Fatalf("unexpected runtime issue: tunnel=%d version=%d reason=%q", tunnelID, configVersion, reason)
			}
			recorded = append(recorded, tunnelID)
		},
		RuntimeReason: func(controldomainruntime.GroupRuntime, error) string {
			return "runtime reason"
		},
		StartupRejectedReason: func(controldomainruntime.GroupRuntime, error) (string, bool) {
			return "startup rejected", true
		},
	})
	if got := protocolError(t, err); got.Code != protocol.ErrorCodeConfigApplyFailed || got.Message != "startup rejected" {
		t.Fatalf("unexpected validation error: %#v", got)
	}
	if len(recorded) != 1 || recorded[0] != 1 {
		t.Fatalf("unexpected recorded tunnel ids: %#v", recorded)
	}
}

func mustMarshalConfigAck(t *testing.T, ack protocol.ConfigAck) []byte {
	t.Helper()
	body, err := protocol.MarshalConfigAck(ack)
	if err != nil {
		t.Fatalf("marshal config ack: %v", err)
	}
	return body
}

func mustParseHost(t *testing.T, value string) protocol.Host {
	t.Helper()
	host, err := protocol.ParseHost(value)
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}
	return host
}

func protocolError(t *testing.T, err error) *protocol.ProtocolError {
	t.Helper()
	protocolErr := protocol.AsProtocolError(err)
	if protocolErr == nil {
		t.Fatalf("expected protocol error, got %v", err)
	}
	return protocolErr
}
