package bind

import (
	"context"
	"errors"
	"sync"

	"github.com/zightch/frp/frps/internal/control/session"
)

var ErrBindingConflict = errors.New("binding conflict")

type PrepareBindingsRequest struct {
	GroupID     int64
	SessionID   uint64
	Epoch       uint64
	EffectiveIP string
	Tunnels     []session.DesiredTunnelRuntime
}

type PrepareBindingsResult struct {
	Keys      []session.BindingKey
	Conflicts []Conflict
}

type StartBindingsRequest struct {
	GroupID   int64
	SessionID uint64
	Epoch     uint64
	TunnelIDs map[session.BindingKey]uint32
	Keys      []session.BindingKey
}

type StopBindingsRequest struct {
	GroupID   int64
	SessionID uint64
	Keys      []session.BindingKey
}

type Manager interface {
	Prepare(ctx context.Context, req PrepareBindingsRequest) (PrepareBindingsResult, error)
	Start(ctx context.Context, req StartBindingsRequest) error
	Stop(ctx context.Context, req StopBindingsRequest) error
}

type MemoryManager struct {
	mu     sync.Mutex
	claims map[session.BindingKey]ClaimOwner
}

func NewMemoryManager() *MemoryManager {
	return &MemoryManager{
		claims: make(map[session.BindingKey]ClaimOwner),
	}
}

func (m *MemoryManager) Prepare(_ context.Context, req PrepareBindingsRequest) (PrepareBindingsResult, error) {
	if m == nil {
		return PrepareBindingsResult{}, nil
	}

	keys := ExpandBindings(req.EffectiveIP, req.Tunnels)

	m.mu.Lock()
	defer m.mu.Unlock()

	result := PrepareBindingsResult{
		Keys: keys,
	}
	for _, key := range keys {
		owner, claimed := m.claims[key]
		if !claimed {
			continue
		}
		if owner.GroupID == req.GroupID && owner.SessionID == req.SessionID {
			continue
		}
		result.Conflicts = append(result.Conflicts, Conflict{
			Key:   key,
			Owner: owner,
		})
	}
	if len(result.Conflicts) != 0 {
		return result, ErrBindingConflict
	}
	return result, nil
}

func (m *MemoryManager) Start(_ context.Context, req StartBindingsRequest) error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, key := range req.Keys {
		if owner, claimed := m.claims[key]; claimed && (owner.GroupID != req.GroupID || owner.SessionID != req.SessionID) {
			return ErrBindingConflict
		}
	}

	for _, key := range req.Keys {
		m.claims[key] = ClaimOwner{
			GroupID:   req.GroupID,
			SessionID: req.SessionID,
			Epoch:     req.Epoch,
			TunnelID:  req.TunnelIDs[key],
		}
	}
	return nil
}

func (m *MemoryManager) Stop(_ context.Context, req StopBindingsRequest) error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, key := range req.Keys {
		owner, claimed := m.claims[key]
		if !claimed {
			continue
		}
		if owner.GroupID == req.GroupID && owner.SessionID == req.SessionID {
			delete(m.claims, key)
		}
	}
	return nil
}
