package tool

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type environmentAccessSessions struct {
	manageSessionService
	snapshot pebblestore.SessionSnapshot
}

func (s environmentAccessSessions) GetSession(string) (pebblestore.SessionSnapshot, bool, error) {
	return s.snapshot, true, nil
}

// Purpose: direct handlers must reject durable Coder identity before any environment
// store/provider access, even after resume or with forged arguments. Handler-level
// tests are the narrowest boundary proving tool visibility cannot grant authority.
func TestEnvironmentAccessDirectHandlers(t *testing.T) {
	for _, role := range []string{"coder", "clone", "system-coder"} {
		for _, key := range []string{"agent_name", "subagent"} {
			t.Run(role+"/"+key, func(t *testing.T) {
				scope := WorkspaceScope{SessionID: "child", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}}
				r := &Runtime{sessions: environmentAccessSessions{snapshot: pebblestore.SessionSnapshot{ID: "child", AccountScopeID: "account", UserID: "user", Metadata: map[string]any{key: role, "parent_session_id": "parent", "task_program_id": "program", "agent_profile": pebblestore.AgentProfile{Name: "swarm"}}}}}
				for _, action := range []string{"list", "get", "ensure", "exec", "release"} {
					args := map[string]any{"action": action, "agent": "swarm"}
					out, err := r.executeManageEnvironments(context.Background(), scope, "call", args)
					if err == nil || !strings.Contains(err.Error(), "environment access denied") || out != "" {
						t.Fatalf("environment action %s: output=%q error=%v", action, out, err)
					}
					out, err = r.executeManageConnections(context.Background(), scope, args)
					if err == nil || !strings.Contains(err.Error(), "environment access denied") || out != "" {
						t.Fatalf("connection action %s: output=%q error=%v", action, out, err)
					}
				}
			})
		}
	}
}

// Purpose: missing identity and explicit profile denials must fail closed at the
// common admission boundary; allowed compiled roles must not require ambient cwd.
func TestEnvironmentAccessProfileAdmission(t *testing.T) {
	scope := WorkspaceScope{SessionID: "session", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}}
	for _, role := range []string{"swarm", "system-orchestrator", "", "system-coder"} {
		for _, disabled := range []bool{false, true} {
			profile := pebblestore.AgentProfile{Name: role}
			if disabled {
				profile.ToolContract = &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"manage_environments": {Enabled: pebblestore.BoolPtr(false)}}}
			}
			r := &Runtime{sessions: environmentAccessSessions{snapshot: pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "account", UserID: "user", Metadata: map[string]any{"agent_profile": profile}}}}
			_, err := r.authorizeEnvironmentAccess(scope, "manage_environments")
			want := !disabled && (role == "swarm" || role == "system-orchestrator")
			if (err == nil) != want {
				t.Fatalf("role=%q disabled=%v error=%v", role, disabled, err)
			}
		}
	}
	if _, err := (&Runtime{}).authorizeEnvironmentAccess(scope, "manage_environments"); err == nil {
		t.Fatal("missing session authority accepted")
	}
}

// Purpose: explicit project source selection must cross the existing catalog
// resolver without ambient cwd or current-binding inference. This unit boundary
// proves routing and rejection propagation; API tests own real membership checks.
func TestEnvironmentProjectSourceResolution(t *testing.T) {
	root := t.TempDir()
	scope := WorkspaceScope{SessionID: "session", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}}
	r := &Runtime{sessions: environmentAccessSessions{snapshot: pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "account", UserID: "user", Metadata: map[string]any{"project_id": "project", "agent_profile": pebblestore.AgentProfile{Name: "swarm"}}}}}
	resolver := &inspectionLifecycle{target: ProjectInspectionTarget{Root: root, Reference: ProjectInspectionRequest{WorkspaceID: "workspace"}}}
	r.projectTaskLifecycle = resolver
	account, workspace, path, err := r.resolveWorkspaceScopeForEnvironments(scope, map[string]any{"workspace_id": "workspace"}, "manage_environments")
	if err != nil || account != "account" || workspace != "workspace" || path != root {
		t.Fatalf("explicit source resolution: %q %q %q %v", account, workspace, path, err)
	}
	resolver.err = context.Canceled
	if _, _, _, err := r.resolveWorkspaceScopeForEnvironments(scope, map[string]any{"workspace_id": "workspace"}, "manage_environments"); err == nil {
		t.Fatal("source authority rejection swallowed")
	}
	scope.Principal.AccountScopeID = "foreign"
	resolver.err = nil
	if _, _, _, err := r.resolveWorkspaceScopeForEnvironments(scope, map[string]any{"workspace_id": "workspace"}, "manage_environments"); err == nil {
		t.Fatal("foreign account accepted")
	}
}
