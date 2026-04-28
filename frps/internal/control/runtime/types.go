package runtime

import (
	"time"

	controlruntimestate "github.com/zightch/frp/frps/internal/control/runtime/state"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

// InitialServerRequestID is the initial server request ID constant.
const InitialServerRequestID = controlruntimestate.InitialServerRequestID

// ConfigPushOperation represents a pending config push operation.
type ConfigPushOperation struct {
	RequestID    uint32
	Group        GroupRuntime
	Snapshot     ConfigSnapshot
	RecoveryMode testsupport.RecoveryMode
}

// ConfigApplyResult represents the result of applying a config.
type ConfigApplyResult struct {
	Group        GroupRuntime
	Snapshot     ConfigSnapshot
	RecoveryMode testsupport.RecoveryMode
}

type (
	ListenerState        = controlruntimestate.ListenerState
	UDPState             = controlruntimestate.UDPState
	RuntimeState         = controlruntimestate.RuntimeState
	ConcreteSessionState = controlruntimestate.ConcreteSessionState
	ObservedConfigState  = controlruntimestate.ObservedConfigState
	Stream               = controlruntimestate.Stream
	UDPSession           = controlruntimestate.UDPSession
)

// SockAddrString returns the string representation of a SockAddr.
func SockAddrString(addr protocol.SockAddr) string {
	return controlruntimestate.SockAddrString(addr)
}

// NonNegativeUnixMilli returns the unix milliseconds, or 0 if negative.
func NonNegativeUnixMilli(ms int64) uint64 {
	return controlruntimestate.NonNegativeUnixMilli(ms)
}

// NewConcreteSessionState creates a new concrete session state.
func NewConcreteSessionState(id uint64, group GroupRuntime, snapshot ConfigSnapshot, readTimeout time.Duration) *ConcreteSessionState {
	return controlruntimestate.NewConcreteSessionState(id, group, snapshot, readTimeout)
}
