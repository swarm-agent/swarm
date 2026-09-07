package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktree "swarm/packages/swarmd/internal/worktree"
)

// Purpose: source identity, owned Git lane and persisted default must agree after
// setSessionWorkspaces/ApplySessionMutation and ResolveRuntimeWorkspaceScope.
// Real Git and temporary Pebble prove isolation, retained history and rejection
// postconditions that a display/snapshot assertion alone cannot establish.
func TestMultiWorkspaceIdentityTransitions(t *testing.T) {
	for _, shape := range []string{"nested-managed", "sibling-managed", "nested-unmanaged"} {
		t.Run(shape, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			git := func(path string, args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			makeRepo := func(path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				git(path, "init", "-b", "dev")
				git(path, "config", "user.name", "Fixture")
				git(path, "config", "user.email", "fixture@example.invalid")
				if err := os.WriteFile(filepath.Join(path, ".gitignore"), []byte("nested/\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "marker.txt"), []byte(filepath.Base(path)), 0600); err != nil {
					t.Fatal(err)
				}
				git(path, "add", ".gitignore", "marker.txt")
				git(path, "commit", "-m", "fixture")
			}
			parentPath := filepath.Join(root, "parent")
			targetPath := filepath.Join(parentPath, "nested")
			if shape == "sibling-managed" {
				targetPath = filepath.Join(root, "sibling")
			}
			makeRepo(parentPath)
			makeRepo(targetPath)
			common := func(path string) string { return git(path, "rev-parse", "--path-format=absolute", "--git-common-dir") }
			parentCommon, targetCommon := common(parentPath), common(targetPath)
			if parentCommon == targetCommon {
				t.Fatal("fixture repositories are not independent")
			}
			parentHead, targetHead := git(parentPath, "rev-parse", "HEAD"), git(targetPath, "rev-parse", "HEAD")
			principal := testRunPrincipal()
			workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
			defer cleanup()
			parent, err := workspaceSvc.AddForPrincipal(principal, parentPath, "parent", "", true)
			if err != nil {
				t.Fatal(err)
			}
			if shape != "sibling-managed" {
				configured := worktree.NewService(pebblestore.NewWorktreeStore(rawStore), workspaceSvc, nil)
				inventory := git(parentPath, "worktree", "list", "--porcelain")
				if _, err := configured.GetConfigForPrincipal(principal, targetPath); err == nil || !strings.Contains(err.Error(), "differs from saved workspace") {
					t.Fatalf("unsaved nested repo routed to ancestor: %v", err)
				}
				if git(parentPath, "worktree", "list", "--porcelain") != inventory {
					t.Fatal("rejected nested lookup allocated ancestor lane")
				}
			}
			target, err := workspaceSvc.AddForPrincipal(principal, targetPath, "target", "", false)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := workspaceSvc.ScopeForPathForPrincipal(principal, targetPath)
			if err != nil || resolved.WorkspaceID != target.WorkspaceID {
				t.Fatalf("nested saved resolution: %+v %v", resolved, err)
			}
			managed := shape != "nested-unmanaged"
			allocation := worktree.Allocation{}
			worktrees := &worktree.Service{}
			if managed {
				base, err := worktrees.ResolveTaskBase(parentPath)
				if err != nil {
					t.Fatal(err)
				}
				allocation, err = worktrees.AllocateTaskWorkspace(parentPath, base, "incident-fixture", nil)
				if err != nil {
					t.Fatal(err)
				}
				if common(allocation.WorkspacePath) != parentCommon || allocation.BaseCommit != parentHead {
					t.Fatal("initial allocator selected wrong Git authority")
				}
			}
			store := pebblestore.NewSessionStore(rawStore)
			sessions := sessionruntime.NewService(store, nil)
			id := "incident-fixture"
			available := true
			snapshot := pebblestore.SessionSnapshot{
				ID: id, WorkspacePath: parentPath, WorkspaceName: "parent",
				WorktreeEnabled: managed, WorktreeRootPath: allocation.WorkspacePath, WorktreeBranch: allocation.BranchName, WorktreeBaseBranch: allocation.BaseBranch,
				Metadata:        map[string]any{"swarm_v3_source_workspace_id": parent.WorkspaceID, "swarm_v3_source_workspace_generation": "1", "swarm_v3_source_workspace_path": parentPath, "swarm_v3_runtime_workspace_path": parentPath, "swarm_v3_worktree_owner_session_id": id, "base_commit": parentHead},
				WorkspaceGrants: []pebblestore.WorkspaceGrant{{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: parent.WorkspaceID, WorkspaceGeneration: parent.WorkspaceGeneration, Path: parentPath, Name: "parent", Available: &available}},
			}
			if managed {
				snapshot.Metadata["swarm_v3_runtime_workspace_path"] = allocation.WorkspacePath
			}
			if err := store.CreateSessionForAccount(snapshot, principal.UserID, principal.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			svc := NewService(sessions, nil, nil, nil, nil, nil, nil, nil)
			svc.SetWorkspaceService(workspaceSvc)
			svc.SetWorktreeService(worktrees)
			svc.SetSessionWorkspaceCanonicalizer(testManageWorkspaceCanonicalizer(parent, target))
			load := func() pebblestore.SessionSnapshot {
				t.Helper()
				s, ok, err := sessions.GetSession(id)
				if err != nil || !ok {
					t.Fatalf("session lookup: %t %v", ok, err)
				}
				return s
			}
			before := load()
			beforeScope, err := svc.ResolveRuntimeWorkspaceScope(before, principal)
			if err != nil || common(beforeScope.PrimaryPath) != parentCommon {
				t.Fatalf("initial scope: %+v %v", beforeScope, err)
			}
			args := mustJSON(t, map[string]any{"action": "set_default", "workspace_id": target.WorkspaceID})
			out, err := svc.executeManageWorkspaceTool(id, args, principal, sessions.ApplySessionMutation)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, load()) {
				t.Fatal("account default mutated existing session")
			}
			if providerManagedToolRequiresTurnRestart(tool.Call{Name: "manage_workspace"}, tool.Result{Output: out}) {
				t.Fatal("account default unexpectedly requests restart")
			}
			current, ok, err := workspaceSvc.CurrentBindingForPrincipal(principal)
			if err != nil || !ok || current.WorkspaceID != target.WorkspaceID {
				t.Fatal("account default not selected")
			}
			args = mustJSON(t, map[string]any{"action": "set_session", "workspace_id": target.WorkspaceID, "workspace_generation": target.WorkspaceGeneration})
			if managed {
				inventory := git(targetPath, "worktree", "list", "--porcelain")
				injected := errors.New("injected storage failure")
				_, failure := svc.executeManageWorkspaceTool(id, args, principal, func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
					return sessionruntime.SessionMutationResult{}, injected
				})
				if !errors.Is(failure, injected) || !reflect.DeepEqual(before, load()) || git(targetPath, "worktree", "list", "--porcelain") != inventory {
					t.Fatal("failed persistence did not roll back allocation")
				}
				_, failure = svc.executeManageWorkspaceTool(id, args, principal, func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
					if input.ExpectedLastEventSeq == nil {
						t.Fatal("mutation has no stale guard")
					}
					wrong := *input.ExpectedLastEventSeq + 1
					input.ExpectedLastEventSeq = &wrong
					return sessions.ApplySessionMutation(input)
				})
				if failure == nil || !reflect.DeepEqual(before, load()) || git(targetPath, "worktree", "list", "--porcelain") != inventory {
					t.Fatal("CAS rejection did not retain snapshot and inventory")
				}
			}
			// Requirement: competing default mutations from one immutable sequence
			// yield exactly one winner; the losing allocation must be removed.
			// Authority: setSessionWorkspaces -> ApplySessionMutation CAS. Both
			// real-Git allocations stop at the publisher barrier, not a sleep race.
			if managed {
				thirdPath := filepath.Join(root, "third")
				makeRepo(thirdPath)
				third, err := workspaceSvc.AddForPrincipal(principal, thirdPath, "third", "", false)
				if err != nil {
					t.Fatal(err)
				}
				svc.SetSessionWorkspaceCanonicalizer(testManageWorkspaceCanonicalizer(parent, target, third))
				type attempt struct {
					input   sessionruntime.SessionMutationInput
					release chan struct{}
				}
				abort := make(chan struct{})
				var workers sync.WaitGroup
				defer func() { close(abort); workers.Wait() }()
				ready := make(chan attempt, 2)
				done := make(chan error, 2)
				publish := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
					gate := attempt{input: input, release: make(chan struct{})}
					ready <- gate
					select {
					case <-gate.release:
						return sessions.ApplySessionMutation(input)
					case <-abort:
						return sessionruntime.SessionMutationResult{}, errors.New("publisher barrier aborted")
					case <-time.After(15 * time.Second):
						return sessionruntime.SessionMutationResult{}, errors.New("publisher barrier timeout")
					}
				}
				thirdArgs := mustJSON(t, map[string]any{"action": "set_session", "workspace_id": third.WorkspaceID})
				for _, requested := range []string{args, thirdArgs} {
					workers.Add(1)
					go func(requested string) {
						defer workers.Done()
						_, err := svc.executeManageWorkspaceTool(id, requested, principal, publish)
						done <- err
					}(requested)
				}
				gates := make([]attempt, 0, 2)
				for i := 0; i < 2; i++ {
					select {
					case gate := <-ready:
						gates = append(gates, gate)
					case err := <-done:
						t.Fatalf("mutation failed before publisher barrier: %v", err)
					case <-time.After(15 * time.Second):
						t.Fatal("mutation admission barrier timed out")
					}
				}
				if gates[0].input.Session.WorkspacePath != targetPath {
					gates[0], gates[1] = gates[1], gates[0]
				}
				if gates[0].input.ExpectedLastEventSeq == nil || gates[1].input.ExpectedLastEventSeq == nil || *gates[0].input.ExpectedLastEventSeq != *gates[1].input.ExpectedLastEventSeq {
					t.Fatal("competing mutations lack common immutable CAS base")
				}
				close(gates[0].release)
				if err := <-done; err != nil {
					t.Fatalf("first CAS writer: %v", err)
				}
				winner := load()
				close(gates[1].release)
				if err := <-done; err == nil {
					t.Fatal("second CAS writer accepted stale mutation")
				}
				if !reflect.DeepEqual(winner, load()) {
					t.Fatal("CAS loser partially changed winner")
				}
				loserPath := gates[1].input.Session.WorktreeRootPath
				if loserPath == winner.WorktreeRootPath {
					t.Fatal("competing allocations share a lane")
				}
				if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
					t.Fatalf("CAS loser leaked allocation: %v", err)
				}
				if strings.Contains(git(thirdPath, "worktree", "list", "--porcelain"), loserPath) {
					t.Fatal("CAS loser leaked Git inventory")
				}
				if len(winner.WorkspaceGrants) != len(gates[0].input.Session.WorkspaceGrants) {
					t.Fatal("CAS changed attachment set")
				}
				// Return through the canonical transition before the original cases.
				back := mustJSON(t, map[string]any{"action": "set_session", "workspace_id": parent.WorkspaceID})
				if _, err := svc.executeManageWorkspaceTool(id, back, principal, sessions.ApplySessionMutation); err != nil {
					t.Fatal(err)
				}
			}
			out, err = svc.executeManageWorkspaceTool(id, args, principal, sessions.ApplySessionMutation)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["restart_turn"] != true || !providerManagedToolRequiresTurnRestart(tool.Call{Name: "manage_workspace"}, tool.Result{Output: out}) {
				t.Fatal("restart request missing")
			}
			after := load()
			if after.WorkspacePath != targetPath || mapString(after.Metadata, "swarm_v3_source_workspace_id") != target.WorkspaceID {
				t.Fatal("durable selected identity did not move")
			}
			afterScope, err := svc.ResolveRuntimeWorkspaceScope(after, principal)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.providerManagedWorkspaceContext(providerToolInvokerConfig{sessionID: id, workspacePath: beforeScope.PrimaryPath}, principal); err == nil {
				t.Fatal("stale provider step silently retargeted after workspace mutation")
			}
			ctx, err := svc.providerManagedWorkspaceContext(providerToolInvokerConfig{sessionID: id, workspacePath: afterScope.PrimaryPath}, principal)
			if err != nil || ctx.WorkspacePath != afterScope.PrimaryPath {
				t.Fatalf("provider context rehydration: %+v %v", ctx, err)
			}
			worker, _, err := svc.resolveTaskTargetWorkspace(after, principal, taskLaunchSpec{RequestedSubagentType: "coder"})
			if err != nil || worker != afterScope.PrimaryPath {
				t.Fatalf("default worker: %q %v", worker, err)
			}
			explicit, _, err := svc.resolveTaskTargetWorkspace(after, principal, taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: targetPath})
			if err != nil || common(explicit) != targetCommon {
				t.Fatalf("explicit worker: %q %v", explicit, err)
			}
			if managed {
				// Requirement: returning a worker to the prior source uses its retained
				// owned lane, never the captured checkout. Target resolution is the
				// narrowest authority; source/status assertions below prove no mutation.
				retained, _, err := svc.resolveTaskTargetWorkspace(after, principal, taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: parentPath})
				if err != nil || retained != before.WorktreeRootPath {
					t.Fatalf("retained worker lane: %q %v", retained, err)
				}
				if after.WorktreeRootPath == before.WorktreeRootPath || mapString(after.Metadata, "swarm_v3_worktree_owner_session_id") != id {
					t.Fatal("target did not receive an independent owned lane")
				}
				if common(afterScope.PrimaryPath) != targetCommon || afterScope.PrimaryPath == targetPath {
					t.Fatal("runtime escaped target isolation")
				}
				if len(sessionWorktreeHistory(after.Metadata["swarm_v3_worktree_history"])) != 2 {
					t.Fatal("prior lane provenance not retained")
				}
				contradictory := after
				contradictory.Metadata = cloneGenericMap(after.Metadata)
				contradictory.WorktreeRootPath, contradictory.WorktreeBranch = before.WorktreeRootPath, before.WorktreeBranch
				contradictory.Metadata["swarm_v3_runtime_workspace_path"] = before.WorktreeRootPath
				if _, err := svc.ResolveRuntimeWorkspaceScope(contradictory, principal); err == nil {
					t.Fatal("contradictory legacy provenance executed")
				}
			} else {
				if common(afterScope.PrimaryPath) != targetCommon {
					t.Fatal("unmanaged move failed")
				}
				t.Log("CONTROL: unmanaged move resolves target Git correctly; account default alone leaves session unchanged")
			}
			if git(parentPath, "rev-parse", "HEAD") != parentHead || git(targetPath, "rev-parse", "HEAD") != targetHead || git(parentPath, "status", "--porcelain") != "" || git(targetPath, "status", "--porcelain") != "" {
				t.Fatal("captured source repositories mutated")
			}
			// Exact flat-set removal preserves the default and historical lanes, not access.
			args = mustJSON(t, map[string]any{"action": "set_session", "workspace_ids": []string{target.WorkspaceID}})
			if _, err := svc.executeManageWorkspaceTool(id, args, principal, sessions.ApplySessionMutation); err != nil {
				t.Fatal(err)
			}
			after = load()
			for _, grant := range after.WorkspaceGrants {
				if grant.WorkspaceID == parent.WorkspaceID {
					t.Fatal("removed attachment retained execution grant")
				}
			}
			scope, err := svc.ResolveRuntimeWorkspaceScope(after, principal)
			if err != nil || containsTrimmedString(scope.Roots, parentPath) {
				t.Fatalf("removed root authorized: %+v %v", scope, err)
			}
			if managed && len(sessionWorktreeHistory(after.Metadata["swarm_v3_worktree_history"])) != 2 {
				t.Fatal("removal erased history")
			}
			// Reload through a new service, not an in-memory workspace cache.
			reloaded, ok, err := sessionruntime.NewService(pebblestore.NewSessionStore(rawStore), nil).GetSession(id)
			if err != nil || !ok || !reflect.DeepEqual(after, reloaded) {
				t.Fatal("attachment persistence mismatch")
			}
			for _, bad := range []map[string]any{
				{"action": "set_session", "workspace_ids": []string{parent.WorkspaceID}},
				{"action": "set_session", "workspace_id": "unknown"},
				{"action": "set_session", "workspace_id": parent.WorkspaceID, "primary_workspace_id": target.WorkspaceID},
				{"action": "set_session", "workspace_ids": []any{nil}},
			} {
				if _, err := svc.executeManageWorkspaceTool(id, mustJSON(t, bad), principal, sessions.ApplySessionMutation); err == nil {
					t.Fatalf("invalid transition accepted: %v", bad)
				}
				if !reflect.DeepEqual(after, load()) {
					t.Fatal("invalid transition changed snapshot")
				}
			}
			foreign := principal
			foreign.AccountScopeID = "foreign-account"
			if _, err := svc.executeManageWorkspaceTool(id, args, foreign, sessions.ApplySessionMutation); err == nil || !reflect.DeepEqual(after, load()) {
				t.Fatal("foreign principal mutated attachments")
			}
			// Negative mutation: stale generation must leave the entire durable snapshot unchanged.
			args = mustJSON(t, map[string]any{"action": "set_session", "workspace_id": parent.WorkspaceID, "workspace_generation": parent.WorkspaceGeneration + 99})
			if _, err := svc.executeManageWorkspaceTool(id, args, principal, sessions.ApplySessionMutation); err == nil {
				t.Fatal("stale generation accepted")
			}
			if !reflect.DeepEqual(after, load()) {
				t.Fatal("rejected stale move partially mutated session")
			}
			if managed {
				// Dirty work remains untouched and cannot be abandoned by a default change.
				dirty := filepath.Join(after.WorktreeRootPath, "dirty.txt")
				if err := os.WriteFile(dirty, []byte("retain"), 0600); err != nil {
					t.Fatal(err)
				}
				args = mustJSON(t, map[string]any{"action": "set_session", "workspace_id": parent.WorkspaceID})
				if _, err := svc.executeManageWorkspaceTool(id, args, principal, sessions.ApplySessionMutation); err == nil || !reflect.DeepEqual(after, load()) {
					t.Fatal("dirty move mutated session")
				}
				if body, err := os.ReadFile(dirty); err != nil || string(body) != "retain" {
					t.Fatal("dirty work lost")
				}
				if err := os.Remove(dirty); err != nil {
					t.Fatal(err)
				}
				// Reattach and select the exact retained original lane; no duplicate allocation.
				if _, err := svc.executeManageWorkspaceTool(id, args, principal, sessions.ApplySessionMutation); err != nil {
					t.Fatal(err)
				}
				if load().WorktreeRootPath != before.WorktreeRootPath {
					t.Fatal("return did not reuse owned lane")
				}
			}
			if git(parentPath, "rev-parse", "HEAD") != parentHead || git(targetPath, "rev-parse", "HEAD") != targetHead {
				t.Fatal("source HEAD changed")
			}
			retained := load()
			// Requirement: an admitted worker's immutable scope remains pinned while
			// a parent transition is attempted. This is a held runtime-scope fixture,
			// not provider-backed execution or the child-allocation race window.
			childBase, err := worktrees.ResolveTaskBase(retained.WorkspacePath)
			if err != nil {
				t.Fatal(err)
			}
			childLane, err := worktrees.AllocateTaskWorkspace(retained.WorkspacePath, childBase, "held-child", nil)
			if err != nil {
				t.Fatal(err)
			}
			child := retained
			child.ID = "held-child"
			child.Metadata = cloneGenericMap(retained.Metadata)
			child.Metadata["parent_session_id"] = id
			child.Metadata["lineage_kind"] = "delegated_subagent"
			child.Metadata["swarm_v3_worktree_owner_session_id"] = child.ID
			child.Metadata["swarm_v3_runtime_workspace_path"] = childLane.WorkspacePath
			child.Metadata["swarm_v3_worktree_base_commit"] = childBase.BaseCommit
			child.Metadata["base_commit"] = childBase.BaseCommit
			child.WorktreeEnabled = true
			child.WorktreeRootPath, child.WorktreeBranch, child.WorktreeBaseBranch = childLane.WorkspacePath, childLane.BranchName, childLane.BaseBranch
			if err := store.CreateSessionForAccount(child, principal.UserID, principal.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			childBefore, _, err := sessions.GetSession(child.ID)
			if err != nil {
				t.Fatal(err)
			}
			workerReady, workerRelease, workerDone := make(chan error, 1), make(chan struct{}), make(chan error, 1)
			var releaseOnce sync.Once
			workerJoined := false
			defer func() {
				releaseOnce.Do(func() { close(workerRelease) })
				if !workerJoined {
					<-workerDone
				}
			}()
			go func() {
				first, err := svc.ResolveRuntimeWorkspaceScope(childBefore, principal)
				workerReady <- err
				<-workerRelease
				if err == nil {
					second, secondErr := svc.ResolveRuntimeWorkspaceScope(childBefore, principal)
					err = secondErr
					if err == nil && !reflect.DeepEqual(first, second) {
						err = errors.New("held worker scope changed")
					}
				}
				workerDone <- err
			}()
			if err := <-workerReady; err != nil {
				t.Fatal(err)
			}
			childInventory := git(childLane.WorkspacePath, "worktree", "list", "--porcelain")
			other := target.WorkspaceID
			if retained.WorkspacePath == targetPath {
				other = parent.WorkspaceID
			}
			if _, err := svc.executeManageWorkspaceTool(id, mustJSON(t, map[string]any{"action": "set_session", "workspace_id": other}), principal, sessions.ApplySessionMutation); err == nil || !reflect.DeepEqual(retained, load()) {
				t.Fatal("retained worker allowed parent retargeting")
			}
			releaseOnce.Do(func() { close(workerRelease) })
			workerErr := <-workerDone
			workerJoined = true
			if workerErr != nil {
				t.Fatal(workerErr)
			}
			childAfter, _, err := sessions.GetSession(child.ID)
			if err != nil || !reflect.DeepEqual(childBefore, childAfter) || git(childLane.WorkspacePath, "worktree", "list", "--porcelain") != childInventory || git(childLane.WorkspacePath, "rev-parse", "HEAD") != childBase.BaseCommit || git(childLane.WorkspacePath, "status", "--porcelain") != "" {
				t.Fatal("rejected parent transition changed held worker or Git state")
			}
			if _, err := workspaceSvc.DeleteCatalogEntryForPrincipal(principal, target.WorkspaceID, target.WorkspaceGeneration); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ResolveRuntimeWorkspaceScope(load(), principal); err == nil {
				t.Fatal("revoked attachment retained execution")
			}
			if !reflect.DeepEqual(retained, load()) {
				t.Fatal("revocation check rewrote historical session")
			}
		})
	}
}
