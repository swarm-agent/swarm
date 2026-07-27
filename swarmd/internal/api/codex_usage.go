package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/codex"
)

const maxCodexConsumeRequestBytes int64 = 16 << 10

type codexConsumeResetCreditRequest struct {
	CreditID       string `json:"credit_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Server) handleCodexAccountUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	ctx, ok := codexAccountRequestContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.codexAccount == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("codex account service is unavailable"))
		return
	}
	usage, err := s.codexAccount.GetAccountUsage(ctx)
	if err != nil {
		writeCodexAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) handleCodexResetCredits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	ctx, ok := codexAccountRequestContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.codexAccount == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("codex account service is unavailable"))
		return
	}
	credits, err := s.codexAccount.GetResetCredits(ctx)
	if err != nil {
		writeCodexAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credits)
}

func (s *Server) handleCodexConsumeResetCredit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	ctx, ok := codexAccountRequestContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.codexAccount == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("codex account service is unavailable"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCodexConsumeRequestBytes)
	var req codexConsumeResetCreditRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid reset credit request"))
		return
	}
	req.CreditID = strings.TrimSpace(req.CreditID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.CreditID == "" {
		writeError(w, http.StatusBadRequest, errors.New("credit_id is required"))
		return
	}
	if req.IdempotencyKey == "" {
		writeError(w, http.StatusBadRequest, errors.New("idempotency_key is required"))
		return
	}
	if len(req.CreditID) > 1024 || len(req.IdempotencyKey) > 256 {
		writeError(w, http.StatusBadRequest, errors.New("reset credit request fields are too long"))
		return
	}
	out, err := s.codexAccount.ConsumeResetCredit(ctx, codex.ConsumeResetCreditRequest{
		CreditID: req.CreditID, RedeemRequestID: req.IdempotencyKey,
	})
	if err != nil {
		writeCodexAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func codexAccountRequestContext(r *http.Request) (context.Context, bool) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return nil, false
	}
	return identity.ContextWithPrincipal(r.Context(), principal), true
}

func writeCodexAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrPrincipalRequired):
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
	case errors.Is(err, codex.ErrCodexAccountOAuthRequired),
		strings.Contains(err.Error(), "not configured"),
		strings.Contains(err.Error(), "reconnect ChatGPT OAuth"),
		strings.Contains(err.Error(), "oauth record is incomplete"):
		writeError(w, http.StatusConflict, errors.New("Codex account usage requires connected ChatGPT OAuth; open Auth settings to connect it"))
	default:
		writeError(w, http.StatusBadGateway, errors.New("Codex account service request failed"))
	}
}
