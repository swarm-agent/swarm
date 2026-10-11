package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Secret slots are machine-owner territory: a scoped token (and therefore any
// agent or AI key) can never create a slot, set a value, or grant one. Agents
// only ever ask for a slot through the requests inbox (Phase 3); the owner,
// here, decides. The value goes in and never comes back out of this API.

type secretValueSetter interface {
	PutSecretValueForAccount(accountScopeID, name string, value []byte) error
	DeleteSecretValueForAccount(accountScopeID, name string) error
}

func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request) {
	if s.secretSlots == nil || s.secretValues == nil {
		writeError(w, http.StatusInternalServerError, errors.New("secret slots are not configured"))
		return
	}
	if _, scoped := ScopedTokenFromRequest(r); scoped {
		writeError(w, http.StatusForbidden, errors.New("managing secrets requires the machine owner"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	account := strings.TrimSpace(principal.AccountScopeID)

	path := strings.TrimPrefix(r.URL.Path, "/v1/secrets")
	switch {
	case path == "" || path == "/":
		s.handleSecretSlotCollection(w, r, account)
	case strings.HasSuffix(path, "/value"):
		s.handleSecretValue(w, r, account, strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/value"))
	case strings.HasSuffix(path, "/grants"):
		s.handleSecretGrants(w, r, account, strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/grants"))
	case strings.HasPrefix(path, "/uses"):
		s.handleSecretUses(w, r, account)
	default:
		name := strings.TrimPrefix(path, "/")
		if strings.Contains(name, "/") {
			writeError(w, http.StatusNotFound, errors.New("unknown secrets route"))
			return
		}
		s.handleSecretSlotItem(w, r, account, name)
	}
}

func (s *Server) handleSecretSlotCollection(w http.ResponseWriter, r *http.Request, account string) {
	switch r.Method {
	case http.MethodGet:
		slots, err := s.secretSlots.ListSlots(account)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "secrets": slots})
	case http.MethodPost:
		var req struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Hosts       []string `json:"hosts"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		slot, err := s.secretSlots.UpsertSlot(account, req.Name, req.Description, req.Hosts)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "secret": slot})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleSecretSlotItem(w http.ResponseWriter, r *http.Request, account, name string) {
	switch r.Method {
	case http.MethodGet:
		slot, ok, err := s.secretSlots.GetSlot(account, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, pebblestore.ErrSecretSlotNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "secret": slot})
	case http.MethodDelete:
		if err := s.secretSlots.DeleteSlot(account, name); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := s.secretValues.DeleteSecretValueForAccount(account, name); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

// handleSecretValue sets the sealed value. The body is the raw secret, so the
// value is never logged and never returned; only "has value" is observable.
func (s *Server) handleSecretValue(w http.ResponseWriter, r *http.Request, account, name string) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, ok, err := s.secretSlots.GetSlot(account, name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeError(w, http.StatusNotFound, errors.New("create the secret slot before setting its value"))
		return
	}
	value, err := io.ReadAll(io.LimitReader(r.Body, 16<<10+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value = trimTrailingNewline(value)
	if err := s.secretValues.PutSecretValueForAccount(account, name, value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	slot, err := s.secretSlots.MarkValue(account, name, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "secret": slot})
}

func (s *Server) handleSecretGrants(w http.ResponseWriter, r *http.Request, account, name string) {
	switch r.Method {
	case http.MethodGet:
		grants, err := s.secretSlots.ListGrants(account, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "grants": grants})
	case http.MethodPost:
		var req struct {
			WorkspacePath  string `json:"workspace_path"`
			ExpiresSeconds int64  `json:"expires_seconds"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		grant, err := s.secretSlots.Grant(account, name, req.WorkspacePath, time.Duration(req.ExpiresSeconds)*time.Second)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "grant": grant})
	case http.MethodDelete:
		grantID := strings.TrimSpace(r.URL.Query().Get("grant_id"))
		if grantID == "" {
			writeError(w, http.StatusBadRequest, errors.New("grant_id is required"))
			return
		}
		if err := s.secretSlots.Revoke(account, name, grantID); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleSecretUses(w http.ResponseWriter, r *http.Request, account string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	uses, err := s.secretSlots.ListUses(account, 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uses": uses})
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
