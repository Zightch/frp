package wiring

import (
	"context"

	controlobserve "github.com/zightch/frp/frps/internal/control/runtime/observe"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

func (s *Server) ObserveState() testsupport.ServerObservedState {
	if s == nil {
		return testsupport.ServerObservedState{}
	}

	s.mu.Lock()
	initialRuntimeScanDone := s.initialRuntimeScanDone
	controlListenerOpen := s.controlListenerOpen
	s.mu.Unlock()

	supervisorSnapshot := supervisorSnapshot{}
	if s.supervisor != nil {
		supervisorSnapshot = s.supervisor.Snapshot(nil)
	}

	return controlobserve.Build(controlobserve.Input{
		Lifecycle: controlobserve.LifecycleSnapshot{
			InitialRuntimeScanDone: initialRuntimeScanDone,
			ControlListenerOpen:    controlListenerOpen,
		},
		GroupSlots:    supervisorSnapshot.groupSlots,
		Sessions:      supervisorSnapshot.sessions,
		Groups:        s.observeGroups(),
		RuntimeIssues: s.TunnelRuntimeIssues(),
	})
}

func (s *Server) observeGroups() []GroupRuntime {
	if s == nil || s.repo == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.options.ReadTimeout)
	defer cancel()

	groups, err := s.repo.ListGroupRuntimes(ctx)
	if err != nil {
		return nil
	}
	return groups
}
