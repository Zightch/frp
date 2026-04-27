package runtime

import (
	"strings"
	"sync"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type tunnelIssue struct {
	Reason        string
	ConfigVersion uint64
}

type IssueStore struct {
	mu      sync.RWMutex
	tunnels map[int64]tunnelIssue
}

func NewIssueStore() *IssueStore {
	return &IssueStore{
		tunnels: make(map[int64]tunnelIssue),
	}
}

func (s *IssueStore) SnapshotReasons() map[int64]string {
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

func (s *IssueStore) ClearTunnels(tunnels []protocol.TunnelEntry) {
	if s == nil || len(tunnels) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, tunnel := range tunnels {
		delete(s.tunnels, int64(tunnel.TunnelID))
	}
}

func (s *IssueStore) Record(tunnelID uint32, reason string) {
	s.RecordForConfig(tunnelID, 0, reason)
}

func (s *IssueStore) RecordForConfig(tunnelID uint32, configVersion uint64, reason string) {
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

	s.tunnels[int64(tunnelID)] = tunnelIssue{
		Reason:        trimmedReason,
		ConfigVersion: configVersion,
	}
}

func (s *IssueStore) ClearUnknown(knownTunnelIDs map[int64]struct{}) {
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

func (s *IssueStore) ApplyScanResult(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{}) {
	if s == nil {
		return
	}

	for _, tunnel := range snapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			s.RecordForConfig(tunnel.TunnelID, snapshot.Version, "")
			continue
		}
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			s.RecordForConfig(tunnel.TunnelID, snapshot.Version, "")
			continue
		}
		if _, keep := preserved[tunnel.TunnelID]; keep {
			continue
		}
		s.RecordForConfig(tunnel.TunnelID, snapshot.Version, issues[tunnel.TunnelID])
	}
}
