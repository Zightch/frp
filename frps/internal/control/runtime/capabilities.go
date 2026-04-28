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

// RuntimeActiveSessionFinder exposes active session lookup by group.
type RuntimeActiveSessionFinder interface {
	ActiveSession(groupID int64) (*ActiveSession, bool)
}

// RuntimeActiveGroupProvider exposes currently listening runtime groups.
type RuntimeActiveGroupProvider interface {
	ActiveRuntimeGroups(exclude SessionStateProjectionTarget) []RuntimeGroupSnapshot
}

// RuntimeSnapshotProvider exposes the runtime snapshot index.
type RuntimeSnapshotProvider interface {
	RuntimeSnapshotIndex() RuntimeSnapshotIndex
}

// RuntimeSessionStateProvider exposes projected session state by session ID.
type RuntimeSessionStateProvider interface {
	SessionState(sessionID uint64) (SessionState, bool)
}

// RuntimeSessionEventDispatcher dispatches events to live session agents.
type RuntimeSessionEventDispatcher interface {
	DispatchBySessionID(sessionID uint64, event Event) bool
}

// RuntimeSessionEventApplier applies an event to concrete session state.
type RuntimeSessionEventApplier interface {
	ApplySessionEvent(sessionID uint64, event Event)
}

// RuntimeAuditedRecoveryRequester requests audited listener recovery for a session.
type RuntimeAuditedRecoveryRequester interface {
	RequestAuditedSessionRuntimeRecovery(sessionID uint64, targetTunnels []protocol.TunnelEntry) error
}

// RuntimeDesiredUpdater applies recovered desired runtime state to a live session.
type RuntimeDesiredUpdater interface {
	ApplyActiveSessionConfigRecovery(groupID int64, group GroupRuntime) error
}

// RuntimeExecutorProvider exposes runtime executor state for recovery waits.
type RuntimeExecutorProvider interface {
	RuntimeExecutor(sessionID uint64) *RuntimeExecutor
}

// RuntimeScannedSessionRecoveryDeps are the side effects needed by scan-triggered recovery.
type RuntimeScannedSessionRecoveryDeps interface {
	RuntimeReadiness
	RuntimeIPResolver
	RuntimeActiveSessionFinder
	RuntimeAuditedRecoveryRequester
	RuntimeDesiredUpdater
	RuntimeLoggerProvider
}

// RuntimeAuditedSessionRecoveryDeps are the registry operations needed by audited recovery.
type RuntimeAuditedSessionRecoveryDeps interface {
	RuntimeSessionStateProvider
	RuntimeSessionEventApplier
	RuntimeSessionEventDispatcher
	RuntimeExecutorProvider
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

// RuntimeStartPlannerDeps are the read dependencies needed to plan listener startup.
type RuntimeStartPlannerDeps interface {
	RuntimeReadiness
	RuntimeIPResolver
	RuntimeActiveGroupProvider
}

// RuntimeStartApplyDeps are the side-effect dependencies needed to apply a start plan.
type RuntimeStartApplyDeps interface {
	RuntimeReadiness
	RuntimeIssueWriter
	ListenerStarter
	RuntimeServeContextFactory
	RuntimeDataPlaneServicer
}

// RuntimeStartDeps groups runtime start planning and application capabilities.
type RuntimeStartDeps interface {
	RuntimeStartPlannerDeps
	RuntimeStartApplyDeps
}

// RuntimeScannerDeps is the target dependency seam for scan and recovery code.
type RuntimeScannerDeps interface {
	RuntimeIssueWriter
	RuntimeListenerProbe
	RuntimeActiveGroupProvider
	RuntimeSnapshotProvider
	RuntimeScannedSessionRecoveryDeps
	RuntimeScanCoordinator
	RuntimeRepositoryProvider
	RuntimeSchedulerProvider
}

// RuntimeServeDeps is the target dependency seam for TCP/UDP data-plane code.
type RuntimeServeDeps interface {
	RuntimeClockProvider
	RuntimeSchedulerProvider
	RuntimeServeContextFactory
	RuntimeDataPlaneServicer
}
