package auth

import (
	"github.com/zightch/frp/frps/pkg/protocol"
)

func DecodeAuthBeginFrame(frame protocol.Frame, expectedClientID [16]byte) (protocol.AuthBegin, error) {
	if frame.Type != protocol.TypeAuthBegin {
		return protocol.AuthBegin{}, protocol.NewError(
			protocol.ErrorCodeProtocolBadBody,
			"expected auth.begin, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return protocol.AuthBegin{}, protocol.NewError(protocol.ErrorCodeProtocolBadBody, "auth.begin requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return protocol.AuthBegin{}, protocol.NewError(protocol.ErrorCodeProtocolBadBody, "auth.begin streamId must be zero")
	}

	begin, err := protocol.UnmarshalAuthBegin(frame.Body)
	if err != nil {
		return protocol.AuthBegin{}, err
	}
	if begin.ClientID != expectedClientID {
		return protocol.AuthBegin{}, protocol.NewError(protocol.ErrorCodeAuthInvalidClient, "auth.begin client_id does not match transport.client_hello")
	}
	return begin, nil
}

func DecodeAuthFinishFrame(frame protocol.Frame) (protocol.AuthFinish, error) {
	if frame.Type != protocol.TypeAuthFinish {
		return protocol.AuthFinish{}, protocol.NewError(
			protocol.ErrorCodeProtocolBadBody,
			"expected auth.finish, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return protocol.AuthFinish{}, protocol.NewError(protocol.ErrorCodeProtocolBadBody, "auth.finish requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return protocol.AuthFinish{}, protocol.NewError(protocol.ErrorCodeProtocolBadBody, "auth.finish streamId must be zero")
	}
	return protocol.UnmarshalAuthFinish(frame.Body)
}
