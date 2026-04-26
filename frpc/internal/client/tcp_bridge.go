package client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type localStream struct {
	conn      net.Conn
	closeOnce sync.Once
}

func (c *Client) handleStreamOpen(conn net.Conn, state *sessionState, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return fmt.Errorf("stream.open requestId must be non-zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("stream.open streamId must be non-zero")
	}

	open, err := protocol.UnmarshalStreamOpen(frame.Body)
	if err != nil {
		return err
	}

	target, err := state.localTarget(open)
	if err != nil {
		return c.replyStreamOpened(conn, state, frame, protocol.StreamOpened{
			Status:    protocol.StatusError,
			ErrorCode: protocol.ErrorCodeStreamTunnelNotFound,
			Message:   err.Error(),
		})
	}

	tunnel, ok := state.tunnelByID(open.TunnelID)
	if !ok {
		return c.replyStreamOpened(conn, state, frame, protocol.StreamOpened{
			Status:    protocol.StatusError,
			ErrorCode: protocol.ErrorCodeStreamTunnelNotFound,
			Message:   fmt.Sprintf("tunnel %d not found", open.TunnelID),
		})
	}

	localConn, err := dialTunnelBackend(tunnel, target)
	if err != nil {
		return c.replyStreamOpened(conn, state, frame, protocol.StreamOpened{
			Status:    protocol.StatusError,
			ErrorCode: protocol.ErrorCodeStreamLocalDialFailed,
			Message:   err.Error(),
		})
	}

	stream := &localStream{conn: localConn}
	if !state.addStream(frame.StreamID, stream) {
		_ = localConn.Close()
		return c.replyStreamOpened(conn, state, frame, protocol.StreamOpened{
			Status:    protocol.StatusError,
			ErrorCode: protocol.ErrorCodeProtocolBadBody,
			Message:   fmt.Sprintf("stream %d already exists", frame.StreamID),
		})
	}

	if err := c.replyStreamOpened(conn, state, frame, protocol.StreamOpened{Status: protocol.StatusOK}); err != nil {
		state.closeStream(frame.StreamID)
		return err
	}

	go c.copyLocalToServer(conn, state, frame.StreamID, stream)
	return nil
}

func (c *Client) handleStreamData(conn net.Conn, state *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return fmt.Errorf("stream.data requestId must be zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("stream.data streamId must be non-zero")
	}

	stream := state.stream(frame.StreamID)
	if stream == nil {
		return c.sendStreamClose(conn, state, frame.StreamID, protocol.CloseReasonProtocolError, "stream not found")
	}

	if err := writeConnFull(stream.conn, frame.Body); err != nil {
		if state.closeStream(frame.StreamID) {
			return c.sendStreamClose(conn, state, frame.StreamID, protocol.CloseReasonWriteError, err.Error())
		}
	}
	return nil
}

func (c *Client) handleStreamClose(state *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return fmt.Errorf("stream.close requestId must be zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("stream.close streamId must be non-zero")
	}

	if _, err := protocol.UnmarshalStreamClose(frame.Body); err != nil {
		return err
	}
	state.closeStream(frame.StreamID)
	return nil
}

func (c *Client) replyStreamOpened(conn net.Conn, state *sessionState, frame protocol.Frame, opened protocol.StreamOpened) error {
	body, err := protocol.MarshalStreamOpened(opened)
	if err != nil {
		return err
	}
	return c.writeMessage(conn, &state.writeMu, protocol.Frame{
		Type:      protocol.TypeStreamOpened,
		RequestID: frame.RequestID,
		StreamID:  frame.StreamID,
		Body:      body,
	})
}

func (c *Client) sendStreamClose(conn net.Conn, state *sessionState, streamID uint32, reasonCode uint16, message string) error {
	body, err := protocol.MarshalStreamClose(protocol.StreamClose{
		ReasonCode: reasonCode,
		Initiator:  protocol.InitiatorFRPC,
		Message:    message,
	})
	if err != nil {
		return err
	}
	return c.writeMessage(conn, &state.writeMu, protocol.Frame{
		Type:     protocol.TypeStreamClose,
		StreamID: streamID,
		Body:     body,
	})
}

func (c *Client) copyLocalToServer(conn net.Conn, state *sessionState, streamID uint32, stream *localStream) {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	for {
		n, err := stream.conn.Read(buffer)
		if n > 0 {
			payload := append([]byte(nil), buffer[:n]...)
			writeErr := c.writeMessage(conn, &state.writeMu, protocol.Frame{
				Type:     protocol.TypeStreamData,
				StreamID: streamID,
				Body:     payload,
			})
			if writeErr != nil {
				state.closeStream(streamID)
				return
			}
		}

		if err == nil {
			continue
		}

		reasonCode := protocol.CloseReasonReadError
		message := err.Error()
		if errors.Is(err, io.EOF) {
			reasonCode = protocol.CloseReasonEOF
			message = "eof"
		}

		if state.closeStream(streamID) {
			_ = c.sendStreamClose(conn, state, streamID, reasonCode, message)
		}
		return
	}
}

func (s *sessionState) addStream(streamID uint32, stream *localStream) bool {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()

	if _, exists := s.streams[streamID]; exists {
		return false
	}
	s.streams[streamID] = stream
	s.activeStreams.Add(1)
	return true
}

func (s *sessionState) stream(streamID uint32) *localStream {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.streams[streamID]
}

func (s *sessionState) closeStream(streamID uint32) bool {
	s.streamMu.Lock()
	stream, ok := s.streams[streamID]
	if ok {
		delete(s.streams, streamID)
		s.activeStreams.Add(^uint32(0))
	}
	s.streamMu.Unlock()

	if !ok {
		return false
	}

	stream.close()
	return true
}

func (s *sessionState) closeAllStreams() int {
	s.streamMu.Lock()
	streams := make([]*localStream, 0, len(s.streams))
	for streamID, stream := range s.streams {
		delete(s.streams, streamID)
		streams = append(streams, stream)
	}
	s.activeStreams.Store(0)
	s.streamMu.Unlock()

	for _, stream := range streams {
		stream.close()
	}
	return len(streams)
}

func (s *localStream) close() {
	s.closeOnce.Do(func() {
		_ = s.conn.Close()
	})
}

func writeConnFull(conn net.Conn, payload []byte) error {
	for len(payload) > 0 {
		n, err := conn.Write(payload)
		if err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}
