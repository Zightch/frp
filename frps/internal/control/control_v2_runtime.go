package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"

	"github.com/zightch/frp/frps/internal/controlv2/bind"
	"github.com/zightch/frp/frps/internal/controlv2/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type controlV2Runtime struct {
	conn    net.Conn
	logger  *slog.Logger
	session *sessionState

	mu              sync.Mutex
	desiredGroup    GroupRuntime
	drainedStreams  map[uint32]*publicStream
	drainedSessions []*publicUDPSession
}

type controlV2Executor struct {
	server *Server
}

func (s *Server) registerControlV2Runtime(sessionID uint64, runtime *controlV2Runtime) {
	if s == nil || runtime == nil || sessionID == 0 {
		return
	}

	s.v2Mu.Lock()
	s.v2Sessions[sessionID] = runtime
	s.v2Mu.Unlock()
}

func (s *Server) unregisterControlV2Runtime(sessionID uint64) {
	if s == nil || sessionID == 0 {
		return
	}

	s.v2Mu.Lock()
	delete(s.v2Sessions, sessionID)
	s.v2Mu.Unlock()
}

func (s *Server) controlV2Runtime(sessionID uint64) *controlV2Runtime {
	if s == nil || sessionID == 0 {
		return nil
	}

	s.v2Mu.RLock()
	runtime := s.v2Sessions[sessionID]
	s.v2Mu.RUnlock()
	return runtime
}

