package executor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlconfigsync "github.com/zightch/frp/frps/internal/control/protocol/configsync"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type Logger interface {
	Warn(msg string, args ...any)
}

type Clock interface {
	Now() time.Time
}

type Runtime interface {
	Conn() net.Conn
	Logger() Logger
	DesiredGroup() controlruntime.GroupRuntime
	ActiveRuntimeTunnelIDs() map[uint32]struct{}
	FreezeTunnelRuntime() RuntimeDrain
	StoreDrained(RuntimeDrain)
	TakeDrainedStreams() map[uint32]*controlruntime.Stream
	TakeDrainedUDPSessions() []*controlruntime.UDPSession
	AllowTunnelRuntimeStart()
	SessionID() uint64
	TCPWorkConfig() (uint16, [32]byte)
}

type RuntimeDrain struct {
	TCPListeners []net.Listener
	UDPListeners []controlruntime.UDPListener
	Streams      map[uint32]*controlruntime.Stream
	UDPSessions  []*controlruntime.UDPSession
}

type RuntimeProvider interface {
	RuntimeForSession(sessionID uint64) Runtime
}

type FrameSender interface {
	WriteFrame(runtime Runtime, frame protocol.Frame) error
	WriteError(runtime Runtime, requestID, streamID uint32, code uint16, retryable bool, message string) error
}

type EffectiveIPResolver interface {
	ResolveGroupEffectiveIP(group controlruntime.GroupRuntime) (string, error)
}

type BindingStarter interface {
	StartBindings(runtime Runtime) error
	RuntimeIssues() map[int64]string
}

type EventDispatcher interface {
	DispatchBySessionID(sessionID uint64, event controlsession.Event) bool
}

type StreamCloser interface {
	SendStreamClose(runtime Runtime, streamID uint32, reasonCode uint16, message string) error
}

type UDPCloser interface {
	SendUDPClose(runtime Runtime, sessionID uint32, reasonCode uint16, message string) error
}

type Options struct {
	RuntimeProvider   RuntimeProvider
	Frames            FrameSender
	Resolver          EffectiveIPResolver
	BindingStarter    BindingStarter
	StreamCloser      StreamCloser
	UDPCloser         UDPCloser
	Clock             Clock
	Events            EventDispatcher
	HeartbeatInterval time.Duration
	Version           string
}

type Executor struct {
	runtimes RuntimeProvider

	hello          HelloSender
	config         ConfigPusher
	configError    ConfigErrorSender
	heartbeat      HeartbeatPongSender
	bindPreparer   BindingPreparer
	bindStarter    BindingStartHandler
	runtimeStopper RuntimeStopper
	streamDrainer  StreamDrainer
	udpDrainer     UDPDrainer
	runtimeReset   RuntimeResetter
	controlCloser  ControlCloser
}

func New(options Options) Executor {
	return Executor{
		runtimes: options.RuntimeProvider,
		hello: HelloSender{
			Frames:            options.Frames,
			HeartbeatInterval: options.HeartbeatInterval,
			Version:           options.Version,
		},
		config:      ConfigPusher{Frames: options.Frames},
		configError: ConfigErrorSender{Frames: options.Frames},
		heartbeat: HeartbeatPongSender{
			Frames: options.Frames,
			Clock:  options.Clock,
		},
		bindPreparer: BindingPreparer{Resolver: options.Resolver},
		bindStarter: BindingStartHandler{
			Starter: options.BindingStarter,
			Events:  options.Events,
		},
		runtimeStopper: RuntimeStopper{},
		streamDrainer:  StreamDrainer{Closer: options.StreamCloser},
		udpDrainer:     UDPDrainer{Closer: options.UDPCloser},
		runtimeReset:   RuntimeResetter{},
		controlCloser:  ControlCloser{},
	}
}

