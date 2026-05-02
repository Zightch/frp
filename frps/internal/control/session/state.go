package session

type SessionPhase uint8

const (
	SessionPhaseUnknown SessionPhase = iota
	SessionPhaseHandshaking
	SessionPhaseSyncingConfig
	SessionPhaseOnline
	SessionPhaseDraining
	SessionPhaseClosed
)

type RuntimePhase uint8

const (
	RuntimePhaseUnknown RuntimePhase = iota
	RuntimePhaseEmpty
	RuntimePhaseBinding
	RuntimePhaseActive
	RuntimePhaseBlocked
	RuntimePhaseRecovering
)

type BlockReason uint8

const (
	BlockReasonNone BlockReason = iota
	BlockReasonEffectiveIPInvalid
	BlockReasonEffectiveIPNotLocal
	BlockReasonPortConflict
	BlockReasonListenerStartFailed
	BlockReasonSessionReplaced
	BlockReasonShutdown
)

type BindingPhase uint8

const (
	BindingPhaseUnknown BindingPhase = iota
	BindingPhasePrepared
	BindingPhaseStarting
	BindingPhaseActive
	BindingPhaseClosing
	BindingPhaseClosed
	BindingPhaseFailed
)

type ControlConnState struct {
	Attached bool
	Closing  bool
	ConnID   string
}

type DesiredTunnelRuntime struct {
	TunnelID                     uint32
	TunnelName                   string
	Protocol                     string
	Enabled                      bool
	ListenTLSMode                uint8
	RemoteStart                  uint16
	RemoteEnd                    uint16
	LocalHost                    string
	LocalStart                   uint16
	LocalEnd                     uint16
	BackendTLSMode               uint8
	BackendTLSLoadSystemCA       bool
	BackendTLSInsecureSkipVerify bool
	BackendTLSServerName         string
	BackendTLSCAPEM              string
	BackendTLSClientCertPEM      string
	BackendTLSClientKeyPEM       string
	RatePolicyID                 uint32
	RatePolicyMode               uint8
	RatePolicyDownlinkBPS        uint64
	RatePolicyUplinkBPS          uint64
}

type DesiredRuntimeSnapshot struct {
	Version       uint64
	GeneratedAtMs uint64
	EffectiveIP   string
	Tunnels       []DesiredTunnelRuntime
}

type PendingConfigPush struct {
	RequestID uint32
	Snapshot  DesiredRuntimeSnapshot
}

type AppliedRuntimeSnapshot struct {
	Snapshot DesiredRuntimeSnapshot
}

type BindingKey struct {
	Protocol    string
	EffectiveIP string
	Port        uint16
}

type BindingState struct {
	Key       BindingKey
	Epoch     uint64
	Phase     BindingPhase
	LastError string
}

type TCPStreamState struct {
	StreamID      uint32
	TunnelID      uint32
	Epoch         uint64
	RemotePort    uint16
	ClientAddr    string
	OpenRequestID uint32
	Established   bool
}

type UDPSessionState struct {
	SessionID   uint32
	TunnelID    uint32
	Epoch       uint64
	RemotePort  uint16
	ClientAddr  string
	Opened      bool
	LastPayload int
}

type SessionState struct {
	GroupID   int64
	SessionID uint64

	Phase        SessionPhase
	RuntimePhase RuntimePhase
	BlockReason  BlockReason

	Desired *DesiredRuntimeSnapshot
	Pending *PendingConfigPush
	Applied *AppliedRuntimeSnapshot

	Conn          ControlConnState
	Epoch         uint64
	NextRequestID uint32
	NextStreamID  uint32

	Bindings    map[BindingKey]BindingState
	Streams     map[uint32]TCPStreamState
	UDPSessions map[uint32]UDPSessionState
}

