package wiring

import (
	"time"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
)

// sessionState is the root assembly wrapper around concrete session runtime state.
type sessionState struct {
	*controlruntime.ConcreteSessionState
}

func newSessionState(id uint64, group GroupRuntime, snapshot ConfigSnapshot, readTimeout time.Duration) *sessionState {
	return &sessionState{
		ConcreteSessionState: controlruntime.NewConcreteSessionState(id, group, snapshot, readTimeout),
	}
}
