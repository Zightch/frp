package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controltcp "github.com/zightch/frp/frps/internal/control/runtime/serve/tcp"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

func (s *Server) registerConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeConn[conn] = struct{}{}
}

func (s *Server) unregisterConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeConn, conn)
}

func logConnection(logger *slog.Logger, level slog.Level, message string, err error) {
	if controlprotocolerrors.IsExpectedConnectionClose(err) {
		logger.Log(context.Background(), slog.LevelInfo, message, "error", err)
		return
	}
	logger.Log(context.Background(), level, message, "error", err)
}

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

// WriteFrame implements controlruntime.RuntimeIOWriter.
func (w sessionRuntimeIOWriter) WriteFrame(frame protocol.Frame) error {
	return w.writeFrame(frame)
}

// WriteFrames implements controlruntime.RuntimeIOWriter.
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
	udpSession := newPublicUDPSession(s.NextTunnelStreamID(), tunnel, remotePort, listener, clientAddr, now)
	udpSession, created := s.bindPublicUDPSession(udpSession, configVersion)
	if udpSession == nil {
		return sessionUDPDatagramForwardOperation{blocked: true}, nil
	}
	if !created {
		udpSession.Touch(now)
		return sessionUDPDatagramForwardOperation{
			udpSession: udpSession,
			frames: []protocol.Frame{
				{
					Type:     protocol.TypeUDPData,
					StreamID: udpSession.SessionID,
					Body:     payload,
				},
			},
		}, nil
	}

	requestID := s.NextRequestID()
	openBody, err := protocol.MarshalUDPOpen(protocol.UDPOpen{
		TunnelID:      tunnel.TunnelID,
		RemotePort:    udpSession.RemotePort,
		ClientAddr:    udpSession.ClientAddr,
		IdleTimeoutMs: uint32(udpSession.IdleTimeout / time.Millisecond),
	})
	if err != nil {
		s.closePublicUDPSession(udpSession.SessionID)
		return sessionUDPDatagramForwardOperation{}, err
	}

	return sessionUDPDatagramForwardOperation{
		created:    true,
		udpSession: udpSession,
		frames: []protocol.Frame{
			{
				Type:      protocol.TypeUDPOpen,
				RequestID: requestID,
				StreamID:  udpSession.SessionID,
				Body:      openBody,
			},
			{
				Type:     protocol.TypeUDPData,
				StreamID: udpSession.SessionID,
				Body:     payload,
			},
		},
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

func (s *Server) handleUDPData(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "udp.data requestId must be zero")
	}
	if frame.StreamID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "udp.data streamId must be non-zero")
	}
	if len(frame.Body) > protocol.MaxDataBodyLen {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "udp.data body exceeds %d bytes", protocol.MaxDataBodyLen)
	}

	udpSession := session.publicUDPSession(frame.StreamID)
	if udpSession == nil {
		return s.sendUDPClose(conn, session, frame.StreamID, protocol.CloseReasonProtocolError, "udp session not found")
	}

	if _, err := udpSession.Listener.WriteToUDP(frame.Body, udpSession.PublicAddr); err != nil {
		if session.closePublicUDPSession(frame.StreamID) {
			return s.sendUDPClose(conn, session, frame.StreamID, protocol.CloseReasonWriteError, err.Error())
		}
		return nil
	}
	udpSession.Touch(s.clock.Now())
	return nil
}

func (s *Server) handleUDPClose(session *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return fmt.Errorf("udp.close requestId must be zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("udp.close streamId must be non-zero")
	}
	if _, err := protocol.UnmarshalUDPClose(frame.Body); err != nil {
		return err
	}
	session.closePublicUDPSession(frame.StreamID)
	return nil
}

func (s *Server) sendUDPClose(conn net.Conn, session *sessionState, sessionID uint32, reasonCode uint16, message string) error {
	body, err := protocol.MarshalUDPClose(protocol.UDPClose{
		ReasonCode: reasonCode,
		Initiator:  protocol.InitiatorFRPS,
		Message:    message,
	})
	if err != nil {
		return err
	}
	return s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:     protocol.TypeUDPClose,
		StreamID: sessionID,
		Body:     body,
	})
}

