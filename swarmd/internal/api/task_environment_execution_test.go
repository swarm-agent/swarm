package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type managedCleanupFixture struct {
	manageDeploymentLifecycleService
	released bool
	request  lifecycle.ReleaseDeploymentRequest
}

func (f *managedCleanupFixture) ReleaseDeployment(ctx context.Context, req lifecycle.ReleaseDeploymentRequest) (*lifecycle.ReleaseDeploymentResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.released = true
	f.request = req
	return &lifecycle.ReleaseDeploymentResult{}, nil
}

// Requirement: acquisition followed by failed canonical binding releases only
// the new consumer receipt. Threat: cancelled caller leaks a usable orphan lease.
// bindOrReleaseTaskExecution owns this cleanup; a cancelled context and recording
// release boundary prove identity and uncancelled finite cleanup without effects.
func TestManagedExecutionBindingFailureReleases(t *testing.T) {
	f := &managedCleanupFixture{}
	s := &Server{deployments: f}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	err := s.bindOrReleaseTaskExecution(ctx, p, nil, "child", environments.DeploymentLease{ID: "receipt", WorkspaceID: "workspace"})
	if !errors.Is(err, context.Canceled) || !f.released || f.request.LeaseID != "receipt" || f.request.AccountScopeID != "account" || f.request.WorkspaceID != "workspace" || f.request.Attribution.SessionID != "child" || f.request.Attribution.Actor != "user" {
		t.Fatalf("wrong cleanup: %#v %v", f, err)
	}
}

// Requirement: managed execution admits only the exact current task Coder while
// retaining management denial. Threat: parent/foreign/stale sessions receiving a
// task receipt or replacing unrelated session metadata. Production
// validateTaskExecutionConsumer/bindTaskExecutionSession and V3 mutation own the
// boundary; isolated Pebble proves durable selector behavior without a daemon,
// credentials or provider. This does not qualify container execution/editing.
func TestManagedExecutionConsumerAndBinding(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	s := &Server{sessions: sessionruntime.NewService(store, nil)}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	root := t.TempDir()
	metadata := map[string]any{"agent_profile": map[string]any{"name": "coder"}, "project_id": "project", "task_id": "task", "task_attempt_id": "attempt", "swarm_v3_worktree_owner_session_id": "child", "swarm_v3_runtime_workspace_path": root, "unrelated": "retained"}
	child := pebblestore.SessionSnapshot{ID: "child", WorktreeEnabled: true, WorktreeRootPath: root, WorktreeBranch: "swarm/task", WorkspacePath: root, Metadata: metadata}
	child.AccountScopeID, child.UserID = p.AccountScopeID, p.UserID
	// Seed an already admitted repository lane; lane allocation has its own
	// worktree-admission tests. Then initialize its canonical V3 projection.
	if err := store.CreateSessionForAccount(child, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{Kind: pebblestore.V3SessionMutationUpdateMetadata, SessionID: child.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Session: &child, ClientRequestID: "create", PayloadHash: "create", WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "legacy", Path: root, SourcePath: root, OwnerSessionID: child.ID, Branch: child.WorktreeBranch}}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: p.AccountScopeID, SessionID: "child", Agent: "coder", ActiveAttemptID: "attempt", WorkspacePath: root, Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt", SessionID: "child", Role: "coder"}}}
	if err := s.validateTaskExecutionConsumer(p, task, "child"); err != nil {
		t.Fatal(err)
	}
	// Coders may use the environments the user set up.
	if !tool.EnvironmentToolAllowed(metadata, "manage_environments") {
		t.Fatal("Coder lost manage_environments")
	}
	for _, name := range []string{"account", "user", "session", "task", "attempt", "archived"} {
		t.Run(name, func(t *testing.T) {
			principal, changed := p, *task
			session := "child"
			switch name {
			case "account":
				principal.AccountScopeID = "other"
			case "user":
				principal.UserID = "other"
			case "session":
				session = "other"
			case "task":
				changed.ID = "other"
			case "attempt":
				changed.ActiveAttemptID = "other"
			case "archived":
				changed.Archived = true
			}
			if err := s.validateTaskExecutionConsumer(principal, &changed, session); err == nil {
				t.Fatal("wrong consumer admitted")
			}
			snapshot, _, _ := store.GetSession("child")
			if snapshot.Metadata[tool.ManagedExecutionMetadataKey] != nil {
				t.Fatal("rejected admission changed selector")
			}
		})
	}
	lease := environments.DeploymentLease{ID: "receipt", WorkspaceID: "workspace"}
	if err := s.bindTaskExecutionSession(context.Background(), p, task, "child", lease); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := store.GetSession("child")
	if err != nil || !found || snapshot.Metadata["unrelated"] != "retained" || snapshot.Metadata[tool.ManagedExecutionMetadataKey] == nil {
		t.Fatalf("binding was not durable or lost metadata: %v", err)
	}
	projection, found, err := store.GetV3SessionProjection("child")
	if err != nil || !found || projection.LastEventSeq < 2 {
		t.Fatalf("binding bypassed canonical V3 projection: %#v %v", projection, err)
	}
}
