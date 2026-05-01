package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/api/httpx"
	authn "github.com/zightch/frp/frps/internal/auth"
)

const managementSessionCookieName = "frps_management_session"

type authInitRequest struct {
	KeyHash string `json:"key_hash"`
}

type authLoginRequest struct {
	ChallengeID string `json:"challenge_id"`
	Proof       string `json:"proof"`
}

type authTakeoverRequest struct {
	PendingLoginToken  string `json:"pending_login_ticket"`
	ObservedGeneration uint64 `json:"observed_generation"`
}

func (s *Server) handleAuthState(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		httpx.WriteMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	response := map[string]any{
		"initialized":   s.auth.Initialized(),
		"authenticated": false,
	}
	if !s.auth.Initialized() {
		if hasManagementSessionCookie(request) {
			s.clearManagementSessionCookie(writer)
		}
		httpx.WriteJSON(writer, http.StatusOK, response)
		return
	}

	session, _, err := s.lookupManagementSession(request)
	switch {
	case err == nil:
		response["authenticated"] = true
		response["expires_at"] = session.ExpiresAt.Format(time.RFC3339)
	case errors.Is(err, authn.ErrSessionRequired), errors.Is(err, authn.ErrSessionExpired):
		if hasManagementSessionCookie(request) {
			s.clearManagementSessionCookie(writer)
		}
	default:
		httpx.WriteError(writer, translateAuthError(err))
		return
	}

	httpx.WriteJSON(writer, http.StatusOK, response)
}

func (s *Server) handleAuthInit(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	var payload authInitRequest
	if err := httpx.DecodeJSONBody(request, &payload); err != nil {
		httpx.WriteError(writer, err)
		return
	}

	if err := s.auth.Initialize(payload.KeyHash); err != nil {
		httpx.WriteError(writer, translateAuthError(err))
		return
	}

	httpx.WriteJSON(writer, http.StatusCreated, map[string]any{
		"initialized": true,
	})
}

