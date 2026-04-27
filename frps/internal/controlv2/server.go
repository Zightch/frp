package controlv2

import (
	"context"
	"fmt"

	"github.com/zightch/frp/frps/internal/controlv2/session"
)

type Options struct {
	Repo       RuntimeRepo
	Supervisor *Supervisor
}

type Server struct {
	repo       RuntimeRepo
	supervisor *Supervisor
}

func NewServer(options Options) (*Server, error) {
	supervisor := options.Supervisor
	if supervisor == nil {
		supervisor = NewSupervisor(session.NoopExecutor{})
	}

	if options.Repo == nil {
		return nil, fmt.Errorf("controlv2 runtime repo is required")
	}

	return &Server{
		repo:       options.Repo,
		supervisor: supervisor,
	}, nil
}

func (s *Server) Supervisor() *Supervisor {
	if s == nil {
		return nil
	}
	return s.supervisor
}

func (s *Server) HandleAuthenticatedSession(parent context.Context, initial session.SessionState, connID string) *session.Agent {
	if s == nil || s.supervisor == nil {
		return nil
	}
	return s.supervisor.AttachSession(parent, initial, connID)
}

func (s *Server) Dispatch(sessionID uint64, event session.Event) bool {
	if s == nil || s.supervisor == nil {
		return false
	}
	return s.supervisor.DispatchBySessionID(sessionID, event)
}

func (s *Server) RefreshDesiredRuntimeByGroup(ctx context.Context, groupID int64) error {
	if s == nil {
		return fmt.Errorf("controlv2 server is unavailable")
	}
	if s.repo == nil {
		return fmt.Errorf("controlv2 runtime repo is unavailable")
	}

	snapshot, err := s.repo.LoadDesiredRuntimeByGroupID(ctx, groupID)
	if err != nil {
		return err
	}
	if s.supervisor != nil {
		s.supervisor.UpdateDesiredRuntime(groupID, snapshot)
	}
	return nil
}

func (s *Server) NotifyNetworkChange() {
	if s == nil || s.supervisor == nil {
		return
	}
	s.supervisor.NotifyNetworkChange()
}

func (s *Server) Shutdown() {
	if s == nil || s.supervisor == nil {
		return
	}
	s.supervisor.Shutdown()
}
