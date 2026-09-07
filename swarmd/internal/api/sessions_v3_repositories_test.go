package api

import (
	"context"
	"encoding/json"
	"encoding/base64"
	"strings"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the canonical HTTP inventory must retain attachment identities and
// dirty file evidence across bounded pages without exposing another principal
// or mutating Git. This real-Git/temp-store API test exercises the narrowest
// end-to-end boundary: retained index -> catalog authorization -> status JSON.
func TestSessionRepositoriesPaginationIsolationAndCancellation(t *testing.T) {
	repoA, repoB := initGitCommitTestRepo(t), initGitCommitTestRepo(t)
	server, principal, entries := newSessionRouterTestServer(t, &sessionRouterRecordingRunner{id: "recording"}, []sessionRouterWorkspace{{repoA, "A", "A"}, {repoB, "B", "B"}})
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	sessions := pebblestore.NewSessionStore(store)
	server.sessions = sessionruntime.NewService(sessions, nil)
	owner := pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: repoA}
	for i, entry := range entries {
		kind := pebblestore.WorkspaceGrantAdditional
		if i == 0 { kind = pebblestore.WorkspaceGrantPrimary }
		owner.WorkspaceGrants = append(owner.WorkspaceGrants, pebblestore.WorkspaceGrant{Kind: kind, Path: entry.Path, Name: entry.Name, WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration})
	}
	_, err = sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: owner.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: pebblestore.V3SessionMutationCreateSession, Session: &owner, IdempotencyKey: "create", RequestHash: "create", NowUnixMs: 100})
	if err != nil { t.Fatal(err) }
	ready := false
	for i := 0; i < 5; i++ { ready, err = sessions.BackfillRepositoryHistory(10); if err != nil { t.Fatal(err) }; if ready { break } }
	if !ready { t.Fatal("history not ready") }
	if err := os.WriteFile(filepath.Join(repoB, "untracked.txt"), []byte("dirty"), 0600); err != nil { t.Fatal(err) }
	headBefore := runGitCommitTestCommand(t, repoB, "rev-parse", "HEAD")
	indexBefore := runGitCommitTestCommand(t, repoB, "diff", "--cached")
	cursor := ""
	seen := map[string]sessionRepositoryItem{}
	for n := 0; n < 5; n++ {
		r := httptest.NewRequest(http.MethodGet, "/v3/sessions/parent/repositories?limit=1&cursor="+url.QueryEscape(cursor), nil)
		r = r.WithContext(identity.ContextWithPrincipal(r.Context(), principal))
		w := httptest.NewRecorder()
		server.handleSessionV3PrimaryByID(w, r)
		if w.Code != http.StatusOK { t.Fatalf("inventory: %d %s", w.Code, w.Body.String()) }
		var page sessionRepositoriesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil { t.Fatal(err) }
		if len(page.Items) > 1 { t.Fatal("page bound exceeded") }
		for _, item := range page.Items {
			if _, exists := seen[item.ID]; exists { t.Fatal("duplicate identity") }
			if item.Status == nil || item.Availability != "available" { t.Fatalf("missing status: %+v", item) }
			seen[item.ID] = item
			if item.WorkspacePath == repoB && (item.Status.UntrackedCount != 1 || item.Default) { t.Fatalf("wrong selected status: %+v", item) }
		}
		cursor = page.NextCursor
		if n == 0 {
			for _, suffix := range []string{"limit=2&cursor="+url.QueryEscape(cursor), "limit=1&cursor="+base64.RawURLEncoding.EncodeToString([]byte(`{"phase":"sessions","offset":1}`)), "limit=1&cursor="+strings.Repeat("a", 24001)} {
				bad := httptest.NewRecorder()
				server.handleSessionV3Repositories(bad, httptest.NewRequest(http.MethodGet, "/?"+suffix, nil), principal, owner.ID)
				if bad.Code == http.StatusOK { t.Fatal("forged/limit-changed continuation accepted") }
			}
		}
		if cursor == "" { break }
	}
	if len(seen) != 2 || cursor != "" { t.Fatalf("incomplete inventory: %d", len(seen)) }
	for _, mutate := range []func(*identity.Principal){func(p *identity.Principal) { p.UserID = "foreign" }, func(p *identity.Principal) { p.AccountScopeID = "foreign" }} {
		other := principal; mutate(&other)
		w := httptest.NewRecorder()
		server.handleSessionV3Repositories(w, httptest.NewRequest(http.MethodGet, "/?limit=1", nil), other, owner.ID)
		if w.Code == http.StatusOK { t.Fatal("foreign principal accepted") }
	}
	ctx, cancel := context.WithCancel(context.Background()); cancel()
	w := httptest.NewRecorder()
	server.handleSessionV3Repositories(w, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx), principal, owner.ID)
	if w.Code != http.StatusRequestTimeout { t.Fatalf("cancellation ignored: %d", w.Code) }
	for _, path := range []string{repoB, filepath.Join(repoB, "missing")} {
		r := httptest.NewRequest(http.MethodGet, "/?session_id=parent&workspace_path="+url.QueryEscape(path), nil)
		got, err := server.resolveGitStatusWorkspacePath(r, principal)
		if path == repoB && (err != nil || got != repoB) { t.Fatalf("explicit path switched: %q %v", got, err) }
		if path != repoB && err == nil { t.Fatal("unknown selector accepted") }
	}
	got, err := server.resolveGitCommitWorkspacePath(workspaceGitCommitRequest{WorkspacePath: repoB}, principal, owner.ID)
	if err != nil || got != repoB { t.Fatalf("commit switched selection: %q %v", got, err) }
	stale := sessionRepositoryItem{WorkspaceID: entries[1].WorkspaceID, WorkspaceGeneration: entries[1].WorkspaceGeneration + 1, SourcePath: repoB, WorkspacePath: repoB}
	if err := server.authorizeRepositoryItem(principal, stale); err == nil { t.Fatal("stale generation accepted") }
	// Detached history remains inspectable, but cannot authorize a commit.
	owner.WorkspaceGrants = owner.WorkspaceGrants[:1]
	_, err = sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: owner.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: pebblestore.V3SessionMutationUpdateMetadata, Session: &owner, IdempotencyKey: "detach", RequestHash: "detach", NowUnixMs: 200})
	if err != nil { t.Fatal(err) }
	if item, err := server.selectedSessionRepository(principal, owner.ID, repoB); err != nil || item.WorkspacePath != repoB { t.Fatalf("retained exact lookup: %+v %v", item, err) }
	if path, err := server.resolveGitCommitWorkspacePath(workspaceGitCommitRequest{WorkspacePath: repoB}, principal, owner.ID); err == nil || path != "" { t.Fatal("retained row authorized mutation") }
	if after := runGitCommitTestCommand(t, repoB, "rev-parse", "HEAD"); after != headBefore { t.Fatal("read changed HEAD") }
	if after := runGitCommitTestCommand(t, repoB, "diff", "--cached"); after != indexBefore { t.Fatal("read staged files") }
}

