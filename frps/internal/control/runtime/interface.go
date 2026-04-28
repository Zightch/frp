package runtime

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlrepo "github.com/zightch/frp/frps/internal/control/repo"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

// RuntimeOperator 是迁移期遗留聚合接口。
//
// 不要继续向这里追加方法；新增 runtime 能力应先落到 capabilities.go 中的
// 小接口，再逐步把调用点从 RuntimeOperator 收敛到最小依赖。
type RuntimeOperator interface {
	// 状态检查
	IsShuttingDown() bool

	// 网络/IP 解析
	ResolveGroupEffectiveIP(group GroupRuntime) (string, error)

	// 会话管理
	ActiveSession(groupID int64) (*ActiveSession, bool)
	// ActiveRuntimeGroups returns all active runtime groups, optionally excluding a specific session.
	// The exclude parameter is passed through to the supervisor and can be nil.
	ActiveRuntimeGroups(exclude any) []RuntimeGroupSnapshot

	// Issue 记录
	RecordTunnelRuntimeIssueForConfig(tunnelID uint32, configVersion uint64, reason string)
	ClearUnknownTunnelRuntimeIssues(knownTunnelIDs map[int64]struct{})
	ApplyScannedTunnelRuntimeIssues(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{})

	// 监听器操作
	StartTunnelListeners(opCtx TunnelListenerOperationContext) (TunnelListenerBatch, error)
	ProbeTunnelRuntimeIssue(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string
	// NewTunnelRuntimeServeContext creates a serve context for a tunnel runtime.
	NewTunnelRuntimeServeContext(opCtx TunnelListenerOperationContext, session SessionRuntimeStartTarget, remotePort uint16) TunnelRuntimeServeContext

	// 会话生命周期 (using any to avoid circular type dependencies)
	ShutdownSession(session any)
	ServeUDPIdleCleanup(conn net.Conn, logger Logger, session any)
	ServeTunnelListener(serve TunnelRuntimeServeContext, listener net.Listener)
	ServeUDPTunnelListener(serve TunnelRuntimeServeContext, listener UDPListener)

	// 扫描协调
	BeginRuntimeScanRound() bool
	FinishRuntimeScanRound()
	RuntimeSnapshotIndex() RuntimeSnapshotIndex
	SetRuntimeScanCancel(cancel context.CancelFunc)

	// 恢复操作
	RequestAuditedSessionRuntimeRecovery(sessionID uint64, targetTunnels []protocol.TunnelEntry) error
	RuntimeExecutor(sessionID uint64) *RuntimeExecutor
	DispatchBySessionID(sessionID uint64, event Event) bool
	ApplyActiveSessionConfigRecovery(groupID int64, group GroupRuntime) error
	SessionState(sessionID uint64) (SessionState, bool)
	ApplySessionEvent(sessionID uint64, event Event)

	// Supervisor
	Supervisor() *Supervisor

	// 基础设施
	Logger() *slog.Logger
	Repo() Repository
	Scheduler() clock.Scheduler
	ScanWG() *sync.WaitGroup
	RuntimeScanPoll() time.Duration
}

// SessionState 是 controlsession.SessionState 的别名
type SessionState = controlsession.SessionState

// Repository 是 controlrepo.Repository 的别名
type Repository = controlrepo.Repository

// ActiveSession 表示活跃会话
type ActiveSession struct {
	Conn    net.Conn
	Session *SessionState
}

// TunnelRuntimeServeContext 定义隧道运行时服务上下文
type TunnelRuntimeServeContext struct {
	Logger     Logger
	Session    any // *sessionState from control package
	RuntimeIO  RuntimeIOWriter
	Tunnel     protocol.TunnelEntry
	RemotePort uint16
}

// TunnelListenerBatch 定义隧道监听器批次
type TunnelListenerBatch struct {
	TCPListeners []net.Listener
	TCPRuntimes  []TCPTunnelListener
	UDPListeners []UDPListener
	UDPRuntimes  []UDPTunnelListener
}

// TCPTunnelListener 定义 TCP 隧道监听器
type TCPTunnelListener struct {
	ConfigVersion uint64
	Tunnel        protocol.TunnelEntry
	RemotePort    uint16
	Listener      net.Listener
}

// UDPTunnelListener 定义 UDP 隧道监听器
type UDPTunnelListener struct {
	ConfigVersion uint64
	Tunnel        protocol.TunnelEntry
	RemotePort    uint16
	Listener      UDPListener
}

// BindKind 是 controlbind.BindKind 的别名
type BindKind = controlbind.BindKind

// UDPListener 是 controlbind.UDPListener 的别名
type UDPListener = controlbind.UDPListener

// Logger 定义日志接口
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

// RuntimeIOWriter 定义运行时 IO 写入接口
type RuntimeIOWriter interface {
	WriteFrame(frame protocol.Frame) error
	WriteFrames(frames ...protocol.Frame) error
}

// RuntimeExecutor 定义运行时执行器
type RuntimeExecutor struct {
	GroupID      int64
	Conn         net.Conn
	Logger       *slog.Logger
	Session      any // *sessionState from control package
	DesiredGroup GroupRuntime
}

// Supervisor 定义主管
// Note: The actual implementation is in the control package's Supervisor.
// The Server.Supervisor() method will return the control package's Supervisor.
type Supervisor struct {
	_ struct{} // placeholder to make it a distinct type
}

// SessionState returns the session state for the given session ID.
// This method is provided for the SupervisorOperator interface.
func (s *Supervisor) SessionState(sessionID uint64) (SessionState, bool) {
	// The actual implementation is in control.Supervisor, which is accessed
	// through the Server.Supervisor() method
	return SessionState{}, false
}

// DispatchBySessionID dispatches an event to the session with the given ID.
// This method is provided for the SupervisorOperator interface.
func (s *Supervisor) DispatchBySessionID(sessionID uint64, event Event) bool {
	// The actual implementation is in control.Supervisor, which is accessed
	// through the Server.Supervisor() method
	return false
}

// Event 是 controlsession.Event 的别名
type Event = controlsession.Event

// RuntimeSnapshotIndex 定义运行时快照索引
type RuntimeSnapshotIndex struct {
	Sessions        []RuntimeSessionTarget
	SessionsByID    map[RuntimeSessionTargetID]RuntimeSessionTarget
	SessionsByGroup map[int64]RuntimeSessionTarget
}

// RuntimeSessionTargetID 定义运行时会话目标 ID
type RuntimeSessionTargetID struct {
	GroupID   int64
	SessionID uint64
}

// RuntimeSessionTarget 定义运行时会话目标
type RuntimeSessionTarget struct {
	ID                RuntimeSessionTargetID
	Conn              net.Conn
	ConnID            string
	EffectiveIP       string
	Snapshot          ConfigSnapshot
	LastAckedVersion  uint64
	Pending           *RuntimePendingConfigTarget
	RecoveryMode      testsupport.RecoveryMode
	RuntimeFrozen     bool
	ListenersStarted  bool
	RuntimeGeneration uint64
	ActiveStreamCount uint32
	ActiveUDPCount    uint32
	ActiveTunnelIDs   map[uint32]struct{}
	Listeners         []RuntimeListenerTarget
	MissingListeners  []RuntimeMissingListenerTarget
	Connections       []RuntimeConnectionTarget
	// Internal lookup maps
	ListenersByTunnel map[uint32][]RuntimeListenerTarget
	MissingByTunnel   map[uint32]RuntimeMissingListenerTarget
}

// RuntimePendingConfigTarget 定义运行时待定配置目标
type RuntimePendingConfigTarget struct {
	RequestID   uint32
	Snapshot    ConfigSnapshot
	EffectiveIP string
}

// RuntimeListenerTarget 定义运行时监听器目标
type RuntimeListenerTarget struct {
	ID            RuntimeTunnelTargetID
	Protocol      string
	BindIP        string
	Port          uint16
	ConfigVersion uint64
	Kind          string
}

// RuntimeMissingListenerTarget 定义运行时缺失监听器目标
type RuntimeMissingListenerTarget struct {
	ID           RuntimeTunnelTargetID
	Protocol     string
	MissingPorts []uint16
}

// RuntimeConnectionTarget 定义运行时连接目标
type RuntimeConnectionTarget struct {
	ID             RuntimeConnectionTargetID
	Protocol       string
	TunnelID       uint32
	RemotePort     uint16
	ClientAddr     string
	OpenedAtMs     uint64
	LastActiveAtMs uint64
	IdleTimeoutMs  uint32
}

// RuntimeTunnelTargetID 定义运行时隧道目标 ID
type RuntimeTunnelTargetID struct {
	GroupID   int64
	SessionID uint64
	TunnelID  uint32
}

// RuntimeConnectionTargetID 定义运行时连接目标 ID
type RuntimeConnectionTargetID struct {
	GroupID      int64
	SessionID    uint64
	ConnectionID uint32
	Kind         string
}

// RuntimeTunnelTarget 定义运行时隧道目标
type RuntimeTunnelTarget struct {
	ID             RuntimeTunnelTargetID
	Tunnel         protocol.TunnelEntry
	StaticConflict bool
	RuntimeIssue   string
	RuntimeKind    string
	FinalStatus    string
	FinalReason    string
	Listeners      []RuntimeListenerTarget
	MissingPorts   []uint16
}

// RuntimeObservedConfig 定义运行时观察到的配置
type RuntimeObservedConfig struct {
	EffectiveIP            string
	Snapshot               ConfigSnapshot
	LastAckedConfigVersion uint64
	Pending                *RuntimePendingConfigTarget
}

// SessionRuntimeStartTarget defines the interface for session runtime start operations.
// This interface is implemented by the control package's sessionRuntimeStartTarget.
type SessionRuntimeStartTarget interface {
	// Session operations
	SessionIsDone() bool
	SessionCanStartTunnelRuntime() bool
	SessionActiveRuntimeTunnelIDs() map[uint32]struct{}
	SessionResetRuntimeGenerationIfIdle()
	SessionSetRecoveryMode(mode testsupport.RecoveryMode)
	SessionAttachTunnelListeners(configVersion uint64, tunnelID uint32, tcpListeners []net.Listener, udpListeners []UDPListener) (startUDPCleanup bool, attached bool)
	SessionHasActiveRuntimeListeners() bool

	// Accessors
	Conn() net.Conn
	Logger() Logger
	Group() GroupRuntime
	Snapshot() ConfigSnapshot
}

// SessionRuntimeStartPlan represents the plan for starting a session runtime.
type SessionRuntimeStartPlan struct {
	Group               GroupRuntime
	Snapshot            ConfigSnapshot
	Blocked             bool
	ActiveRuntime       bool
	ClearIssueTunnelIDs []uint32
	TargetTunnels       []protocol.TunnelEntry
	BindIP              string
	BindErr             error
	ConflictIssues      map[uint32]string
}

// 常量定义
const (
	BindKindRuntimeProbe = controlbind.BindKindRuntimeProbe
	BindKindRuntimeStart = controlbind.BindKindRuntimeStart
)
