package errors

import (
	stderrors "errors"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

type FrameWriter interface {
	WriteFrame(frame protocol.Frame) error
}

func ReplyProtocolError(writer FrameWriter, frame protocol.Frame, err error) error {
	protocolErr := protocol.AsProtocolError(err)
	if protocolErr == nil {
		return err
	}
	if writeErr := WriteError(writer, frame.RequestID, frame.StreamID, protocolErr.Code, false, protocolErr.Message); writeErr != nil {
		return stderrors.Join(err, writeErr)
	}
	return err
}

func ReplyError(writer FrameWriter, requestID, streamID uint32, code uint16, format string, args ...any) error {
	err := protocol.NewError(code, format, args...)
	if writeErr := WriteError(writer, requestID, streamID, code, false, err.Message); writeErr != nil {
		return stderrors.Join(err, writeErr)
	}
	return err
}

func WriteError(writer FrameWriter, requestID, streamID uint32, code uint16, retryable bool, message string) error {
	if writer == nil {
		return fmt.Errorf("frame writer is nil")
	}
	body, err := protocol.MarshalErrorBody(protocol.ErrorBody{
		ErrorCode: code,
		Retryable: retryable,
		Message:   message,
	})
	if err != nil {
		return err
	}
	return writer.WriteFrame(protocol.Frame{
		Type:      protocol.TypeError,
		RequestID: requestID,
		StreamID:  streamID,
		Body:      body,
	})
}

func ConnectionDetails(err error) (slog.Level, string) {
	if err == nil {
		return slog.LevelInfo, "completed"
	}
	if IsExpectedConnectionClose(err) {
		return slog.LevelInfo, ConnectionReason(err)
	}
	return slog.LevelWarn, ConnectionReason(err)
}

func IsExpectedConnectionClose(err error) bool {
	if err == nil {
		return true
	}
	if stderrors.Is(err, io.EOF) || stderrors.Is(err, net.ErrClosed) {
		return true
	}

	var netErr net.Error
	return stderrors.As(err, &netErr) && netErr.Timeout()
}

func ConnectionReason(err error) string {
	switch {
	case err == nil:
		return "completed"
	case stderrors.Is(err, io.EOF):
		return "eof"
	case stderrors.Is(err, net.ErrClosed):
		return "closed"
	case stderrors.Is(err, transport.ErrFrameTooSmall):
		return "invalid frame length below minimum"
	case stderrors.Is(err, transport.ErrFrameTooLarge):
		return "invalid frame length above maximum"
	}

	var netErr net.Error
	if stderrors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	var protocolErr *protocol.ProtocolError
	if stderrors.As(err, &protocolErr) {
		return protocolErr.Message
	}

	return err.Error()
}
