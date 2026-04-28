package wiring

import (
	"context"
	"errors"
	"net"
	"time"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controltcp "github.com/zightch/frp/frps/internal/control/runtime/serve/tcp"
	controludp "github.com/zightch/frp/frps/internal/control/runtime/serve/udp"
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

type publicStream = controlruntime.Stream

type publicUDPSession = controlruntime.UDPSession

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

func (w sessionRuntimeIOWriter) controlFrameWriter() sessionFrameWriter {
	return sessionFrameWriter{
		server:  w.server,
		conn:    w.conn,
		session: w.session,
	}
}

func (w sessionRuntimeIOWriter) WriteFrame(frame protocol.Frame) error {
	return w.writeFrame(frame)
}

func (w sessionRuntimeIOWriter) WriteFrames(frames ...protocol.Frame) error {
	return w.writeFrames(frames...)
}

func (s *sessionState) preparePublicStreamOpen(configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, publicConn net.Conn, now time.Time) (sessionStreamOpenOperation, error) {
	op, err := controltcp.PreparePublicStreamOpen(s, configVersion, tunnel, remotePort, publicConn, now)
	if err != nil {
		return sessionStreamOpenOperation{}, err
	}
	return sessionStreamOpenOperation{
		blocked:   op.Blocked,
		streamID:  op.StreamID,
		stream:    op.Stream,
		openFrame: op.OpenFrame,
	}, nil
}

func (s *sessionState) preparePublicUDPDatagramForward(configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, listener UDPListener, clientAddr *net.UDPAddr, payload []byte, now time.Time) (sessionUDPDatagramForwardOperation, error) {
	op, err := controludp.PrepareDatagramForward(s, defaultUDPIdleTimeout, configVersion, tunnel, remotePort, listener, clientAddr, payload, now)
	if err != nil {
		return sessionUDPDatagramForwardOperation{}, err
	}
	return sessionUDPDatagramForwardOperation{
		blocked:    op.Blocked,
		created:    op.Created,
		udpSession: op.UDPSession,
		frames:     op.Frames,
	}, nil
}

func (s *Server) handlePublicConnection(serve tunnelRuntimeServeContext, publicConn net.Conn) {
	s.tcpHandler().HandlePublicConnection(tcpServeContext(serve), publicConn)
}

func (s *Server) handleStreamOpened(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	return s.tcpHandler().HandleStreamOpened(s.sessionFrameWriter(conn, session), session, frame)
}

func (s *Server) handleStreamData(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	return s.tcpHandler().HandleStreamData(s.sessionFrameWriter(conn, session), session, frame)
}

func (s *Server) handleStreamClose(session *sessionState, frame protocol.Frame) error {
	return s.tcpHandler().HandleStreamClose(session, frame)
}

func (s *Server) sendStreamClose(conn net.Conn, session *sessionState, streamID uint32, reasonCode uint16, message string) error {
	return controltcp.SendStreamClose(s.sessionFrameWriter(conn, session), streamID, reasonCode, message)
}

func (s *Server) copyPublicToClient(runtimeIO sessionRuntimeIOWriter, streamID uint32, stream *publicStream) {
	s.tcpHandler().CopyPublicToClient(runtimeIO, runtimeIO.controlFrameWriter(), runtimeIO.session, streamID, stream)
}

func (s *Server) tcpHandler() controltcp.Handler {
	return controltcp.Handler{
		Clock:        s.clock,
		WriteTimeout: s.options.WriteTimeout,
		RuntimeWriteStopped: func(err error) bool {
			return errors.Is(err, errRuntimeIOStopped)
		},
	}
}

func tcpServeContext(serve tunnelRuntimeServeContext) controltcp.ServeContext {
	return controltcp.ServeContext{
		Logger:        serve.logger,
		Session:       serve.session,
		RuntimeWriter: serve.runtimeIO,
		ControlWriter: serve.runtimeIO.controlFrameWriter(),
		ConfigVersion: serve.runtimeIO.configVersion,
		Tunnel:        serve.tunnel,
		RemotePort:    serve.remotePort,
	}
}

func (s *Server) udpHandler() controludp.Handler {
	return controludp.Handler{
		Clock:       s.clock,
		Scheduler:   s.scheduler,
		IdleTimeout: defaultUDPIdleTimeout,
		IdleSweep:   defaultUDPIdleSweep,
		RuntimeWriteStopped: func(err error) bool {
			return errors.Is(err, errRuntimeIOStopped)
		},
	}
}

func udpServeContext(serve tunnelRuntimeServeContext) controludp.ServeContext {
	return controludp.ServeContext{
		Logger:        serve.logger,
		Session:       serve.session,
		RuntimeWriter: serve.runtimeIO,
		ControlWriter: serve.runtimeIO.controlFrameWriter(),
		ConfigVersion: serve.runtimeIO.configVersion,
		Tunnel:        serve.tunnel,
		RemotePort:    serve.remotePort,
	}
}

func (s *Server) handleUDPData(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	return s.udpHandler().HandleUDPData(s.sessionFrameWriter(conn, session), session, frame)
}

func (s *Server) handleUDPClose(session *sessionState, frame protocol.Frame) error {
	return s.udpHandler().HandleUDPClose(session, frame)
}

func (s *Server) sendUDPClose(conn net.Conn, session *sessionState, sessionID uint32, reasonCode uint16, message string) error {
	return controludp.SendUDPClose(s.sessionFrameWriter(conn, session), sessionID, reasonCode, message)
}

func (s *Server) serveUDPIdleCleanup(conn net.Conn, logger Logger, session *sessionState) {
	s.udpHandler().ServeIdleCleanup(context.Background(), s.sessionFrameWriter(conn, session), logger, session)
}

func (s *Server) cleanupIdlePublicUDPSessions(conn net.Conn, logger Logger, session *sessionState, now time.Time) error {
	return s.udpHandler().CleanupIdleSessions(s.sessionFrameWriter(conn, session), logger, session, now)
}

func (s *Server) handlePublicUDPDatagram(serve tunnelRuntimeServeContext, listener UDPListener, clientAddr *net.UDPAddr, payload []byte) error {
	return s.udpHandler().HandlePublicDatagram(udpServeContext(serve), listener, clientAddr, payload)
}

func newPublicUDPSession(sessionID uint32, tunnel protocol.TunnelEntry, remotePort uint16, listener UDPListener, clientAddr *net.UDPAddr, now time.Time) *publicUDPSession {
	return controludp.NewPublicSession(sessionID, tunnel, remotePort, listener, clientAddr, defaultUDPIdleTimeout, now)
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	return controludp.CloneUDPAddr(addr)
}

func sockAddrFromNetAddr(addr net.Addr) protocol.SockAddr {
	return controltcp.SockAddrFromNetAddr(addr)
}

func sockAddrString(addr protocol.SockAddr) string {
	return controltcp.SockAddrString(addr)
}
