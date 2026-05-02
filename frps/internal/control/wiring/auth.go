package wiring

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	controlauth "github.com/zightch/frp/frps/internal/control/protocol/auth"
	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) authenticate(conn net.Conn, expectedClientID [16]byte, logger *slog.Logger) (*sessionState, *controlsession.Agent, error) {
	result, err := controlauth.Authenticate(controlauth.AuthenticateOptions{
		Conn:             conn,
		ExpectedClientID: expectedClientID,
		Reader:           controlauth.FrameReaderFunc(s.readFrame),
		Writer:           s.frameWriter(conn, nil),
		Repository:       s.repo,
		Challenges:       s.authChallenges,
		ReadTimeout:      s.options.ReadTimeout,
	})
	if err != nil {
		return nil, nil, err
	}
	group := result.Group
	sessionID := s.nextSessionID.Add(1)
	remoteIP := connectionRemoteIP(conn)
	if !s.supervisor.ReserveGroupSlotWithRemoteIP(group.ID, sessionID, remoteIP) {
		message := "other frpc already online"
		if occupiedIP := s.supervisor.GroupSlotRemoteIP(group.ID); occupiedIP != "" {
			message = fmt.Sprintf("other frpc already online ip=%s", occupiedIP)
		}
		return nil, nil, controlprotocolerrors.ReplyError(
			s.frameWriter(conn, nil),
			result.FinishRequestID,
			0,
			protocol.ErrorCodeAuthClientLimitReached,
			"%s",
			message,
		)
	}

	session := newSessionState(
		sessionID,
		group,
		controlruntime.RuntimeSnapshotForGroup(group),
		controlruntime.SessionReadTimeout(s.options.HeartbeatInterval, s.options.ReadTimeout),
	)
	session.SetSharedRateLimitStore(s.sharedRate)
	initial := controlsession.NewState(group.ID, session.ID)
	desired := controlruntime.DesiredRuntimeFromGroup(group)
	initial.Desired = &desired

	runtime := &runtimeExecutor{
		groupID: group.ID,
		conn:    conn,
		logger:  logger,
		session: session,
	}
	agent := s.supervisor.AttachSession(context.Background(), initial, runtime)
	if agent == nil {
		s.supervisor.ReleaseGroupSlot(group.ID, sessionID)
		return nil, nil, fmt.Errorf("control supervisor is unavailable")
	}
	attachEvent := controlsession.SessionAttached{
		ConnID:         conn.RemoteAddr().String(),
		HelloRequestID: result.FinishRequestID,
	}
	if !agent.Enqueue(attachEvent) {
		s.supervisor.DetachRuntime(session.ID)
		return nil, nil, fmt.Errorf("control session attach failed")
	}

	return session, agent, nil
}

func connectionRemoteIP(conn net.Conn) string {
	if conn == nil {
		return ""
	}
	return remoteAddrIP(conn.RemoteAddr())
}

func remoteAddrIP(addr net.Addr) string {
	switch typed := addr.(type) {
	case *net.TCPAddr:
		if typed.IP != nil {
			return typed.IP.String()
		}
	case *net.UDPAddr:
		if typed.IP != nil {
			return typed.IP.String()
		}
	}
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err == nil {
		return host
	}
	return addr.String()
}
