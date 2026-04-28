package control

import (
	"context"
	"crypto/tls"
	"net"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controllistener "github.com/zightch/frp/frps/internal/control/runtime/listener"
	controllistenertls "github.com/zightch/frp/frps/internal/control/runtime/listener/tls"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

type sessionRuntimeStartTarget struct {
	conn     net.Conn
	logger   Logger
	session  *sessionState
	group    GroupRuntime
	snapshot ConfigSnapshot
}

func (t sessionRuntimeStartTarget) SessionIsDone() bool { return t.session.IsDone() }

func (t sessionRuntimeStartTarget) SessionCanStartTunnelRuntime() bool {
	return t.session.CanStartTunnelRuntime()
}

func (t sessionRuntimeStartTarget) SessionActiveRuntimeTunnelIDs() map[uint32]struct{} {
	return t.session.ActiveRuntimeTunnelIDs()
}

func (t sessionRuntimeStartTarget) SessionResetRuntimeGenerationIfIdle() {
	t.session.resetRuntimeGenerationIfIdle()
}

func (t sessionRuntimeStartTarget) SessionSetRecoveryMode(mode testsupport.RecoveryMode) {
	t.session.SetRecoveryMode(mode)
}

func (t sessionRuntimeStartTarget) SessionAttachTunnelListeners(configVersion uint64, tunnelID uint32, tcpListeners []net.Listener, udpListeners []UDPListener) (bool, bool) {
	return t.session.attachTunnelListeners(configVersion, tunnelID, tcpListeners, udpListeners)
}

func (t sessionRuntimeStartTarget) SessionHasActiveRuntimeListeners() bool {
	return t.session.HasActiveRuntimeListeners()
}

func (t sessionRuntimeStartTarget) Conn() net.Conn                          { return t.conn }
func (t sessionRuntimeStartTarget) Logger() controlruntime.Logger           { return t.logger }
func (t sessionRuntimeStartTarget) Group() controlruntime.GroupRuntime      { return t.group }
func (t sessionRuntimeStartTarget) Snapshot() controlruntime.ConfigSnapshot { return t.snapshot }
func (t sessionRuntimeStartTarget) getSession() *sessionState               { return t.session }

type sessionRuntimeStartPlan = controlruntime.SessionRuntimeStartPlan

type tunnelRuntimeServeContext struct {
	logger     Logger
	session    *sessionState
	runtimeIO  sessionRuntimeIOWriter
	tunnel     protocol.TunnelEntry
	remotePort uint16
}

func newSessionRuntimeStartTarget(conn net.Conn, logger Logger, session *sessionState) sessionRuntimeStartTarget {
	group, snapshot := session.CurrentGroupAndSnapshot()
	return sessionRuntimeStartTarget{
		conn:     conn,
		logger:   logger,
		session:  session,
		group:    group,
		snapshot: snapshot,
	}
}

func (s *Server) activeSession(groupID int64) (*activeSession, bool) {
	return s.supervisor.activeSession(groupID)
}

func (s *Server) ensureTunnelListeners(conn net.Conn, logger Logger, session *sessionState) error {
	target := newSessionRuntimeStartTarget(conn, logger, session)
	plan := controlruntime.PlanSessionRuntimeStart(s, target)
	return controlruntime.ApplySessionRuntimeStartPlan(s, target, plan)
}

func (s *Server) startTunnelListeners(opCtx controlruntime.TunnelListenerOperationContext) (controlruntime.TunnelListenerBatch, error) {
	starter := controllistener.NewStarter(controllistener.StarterOptions{
		Factory: s.listeners,
		TLSLoader: func(ctx context.Context, tunnelID uint32) (*tls.Config, error) {
			return controllistenertls.LoadTunnelListenerTLSConfig(ctx, s.options.Store, tunnelID)
		},
	})
	return starter.StartTunnelListeners(opCtx)
}

func (s *Server) resolveGroupEffectiveIP(group GroupRuntime) (string, error) {
	effectiveIP, err := system.NormalizeListenIP(group.EffectiveIP)
	if err != nil {
		return "", &controlruntime.GroupEffectiveIPStartError{
			EffectiveIP: group.EffectiveIP,
			Kind:        controlruntime.GroupEffectiveIPStartErrorInvalid,
			Cause:       err,
		}
	}
	if system.IsSpecialListenIP(effectiveIP) {
		return effectiveIP, nil
	}
	if s.network != nil && !s.network.Current().HasIP(effectiveIP) {
		return "", &controlruntime.GroupEffectiveIPStartError{
			EffectiveIP: effectiveIP,
			Kind:        controlruntime.GroupEffectiveIPStartErrorNotLocal,
		}
	}
	return effectiveIP, nil
}

func (s *Server) serveTunnelListener(serve tunnelRuntimeServeContext, listener net.Listener) {
	s.tcpHandler().ServeTunnelListener(tcpServeContext(serve), listener)
}

func (s *Server) serveUDPTunnelListener(serve tunnelRuntimeServeContext, listener UDPListener) {
	s.udpHandler().ServeUDPTunnelListener(udpServeContext(serve), listener)
}
