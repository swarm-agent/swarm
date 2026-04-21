package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	desktopLocalSessionCookieName = "swarm_desktop_session"
	desktopLocalSessionTTL        = 12 * time.Hour
)

type desktopLocalAuthContextKey string

const (
	desktopLocalAuthIssuedTokenKey desktopLocalAuthContextKey = "desktop-local-auth-issued-token"
	localTransportAuthEnabledKey   desktopLocalAuthContextKey = "local-transport-auth-enabled"
)

type desktopLocalSessionManager struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func newDesktopLocalSessionManager() *desktopLocalSessionManager {
	return &desktopLocalSessionManager{}
}

func (m *desktopLocalSessionManager) Ensure(now time.Time) (string, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if now.IsZero() {
		now = time.Now()
	}
	if strings.TrimSpace(m.token) == "" || !now.Before(m.expiresAt) {
		token, err := generateDesktopLocalSessionToken()
		if err != nil {
			return "", time.Time{}, err
		}
		m.token = token
	}
	m.expiresAt = now.Add(desktopLocalSessionTTL)
	return m.token, m.expiresAt, nil
}

func (m *desktopLocalSessionManager) Validate(token string, now time.Time) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if now.IsZero() {
		now = time.Now()
	}
	if strings.TrimSpace(m.token) == "" || !now.Before(m.expiresAt) {
		return false
	}
	if len(token) != len(m.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(m.token)) == 1
}

func (s *Server) withDesktopLocalSession(next http.Handler) http.Handler {
	if s == nil || s.desktopLocalSessions == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldBootstrapDesktopLocalSession(r) {
			var err error
			r, err = s.issueDesktopLocalSession(w, r)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func shouldUseDesktopLocalSessionAuth(r *http.Request) bool {
	return isLocalDesktopBrowserRequest(r) && isSameOriginBrowserRequest(r)
}

func isLocalDesktopBrowserRequest(r *http.Request) bool {
	ip := remoteRequestIP(r)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if ip.To4() != nil {
		for _, candidate := range detectLANAddresses() {
			candidateIP := net.ParseIP(strings.TrimSpace(candidate))
			if candidateIP == nil {
				continue
			}
			if candidateIP.Equal(ip) {
				return true
			}
		}
	}
	tailscale := detectTailscale()
	for _, candidate := range tailscale.IPs {
		candidateIP := net.ParseIP(strings.TrimSpace(candidate))
		if candidateIP == nil {
			continue
		}
		if candidateIP.Equal(ip) {
			return true
		}
	}
	return false
}

func markLocalTransportRequest(r *http.Request) *http.Request {
	if r == nil {
		return nil
	}
	ctx := context.WithValue(r.Context(), localTransportAuthEnabledKey, true)
	return r.WithContext(ctx)
}

func shouldBootstrapDesktopLocalSession(r *http.Request) bool {
	if !shouldUseDesktopLocalSessionAuth(r) {
		return false
	}
	return shouldServeDesktopAsset(r)
}

func (s *Server) issueDesktopLocalSession(w http.ResponseWriter, r *http.Request) (*http.Request, error) {
	if s == nil || s.desktopLocalSessions == nil {
		return r, nil
	}
	token, expiresAt, err := s.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		return r, err
	}
	http.SetCookie(w, buildDesktopLocalSessionCookie(token, expiresAt, requestScheme(r) == "https"))
	if r != nil {
		r = r.WithContext(context.WithValue(r.Context(), desktopLocalAuthIssuedTokenKey, token))
	}
	return r, nil
}

func desktopLocalSessionTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if issued, _ := r.Context().Value(desktopLocalAuthIssuedTokenKey).(string); strings.TrimSpace(issued) != "" {
		return strings.TrimSpace(issued)
	}
	cookie, err := r.Cookie(desktopLocalSessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func buildDesktopLocalSessionCookie(token string, expiresAt time.Time, secure bool) *http.Cookie {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	return &http.Cookie{
		Name:     desktopLocalSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
		Expires:  expiresAt,
		MaxAge:   maxAge,
	}
}

func (s *Server) handleDesktopLocalSessionBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !shouldUseDesktopLocalSessionAuth(r) {
		writeError(w, http.StatusForbidden, errors.New("desktop local session bootstrap requires a same-origin browser request from this machine"))
		return
	}
	var err error
	r, err = s.issueDesktopLocalSession(w, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
	})
}

func isLocalTransportRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	enabled, _ := r.Context().Value(localTransportAuthEnabledKey).(bool)
	return enabled
}

func generateDesktopLocalSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
