package control

import (
	"strings"
	"sync"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type tunnelRuntimeIssue struct {
	Reason        string
	ConfigVersion uint64
}

type runtimeIssueStore struct {
	mu      sync.RWMutex
	tunnels map[int64]tunnelRuntimeIssue
}

func newRuntimeIssueStore() *runtimeIssueStore {
	return &runtimeIssueStore{
		tunnels: make(map[int64]tunnelRuntimeIssue),
	}
}

func (s *runtimeIssueStore) snapshotReasons() map[int64]string {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.tunnels) == 0 {
		return nil
	}

	issues := make(map[int64]string, len(s.tunnels))
	for tunnelID, issue := range s.tunnels {
		issues[tunnelID] = issue.Reason
	}
	return issues
}

func (s *runtimeIssueStore) clearTunnels(tunnels []protocol.TunnelEntry) {
	if s == nil || len(tunnels) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, tunnel := range tunnels {
		delete(s.tunnels, int64(tunnel.TunnelID))
	}
}

func (s *runtimeIssueStore) record(tunnelID uint32, reason string) {
	s.recordForConfig(tunnelID, 0, reason)
}

func (s *runtimeIssueStore) recordForConfig(tunnelID uint32, configVersion uint64, reason string) {
	if s == nil || tunnelID == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.tunnels[int64(tunnelID)]
	if ok && configVersion != 0 && current.ConfigVersion > configVersion {
		return
	}

	trimmedReason := strings.TrimSpace(reason)
	if trimmedReason == "" {
		delete(s.tunnels, int64(tunnelID))
		return
	}

	s.tunnels[int64(tunnelID)] = tunnelRuntimeIssue{
		Reason:        trimmedReason,
		ConfigVersion: configVersion,
	}
}

func (s *runtimeIssueStore) clearUnknown(knownTunnelIDs map[int64]struct{}) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for tunnelID := range s.tunnels {
		if _, ok := knownTunnelIDs[tunnelID]; ok {
			continue
		}
		delete(s.tunnels, tunnelID)
	}
}

func (s *runtimeIssueStore) applyScanResult(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{}) {
	if s == nil {
		return
	}

	for _, tunnel := range snapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			s.recordForConfig(tunnel.TunnelID, snapshot.Version, "")
			continue
		}
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			s.recordForConfig(tunnel.TunnelID, snapshot.Version, "")
			continue
		}
		if _, keep := preserved[tunnel.TunnelID]; keep {
			continue
		}
		s.recordForConfig(tunnel.TunnelID, snapshot.Version, issues[tunnel.TunnelID])
	}
}

func (s *Server) TunnelRuntimeIssues() map[int64]string {
	if s == nil || s.runtimeIssues == nil {
		return nil
	}
	return s.runtimeIssues.snapshotReasons()
}

func (s *Server) clearTunnelRuntimeIssues(tunnels []protocol.TunnelEntry) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.clearTunnels(tunnels)
}

func (s *Server) recordTunnelRuntimeIssue(tunnelID uint32, reason string) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.record(tunnelID, reason)
}

func (s *Server) recordTunnelRuntimeIssueForConfig(tunnelID uint32, configVersion uint64, reason string) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.recordForConfig(tunnelID, configVersion, reason)
}

func (s *Server) clearUnknownTunnelRuntimeIssues(knownTunnelIDs map[int64]struct{}) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.clearUnknown(knownTunnelIDs)
}

func (s *Server) applyScannedTunnelRuntimeIssues(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{}) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.applyScanResult(snapshot, staticConflictIDs, issues, preserved)
}
