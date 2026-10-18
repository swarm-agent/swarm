package api

import (
	"net/http"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/discovery"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/provider/registry"
	"swarm/packages/swarmd/internal/security"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/workspace"
)

// NewServer preserves the existing Desktop-capable API composition. Headless
// startup uses NewCoreServer instead; it does not install these browser hooks.
func NewServer(authSvc *auth.Service, agentSvc *agentruntime.Service, modelSvc *model.Service, runSvc runService, sessionSvc *sessionruntime.Service, workspaceSvc *workspace.Service, discoverySvc *discovery.Service, securitySvc *security.Service, providers *registry.Registry, permSvc permissionService, notificationSvc notificationService, events *pebblestore.EventLog, hub *stream.Hub) *Server {
	server := NewCoreServer(authSvc, agentSvc, modelSvc, runSvc, sessionSvc, workspaceSvc, discoverySvc, securitySvc, providers, permSvc, notificationSvc, events, hub)
	ConfigureDesktop(server)
	return server
}

// ConfigureDesktop is a startup composition operation, never an API setting.
// It must run before any handler is constructed or any request is served.
func ConfigureDesktop(s *Server) {
	s.desktopLocalSessions = newDesktopLocalSessionManager()
	s.desktopLocalSessions.server = s
	s.browser = &browserComposition{
		handler: s.desktopHandler,
		registerRoutes: func(mux *http.ServeMux) {
			mux.HandleFunc("/ws", s.handleDesktopStream)
			mux.HandleFunc("/v1/auth/desktop/session", s.handleDesktopLocalSessionBootstrap)
			mux.HandleFunc(TailscaleOnboardingApprovalPath, s.handleTailscaleOnboardingApproval)
			mux.HandleFunc("/v1/feedback", s.handleDesktopFeedback)
			mux.HandleFunc("/v1/update/status", s.handleUpdateStatus)
			mux.HandleFunc("/v1/update/apply", s.handleUpdateApply)
			mux.HandleFunc("/v1/update/run", s.handleUpdateRun)
		},
		authExempt: func(r *http.Request) bool {
			switch r.URL.Path {
			case "/v1/auth/desktop/session":
				return r.Method == http.MethodGet && shouldAllowDesktopLocalSessionBootstrapRequest(r)
			case TailscaleOnboardingApprovalPath:
				_, pending := pendingDesktopOrigin(r)
				_, admitted := admittedDesktopOrigin(r)
				return (pending || admitted) && (r.Method == http.MethodGet || (pending && r.Method == http.MethodPost))
			}
			return false
		},
		sessionToken:   productSessionTokenFromRequest,
		allowBootstrap: shouldAllowDesktopLocalSessionBootstrapRequest,
		sensitiveOnboarding: func(r *http.Request) bool {
			if !shouldUseDesktopLocalSessionAuth(r) {
				return false
			}
			_, ok := s.actorFromDesktopLocalSession(r)
			return ok
		},
		setSessionCookie: func(w http.ResponseWriter, r *http.Request, issued *identity.IssuedSession) {
			http.SetCookie(w, buildDesktopLocalSessionCookie(r, issued.Token, issued.ExpiresAt))
		},
	}
}
