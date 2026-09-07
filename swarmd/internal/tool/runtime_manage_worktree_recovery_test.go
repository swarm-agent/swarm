package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func recoveryGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func recoveryRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	recoveryGit(t, dir, "init", "-b", "dev")
	recoveryGit(t, dir, "config", "user.name", "Test")
	recoveryGit(t, dir, "config", "user.email", "test@example.invalid")
	recoveryGit(t, dir, "commit", "--allow-empty", "-m", filepath.Base(dir))
	return dir
}

type recoveryFixture struct {
	runtime                                                *Runtime
	sessions                                               *sessionruntime.Service
	workspace                                              *workspaceruntime.Service
	primarySource                                          string
	scope                                                  WorkspaceScope
	source, primary, lane, goodPath, dirtyPath, base, head string
	rows                                                   []any
}

func newRecoveryFixture(t *testing.T, primaryLane ...bool) recoveryFixture {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	wt := &worktreeruntime.Service{}
	primarySource, source := recoveryRepo(t), recoveryRepo(t)
	primaryBase, err := wt.ResolveTaskBase(primarySource)
	if err != nil {
		t.Fatal(err)
	}
	primary, err := wt.AllocateTaskWorkspace(primarySource, primaryBase, "recovery-parent", nil)
	if len(primaryLane) > 1 && primaryLane[1] {
		primary, err = wt.AllocateDetachedWorkspaceRequestedForPrincipal(identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account"}, primarySource, "recovery-parent", "", "agent/named-recovery")
	}
	if err != nil {
		t.Fatal(err)
	}
	usePrimary := len(primaryLane) > 0 && primaryLane[0]
	if usePrimary {
		source = primarySource
	}
	base, err := wt.ResolveTaskBase(source)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("recovery-parent\x00" + source))
	lane, err := wt.AllocateTaskWorkspace(source, base, "program-lane-"+hex.EncodeToString(digest[:12]), nil)
	if err != nil {
		t.Fatal(err)
	}
	if usePrimary {
		lane = primary
	}
	create := func(id string, allocation worktreeruntime.Allocation, metadata map[string]any) {
		t.Helper()
		_, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: id, UserID: "user", AccountScopeID: "account", WorkspacePath: allocation.WorkspacePath, Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "high"}, Worktree: &sessionruntime.CreateSessionWorktree{RootPath: allocation.WorkspacePath, BranchName: allocation.BranchName, BaseBranch: "dev"}, Metadata: metadata})
		if err != nil {
			t.Fatal(err)
		}
	}
	create("recovery-parent", primary, map[string]any{"swarm_v3_source_workspace_path": primarySource})
	f := recoveryFixture{sessions: sessions, source: source, primarySource: primarySource, primary: primary.WorkspacePath, lane: lane.WorkspacePath, base: base.BaseCommit}
	laneBase, err := wt.ResolveTaskBase(lane.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"recovery-good", "recovery-dirty"} {
		child, err := wt.AllocateTaskWorkspace(lane.WorkspacePath, laneBase, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(child.WorkspacePath, "change.txt"), []byte(id), 0600); err != nil {
			t.Fatal(err)
		}
		head := base.BaseCommit
		if i == 0 {
			recoveryGit(t, child.WorkspacePath, "add", "change.txt")
			recoveryGit(t, child.WorkspacePath, "commit", "-m", "child")
			head = recoveryGit(t, child.WorkspacePath, "rev-parse", "HEAD")
			f.goodPath = child.WorkspacePath
			f.head = head
		} else {
			f.dirtyPath = child.WorkspacePath
		}
		create(id, child, map[string]any{"parent_session_id": "recovery-parent", "lineage_kind": "delegated_subagent", "subagent": "system-coder", "target_workspace_path": lane.WorkspacePath})
		row := map[string]any{"child_session_id": id, "subagent": "system-coder", "launch_index": i + 1, "parent_workspace_path": lane.WorkspacePath, "worktree_root_path": child.WorkspacePath, "worktree_branch": child.BranchName, "base_commit": base.BaseCommit, "head_commit": head}
		if i == 1 {
			row["error"] = "fixture failed with dirty work"
			row["head_commit"] = ""
		}
		f.rows = append(f.rows, row)
	}
	_, _, err = recoveryMetadata(sessions, "recovery-parent", map[string]any{"task_launches": map[string]any{"mixed-wave": map[string]any{"launches": f.rows}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = sessions.CreateTaskProgram(pebblestore.TaskProgramRecord{ParentSessionID: "recovery-parent", ProgramID: "recovery-program", DefinitionHash: "fixture", State: pebblestore.TaskProgramStateBlocked, Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "good", StageID: "build", AgentType: "coder", OwnedScope: []string{"change.txt"}}}}, Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "good", StageID: "build", State: pebblestore.TaskProgramJobHandoffReady, ChildSessionID: "recovery-good"}}, RepositoryLane: &pebblestore.TaskProgramRepositoryLane{SourcePath: source, WorkspacePath: lane.WorkspacePath, Branch: lane.BranchName, BaseCommit: base.BaseCommit}})
	if err != nil {
		t.Fatal(err)
	}
	f.workspace = workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	f.runtime = &Runtime{sessions: sessions, worktrees: wt, workspace: f.workspace}
	f.scope = WorkspaceScope{PrimaryPath: primary.WorkspacePath, Roots: []string{primary.WorkspacePath, primarySource}, SessionID: "recovery-parent", Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account", SessionID: "recovery-parent"}}
	for _, path := range []string{primarySource, source} {
		if _, err := f.workspace.AddForPrincipal(f.scope.Principal, path, "Recovery fixture", "", false); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// Purpose: manual recovery must use the authenticated parent's persisted program
// destination, not broaden active roots or classify commits in the primary repo.
// Runtime integrate/recall, session TaskProgramRepositoryLanes and real managed Git
// are the narrowest combined boundary proving a good sibling survives a mixed wave
// while captured checkouts and the dirty sibling remain unchanged.
func TestManageWorktreeProgramRecovery(t *testing.T) {
	for _, action := range []string{"recall", "integrate"} {
		t.Run(action, func(t *testing.T) {
			f := newRecoveryFixture(t)
			primaryHead := recoveryGit(t, f.primary, "rev-parse", "HEAD")
			dirtyBefore := recoveryGit(t, f.dirtyPath, "status", "--porcelain")
			if action == "integrate" {
				if _, err := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"task_call_id": "mixed-wave"}); err == nil {
					t.Fatal("failed whole wave accepted")
				}
				if recoveryGit(t, f.lane, "rev-parse", "HEAD") != f.base || recoveryGit(t, f.lane, "status", "--porcelain") != "" || recoveryGit(t, f.dirtyPath, "status", "--porcelain") != dirtyBefore {
					t.Fatal("rejected whole wave partially mutated lane or dirty child")
				}
				if _, err := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{"recovery-good"}}); err != nil {
					t.Fatal(err)
				}
				if got, err := os.ReadFile(filepath.Join(f.lane, "change.txt")); err != nil || string(got) != "recovery-good" {
					t.Fatalf("missing integrated bytes: %q %v", got, err)
				}
			}
			out, err := f.runtime.manageWorktreeRecall(f.scope, map[string]any{"task_call_id": "mixed-wave"})
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Children []struct {
					ID    string `json:"child_session_id"`
					State string `json:"child_state"`
				} `json:"children"`
			}
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
				t.Fatal(err)
			}
			want := "committed"
			if action == "integrate" {
				want = "integrated"
			}
			states := map[string]string{}
			for _, child := range payload.Children {
				states[child.ID] = child.State
			}
			if states["recovery-good"] != want || states["recovery-dirty"] != "dirty-recoverable" {
				t.Fatalf("recall states: %v; %s", states, out)
			}
			if recoveryGit(t, f.source, "rev-parse", "HEAD") != f.base || recoveryGit(t, f.primary, "rev-parse", "HEAD") != primaryHead || recoveryGit(t, f.dirtyPath, "status", "--porcelain") != dirtyBefore || recoveryGit(t, f.goodPath, "rev-parse", "HEAD") != f.head {
				t.Fatal("recovery mutated captured/primary/child state")
			}
		})
	}
}

