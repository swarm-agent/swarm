package api

import (
	"context"

	"swarm/packages/swarmd/internal/provider/claudecode"
)

type fakeClaudeCodeUsage struct {
	limits  *claudecode.RateLimits
	account claudecode.Account
}

func (f fakeClaudeCodeUsage) Status(context.Context) claudecode.CLIStatus {
	return claudecode.CLIStatus{Path: "/opt/private/claude", Version: "2.1.296", Ready: true, Reason: "Claude Code 2.1.296 signed in",
		Auth: claudecode.AuthStatus{LoggedIn: true, AuthMethod: "claude.ai", SubscriptionType: "max"}, Account: f.account}
}

func (f fakeClaudeCodeUsage) LatestRateLimits() (claudecode.RateLimits, bool) {
	if f.limits == nil {
		return claudecode.RateLimits{}, false
	}
	return *f.limits, true
}