func (s *Server) serveUDPIdleCleanup(conn net.Conn, logger Logger, session *sessionState) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-session.DoneCh()
		cancel()
	}()

	task := s.scheduler.Every(ctx, "control.udp_idle_cleanup", defaultUDPIdleSweep, func(ctx context.Context, now time.Time) {
		if err := s.cleanupIdlePublicUDPSessions(conn, logger, session, now.UTC()); err != nil {
			logger.Warn("udp session idle cleanup failed", "error", err)
		}
	})
	<-task.Done()
}

func (s *Server) cleanupIdlePublicUDPSessions(conn net.Conn, logger Logger, session *sessionState, now time.Time) error {
	idleSessions := session.takeIdlePublicUDPSessions(now)
	for _, udpSession := range idleSessions {
		if err := s.sendUDPClose(conn, session, udpSession.SessionID, protocol.CloseReasonIdleTimeout, "udp session idle timeout"); err != nil {
			return err
		}
		logger.Info(
			"udp session closed for idle timeout",
			"session_id", udpSession.SessionID,
			"tunnel_id", udpSession.TunnelID,
			"client_addr", udpSession.PublicAddr.String(),
		)
	}
	return nil
}

func (s *Server) handlePublicUDPDatagram(serve tunnelRuntimeServeContext, listener UDPListener, clientAddr *net.UDPAddr, payload []byte) error {
	now := s.clock.Now()
	forwardOp, err := serve.session.preparePublicUDPDatagramForward(serve.runtimeIO.configVersion, serve.tunnel, serve.remotePort, listener, clientAddr, payload, now)
	if err != nil {
		return err
	}
	if forwardOp.blocked {
		return nil
	}
	err = serve.runtimeIO.writeFrames(forwardOp.frames...)
	if err != nil {
		if forwardOp.created {
			serve.session.closePublicUDPSession(forwardOp.udpSession.SessionID)
		}
		if errors.Is(err, errRuntimeIOStopped) {
			return nil
		}
		return err
	}

	if forwardOp.created {
		serve.logger.Info("udp session opened", "session_id", forwardOp.udpSession.SessionID, "tunnel_id", serve.tunnel.TunnelID, "client_addr", clientAddr.String())
	}
	return nil
}

func (s *sessionState) bindPublicUDPSession(udpSession *publicUDPSession, configVersion uint64) (*publicUDPSession, bool) {
	return s.BindPublicUDPSession(udpSession, configVersion)
}

func (s *sessionState) publicUDPSession(sessionID uint32) *publicUDPSession {
	return s.PublicUDPSession(sessionID)
}

func (s *sessionState) closePublicUDPSession(sessionID uint32) bool {
	return s.ClosePublicUDPSession(sessionID)
}

func (s *sessionState) takeIdlePublicUDPSessions(now time.Time) []*publicUDPSession {
	return s.TakeIdlePublicUDPSessions(now)
}

func newPublicUDPSession(sessionID uint32, tunnel protocol.TunnelEntry, remotePort uint16, listener UDPListener, clientAddr *net.UDPAddr, now time.Time) *publicUDPSession {
	udpSession := &publicUDPSession{
		SessionID:   sessionID,
		TunnelID:    tunnel.TunnelID,
		RemotePort:  remotePort,
		ClientAddr:  sockAddrFromNetAddr(clientAddr),
		PublicAddr:  cloneUDPAddr(clientAddr),
		Listener:    listener,
		OpenedAtMs:  uint64(now.UTC().UnixMilli()),
		IdleTimeout: defaultUDPIdleTimeout,
	}
	udpSession.Touch(now)
	return udpSession
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	return &net.UDPAddr{
		IP:   append(net.IP(nil), addr.IP...),
		Port: addr.Port,
		Zone: addr.Zone,
	}
}

func sockAddrFromNetAddr(addr net.Addr) protocol.SockAddr {
	return controltcp.SockAddrFromNetAddr(addr)
}

func sockAddrString(addr protocol.SockAddr) string {
	return controltcp.SockAddrString(addr)
}