// Purpose: recovery must reject foreign principals, parent/child lineage forgery,
// stale Git identities, unauthorized sources and dirty destinations before any
// integration. Real Git plus the session store proves negative postconditions at
// manageWorktreeRecoveryDestination and VerifyTaskIntegrationWorkspace rather
// than relying on an error-only mock or changing the active filesystem scope.
func TestManageWorktreeProgramRecoveryRejectsUnsafeContext(t *testing.T) {
	cases := []string{"cross-account", "cross-user", "foreign-parent", "principal-session", "forged-row-path", "forged-destination", "source-revoked", "stale-branch", "stale-head", "dirty-lane", "child-symlink", "lane-branch", "lane-symlink", "forged-program-lane"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			row := f.rows[0].(map[string]any)
			updateChild := func(metadata map[string]any) {
				t.Helper()
				if _, _, err := recoveryMetadata(f.sessions, "recovery-good", metadata); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "cross-account":
				f.scope.Principal.AccountScopeID = "foreign"
			case "cross-user":
				f.scope.Principal.UserID = "foreign"
			case "principal-session":
				f.scope.Principal.SessionID = "foreign"
			case "foreign-parent":
				updateChild(map[string]any{"parent_session_id": "foreign"})
			case "forged-row-path":
				row["worktree_root_path"] = f.dirtyPath
			case "forged-destination":
				row["parent_workspace_path"] = f.source
				updateChild(map[string]any{"target_workspace_path": f.source})
			case "source-revoked":
				if _, err := f.workspace.DeleteForPrincipal(f.scope.Principal, f.source); err != nil {
					t.Fatal(err)
				}
			case "stale-branch":
				recoveryGit(t, f.goodPath, "branch", "-m", "agent/stale")
			case "stale-head":
				recoveryGit(t, f.goodPath, "commit", "--allow-empty", "-m", "stale")
			case "dirty-lane":
				if err := os.WriteFile(filepath.Join(f.lane, "dirty.txt"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "lane-branch":
				recoveryGit(t, f.lane, "branch", "-m", "agent/stale-lane")
			case "lane-symlink":
				moved := filepath.Join(t.TempDir(), "moved-lane")
				if err := os.Rename(f.lane, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, f.lane); err != nil {
					t.Fatal(err)
				}
			case "forged-program-lane":
				row["parent_workspace_path"] = f.goodPath
				updateChild(map[string]any{"target_workspace_path": f.goodPath})
				_, _, err := f.sessions.CreateTaskProgram(pebblestore.TaskProgramRecord{ParentSessionID: "recovery-parent", ProgramID: "forged-lane", DefinitionHash: "fixture-forged", Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "job", StageID: "build", State: pebblestore.TaskProgramJobDeclared}}, Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "job", StageID: "build", AgentType: "coder", OwnedScope: []string{"change.txt"}}}}, State: pebblestore.TaskProgramStateBlocked, RepositoryLane: &pebblestore.TaskProgramRepositoryLane{SourcePath: f.source, WorkspacePath: f.goodPath, Branch: asString(row["worktree_branch"]), BaseCommit: f.base}})
				if err != nil {
					t.Fatal(err)
				}
			case "child-symlink":
				link := filepath.Join(t.TempDir(), "child-link")
				if err := os.Symlink(f.goodPath, link); err != nil {
					t.Fatal(err)
				}
				row["worktree_root_path"] = link
			}
			if _, _, err := recoveryMetadata(f.sessions, "recovery-parent", map[string]any{"task_launches": map[string]any{"mixed-wave": map[string]any{"launches": f.rows}}}); err != nil {
				t.Fatal(err)
			}
			paths := []string{f.primarySource, f.primary, f.source, f.lane, f.goodPath, f.dirtyPath}
			before := map[string]string{}
			for _, path := range paths {
				before[path] = recoveryGit(t, path, "rev-parse", "HEAD") + recoveryGit(t, path, "status", "--porcelain")
			}
			if _, err := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{"recovery-good"}}); err == nil {
				t.Fatal("unsafe integration accepted")
			}
			out, err := f.runtime.manageWorktreeRecall(f.scope, map[string]any{"task_call_id": "mixed-wave"})
			if err == nil {
				var payload struct {
					Children []struct {
						ID    string `json:"child_session_id"`
						State string `json:"child_state"`
					} `json:"children"`
				}
				if err := json.Unmarshal([]byte(out), &payload); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, child := range payload.Children {
					if child.ID == "recovery-good" {
						found = true
						if child.State != "blocked" && child.State != "stale" {
							t.Fatalf("unsafe recall classified ready: %s", out)
						}
					}
				}
				if !found {
					t.Fatalf("recall omitted rejected child: %s", out)
				}
			}
			for _, path := range paths {
				if got := recoveryGit(t, path, "rev-parse", "HEAD") + recoveryGit(t, path, "status", "--porcelain"); got != before[path] {
					t.Fatal("rejected recovery mutated a repository")
				}
			}
		})
	}
}

