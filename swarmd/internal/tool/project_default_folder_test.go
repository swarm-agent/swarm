package tool_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// projectFolderFixture is a project conversation (no workspace bound to its
// session) whose project has a primary_code folder and a docs folder, both
// Git repositories in the account's workspace catalog.
type projectFolderFixture struct {
	runtime       *tool.Runtime
	store         *pebblestore.SessionStore
	scope         tool.WorkspaceScope
	primary, docs string
}

func newProjectFolderFixture(t *testing.T) *projectFolderFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Explicit recovery refuses inherited Git repository/config overrides;
	// the fixture runs with none, as the daemon does (restored after).
	for _, kv := range os.Environ() {
		if key, _, _ := strings.Cut(kv, "="); strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "")
			_ = os.Unsetenv(key)
		}
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	f := &projectFolderFixture{primary: filepath.Join(root, "primary"), docs: filepath.Join(root, "docs")}
	for _, dir := range []string{f.primary, f.docs} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		validationGit(t, dir, "init", "-b", "dev")
		validationWrite(t, filepath.Join(dir, "README.md"), "readme of "+filepath.Base(dir)+"\n")
		validationGit(t, dir, "add", "README.md")
		validationGit(t, dir, "commit", "-m", "base")
	}
	db, err := pebblestore.Open(filepath.Join(root, "state", "fixture.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f.store = pebblestore.NewSessionStore(db)
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(f.store, events)
	workspaceStore := pebblestore.NewWorkspaceStore(db)
	workspace := workspaceruntime.NewService(workspaceStore)
	worktrees := worktreeruntime.NewService(pebblestore.NewWorktreeStore(db), workspace, events)
	t.Cleanup(worktrees.Close)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "folder-user", AccountScopeID: "folder-account", SessionID: "project-chat"}
	for _, dir := range []string{f.primary, f.docs} {
		if _, _, err := workspaceStore.CreateForAccountIfAbsent(principal.AccountScopeID, dir, filepath.Base(dir), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.PutProject(principal.AccountScopeID, &pebblestore.ProjectRecord{ID: "proj-folders", AccountID: principal.AccountScopeID, Name: "Folders", Workspaces: []pebblestore.ProjectWorkspaceRef{
		{Path: f.docs, Role: "docs"}, {Path: f.primary, Role: "primary_code"},
	}}); err != nil {
		t.Fatal(err)
	}
	chat := pebblestore.SessionSnapshot{ID: principal.SessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Metadata: map[string]any{
		"swarm_v3_project_id": "proj-folders", "project_id": "proj-folders", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator",
	}}
	if err := f.store.CreateSession(chat); err != nil {
		t.Fatal(err)
	}
	// The scope the run service gives a project conversation
	// (resolveRunWorkspaceScope): no folder, explicit recovery only.
	f.scope = tool.WorkspaceScope{SessionID: chat.ID, Principal: principal, RejectScopeExpansion: true, ExplicitRepositoryRecovery: true}
	f.runtime = tool.NewRuntime(1)
	f.runtime.SetManageWorktreeServices(sessions, workspace, worktrees)
	f.runtime.SetManageProjectStore(f.store)
	t.Cleanup(func() { _ = f.runtime.Close() })
	return f
}

func (f *projectFolderFixture) call(t *testing.T, scope tool.WorkspaceScope, name string, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return f.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{CallID: "folder-" + name, Name: name, Arguments: string(raw)})
}

func bashArgs(command string) map[string]any {
	return map[string]any{"command": command, "explanation": []string{"Print the working directory."}, "category": "read", "critical": false, "timeout_ms": 5000}
}

// Purpose: in a project conversation, bash, read, search and find that name
// no folder run in the project's primary_code folder, so the Orchestrator's
// first command works instead of failing with "bash workspace path is
// required". Requirement: the folder is resolved per call from the project
// record and the account's workspace catalog (the same authority as an
// explicit workspace_path); the session stays unbound; another project folder
// still needs an explicit path; a session of another user, or a session that
// is not a project conversation, gets no default.
// Threat: the first command of every project chat failing; or a default that
// binds a folder to the session, reaches a folder outside the project's
// primary_code, or crosses principals.
// Owners: Runtime.executeOne default-folder step, projectPrimaryFolder,
// recoveryRepository and openRootedWorkspacePath, over real Pebble, workspace
// catalog, Git and shell: the tool entry is the narrowest layer that proves
// where the command ran.
func TestProjectConversationToolsDefaultToPrimaryFolder(t *testing.T) {
	f := newProjectFolderFixture(t)

	out, err := f.call(t, f.scope, "bash", bashArgs("pwd"))
	if err != nil {
		t.Fatalf("bash without a path in a project chat: %v", err)
	}
	if !strings.Contains(out, f.primary) || strings.Contains(out, f.docs) {
		t.Fatalf("bash did not run in the primary folder %s: %s", f.primary, out)
	}
	out, err = f.call(t, f.scope, "read", map[string]any{"path": "README.md"})
	if err != nil || !strings.Contains(out, "readme of primary") {
		t.Fatalf("read without a folder: %v %s", err, out)
	}
	// search and find resolve their root to the primary folder (matches
	// depend on the installed FFF helper, which this test does not need).
	for _, name := range []string{"search", "find"} {
		out, err = f.call(t, f.scope, name, map[string]any{"query": "README", "timeout_ms": 2000})
		var answer struct {
			Path string `json:"path"`
		}
		if err != nil || json.Unmarshal([]byte(out), &answer) != nil || answer.Path != f.primary {
			t.Fatalf("%s without a folder searched %q: %v %s", name, answer.Path, err, out)
		}
	}

	// Another project folder still needs an explicit path: bash with its
	// workspace_path runs there; read of an absolute path there is refused.
	explicit := bashArgs("pwd")
	explicit["workspace_path"] = f.docs
	if out, err := f.call(t, f.scope, "bash", explicit); err != nil || !strings.Contains(out, f.docs) {
		t.Fatalf("explicit docs folder: %v %s", err, out)
	}
	if out, err := f.call(t, f.scope, "read", map[string]any{"path": filepath.Join(f.docs, "README.md")}); err == nil || strings.Contains(out, "readme of docs") {
		t.Fatalf("read of another project folder without inspect_files: %v %s", err, out)
	}

	// The session itself stays unbound.
	chat, ok, err := f.store.GetSession(f.scope.SessionID)
	if err != nil || !ok || chat.WorkspacePath != "" || len(chat.WorkspaceGrants) != 0 || len(chat.TemporaryWorkspaceRoots) != 0 {
		t.Fatalf("project chat was bound to a folder: %v %+v", err, chat)
	}

	// Another user's call naming this conversation gets no default.
	other := f.scope
	other.Principal.UserID, other.Principal.AccountScopeID = "other-user", "other-account"
	if out, err := f.call(t, other, "bash", bashArgs("pwd")); err == nil || strings.Contains(out, f.primary) {
		t.Fatalf("cross-principal default: %v %s", err, out)
	}
	// A session that is not a project conversation keeps its own rule.
	if err := f.store.CreateSession(pebblestore.SessionSnapshot{ID: "plain-chat", UserID: f.scope.Principal.UserID, AccountScopeID: f.scope.Principal.AccountScopeID}); err != nil {
		t.Fatal(err)
	}
	plain := f.scope
	plain.SessionID = "plain-chat"
	if _, err := f.call(t, plain, "bash", bashArgs("pwd")); err == nil || !strings.Contains(err.Error(), "bash workspace path is required") {
		t.Fatalf("non-project session: %v", err)
	}
}

// Purpose: a project with folders but none marked primary_code gives no
// default and says how to name a folder, rather than picking one.
// Threat: a silent fallback that runs commands in a docs or auxiliary folder.
// Owners: projectPrimaryFolder through the tool entry.
func TestProjectConversationWithoutPrimaryFolderNamesTheFix(t *testing.T) {
	f := newProjectFolderFixture(t)
	if _, err := f.store.UpdateProject(f.scope.Principal.AccountScopeID, "proj-folders", func(p *pebblestore.ProjectRecord) error {
		p.Workspaces = []pebblestore.ProjectWorkspaceRef{{Path: f.docs, Role: "docs"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := f.call(t, f.scope, "bash", bashArgs("pwd"))
	if err == nil || !strings.Contains(err.Error(), "no primary_code folder") || !strings.Contains(err.Error(), "workspace_path") || strings.Contains(out, f.docs) {
		t.Fatalf("no primary folder: %v %s", err, out)
	}
}
