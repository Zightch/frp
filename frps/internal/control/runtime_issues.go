package control

import (
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) TunnelRuntimeIssues() map[int64]string {
	if s == nil || s.runtimeIssues == nil {
		return nil
	}
	return s.runtimeIssues.SnapshotReasons()
}

func (s *Server) clearTunnelRuntimeIssues(tunnels []protocol.TunnelEntry) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.ClearTunnels(tunnels)
}

func (s *Server) recordTunnelRuntimeIssue(tunnelID uint32, reason string) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.Record(tunnelID, reason)
}

func (s *Server) recordTunnelRuntimeIssueForConfig(tunnelID uint32, configVersion uint64, reason string) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.RecordForConfig(tunnelID, configVersion, reason)
}

func (s *Server) clearUnknownTunnelRuntimeIssues(knownTunnelIDs map[int64]struct{}) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.ClearUnknown(knownTunnelIDs)
}

func (s *Server) applyScannedTunnelRuntimeIssues(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{}) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.ApplyScanResult(snapshot, staticConflictIDs, issues, preserved)
}

func (s *Server) RecordTunnelRuntimeIssueForConfig(tunnelID uint32, configVersion uint64, reason string) {
	s.recordTunnelRuntimeIssueForConfig(tunnelID, configVersion, reason)
}

func (s *Server) ClearUnknownTunnelRuntimeIssues(knownTunnelIDs map[int64]struct{}) {
	s.clearUnknownTunnelRuntimeIssues(knownTunnelIDs)
}

func (s *Server) ApplyScannedTunnelRuntimeIssues(snapshot controlruntime.ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{}) {
	s.applyScannedTunnelRuntimeIssues(snapshot, staticConflictIDs, issues, preserved)
}
