package runtime

import (
	"net"

	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

type ObservedListener struct {
	TunnelID uint32
	Protocol string
	BindIP   string
	Port     uint16
}

type ObservedConnection struct {
	ConnectionID   uint32
	Kind           string
	Protocol       string
	TunnelID       uint32
	RemotePort     uint16
	ClientAddr     string
	OpenedAtMs     uint64
	LastActiveAtMs uint64
	IdleTimeoutMs  uint32
}

type ObservedState struct {
	Frozen                bool
	ListenersStarted      bool
	Generation            uint64
	ActiveTunnelIDs       map[uint32]struct{}
	AttachedListeners     []ObservedListener
	ActiveStreamCount     uint32
	ActiveUDPSessionCount uint32
	Connections           []ObservedConnection
}

type SessionSnapshot struct {
	GroupID      int64
	SessionID    uint64
	Conn         net.Conn
	DesiredGroup GroupRuntime
	RecoveryMode testsupport.RecoveryMode
	State        controlsession.SessionState
	Runtime      ObservedState
}