func (r *controlV2Runtime) desiredGroupRuntime() GroupRuntime {
	if r == nil {
		return GroupRuntime{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.desiredGroup
}

func (r *controlV2Runtime) setDesiredGroup(group GroupRuntime) {
	if r == nil {
		return
	}

	r.mu.Lock()
	r.desiredGroup = group
	r.mu.Unlock()
}

func (r *controlV2Runtime) storeDrained(streams map[uint32]*publicStream, udpSessions []*publicUDPSession) {
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

func (r *controlV2Runtime) takeDrainedStreams() map[uint32]*publicStream {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	streams := r.drainedStreams
	r.drainedStreams = nil
	r.mu.Unlock()
	return streams
}

func (r *controlV2Runtime) takeDrainedUDPSessions() []*publicUDPSession {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	sessions := r.drainedSessions
	r.drainedSessions = nil
	r.mu.Unlock()
	return sessions
}

func (e controlV2Executor) Execute(_ context.Context, state session.SessionState, action session.Action) []session.Event {
	if e.server == nil {
		return nil
	}

	runtime := e.server.controlV2Runtime(state.SessionID)
	if runtime == nil || runtime.session == nil || runtime.conn == nil {
		return nil
	}

	switch typed := action.(type) {
	case session.ActionSendServerHello:
		body, err := protocol.MarshalServerHello(protocol.ServerHello{
			HeartbeatIntervalMs: uint32(e.server.options.HeartbeatInterval.Milliseconds()),
			SessionID:           state.SessionID,
			CapabilityBits:      0,
			ServerVersion:       e.server.version,
		})
		if err != nil {
			return []session.Event{session.ProtocolErrorDetected{Reason: err.Error()}}
		}
		if err := e.server.writeFrameWithSession(runtime.conn, runtime.session, protocol.Frame{
			Type:      protocol.TypeServerHello,
			RequestID: typed.RequestID,
			Body:      body,
		}); err != nil {
			_ = runtime.conn.Close()
			return []session.Event{session.ControlConnClosed{Reason: err.Error()}}
		}
		return nil

	case session.ActionPushConfig:
		group := runtime.desiredGroupRuntime()
		snapshot := configSnapshotFromDesired(typed.Snapshot)
		group.Snapshot = snapshot
		if err := runtime.session.reconfigure(group, snapshot, typed.RequestID); err != nil {
			return []session.Event{session.ProtocolErrorDetected{Reason: err.Error()}}
		}

		body, err := protocol.MarshalConfigPush(protocol.ConfigPush{
			ConfigVersion: snapshot.Version,
			GeneratedAtMs: snapshot.GeneratedAtMs,
			Tunnels:       snapshot.Tunnels,
		})
		if err != nil {
			runtime.session.clearPendingConfigRequest(typed.RequestID)
			return []session.Event{session.ProtocolErrorDetected{Reason: err.Error()}}
		}

		if err := e.server.writeFrameWithSession(runtime.conn, runtime.session, protocol.Frame{
			Type:      protocol.TypeConfigPush,
			RequestID: typed.RequestID,
			Body:      body,
		}); err != nil {
			runtime.session.clearPendingConfigRequest(typed.RequestID)
			_ = runtime.conn.Close()
			return []session.Event{session.ControlConnClosed{Reason: err.Error()}}
		}
		return nil

	case session.ActionSendConfigError:
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
			return []session.Event{session.ControlConnClosed{Reason: err.Error()}}
		}
		return nil

	case session.ActionSendHeartbeatPong:
		body, err := protocol.MarshalHeartbeatPong(protocol.HeartbeatPong{
			ClientUnixMs: typed.ClientUnixMs,
			ServerUnixMs: uint64(e.server.clock.Now().UnixMilli()),
		})
		if err != nil {
			return []session.Event{session.ProtocolErrorDetected{Reason: err.Error()}}
		}
		if err := e.server.writeFrameWithSession(runtime.conn, runtime.session, protocol.Frame{
			Type:      protocol.TypeHeartbeatPong,
			RequestID: typed.RequestID,
			Body:      body,
		}); err != nil {
			_ = runtime.conn.Close()
			return []session.Event{session.ControlConnClosed{Reason: err.Error()}}
		}
		return nil

	case session.ActionPrepareBindings:
		group := runtime.desiredGroupRuntime()
		group.EffectiveIP = typed.EffectiveIP
		group.Snapshot = configSnapshotFromDesired(typed.Snapshot)
		bindIP, err := e.server.resolveGroupEffectiveIP(group)
		if err != nil {
			return []session.Event{
				session.BindingsPreparationFailed{
					Reason:  blockReasonForRuntimeError(err),
					Message: buildGroupEffectiveIPRuntimeReason(group, err),
				},
			}
		}
		return []session.Event{
			session.BindingsPrepared{
				Keys: bind.ExpandBindings(bindIP, typed.Snapshot.Tunnels),
			},
		}

	case session.ActionStartBindings:
		if err := e.server.ensureTunnelListeners(runtime.conn, runtime.logger, runtime.session); err != nil {
			return bindingFailureEvents(typed.Keys, err)
		}
		return bindingOutcomeEvents(state, typed.Keys, runtime.session.activeRuntimeTunnelIDs(), e.server.TunnelRuntimeIssues())

	case session.ActionStopBindings:
		listeners, udpListeners, streams, udpSessions := runtime.session.freezeTunnelRuntime()
		closeStartedTunnelListeners(listeners, udpListeners)
		runtime.storeDrained(streams, udpSessions)
		return nil

	case session.ActionDrainStreams:
		for streamID, stream := range runtime.takeDrainedStreams() {
			if err := e.server.sendStreamClose(runtime.conn, runtime.session, streamID, protocol.CloseReasonAdminTerminated, typed.Reason); err != nil {
				if runtime.logger != nil {
					runtime.logger.Warn("stream drain close failed", "stream_id", streamID, "error", err)
				}
			}
			stream.signalReady(net.ErrClosed)
			stream.close()
		}
		return nil

	case session.ActionDrainUDPSessions:
		for _, udpSession := range runtime.takeDrainedUDPSessions() {
			if udpSession == nil {
				continue
			}
			if err := e.server.sendUDPClose(runtime.conn, runtime.session, udpSession.sessionID, protocol.CloseReasonAdminTerminated, typed.Reason); err != nil {
				if runtime.logger != nil {
					runtime.logger.Warn("udp drain close failed", "udp_session_id", udpSession.sessionID, "error", err)
				}
			}
		}
		return nil

	case session.ActionResetRuntime:
		runtime.session.allowTunnelRuntimeStart()
		return nil

	case session.ActionCloseControlConn:
		_ = runtime.conn.Close()
		return []session.Event{session.ControlConnClosed{Reason: typed.Reason}}

	default:
		return nil
	}
}

func desiredRuntimeFromGroup(group GroupRuntime) session.DesiredRuntimeSnapshot {
	snapshot := runtimeSnapshotForGroup(group)
	return session.DesiredRuntimeSnapshot{
		Version:       snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		EffectiveIP:   group.EffectiveIP,
		Tunnels:       desiredTunnelsFromConfig(snapshot.Tunnels),
	}
}

func desiredTunnelsFromConfig(tunnels []protocol.TunnelEntry) []session.DesiredTunnelRuntime {
	desired := make([]session.DesiredTunnelRuntime, 0, len(tunnels))
	for _, tunnel := range tunnels {
		desired = append(desired, session.DesiredTunnelRuntime{
			TunnelID:    tunnel.TunnelID,
			Protocol:    protocolName(tunnel.Protocol),
			Enabled:     tunnel.TunnelFlags&protocol.TunnelFlagEnabled != 0,
			RemoteStart: tunnel.RemoteStart,
			RemoteEnd:   tunnel.RemoteEnd,
			LocalHost:   tunnel.LocalHost.String(),
			LocalStart:  tunnel.LocalStart,
			LocalEnd:    tunnel.LocalEnd,
		})
	}
	return desired
}

func configSnapshotFromDesired(snapshot session.DesiredRuntimeSnapshot) ConfigSnapshot {
	return ConfigSnapshot{
		Version:       snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		Tunnels:       configTunnelsFromDesired(snapshot.Tunnels),
	}
}

func configTunnelsFromDesired(tunnels []session.DesiredTunnelRuntime) []protocol.TunnelEntry {
	configured := make([]protocol.TunnelEntry, 0, len(tunnels))
	for _, tunnel := range tunnels {
		host, _ := protocol.ParseHost(tunnel.LocalHost)
		flags := uint8(0)
		if tunnel.Enabled {
			flags |= protocol.TunnelFlagEnabled
		}
		if tunnel.RemoteStart != tunnel.RemoteEnd || tunnel.LocalStart != tunnel.LocalEnd {
			flags |= protocol.TunnelFlagRange
		}
		configured = append(configured, protocol.TunnelEntry{
			TunnelID:    tunnel.TunnelID,
			Protocol:    protocolValue(tunnel.Protocol),
			TunnelFlags: flags,
			RemoteStart: tunnel.RemoteStart,
			RemoteEnd:   tunnel.RemoteEnd,
			LocalHost:   host,
			LocalStart:  tunnel.LocalStart,
			LocalEnd:    tunnel.LocalEnd,
		})
	}
	return configured
}

func protocolValue(value string) uint8 {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tcp":
		return protocol.ProtocolTCP
	case "udp":
		return protocol.ProtocolUDP
	default:
		return 0
	}
}

func blockReasonForRuntimeError(err error) session.BlockReason {
	var effectiveIPErr *groupEffectiveIPStartError
	if errors.As(err, &effectiveIPErr) {
		switch effectiveIPErr.Kind {
		case groupEffectiveIPStartErrorInvalid:
			return session.BlockReasonEffectiveIPInvalid
		case groupEffectiveIPStartErrorNotLocal:
			return session.BlockReasonEffectiveIPNotLocal
		}
	}

	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "端口冲突"), strings.Contains(message, "conflict"):
		return session.BlockReasonPortConflict
	default:
		return session.BlockReasonListenerStartFailed
	}
}

