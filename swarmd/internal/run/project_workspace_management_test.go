package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

// Purpose: executeManageWorkspaceTool must support account catalog CRUD from an
// authenticated project conversation without acquiring checkout authority. Real
// temporary catalog, topology and V3 stores are the narrowest layer proving
// persistence, principal/scope/generation rejection and filesystem preservation;
// this is not a provider or live-daemon test.
func TestProjectConversationWorkspaceCatalogManagement(t *testing.T) {
	workspaceSvc, _, db, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()
	store := pebblestore.NewSessionStore(db)
	p := testRunPrincipal()
	p.SessionID = "project-catalog"
	if err := store.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(store, nil)
	parent := pebblestore.SessionSnapshot{ID: p.SessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Mode: "auto", Metadata: map[string]any{
		"swarm_v3_project_id": "project", "project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator",
	}}
	created, err := sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: parent.ID, AccountScopeID: p.AccountScopeID, UserID: p.UserID,
		Kind: sessionruntime.SessionMutationCreateSession, Session: &parent,
		ClientRequestID: "create", IdempotencyKey: "create", PayloadHash: "create", RequestHash: "create",
	})
	if err != nil || created.Session == nil || created.Error != nil || created.Conflict != nil {
		t.Fatalf("create project conversation: %+v %v", created, err)
	}
	nodes := pebblestore.NewSwarmStore(db)
	if _, err := nodes.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local", Name: "Local", Role: "host"}); err != nil {
		t.Fatal(err)
	}
	workspaceSvc.SetLocalBindingService(topologyruntime.NewService(pebblestore.NewTopologyStore(db), nodes))
	svc := NewService(sessions, nil, nil, nil, nil, nil, nil, nil)
	svc.SetWorkspaceService(workspaceSvc)
	svc.SetSessionWorkspaceCanonicalizer(func(SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		t.Error("project catalog management attempted checkout selection")
		return SessionWorkspaceCanonicalization{}, errors.New("no ambient checkout")
	})
	assertCheckoutFree := func() {
		t.Helper()
		got, ok, err := sessions.GetSession(parent.ID)
		if err != nil || !ok {
			t.Fatalf("get conversation: %v", err)
		}
		if err := store.ValidateProjectConversation(got, p.AccountScopeID, p.UserID); err != nil {
			t.Fatal(err)
		}
		if got.WorkspacePath != "" || len(got.WorkspaceGrants) != 0 || got.WorktreeEnabled {
			t.Fatalf("project acquired filesystem authority: %+v", got)
		}
		if _, selected, err := workspaceSvc.CurrentBindingForPrincipal(p); err != nil || selected {
			t.Fatalf("catalog operation selected default: %v %v", selected, err)
		}
	}
	call := func(args map[string]any) map[string]any {
		t.Helper()
		arguments := mustJSON(t, args)
		approved := ""
		if manageWorkspaceMutationAction(arguments) != "" {
			payload, err := svc.buildManageWorkspacePermissionPayload(parent.ID, arguments)
			if err != nil {
				t.Fatal(err)
			}
			approved = mustJSON(t, payload)
		}
		handled, response, err := svc.executeControlPlaneToolWithMutation(identity.ContextWithPrincipal(context.Background(), p), parent.ID, "auto", agent.SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{}), 1, tool.Call{Name: "manage_workspace", Arguments: arguments}, approved, nil, sessions.ApplySessionMutation)
		if err != nil || !handled {
			t.Fatalf("control-plane dispatch: handled=%v err=%v", handled, err)
		}
		out := response.Output
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil || result["status"] != "ok" {
			t.Fatalf("result: %s %v", out, err)
		}
		assertCheckoutFree()
		return result
	}
	for _, profile := range []pebblestore.AgentProfile{
		agent.CoderAgentProfileForParent(pebblestore.AgentProfile{}),
		agent.FinderAgentProfileForParent(pebblestore.AgentProfile{}),
	} {
		handled, _, err := svc.executeControlPlaneTool(identity.ContextWithPrincipal(context.Background(), p), parent.ID, "auto", profile, 1, tool.Call{Name: "manage_workspace", Arguments: `{"action":"list"}`}, "", nil)
		if !handled || err == nil || !strings.Contains(err.Error(), "restricted") {
			t.Fatalf("restricted role admitted: %s %v", profile.Name, err)
		}
	}
	root := programFixtureRepo(t)
	unapproved := mustJSON(t, map[string]any{"action": "create", "workspace_path": root})
	if _, _, err := svc.executeControlPlaneTool(identity.ContextWithPrincipal(context.Background(), p), parent.ID, "auto", agent.SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{}), 1, tool.Call{Name: "manage_workspace", Arguments: unapproved}, "", nil); err == nil || !strings.Contains(err.Error(), "approved canonical arguments") {
		t.Fatalf("unapproved mutation admitted: %v", err)
	}
	if entries, err := workspaceSvc.ListKnownForPrincipal(p, 10); err != nil || len(entries) != 0 {
		t.Fatalf("unapproved mutation changed catalog: %+v %v", entries, err)
	}
	assertCheckoutFree()
	result := call(map[string]any{"action": "create", "workspace_path": root, "workspace_name": "Disposable", "intent": "Register repository"})
	target := result["target"].(map[string]any)
	id := target["workspace_id"].(string)
	generation := target["workspace_generation"]
	for _, action := range []string{"inspect", "list"} {
		result := call(map[string]any{"action": action})
		entries := result["workspaces"].([]any)
		if len(entries) != 1 || entries[0].(map[string]any)["workspace_id"] != id {
			t.Fatalf("catalog inspection lost registered identity: %+v", result)
		}
	}
	originalRoot := root
	root = programFixtureRepo(t)
	result = call(map[string]any{"action": "update", "workspace_id": id, "workspace_generation": generation, "workspace_path": root, "workspace_name": "Renamed", "intent": "Rename registration"})
	currentGeneration := result["target"].(map[string]any)["workspace_generation"]
	entry, ok, err := workspaceSvc.GetByWorkspaceIDForPrincipal(p, id)
	if err != nil || !ok || entry.Name != "Renamed" {
		t.Fatalf("update not persisted: %+v %v", entry, err)
	}
	foreignAccount, foreignUser, foreignSession := p, p, p
	foreignAccount.AccountScopeID = "foreign-account"
	foreignUser.UserID = "foreign-user"
	foreignSession.SessionID = "foreign-session"
	for _, tc := range []struct {
		name       string
		principal  identity.Principal
		generation any
		scope      string
	}{
		{"account", foreignAccount, currentGeneration, "workspace_delete"},
		{"user", foreignUser, currentGeneration, "workspace_delete"},
		{"session", foreignSession, currentGeneration, "workspace_delete"},
		{"scope", p, currentGeneration, "workspace_update"},
		{"stale", p, generation, "workspace_delete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.executeManageWorkspaceTool(parent.ID, mustJSON(t, map[string]any{"action": "delete", "workspace_id": id, "workspace_generation": tc.generation, "intent": "Unlink registration", "permission_scope": tc.scope}), tc.principal, sessions.ApplySessionMutation)
			if err == nil {
				t.Fatal("unauthorized/stale mutation accepted")
			}
			after, ok, err := workspaceSvc.GetByWorkspaceIDForPrincipal(p, id)
			if err != nil || !ok || !reflect.DeepEqual(entry, after) {
				t.Fatalf("rejected mutation changed catalog: %+v %v", after, err)
			}
			assertCheckoutFree()
		})
	}
	for _, action := range []string{"set_session", "adopt_worktree"} {
		_, err := svc.executeManageWorkspaceTool(parent.ID, mustJSON(t, map[string]any{"action": action}), p, sessions.ApplySessionMutation)
		if err == nil || !strings.Contains(err.Error(), "checkout-free project conversation") {
			t.Fatalf("%s did not clearly reject project retargeting: %v", action, err)
		}
		assertCheckoutFree()
	}
	for _, action := range []string{"reclaim_worktree", "copy_worktree", "cancel_worktree_recovery"} {
		payload := map[string]any{
			"action":             action,
			"owner_session_id":   "owner",
			"ownership_revision": 1,
			"head":               "head",
			"fingerprint":        strings.Repeat("0", 64),
			"operation_id":       "op",
		}
		if action == "copy_worktree" {
			payload["files"] = []any{"file"}
		}
		_, err := svc.executeManageWorkspaceTool(parent.ID, mustJSON(t, payload), p, sessions.ApplySessionMutation)
		if err == nil || !strings.Contains(err.Error(), "checkout-free project conversation") {
			t.Fatalf("%s did not clearly reject project retargeting: %v", action, err)
		}
		assertCheckoutFree()
	}
	call(map[string]any{"action": "delete", "workspace_id": id, "workspace_generation": currentGeneration, "intent": "Unlink registration only"})
	entries, err := workspaceSvc.ListKnownForPrincipal(p, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("deleted registration remains: %+v %v", entries, err)
	}
	for _, path := range []string{originalRoot, root} {
		if data, err := os.ReadFile(filepath.Join(path, "source.txt")); err != nil || string(data) != "base\n" {
			t.Fatalf("catalog mutation changed files: %q %v", data, err)
		}
	}
}
