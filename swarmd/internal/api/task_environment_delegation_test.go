package api

import (
	"context"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// The durable lineage and current parent attempt, rather than copied metadata,
// authorize a delegated consumer. These isolated store tests exercise the real
// admission code; they are not provider-backed execution evidence.
func TestManagedDelegatedExecutionAdmission(t *testing.T) {
	for _, test := range []string{"current", "finished_lineage", "missing_lineage", "foreign_parent", "foreign_account", "foreign_user", "wrong_workspace", "wrong_owner", "wrong_source", "wrong_attempt", "wrong_task", "archived", "malformed_metadata"} {
		t.Run(test, func(t *testing.T) {
			db, err := pebblestore.Open(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := pebblestore.NewSessionStore(db)
			s := &Server{sessions: sessionruntime.NewService(store, nil)}
			p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
			source, worktree := t.TempDir(), t.TempDir()
			parent := pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user", WorkspacePath: source, Metadata: map[string]any{"agent_profile": map[string]any{"name": "swarm"}, "project_id": "project", "task_id": "task", "task_attempt_id": "attempt", "swarm_v3_source_workspace_path": source, "swarm_v3_source_workspace_id": "workspace", "swarm_v3_source_workspace_generation": 1}}
			if err := store.CreateSessionForAccount(parent, p.UserID, p.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			child := pebblestore.SessionSnapshot{ID: "child", AccountScopeID: "account", UserID: "user", WorkspacePath: worktree, WorktreeEnabled: true, WorktreeRootPath: worktree, WorktreeBranch: "swarm/child", Metadata: map[string]any{"agent_profile": map[string]any{"name": "coder"}, "project_id": "project", "task_id": "task", "task_attempt_id": "attempt", "swarm_v3_source_workspace_path": source, "swarm_v3_worktree_owner_session_id": "child", "swarm_v3_runtime_workspace_path": worktree}}
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", SessionID: "parent", Agent: "swarm", ActiveAttemptID: "attempt", SourceWorkspace: pebblestore.ProjectTaskSource{Path: source, WorkspaceID: "workspace", WorkspaceGeneration: 1}, Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt", SessionID: "parent", Role: "swarm"}}}
			generation := pebblestore.DelegatedChildGenerationRecord{SessionID: "child", ParentSessionID: "parent", RunID: "run", ParentRunID: "parent-run", AttemptID: "child-attempt", PermissionPrincipalID: "user", PermissionScopeID: "parent", ReservationSessionID: "parent", ReservationRunID: "parent-run", ReservationCallID: "call", WorkspacePath: worktree, WorktreeBranch: "swarm/child", ImmutableBaseCommit: "base"}
			switch test {
			case "foreign_parent":
				generation.ParentSessionID = "other"
			case "foreign_account":
				p.AccountScopeID = "other"
			case "foreign_user":
				p.UserID = "other"
			case "wrong_workspace":
				generation.WorkspacePath = t.TempDir()
			case "wrong_owner":
				child.Metadata["swarm_v3_worktree_owner_session_id"] = "other"
			case "wrong_source":
				child.Metadata["swarm_v3_source_workspace_path"] = "other"
			case "wrong_attempt":
				task.ActiveAttemptID = "other"
			case "wrong_task":
				child.Metadata["task_id"] = "other"
			case "archived":
				task.Archived = true
			case "malformed_metadata":
				child.Metadata["project_id"] = []string{"project"}
			}
			if test != "missing_lineage" {
				if _, _, err := store.CreateDelegatedChildLineage(pebblestore.DelegatedChildLineageRecord{AccountScopeID: "account", LogicalTaskID: "logical"}, generation, "create"); err != nil {
					t.Fatal(err)
				}
			}
			if test == "finished_lineage" {
				if _, _, err := store.FinishDelegatedChild(pebblestore.FinishDelegatedChildInput{AccountScopeID: "account", LogicalTaskID: "logical", ExpectedLineageRevision: 1, ExpectedGenerationRevision: 1, Generation: 1, SessionID: "child", MutationID: "finish", State: pebblestore.DelegatedChildGenerationSucceeded}); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.delegatedExecutionParent(p, task, child)
			if test == "current" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("untrusted delegated consumer admitted")
			}
			// Coders may use the environments the user set up.
			if !tool.EnvironmentToolAllowed(child.Metadata, "manage_environments") && test == "current" {
				t.Fatal("Coder lost manage_environments")
			}
		})
	}
}

func TestManagedDelegatedExecutionUnavailable(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{sessions: sessionruntime.NewService(pebblestore.NewSessionStore(db), nil)}
	if err := s.BindDelegatedTaskExecution(context.Background(), "missing-parent", "child"); err == nil {
		t.Fatal("missing parent admitted")
	}
	for _, value := range []any{nil, "bad", map[string]any{"workspace_id": "workspace"}, map[string]any{"lease_id": "lease"}} {
		if _, err := selectedExecution(pebblestore.SessionSnapshot{Metadata: map[string]any{tool.ManagedExecutionMetadataKey: value}}); err == nil {
			t.Fatal("malformed selection admitted")
		}
	}
}

// Only the default general Swarm coordinator may select a clean catalog source
// without a parent worktree. Coder, coding outcomes, and dirty/moved sources
// remain rejected. The independent worktree admission is tested above.
func TestManagedGeneralSwarmSource(t *testing.T) {
	for _, name := range []string{"current", "coder", "coding_outcome", "other_workspace", "dirty", "changed_commit", "missing_source"} {
		t.Run(name, func(t *testing.T) {
			db, err := pebblestore.Open(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := pebblestore.NewSessionStore(db)
			root := t.TempDir()
			parent := pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user", WorkspacePath: root, Metadata: map[string]any{"project_id": "project", "task_id": "task", "task_attempt_id": "attempt", "swarm_v3_source_workspace_path": root, "swarm_v3_source_workspace_id": "workspace", "swarm_v3_source_workspace_generation": 1}}
			if err := store.CreateSessionForAccount(parent, "user", "account"); err != nil {
				t.Fatal(err)
			}
			task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", SessionID: "parent", WorkspacePath: root, Agent: "swarm", OutcomeType: "general", ActiveAttemptID: "attempt", SourceWorkspace: pebblestore.ProjectTaskSource{Path: root, WorkspaceID: "workspace", WorkspaceGeneration: 1}}
			state := worktreeruntime.TaskWorkspaceState{WorkspacePath: root, Clean: true, HeadCommit: "exact-commit"}
			switch name {
			case "coder":
				task.Agent = "coder"
			case "coding_outcome":
				task.OutcomeType = "code_pr"
			case "other_workspace":
				task.WorkspacePath = t.TempDir()
			case "dirty":
				state.Clean = false
			case "changed_commit":
				state.HeadCommit = "other"
			case "missing_source":
				task.SourceWorkspace.Path = ""
			}
			s := &Server{sessions: sessionruntime.NewService(store, nil), worktrees: &fakeWorktreeService{inspectStates: map[string]worktreeruntime.TaskWorkspaceState{root: state}}}
			err = s.validateTaskEnvironmentCommit(task, "exact-commit")
			if name == "current" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid source admitted")
			}
		})
	}
}
