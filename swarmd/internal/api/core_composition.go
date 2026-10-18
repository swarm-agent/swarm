package api

import (
	"net/http"

	"swarm/packages/swarmd/internal/identity"
)

// browserComposition contains optional presentation adapters, not backend
// authority. Its zero value is deliberately absent from NewCoreServer.
type browserComposition struct {
	handler             func() http.Handler
	registerRoutes      func(*http.ServeMux)
	authExempt          func(*http.Request) bool
	sessionToken        func(*http.Request) string
	allowBootstrap      func(*http.Request) bool
	sensitiveOnboarding func(*http.Request) bool
	setSessionCookie    func(http.ResponseWriter, *http.Request, *identity.IssuedSession)
}

// DesktopHandler exposes only an explicitly installed Desktop composition.
// Keeping this adapter indirect prevents reflection on Server from linking
// Desktop assets and session bootstrap into the Headless executable.
func (s *Server) DesktopHandler() http.Handler {
	if s.browser == nil {
		return http.NotFoundHandler()
	}
	return s.browser.handler()
}

func (s *Server) productSessionToken(r *http.Request) string {
	if s.browser != nil {
		return s.browser.sessionToken(r)
	}
	return extractAttachToken(r)
}

func (s *Server) allowsIdentityBootstrap(r *http.Request) bool {
	return isLocalTransportRequest(r) || (s.browser != nil && s.browser.allowBootstrap(r))
}
