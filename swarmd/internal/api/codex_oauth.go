package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	codexruntime "swarm/packages/swarmd/internal/provider/codex"
)

const codexOAuthSessionTTL = 20 * time.Minute

type codexOAuthSessionResponse struct {
	SessionID       string                 `json:"session_id"`
	Provider        string                 `json:"provider"`
	Method          string                 `json:"method"`
	Label           string                 `json:"label,omitempty"`
	Active          bool                   `json:"active"`
	AuthURL         string                 `json:"auth_url,omitempty"`
	VerificationURL string                 `json:"verification_url,omitempty"`
	UserCode        string                 `json:"user_code,omitempty"`
	ExpiresAt       int64                  `json:"expires_at,omitempty"`
	Status          string                 `json:"status"`
	Error           string                 `json:"error,omitempty"`
	Credential      *auth.CredentialStatus `json:"credential,omitempty"`
}

func (s *Server) handleCodexOAuthStart(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.auth == nil {
		writeError(w, http.StatusInternalServerError, errors.New("auth service not configured"))
		return
	}

	var req struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
		Active   bool   `json:"active"`
		Method   string `json:"method"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		provider = "codex"
	}
	if provider != "codex" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported provider %q", provider))
		return
	}

	method := strings.ToLower(strings.TrimSpace(req.Method))
	if method == "" {
		method = "browser"
	}
	if method != "browser" && method != "manual" && method != "device" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported method %q", method))
		return
	}

	sessionID, err := newCodexOAuthSessionID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	now := time.Now()
	session := &codexOAuthSession{
		UserID:         strings.TrimSpace(principal.UserID),
		AccountScopeID: strings.TrimSpace(principal.AccountScopeID),
		Provider:       provider,
		Label:          strings.TrimSpace(req.Label),
		Active:         req.Active,
		Method:         method,
		Status:         "waiting",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if method == "device" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		authorization, deviceErr := codexruntime.RequestDeviceAuthorization(ctx)
		cancel()
		if deviceErr != nil {
			writeError(w, http.StatusBadGateway, deviceErr)
			return
		}
		session.VerificationURL = authorization.VerificationURL
		session.UserCode = authorization.UserCode
		session.ExpiresAt = authorization.ExpiresAt
		session.DeviceAuthorization = &authorization
	} else {
		login, loginErr := codexruntime.StartOAuthLogin()
		if loginErr != nil {
			writeError(w, http.StatusInternalServerError, loginErr)
			return
		}
		session.CodeVerifier = login.CodeVerifier
		session.State = login.State
		session.AuthURL = login.AuthURL
	}

	s.codexOAuthSet(sessionID, session)
	response := s.codexOAuthResponse(sessionID, *session)

	switch method {
	case "browser":
		go s.awaitCodexOAuthBrowser(sessionID)
	case "device":
		go s.awaitCodexOAuthDevice(sessionID)
	}

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleCodexOAuthStatus(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
		return
	}
	session, ok := s.codexOAuthGet(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("oauth session not found"))
		return
	}
	if !codexOAuthSessionMatchesPrincipal(session, principal) {
		writeError(w, http.StatusNotFound, errors.New("oauth session not found"))
		return
	}
	writeJSON(w, http.StatusOK, s.codexOAuthResponse(sessionID, session))
}

func (s *Server) handleCodexOAuthComplete(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.auth == nil {
		writeError(w, http.StatusInternalServerError, errors.New("auth service not configured"))
		return
	}

	var req struct {
		SessionID     string `json:"session_id"`
		CallbackInput string `json:"callback_input"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
		return
	}
	session, ok := s.codexOAuthGet(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("oauth session not found"))
		return
	}
	if !codexOAuthSessionMatchesPrincipal(session, principal) {
		writeError(w, http.StatusNotFound, errors.New("oauth session not found"))
		return
	}
	if session.Credential != nil {
		writeJSON(w, http.StatusOK, s.codexOAuthResponse(sessionID, session))
		return
	}
	if session.Method == "device" {
		writeError(w, http.StatusBadRequest, errors.New("device oauth sessions complete asynchronously; poll the status endpoint"))
		return
	}

	callbackInput := strings.TrimSpace(req.CallbackInput)
	if callbackInput == "" {
		writeError(w, http.StatusBadRequest, errors.New("callback_input is required"))
		return
	}

	code, callbackState := codexruntime.ParseOAuthCallbackInput(callbackInput)
	if code == "" {
		writeError(w, http.StatusBadRequest, errors.New("authorization code is required"))
		return
	}
	if callbackState != "" && session.State != "" && callbackState != session.State {
		writeError(w, http.StatusBadRequest, errors.New("oauth state mismatch"))
		return
	}

	s.codexOAuthUpdate(sessionID, func(next *codexOAuthSession) {
		next.Status = "authorizing"
		next.Error = ""
	})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	tokens, err := codexruntime.ExchangeOAuthCode(ctx, code, session.CodeVerifier)
	cancel()
	if err != nil {
		s.codexOAuthUpdate(sessionID, func(next *codexOAuthSession) {
			next.Status = "error"
			next.Error = err.Error()
		})
		writeError(w, http.StatusBadRequest, err)
		return
	}

	status, saveErr := s.finishCodexOAuthSession(sessionID, tokens)
	if saveErr != nil {
		writeError(w, http.StatusInternalServerError, saveErr)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) awaitCodexOAuthDevice(sessionID string) {
	session, ok := s.codexOAuthGet(sessionID)
	if !ok || session.DeviceAuthorization == nil {
		return
	}
	s.codexOAuthUpdate(sessionID, func(next *codexOAuthSession) {
		next.Status = "authorizing"
	})
	ctx, cancel := context.WithDeadline(context.Background(), session.ExpiresAt)
	defer cancel()
	tokens, err := codexruntime.CompleteDeviceAuthorization(ctx, *session.DeviceAuthorization)
	if err != nil {
		s.codexOAuthUpdate(sessionID, func(next *codexOAuthSession) {
			next.Status = "error"
			next.Error = err.Error()
			next.DeviceAuthorization = nil
		})
		return
	}
	_, _ = s.finishCodexOAuthSession(sessionID, tokens)
}