func (e Executor) Execute(_ context.Context, state controlsession.SessionState, action controlsession.Action) []controlsession.Event {
	if e.runtimes == nil {
		return nil
	}

	runtime := e.runtimes.RuntimeForSession(state.SessionID)
	if runtime == nil || runtime.Conn() == nil {
		return nil
	}

	switch typed := action.(type) {
	case controlsession.ActionSendServerHello:
		return e.hello.Handle(runtime, state, typed)
	case controlsession.ActionPushConfig:
		return e.config.Handle(runtime, typed)
	case controlsession.ActionSendConfigError:
		return e.configError.Handle(runtime, typed)
	case controlsession.ActionSendHeartbeatPong:
		return e.heartbeat.Handle(runtime, typed)
	case controlsession.ActionPrepareBindings:
		return e.bindPreparer.Handle(runtime, typed)
	case controlsession.ActionStartBindings:
		return e.bindStarter.Handle(runtime, state, typed)
	case controlsession.ActionStopBindings:
		return e.runtimeStopper.Handle(runtime)
	case controlsession.ActionDrainStreams:
		return e.streamDrainer.Handle(runtime, typed)
	case controlsession.ActionDrainUDPSessions:
		return e.udpDrainer.Handle(runtime, typed)
	case controlsession.ActionResetRuntime:
		return e.runtimeReset.Handle(runtime)
	case controlsession.ActionCloseControlConn:
		return e.controlCloser.Handle(runtime, typed)
	default:
		return nil
	}
}

type HelloSender struct {
	Frames            FrameSender
	HeartbeatInterval time.Duration
	Version           string
}

func (h HelloSender) Handle(runtime Runtime, state controlsession.SessionState, action controlsession.ActionSendServerHello) []controlsession.Event {
	workPoolSize, workSecret := runtime.TCPWorkConfig()
	body, err := protocol.MarshalServerHello(protocol.ServerHello{
		HeartbeatIntervalMs: uint32(h.HeartbeatInterval.Milliseconds()),
		SessionID:           state.SessionID,
		CapabilityBits:      0,
		ServerVersion:       h.Version,
		TCPWorkPoolSize:     workPoolSize,
		TCPWorkSecret:       workSecret,
	})
	if err != nil {
		return protocolError(err)
	}
	return h.write(runtime, protocol.Frame{
		Type:      protocol.TypeServerHello,
		RequestID: action.RequestID,
		Body:      body,
	})
}

func (h HelloSender) write(runtime Runtime, frame protocol.Frame) []controlsession.Event {
	if h.Frames == nil {
		return nil
	}
	if err := h.Frames.WriteFrame(runtime, frame); err != nil {
		return closeControlConn(runtime, err)
	}
	return nil
}

type ConfigPusher struct {
	Frames FrameSender
}

func (h ConfigPusher) Handle(runtime Runtime, action controlsession.ActionPushConfig) []controlsession.Event {
	snapshot := controlruntime.ConfigSnapshotFromDesired(action.Snapshot)
	group := runtime.DesiredGroup()
	group.Snapshot = snapshot

	sessionID := runtime.SessionID()
	testhooks.Point("control.config_push.before_write",
		testhooks.F("group_id", group.ID),
		testhooks.F("session_id", sessionID),
		testhooks.F("request_id", action.RequestID),
		testhooks.F("config_version", snapshot.Version),
		testhooks.F("tunnel_count", len(snapshot.Tunnels)),
	)

	frame, err := controlconfigsync.BuildPushFrame(action.RequestID, snapshot)
	if err != nil {
		return protocolError(err)
	}
	if h.Frames == nil {
		return nil
	}
	if err := h.Frames.WriteFrame(runtime, frame); err != nil {
		return closeControlConn(runtime, err)
	}

	testhooks.Point("control.config_push.after_write",
		testhooks.F("group_id", group.ID),
		testhooks.F("session_id", sessionID),
		testhooks.F("request_id", action.RequestID),
		testhooks.F("config_version", snapshot.Version),
		testhooks.F("tunnel_count", len(snapshot.Tunnels)),
	)
	return nil
}

type ConfigErrorSender struct {
	Frames FrameSender
}

func (h ConfigErrorSender) Handle(runtime Runtime, action controlsession.ActionSendConfigError) []controlsession.Event {
	if h.Frames == nil {
		return nil
	}
	if err := h.Frames.WriteError(
		runtime,
		action.RequestID,
		0,
		protocol.ErrorCodeProtocolBadBody,
		false,
		action.Message,
	); err != nil {
		return closeControlConn(runtime, err)
	}
	return nil
}

type HeartbeatPongSender struct {
	Frames FrameSender
	Clock  Clock
}

