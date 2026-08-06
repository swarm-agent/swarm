package api

import (
	"errors"
	"net/http"
	"strings"
)

const TailscaleOnboardingApprovalPath = "/v1/onboarding/tailscale-origin"

type tailscaleOnboardingApprovalResponse struct {
	Required bool   `json:"required"`
	Origin   string `json:"origin,omitempty"`
}

func (s *Server) handleTailscaleOnboardingApproval(w http.ResponseWriter, r *http.Request) {
	pending, requiresApproval := pendingDesktopOrigin(r)
	if !requiresApproval {
		if _, admitted := admittedDesktopOrigin(r); !admitted {
			writeError(w, http.StatusForbidden, errors.New("desktop origin approval context is required"))
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, tailscaleOnboardingApprovalResponse{
			Required: requiresApproval,
			Origin:   pending.origin,
		})
	case http.MethodPost:
		if !requiresApproval {
			writeJSON(w, http.StatusOK, tailscaleOnboardingApprovalResponse{})
			return
		}
		if !isExplicitPendingTailscaleApproval(r, pending.origin) {
			writeError(w, http.StatusForbidden, errors.New("same-origin browser approval is required"))
			return
		}
		if _, status, err := s.approveTailscaleOrigin(r.Context(), pending.origin); err != nil {
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, tailscaleOnboardingApprovalResponse{})
	default:
		methodNotAllowed(w)
	}
}

func isExplicitPendingTailscaleApproval(r *http.Request, origin string) bool {
	if r == nil || strings.TrimSpace(r.Header.Get("Origin")) == "" {
		return false
	}
	if !browserHeadersMatchAdmittedOrigin(r, origin) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "same-origin")
}
