package tool

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

// Purpose: a project created without workspaces (SDK ensureProject) has no
// place to run commands. Its orchestrator's bash call must fail with a plain
// "this project has no workspace yet" error, whether or not it names a
// workspace_path, instead of the internal "recovery repository is not in the
// account workspace catalog" wording. A project that has workspaces keeps the
// existing catalog error, and nothing runs in either case.
// Owner: Runtime.executeOne bash and recoveryRepository, the source of the
// error; the tool runtime over the real session and project store is the
// narrowest layer that joins the calling session, its project and the catalog.
func TestBashInProjectWithoutWorkspaceFailsPlainly(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "projects.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	store := pebblestore.NewSessionStore(db)
	sessions := sessionruntime.NewService(store, events)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	if err := store.PutProject("account", &pebblestore.ProjectRecord{ID: "bare", Name: "My app"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject("account", &pebblestore.ProjectRecord{ID: "linked", Name: "Linked", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: t.TempDir()}}}); err != nil {
		t.Fatal(err)
	}
	conversation := func(id, projectID string) WorkspaceScope {
		t.Helper()
		session := pebblestore.SessionSnapshot{ID: id, AccountScopeID: "account", UserID: "user", Metadata: map[string]any{
			"swarm_v3_project_id": projectID, "project_id": projectID, "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator",
		}}
		input := pebblestore.V3SessionMutationInput{SessionID: id, UserID: "user", AccountScopeID: "account", ClientRequestID: "create-" + id, PayloadHash: "create-" + id, Kind: pebblestore.V3SessionMutationCreateSession, Session: &session}
		if _, err := store.ApplyV3SessionMutation(input); err != nil {
			t.Fatal(err)
		}
		return WorkspaceScope{Principal: principal, SessionID: id, RejectScopeExpansion: true, ExplicitRepositoryRecovery: true}
	}
	bare := conversation("bare-chat", "bare")
	linked := conversation("linked-chat", "linked")

	rt := NewRuntime(1)
	rt.SetManageWorktreeServices(sessions, workspaceruntime.NewService(pebblestore.NewWorkspaceStore(db)), nil)
	rt.SetManageProjectStore(store)
	marker := filepath.Join(t.TempDir(), "ran")
	unregistered := t.TempDir()
	bash := func(scope WorkspaceScope, args string) error {
		t.Helper()
		_, err := rt.executeOne(context.Background(), scope, Call{Name: "bash", Arguments: args}, nil)
		return err
	}
	command := `"command":"touch ` + marker + `"`
	if err := bash(bare, `{`+command+`,"workspace_path":"`+unregistered+`"}`); !errors.Is(err, errProjectWithoutWorkspace) {
		t.Fatalf("bare project with workspace_path = %v, want %v", err, errProjectWithoutWorkspace)
	}
	if err := bash(bare, `{`+command+`}`); !errors.Is(err, errProjectWithoutWorkspace) {
		t.Fatalf("bare project without workspace_path = %v, want %v", err, errProjectWithoutWorkspace)
	}
	if err := bash(linked, `{`+command+`,"workspace_path":"`+unregistered+`"}`); err == nil || !strings.Contains(err.Error(), "not in the account workspace catalog") {
		t.Fatalf("project with workspaces = %v, want the catalog error", err)
	}
	if matches, _ := filepath.Glob(marker); len(matches) != 0 {
		t.Fatal("a command ran without a workspace")
	}
}
