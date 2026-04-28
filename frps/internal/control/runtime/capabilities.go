package runtime

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	"github.com/zightch/frp/frps/pkg/protocol"
)

// RuntimeReadiness exposes only server shutdown state.
type RuntimeReadiness interface {
	IsShuttingDown() bool
}

// RuntimeIssueWriter records and applies runtime issue projections.
type RuntimeIssueWriter interface {
	RecordTunnelRuntimeIssueForConfig(tunnelID uint32, configVersion uint64, reason string)
	ClearUnknownTunnelRuntimeIssues(knownTunnelIDs map[int64]struct{})
	ApplyScannedTunnelRuntimeIssues(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{})
}

// RuntimeIPResolver resolves the effective bind IP for a runtime group.
type RuntimeIPResolver interface {
	ResolveGroupEffectiveIP(group GroupRuntime) (string, error)
}

// ListenerStarter starts and probes public tunnel listeners.
type ListenerStarter interface {
	StartTunnelListeners(opCtx TunnelListenerOperationContext) (TunnelListenerBatch, error)
	ProbeTunnelRuntimeIssue(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string
}

// RuntimeListenerProbe is the read-only listener capability used by scans.
type RuntimeListenerProbe interface {
	ProbeTunnelRuntimeIssue(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string
}

// RuntimeSessionRegistry exposes active session and runtime snapshots.
type RuntimeSessionRegistry interface {
	ActiveSession(groupID int64) (*ActiveSession, bool)
	ActiveRuntimeGroups(exclude SessionStateProjectionTarget) []RuntimeGroupSnapshot
	RuntimeSnapshotIndex() RuntimeSnapshotIndex
	SessionState(sessionID uint64) (SessionState, bool)
	DispatchBySessionID(sessionID uint64, event Event) bool
	ApplySessionEvent(sessionID uint64, event Event)
}

// RuntimeRecoveryCoordinator owns recovery requests that mutate live sessions.
type RuntimeRecoveryCoordinator interface {
	RequestAuditedSessionRuntimeRecovery(sessionID uint64, targetTunnels []protocol.TunnelEntry) error
	ApplyActiveSessionConfigRecovery(groupID int64, group GroupRuntime) error
	RuntimeExecutor(sessionID uint64) *RuntimeExecutor
}

// RuntimeScanCoordinator serializes scan rounds and owns polling lifecycle.
type RuntimeScanCoordinator interface {
	BeginRuntimeScanRound() bool
	FinishRuntimeScanRound()
	SetRuntimeScanCancel(cancel context.CancelFunc)
	ScanWG() *sync.WaitGroup
	RuntimeScanPoll() time.Duration
}

// RuntimeRepositoryProvider exposes the runtime repository dependency.
type RuntimeRepositoryProvider interface {
	Repo() Repository
}

// RuntimeLoggerProvider exposes structured logging for runtime orchestration.
type RuntimeLoggerProvider interface {
	Logger() *slog.Logger
}

// RuntimeSchedulerProvider exposes scheduled runtime work.
type RuntimeSchedulerProvider interface {
	Scheduler() clock.Scheduler
}

// RuntimeClockProvider exposes wall-clock reads for data-plane bookkeeping.
type RuntimeClockProvider interface {
	Clock() clock.Clock
}

// RuntimeServeContextFactory builds per-listener serve contexts.
type RuntimeServeContextFactory interface {
	NewTunnelRuntimeServeContext(opCtx TunnelListenerOperationContext, session SessionRuntimeStartTarget, remotePort uint16) TunnelRuntimeServeContext
}

// RuntimeDataPlaneServicer owns TCP/UDP data-plane goroutine entry points.
type RuntimeDataPlaneServicer interface {
	ShutdownSession(session SessionRuntimeStartTarget)
	ServeUDPIdleCleanup(conn net.Conn, logger Logger, session SessionRuntimeStartTarget)
	ServeTunnelListener(serve TunnelRuntimeServeContext, listener net.Listener)
	ServeUDPTunnelListener(serve TunnelRuntimeServeContext, listener UDPListener)
}

// RuntimeScannerDeps is the target dependency seam for scan and recovery code.
type RuntimeScannerDeps interface {
	RuntimeReadiness
	RuntimeIssueWriter
	RuntimeIPResolver
	RuntimeListenerProbe
	RuntimeSessionRegistry
	RuntimeRecoveryCoordinator
	RuntimeScanCoordinator
	RuntimeRepositoryProvider
	RuntimeLoggerProvider
	RuntimeSchedulerProvider
}

// RuntimeServeDeps is the target dependency seam for TCP/UDP data-plane code.
type RuntimeServeDeps interface {
	RuntimeClockProvider
	RuntimeSchedulerProvider
	RuntimeServeContextFactory
	RuntimeDataPlaneServicer
}
