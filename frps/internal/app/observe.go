package app

import "github.com/zightch/frp/frps/pkg/testsupport"

func (a *App) ObserveState() testsupport.ObservedState {
	if a == nil {
		return testsupport.ObservedState{}
	}

	var state testsupport.ObservedState
	if a.control != nil {
		state.Server = a.control.ObserveState()
		state.App.InitialRuntimeScanDone = state.Server.InitialRuntimeScanDone
		state.App.ControlListenerOpen = state.Server.ControlListenerOpen
		state.App.LoginGateOpen = state.Server.LoginGateOpen
	}
	if a.api != nil {
		state.App.ManagementAPIVisible = a.api.Visible()
	}
	if a.network != nil {
		snapshotState := a.network.ObserveState()
		state.Snapshot = &snapshotState
	}
	return state
}
