package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: CreateProjectTask must reject omitted/ambiguous and non-Git coding
// sources before reservation or allocation, while a project conversation remains
// unchanged and can admit a message. HTTP admission plus real Pebble postconditions
// is the narrowest layer proving this separation; no provider or live daemon runs.
func TestProjectCodingSourceAdmission(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	projectID := f.createProject(t)
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.userID, AccountScopeID: f.accountID}
	store := f.server.sessions.Store()
	project, _, err := store.GetProject(p.AccountScopeID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	repo := project.Workspaces[0]
	folder := t.TempDir()
	entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(p.AccountScopeID, folder, "Context")
	if err != nil {
		t.Fatal(err)
	}
	conversation := pebblestore.SessionSnapshot{ID: "source-conversation", AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{"project_id": projectID, "swarm_v3_project_id": projectID, "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}}
	created, err := f.server.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: conversation.ID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Kind: sessionruntime.SessionMutationCreateSession, Session: &conversation, ClientRequestID: "source-conversation", IdempotencyKey: "source-conversation", PayloadHash: "source-conversation", RequestHash: "source-conversation"})
	if err != nil || created.Session == nil || created.Error != nil || created.Conflict != nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	before, found, err := f.server.sessions.GetSession(conversation.ID)
	if err != nil || !found {
		t.Fatalf("read conversation: %v", err)
	}
	for _, tc := range []struct {
		name, path string
		mixed      bool
		want       string
	}{
		{name: "single omitted", want: "coding task source required"},
		{name: "mixed omitted", mixed: true, want: "coding task source required"},
		{name: "non git", path: folder, mixed: true, want: "Git setup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project.Workspaces = []pebblestore.ProjectWorkspaceRef{repo}
			if tc.mixed {
				project.Workspaces = append(project.Workspaces, pebblestore.ProjectWorkspaceRef{WorkspaceID: entry.WorkspaceID, Path: folder})
			}
			if err := store.PutProject(p.AccountScopeID, project); err != nil {
				t.Fatal(err)
			}
			w := f.callAPI(http.MethodPost, "/"+projectID+"/tasks", map[string]any{"id": "blocked-code", "title": "Change", "prompt": "Implement change", "agent": "coder", "workspace_path": tc.path}, p)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("rejection: %d %s", w.Code, w.Body.String())
			}
			if _, found, err := store.GetProjectTask(p.AccountScopeID, projectID, "blocked-code"); err != nil || found || f.wt.allocCalls != 0 {
				t.Fatalf("partial task: %v %v allocations=%d", found, err, f.wt.allocCalls)
			}
			after, found, err := f.server.sessions.GetSession(conversation.ID)
			if err != nil || !found || !reflect.DeepEqual(before, after) {
				t.Fatalf("task rejection changed conversation: %+v %v", after, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(folder, ".git")); !os.IsNotExist(err) {
		t.Fatalf("non-Git source initialized: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+conversation.ID+"/messages", strings.NewReader(`{"client_request_id":"after-block","role":"user","content":"Continue discussing the project"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), productPrincipalRequestContextKey, p)
	ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:write"}})
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, req.WithContext(ctx))
	if response.Code != http.StatusOK {
		t.Fatalf("chat after rejection: %d %s", response.Code, response.Body.String())
	}
	after, found, err := f.server.sessions.GetSession(conversation.ID)
	if err != nil || !found {
		t.Fatalf("conversation: %+v %v", after, err)
	}
	if err := store.ValidateProjectConversation(after, p.AccountScopeID, p.UserID); err != nil {
		t.Fatal(err)
	}
	// Retry the same rejected identity with an explicit Git-ready source.
	task := requireMatrixTaskResponse(t, f.callAPI(http.MethodPost, "/"+projectID+"/tasks", map[string]any{"id": "blocked-code", "title": "Change", "prompt": "Implement change", "agent": "coder", "workspace_path": repo.Path, "auto_approve": false}, p), http.StatusCreated)
	if task["status"] != "in_progress" {
		t.Fatalf("retry: %+v", task)
	}
	saved, found, err := store.GetProjectTask(p.AccountScopeID, projectID, "blocked-code")
	if err != nil || !found || saved.SourceWorkspace.Path != repo.Path || saved.SourceWorkspace.Provenance != "explicit" {
		t.Fatalf("source: %+v %v", saved, err)
	}
	child, found, err := f.server.sessions.GetSession(saved.SessionID)
	if err != nil || !found || !child.WorktreeEnabled || child.WorkspacePath == repo.Path || f.wt.allocCalls != 1 {
		t.Fatalf("isolated child: %+v %v allocations=%d", child, err, f.wt.allocCalls)
	}
	if err := verifyProjectTaskSession(saved, child, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
}
