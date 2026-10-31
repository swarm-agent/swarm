package api

import (
	"errors"
	"net/http"
)

// handleClaudeCodeAccounts manages machine routing, not Claude authentication.
// It is registered with the same owner-only boundary as credential management.
func (s *Server) handleClaudeCodeAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := PrincipalFromRequest(r); !ok {
		writeError(w, http.StatusUnauthorized, errors.New("product identity required"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Order []string `json:"order"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	client, ok := s.claudeCodeUsage.(claudeCodeAccountsClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, errors.New("Claude Code account slots unavailable"))
		return
	}
	if req.Order != nil {
		if err := client.OrderAccounts(req.Order); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	id, command, err := client.AddAccount()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "login_command": command})
}
