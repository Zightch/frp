package udp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlruntimestate "github.com/zightch/frp/frps/internal/control/runtime/state"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type Session = controlruntimestate.UDPSession

type Clock interface {
	Now() time.Time
}

type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

type UDPListener = controlbind.UDPListener

type SessionState interface {
	NextTunnelStreamID() uint32
	NextRequestID() uint32
	BindPublicUDPSession(udpSession *Session, configVersion uint64) (*Session, bool)
	PublicUDPSession(sessionID uint32) *Session
	ClosePublicUDPSession(sessionID uint32) bool
	TakeIdlePublicUDPSessions(now time.Time) []*Session
	DoneCh() <-chan struct{}
}

type RuntimeFrameWriter interface {
	WriteFrames(frames ...protocol.Frame) error
}

type FrameWriter = controlprotocolerrors.FrameWriter

type RuntimeWriteStoppedFunc func(error) bool

type Handler struct {
	Clock               Clock
	Scheduler           clock.Scheduler
	IdleTimeout         time.Duration
	IdleSweep           time.Duration
	RuntimeWriteStopped RuntimeWriteStoppedFunc
}

type ServeContext struct {
	Logger        Logger
	Session       SessionState
	RuntimeWriter RuntimeFrameWriter
	ControlWriter FrameWriter
	ConfigVersion uint64
	Tunnel        protocol.TunnelEntry
	RemotePort    uint16
}

type DatagramForwardOperation struct {
	Blocked    bool
	Created    bool
	UDPSession *Session
	Frames     []protocol.Frame
}

func (h Handler) ServeUDPTunnelListener(serve ServeContext, listener UDPListener) {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	for {
		n, clientAddr, err := listener.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if serve.Logger != nil {
				serve.Logger.Warn("udp tunnel read failed", "tunnel_id", serve.Tunnel.TunnelID, "error", err)
			}
			continue
		}
		payload := append([]byte(nil), buffer[:n]...)
		if err := h.HandlePublicDatagram(serve, listener, clientAddr, payload); err != nil && serve.Logger != nil {
			serve.Logger.Warn(
				"udp tunnel forward failed",
				"tunnel_id", serve.Tunnel.TunnelID,
				"remote_port", serve.RemotePort,
				"client_addr", clientAddr.String(),
				"error", err,
			)
		}
	}
}

func (h Handler) HandlePublicDatagram(serve ServeContext, listener UDPListener, clientAddr *net.UDPAddr, payload []byte) error {
	forwardOp, err := PrepareDatagramForward(serve.Session, h.idleTimeout(), serve.ConfigVersion, serve.Tunnel, serve.RemotePort, listener, clientAddr, payload, h.now())
	if err != nil {
		return err
	}
	if forwardOp.Blocked {
		return nil
	}
	err = serve.RuntimeWriter.WriteFrames(forwardOp.Frames...)
	if err != nil {
		if forwardOp.Created {
			serve.Session.ClosePublicUDPSession(forwardOp.UDPSession.SessionID)
		}
		if h.RuntimeWriteStopped != nil && h.RuntimeWriteStopped(err) {
			return nil
		}
		return err
	}

	if forwardOp.Created && serve.Logger != nil {
		serve.Logger.Info("udp session opened", "session_id", forwardOp.UDPSession.SessionID, "tunnel_id", serve.Tunnel.TunnelID, "client_addr", clientAddr.String())
	}
	return nil
}

func (h Handler) HandleUDPData(writer FrameWriter, session SessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return controlprotocolerrors.ReplyError(writer, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "udp.data requestId must be zero")
	}
	if frame.StreamID == 0 {
		return controlprotocolerrors.ReplyError(writer, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "udp.data streamId must be non-zero")
	}
	if len(frame.Body) > protocol.MaxDataBodyLen {
		return controlprotocolerrors.ReplyError(writer, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "udp.data body exceeds %d bytes", protocol.MaxDataBodyLen)
	}

	udpSession := session.PublicUDPSession(frame.StreamID)
	if udpSession == nil {
		return SendUDPClose(writer, frame.StreamID, protocol.CloseReasonProtocolError, "udp session not found")
	}

	if _, err := udpSession.Listener.WriteToUDP(frame.Body, udpSession.PublicAddr); err != nil {
		if session.ClosePublicUDPSession(frame.StreamID) {
			return SendUDPClose(writer, frame.StreamID, protocol.CloseReasonWriteError, err.Error())
		}
		return nil
	}
	udpSession.Touch(h.now())
	return nil
}

func (h Handler) HandleUDPClose(session SessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return fmt.Errorf("udp.close requestId must be zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("udp.close streamId must be non-zero")
	}
	if _, err := protocol.UnmarshalUDPClose(frame.Body); err != nil {
		return err
	}
	session.ClosePublicUDPSession(frame.StreamID)
	return nil
}