// Purpose: the same recovery resolver must preserve normal primary-parent lane
// integration while refusing stale runtime identity. Real Git proves that only
// the managed parent changes, and a repeat integration does not replay commits.
func TestManageWorktreePrimaryRecovery(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "stale-runtime"}[stale], func(t *testing.T) {
			f := newRecoveryFixture(t, true)
			if stale {
				if _, _, err := recoveryMetadata(f.sessions, "recovery-parent", map[string]any{"swarm_v3_runtime_workspace_path": f.source}); err != nil {
					t.Fatal(err)
				}
			}
			before := recoveryGit(t, f.lane, "rev-parse", "HEAD")
			_, err := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{"recovery-good"}})
			if stale {
				if err == nil || recoveryGit(t, f.lane, "rev-parse", "HEAD") != before {
					t.Fatal("stale parent context mutated lane")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				after := recoveryGit(t, f.lane, "rev-parse", "HEAD")
				if after == before {
					t.Fatal("managed parent did not advance")
				}
				if _, err := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{"recovery-good"}}); err != nil {
					t.Fatal(err)
				}
				if recoveryGit(t, f.lane, "rev-parse", "HEAD") != after {
					t.Fatal("repeat integration replayed child")
				}
			}
			if recoveryGit(t, f.source, "rev-parse", "HEAD") != f.base {
				t.Fatal("captured checkout advanced")
			}
		})
	}
}

