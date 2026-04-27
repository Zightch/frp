package session

func Reduce(state SessionState, event Event) (SessionState, []Action) {
	next := cloneState(state)

	switch typed := event.(type) {
	case SessionAttached:
		next.Conn = ControlConnState{
			Attached: true,
			ConnID:   typed.ConnID,
		}
		next.Phase = SessionPhaseSyncingConfig
		return next, []Action{
			ActionSendServerHello{},
			ActionRequestReconcile{Reason: "session_attached"},
		}

	case DesiredRuntimeUpdated:
		snapshot := typed.Snapshot
		next.Desired = &snapshot
		return next, []Action{
			ActionRequestReconcile{Reason: "desired_runtime_updated"},
		}

	case NetworkSnapshotChanged:
		return next, []Action{
			ActionRequestReconcile{Reason: "network_snapshot_changed"},
		}

	case ReconcileRequested:
		return Reconcile(next)

	case ConfigAckReceived:
		if next.Pending == nil {
			return next, []Action{
				ActionSendConfigError{RequestID: typed.RequestID, Message: "unexpected config.ack"},
			}
		}
		if typed.RequestID != next.Pending.RequestID || typed.ConfigVersion != next.Pending.Snapshot.Version {
			return next, []Action{
				ActionSendConfigError{RequestID: typed.RequestID, Message: "config.ack mismatch"},
			}
		}

		applied := AppliedRuntimeSnapshot{Snapshot: next.Pending.Snapshot}
		next.Applied = &applied
		next.Pending = nil
		next.Epoch++
		next.BlockReason = BlockReasonNone
		next.Phase = SessionPhaseOnline

		if !desiredHasEnabledTunnels(applied.Snapshot) {
			next.RuntimePhase = RuntimePhaseEmpty
			return next, []Action{ActionRequestReconcile{Reason: "config_ack_empty_runtime"}}
		}

		next.RuntimePhase = RuntimePhaseBinding
		return next, []Action{
			ActionPrepareBindings{
				EffectiveIP: applied.Snapshot.EffectiveIP,
				Snapshot:    applied.Snapshot,
				Epoch:       next.Epoch,
			},
		}

	case BindingsPrepared:
		next.Bindings = make(map[BindingKey]BindingState, len(typed.Keys))
		for _, key := range typed.Keys {
			next.Bindings[key] = BindingState{
				Key:   key,
				Epoch: next.Epoch,
				Phase: BindingPhasePrepared,
			}
		}
		if len(typed.Keys) == 0 {
			next.RuntimePhase = RuntimePhaseEmpty
			next.BlockReason = BlockReasonNone
			return next, nil
		}
		next.RuntimePhase = RuntimePhaseBinding
		return next, []Action{
			ActionStartBindings{Keys: typed.Keys, Epoch: next.Epoch},
		}

	case BindingsPreparationFailed:
		next.RuntimePhase = RuntimePhaseBlocked
		next.BlockReason = typed.Reason
		next.Bindings = make(map[BindingKey]BindingState)
		return next, []Action{
			ActionLogTransition{Message: typed.Message},
		}

	case BindingStarted:
		binding := next.Bindings[typed.Key]
		binding.Key = typed.Key
		binding.Epoch = next.Epoch
		binding.Phase = BindingPhaseActive
		binding.LastError = ""
		next.Bindings[typed.Key] = binding
		if allBindingsActive(next.Bindings) {
			next.RuntimePhase = RuntimePhaseActive
			next.BlockReason = BlockReasonNone
		}
		return next, nil

	case BindingStartFailed:
		binding := next.Bindings[typed.Key]
		binding.Key = typed.Key
		binding.Epoch = next.Epoch
		binding.Phase = BindingPhaseFailed
		binding.LastError = typed.Message
		next.Bindings[typed.Key] = binding
		next.RuntimePhase = RuntimePhaseBlocked
		next.BlockReason = typed.Reason
		return next, []Action{
			ActionLogTransition{Message: typed.Message},
		}

	case BindingClosed:
		if binding, ok := next.Bindings[typed.Key]; ok {
			binding.Phase = BindingPhaseClosed
			next.Bindings[typed.Key] = binding
		}
		next.RuntimePhase = RuntimePhaseRecovering
		return next, []Action{
			ActionRequestReconcile{Reason: "binding_closed"},
		}

	case TCPAccepted:
		if next.Phase != SessionPhaseOnline || next.RuntimePhase != RuntimePhaseActive || !next.Conn.Attached {
			return next, nil
		}
		streamID := nextStreamID(&next)
		requestID := nextRequestID(&next)
		next.Streams[streamID] = TCPStreamState{
			StreamID:      streamID,
			TunnelID:      typed.TunnelID,
			Epoch:         next.Epoch,
			RemotePort:    typed.RemotePort,
			ClientAddr:    typed.ClientAddr,
			OpenRequestID: requestID,
		}
		return next, []Action{
			ActionSendStreamOpen{
				StreamID:   streamID,
				RequestID:  requestID,
				TunnelID:   typed.TunnelID,
				RemotePort: typed.RemotePort,
				ClientAddr: typed.ClientAddr,
			},
		}

	case StreamOpenedReceived:
		stream, ok := next.Streams[typed.StreamID]
		if !ok {
			return next, nil
		}
		if !typed.OK {
			delete(next.Streams, typed.StreamID)
			return next, []Action{
				ActionSendStreamClose{StreamID: typed.StreamID, Message: typed.Message},
			}
		}
		stream.Established = true
		next.Streams[typed.StreamID] = stream
		return next, nil

	case StreamClosedReceived:
		delete(next.Streams, typed.StreamID)
		return next, nil

	case StreamDataReceived:
		return next, nil

	case UDPDatagramReceived:
		if next.Phase != SessionPhaseOnline || next.RuntimePhase != RuntimePhaseActive || !next.Conn.Attached {
			return next, nil
		}
		if existing, ok := findUDPSession(next, typed.TunnelID, typed.RemotePort, typed.ClientAddr); ok {
			existing.LastPayload = typed.PayloadLen
			next.UDPSessions[existing.SessionID] = existing
			return next, []Action{
				ActionSendUDPData{SessionID: existing.SessionID, PayloadLen: typed.PayloadLen},
			}
		}

		sessionID := nextStreamID(&next)
		next.UDPSessions[sessionID] = UDPSessionState{
			SessionID:   sessionID,
			TunnelID:    typed.TunnelID,
			Epoch:       next.Epoch,
			RemotePort:  typed.RemotePort,
			ClientAddr:  typed.ClientAddr,
			Opened:      true,
			LastPayload: typed.PayloadLen,
		}
		return next, []Action{
			ActionSendUDPStart{
				SessionID:  sessionID,
				TunnelID:   typed.TunnelID,
				RemotePort: typed.RemotePort,
				ClientAddr: typed.ClientAddr,
			},
			ActionSendUDPData{SessionID: sessionID, PayloadLen: typed.PayloadLen},
		}

	case UDPIdleTimeoutReached:
		delete(next.UDPSessions, typed.SessionID)
		return next, []Action{
			ActionSendUDPClose{SessionID: typed.SessionID, Message: "udp idle timeout"},
		}

	case UDPDataReceived:
		return next, nil

	case UDPCloseReceived:
		delete(next.UDPSessions, typed.SessionID)
		return next, nil

	case HeartbeatPingReceived:
		return next, []Action{
			ActionSendHeartbeatPong{
				RequestID:    typed.RequestID,
				ClientUnixMs: typed.ClientUnixMs,
			},
		}

	case SessionTakeoverRequested:
		next.Phase = SessionPhaseDraining
		next.BlockReason = BlockReasonSessionReplaced
		next.Conn.Closing = true
		return next, []Action{
			ActionStopBindings{Keys: bindingKeys(next.Bindings), Epoch: next.Epoch},
			ActionDrainStreams{Reason: "session replaced"},
			ActionDrainUDPSessions{Reason: "session replaced"},
			ActionCloseControlConn{Reason: "session replaced"},
		}

	case ShutdownRequested:
		next.Phase = SessionPhaseDraining
		next.BlockReason = BlockReasonShutdown
		next.Conn.Closing = true
		return next, []Action{
			ActionStopBindings{Keys: bindingKeys(next.Bindings), Epoch: next.Epoch},
			ActionDrainStreams{Reason: "shutdown requested"},
			ActionDrainUDPSessions{Reason: "shutdown requested"},
			ActionCloseControlConn{Reason: "shutdown requested"},
		}

	case ControlConnClosed:
		next.Conn = ControlConnState{}
		if next.Phase == SessionPhaseDraining {
			next.Phase = SessionPhaseClosed
		} else {
			next.Phase = SessionPhaseClosed
		}
		return next, nil

	case ProtocolErrorDetected:
		next.Phase = SessionPhaseDraining
		next.Conn.Closing = true
		return next, []Action{
			ActionCloseControlConn{Reason: typed.Reason},
		}

	case DrainCompleted:
		next.Phase = SessionPhaseClosed
		next.Conn = ControlConnState{}
		return next, nil

	default:
		return next, nil
	}
}
