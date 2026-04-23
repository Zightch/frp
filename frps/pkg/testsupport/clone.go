package testsupport

func CloneObservedState(source ObservedState) ObservedState {
	cloned := source
	cloned.Server = cloneServerObservedState(source.Server)
	if source.Snapshot != nil {
		snapshot := *source.Snapshot
		if len(snapshot.AvailableIPs) > 0 {
			snapshot.AvailableIPs = append([]string(nil), snapshot.AvailableIPs...)
		}
		cloned.Snapshot = &snapshot
	}
	if source.Listeners != nil {
		listeners := cloneListenerWorldObservedState(*source.Listeners)
		cloned.Listeners = &listeners
	}
	if source.Transport != nil {
		transportState := cloneTransportObservedState(*source.Transport)
		cloned.Transport = &transportState
	}
	return cloned
}

func cloneServerObservedState(source ServerObservedState) ServerObservedState {
	cloned := source
	if len(source.GroupSlots) > 0 {
		cloned.GroupSlots = make(map[int64]uint64, len(source.GroupSlots))
		for groupID, sessionID := range source.GroupSlots {
			cloned.GroupSlots[groupID] = sessionID
		}
	}
	if len(source.Sessions) > 0 {
		cloned.Sessions = append([]SessionObservedState(nil), source.Sessions...)
		for index := range cloned.Sessions {
			if cloned.Sessions[index].Pending != nil {
				pending := *cloned.Sessions[index].Pending
				cloned.Sessions[index].Pending = &pending
			}
		}
	}
	if len(source.Tunnels) > 0 {
		cloned.Tunnels = append([]TunnelObservedState(nil), source.Tunnels...)
	}
	if len(source.Listeners) > 0 {
		cloned.Listeners = append([]AttachedListenerObservedState(nil), source.Listeners...)
	}
	if len(source.MissingListeners) > 0 {
		cloned.MissingListeners = append([]MissingListenerObservedState(nil), source.MissingListeners...)
		for index := range cloned.MissingListeners {
			if len(cloned.MissingListeners[index].MissingPorts) > 0 {
				cloned.MissingListeners[index].MissingPorts = append([]uint16(nil), cloned.MissingListeners[index].MissingPorts...)
			}
		}
	}
	if len(source.Connections) > 0 {
		cloned.Connections = append([]ConnectionObservedState(nil), source.Connections...)
	}
	return cloned
}

func cloneListenerWorldObservedState(source ListenerWorldObservedState) ListenerWorldObservedState {
	cloned := source
	if len(source.Occupied) > 0 {
		cloned.Occupied = append([]ListenerOccupancyObservedState(nil), source.Occupied...)
	}
	if len(source.Handles) > 0 {
		cloned.Handles = append([]ListenerHandleObservedState(nil), source.Handles...)
	}
	if len(source.Calls) > 0 {
		cloned.Calls = append([]ListenerCallObservedState(nil), source.Calls...)
	}
	return cloned
}

func cloneTransportObservedState(source TransportObservedState) TransportObservedState {
	cloned := source
	if len(source.Connections) > 0 {
		cloned.Connections = append([]TransportConnObservedState(nil), source.Connections...)
	}
	if len(source.Delivered) > 0 {
		cloned.Delivered = append([]FrameObservedState(nil), source.Delivered...)
	}
	if len(source.Delayed) > 0 {
		cloned.Delayed = append([]FrameObservedState(nil), source.Delayed...)
	}
	if len(source.Dropped) > 0 {
		cloned.Dropped = append([]FrameObservedState(nil), source.Dropped...)
	}
	if len(source.Errors) > 0 {
		cloned.Errors = append([]FrameObservedState(nil), source.Errors...)
	}
	return cloned
}
