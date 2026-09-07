package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// Purpose: the canonical HTTP inventory must retain attachment identities and
// dirty file evidence across bounded pages without exposing another principal
// or mutating Git. This real-Git/temp-store API test exercises the narrowest
// end-to-end boundary: retained index -> catalog authorization -> status JSON.
func TestSessionRepositoriesPaginationIsolationAndCancellation(t *testing.T) {
	repoA, repoB := initGitCommitTestRepo(t), initGitCommitTestRepo(t)
	server, principal, entries := newSessionRouterTestServer(t, &sessionRouterRecordingRunner{id: "recording"}, []sessionRouterWorkspace{{repoA, "A", "A"}, {repoB, "B", "B"}})
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions := pebblestore.NewSessionStore(store)
	server.sessions = sessionruntime.NewService(sessions, nil)
	owner := pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: repoA}
	for i, entry := range entries {
		kind := pebblestore.WorkspaceGrantAdditional
		if i == 0 {
			kind = pebblestore.WorkspaceGrantPrimary
		}
		owner.WorkspaceGrants = append(owner.WorkspaceGrants, pebblestore.WorkspaceGrant{Kind: kind, Path: entry.Path, Name: entry.Name, WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration})
	}
	_, err = sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: owner.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: pebblestore.V3SessionMutationCreateSession, Session: &owner, IdempotencyKey: "create", RequestHash: "create", NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	ready := false
	for i := 0; i < 5; i++ {
		ready, err = sessions.BackfillRepositoryHistory(10)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			break
		}
	}
	if !ready {
		t.Fatal("history not ready")
	}
	if err := os.WriteFile(filepath.Join(repoB, "untracked.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	headBefore := runGitCommitTestCommand(t, repoB, "rev-parse", "HEAD")
	indexBefore := runGitCommitTestCommand(t, repoB, "diff", "--cached")
	cursor := ""
	seen := map[string]sessionRepositoryItem{}
	for n := 0; n < 5; n++ {
		r := httptest.NewRequest(http.MethodGet, "/v3/sessions/parent/repositories?limit=1&cursor="+url.QueryEscape(cursor), nil)
		r = r.WithContext(identity.ContextWithPrincipal(r.Context(), principal))
		w := httptest.NewRecorder()
		server.handleSessionV3PrimaryByID(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("inventory: %d %s", w.Code, w.Body.String())
		}
		var page sessionRepositoriesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 1 {
			t.Fatal("page bound exceeded")
		}
		for _, item := range page.Items {
			if _, exists := seen[item.ID]; exists {
				t.Fatal("duplicate identity")
			}
			if item.Status == nil || item.Availability != "available" {
				t.Fatalf("missing status: %+v", item)
			}
			seen[item.ID] = item
			if item.WorkspacePath == repoB && (item.Status.UntrackedCount != 1 || item.Default) {
				t.Fatalf("wrong selected status: %+v", item)
			}
		}
		cursor = page.NextCursor
		if n == 0 {
			for _, suffix := range []string{"limit=2&cursor=" + url.QueryEscape(cursor), "limit=1&cursor=" + base64.RawURLEncoding.EncodeToString([]byte(`{"phase":"sessions","offset":1}`)), "limit=1&cursor=" + strings.Repeat("a", 24001)} {
				bad := httptest.NewRecorder()
				server.handleSessionV3Repositories(bad, httptest.NewRequest(http.MethodGet, "/?"+suffix, nil), principal, owner.ID)
				if bad.Code == http.StatusOK {
					t.Fatal("forged/limit-changed continuation accepted")
				}
			}
		}
		if cursor == "" {
			break
		}
	}
	if len(seen) != 2 || cursor != "" {
		t.Fatalf("incomplete inventory: %d", len(seen))
	}
	for _, mutate := range []func(*identity.Principal){func(p *identity.Principal) { p.UserID = "foreign" }, func(p *identity.Principal) { p.AccountScopeID = "foreign" }} {
		other := principal
		mutate(&other)
		w := httptest.NewRecorder()
		server.handleSessionV3Repositories(w, httptest.NewRequest(http.MethodGet, "/?limit=1", nil), other, owner.ID)
		if w.Code == http.StatusOK {
			t.Fatal("foreign principal accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	server.handleSessionV3Repositories(w, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx), principal, owner.ID)
	if w.Code != http.StatusRequestTimeout {
		t.Fatalf("cancellation ignored: %d", w.Code)
	}
	for _, path := range []string{repoB, filepath.Join(repoB, "missing")} {
		r := httptest.NewRequest(http.MethodGet, "/?session_id=parent&workspace_path="+url.QueryEscape(path), nil)
		got, err := server.resolveGitStatusWorkspacePath(r, principal)
		if path == repoB && (err != nil || got != repoB) {
			t.Fatalf("explicit path switched: %q %v", got, err)
		}
		if path != repoB && err == nil {
			t.Fatal("unknown selector accepted")
		}
	}
	got, err := server.resolveGitCommitWorkspacePath(workspaceGitCommitRequest{WorkspacePath: repoB}, principal, owner.ID)
	if err != nil || got != repoB {
		t.Fatalf("commit switched selection: %q %v", got, err)
	}
	stale := sessionRepositoryItem{WorkspaceID: entries[1].WorkspaceID, WorkspaceGeneration: entries[1].WorkspaceGeneration + 1, SourcePath: repoB, WorkspacePath: repoB}
	if err := server.authorizeRepositoryItem(principal, stale); err == nil {
		t.Fatal("stale generation accepted")
	}
	// Detached history remains inspectable, but cannot authorize a commit.
	owner.WorkspaceGrants = owner.WorkspaceGrants[:1]
	_, err = sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: owner.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: pebblestore.V3SessionMutationUpdateMetadata, Session: &owner, IdempotencyKey: "detach", RequestHash: "detach", NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	if item, err := server.selectedSessionRepository(principal, owner.ID, repoB); err != nil || item.WorkspacePath != repoB {
		t.Fatalf("retained exact lookup: %+v %v", item, err)
	}
	if path, err := server.resolveGitCommitWorkspacePath(workspaceGitCommitRequest{WorkspacePath: repoB}, principal, owner.ID); err == nil || path != "" {
		t.Fatal("retained row authorized mutation")
	}
	if after := runGitCommitTestCommand(t, repoB, "rev-parse", "HEAD"); after != headBefore {
		t.Fatal("read changed HEAD")
	}
	if after := runGitCommitTestCommand(t, repoB, "diff", "--cached"); after != indexBefore {
		t.Fatal("read staged files")
	}
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
	if items[0].ID != again[0].ID || items[2].ID != again[2].ID {
		t.Fatal("context/default changed logical identity")
	}
	if len(items) != 3 || items[0].ID == items[1].ID {
		t.Fatalf("collapsed attachments: %+v", items)
	}
	worker := items[2]
	if worker.Kind != "worker" || worker.BaseCommit != "base" || worker.SourcePath != source || worker.WorkspacePath != lane || worker.Lifecycle != "dirty-recoverable" {
		t.Fatalf("lost worker provenance: %+v", worker)
	}
}

// Purpose: real managed allocations must remain visible after default changes,
// including pre-index historical lanes and archived/deleted children. HTTP
// inventory and exact selectors must retain provenance without granting writes
// or touching HEAD/index/source bytes. This API/temp-store/real-Git layer is the
// narrowest proof of catalog + retained index + lane validation + dirty status.
func TestSessionRepositoriesManagedRetainedLanes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	repoA, repoB := initGitCommitTestRepo(t), initGitCommitTestRepo(t)
	server, principal, entries := newSessionRouterTestServer(t, &sessionRouterRecordingRunner{id: "recording"}, []sessionRouterWorkspace{{repoA, "A", "A"}, {repoB, "B", "B"}})
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions := pebblestore.NewSessionStore(store)
	server.sessions = sessionruntime.NewService(sessions, nil)
	wt := worktree.NewService(pebblestore.NewWorktreeStore(store), server.workspace, nil)
	server.worktrees = wt
	old, err := wt.AllocateDetachedWorkspaceRequestedForPrincipal(principal, repoA, "parent", "", "agent/old-parent")
	if err != nil {
		t.Fatal(err)
	}
	current, err := wt.AllocateDetachedWorkspaceRequestedForPrincipal(principal, repoB, "parent", "", "agent/current-parent")
	if err != nil {
		t.Fatal(err)
	}
	base, err := wt.ResolveTaskBase(repoA)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := wt.AllocateTaskWorkspace(repoA, base, "completed-child", nil)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := wt.AllocateTaskWorkspace(repoA, base, "failed-child", nil)
	if err != nil {
		t.Fatal(err)
	}
	makeOwner := func(id, parent, source string, lane worktree.Allocation) pebblestore.SessionSnapshot {
		return pebblestore.SessionSnapshot{ID: id, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspacePath: lane.WorkspacePath, WorktreeEnabled: true, WorktreeRootPath: lane.WorkspacePath, WorktreeBranch: lane.BranchName, WorktreeBaseBranch: lane.BaseBranch, Metadata: map[string]any{"parent_session_id": parent, "base_commit": lane.BaseCommit, "swarm_v3_source_workspace_path": source}}
	}
	owner := makeOwner("parent", "", repoB, current)
	for i, entry := range entries {
		kind := pebblestore.WorkspaceGrantAdditional
		if i == 1 {
			kind = pebblestore.WorkspaceGrantPrimary
		}
		owner.WorkspaceGrants = append(owner.WorkspaceGrants, pebblestore.WorkspaceGrant{Kind: kind, Path: entry.Path, WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration})
	}
	owner.Metadata["swarm_v3_source_workspace_id"] = entries[1].WorkspaceID
	history := map[string]any{"owner_session_id": owner.ID, "path": old.WorkspacePath, "source_workspace_path": repoA, "workspace_id": entries[0].WorkspaceID, "workspace_generation": entries[0].WorkspaceGeneration, "branch": old.BranchName, "base_branch": old.BaseBranch, "base_commit": old.BaseCommit}
	owner.Metadata["swarm_v3_worktree_history"] = []any{history}
	put := func(snapshot pebblestore.SessionSnapshot, kind string, key string) {
		t.Helper()
		_, err := sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: snapshot.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: kind, Session: &snapshot, IdempotencyKey: key, RequestHash: key, NowUnixMs: 100})
		if err != nil {
			t.Fatal(err)
		}
	}
	put(owner, pebblestore.V3SessionMutationCreateSession, "parent")
	put(makeOwner("completed-child", owner.ID, repoA, complete), pebblestore.V3SessionMutationCreateSession, "completed")
	put(makeOwner("failed-child", owner.ID, repoA, failed), pebblestore.V3SessionMutationCreateSession, "failed")
	if err := sessions.ArchiveSession("completed-child"); err != nil {
		t.Fatal(err)
	}
	if err := sessions.DeleteSession("failed-child"); err != nil {
		t.Fatal(err)
	}
	if err := sessions.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	collect := func() map[string]sessionRepositoryItem {
		t.Helper()
		seen := map[string]sessionRepositoryItem{}
		cursor := ""
		for n := 0; n < 32; n++ {
			r := httptest.NewRequest(http.MethodGet, "/v3/sessions/parent/repositories?limit=1&cursor="+url.QueryEscape(cursor), nil)
			r = r.WithContext(identity.ContextWithPrincipal(r.Context(), principal))
			w := httptest.NewRecorder()
			server.handleSessionV3PrimaryByID(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("inventory %d: %s", w.Code, w.Body.String())
			}
			var page sessionRepositoriesResponse
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) > 1 {
				t.Fatal("page bound exceeded")
			}
			for _, item := range page.Items {
				if _, ok := seen[item.ID]; ok {
					t.Fatalf("duplicate row %s", item.WorkspacePath)
				}
				seen[item.ID] = item
			}
			cursor = page.NextCursor
			if cursor == "" {
				return seen
			}
		}
		t.Fatal("pagination did not terminate")
		return nil
	}
	initial := collect()
	assertLanes := func(items map[string]sessionRepositoryItem, dirty bool) {
		t.Helper()
		for path, allocation := range map[string]worktree.Allocation{old.WorkspacePath: old, current.WorkspacePath: current, complete.WorkspacePath: complete, failed.WorkspacePath: failed} {
			count := 0
			for _, item := range items {
				if item.WorkspacePath != path {
					continue
				}
				count++
				if item.Branch != allocation.BranchName || item.BaseCommit != allocation.BaseCommit || item.Status == nil || item.Availability != "available" {
					t.Errorf("lane provenance/status: %+v", item)
				}
				if path != current.WorkspacePath && item.SourcePath != repoA {
					t.Error("historical source changed")
				}
				if path == complete.WorkspacePath && item.Lifecycle != "archived" {
					t.Error("archive lost")
				}
				if path == failed.WorkspacePath && item.Lifecycle != "deleted" {
					t.Error("delete lost")
				}
				if dirty && path == old.WorkspacePath && (item.Status == nil || item.Status.UntrackedCount != 1) {
					t.Error("dirty retained lane status missing")
				}
			}
			if count != 1 {
				t.Errorf("lane %s appears %d times", path, count)
			}
		}
	}
	assertLanes(initial, false)
	for path, lifecycle := range map[string]string{complete.WorkspacePath: "archived", failed.WorkspacePath: "deleted"} {
		item, err := server.selectedSessionRepository(principal, owner.ID, path)
		if err != nil || item.Lifecycle != lifecycle || item.currentAuthority {
			t.Errorf("retained child selector: %+v %v", item, err)
		}
	}
	// Removing/readding an attachment changes presentation, not logical IDs.
	grants := append([]pebblestore.WorkspaceGrant(nil), owner.WorkspaceGrants...)
	owner.WorkspaceGrants = grants[1:]
	put(owner, pebblestore.V3SessionMutationUpdateMetadata, "detach")
	owner.WorkspaceGrants = grants
	put(owner, pebblestore.V3SessionMutationUpdateMetadata, "reattach")
	for id, item := range collect() {
		if _, ok := initial[id]; !ok {
			t.Errorf("reattach changed identity: %+v", item)
		}
		if item.SessionID == owner.ID && item.Kind == "source" && (!item.Attached || item.Default != (item.WorkspacePath == repoB)) {
			t.Errorf("attachment/default drift: %+v", item)
		}
	}
	if err := os.WriteFile(filepath.Join(old.WorkspacePath, "dirty.txt"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, path := range []string{repoA, repoB, old.WorkspacePath, complete.WorkspacePath, failed.WorkspacePath} {
		before[path] = runGitCommitTestCommand(t, path, "rev-parse", "HEAD") + runGitCommitTestCommand(t, path, "diff", "--cached")
	}
	assertLanes(collect(), true)
	r := httptest.NewRequest(http.MethodGet, "/?session_id=parent&workspace_path="+url.QueryEscape(old.WorkspacePath), nil)
	if got, err := server.resolveGitStatusWorkspacePath(r, principal); err != nil || got != old.WorkspacePath {
		t.Errorf("old selector changed: %q %v", got, err)
	}
	if got, err := server.resolveGitCommitWorkspacePath(workspaceGitCommitRequest{WorkspacePath: old.WorkspacePath}, principal, owner.ID); err == nil || got != "" {
		t.Error("retained lane authorized mutation")
	}
	for _, change := range []func(*identity.Principal){func(p *identity.Principal) { p.UserID = "foreign" }, func(p *identity.Principal) { p.AccountScopeID = "foreign" }} {
		foreign := principal
		change(&foreign)
		w := httptest.NewRecorder()
		server.handleSessionV3Repositories(w, httptest.NewRequest(http.MethodGet, "/", nil), foreign, owner.ID)
		if w.Code == http.StatusOK {
			t.Error("foreign principal inventory accepted")
		}
		if _, err := server.selectedSessionRepository(foreign, owner.ID, old.WorkspacePath); err == nil {
			t.Error("foreign selector accepted")
		}
	}
	for _, mismatch := range []string{"branch", "source_workspace_path"} {
		previous := history[mismatch]
		if mismatch == "branch" {
			history[mismatch] = "agent/wrong"
		} else {
			history[mismatch] = repoB
		}
		put(owner, pebblestore.V3SessionMutationUpdateMetadata, "mismatch-"+mismatch)
		if _, err := server.selectedSessionRepository(principal, owner.ID, old.WorkspacePath); err == nil {
			t.Error("forged lane provenance accepted")
		}
		history[mismatch] = previous
	}
	for path, state := range before {
		if got := runGitCommitTestCommand(t, path, "rev-parse", "HEAD") + runGitCommitTestCommand(t, path, "diff", "--cached"); got != state {
			t.Errorf("read/rejection changed %s", path)
		}
	}
	if data, err := os.ReadFile(filepath.Join(old.WorkspacePath, "dirty.txt")); err != nil || string(data) != "retained" {
		t.Error("dirty bytes changed")
	}
}
