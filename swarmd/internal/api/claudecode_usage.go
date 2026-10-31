package api

import (
	"context"
	"errors"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/claudecode"
)

// claudeCodeUsageClient is the local Claude Code CLI as /claude reads it: the
// cached install and sign-in state, and the latest subscription usage reading a
// turn reported. Nothing here holds or returns credentials.
type claudeCodeUsageClient interface {
	Status(ctx context.Context) claudecode.CLIStatus
	LatestRateLimits() (claudecode.RateLimits, bool)
}

type claudeCodeUsageCLI struct {
	Ready            bool   `json:"ready"`
	Version          string `json:"version,omitempty"`
	LoggedIn         bool   `json:"logged_in"`
	AuthMethod       string `json:"auth_method,omitempty"`
	SubscriptionType string `json:"subscription_type,omitempty"`
	// The signed-in Claude account (identity only, never a credential), so
	// machines that share one account can be told apart from ones that do not.
	AccountEmail     string `json:"account_email,omitempty"`
	AccountUUID      string `json:"account_uuid,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	OrganizationUUID string `json:"organization_uuid,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

type claudeCodeUsageResponse struct {
	CLI        claudeCodeUsageCLI     `json:"cli"`
	RateLimits *claudecode.RateLimits `json:"rate_limits"`
}

func (s *Server) SetClaudeCodeUsageClient(client claudeCodeUsageClient) {
	if s == nil {
		return
	}
	s.claudeCodeUsage = client
}

// handleClaudeCodeUsage serves /claude: Claude Code readiness, plan and the
// latest five-hour and weekly usage Claude Code reported. The limits are known
// only after a Claude Code turn has run since the daemon started.
func (s *Server) handleClaudeCodeUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	ctx, ok := codexAccountRequestContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.claudeCodeUsage == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("claude code is unavailable"))
		return
	}
	status := s.claudeCodeUsage.Status(ctx)
	out := claudeCodeUsageResponse{CLI: claudeCodeUsageCLI{
		Ready:            status.Ready,
		Version:          status.Version,
		LoggedIn:         status.Auth.LoggedIn,
		AuthMethod:       status.Auth.AuthMethod,
		SubscriptionType: status.Auth.SubscriptionType,
		AccountEmail:     status.Account.EmailAddress,
		AccountUUID:      status.Account.AccountUUID,
		OrganizationName: status.Account.OrganizationName,
		OrganizationUUID: status.Account.OrganizationUUID,
		Reason:           status.Reason,
	}}
	if limits, ok := s.claudeCodeUsage.LatestRateLimits(); ok {
		out.RateLimits = &limits
	}
	writeJSON(w, http.StatusOK, out)
}
