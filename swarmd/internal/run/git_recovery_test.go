package run

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: validated project conversations must retain empty ambient scope while
// explicit catalog-authorized Bash reaches the selected repo. The service scope
// resolver plus real runtime is the narrowest cross-boundary regression for the
// original no-checkout failure; a foreign account and delegated scope stay denied.
func TestGitRecoveryProjectScopeRuntime(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "project.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	if err := store.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	s := &Service{sessions: sessionruntime.NewService(store, nil)}
	session := pebblestore.SessionSnapshot{ID: "conversation", AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{"swarm_v3_project_id": "project", "project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}}
	scope, err := s.ResolveRuntimeWorkspaceScope(session, p)
	if err != nil {
		t.Fatal(err)
	}
	if !scope.ExplicitRepositoryRecovery || scope.PrimaryPath != "" || len(scope.Roots) != 0 || !scope.RejectScopeExpansion {
		t.Fatalf("wrong project scope: %+v", scope)
	}
	repo := programFixtureRepo(t)
	catalog := pebblestore.NewWorkspaceStore(db)
	if _, err := catalog.AddForAccount(p.AccountScopeID, repo, "Recovery"); err != nil {
		t.Fatal(err)
	}
	runtime := tool.NewRuntime(1)
	runtime.SetManageWorktreeServices(nil, workspace.NewService(catalog), nil)
	raw, _ := json.Marshal(map[string]any{"workspace_path": repo, "command": "git rev-parse HEAD", "category": "read", "critical": false, "explanation": []string{"Inspect selected repository."}})
	call := tool.Call{Name: "bash", Arguments: string(raw)}
	out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, call)
	if err != nil || !strings.Contains(out, programFixtureGit(t, repo, "rev-parse", "HEAD")) {
		t.Fatalf("explicit project Bash: %s %v", out, err)
	}
	scope.Principal.AccountScopeID = "foreign"
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, call); err == nil {
		t.Fatal("foreign catalog accepted")
	}
}

// Purpose: recovery permission payloads must not demand attribution, but remain
// subject to the normal explicit deny policy. This tests the permission-admission
// owner separately from Git, avoiding session fixtures masking the original bug.
func TestGitRecoveryPermissionPayloadWithoutAttribution(t *testing.T) {
	s := &Service{}
	raw := `{"action":"commit","recovery":true,"workspace_path":"/selected","files":["file"],"expected_branch":"dev","expected_head":"head","request_id":"key","message":"recover"}`
	payload, err := s.buildManageSessionsPermissionPayload("missing", tool.Call{Name: "manage-sessions", Arguments: raw})
	if err != nil || payload["approved_arguments"] == nil {
		t.Fatalf("attribution still required: %v %v", payload, err)
	}
	// The existing classifier must continue classifying recovery as a commit,
	// rather than a read-only or bypass operation.
	policy := permission.NormalizePolicy(permission.Policy{Version: 1, Rules: []permission.PolicyRule{{Kind: permission.PolicyRuleKindTool, Tool: "session_commit", Decision: permission.PolicyDecisionDeny}}})
	if decision := permission.ExplainPolicy("auto", "manage-sessions", raw, policy); decision.Decision != permission.PolicyDecisionDeny {
		t.Fatalf("explicit denial bypassed: %+v", decision)
	}
	if decision := permission.ExplainPolicy("auto", "manage-sessions", raw, permission.Policy{}); decision.Decision != permission.PolicyDecisionAsk {
		t.Fatalf("recovery lost commit approval: %+v", decision)
	}
}
