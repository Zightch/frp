package session

func Reconcile(state SessionState) (SessionState, []Action) {
	next := cloneState(state)

	if next.Phase == SessionPhaseClosed || !next.Conn.Attached {
		return next, nil
	}
	if next.Pending != nil || next.Desired == nil {
		return next, nil
	}

	desired := *next.Desired
	if next.Applied == nil || !snapshotsEqual(next.Applied.Snapshot, desired) {
		actions := make([]Action, 0, 4)
		if next.Applied != nil {
			oldEpoch := next.Epoch
			next.Epoch++
			next.RuntimePhase = RuntimePhaseRecovering
			actions = append(actions,
				ActionStopBindings{Keys: bindingKeys(next.Bindings), Epoch: oldEpoch},
				ActionDrainStreams{Reason: "config update pending"},
				ActionDrainUDPSessions{Reason: "config update pending"},
			)
		}
		requestID := nextRequestID(&next)
		next.Pending = &PendingConfigPush{
			RequestID: requestID,
			Snapshot:  desired,
		}
		next.Phase = SessionPhaseSyncingConfig
		actions = append(actions, ActionPushConfig{
			RequestID: requestID,
			Snapshot:  desired,
		})
		return next, actions
	}

	if !desiredHasEnabledTunnels(next.Applied.Snapshot) {
		if len(next.Bindings) == 0 && len(next.UDPSessions) == 0 {
			next.RuntimePhase = RuntimePhaseEmpty
			next.BlockReason = BlockReasonNone
			next.Phase = SessionPhaseOnline
			return next, nil
		}
		next.RuntimePhase = RuntimePhaseEmpty
		next.BlockReason = BlockReasonNone
		next.Phase = SessionPhaseOnline
		return next, []Action{
			ActionStopBindings{Keys: bindingKeys(next.Bindings), Epoch: next.Epoch},
			ActionDrainStreams{Reason: "empty runtime"},
			ActionDrainUDPSessions{Reason: "empty runtime"},
			ActionResetRuntime{},
		}
	}

	if next.RuntimePhase == RuntimePhaseActive && allBindingsActive(next.Bindings) {
		next.BlockReason = BlockReasonNone
		next.Phase = SessionPhaseOnline
		return next, nil
	}

	next.RuntimePhase = RuntimePhaseBinding
	next.BlockReason = BlockReasonNone
	next.Phase = SessionPhaseOnline
	return next, []Action{
		ActionPrepareBindings{
			EffectiveIP: desired.EffectiveIP,
			Snapshot:    desired,
			Epoch:       next.Epoch,
		},
	}
}