func recoveryMetadata(sessions *sessionruntime.Service, id string, patch map[string]any) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	snapshot, _, err := sessions.GetSession(id)
	if err != nil {
		return snapshot, nil, err
	}
	for key, value := range patch {
		snapshot.Metadata[key] = value
	}
	return sessions.UpdateMetadata(id, snapshot.Metadata)
}

// Purpose: manageWorktreeRecoveryDestination must authenticate the exact recorded
// source B through workspace.ScopeForPathForPrincipal's current account catalog,
// even when the parent scope contains only A (and its private runtime lane).
// Legacy Directories, broader saved ancestors, revoked roots and identity drift
// must not authorize B. Real workspace/session Pebble stores plus managed Git are
// the narrowest boundary reproducing the flat-catalog mismatch and proving no
// captured checkout, child bytes, durable lineage or caller scope is mutated.
func TestManageWorktreeRecoverySavedSourceAuthority(t *testing.T) {
	for _, mode := range []string{"separate", "captured", "captured-revoked", "revoked", "revoked-active-root", "foreign-catalog", "ancestor-only", "stale-source", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t, strings.HasPrefix(mode, "captured"))
			if !strings.HasPrefix(mode, "captured") {
				if _, err := resolveWorkspacePath(f.scope, f.source); err == nil {
					t.Fatal("fixture already authorizes B through active scope")
				}
			}
			a, err := f.workspace.ScopeForPathForPrincipal(f.scope.Principal, f.primarySource)
			if err != nil || !a.Matched || len(a.Directories) != 1 || a.Directories[0] != f.primarySource {
				t.Fatalf("fixture must use real flat catalog A: %+v %v", a, err)
			}
			switch mode {
			case "captured-revoked", "revoked", "revoked-active-root", "foreign-catalog", "ancestor-only":
				if _, err := f.workspace.DeleteForPrincipal(f.scope.Principal, f.source); err != nil {
					t.Fatal(err)
				}
				if mode == "revoked-active-root" {
					f.scope.Roots = append(f.scope.Roots, f.source)
				}
				if mode == "foreign-catalog" {
					foreign := f.scope.Principal
					foreign.AccountScopeID, foreign.UserID = "foreign-account", "foreign-user"
					if _, err := f.workspace.AddForPrincipal(foreign, f.source, "Foreign source", "", false); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "ancestor-only" {
					ancestor := filepath.Dir(f.source)
					recoveryGit(t, ancestor, "init", "-b", "dev")
					recoveryGit(t, ancestor, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "ancestor")
					if _, err := f.workspace.AddForPrincipal(f.scope.Principal, ancestor, "Ancestor", "", false); err != nil {
						t.Fatal(err)
					}
				}
			case "stale-source":
				// Keep Git's recorded common-directory path usable while replacing
				// the saved root with a symlink; the catalog must reject identity drift.
				moved := filepath.Join(t.TempDir(), "moved-source")
				if err := os.Rename(f.source, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, f.source); err != nil {
					t.Fatal(err)
				}
			case "unavailable":
				f.runtime.workspace = nil
				f.scope.Roots = append(f.scope.Roots, f.source)
			}
			paths := []string{f.primarySource, f.primary, f.source, f.lane, f.goodPath, f.dirtyPath}
			before := map[string]string{}
			for _, path := range paths {
				before[path] = recoveryGit(t, path, "rev-parse", "HEAD") + recoveryGit(t, path, "status", "--porcelain")
			}
			parentBefore, found, err := f.sessions.GetSession(f.scope.SessionID)
			if err != nil || !found {
				t.Fatalf("read parent before recovery: %v", err)
			}
			lineageBefore, err := json.Marshal(parentBefore)
			if err != nil {
				t.Fatal(err)
			}
			rootsBefore := strings.Join(f.scope.Roots, "\x00")
			_, err = f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{"recovery-good"}})
			allowed := mode == "separate" || mode == "captured"
			if !allowed {
				if err == nil || !strings.Contains(err.Error(), "recovery source") {
					t.Fatalf("want source authorization rejection, got %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got, err := os.ReadFile(filepath.Join(f.lane, "change.txt")); err != nil || string(got) != "recovery-good" {
					t.Fatalf("missing integrated bytes: %q %v", got, err)
				}
				if recoveryGit(t, f.lane, "status", "--porcelain") != "" {
					t.Fatal("integration left dirty parent lane")
				}
			}
			for _, path := range paths {
				if allowed && path == f.lane {
					continue
				}
				if got := recoveryGit(t, path, "rev-parse", "HEAD") + recoveryGit(t, path, "status", "--porcelain"); got != before[path] {
					t.Fatal("recovery mutated protected repository state")
				}
			}
			parentAfter, found, err := f.sessions.GetSession(f.scope.SessionID)
			if err != nil || !found {
				t.Fatalf("read parent after recovery: %v", err)
			}
			lineageAfter, err := json.Marshal(parentAfter)
			if err != nil || string(lineageBefore) != string(lineageAfter) || rootsBefore != strings.Join(f.scope.Roots, "\x00") {
				t.Fatal("recovery changed durable lineage or active scope")
			}
			if got, err := os.ReadFile(filepath.Join(f.dirtyPath, "change.txt")); err != nil || string(got) != "recovery-dirty" {
				t.Fatal("recovery changed dirty sibling bytes")
			}
		})
	}
}

// Purpose: canonical manual integration must accept the named-primary allocator
// while preserving exact source, child and principal authority; unrelated dirty
// child work and the captured source must remain unchanged.
func TestManageWorktreeNamedPrimaryRecovery(t *testing.T) {
	f := newRecoveryFixture(t, true, true)
	before := recoveryGit(t, f.source, "rev-parse", "HEAD")
	dirtyBefore := recoveryGit(t, f.dirtyPath, "status", "--porcelain")
	if _, err := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{"recovery-good"}}); err != nil {
		t.Fatal(err)
	}
	if recoveryGit(t, f.source, "rev-parse", "HEAD") != before || recoveryGit(t, f.dirtyPath, "status", "--porcelain") != dirtyBefore {
		t.Fatal("named recovery changed unrelated source or dirty child")
	}
	if data, err := os.ReadFile(filepath.Join(f.lane, "change.txt")); err != nil || string(data) != "recovery-good" {
		t.Fatal("named lane missing committed change")
	}
}
