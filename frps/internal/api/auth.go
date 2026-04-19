package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

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

func (s *Server) handleAuthState(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		writeError(writer, &apiError{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
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
		writeJSON(writer, http.StatusOK, response)
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
		writeError(writer, translateAuthError(err))
		return
	}

	writeJSON(writer, http.StatusOK, response)
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

func (s *Server) handleAuthLogin(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		writeError(writer, &apiError{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	var payload authLoginRequest
	if err := decodeJSONBody(request, &payload); err != nil {
		writeError(writer, err)
		return
	}

	session, token, err := s.auth.Login(payload.ChallengeID, payload.Proof)
	if err != nil {
		writeError(writer, translateAuthError(err))
		return
	}

	s.writeManagementSessionCookie(writer, request, token, session.ExpiresAt)
	writeJSON(writer, http.StatusOK, map[string]any{
		"initialized":   true,
		"authenticated": true,
		"expires_at":    session.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleAuthSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeMethodNotAllowed(writer)
		return
	}
	if !s.requireManagementSession(writer, request) {
		return
	}

	session, _, err := s.lookupManagementSession(request)
	if err != nil {
		writeError(writer, translateAuthError(err))
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"initialized":   true,
		"authenticated": true,
		"expires_at":    session.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleAuthLogout(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeMethodNotAllowed(writer)
		return
	}
	if s.auth == nil {
		writeError(writer, &apiError{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return
	}

	if token := readManagementSessionToken(request); token != "" {
		s.auth.Logout(token)
	}
	s.clearManagementSessionCookie(writer)

	writeJSON(writer, http.StatusOK, map[string]any{
		"initialized":   s.auth.Initialized(),
		"authenticated": false,
		"logged_out":    true,
	})
}

func (s *Server) requireManagementSession(writer http.ResponseWriter, request *http.Request) bool {
	if s.auth == nil {
		writeError(writer, &apiError{Status: http.StatusServiceUnavailable, Message: "management auth is unavailable"})
		return false
	}
	if !s.auth.Initialized() {
		writeError(writer, &apiError{Status: http.StatusConflict, Message: "management secret is not initialized"})
		return false
	}

	if _, _, err := s.lookupManagementSession(request); err != nil {
		if (errors.Is(err, authn.ErrSessionRequired) || errors.Is(err, authn.ErrSessionExpired)) && hasManagementSessionCookie(request) {
			s.clearManagementSessionCookie(writer)
		}
		writeError(writer, translateAuthError(err))
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
	case errors.Is(err, authn.ErrSessionRequired):
		return &apiError{Status: http.StatusUnauthorized, Message: err.Error()}
	case errors.Is(err, authn.ErrSessionExpired):
		return &apiError{Status: http.StatusUnauthorized, Message: err.Error()}
	default:
		return err
	}
}
