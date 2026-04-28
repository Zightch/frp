package control

import (
	"context"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) runtimeSnapshotIndex() controlruntime.RuntimeSnapshotIndex {
	if s == nil || s.supervisor == nil {
		return controlruntime.RuntimeSnapshotIndex{}
	}
	return controlruntime.NewRuntimeSnapshotIndex(s.supervisor.Snapshot(nil).sessions)
}

func (s *Server) RuntimeSnapshotIndex() controlruntime.RuntimeSnapshotIndex {
	return s.runtimeSnapshotIndex()
}

func (s *Server) startRuntimeIssuePolling(parent context.Context) {
	controlruntime.StartRuntimeIssuePolling(s, parent)
}

func (s *Server) scanNonListeningTunnelRuntimeIssues(ctx context.Context) error {
	return controlruntime.ScanNonListeningTunnelRuntimeIssues(s, ctx)
}

func (s *Server) BeginRuntimeScanRound() bool {
	if s == nil {
		return false
	}

	s.runtimeScanStateMu.Lock()
	defer s.runtimeScanStateMu.Unlock()
	if s.runtimeScanInFlight {
		return false
	}
	s.runtimeScanInFlight = true
	return true
}

func (s *Server) FinishRuntimeScanRound() {
	if s == nil {
		return
	}

	s.runtimeScanStateMu.Lock()
	s.runtimeScanInFlight = false
	s.runtimeScanStateMu.Unlock()
}

func (s *Server) recoverScannedActiveSessionTunnels(viewIndex controlruntime.RuntimeSnapshotIndex, group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) error {
	return controlruntime.RecoverScannedActiveSessionTunnels(s, viewIndex, group, targetTunnels, staticConflictIDs, issues)
}

func (s *Server) requestAuditedSessionRuntimeRecovery(sessionID uint64, targetTunnels []protocol.TunnelEntry) error {
	return controlruntime.RequestAuditedSessionRuntimeRecovery(s, sessionID, targetTunnels)
}

func (s *Server) probeTunnelRuntimeIssue(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string {
	return controlruntime.ProbeTunnelRuntimeIssue(s, groupID, bindIP, tunnel)
}

func (s *Server) SetRuntimeScanCancel(cancel context.CancelFunc) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isShuttingDown() {
		if cancel != nil {
			cancel()
		}
		return
	}
	s.runtimeScanCancel = cancel
}