// Purpose: repositorySessionItems must not collapse distinct attachments or
// forget retained worker provenance. A pure projection test isolates identity
// construction from filesystem availability and current sidebar state.
func TestSessionRepositoriesRetainedWorkerIdentity(t *testing.T) {
	root := t.TempDir()
	source, lane := filepath.Join(root, "source"), filepath.Join(root, "lane")
	owner := pebblestore.SessionSnapshot{ID: "worker", WorkspacePath: source, WorktreeEnabled: true, WorktreeRootPath: lane, WorktreeBranch: "agent/worker", Metadata: map[string]any{"base_commit": "base", "integration_status": "dirty-recoverable"}, WorkspaceGrants: []pebblestore.WorkspaceGrant{{Kind: "primary", Path: source, WorkspaceID: "a", WorkspaceGeneration: 1}, {Kind: "additional", Path: source, WorkspaceID: "b", WorkspaceGeneration: 1}, {Kind: "worktree", Path: lane}}}
	items := repositorySessionItems(pebblestore.SessionRepositoryHistory{Session: owner, ContextID: "retained"}, "parent")
	changed := owner
	changed.WorkspaceGrants = append([]pebblestore.WorkspaceGrant(nil), owner.WorkspaceGrants...)
	changed.WorkspaceGrants[0].Kind = pebblestore.WorkspaceGrantAdditional
	again := repositorySessionItems(pebblestore.SessionRepositoryHistory{Session: changed, ContextID: "new-default"}, "parent")
	if items[0].ID != again[0].ID || items[2].ID != again[2].ID { t.Fatal("context/default changed logical identity") }
	if len(items) != 3 || items[0].ID == items[1].ID { t.Fatalf("collapsed attachments: %+v", items) }
	worker := items[2]
	if worker.Kind != "worker" || worker.BaseCommit != "base" || worker.SourcePath != source || worker.WorkspacePath != lane || worker.Lifecycle != "dirty-recoverable" { t.Fatalf("lost worker provenance: %+v", worker) }
}
