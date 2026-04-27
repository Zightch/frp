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
		requestID := nextRequestID(&next)
		next.Pending = &PendingConfigPush{
			RequestID: requestID,
			Snapshot:  desired,
		}
		next.Phase = SessionPhaseSyncingConfig
		return next, []Action{
			ActionPushConfig{
				RequestID: requestID,
				Snapshot:  desired,
			},
		}
	}

	if !desiredHasEnabledTunnels(next.Applied.Snapshot) {
		if len(next.Bindings) == 0 && len(next.Streams) == 0 && len(next.UDPSessions) == 0 {
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
