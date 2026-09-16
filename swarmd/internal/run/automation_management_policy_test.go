package run

import (
	"testing"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: management purpose is presentation/context, not execution policy.
// Threat: interactive Swarm loses tools merely because it is hidden in Automations.
// automationPolicy and enforceAutomationTool own the overlay; real durable sessions
// in both modes prove this boundary without providers or permission side effects.
// This does not grant tools excluded by the ordinary mode/account policy.
func TestAutomationManagementToolParity(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository := store.NewSessionStore(db)
	service := sessions.NewService(repository, nil)
	for _, mode := range []string{"plan", "auto"} {
		snapshot := store.SessionSnapshot{ID: "management-" + mode, UserID: "user", AccountScopeID: "account", Mode: mode, WorkspacePath: t.TempDir(), Metadata: map[string]any{store.SessionPurposeMetadataKey: store.SessionPurposeAutomationManagement, store.SessionPurposeWorkspaceMetadataKey: "workspace"}, WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: "workspace"}}}
		_, err := repository.ApplyV3SessionMutation(store.V3SessionMutationInput{SessionID: snapshot.ID, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID, ClientRequestID: snapshot.ID, IdempotencyKey: snapshot.ID, PayloadHash: snapshot.ID, RequestHash: snapshot.ID, Kind: store.V3SessionMutationCreateSession, Session: &snapshot, NowUnixMs: 1000})
		if err != nil {
			t.Fatal(err)
		}
		runs := &Service{sessions: service}
		policy, err := runs.automationPolicy(snapshot.ID)
		if err != nil || policy != nil {
			t.Fatalf("management acquired execution overlay: %+v %v", policy, err)
		}
		for _, name := range []string{"read", "write", "bash", "task", "manage_sessions", "manage_automation", "plan_manage"} {
			if err := runs.enforceAutomationTool(snapshot.ID, name); err != nil {
				t.Fatalf("%s: %s denied: %v", mode, name, err)
			}
		}
		current, found, err := repository.GetSession(snapshot.ID)
		if err != nil || !found || current.Automation != nil || current.Mode != mode {
			t.Fatalf("policy inspection changed session: %+v %v", current, err)
		}
	}
	// Management parity must not weaken an admitted occurrence's restricted policy.
	policy := &store.AutomationAuthorizationPolicy{AllowedTools: []string{"read"}}
	if automationToolPermitted(policy, "manage_automation") || automationToolPermitted(policy, "task") {
		t.Fatal("execution overlay widened")
	}
}
