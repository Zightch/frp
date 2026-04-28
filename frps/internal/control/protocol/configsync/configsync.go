package configsync

import (
	"errors"
	"fmt"
	"strings"

	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	"github.com/zightch/frp/frps/pkg/protocol"
)

var (
	ErrUnexpectedAck   = errors.New("unexpected config ack")
	ErrVersionMismatch = errors.New("config version mismatch")
)

type AckState struct {
	PendingRequestID uint32
	ExpectedVersion  uint64
}

type InitialRuntimeValidationOptions struct {
	Group                 controldomainruntime.GroupRuntime
	Snapshot              controldomainruntime.ConfigSnapshot
	ResolveEffectiveIP    func(controldomainruntime.GroupRuntime) (string, error)
	RecordRuntimeIssue    func(tunnelID uint32, configVersion uint64, reason string)
	RuntimeReason         func(controldomainruntime.GroupRuntime, error) string
	StartupRejectedReason func(controldomainruntime.GroupRuntime, error) (string, bool)
}

func BuildPushFrame(requestID uint32, snapshot controldomainruntime.ConfigSnapshot) (protocol.Frame, error) {
	body, err := BuildPushBody(snapshot)
	if err != nil {
		return protocol.Frame{}, err
	}
	return NewPushFrame(requestID, body), nil
}

func NewPushFrame(requestID uint32, body []byte) protocol.Frame {
	return protocol.Frame{
		Type:      protocol.TypeConfigPush,
		RequestID: requestID,
		Body:      body,
	}
}

func BuildPushBody(snapshot controldomainruntime.ConfigSnapshot) ([]byte, error) {
	return protocol.MarshalConfigPush(protocol.ConfigPush{
		ConfigVersion: snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		Tunnels:       snapshot.Tunnels,
	})
}

func DecodeAckFrame(frame protocol.Frame, state AckState) (protocol.ConfigAck, error) {
	if frame.Type != protocol.TypeConfigAck {
		return protocol.ConfigAck{}, protocol.NewError(
			protocol.ErrorCodeProtocolBadBody,
			"expected config.ack, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return protocol.ConfigAck{}, protocol.NewError(protocol.ErrorCodeProtocolBadBody, "config.ack requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return protocol.ConfigAck{}, protocol.NewError(protocol.ErrorCodeProtocolBadBody, "config.ack streamId must be zero")
	}
	if state.PendingRequestID == 0 || frame.RequestID != state.PendingRequestID {
		return protocol.ConfigAck{}, protocol.NewError(protocol.ErrorCodeProtocolBadBody, "unexpected config.ack requestId %d", frame.RequestID)
	}

	ack, err := protocol.UnmarshalConfigAck(frame.Body)
	if err != nil {
		return protocol.ConfigAck{}, err
	}
	if ack.ConfigVersion != state.ExpectedVersion {
		return protocol.ConfigAck{}, protocol.NewError(
			protocol.ErrorCodeProtocolBadBody,
			"config.ack version mismatch: got %d want %d",
			ack.ConfigVersion,
			state.ExpectedVersion,
		)
	}
	if ack.Status == protocol.StatusError {
		return protocol.ConfigAck{}, protocol.NewError(
			protocol.ErrorCodeConfigApplyFailed,
			"client rejected config version %d: %s",
			ack.ConfigVersion,
			strings.TrimSpace(ack.Message),
		)
	}
	if ack.Status != protocol.StatusOK {
		return protocol.ConfigAck{}, protocol.NewError(protocol.ErrorCodeProtocolBadBody, "unsupported config.ack status %d", ack.Status)
	}
	return ack, nil
}

func MapAcceptError(err error, requestID uint32, ack protocol.ConfigAck, expectedVersion uint64) (error, bool) {
	switch {
	case errors.Is(err, ErrUnexpectedAck):
		return protocol.NewError(protocol.ErrorCodeProtocolBadBody, "unexpected config.ack requestId %d", requestID), true
	case errors.Is(err, ErrVersionMismatch):
		return protocol.NewError(
			protocol.ErrorCodeProtocolBadBody,
			"config.ack version mismatch: got %d want %d",
			ack.ConfigVersion,
			expectedVersion,
		), true
	default:
		return nil, false
	}
}

func ValidateInitialRuntime(options InitialRuntimeValidationOptions) error {
	if options.ResolveEffectiveIP == nil {
		return fmt.Errorf("config initial runtime effective ip resolver is nil")
	}
	if options.RuntimeReason == nil {
		return fmt.Errorf("config initial runtime reason builder is nil")
	}
	if options.StartupRejectedReason == nil {
		return fmt.Errorf("config initial runtime rejected reason builder is nil")
	}

	if _, err := options.ResolveEffectiveIP(options.Group); err != nil {
		reason := options.RuntimeReason(options.Group, err)
		if options.RecordRuntimeIssue != nil {
			for _, tunnel := range controldomainruntime.EnabledTunnels(options.Snapshot) {
				options.RecordRuntimeIssue(tunnel.TunnelID, options.Snapshot.Version, reason)
			}
		}
		if reason, ok := options.StartupRejectedReason(options.Group, err); ok {
			return protocol.NewError(protocol.ErrorCodeConfigApplyFailed, "%s", reason)
		}
		return err
	}
	return nil
}