func (s *Server) awaitCodexOAuthBrowser(sessionID string) {
	session, ok := s.codexOAuthGet(sessionID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	tokens, err := codexruntime.WaitForOAuthCallback(ctx, session.CodeVerifier, session.State)
	if err != nil {
		s.codexOAuthUpdate(sessionID, func(next *codexOAuthSession) {
			next.Status = "error"
			next.Error = err.Error()
		})
		return
	}
	_, _ = s.finishCodexOAuthSession(sessionID, tokens)
}

func (s *Server) finishCodexOAuthSession(sessionID string, tokens codexruntime.OAuthTokens) (codexOAuthSessionResponse, error) {
	session, ok := s.codexOAuthGet(sessionID)
	if !ok {
		return codexOAuthSessionResponse{}, errors.New("oauth session not found")
	}

	status, event, err := s.auth.UpsertCredential(auth.CredentialUpsertInput{
		AccountScopeID: session.AccountScopeID,
		Provider:       session.Provider,
		Type:           "oauth",
		Label:          session.Label,
		AccessToken:    tokens.AccessToken,
		RefreshToken:   tokens.RefreshToken,
		ExpiresAt:      tokens.ExpiresAt,
		AccountID:      codexruntime.ExtractAccountID(tokens.AccessToken),
		Active:         session.Active,
	})
	if err != nil {
		s.codexOAuthUpdate(sessionID, func(next *codexOAuthSession) {
			next.Status = "error"
			next.Error = err.Error()
		})
		return codexOAuthSessionResponse{}, err
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}

	if connection, verifyErr := s.verifyAuthCredentialConnectionForAccount(context.Background(), session.AccountScopeID, session.Provider, status.ID); verifyErr == nil {
		status.Connection = connection
	}
	if autoDefaults, defaultsErr := s.hydrateOnboardingProviderDefaultsAfterVerifiedCredentialActivationForAccount(session.AccountScopeID, session.UserID, session.Provider); defaultsErr != nil {
		status.AutoDefaults = &auth.AutoDefaultsStatus{Error: defaultsErr.Error()}
	} else if autoDefaults != nil {
		status.AutoDefaults = autoDefaults
	}

	captured := status
	s.codexOAuthUpdate(sessionID, func(next *codexOAuthSession) {
		next.Status = "success"
		next.Error = ""
		next.Credential = &captured
		next.DeviceAuthorization = nil
	})

	session, _ = s.codexOAuthGet(sessionID)
	return s.codexOAuthResponse(sessionID, session), nil
}

func codexOAuthSessionMatchesPrincipal(session codexOAuthSession, principal identity.Principal) bool {
	return strings.TrimSpace(session.UserID) != "" &&
		strings.TrimSpace(session.AccountScopeID) != "" &&
		strings.TrimSpace(session.UserID) == strings.TrimSpace(principal.UserID) &&
		strings.TrimSpace(session.AccountScopeID) == strings.TrimSpace(principal.AccountScopeID)
}

func (s *Server) codexOAuthResponse(sessionID string, session codexOAuthSession) codexOAuthSessionResponse {
	response := codexOAuthSessionResponse{
		SessionID:       sessionID,
		Provider:        session.Provider,
		Method:          session.Method,
		Label:           session.Label,
		Active:          session.Active,
		AuthURL:         session.AuthURL,
		VerificationURL: session.VerificationURL,
		UserCode:        session.UserCode,
		Status:          session.Status,
		Error:           session.Error,
		Credential:      session.Credential,
	}
	if !session.ExpiresAt.IsZero() {
		response.ExpiresAt = session.ExpiresAt.UnixMilli()
	}
	return response
}

func (s *Server) codexOAuthGet(sessionID string) (codexOAuthSession, bool) {
	if s == nil {
		return codexOAuthSession{}, false
	}
	s.codexOAuthMu.Lock()
	defer s.codexOAuthMu.Unlock()
	s.codexOAuthPruneLocked()
	session, ok := s.codexOAuthSessions[sessionID]
	if !ok || session == nil {
		return codexOAuthSession{}, false
	}
	return *session, true
}

func (s *Server) codexOAuthSet(sessionID string, session *codexOAuthSession) {
	if s == nil || session == nil {
		return
	}
	s.codexOAuthMu.Lock()
	defer s.codexOAuthMu.Unlock()
	s.codexOAuthPruneLocked()
	s.codexOAuthSessions[sessionID] = session
}

func (s *Server) codexOAuthUpdate(sessionID string, update func(*codexOAuthSession)) {
	if s == nil || update == nil {
		return
	}
	s.codexOAuthMu.Lock()
	defer s.codexOAuthMu.Unlock()
	s.codexOAuthPruneLocked()
	session, ok := s.codexOAuthSessions[sessionID]
	if !ok || session == nil {
		return
	}
	update(session)
	session.UpdatedAt = time.Now()
}

func (s *Server) codexOAuthPruneLocked() {
	if s == nil || s.codexOAuthSessions == nil {
		return
	}
	cutoff := time.Now().Add(-codexOAuthSessionTTL)
	for sessionID, session := range s.codexOAuthSessions {
		if session == nil {
			delete(s.codexOAuthSessions, sessionID)
			continue
		}
		updatedAt := session.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = session.CreatedAt
		}
		if updatedAt.Before(cutoff) {
			delete(s.codexOAuthSessions, sessionID)
		}
	}
}

func newCodexOAuthSessionID() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate oauth session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
