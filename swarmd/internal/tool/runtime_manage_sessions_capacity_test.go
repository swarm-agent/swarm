package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/identity"
)

type staticCapacityProvider struct {
	snapshots map[string]executioncapacity.Snapshot
}

func (s *staticCapacityProvider) ExecutionCapacitySnapshot(accountScopeID string) executioncapacity.Snapshot {
	if snap, ok := s.snapshots[accountScopeID]; ok {
		return snap
	}
	return executioncapacity.Snapshot{
		AccountScopeID:       accountScopeID,
		EffectiveLimit:       executioncapacity.DefaultActiveExecutionLimit,
		Available:            executioncapacity.DefaultActiveExecutionLimit,
		DeploymentBatchBound: executioncapacity.DeploymentBatchBound,
		SavedQuota:           executioncapacity.SavedQuotaNoneConfigured,
	}
}

// TestManageSessionsInspectCapacityUnavailable verifies that when no capacity provider
// is configured, inspect returns an explicit error rather than fabricating 100 available.
func TestManageSessionsInspectCapacityUnavailable(t *testing.T) {
	runtime := &Runtime{sessions: &gitManageSessionService{}}
	principal := identity.Principal{AccountScopeID: "account-default", UserID: "user-1"}
	scope := WorkspaceScope{Principal: principal}

	_, err := runtime.executeManageSessions(context.Background(), scope, map[string]any{"action": "inspect"})
	if err == nil || !strings.Contains(err.Error(), "execution capacity service is unavailable") {
		t.Fatalf("expected unavailable capacity error, got: %v", err)
	}
}

