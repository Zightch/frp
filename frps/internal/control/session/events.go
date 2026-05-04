package session

type Event interface {
	sessionEvent()
}

type SessionAttached struct {
	ConnID         string
	HelloRequestID uint32
}

func (SessionAttached) sessionEvent() {}

type DesiredRuntimeUpdated struct {
	Snapshot DesiredRuntimeSnapshot
}

func (DesiredRuntimeUpdated) sessionEvent() {}

type NetworkSnapshotChanged struct{}

func (NetworkSnapshotChanged) sessionEvent() {}

type SessionTakeoverRequested struct {
	ReplacementSessionID uint64
}

func (SessionTakeoverRequested) sessionEvent() {}

type ShutdownRequested struct{}

func (ShutdownRequested) sessionEvent() {}

type ConfigAckReceived struct {
	RequestID     uint32
	ConfigVersion uint64
}

func (ConfigAckReceived) sessionEvent() {}

type HeartbeatPingReceived struct {
	RequestID    uint32
	ClientUnixMs uint64
}

func (HeartbeatPingReceived) sessionEvent() {}

type UDPDataReceived struct {
	SessionID  uint32
	PayloadLen int
}

func (UDPDataReceived) sessionEvent() {}

type UDPCloseReceived struct {
	SessionID uint32
}

func (UDPCloseReceived) sessionEvent() {}

type ControlConnClosed struct {
	Reason string
}

func (ControlConnClosed) sessionEvent() {}

type ProtocolErrorDetected struct {
	Reason string
}

func (ProtocolErrorDetected) sessionEvent() {}

type BindingsPrepared struct {
	Keys []BindingKey
}

func (BindingsPrepared) sessionEvent() {}

type BindingsPreparationFailed struct {
	Reason  BlockReason
	Message string
}

func (BindingsPreparationFailed) sessionEvent() {}

type BindingStarted struct {
	Key   BindingKey
	Epoch uint64
}

func (BindingStarted) sessionEvent() {}

type BindingStartFailed struct {
	Key     BindingKey
	Epoch   uint64
	Reason  BlockReason
	Message string
}

func (BindingStartFailed) sessionEvent() {}

type BindingClosed struct {
	Key    BindingKey
	Reason string
}

func (BindingClosed) sessionEvent() {}

type UDPDatagramReceived struct {
	TunnelID   uint32
	RemotePort uint16
	ClientAddr string
	PayloadLen int
}

func (UDPDatagramReceived) sessionEvent() {}

type UDPIdleTimeoutReached struct {
	SessionID uint32
}

func (UDPIdleTimeoutReached) sessionEvent() {}

type ReconcileRequested struct {
	Reason string
}

func (ReconcileRequested) sessionEvent() {}

type DrainCompleted struct{}

func (DrainCompleted) sessionEvent() {}
