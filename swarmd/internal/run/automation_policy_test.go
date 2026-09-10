package run

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: executeProviderManagedToolCall must reject before permission or tool
// effects. A real creation event plus mutated projection proves recovery uses
// immutable policy, not agent-editable metadata; nil tool authorities detect any
// dispatch past this narrow executor boundary.
func TestAutomationPolicyDispatchRecovery(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer db.Close()
	repository := store.NewSessionStore(db)
	service := sessions.NewService(repository, nil)
	id := "automation-policy-test"
	policy, _ := json.Marshal(store.AutomationAuthorizationPolicy{AllowedTools: []string{"read"}})
	snapshot := store.SessionSnapshot{ID: id, UserID: "user", AccountScopeID: "account", WorkspacePath: t.TempDir(), WorktreeEnabled: true, Metadata: map[string]any{"automation_execution_policy": string(policy)}}
	snapshot.WorktreeRootPath = snapshot.WorkspacePath
	_, err = repository.ApplyV3SessionMutation(store.V3SessionMutationInput{SessionID: id, UserID: "user", AccountScopeID: "account", ClientRequestID: "create", IdempotencyKey: "create", PayloadHash: "create", RequestHash: "create", Kind: store.V3SessionMutationCreateSession, Session: &snapshot, NowUnixMs: 1000})
	if err != nil { t.Fatal(err) }
	for _, metadata := range []map[string]any{snapshot.Metadata, {}, {"automation_execution_policy": `{"allowed_tools":["write","task"]}`}} {
		snapshot.Metadata = metadata
		if err := repository.UpdateSession(snapshot); err != nil { t.Fatal(err) }
		// New Service on each iteration simulates run/checkpoint reconstruction.
		runs := &Service{sessions: service}
		if err := runs.enforceAutomationTool(id, "read"); err != nil { t.Fatal(err) }
		for _, name := range []string{"write", "task", "manage_sessions", "custom_tool"} {
			result, duration, err := runs.executeProviderManagedToolCall(context.Background(), providerToolInvokerConfig{sessionID: id}, tool.Call{Name: name}, nil)
			if !errors.Is(err, errAutomationPolicy) || result.Output != "" || duration != 0 { t.Fatalf("dispatch escaped: %s %#v %v", name, result, err) }
		}
	}
	if err := (&Service{}).enforceAutomationTool("ordinary", "task"); err != nil { t.Fatal("ordinary session changed", err) }
	if err := (&Service{}).enforceAutomationTool("automation-missing", "read"); !errors.Is(err, errAutomationPolicy) { t.Fatal("missing authority accepted", err) }
}

// Purpose: automationTargetPolicy must resolve exact canonical local identity;
// target strings never grant remote/custom execution. Pure policy checks isolate
// the identity and tool intersection from deployment or network side effects.
func TestAutomationPolicyTargets(t *testing.T) {
	metadata := map[string]any{"swarm_v3_runtime_kind": "host", "swarm_v3_runtime_swarm_id": "local", "swarm_v3_authority_host_swarm_id": "local"}
	policy := store.AutomationAuthorizationPolicy{TargetIDs: []string{"local"}}
	if err := automationTargetPolicy(policy, metadata); err != nil { t.Fatal(err) }
	if !automationToolPermitted(&policy, "read") { t.Fatal("local read denied") }
	for _, name := range []string{"bash", "task", "manage_sessions", "custom", "webfetch"} {
		if automationToolPermitted(&policy, name) { t.Fatal("unbounded capability", name) }
	}
	policy.TargetIDs = []string{"remote"}
	if automationTargetPolicy(policy, metadata) == nil { t.Fatal("unknown target granted") }
	policy.TargetIDs = []string{"local"}
	metadata["swarm_v3_runtime_kind"] = "runner"
	if automationTargetPolicy(policy, metadata) == nil { t.Fatal("nonlocal target granted") }
}
