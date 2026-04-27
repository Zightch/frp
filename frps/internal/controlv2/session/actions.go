package session

type Action interface {
	sessionAction()
}

type ActionSendServerHello struct{}

func (ActionSendServerHello) sessionAction() {}

type ActionPushConfig struct {
	RequestID uint32
	Snapshot  DesiredRuntimeSnapshot
}

func (ActionPushConfig) sessionAction() {}

type ActionSendConfigError struct {
	RequestID uint32
	Message   string
}

func (ActionSendConfigError) sessionAction() {}

type ActionSendHeartbeatPong struct {
	RequestID    uint32
	ClientUnixMs uint64
}

func (ActionSendHeartbeatPong) sessionAction() {}

type ActionSendStreamOpen struct {
	StreamID   uint32
	RequestID  uint32
	TunnelID   uint32
	RemotePort uint16
	ClientAddr string
}

func (ActionSendStreamOpen) sessionAction() {}

type ActionSendStreamClose struct {
	StreamID uint32
	Message  string
}

func (ActionSendStreamClose) sessionAction() {}

type ActionSendUDPStart struct {
	SessionID  uint32
	TunnelID   uint32
	RemotePort uint16
	ClientAddr string
}

func (ActionSendUDPStart) sessionAction() {}

type ActionSendUDPData struct {
	SessionID  uint32
	PayloadLen int
}

func (ActionSendUDPData) sessionAction() {}

type ActionSendUDPClose struct {
	SessionID uint32
	Message   string
}

func (ActionSendUDPClose) sessionAction() {}

type ActionCloseControlConn struct {
	Reason string
}

func (ActionCloseControlConn) sessionAction() {}

type ActionPrepareBindings struct {
	EffectiveIP string
	Snapshot    DesiredRuntimeSnapshot
	Epoch       uint64
}

func (ActionPrepareBindings) sessionAction() {}

type ActionStartBindings struct {
	Keys  []BindingKey
	Epoch uint64
}

func (ActionStartBindings) sessionAction() {}

type ActionStopBindings struct {
	Keys  []BindingKey
	Epoch uint64
}

func (ActionStopBindings) sessionAction() {}

type ActionDrainStreams struct {
	Reason string
}

func (ActionDrainStreams) sessionAction() {}

type ActionDrainUDPSessions struct {
	Reason string
}

func (ActionDrainUDPSessions) sessionAction() {}

type ActionResetRuntime struct{}

func (ActionResetRuntime) sessionAction() {}

type ActionRequestReconcile struct {
	Reason string
}

func (ActionRequestReconcile) sessionAction() {}

type ActionLogTransition struct {
	Message string
}

func (ActionLogTransition) sessionAction() {}