func NewState(groupID int64, sessionID uint64) SessionState {
	return SessionState{
		GroupID:       groupID,
		SessionID:     sessionID,
		Phase:         SessionPhaseHandshaking,
		RuntimePhase:  RuntimePhaseEmpty,
		BlockReason:   BlockReasonNone,
		NextRequestID: 1,
		NextStreamID:  1,
		Bindings:      make(map[BindingKey]BindingState),
		Streams:       make(map[uint32]TCPStreamState),
		UDPSessions:   make(map[uint32]UDPSessionState),
	}
}

func cloneState(state SessionState) SessionState {
	next := state

	if state.Desired != nil {
		desired := *state.Desired
		desired.Tunnels = append([]DesiredTunnelRuntime(nil), state.Desired.Tunnels...)
		next.Desired = &desired
	}
	if state.Pending != nil {
		pending := *state.Pending
		pending.Snapshot.Tunnels = append([]DesiredTunnelRuntime(nil), state.Pending.Snapshot.Tunnels...)
		next.Pending = &pending
	}
	if state.Applied != nil {
		applied := *state.Applied
		applied.Snapshot.Tunnels = append([]DesiredTunnelRuntime(nil), state.Applied.Snapshot.Tunnels...)
		next.Applied = &applied
	}

	next.Bindings = make(map[BindingKey]BindingState, len(state.Bindings))
	for key, value := range state.Bindings {
		next.Bindings[key] = value
	}
	next.Streams = make(map[uint32]TCPStreamState, len(state.Streams))
	for key, value := range state.Streams {
		next.Streams[key] = value
	}
	next.UDPSessions = make(map[uint32]UDPSessionState, len(state.UDPSessions))
	for key, value := range state.UDPSessions {
		next.UDPSessions[key] = value
	}

	return next
}

func Clone(state SessionState) SessionState {
	return cloneState(state)
}

func Advance(state SessionState, event Event) SessionState {
	current := cloneState(state)
	queue := []Event{event}

	for len(queue) > 0 {
		nextEvent := queue[0]
		queue = queue[1:]

		next, actions := Reduce(current, nextEvent)
		current = next
		for _, action := range actions {
			request, ok := action.(ActionRequestReconcile)
			if !ok {
				continue
			}
			queue = append(queue, ReconcileRequested{Reason: request.Reason})
		}
	}

	return current
}

func nextRequestID(state *SessionState) uint32 {
	requestID := state.NextRequestID
	state.NextRequestID++
	if state.NextRequestID == 0 {
		state.NextRequestID = 1
	}
	return requestID
}

func nextStreamID(state *SessionState) uint32 {
	streamID := state.NextStreamID
	state.NextStreamID++
	if state.NextStreamID == 0 {
		state.NextStreamID = 1
	}
	return streamID
}

func bindingKeys(bindings map[BindingKey]BindingState) []BindingKey {
	keys := make([]BindingKey, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	return keys
}

func desiredHasEnabledTunnels(snapshot DesiredRuntimeSnapshot) bool {
	for _, tunnel := range snapshot.Tunnels {
		if tunnel.Enabled {
			return true
		}
	}
	return false
}

func findUDPSession(state SessionState, tunnelID uint32, remotePort uint16, clientAddr string) (UDPSessionState, bool) {
	for _, session := range state.UDPSessions {
		if session.TunnelID == tunnelID && session.RemotePort == remotePort && session.ClientAddr == clientAddr {
			return session, true
		}
	}
	return UDPSessionState{}, false
}

func snapshotsEqual(left, right DesiredRuntimeSnapshot) bool {
	if left.Version != right.Version ||
		left.GeneratedAtMs != right.GeneratedAtMs ||
		left.EffectiveIP != right.EffectiveIP ||
		len(left.Tunnels) != len(right.Tunnels) {
		return false
	}
	for index := range left.Tunnels {
		if left.Tunnels[index] != right.Tunnels[index] {
			return false
		}
	}
	return true
}

func allBindingsActive(bindings map[BindingKey]BindingState) bool {
	if len(bindings) == 0 {
		return false
	}
	for _, binding := range bindings {
		if binding.Phase != BindingPhaseActive {
			return false
		}
	}
	return true
}
