package client

import (
	"fmt"
	"net"
	"sync"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type localStream struct {
	conn      net.Conn
	closeOnce sync.Once
}

func (c *Client) handleStreamOpen(conn net.Conn, state *sessionState, frame protocol.Frame) error {
	return c.handleWorkStreamOpen(conn, state, frame)
}

func (c *Client) prepareLocalTCPStream(state *sessionState, open protocol.StreamOpen) (protocol.TunnelEntry, *localStream, uint16, error) {
	target, err := state.localTarget(open)
	if err != nil {
		return protocol.TunnelEntry{}, nil, protocol.ErrorCodeStreamTunnelNotFound, err
	}

	tunnel, ok := state.tunnelByID(open.TunnelID)
	if !ok {
		return protocol.TunnelEntry{}, nil, protocol.ErrorCodeStreamTunnelNotFound, fmt.Errorf("tunnel %d not found", open.TunnelID)
	}

	localConn, err := dialTunnelBackend(tunnel, target)
	if err != nil {
		c.logBackendDialFailure(tunnel, target, err)
		return tunnel, nil, protocol.ErrorCodeStreamLocalDialFailed, err
	}
	c.clearBackendDialFailure(tunnel, target)
	return tunnel, &localStream{conn: localConn}, 0, nil
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
