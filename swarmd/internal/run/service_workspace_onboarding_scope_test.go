package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Requirement: a pre-admission onboarding session has exactly one revalidated
// filesystem root and cannot reuse its agent identity outside that flow.
// Threat: forged metadata, symlink drift, or workspace scope expansion could
// turn onboarding into arbitrary host access. Runtime scope is the narrowest
// layer proving those denials before tool execution or permission creation.
func TestWorkspaceOnboardingRunScopeIsExactAndNonReusable(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "onboarding-scope.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspaceService := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	service := &Service{workspace: workspaceService}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account"}
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := agentruntime.WorkspaceOnboardingAgentProfileForParent(pebblestore.AgentProfile{Provider: "test", Model: "model", Thinking: "high"})
	metadata := map[string]any{
		"workspace_onboarding": true, "pre_admission": true, "owner_transport": "workspace_onboarding_api", "agent_profile": profile,
		"agent_name": agentruntime.WorkspaceOnboardingAgentID, "resolved_agent_name": agentruntime.WorkspaceOnboardingAgentID,
		"workspace_onboarding_path": path, "workspace_onboarding_expected_path": path,
	}
	scope, err := service.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{ID: "session", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: path, Metadata: metadata}, principal)
	if err != nil {
		t.Fatal(err)
	}
	if scope.PrimaryPath != path || len(scope.Roots) != 1 || scope.Roots[0] != path || !scope.RejectScopeExpansion || len(scope.MutationScopes) != 1 || scope.MutationScopes[0] != "**" {
		t.Fatalf("scope=%+v", scope)
	}
	if _, err := exec.Command("git", "-C", path, "init", "--initial-branch=main").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{ID: "unborn", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: path, Metadata: metadata}, principal); err != nil {
		t.Fatalf("unborn repository invalidated onboarding scope: %v", err)
	}
	if _, err := exec.Command("git", "-C", path, "-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@localhost", "commit", "--allow-empty", "--no-gpg-sign", "-m", "Initial commit").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{ID: "ready", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: path, Metadata: metadata}, principal); err != nil {
		t.Fatalf("ready repository invalidated onboarding scope before admission: %v", err)
	}
	forged := make(map[string]any, len(metadata))
	for key, value := range metadata {
		forged[key] = value
	}
	delete(forged, "workspace_onboarding")
	if _, err := service.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{ID: "forged", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: path, Metadata: forged}, principal); err == nil || !strings.Contains(err.Error(), "dedicated pre-admission flow") {
		t.Fatalf("forged scope error=%v", err)
	}
	if err := os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+"-moved", path); err != nil {
		t.Fatal(err)
	}
	if _, err := service.resolveRunWorkspaceScope(pebblestore.SessionSnapshot{ID: "drift", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: path, Metadata: metadata}, principal); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink drift error=%v", err)
	}
}

