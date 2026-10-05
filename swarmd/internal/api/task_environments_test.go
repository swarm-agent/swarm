package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: ManageTaskEnvironment and ValidateTaskEnvironmentLease must reject
// revoked task evidence before catalog/provider access. Real temporary Pebble is
// the narrowest boundary proving rejection leaves task state unchanged. This is
// deterministic authorization coverage, not live environment validation.
func TestTaskEnvironmentRevokedBinding(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := environments.CommittedBuildSource{WorkspaceID: "workspace", WorkspaceGeneration: 1, Commit: strings.Repeat("a", 40)}
	a := environments.TaskEnvironmentAttachment{ID: "attachment", Revision: 1, AccountScopeID: f.accountID, ProjectID: "project", TaskID: "task", AttemptID: "initial", EnvironmentID: "environment", EnvironmentName: "Review", State: "ready", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Source: environments.PreparedDeploymentSource{AccountScopeID: f.accountID, WorkspaceID: "workspace", EnvironmentID: "environment", DeploymentID: "deployment", CreatedAt: 1, ContainerID: "runtime", Build: environments.ImageBuildResult{OperationID: "build", ImageID: "sha256:"+strings.Repeat("b",64), Product: source, Recipe: source, DefinitionDigest: "definition", ContextDigest: "context"}}}
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Revision: 1, Agent: "swarm", EnvironmentAttachments: []environments.TaskEnvironmentAttachment{a}}
	if err := f.server.sessions.Store().PutProjectTask(f.accountID, task); err != nil { t.Fatal(err) }
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.userID, AccountScopeID: f.accountID}
	lease := environments.DeploymentLease{AccountScopeID: f.accountID, ConsumerType: environments.ConsumerTypeCustom, ConsumerID: f.userID, PreparedSource: &a.Source, TaskBinding: &environments.TaskLeaseBinding{ProjectID: "project", TaskID: "task", AttemptID: "initial", AttachmentID: a.ID, AttachmentRevision: 2, UserID: f.userID}}
	if err := f.server.ValidateTaskEnvironmentLease(ctx, lease); err == nil || !strings.Contains(err.Error(), "replaced") { t.Fatalf("stale revision: %v", err) }
	lease.TaskBinding.AttachmentRevision = 1
	lease.TaskBinding.AttemptID = "old"
	if err := f.server.ValidateTaskEnvironmentLease(ctx, lease); err == nil || !strings.Contains(err.Error(), "attempt") { t.Fatalf("old attempt: %v", err) }
	lease.TaskBinding.AttemptID = "initial"
	for _, bad := range []identity.Principal{{}, {Type: "user", UserID: f.userID, AccountScopeID: "other"}, {Type: "user", UserID: f.userID, AccountScopeID: f.accountID, SessionID: "missing"}} {
		if _, err := f.server.ManageTaskEnvironment(ctx, bad, "", tool.TaskEnvironmentRequest{Action: "detach_task", ProjectID: "project", TaskID: "task", AttachmentID: a.ID, ExpectedTaskRevision: task.Revision, ExpectedAttachmentRevision: 1}); err == nil { t.Fatal("foreign caller mutated attachment") }
	}
	stored, _, err := f.server.sessions.Store().GetProjectTask(f.accountID, "project", "task")
	if err != nil || len(stored.EnvironmentAttachments) != 1 || stored.Revision != task.Revision { t.Fatal("denial changed durable task") }
	_, err = f.server.ManageTaskEnvironment(ctx, p, "", tool.TaskEnvironmentRequest{Action: "detach_task", ProjectID: "project", TaskID: "task", AttachmentID: a.ID, ExpectedTaskRevision: task.Revision, ExpectedAttachmentRevision: 1})
	if err != nil { t.Fatal(err) }
	if err := f.server.ValidateTaskEnvironmentLease(ctx, lease); err == nil || !strings.Contains(err.Error(), "detached") { t.Fatalf("detached receipt: %v", err) }
}

// Purpose: task-linked Swarm must be admitted only for its own durable current
// attempt; Coder and explicitly denied capabilities cannot borrow the environment
// tool. This service test uses durable sessions and checks no task mutation.
func TestTaskEnvironmentConsumerAdmission(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	for _, role := range []string{"swarm", "coder", "system-orchestrator"} {
		sid := "task-env-"+role
		task := &pebblestore.ProjectTaskRecord{ID: sid, ProjectID: "project", Title: "Task", Revision: 1, Agent: "swarm", SessionID: sid}
		if err := f.server.sessions.Store().PutProjectTask(f.accountID, task); err != nil { t.Fatal(err) }
		snap := pebblestore.SessionSnapshot{ID: sid, UserID: f.userID, AccountScopeID: f.accountID, Mode: "auto", Metadata: map[string]any{"project_id": "project", "task_id": sid, "agent_name": role, "agent_profile": pebblestore.AgentProfile{Name: role}}}
		if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: sid, UserID: f.userID, AccountScopeID: f.accountID, ClientRequestID: sid, IdempotencyKey: sid, PayloadHash: sid, RequestHash: sid, Kind: sessionruntime.SessionMutationCreateSession, Session: &snap}); err != nil { t.Fatal(err) }
		_, err := f.server.authorizeTaskEnvironment(p, sid, "project", sid)
		if (err == nil) != (role != "coder") { t.Fatalf("role %s: %v", role, err) }
		if _, err := f.server.authorizeTaskEnvironment(p, sid, "project", "other-task"); err == nil { t.Fatal("foreign task admitted") }
		stored, _, err := f.server.sessions.Store().GetProjectTask(f.accountID, "project", sid)
		if err != nil || stored.Revision != task.Revision || len(stored.EnvironmentAttachments) != 0 { t.Fatal("admission mutated task") }
	}
}
