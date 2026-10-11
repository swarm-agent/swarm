package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// SetSignalSinkStore wires the signal sinks (/v3/signals/sinks).
func (s *Server) SetSignalSinkStore(sinks *pebblestore.SignalSinkStore) {
	if s != nil {
		s.signalSinks = sinks
	}
}

// handleSignalSinks manages where this machine forwards its signal feed.
//
//	GET    /v3/signals/sinks          list (secrets omitted)
//	POST   /v3/signals/sinks          {name, url, kinds, min_severity, heartbeat_seconds}
//	                                  → the sink and its signing secret, shown once
//	DELETE /v3/signals/sinks/{id}
//
// Owner only: a sink sends this machine's signals somewhere, so no scoped
// token (an AI key, an app key, even admin) may add, list or remove one.
func (s *Server) handleSignalSinks(w http.ResponseWriter, r *http.Request) {
	if s.signalSinks == nil || s.signals == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("signal forwarding is not configured"))
		return
	}
	if _, scoped := ScopedTokenFromRequest(r); scoped {
		writeError(w, http.StatusForbidden, errors.New("managing signal sinks requires the machine owner"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	account := strings.TrimSpace(principal.AccountScopeID)
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v3/signals/sinks"), "/")
	if strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, errors.New("unknown signal sink route"))
		return
	}
	switch {
	case id == "" && r.Method == http.MethodGet:
		sinks, err := s.signalSinks.List(account)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		public := make([]pebblestore.SignalSink, 0, len(sinks))
		for _, sink := range sinks {
			public = append(public, sink.Public())
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sinks": public})
	case id == "" && r.Method == http.MethodPost:
		var req struct {
			Name             string   `json:"name"`
			URL              string   `json:"url"`
			Kinds            []string `json:"kinds"`
			MinSeverity      string   `json:"min_severity"`
			HeartbeatSeconds int      `json:"heartbeat_seconds"`
		}
		if err := decodeJSONLimited(w, r, &req, maxSignalBody); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		// Start at the feed's current end: a new sink gets what happens next.
		page, err := s.signals.ListAfter(0, 1, pebblestore.SignalFilter{Account: account})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		sink, err := s.signalSinks.Create(account, req.Name, req.URL, req.Kinds, req.MinSeverity, req.HeartbeatSeconds, page.LatestSeq)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sink": sink.Public(), "secret": sink.Secret})
	case id != "" && r.Method == http.MethodDelete:
		if err := s.signalSinks.Delete(account, id); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, pebblestore.ErrSignalSinkNotFound) {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}
