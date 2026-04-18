package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
)

func (s *Server) handleVaultStatus(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusInternalServerError, errors.New("auth service not configured"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	status, err := s.auth.VaultStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleVaultEnable(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusInternalServerError, errors.New("auth service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.auth.EnableVault(strings.TrimSpace(req.Password))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleVaultUnlock(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusInternalServerError, errors.New("auth service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.auth.UnlockVault(strings.TrimSpace(req.Password))
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleVaultLock(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusInternalServerError, errors.New("auth service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	status, err := s.auth.LockVault()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleVaultDisable(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusInternalServerError, errors.New("auth service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.auth.DisableVault(strings.TrimSpace(req.Password))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleVaultExport(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusInternalServerError, errors.New("auth service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Password      string `json:"password"`
		VaultPassword string `json:"vault_password,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	payload, exported, err := s.auth.ExportCredentials(strings.TrimSpace(req.Password), strings.TrimSpace(req.VaultPassword))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, io.EOF) {
			status = http.StatusBadRequest
		}
		if strings.Contains(strings.ToLower(err.Error()), "unlock") {
			status = http.StatusLocked
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"exported": exported,
		"bundle":   payload,
	})
}

func (s *Server) handleVaultImport(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusInternalServerError, errors.New("auth service not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Password      string `json:"password"`
		VaultPassword string `json:"vault_password,omitempty"`
		Bundle        []byte `json:"bundle"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.auth.ImportCredentials(strings.TrimSpace(req.Password), strings.TrimSpace(req.VaultPassword), req.Bundle)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "unlock") {
			status = http.StatusLocked
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) withVaultGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s == nil || s.auth == nil {
			next.ServeHTTP(w, r)
			return
		}
		if isVaultExemptRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		status, err := s.auth.VaultStatus()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if status.Enabled && !status.Unlocked {
			writeError(w, http.StatusLocked, errors.New("vault is locked; unlock it with /vault or the desktop vault screen"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isVaultExemptRequest(r *http.Request) bool {
	switch r.URL.Path {
	case "/healthz", "/readyz", "/v1/auth/desktop/session", "/v1/vault", "/v1/vault/enable", "/v1/vault/unlock", "/v1/vault/lock", "/v1/vault/disable", "/v1/vault/export", "/v1/vault/import", "/v1/system/shutdown", "/v1/swarm/discovery", "/v1/deploy/container/sync/credentials", "/v1/deploy/container/sync/agents", "/v1/deploy/remote/session/sync/credentials":
		return true
	default:
		return false
	}
}
