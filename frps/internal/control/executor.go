package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlconfigsync "github.com/zightch/frp/frps/internal/control/protocol/configsync"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type runtimeExecutor struct {
	groupID int64
	conn    net.Conn
	logger  *slog.Logger
	session *sessionState

	mu              sync.Mutex
	desiredGroup    GroupRuntime
	drainedStreams  map[uint32]*publicStream
	drainedSessions []*publicUDPSession
}

type serverActionExecutor struct {
	server *Server
}

func (s *Server) unregisterRuntimeExecutor(sessionID uint64) {
	if s == nil || s.supervisor == nil || sessionID == 0 {
		return
	}
	s.supervisor.DetachRuntime(sessionID)
}

func (s *Server) runtimeExecutor(sessionID uint64) *runtimeExecutor {
	if s == nil || s.supervisor == nil || sessionID == 0 {
		return nil
	}
	return s.supervisor.RuntimeExecutor(sessionID)
}

func (r *runtimeExecutor) desiredGroupRuntime() GroupRuntime {
	if r == nil {
		return GroupRuntime{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.desiredGroup
}

func (r *runtimeExecutor) RuntimeGroupID() int64 {
	if r == nil {
		return 0
	}
	return r.groupID
}

func (r *runtimeExecutor) RuntimeConn() net.Conn {
	if r == nil {
		return nil
	}
	return r.conn
}

func (r *runtimeExecutor) RuntimeSession() controlruntime.SessionStateProjectionTarget {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session
}

func (r *runtimeExecutor) RuntimeSnapshot(state controlsession.SessionState) controlruntime.SessionSnapshot {
	return r.snapshot(state)
}

func (r *runtimeExecutor) snapshot(state controlsession.SessionState) controlruntime.SessionSnapshot {
	if r == nil {
		return controlruntime.SessionSnapshot{}
	}

	_, runtimeState := r.session.observeState()
	return controlruntime.SessionSnapshot{
		GroupID:      r.groupID,
		SessionID:    state.SessionID,
		Conn:         r.conn,
		DesiredGroup: r.desiredGroupRuntime(),
		RecoveryMode: r.session.RecoveryModeValue(),
		State:        state,
		Runtime:      runtimeState,
	}
}

func (r *runtimeExecutor) setDesiredGroup(group GroupRuntime) {
	if r == nil {
		return
	}

	r.mu.Lock()
	r.desiredGroup = group
	r.mu.Unlock()
}

func (r *runtimeExecutor) storeDrained(streams map[uint32]*publicStream, udpSessions []*publicUDPSession) {
	if r == nil {
		return
	}

	r.mu.Lock()
	if len(streams) != 0 {
		if r.drainedStreams == nil {
			r.drainedStreams = make(map[uint32]*publicStream)
		}
		for streamID, stream := range streams {
			r.drainedStreams[streamID] = stream
		}
	}
	if len(udpSessions) != 0 {
		r.drainedSessions = append(r.drainedSessions, udpSessions...)
	}
	r.mu.Unlock()
}

func (r *runtimeExecutor) takeDrainedStreams() map[uint32]*publicStream {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	streams := r.drainedStreams
	r.drainedStreams = nil
	r.mu.Unlock()
	return streams
}

func (r *runtimeExecutor) takeDrainedUDPSessions() []*publicUDPSession {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	sessions := r.drainedSessions
	r.drainedSessions = nil
	r.mu.Unlock()
	return sessions
}

func (e serverActionExecutor) Execute(_ context.Context, state controlsession.SessionState, action controlsession.Action) []controlsession.Event {
	if e.server == nil {
		return nil
	}

	runtime := e.server.runtimeExecutor(state.SessionID)
	if runtime == nil || runtime.session == nil || runtime.conn == nil {
		return nil
	}

	switch typed := action.(type) {
	case controlsession.ActionSendServerHello:
		body, err := protocol.MarshalServerHello(protocol.ServerHello{
			HeartbeatIntervalMs: uint32(e.server.options.HeartbeatInterval.Milliseconds()),
			SessionID:           state.SessionID,
			CapabilityBits:      0,
			ServerVersion:       e.server.version,
		})
		if err != nil {
			return []controlsession.Event{controlsession.ProtocolErrorDetected{Reason: err.Error()}}
		}
		if err := e.server.writeFrameWithSession(runtime.conn, runtime.session, protocol.Frame{
			Type:      protocol.TypeServerHello,
			RequestID: typed.RequestID,
			Body:      body,
		}); err != nil {
			_ = runtime.conn.Close()
			return []controlsession.Event{controlsession.ControlConnClosed{Reason: err.Error()}}
		}
		return nil

	case controlsession.ActionPushConfig:
		group := runtime.desiredGroupRuntime()
		snapshot := controlruntime.ConfigSnapshotFromDesired(typed.Snapshot)
		group.Snapshot = snapshot

		frame, err := controlconfigsync.BuildPushFrame(typed.RequestID, snapshot)
		if err != nil {
			return []controlsession.Event{controlsession.ProtocolErrorDetected{Reason: err.Error()}}
		}

		if err := e.server.writeFrameWithSession(runtime.conn, runtime.session, frame); err != nil {
			_ = runtime.conn.Close()
			return []controlsession.Event{controlsession.ControlConnClosed{Reason: err.Error()}}
		}
		return nil

	case controlsession.ActionSendConfigError:
		if err := e.server.writeErrorWithSession(
			runtime.conn,
			runtime.session,
			typed.RequestID,
			0,
			protocol.ErrorCodeProtocolBadBody,
			false,
			typed.Message,
		); err != nil {
			_ = runtime.conn.Close()
			return []controlsession.Event{controlsession.ControlConnClosed{Reason: err.Error()}}
		}
		return nil

	case controlsession.ActionSendHeartbeatPong:
		body, err := protocol.MarshalHeartbeatPong(protocol.HeartbeatPong{
			ClientUnixMs: typed.ClientUnixMs,
			ServerUnixMs: uint64(e.server.clock.Now().UnixMilli()),
		})
		if err != nil {
			return []controlsession.Event{controlsession.ProtocolErrorDetected{Reason: err.Error()}}
		}
		if err := e.server.writeFrameWithSession(runtime.conn, runtime.session, protocol.Frame{
			Type:      protocol.TypeHeartbeatPong,
			RequestID: typed.RequestID,
			Body:      body,
		}); err != nil {
			_ = runtime.conn.Close()
			return []controlsession.Event{controlsession.ControlConnClosed{Reason: err.Error()}}
		}
		return nil

	case controlsession.ActionPrepareBindings:
		group := runtime.desiredGroupRuntime()
		group.EffectiveIP = typed.EffectiveIP
		group.Snapshot = controlruntime.ConfigSnapshotFromDesired(typed.Snapshot)
		bindIP, err := e.server.resolveGroupEffectiveIP(group)
		if err != nil {
			return []controlsession.Event{
				controlsession.BindingsPreparationFailed{
					Reason:  blockReasonForRuntimeError(err),
					Message: controlruntime.BuildGroupEffectiveIPRuntimeReason(group, err),
				},
			}
		}
		return []controlsession.Event{
			controlsession.BindingsPrepared{
				Keys: controlbind.ExpandBindings(bindIP, typed.Snapshot.Tunnels),
			},
		}

	case controlsession.ActionStartBindings:
		if err := e.server.ensureTunnelListeners(runtime.conn, runtime.logger, runtime.session); err != nil {
			return bindingFailureEvents(typed.Keys, err)
		}
		return bindingOutcomeEvents(state, typed.Keys, runtime.session.ActiveRuntimeTunnelIDs(), e.server.TunnelRuntimeIssues())

	case controlsession.ActionStopBindings:
		listeners, udpListeners, streams, udpSessions := runtime.session.freezeTunnelRuntime()
		controlruntime.CloseStartedTunnelListeners(listeners, udpListeners)
		runtime.storeDrained(streams, udpSessions)
		return nil

	case controlsession.ActionDrainStreams:
		for streamID, stream := range runtime.takeDrainedStreams() {
			if err := e.server.sendStreamClose(runtime.conn, runtime.session, streamID, protocol.CloseReasonAdminTerminated, typed.Reason); err != nil {
				if runtime.logger != nil {
					runtime.logger.Warn("stream drain close failed", "stream_id", streamID, "error", err)
				}
			}
			stream.SignalReady(net.ErrClosed)
			stream.Close()
		}
		return nil

	case controlsession.ActionDrainUDPSessions:
		for _, udpSession := range runtime.takeDrainedUDPSessions() {
			if udpSession == nil {
				continue
			}
			if err := e.server.sendUDPClose(runtime.conn, runtime.session, udpSession.SessionID, protocol.CloseReasonAdminTerminated, typed.Reason); err != nil {
				if runtime.logger != nil {
					runtime.logger.Warn("udp drain close failed", "udp_session_id", udpSession.SessionID, "error", err)
				}
			}
		}
		return nil

	case controlsession.ActionResetRuntime:
		runtime.session.allowTunnelRuntimeStart()
		return nil

	case controlsession.ActionCloseControlConn:
		_ = runtime.conn.Close()
		return []controlsession.Event{controlsession.ControlConnClosed{Reason: typed.Reason}}

	default:
		return nil
	}
}

func blockReasonForRuntimeError(err error) controlsession.BlockReason {
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

func bindingFailureEvents(keys []controlsession.BindingKey, err error) []controlsession.Event {
	reason := blockReasonForRuntimeError(err)
	message := err.Error()
	events := make([]controlsession.Event, 0, len(keys))
	for _, key := range keys {
		events = append(events, controlsession.BindingStartFailed{
			Key:     key,
			Reason:  reason,
			Message: message,
		})
	}
	return events
}

func bindingOutcomeEvents(state controlsession.SessionState, keys []controlsession.BindingKey, activeTunnelIDs map[uint32]struct{}, issues map[int64]string) []controlsession.Event {
	events := make([]controlsession.Event, 0, len(keys))
	for _, key := range keys {
		tunnelID := tunnelIDForBinding(state, key)
		if _, ok := activeTunnelIDs[tunnelID]; ok {
			events = append(events, controlsession.BindingStarted{Key: key})
			continue
		}

		message := strings.TrimSpace(issues[int64(tunnelID)])
		if message == "" {
			message = fmt.Sprintf("%s listener did not start", strings.ToUpper(key.Protocol))
		}
		events = append(events, controlsession.BindingStartFailed{
			Key:     key,
			Reason:  blockReasonForRuntimeError(errors.New(message)),
			Message: message,
		})
	}
	return events
}

func tunnelIDForBinding(state controlsession.SessionState, key controlsession.BindingKey) uint32 {
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
