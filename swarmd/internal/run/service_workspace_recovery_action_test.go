package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
	"testing"
)

// Purpose: parseManageWorkspaceArguments is the narrow tool-input boundary.
// Missing or mistyped recovery evidence and unsupported imports must fail before
// any reservation or Git access, while exact copy selections survive unchanged.
func TestRecoveryActionArguments(t *testing.T) {
	valid := map[string]any{"action": "copy_worktree", "workspace_id": "workspace", "workspace_generation": 1, "worktree_path": "/fixture/lane", "owner_session_id": "owner", "ownership_revision": 1, "head": "head", "fingerprint": strings.Repeat("a", 64), "operation_id": "copy-one", "files": []string{"file.txt"}}
	encode := func(m map[string]any) string {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	args, err := parseManageWorkspaceArguments(encode(valid))
	if err != nil || len(args.Recovery.Files) != 1 || args.Recovery.Files[0] != "file.txt" || args.Recovery.Revision != 1 {
		t.Fatalf("exact copy: %+v %v", args, err)
	}
	for _, key := range []string{"owner_session_id", "ownership_revision", "head", "fingerprint", "operation_id", "files"} {
		t.Run(key, func(t *testing.T) {
			m := cloneGenericMap(valid)
			delete(m, key)
			if _, err := parseManageWorkspaceArguments(encode(m)); err == nil {
				t.Fatal("accepted missing evidence")
			}
		})
	}
	for _, patch := range []map[string]any{{"commits": []string{"head"}}, {"patch": "diff"}, {"ownership_revision": 1.5}, {"files": []any{1}}, {"action": "discover_worktrees"}, {"action": "reclaim_worktree"}} {
		m := cloneGenericMap(valid)
		for k, v := range patch {
			m[k] = v
		}
		if _, err := parseManageWorkspaceArguments(encode(m)); err == nil {
			t.Fatalf("accepted invalid selection: %v", patch)
		}
	}
}

// Purpose: exercise the actual copy action, real Git allocator and canonical V3
// publisher together. Separate index/worktree layers and untracked binary modes
// must survive; failed publication retains evidence without switching the session
// or modifying the source, and repeating an operation cannot allocate twice.
func TestRecoveryCopyActionIntegration(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "store-failure"}[fail], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			principal := testRunPrincipal()
			repo := programFixtureRepo(t)
			lane := filepath.Join(t.TempDir(), "source-lane")
			runTestGit(t, repo, "worktree", "add", "-b", "agent/source", lane)
			put := func(name string, data []byte, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(lane, name), data, mode); err != nil {
					t.Fatal(err)
				}
			}
			put("layers", []byte("A"), 0644)
			runTestGit(t, lane, "add", "layers")
			put("layers", []byte("B"), 0644)
			put("binary", []byte{0, 1, 255}, 0755)
			workspaceSvc, _, raw, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
			defer cleanup()
			entry, err := workspaceSvc.AddForPrincipal(principal, repo, "repo", "", true)
			if err != nil {
				t.Fatal(err)
			}
			store := pebblestore.NewSessionStore(raw)
			if err := store.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
				t.Fatal(err)
			}
			owner := pebblestore.SessionSnapshot{ID: "copy-owner", WorkspacePath: repo, WorktreeEnabled: true, WorktreeRootPath: lane, WorktreeBranch: "agent/source", Metadata: map[string]any{"swarm_v3_source_workspace_id": entry.WorkspaceID, "swarm_v3_source_workspace_generation": entry.WorkspaceGeneration}}
			_, err = store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: owner.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: pebblestore.V3SessionMutationCreateSession, IdempotencyKey: "create", PayloadHash: "create", Session: &owner, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: lane, SourcePath: repo, OwnerSessionID: owner.ID, Branch: owner.WorktreeBranch}})
			if err != nil {
				t.Fatal(err)
			}
			sessions := sessionruntime.NewService(store, nil)
			svc := NewService(sessions, nil, nil, nil, nil, nil, nil, nil)
			svc.SetWorkspaceService(workspaceSvc)
			svc.SetWorktreeService(&worktreeruntime.Service{})
			svc.SetSessionWorkspaceCanonicalizer(func(SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
				return SessionWorkspaceCanonicalization{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, SourceWorkspacePath: repo, RuntimeWorkspacePath: repo, WorkspaceBindingID: "binding", RuntimeSwarmID: "local", PlacementGeneration: 1, BindingGeneration: 1, WorkspaceName: "repo", WorkspaceState: "active"}, nil
			})
			before, err := worktreeruntime.InspectRecoveryWorktree(repo, lane)
			if err != nil {
				t.Fatal(err)
			}
			index, err := os.ReadFile(filepath.Join(before.GitDir, "index"))
			if err != nil {
				t.Fatal(err)
			}
			args := manageWorkspaceArguments{Action: "copy_worktree", WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, WorktreePath: lane, WorktreeName: "copy-proof", Recovery: recoveryArguments{Owner: owner.ID, Revision: 1, HEAD: before.HEAD, Fingerprint: before.Fingerprint, Operation: "copy-proof", Files: []string{"layers", "binary"}}}
			destination := ""
			apply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
				if input.WorktreeRecovery.Action == "publish_copy" {
					destination = input.Session.WorktreeRootPath
					if fail {
						return sessionruntime.SessionMutationResult{}, errors.New("injected store failure")
					}
				}
				return sessions.ApplySessionMutation(input)
			}
			_, err = svc.recoverSessionWorktree(owner.ID, principal, args, apply)
			if (err != nil) != fail {
				t.Fatalf("copy result: %v", err)
			}
			if destination == "" || destination == lane {
				t.Fatalf("missing isolated copy: %q", destination)
			}
			if got := runTestGit(t, destination, "show", ":layers"); got != "A" {
				t.Fatalf("index flattened: %q", got)
			}
			data, err := os.ReadFile(filepath.Join(destination, "layers"))
			if err != nil || string(data) != "B" {
				t.Fatalf("worktree: %q %v", data, err)
			}
			data, err = os.ReadFile(filepath.Join(destination, "binary"))
			if err != nil || !bytes.Equal(data, []byte{0, 1, 255}) {
				t.Fatalf("binary: %v %v", data, err)
			}
			info, err := os.Stat(filepath.Join(destination, "binary"))
			if err != nil || info.Mode().Perm() != 0755 {
				t.Fatalf("mode: %v %v", info, err)
			}
			after, err := worktreeruntime.InspectRecoveryWorktree(repo, lane)
			if err != nil || after != before {
				t.Fatalf("source changed: %+v %v", after, err)
			}
			data, err = os.ReadFile(filepath.Join(before.GitDir, "index"))
			if err != nil || !bytes.Equal(data, index) {
				t.Fatal("source index changed")
			}
			current, _, err := sessions.GetSession(owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := destination
			if fail {
				want = lane
			}
			if current.WorktreeRootPath != want || current.WorkspacePath != repo {
				t.Fatalf("partial or wrong switch: %+v", current)
			}
			inventory := runTestGit(t, repo, "worktree", "list", "--porcelain")
			if _, err := svc.recoverSessionWorktree(owner.ID, principal, args, apply); err == nil {
				t.Fatal("replayed stale operation accepted")
			}
			if got := runTestGit(t, repo, "worktree", "list", "--porcelain"); got != inventory {
				t.Fatal("retry duplicated allocation")
			}
			claims, err := store.InspectWorktreeOwnership(principal.AccountScopeID, principal.UserID, []string{lane})
			if err != nil {
				t.Fatal(err)
			}
			state := "copied"
			if fail {
				state = "reserved_copy"
			}
			if claims[0].OperationState != state {
				t.Fatalf("lost recovery state: %+v", claims)
			}
		})
	}
}
