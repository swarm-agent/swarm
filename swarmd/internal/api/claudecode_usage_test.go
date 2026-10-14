package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/provider/claudecode"
)

type fakeClaudeCodeUsage struct {
	limits *claudecode.RateLimits
}

func (f fakeClaudeCodeUsage) Status(context.Context) claudecode.CLIStatus {
	return claudecode.CLIStatus{Path: "/opt/private/claude", Version: "2.1.296", Ready: true, Reason: "Claude Code 2.1.296 signed in",
		Auth: claudecode.AuthStatus{LoggedIn: true, AuthMethod: "claude.ai", SubscriptionType: "max"}}
}

func (f fakeClaudeCodeUsage) LatestRateLimits() (claudecode.RateLimits, bool) {
	if f.limits == nil {
		return claudecode.RateLimits{}, false
	}
	return *f.limits, true
}

// TestClaudeCodeUsageRequiresIdentityAndShowsOnlyPlanAndLimits: /claude reads
// Claude Code's sign-in, plan and latest usage windows for an identified
// caller only, never the executable path, and says rate_limits is null until a
// turn has reported one.
func TestClaudeCodeUsageRequiresIdentityAndShowsOnlyPlanAndLimits(t *testing.T) {
	server := &Server{claudeCodeUsage: fakeClaudeCodeUsage{}}

	anonymous := httptest.NewRecorder()
	server.handleClaudeCodeUsage(anonymous, httptest.NewRequest(http.MethodGet, "/v1/claudecode/usage", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonymous.Code)
	}
	post := httptest.NewRecorder()
	server.handleClaudeCodeUsage(post, requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/claudecode/usage", nil)))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", post.Code)
	}

	before := httptest.NewRecorder()
	server.handleClaudeCodeUsage(before, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/claudecode/usage", nil)))
	if before.Code != http.StatusOK || strings.Contains(before.Body.String(), "/opt/private") {
		t.Fatalf("status=%d body=%s", before.Code, before.Body.String())
	}
	var got claudeCodeUsageResponse
	if err := json.Unmarshal(before.Body.Bytes(), &got); err != nil || !got.CLI.Ready || got.CLI.SubscriptionType != "max" || got.RateLimits != nil {
		t.Fatalf("before a turn: %+v %v", got, err)
	}

	server.claudeCodeUsage = fakeClaudeCodeUsage{limits: &claudecode.RateLimits{Status: "allowed", Windows: []claudecode.RateLimitWindow{{Name: "five_hour", Utilization: 0.18}}}}
	after := httptest.NewRecorder()
	server.handleClaudeCodeUsage(after, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/claudecode/usage", nil)))
	got = claudeCodeUsageResponse{}
	if err := json.Unmarshal(after.Body.Bytes(), &got); err != nil || got.RateLimits == nil || got.RateLimits.Windows[0].Utilization != 0.18 {
		t.Fatalf("after a turn: %s %v", after.Body.String(), err)
	}

	unavailable := httptest.NewRecorder()
	(&Server{}).handleClaudeCodeUsage(unavailable, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/claudecode/usage", nil)))
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status = %d", unavailable.Code)
	}
}