func bindingFailureEvents(keys []session.BindingKey, err error) []session.Event {
	reason := blockReasonForRuntimeError(err)
	message := err.Error()
	events := make([]session.Event, 0, len(keys))
	for _, key := range keys {
		events = append(events, session.BindingStartFailed{
			Key:     key,
			Reason:  reason,
			Message: message,
		})
	}
	return events
}

func bindingOutcomeEvents(state session.SessionState, keys []session.BindingKey, activeTunnelIDs map[uint32]struct{}, issues map[int64]string) []session.Event {
	events := make([]session.Event, 0, len(keys))
	for _, key := range keys {
		tunnelID := tunnelIDForBinding(state, key)
		if _, ok := activeTunnelIDs[tunnelID]; ok {
			events = append(events, session.BindingStarted{Key: key})
			continue
		}

		message := strings.TrimSpace(issues[int64(tunnelID)])
		if message == "" {
			message = fmt.Sprintf("%s listener did not start", strings.ToUpper(key.Protocol))
		}
		events = append(events, session.BindingStartFailed{
			Key:     key,
			Reason:  blockReasonForRuntimeError(errors.New(message)),
			Message: message,
		})
	}
	return events
}

func tunnelIDForBinding(state session.SessionState, key session.BindingKey) uint32 {
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
