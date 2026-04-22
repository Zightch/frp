package testsupport

import "time"

type RecoveryMode string

const (
	RecoveryModeUnknown            RecoveryMode = "unknown"
	RecoveryModeRunning            RecoveryMode = "running"
	RecoveryModePendingFullConfig  RecoveryMode = "pending_full_config"
	RecoveryModePendingEmptyConfig RecoveryMode = "pending_empty_config"
	RecoveryModeEmptyConfig        RecoveryMode = "empty_config"
	RecoveryModeListenerRecovery   RecoveryMode = "listener_recovery"
	RecoveryModeReconnect          RecoveryMode = "reconnecting"
	RecoveryModeReplaced           RecoveryMode = "replaced"
)

type AppObservedState struct {
	InitialRuntimeScanDone bool
	ControlListenerOpen    bool
	LoginGateOpen          bool
	ManagementAPIVisible   bool
}

type PendingConfigObservedState struct {
	RequestID   uint32
	Version     uint64
	TunnelCount int
	EffectiveIP string
}

type SessionObservedState struct {
	GroupID                int64
	SessionID              uint64
	ConnID                 string
	EffectiveIP            string
	SnapshotVersion        uint64
	SnapshotTunnelCount    int
	LastAckedConfigVersion uint64
	Pending                *PendingConfigObservedState
	RuntimeFrozen          bool
	ListenersStarted       bool
	RuntimeGeneration      uint64
	RecoveryMode           RecoveryMode
}

type TunnelObservedState struct {
	GroupID        int64
	TunnelID       uint32
	StaticConflict bool
	RuntimeIssue   string
	RuntimeKind    string
	FinalStatus    string
	FinalReason    string
}

type AttachedListenerObservedState struct {
	GroupID       int64
	SessionID     uint64
	TunnelID      uint32
	Protocol      string
	BindIP        string
	Port          uint16
	ConfigVersion uint64
	Kind          string
}

type MissingListenerObservedState struct {
	GroupID      int64
	SessionID    uint64
	TunnelID     uint32
	Protocol     string
	MissingPorts []uint16
}

type ServerObservedState struct {
	InitialRuntimeScanDone bool
	ControlListenerOpen    bool
	LoginGateOpen          bool
	GroupSlots             map[int64]uint64
	Sessions               []SessionObservedState
	Tunnels                []TunnelObservedState
	Listeners              []AttachedListenerObservedState
	MissingListeners       []MissingListenerObservedState
}

type ReloadSummaryObservedState struct {
	AddedTunnels      int
	RemovedTunnels    int
	ReplacedTunnels   int
	UnchangedTunnels  int
	ClosedStreams     int
	ClosedUDPSessions int
}

type ClientObservedState struct {
	Attempt                uint64
	ConnID                 string
	SessionID              uint64
	GroupID                int64
	Replaced               bool
	SnapshotVersion        uint64
	SnapshotGeneratedAtMs  uint64
	SnapshotTunnelCount    int
	LastAckedConfigVersion uint64
	ActiveStreams          uint32
	ActiveUDPSessions      uint32
	RecoveryMode           RecoveryMode
	LastReload             ReloadSummaryObservedState
}

type SnapshotObservedState struct {
	Started              bool
	Version              uint64
	LastCollectSucceeded bool
	LastCollectError     string
	CapturedAt           time.Time
	AvailableIPs         []string
}

type ListenerOccupancyObservedState struct {
	Protocol string
	IP       string
	Port     uint16
	Owner    string
}

type ListenerHandleObservedState struct {
	Protocol string
	IP       string
	Port     uint16
	Count    int
}

type ListenerCallObservedState struct {
	Sequence      int
	Op            string
	GroupID       int64
	TunnelID      uint32
	ConfigVersion uint64
	Kind          string
	Protocol      string
	IP            string
	Port          uint16
}

type ListenerWorldObservedState struct {
	Occupied []ListenerOccupancyObservedState
	Handles  []ListenerHandleObservedState
	Calls    []ListenerCallObservedState
}

type FrameDirection string

const (
	FrameDirectionClientToServer FrameDirection = "c2s"
	FrameDirectionServerToClient FrameDirection = "s2c"
)

type TransportConnState string

const (
	TransportConnStateOpen        TransportConnState = "open"
	TransportConnStateReadClosed  TransportConnState = "read_closed"
	TransportConnStateWriteClosed TransportConnState = "write_closed"
	TransportConnStateClosed      TransportConnState = "closed"
)

type FrameAction string

const (
	FrameActionDeliver   FrameAction = "deliver"
	FrameActionDelay     FrameAction = "delay"
	FrameActionDrop      FrameAction = "drop"
	FrameActionDuplicate FrameAction = "duplicate"
	FrameActionError     FrameAction = "error"
)

type FrameObservedState struct {
	Sequence      uint64
	ConnID        string
	Direction     FrameDirection
	FrameType     string
	RequestID     uint32
	StreamID      uint32
	SessionID     uint64
	ConfigVersion uint64
	Action        FrameAction
}

type TransportConnObservedState struct {
	ConnID       string
	ClientState  TransportConnState
	ServerState  TransportConnState
	DelayedCount int
}

type TransportObservedState struct {
	Connections []TransportConnObservedState
	Delivered   []FrameObservedState
	Delayed     []FrameObservedState
	Dropped     []FrameObservedState
	Errors      []FrameObservedState
}

type ObservedState struct {
	App       AppObservedState
	Server    ServerObservedState
	Client    ClientObservedState
	Snapshot  *SnapshotObservedState
	Listeners *ListenerWorldObservedState
	Transport *TransportObservedState
}

type InvariantCheckInput struct {
	Before *ObservedState
	After  *ObservedState
}

type InvariantViolation struct {
	Rule     string
	Summary  string
	Expected string
	Actual   string
	Fields   []string
}
