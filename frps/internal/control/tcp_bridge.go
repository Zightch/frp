package control

import (
	"net"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) handlePublicConnection(controlConn net.Conn, logger Logger, session *sessionState, tunnel protocol.TunnelEntry, remotePort uint16, publicConn net.Conn) {
	streamID := session.nextTunnelStreamID()
	requestID := session.nextRequestID()
	stream := &publicStream{
		conn:          publicConn,
		tunnel:        tunnel,
		openRequestID: requestID,
		ready:         make(chan error, 1),
	}

	session.addPublicStream(streamID, stream)

	body, err := protocol.MarshalStreamOpen(protocol.StreamOpen{
		TunnelID:   tunnel.TunnelID,
		RemotePort: remotePort,
		ClientAddr: sockAddrFromNetAddr(publicConn.RemoteAddr()),
		OpenedAtMs: uint64(time.Now().UTC().UnixMilli()),
	})
	if err != nil {
		session.closePublicStream(streamID)
		return
	}

	if err := s.writeFrameWithSession(controlConn, session, protocol.Frame{
		Type:      protocol.TypeStreamOpen,
		RequestID: requestID,
		StreamID:  streamID,
		Body:      body,
	}); err != nil {
		session.closePublicStream(streamID)
		return
	}

	select {
	case openErr := <-stream.ready:
		if openErr != nil {
			logger.Warn("stream open rejected", "stream_id", streamID, "tunnel_id", tunnel.TunnelID, "error", openErr)
			session.closePublicStream(streamID)
			return
		}
	case <-time.After(s.options.WriteTimeout):
		_ = s.sendStreamClose(controlConn, session, streamID, protocol.CloseReasonIdleTimeout, "stream open timeout")
		session.closePublicStream(streamID)
		return
	}

	go s.copyPublicToClient(controlConn, session, streamID, stream)
}
