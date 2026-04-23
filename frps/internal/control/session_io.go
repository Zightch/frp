package control

import (
	"net"
	"sort"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

const (
	observedRuntimeConnectionKindTCPStream  = "tcp_stream"
	observedRuntimeConnectionKindUDPSession = "udp_session"
)

type sessionRuntimeIOWriter struct {
	server        *Server
	conn          net.Conn
	session       *sessionState
	configVersion uint64
}

type sessionStreamOpenOperation struct {
	blocked   bool
	streamID  uint32
	stream    *publicStream
	openFrame protocol.Frame
}

type sessionUDPDatagramForwardOperation struct {
	blocked    bool
	created    bool
	udpSession *publicUDPSession
	frames     []protocol.Frame
}

type observedSessionRuntimeConnection struct {
	connectionID   uint32
	kind           string
	protocol       string
	tunnelID       uint32
	remotePort     uint16
	clientAddr     string
	openedAtMs     uint64
	lastActiveAtMs uint64
	idleTimeoutMs  uint32
}

func newSessionRuntimeIOWriter(server *Server, conn net.Conn, session *sessionState, configVersion uint64) sessionRuntimeIOWriter {
	return sessionRuntimeIOWriter{
		server:        server,
		conn:          conn,
		session:       session,
		configVersion: configVersion,
	}
}

func (w sessionRuntimeIOWriter) writeFrame(frame protocol.Frame) error {
	if w.server == nil || w.conn == nil || w.session == nil {
		return errRuntimeIOStopped
	}
	return w.server.writeRuntimeFrameWithSession(w.conn, w.session, w.configVersion, frame)
}

func (w sessionRuntimeIOWriter) writeFrames(frames ...protocol.Frame) error {
	if w.server == nil || w.conn == nil || w.session == nil {
		return errRuntimeIOStopped
	}
	return w.server.writeRuntimeFramesWithSession(w.conn, w.session, w.configVersion, frames...)
}

func (s *sessionState) preparePublicStreamOpen(configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, publicConn net.Conn, now time.Time) (sessionStreamOpenOperation, error) {
	streamID := s.nextTunnelStreamID()
	requestID := s.nextRequestID()
	stream := newPublicStream(configVersion, tunnel, remotePort, requestID, publicConn, now)
	if !s.addPublicStream(streamID, stream, configVersion) {
		return sessionStreamOpenOperation{blocked: true}, nil
	}

	body, err := protocol.MarshalStreamOpen(protocol.StreamOpen{
		TunnelID:   tunnel.TunnelID,
		RemotePort: remotePort,
		ClientAddr: stream.clientAddr,
		OpenedAtMs: stream.openedAtMs,
	})
	if err != nil {
		s.closePublicStream(streamID)
		return sessionStreamOpenOperation{}, err
	}

	return sessionStreamOpenOperation{
		streamID: streamID,
		stream:   stream,
		openFrame: protocol.Frame{
			Type:      protocol.TypeStreamOpen,
			RequestID: requestID,
			StreamID:  streamID,
			Body:      body,
		},
	}, nil
}

func (s *sessionState) preparePublicUDPDatagramForward(configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, listener UDPListener, clientAddr *net.UDPAddr, payload []byte, now time.Time) (sessionUDPDatagramForwardOperation, error) {
	udpSession := newPublicUDPSession(s.nextTunnelStreamID(), tunnel, remotePort, listener, clientAddr, now)
	udpSession, created := s.bindPublicUDPSession(udpSession, configVersion)
	if udpSession == nil {
		return sessionUDPDatagramForwardOperation{blocked: true}, nil
	}
	if !created {
		udpSession.touch(now)
		return sessionUDPDatagramForwardOperation{
			udpSession: udpSession,
			frames: []protocol.Frame{
				{
					Type:     protocol.TypeUDPData,
					StreamID: udpSession.sessionID,
					Body:     payload,
				},
			},
		}, nil
	}

	requestID := s.nextRequestID()
	openBody, err := protocol.MarshalUDPOpen(protocol.UDPOpen{
		TunnelID:      tunnel.TunnelID,
		RemotePort:    udpSession.remotePort,
		ClientAddr:    udpSession.clientAddr,
		IdleTimeoutMs: uint32(udpSession.idleTimeout / time.Millisecond),
	})
	if err != nil {
		s.closePublicUDPSession(udpSession.sessionID)
		return sessionUDPDatagramForwardOperation{}, err
	}

	return sessionUDPDatagramForwardOperation{
		created:    true,
		udpSession: udpSession,
		frames: []protocol.Frame{
			{
				Type:      protocol.TypeUDPOpen,
				RequestID: requestID,
				StreamID:  udpSession.sessionID,
				Body:      openBody,
			},
			{
				Type:     protocol.TypeUDPData,
				StreamID: udpSession.sessionID,
				Body:     payload,
			},
		},
	}, nil
}

func observeRuntimeConnections(streams map[uint32]*publicStream, udpSessions map[uint32]*publicUDPSession) []observedSessionRuntimeConnection {
	connections := make([]observedSessionRuntimeConnection, 0, len(streams)+len(udpSessions))
	for streamID, stream := range streams {
		if stream == nil {
			continue
		}
		connections = append(connections, stream.observedConnection(streamID))
	}
	for _, udpSession := range udpSessions {
		if udpSession == nil {
			continue
		}
		connections = append(connections, udpSession.observedConnection())
	}

	sort.Slice(connections, func(i, j int) bool {
		if connections[i].kind == connections[j].kind {
			if connections[i].tunnelID == connections[j].tunnelID {
				return connections[i].connectionID < connections[j].connectionID
			}
			return connections[i].tunnelID < connections[j].tunnelID
		}
		return connections[i].kind < connections[j].kind
	})

	return connections
}

func nonNegativeUnixMilli(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}
