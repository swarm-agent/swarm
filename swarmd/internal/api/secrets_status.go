package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
)

// secretsReadScope lets an AI key see which secrets exist and where agents
// may use them. It never reaches a value, a grant change or a slot change:
// those stay with the machine owner on /v1/secrets.
const secretsReadScope = "secrets:read"

// handleSecretStatus answers "which secrets can my workers use, and in which
// folders?" (GET /v3/secrets/status, scope secrets:read): each secret's name,
// description, allowed websites, whether a value is set, and its unexpired
// grants (folder and expiry). Values are sealed and never leave the daemon.
func (s *Server) handleSecretStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.secretSlots == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("secret slots are not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if !s.requireScope(w, r, secretsReadScope) {
		return
	}
	account := strings.TrimSpace(principal.AccountScopeID)
	slots, err := s.secretSlots.ListSlots(account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UnixMilli()
	out := make([]map[string]any, 0, len(slots))
	for _, slot := range slots {
		grants, err := s.secretSlots.ListGrants(account, slot.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		folders := []map[string]any{}
		for _, grant := range grants {
			if grant.ExpiresAt != 0 && grant.ExpiresAt <= now {
				continue
			}
			folder := map[string]any{"workspace_path": grant.WorkspacePath}
			if grant.ExpiresAt != 0 {
				folder["expires_at"] = grant.ExpiresAt
			}
			folders = append(folders, folder)
		}
		entry := map[string]any{
			"name":      slot.Name,
			"hosts":     slot.Hosts,
			"has_value": slot.HasValue,
			"granted":   folders,
		}
		if slot.Description != "" {
			entry["description"] = slot.Description
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "gateway_enabled": s.secretsGatewayEnabled, "secrets": out})
}
