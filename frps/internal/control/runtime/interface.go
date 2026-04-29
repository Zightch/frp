package runtime

import (
	"log/slog"
	"net"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlrepo "github.com/zightch/frp/frps/internal/control/repo"
	controllistener "github.com/zightch/frp/frps/internal/control/runtime/listener"
	controlruntimestate "github.com/zightch/frp/frps/internal/control/runtime/state"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

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
	Session    SessionRuntimeStartTarget
	RuntimeIO  RuntimeIOWriter
	Tunnel     protocol.TunnelEntry
	RemotePort uint16
}

type TunnelListenerBatch = controllistener.TunnelListenerBatch
type TCPTunnelListener = controllistener.TCPTunnelListener
type UDPTunnelListener = controllistener.UDPTunnelListener

// BindKind 是 controlbind.BindKind 的别名
type BindKind = controlbind.BindKind

// UDPListener 是 controlbind.UDPListener 的别名
type UDPListener = controllistener.UDPListener

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
	GroupID int64
	Conn    net.Conn
	Logger  *slog.Logger
	Session SessionStateProjectionTarget
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
type RuntimePendingConfigTarget = controlruntimestate.RuntimePendingConfig

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
type RuntimeObservedConfig = controlruntimestate.RuntimeObservedConfig

// SessionRuntimeStartTarget defines the interface for session runtime start operations.
// This interface is implemented by the control package's sessionRuntimeStartTarget.
type SessionRuntimeStartTarget interface {
	// Session operations
	SessionIsDone() bool
	SessionCanStartTunnelRuntime() bool
	SessionActiveRuntimeTunnelIDs() map[uint32]struct{}
	SessionResetRuntimeGenerationIfIdle()
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