func (h HeartbeatPongSender) Handle(runtime Runtime, action controlsession.ActionSendHeartbeatPong) []controlsession.Event {
	var serverUnixMs uint64
	if h.Clock != nil {
		serverUnixMs = uint64(h.Clock.Now().UnixMilli())
	}
	body, err := protocol.MarshalHeartbeatPong(protocol.HeartbeatPong{
		ClientUnixMs: action.ClientUnixMs,
		ServerUnixMs: serverUnixMs,
	})
	if err != nil {
		return protocolError(err)
	}
	if h.Frames == nil {
		return nil
	}
	if err := h.Frames.WriteFrame(runtime, protocol.Frame{
		Type:      protocol.TypeHeartbeatPong,
		RequestID: action.RequestID,
		Body:      body,
	}); err != nil {
		return closeControlConn(runtime, err)
	}
	return nil
}

type BindingPreparer struct {
	Resolver EffectiveIPResolver
}

func (h BindingPreparer) Handle(runtime Runtime, action controlsession.ActionPrepareBindings) []controlsession.Event {
	if h.Resolver == nil {
		return nil
	}
	group := runtime.DesiredGroup()
	group.EffectiveIP = action.EffectiveIP
	group.Snapshot = controlruntime.ConfigSnapshotFromDesired(action.Snapshot)
	bindIP, err := h.Resolver.ResolveGroupEffectiveIP(group)
	if err != nil {
		return []controlsession.Event{
			controlsession.BindingsPreparationFailed{
				Reason:  BlockReasonForRuntimeError(err),
				Message: controlruntime.BuildGroupEffectiveIPRuntimeReason(group, err),
			},
		}
	}
	return []controlsession.Event{
		controlsession.BindingsPrepared{
			Keys: controlbind.ExpandBindings(bindIP, action.Snapshot.Tunnels),
		},
	}
}

type BindingStartHandler struct {
	Starter BindingStarter
	Events  EventDispatcher
}

func (h BindingStartHandler) Handle(runtime Runtime, state controlsession.SessionState, action controlsession.ActionStartBindings) []controlsession.Event {
	if h.Starter == nil {
		return nil
	}
	if h.Events == nil {
		if err := h.Starter.StartBindings(runtime); err != nil {
			return BindingFailureEvents(action.Keys, err, action.Epoch)
		}
		return BindingOutcomeEvents(state, action.Keys, runtime.ActiveRuntimeTunnelIDs(), h.Starter.RuntimeIssues(), action.Epoch)
	}
	sessionID := runtime.SessionID()
	go func() {
		var events []controlsession.Event
		if err := h.Starter.StartBindings(runtime); err != nil {
			events = BindingFailureEvents(action.Keys, err, action.Epoch)
		} else {
			events = BindingOutcomeEvents(state, action.Keys, runtime.ActiveRuntimeTunnelIDs(), h.Starter.RuntimeIssues(), action.Epoch)
		}
		for _, event := range events {
			h.Events.DispatchBySessionID(sessionID, event)
		}
	}()
	return nil
}

type RuntimeStopper struct{}

func (RuntimeStopper) Handle(runtime Runtime) []controlsession.Event {
	drain := runtime.FreezeTunnelRuntime()
	controlruntime.CloseStartedTunnelListeners(drain.TCPListeners, drain.UDPListeners)
	runtime.StoreDrained(drain)
	return nil
}

type StreamDrainer struct {
	Closer StreamCloser
}

func (h StreamDrainer) Handle(runtime Runtime, action controlsession.ActionDrainStreams) []controlsession.Event {
	for streamID, stream := range runtime.TakeDrainedStreams() {
		if h.Closer != nil {
			if err := h.Closer.SendStreamClose(runtime, streamID, protocol.CloseReasonAdminTerminated, action.Reason); err != nil {
				if logger := runtime.Logger(); logger != nil {
					logger.Warn("stream drain close failed", "stream_id", streamID, "error", err)
				}
			}
		}
		stream.SignalReady(net.ErrClosed)
		stream.Close()
	}
	return nil
}

type UDPDrainer struct {
	Closer UDPCloser
}