func (h Handler) ServeIdleCleanup(parent context.Context, writer FrameWriter, logger Logger, session SessionState) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	go func() {
		<-session.DoneCh()
		cancel()
	}()

	task := h.Scheduler.Every(ctx, "control.udp_idle_cleanup", h.idleSweep(), func(ctx context.Context, now time.Time) {
		if err := h.CleanupIdleSessions(writer, logger, session, now.UTC()); err != nil && logger != nil {
			logger.Warn("udp session idle cleanup failed", "error", err)
		}
	})
	<-task.Done()
}

func (h Handler) CleanupIdleSessions(writer FrameWriter, logger Logger, session SessionState, now time.Time) error {
	idleSessions := session.TakeIdlePublicUDPSessions(now)
	for _, udpSession := range idleSessions {
		if err := SendUDPClose(writer, udpSession.SessionID, protocol.CloseReasonIdleTimeout, "udp session idle timeout"); err != nil {
			return err
		}
		if logger != nil {
			logger.Info(
				"udp session closed for idle timeout",
				"session_id", udpSession.SessionID,
				"tunnel_id", udpSession.TunnelID,
				"client_addr", udpSession.PublicAddr.String(),
			)
		}
	}
	return nil
}

func PrepareDatagramForward(session SessionState, idleTimeout time.Duration, configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, listener UDPListener, clientAddr *net.UDPAddr, payload []byte, now time.Time) (DatagramForwardOperation, error) {
	udpSession := NewPublicSession(session.NextTunnelStreamID(), tunnel, remotePort, listener, clientAddr, idleTimeout, now)
	udpSession, created := session.BindPublicUDPSession(udpSession, configVersion)
	if udpSession == nil {
		return DatagramForwardOperation{Blocked: true}, nil
	}
	if !created {
		udpSession.Touch(now)
		return DatagramForwardOperation{
			UDPSession: udpSession,
			Frames: []protocol.Frame{
				{
					Type:     protocol.TypeUDPData,
					StreamID: udpSession.SessionID,
					Body:     payload,
				},
			},
		}, nil
	}

	requestID := session.NextRequestID()
	openBody, err := protocol.MarshalUDPOpen(protocol.UDPOpen{
		TunnelID:      tunnel.TunnelID,
		RemotePort:    udpSession.RemotePort,
		ClientAddr:    udpSession.ClientAddr,
		IdleTimeoutMs: uint32(udpSession.IdleTimeout / time.Millisecond),
	})
	if err != nil {
		session.ClosePublicUDPSession(udpSession.SessionID)
		return DatagramForwardOperation{}, err
	}

	return DatagramForwardOperation{
		Created:    true,
		UDPSession: udpSession,
		Frames: []protocol.Frame{
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

func SendUDPClose(writer FrameWriter, sessionID uint32, reasonCode uint16, message string) error {
	body, err := protocol.MarshalUDPClose(protocol.UDPClose{
		ReasonCode: reasonCode,
		Initiator:  protocol.InitiatorFRPS,
		Message:    message,
	})
	if err != nil {
		return err
	}
	return writer.WriteFrame(protocol.Frame{
		Type:     protocol.TypeUDPClose,
		StreamID: sessionID,
		Body:     body,
	})
}

func NewPublicSession(sessionID uint32, tunnel protocol.TunnelEntry, remotePort uint16, listener UDPListener, clientAddr *net.UDPAddr, idleTimeout time.Duration, now time.Time) *Session {
	udpSession := &Session{
		SessionID:   sessionID,
		TunnelID:    tunnel.TunnelID,
		RemotePort:  remotePort,
		ClientAddr:  SockAddrFromUDPAddr(clientAddr),
		PublicAddr:  CloneUDPAddr(clientAddr),
		Listener:    listener,
		OpenedAtMs:  uint64(now.UTC().UnixMilli()),
		IdleTimeout: idleTimeout,
	}
	udpSession.Touch(now)
	return udpSession
}

func CloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	return &net.UDPAddr{
		IP:   append(net.IP(nil), addr.IP...),
		Port: addr.Port,
		Zone: addr.Zone,
	}
}

func SockAddrFromUDPAddr(addr *net.UDPAddr) protocol.SockAddr {
	if addr == nil {
		return protocol.SockAddr{}
	}
	return protocol.SockAddr{IP: append(net.IP(nil), addr.IP...), Port: uint16(addr.Port)}
}

func (h Handler) now() time.Time {
	if h.Clock == nil {
		return time.Now().UTC()
	}
	return h.Clock.Now()
}

func (h Handler) idleTimeout() time.Duration {
	if h.IdleTimeout > 0 {
		return h.IdleTimeout
	}
	return 30 * time.Second
}

func (h Handler) idleSweep() time.Duration {
	if h.IdleSweep > 0 {
		return h.IdleSweep
	}
	return time.Second
}