// TestManageSessionsInspectCapacityDefaults verifies that manage-sessions inspect
// returns standard execution capacity facts with default 100 ceiling, 8 batch bound,
// and explicitly null saved_session_quota when configured with a real capacity authority.
func TestManageSessionsInspectCapacityDefaults(t *testing.T) {
	runtime := &Runtime{sessions: &gitManageSessionService{}}
	mgr := executioncapacity.NewManager(executioncapacity.ManagerConfig{DefaultLimit: 100})
	defer mgr.Close()
	runtime.SetManageSessionCapacityProvider(mgr)

	principal := identity.Principal{AccountScopeID: "account-default", UserID: "user-1"}
	scope := WorkspaceScope{Principal: principal}

	raw, err := runtime.executeManageSessions(context.Background(), scope, map[string]any{"action": "inspect"})
	if err != nil {
		t.Fatalf("executeManageSessions inspect failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("failed to unmarshal inspect response: %v", err)
	}

	capRaw, ok := parsed["capacity"]
	if !ok || capRaw == nil {
		t.Fatalf("inspect response missing 'capacity' field: %v", raw)
	}
	capacity, ok := capRaw.(map[string]any)
	if !ok {
		t.Fatalf("expected capacity map, got %T: %#v", capRaw, capRaw)
	}

	if limit, ok := capacity["effective_overall_cap"].(float64); !ok || int(limit) != 100 {
		t.Fatalf("effective_overall_cap = %v, want 100", capacity["effective_overall_cap"])
	}
	if limit, ok := capacity["effective_limit"].(float64); !ok || int(limit) != 100 {
		t.Fatalf("effective_limit = %v, want 100", capacity["effective_limit"])
	}
	if active, ok := capacity["total_active"].(float64); !ok || int(active) != 0 {
		t.Fatalf("total_active = %v, want 0", capacity["total_active"])
	}
	if deployed, ok := capacity["deployed_active"].(float64); !ok || int(deployed) != 0 {
		t.Fatalf("deployed_active = %v, want 0", capacity["deployed_active"])
	}
	if pending, ok := capacity["pending"].(float64); !ok || int(pending) != 0 {
		t.Fatalf("pending = %v, want 0", capacity["pending"])
	}
	if avail, ok := capacity["available_slots"].(float64); !ok || int(avail) != 100 {
		t.Fatalf("available_slots = %v, want 100", capacity["available_slots"])
	}
	if avail, ok := capacity["available"].(float64); !ok || int(avail) != 100 {
		t.Fatalf("available = %v, want 100", capacity["available"])
	}
	if batch, ok := capacity["deployment_batch_bound"].(float64); !ok || int(batch) != 8 {
		t.Fatalf("deployment_batch_bound = %v, want 8", capacity["deployment_batch_bound"])
	}

	// Verify saved_session_quota is explicitly null in raw JSON
	if quota, exists := capacity["saved_session_quota"]; !exists || quota != nil {
		t.Fatalf("saved_session_quota = %#v, want explicit nil/null", quota)
	}
	if !strings.Contains(raw, `"saved_session_quota":null`) {
		t.Fatalf("raw JSON missing explicit null for saved_session_quota: %s", raw)
	}

	// Verify saved_quota string is "none configured"
	if quota, ok := capacity["saved_quota"].(string); !ok || quota != "none configured" {
		t.Fatalf("saved_quota = %v, want 'none configured'", capacity["saved_quota"])
	}

	// Verify pool model guidance
	poolModel, _ := capacity["pool_model"].(string)
	if !strings.Contains(poolModel, "one shared pool") || !strings.Contains(poolModel, "ceiling not target") || !strings.Contains(poolModel, "no per-agent") {
		t.Fatalf("pool_model = %q, want shared pool and ceiling-not-target guidance", poolModel)
	}
}

// TestManageSessionsInspectCapacityIntegratedWithSharedAuthority verifies that inspect
// reads live atomic capacity numbers from the shared execution capacity manager without
// fabricated independent counters.
func TestManageSessionsInspectCapacityIntegratedWithSharedAuthority(t *testing.T) {
	mgr := executioncapacity.NewManager(executioncapacity.ManagerConfig{DefaultLimit: 100})
	defer mgr.Close()

	accountID := "acc-shared-authority"
	lease1, err := mgr.Acquire(context.Background(), executioncapacity.AcquireRequest{
		AccountScopeID: accountID,
		SessionID:      "sess-1",
		RunID:          "run-1",
		Kind:           executioncapacity.ExecutionKindOrdinary,
	})
	if err != nil {
		t.Fatalf("mgr.Acquire sess-1 failed: %v", err)
	}
	defer lease1.Release()

	lease2, err := mgr.Acquire(context.Background(), executioncapacity.AcquireRequest{
		AccountScopeID: accountID,
		SessionID:      "sess-2",
		RunID:          "run-2",
		Kind:           executioncapacity.ExecutionKindDeployed,
	})
	if err != nil {
		t.Fatalf("mgr.Acquire sess-2 failed: %v", err)
	}
	defer lease2.Release()

	runtime := &Runtime{sessions: &gitManageSessionService{}}
	runtime.SetManageSessionCapacityProvider(mgr)

	scope := WorkspaceScope{Principal: identity.Principal{AccountScopeID: accountID, UserID: "u1"}}
	raw, err := runtime.executeManageSessions(context.Background(), scope, map[string]any{"action": "inspect"})
	if err != nil {
		t.Fatalf("executeManageSessions failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	capacity := parsed["capacity"].(map[string]any)
	if total, ok := capacity["total_active"].(float64); !ok || int(total) != 2 {
		t.Fatalf("total_active = %v, want 2", capacity["total_active"])
	}
	if deployed, ok := capacity["deployed_active"].(float64); !ok || int(deployed) != 1 {
		t.Fatalf("deployed_active = %v, want 1", capacity["deployed_active"])
	}
	if avail, ok := capacity["available"].(float64); !ok || int(avail) != 98 {
		t.Fatalf("available = %v, want 98", capacity["available"])
	}
	if availSlots, ok := capacity["available_slots"].(float64); !ok || int(availSlots) != 98 {
		t.Fatalf("available_slots = %v, want 98", capacity["available_slots"])
	}
}

// TestManageSessionsInspectCapacityAccountIsolation verifies that capacity facts
// are strictly isolated per account and do not leak across different account scopes.
func TestManageSessionsInspectCapacityAccountIsolation(t *testing.T) {
	provider := &staticCapacityProvider{
		snapshots: map[string]executioncapacity.Snapshot{
			"account-alpha": {
				AccountScopeID:       "account-alpha",
				EffectiveLimit:       50,
				TotalActive:          5,
				DeployedActive:       2,
				Pending:              1,
				Available:            45,
				DeploymentBatchBound: 8,
				SavedQuota:           executioncapacity.SavedQuotaNoneConfigured,
			},
			"account-bravo": {
				AccountScopeID:       "account-bravo",
				EffectiveLimit:       100,
				TotalActive:          0,
				DeployedActive:       0,
				Pending:              0,
				Available:            100,
				DeploymentBatchBound: 8,
				SavedQuota:           executioncapacity.SavedQuotaNoneConfigured,
			},
		},
	}

	runtime := &Runtime{sessions: &gitManageSessionService{}}
	runtime.SetManageSessionCapacityProvider(provider)

	// Inspect for Account Alpha
	scopeAlpha := WorkspaceScope{Principal: identity.Principal{AccountScopeID: "account-alpha", UserID: "user-alpha"}}
	rawAlpha, err := runtime.executeManageSessions(context.Background(), scopeAlpha, map[string]any{"action": "inspect"})
	if err != nil {
		t.Fatalf("executeManageSessions alpha failed: %v", err)
	}

	var parsedAlpha map[string]any
	if err := json.Unmarshal([]byte(rawAlpha), &parsedAlpha); err != nil {
		t.Fatalf("unmarshal alpha failed: %v", err)
	}
	capAlpha := parsedAlpha["capacity"].(map[string]any)
	if capAlpha["account_scope_id"] != "account-alpha" {
		t.Fatalf("alpha account_scope_id = %v, want account-alpha", capAlpha["account_scope_id"])
	}
	if int(capAlpha["effective_overall_cap"].(float64)) != 50 {
		t.Fatalf("alpha limit = %v, want 50", capAlpha["effective_overall_cap"])
	}
	if int(capAlpha["total_active"].(float64)) != 5 {
		t.Fatalf("alpha total_active = %v, want 5", capAlpha["total_active"])
	}
	if int(capAlpha["deployed_active"].(float64)) != 2 {
		t.Fatalf("alpha deployed_active = %v, want 2", capAlpha["deployed_active"])
	}
	if int(capAlpha["pending"].(float64)) != 1 {
		t.Fatalf("alpha pending = %v, want 1", capAlpha["pending"])
	}
	if int(capAlpha["available"].(float64)) != 45 {
		t.Fatalf("alpha available = %v, want 45", capAlpha["available"])
	}

	// Inspect for Account Bravo
	scopeBravo := WorkspaceScope{Principal: identity.Principal{AccountScopeID: "account-bravo", UserID: "user-bravo"}}
	rawBravo, err := runtime.executeManageSessions(context.Background(), scopeBravo, map[string]any{"action": "inspect"})
	if err != nil {
		t.Fatalf("executeManageSessions bravo failed: %v", err)
	}

	var parsedBravo map[string]any
	if err := json.Unmarshal([]byte(rawBravo), &parsedBravo); err != nil {
		t.Fatalf("unmarshal bravo failed: %v", err)
	}
	capBravo := parsedBravo["capacity"].(map[string]any)
	if capBravo["account_scope_id"] != "account-bravo" {
		t.Fatalf("bravo account_scope_id = %v, want account-bravo", capBravo["account_scope_id"])
	}
	if int(capBravo["effective_overall_cap"].(float64)) != 100 {
		t.Fatalf("bravo limit = %v, want 100", capBravo["effective_overall_cap"])
	}
	if int(capBravo["total_active"].(float64)) != 0 {
		t.Fatalf("bravo total_active = %v, want 0", capBravo["total_active"])
	}
	if int(capBravo["available"].(float64)) != 100 {
		t.Fatalf("bravo available = %v, want 100", capBravo["available"])
	}
}