func (h UDPDrainer) Handle(runtime Runtime, action controlsession.ActionDrainUDPSessions) []controlsession.Event {
	for _, udpSession := range runtime.TakeDrainedUDPSessions() {
		if udpSession == nil {
			continue
		}
		if h.Closer == nil {
			continue
		}
		if err := h.Closer.SendUDPClose(runtime, udpSession.SessionID, protocol.CloseReasonAdminTerminated, action.Reason); err != nil {
			if logger := runtime.Logger(); logger != nil {
				logger.Warn("udp drain close failed", "udp_session_id", udpSession.SessionID, "error", err)
			}
		}
	}
	return nil
}

type RuntimeResetter struct{}

func (RuntimeResetter) Handle(runtime Runtime) []controlsession.Event {
	runtime.AllowTunnelRuntimeStart()
	return nil
}

type ControlCloser struct{}

func (ControlCloser) Handle(runtime Runtime, action controlsession.ActionCloseControlConn) []controlsession.Event {
	if conn := runtime.Conn(); conn != nil {
		_ = conn.Close()
	}
	return []controlsession.Event{controlsession.ControlConnClosed{Reason: action.Reason}}
}

func protocolError(err error) []controlsession.Event {
	return []controlsession.Event{controlsession.ProtocolErrorDetected{Reason: err.Error()}}
}

func closeControlConn(runtime Runtime, err error) []controlsession.Event {
	if conn := runtime.Conn(); conn != nil {
		_ = conn.Close()
	}
	return []controlsession.Event{controlsession.ControlConnClosed{Reason: err.Error()}}
}

func BlockReasonForRuntimeError(err error) controlsession.BlockReason {
	var effectiveIPErr *controlruntime.GroupEffectiveIPStartError
	if errors.As(err, &effectiveIPErr) {
		switch effectiveIPErr.Kind {
		case controlruntime.GroupEffectiveIPStartErrorInvalid:
			return controlsession.BlockReasonEffectiveIPInvalid
		case controlruntime.GroupEffectiveIPStartErrorNotLocal:
			return controlsession.BlockReasonEffectiveIPNotLocal
		}
	}

	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "端口冲突"), strings.Contains(message, "conflict"):
		return controlsession.BlockReasonPortConflict
	default:
		return controlsession.BlockReasonListenerStartFailed
	}
}

func BindingFailureEvents(keys []controlsession.BindingKey, err error, epochs ...uint64) []controlsession.Event {
	reason := BlockReasonForRuntimeError(err)
	message := err.Error()
	var epoch uint64
	if len(epochs) != 0 {
		epoch = epochs[0]
	}
	events := make([]controlsession.Event, 0, len(keys))
	for _, key := range keys {
		events = append(events, controlsession.BindingStartFailed{
			Key:     key,
			Epoch:   epoch,
			Reason:  reason,
			Message: message,
		})
	}
	return events
}

func BindingOutcomeEvents(state controlsession.SessionState, keys []controlsession.BindingKey, activeTunnelIDs map[uint32]struct{}, issues map[int64]string, epochs ...uint64) []controlsession.Event {
	var epoch uint64
	if len(epochs) != 0 {
		epoch = epochs[0]
	}
	events := make([]controlsession.Event, 0, len(keys))
	for _, key := range keys {
		tunnelID := TunnelIDForBinding(state, key)
		if _, ok := activeTunnelIDs[tunnelID]; ok {
			events = append(events, controlsession.BindingStarted{Key: key, Epoch: epoch})
			continue
		}

		message := strings.TrimSpace(issues[int64(tunnelID)])
		if message == "" {
			message = fmt.Sprintf("%s listener did not start", strings.ToUpper(key.Protocol))
		}
		events = append(events, controlsession.BindingStartFailed{
			Key:     key,
			Epoch:   epoch,
			Reason:  BlockReasonForRuntimeError(errors.New(message)),
			Message: message,
		})
	}
	return events
}

func TunnelIDForBinding(state controlsession.SessionState, key controlsession.BindingKey) uint32 {
	if state.Applied == nil {
		return 0
	}

	for _, tunnel := range state.Applied.Snapshot.Tunnels {
		if !tunnel.Enabled || tunnel.Protocol != key.Protocol {
			continue
		}
		if key.Port >= tunnel.RemoteStart && key.Port <= tunnel.RemoteEnd {
			return tunnel.TunnelID
		}
	}
	return 0
}
