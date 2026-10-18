package api

import (
	"errors"
	"net/http"
)

func (s *Server) desktopHandler() http.Handler {
	apiHandler := s.withDesktopLocalSession(s.withAuth(s.withVaultGate(s.withJSON(s.apiMux()))))
	desktopSurface := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldServeDesktopAsset(r) {
			s.withDesktopLocalSession(s.withDesktopAssets(http.NotFoundHandler())).ServeHTTP(w, r)
			return
		}
		apiHandler.ServeHTTP(w, r)
	})
	return s.withDesktopBoundary(desktopSurface)
}

func (s *Server) handleDesktopStream(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		writeError(w, http.StatusInternalServerError, errors.New("stream hub not configured"))
		return
	}
	s.hub.ServeHTTP(w, r)
}
