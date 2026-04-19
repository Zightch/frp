package api

import (
	"errors"
	"net/http"
	"time"

	authn "github.com/zightch/frp/frps/internal/auth"
)

type authInitRequest struct {
	KeyHash string `json:"key_hash"`
}

func (s *Server) handleAuthState(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		writeError(writer, &apiError{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"initialized": s.auth.Initialized(),
	})
}

func (s *Server) handleAuthInit(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		writeError(writer, &apiError{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	var payload authInitRequest
	if err := decodeJSONBody(request, &payload); err != nil {
		writeError(writer, err)
		return
	}

	if err := s.auth.Initialize(payload.KeyHash); err != nil {
		writeError(writer, translateAuthError(err))
		return
	}

	writeJSON(writer, http.StatusCreated, map[string]any{
		"initialized": true,
	})
}

func (s *Server) handleAuthChallenge(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		writeError(writer, &apiError{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	challenge, err := s.auth.IssueChallenge()
	if err != nil {
		writeError(writer, translateAuthError(err))
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"challenge_id": challenge.ID,
		"salt":         challenge.Salt,
		"expires_at":   challenge.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) requireManagementSession(writer http.ResponseWriter) bool {
	if s.auth == nil {
		writeError(writer, &apiError{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return false
	}
	if !s.auth.Initialized() {
		writeError(writer, &apiError{Status: http.StatusConflict, Message: "management secret is not initialized"})
		return false
	}

	writeError(writer, &apiError{Status: http.StatusUnauthorized, Message: "management session is required"})
	return false
}

func translateAuthError(err error) error {
	switch {
	case errors.Is(err, authn.ErrAlreadyInitialized):
		return &apiError{Status: http.StatusConflict, Message: err.Error()}
	case errors.Is(err, authn.ErrNotInitialized):
		return &apiError{Status: http.StatusConflict, Message: err.Error()}
	case errors.Is(err, authn.ErrInvalidKeyHash):
		return &apiError{Status: http.StatusBadRequest, Message: err.Error()}
	case errors.Is(err, authn.ErrInvalidProof):
		return &apiError{Status: http.StatusUnauthorized, Message: err.Error()}
	case errors.Is(err, authn.ErrChallengeExpired):
		return &apiError{Status: http.StatusGone, Message: err.Error()}
	case errors.Is(err, authn.ErrChallengeReplayed):
		return &apiError{Status: http.StatusConflict, Message: err.Error()}
	default:
		return err
	}
}