func (s *Server) handleAuthChallenge(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	challenge, err := s.auth.IssueChallenge()
	if err != nil {
		httpx.WriteError(writer, translateAuthError(err))
		return
	}

	httpx.WriteJSON(writer, http.StatusOK, map[string]any{
		"challenge_id": challenge.ID,
		"salt":         challenge.Salt,
		"expires_at":   challenge.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleAuthLogin(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	var payload authLoginRequest
	if err := httpx.DecodeJSONBody(request, &payload); err != nil {
		httpx.WriteError(writer, err)
		return
	}

	result, err := s.auth.StartLogin(payload.ChallengeID, payload.Proof)
	if err != nil {
		httpx.WriteError(writer, translateAuthError(err))
		return
	}
	if result.Occupied {
		httpx.WriteError(writer, &httpx.Error{
			Status:  http.StatusConflict,
			Message: "current administrator is online",
			Code:    "management_admin_occupied",
			Details: map[string]any{
				"pending_login_ticket": result.PendingLoginToken,
				"observed_generation":  result.ObservedGeneration,
			},
		})
		return
	}

	s.writeManagementSessionCookie(writer, request, result.Token, result.Session.ExpiresAt)
	httpx.WriteJSON(writer, http.StatusOK, map[string]any{
		"initialized":   true,
		"authenticated": true,
		"expires_at":    result.Session.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleAuthTakeover(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	var payload authTakeoverRequest
	if err := httpx.DecodeJSONBody(request, &payload); err != nil {
		httpx.WriteError(writer, err)
		return
	}
	if strings.TrimSpace(payload.PendingLoginToken) == "" || payload.ObservedGeneration == 0 {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusBadRequest, Message: "pending_login_ticket and observed_generation are required"})
		return
	}

	session, token, err := s.auth.Takeover(payload.PendingLoginToken, payload.ObservedGeneration)
	if err != nil {
		httpx.WriteError(writer, translateAuthError(err))
		return
	}

	s.writeManagementSessionCookie(writer, request, token, session.ExpiresAt)
	httpx.WriteJSON(writer, http.StatusOK, map[string]any{
		"initialized":   true,
		"authenticated": true,
		"expires_at":    session.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleAuthSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		httpx.WriteMethodNotAllowed(writer)
		return
	}
	if !s.requireManagementSession(writer, request) {
		return
	}

	session, _, err := s.lookupManagementSession(request)
	if err != nil {
		httpx.WriteError(writer, translateAuthError(err))
		return
	}

	httpx.WriteJSON(writer, http.StatusOK, map[string]any{
		"initialized":   true,
		"authenticated": true,
		"expires_at":    session.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleAuthLogout(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	if token := readManagementSessionToken(request); token != "" {
		s.auth.Logout(token)
	}
	s.clearManagementSessionCookie(writer)

	httpx.WriteJSON(writer, http.StatusOK, map[string]any{
		"initialized":   s.auth.Initialized(),
		"authenticated": false,
		"logged_out":    true,
	})
}

func (s *Server) requireManagementSession(writer http.ResponseWriter, request *http.Request) bool {
	if s.auth == nil {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return false
	}
	if !s.auth.Initialized() {
		if hasManagementSessionCookie(request) {
			s.clearManagementSessionCookie(writer)
		}
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusConflict, Message: "management secret is not initialized"})
		return false
	}

	if _, _, err := s.lookupManagementSession(request); err != nil {
		if (errors.Is(err, authn.ErrSessionRequired) || errors.Is(err, authn.ErrSessionExpired)) && hasManagementSessionCookie(request) {
			s.clearManagementSessionCookie(writer)
		}
		httpx.WriteError(writer, translateAuthError(err))
		return false
	}

	return true
}

func (s *Server) lookupManagementSession(request *http.Request) (authn.Session, string, error) {
	token := readManagementSessionToken(request)
	session, err := s.auth.ValidateSession(token)
	return session, token, err
}

func (s *Server) writeManagementSessionCookie(writer http.ResponseWriter, request *http.Request, token string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	http.SetCookie(writer, &http.Cookie{
		Name:     managementSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   request.TLS != nil,
		Expires:  expiresAt,
		MaxAge:   maxAge,
	})
}

func (s *Server) clearManagementSessionCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{
		Name:     managementSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
	})
}

func readManagementSessionToken(request *http.Request) string {
	if authorization := strings.TrimSpace(request.Header.Get("Authorization")); authorization != "" {
		lowered := strings.ToLower(authorization)
		if strings.HasPrefix(lowered, "bearer ") {
			return strings.TrimSpace(authorization[len("Bearer "):])
		}
	}

	cookie, err := request.Cookie(managementSessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func hasManagementSessionCookie(request *http.Request) bool {
	_, err := request.Cookie(managementSessionCookieName)
	return err == nil
}

func translateAuthError(err error) error {
	switch {
	case errors.Is(err, authn.ErrAlreadyInitialized):
		return &httpx.Error{Status: http.StatusConflict, Message: err.Error()}
	case errors.Is(err, authn.ErrNotInitialized):
		return &httpx.Error{Status: http.StatusConflict, Message: err.Error()}
	case errors.Is(err, authn.ErrInvalidKeyHash):
		return &httpx.Error{Status: http.StatusBadRequest, Message: err.Error()}
	case errors.Is(err, authn.ErrInvalidProof):
		return &httpx.Error{Status: http.StatusUnauthorized, Message: err.Error()}
	case errors.Is(err, authn.ErrChallengeExpired):
		return &httpx.Error{Status: http.StatusGone, Message: err.Error()}
	case errors.Is(err, authn.ErrChallengeReplayed):
		return &httpx.Error{Status: http.StatusConflict, Message: err.Error()}
	case errors.Is(err, authn.ErrSessionRequired):
		return &httpx.Error{Status: http.StatusUnauthorized, Message: err.Error()}
	case errors.Is(err, authn.ErrSessionExpired):
		return &httpx.Error{Status: http.StatusUnauthorized, Message: err.Error()}
	case errors.Is(err, authn.ErrTakeoverStale):
		return &httpx.Error{
			Status:  http.StatusConflict,
			Message: "页面已失效，请重新登录",
			Code:    "management_takeover_stale",
		}
	default:
		return err
	}
}
