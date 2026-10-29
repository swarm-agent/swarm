package runtime

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/config"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the configured bypass is effective on the permission service the
// moment the daemon builds it, before api.NewCoreServer constructs the V3
// session executor that resumes interrupted runs; a locked policy still forces
// bypass off.
// Threat (forge, 2026-10-29): bypass was applied only after NewCoreServer, so
// the first bash call of every run resumed at startup became a pending
// approval (mode auto, empty reason, no source) that nobody answered while
// the run showed as running.
// Owners: applyStartupPermissionPolicy (called by newDaemon right after
// permission.NewService) and permission.Service.AuthorizeToolCall over a real
// permission store. The daemon passes this service, already configured, to
// NewCoreServer; the API executor test
// TestStartupRecoveredRunToolCallUsesConfiguredBypass proves a recovered run's
// tool call is approved by that state. This is the narrowest layer that holds
// the daemon's own startup configuration and the authorization decision.
func TestApplyStartupPermissionPolicyBypassBeforeRecovery(t *testing.T) {
	// An ordinary command outside the built-in read-only allow list.
	const bashArgs = `{"command":"git status \u0026\u0026 printf ok \u003e marker.txt","category":"write","critical":false,"explanation":["Inspect git state and write a marker."]}`
	authorize := func(t *testing.T, cfg config.Config) (permission.AuthorizationResult, int) {
		t.Helper()
		db, err := pebblestore.Open(filepath.Join(t.TempDir(), "permissions"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		svc := permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
		applyStartupPermissionPolicy(svc, cfg)
		result, err := svc.AuthorizeToolCall(permission.AuthorizationInput{SessionID: "resumed-session", AccountScopeID: "account", RunID: "resumed-run", CallID: "call-1", Mode: "auto", ToolName: "bash", ToolArguments: bashArgs})
		if err != nil {
			t.Fatalf("authorize: %v", err)
		}
		pending, err := svc.PendingCount("resumed-session")
		if err != nil {
			t.Fatal(err)
		}
		return result, pending
	}

	// Control: with bypass off the same call is the forge's pending approval,
	// so the approval below comes from the configured bypass.
	if result, pending := authorize(t, config.Config{}); result.Decision != permission.AuthorizationPending || pending != 1 {
		t.Fatalf("bypass off: %+v pending=%d, want one pending approval", result, pending)
	}
	if result, pending := authorize(t, config.Config{BypassPermissions: true}); result.Decision != permission.AuthorizationApprove || pending != 0 {
		t.Fatalf("bypass configured on: %+v pending=%d, want approved with no pending record", result, pending)
	}
	if result, pending := authorize(t, config.Config{BypassPermissions: true, LockPermissionPolicy: true}); result.Decision == permission.AuthorizationApprove || pending != 1 {
		t.Fatalf("locked policy with bypass configured: %+v pending=%d, want bypass forced off", result, pending)
	}
}
