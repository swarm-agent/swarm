package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

type taskReceiptBoundary struct {
	manageDeploymentLifecycleService
	lease     environments.DeploymentLease
	submitted int
}

func (f *taskReceiptBoundary) GetLease(string, string, string) (environments.DeploymentLease, bool, error) {
	return f.lease, true, nil
}
func (f *taskReceiptBoundary) Submit(_ context.Context, req lifecycle.SubmitOperationRequest) (*environments.EnvironmentOperation, error) {
	f.submitted++
	return &environments.EnvironmentOperation{Action: req.Action, LeaseID: req.LeaseID}, nil
}

// Purpose: ManageTaskEnvironment must permit cleanup of one's revoked receipt
// after reopen, but never inspection, exec or cancellation using it. Real task
// persistence and a counting submit boundary prove rejection has no provider
// effect and cannot release another principal's receipt.
func TestTaskEnvironmentStaleReceiptRelease(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Revision: 1, Agent: "swarm"}
	if err := f.server.sessions.Store().PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	manager := &taskReceiptBoundary{lease: environments.DeploymentLease{ID: "receipt", AccountScopeID: p.AccountScopeID, WorkspaceID: "workspace", DeploymentID: "deployment", ConsumerType: environments.ConsumerTypeCustom, ConsumerID: p.UserID, Active: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), PreparedSource: &environments.PreparedDeploymentSource{}, TaskBinding: &environments.TaskLeaseBinding{UserID: p.UserID, ProjectID: "project", TaskID: "task", AttemptID: "old-attempt", AttachmentID: "attachment", AttachmentRevision: 1}}}
	f.server.deployments = manager
	req := tool.TaskEnvironmentRequest{ProjectID: "project", TaskID: "task", AttachmentID: "attachment", WorkspaceID: "workspace", LeaseID: "receipt"}
	for _, action := range []string{"exec", "get_deployment", "get_operation", "cancel_operation"} {
		req.Action = action
		if _, err := f.server.ManageTaskEnvironment(ctx, p, "", req); err == nil {
			t.Fatalf("stale %s admitted", action)
		}
		if manager.submitted != 0 {
			t.Fatal("rejected receipt caused side effect")
		}
	}
	req.Action = "release"
	foreign := p
	foreign.UserID = "other-user"
	if _, err := f.server.ManageTaskEnvironment(ctx, foreign, "", req); err == nil || manager.submitted != 0 {
		t.Fatal("foreign release admitted")
	}
	result, err := f.server.ManageTaskEnvironment(ctx, p, "", req)
	if err != nil || manager.submitted != 1 || result.Operation == nil || result.Operation.LeaseID != "receipt" {
		t.Fatalf("own cleanup failed: %+v %v", result, err)
	}
	stored, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, "project", "task")
	if err != nil || stored.Revision != task.Revision {
		t.Fatal("receipt cleanup mutated task")
	}
}

// Purpose: generic HTTP environment/deployment paths must not bypass task receipt
// checks through GET aliases, summary/history, body attribution or cancellation.
// Nil provider services make any accidental traversal observable before mutation.
func TestTaskEnvironmentHTTPBypassAndTrailingJSON(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID, SessionID: "consumer"}
	snap := pebblestore.SessionSnapshot{ID: p.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Mode: "auto", Metadata: map[string]any{"project_id": "project", "task_id": "task", "agent_profile": pebblestore.AgentProfile{Name: "swarm"}}}
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snap.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: snap.ID, IdempotencyKey: snap.ID, PayloadHash: snap.ID, RequestHash: snap.ID, Kind: sessionruntime.SessionMutationCreateSession, Session: &snap}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/environments/summary", "/v1/environments/history", "/v1/environments/operations?operation_id=other", "/v1/environments/deployments", "/v1/environments?action=get_deployment", "/v1/deployments?id=other"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r = r.WithContext(identity.ContextWithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		if strings.HasPrefix(path, "/v1/deployments") {
			f.server.handleDeployments(w, r)
		} else {
			f.server.handleEnvironments(w, r)
		}
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "own receipts") {
			t.Fatalf("bypass %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, payload := range []string{`{"action":"list_attachments"} {}`, `{"action":"list_attachments"} garbage`, `{"action":"list_attachments","unknown":true}`} {
		r := httptest.NewRequest(http.MethodPost, "/v1/task-environments", strings.NewReader(payload))
		r = r.WithContext(identity.ContextWithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		f.server.handleTaskEnvironments(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("ambiguous payload: %d %s", w.Code, w.Body.String())
		}
	}
}

// Purpose: validateTaskEnvironmentSource must reject another project member or
// catalog generation before Git/provider inspection. No services are configured,
// so a successful rejection proves no fallback to ambient/current-dev authority.
func TestTaskEnvironmentExactSource(t *testing.T) {
	s := &Server{}
	task := &pebblestore.ProjectTaskRecord{SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: "product", WorkspaceGeneration: 7}}
	for _, product := range []environments.CommittedBuildSource{{WorkspaceID: "other", WorkspaceGeneration: 7}, {WorkspaceID: "product", WorkspaceGeneration: 8}} {
		err := s.validateTaskEnvironmentSource(identity.Principal{}, task, environments.PreparedDeploymentSource{Build: environments.ImageBuildResult{Product: product}})
		if err == nil || !strings.Contains(err.Error(), "exact task source") {
			t.Fatalf("wrong source accepted: %v", err)
		}
	}
}

// Purpose: initial and reopen seed references must stay bounded and must never
// serialize receipt/runtime material. The formatter is the narrowest layer.
func TestTaskEnvironmentSeedContext(t *testing.T) {
	task := &pebblestore.ProjectTaskRecord{}
	for i := 0; i < 20; i++ {
		task.EnvironmentAttachments = append(task.EnvironmentAttachments, environments.TaskEnvironmentAttachment{ID: "reference", Revision: 1, EnvironmentName: "secret-runtime-marker"})
	}
	text := projectTaskEnvironmentContext(task)
	if len(text) > 2048 || strings.Count(text, "attachment_id") != 4 || strings.Contains(text, "secret-runtime-marker") || !strings.Contains(text, "CAS") {
		t.Fatalf("unsafe context: %s", text)
	}
}

type taskAttachmentLifecycleFixture struct {
	manageDeploymentLifecycleService
	dep     environments.Deployment
	leases  map[string]environments.DeploymentLease
	effects int
}

func (f *taskAttachmentLifecycleFixture) GetDeployment(string, string, string) (environments.Deployment, bool, error) {
	return f.dep, true, nil
}
func (f *taskAttachmentLifecycleFixture) GetLease(_, _, id string) (environments.DeploymentLease, bool, error) {
	l, ok := f.leases[id]
	return l, ok, nil
}
func (f *taskAttachmentLifecycleFixture) AcquirePreparedLease(_ context.Context, req lifecycle.AcquirePreparedLeaseRequest) (environments.DeploymentLease, error) {
	id := fmt.Sprintf("receipt-%d", len(f.leases)+1)
	kind, consumer := environments.ConsumerTypeCustom, req.Attribution.Actor
	if req.Attribution.SessionID != "" {
		kind, consumer = environments.ConsumerTypeSession, req.Attribution.SessionID
	}
	l := environments.DeploymentLease{ID: id, AccountScopeID: req.Source.AccountScopeID, WorkspaceID: req.Source.WorkspaceID, DeploymentID: req.Source.DeploymentID, EnvironmentID: req.Source.EnvironmentID, ConsumerType: kind, ConsumerID: consumer, PreparedSource: &req.Source, TaskBinding: req.Binding, Active: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	f.leases[id] = l
	return l, nil
}
func (f *taskAttachmentLifecycleFixture) Submit(_ context.Context, req lifecycle.SubmitOperationRequest) (*environments.EnvironmentOperation, error) {
	f.effects++
	if req.Action == "release" {
		l := f.leases[req.LeaseID]
		l.Active = false
		f.leases[l.ID] = l
	}
	return &environments.EnvironmentOperation{Action: req.Action, LeaseID: req.LeaseID}, nil
}

// Purpose: the API attachment boundary must support prepare-before-assignment,
// explicit CAS assignment, independent consumer receipts, self attach and cleanup.
// Real Git/catalog/session/task state proves exact source checks; the counting
// lifecycle double confines this test to API authority (not provider or lease-store
// concurrency evidence). Preparation forwards connection selection to the lifecycle
// authority. Failed generation/source/owner requests have no effects. Conversation
// admission retains the real stored-contract compiler, not a permissive stub.
// The initial attempt and later self-attach share one canonically admitted isolated
// session; no run intent is created and no provider execution is started.
func TestTaskEnvironmentAttachmentWorkflow(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	// Use the same admission wiring as project_task_lineage_test.go; the
	// matrix fixture still leaves asynchronous provider execution disabled.
	runner := runruntime.NewService(f.server.sessions, f.server.model, nil, tool.NewRuntime(1), nil, f.server.agents, nil, nil)
	runner.SetAgentModelSettingsService(f.server.agentModelSettings)
	f.server.runner = runner
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "source"), []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source")
	git("commit", "-m", "candidate")
	head := git("rev-parse", "HEAD")
	catalog := pebblestore.NewWorkspaceStore(f.db)
	entry, err := catalog.AddForAccount(f.accountID, repo, "Product")
	if err != nil {
		t.Fatal(err)
	}
	binding := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}
	f.server.workspace = workspace.NewService(catalog)
	worktrees := worktree.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
	f.server.worktrees = worktrees
	seedTaskSessionBinding(t, f, binding)
	db := f.server.sessions.Store()
	proj := &pebblestore.ProjectRecord{ID: "attachment-project", Name: "Review", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}
	if err := db.PutProject(f.accountID, proj); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "attachment-task", ProjectID: proj.ID, Title: "Review", Revision: 1, Agent: "swarm", SourceWorkspace: binding}
	if err := db.PutProjectTask(f.accountID, task); err != nil {
		t.Fatal(err)
	}
	product := environments.CommittedBuildSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Commit: head}
	build := environments.ImageBuildResult{OperationID: "build", ImageID: "sha256:" + strings.Repeat("b", 64), Product: product, Recipe: product, DefinitionDigest: "definition", ContextDigest: "context"}
	manager := &taskAttachmentLifecycleFixture{leases: map[string]environments.DeploymentLease{}, dep: environments.Deployment{ID: "candidate", AccountScopeID: f.accountID, WorkspaceID: entry.WorkspaceID, EnvironmentID: "environment", Status: environments.DeploymentStatusReady, CreatedAt: time.Now().UnixMilli(), Runtime: environments.RuntimeMetadata{ContainerID: "runtime"}, Build: &build}}
	f.server.deployments = manager
	// Attach reads only definition identity, not a mutable definition as build authority.
	f.server.environments = &taskAttachmentDefinitionFixture{env: environments.Environment{ID: "environment", Name: "Review"}}
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	// Preparation uses the same task/catalog checks before forwarding selection.
	preparer := &taskPreparationFixture{}
	f.server.deployments = preparer
	f.server.environments = &taskAttachmentDefinitionFixture{env: environments.Environment{ID: "environment", Name: "Review", Build: &environments.ImageBuildDefinition{Product: product, Recipe: product}}}
	for _, connection := range []string{"", "selected-connection"} {
		prepare := tool.TaskEnvironmentRequest{Action: "build", ProjectID: proj.ID, TaskID: task.ID, WorkspaceID: entry.WorkspaceID, EnvironmentID: "environment", ConnectionID: connection}
		if _, err := f.server.ManageTaskEnvironment(ctx, p, "", prepare); err != nil {
			t.Fatal(err)
		}
		if preparer.request.ConnectionID != connection || preparer.request.AccountScopeID != p.AccountScopeID || preparer.request.WorkspaceID != entry.WorkspaceID {
			t.Fatalf("preparation routing changed: %+v", preparer.request)
		}
		before := preparer.effects
		foreign := p
		foreign.AccountScopeID = "foreign-account"
		if _, err := f.server.ManageTaskEnvironment(ctx, foreign, "", prepare); err == nil || preparer.effects != before {
			t.Fatal("foreign preparation reached lifecycle")
		}
	}
	f.server.deployments = manager
	f.server.environments = &taskAttachmentDefinitionFixture{env: environments.Environment{ID: "environment", Name: "Review"}}
	req := tool.TaskEnvironmentRequest{Action: "attach_task", ProjectID: proj.ID, TaskID: task.ID, AttachmentID: "review", WorkspaceID: entry.WorkspaceID, DeploymentID: "candidate", ExpectedTaskRevision: task.Revision, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	w := projectConversationRequest(t, f.server, p, http.MethodPost, ProjectsPath+"/"+proj.ID+"/sessions", map[string]any{"client_request_id": "attachment-parent", "mode": "auto"})
	if w.Code != http.StatusOK {
		t.Fatalf("Orchestrator conversation: %d %s", w.Code, w.Body.String())
	}
	parents, err := db.ListProjectConversations(p.AccountScopeID, p.UserID, proj.ID, 10)
	if err != nil || len(parents) != 1 {
		t.Fatalf("Orchestrator lookup: %v", err)
	}
	parentID := parents[0].ID
	_, err = f.server.ManageTaskEnvironment(ctx, p, parentID, req)
	if err != nil {
		t.Fatal(err)
	}
	acquire := tool.TaskEnvironmentRequest{Action: "acquire_attachment", ProjectID: proj.ID, TaskID: task.ID, AttachmentID: "review", AttemptID: "initial", ExpectedAttachmentRevision: 1}
	if _, err := f.server.ManageTaskEnvironment(ctx, p, "", acquire); err == nil || len(manager.leases) != 0 {
		t.Fatal("unassigned preparation acquired")
	}
	// Assign one real isolated session only after proving preparation cannot be
	// acquired without an attempt. Persistence creates the canonical initial
	// attempt from SessionID and records the task/session reverse membership.
	alloc, err := worktrees.AllocateProjectTaskFollowup(p, repo, "attached-swarm", "agent/attached-swarm", head, "dev")
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := db.UpdateProjectTask(p.AccountScopeID, proj.ID, task.ID, func(task *pebblestore.ProjectTaskRecord) error {
		task.SessionID, task.WorkspacePath, task.WorktreeBranch, task.BaseCommit = "attached-swarm", alloc.WorkspacePath, alloc.BranchName, head
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := pebblestore.SessionSnapshot{ID: reserved.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Mode: "auto", WorkspacePath: alloc.WorkspacePath, WorktreeEnabled: true, WorktreeRootPath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, WorktreeBaseBranch: "dev", Metadata: map[string]any{"agent_profile": pebblestore.AgentProfile{Name: "swarm"}, "project_id": proj.ID, "task_id": task.ID, "base_commit": head, "swarm_v3_source_workspace_path": repo, "swarm_v3_source_workspace_id": binding.WorkspaceID, "swarm_v3_source_workspace_generation": binding.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": reserved.SessionID, "swarm_v3_runtime_workspace_path": alloc.WorkspacePath}}
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snap.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: snap.ID, IdempotencyKey: snap.ID, PayloadHash: snap.ID, RequestHash: snap.ID, Kind: sessionruntime.SessionMutationCreateSession, Session: &snap, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: alloc.WorkspacePath, SourcePath: repo, OwnerSessionID: snap.ID, Branch: alloc.BranchName, AllocatedRuntimeRoot: true}}); err != nil {
		t.Fatal(err)
	}
	current, err := f.server.authorizeTaskEnvironment(p, snap.ID, proj.ID, task.ID)
	if err != nil || current.ActiveAttempt() == nil || current.ActiveAttemptID != "initial" || current.ActiveAttempt().SessionID != snap.ID || current.SessionID != snap.ID {
		t.Fatalf("durable current consumer: %+v %v", current, err)
	}
	if _, found, err := db.GetV3SessionActiveRunIntent(snap.ID); err != nil || found {
		t.Fatalf("fixture must not start provider execution: found=%v err=%v", found, err)
	}
	req.ExpectedTaskRevision, req.ExpectedAttachmentRevision, req.AttemptID = reserved.Revision, 1, "initial"
	assigned, err := f.server.ManageTaskEnvironment(ctx, p, parentID, req)
	if err != nil {
		t.Fatal(err)
	}
	acquire.ExpectedAttachmentRevision = 2
	one, err := f.server.ManageTaskEnvironment(ctx, p, "", acquire)
	if err != nil {
		t.Fatal(err)
	}
	other := p
	other.UserID = "reviewer"
	two, err := f.server.ManageTaskEnvironment(ctx, other, "", acquire)
	if err != nil || one.Lease.ID == two.Lease.ID {
		t.Fatalf("independent receipts: %v", err)
	}
	use := tool.TaskEnvironmentRequest{Action: "get_deployment", ProjectID: proj.ID, TaskID: task.ID, AttachmentID: "review", WorkspaceID: entry.WorkspaceID, LeaseID: one.Lease.ID}
	view, err := f.server.ManageTaskEnvironment(ctx, p, "", use)
	if err != nil || view.Deployment == nil || view.Deployment.ID != "candidate" {
		t.Fatalf("deployment view: %+v %v", view, err)
	}
	use.Action = "exec"
	if _, err := f.server.ManageTaskEnvironment(ctx, other, "", use); err == nil || manager.effects != 0 {
		t.Fatal("foreign receipt executed")
	}
	if _, err := f.server.ManageTaskEnvironment(ctx, p, "", use); err != nil || manager.effects != 1 {
		t.Fatalf("own execution: %v", err)
	}
	manager.dep.ReviewDeadline = time.Now().Add(-time.Second).UnixMilli()
	if _, err := f.server.ManageTaskEnvironment(ctx, p, "", use); err == nil || manager.effects != 1 {
		t.Fatal("expired review executed")
	}
	listed, err := f.server.ManageTaskEnvironment(ctx, p, "", tool.TaskEnvironmentRequest{Action: "list_attachments", ProjectID: proj.ID, TaskID: task.ID})
	if err != nil || listed.Attachments[0].State != "stale" {
		t.Fatal("expired review projected ready")
	}
	manager.dep.ReviewDeadline = 0
	manager.dep.CreatedAt++
	if _, err := f.server.ManageTaskEnvironment(ctx, p, "", use); err == nil || manager.effects != 1 {
		t.Fatal("changed generation executed")
	}
	manager.dep.CreatedAt--
	detach := tool.TaskEnvironmentRequest{Action: "detach_task", ProjectID: proj.ID, TaskID: task.ID, AttachmentID: "review", ExpectedTaskRevision: assigned.TaskRevision, ExpectedAttachmentRevision: 2}
	if _, err := f.server.ManageTaskEnvironment(ctx, p, "", detach); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.ManageTaskEnvironment(ctx, p, "", use); err == nil || manager.effects != 1 {
		t.Fatal("detached receipt executed")
	}
	use.Action = "release"
	if _, err := f.server.ManageTaskEnvironment(ctx, p, "", use); err != nil {
		t.Fatal(err)
	}
	if manager.leases[one.Lease.ID].Active || !manager.leases[two.Lease.ID].Active {
		t.Fatal("release affected another consumer")
	}
	// Self-attach uses the same admitted session and initial attempt, without
	// rewriting attempt history or creating a second consumer identity.
	task, found, err := db.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
	if err != nil || !found || task.ActiveAttempt() == nil || task.SessionID != snap.ID || task.ActiveAttemptID != reserved.ActiveAttemptID || task.ActiveAttempt().SessionID != snap.ID || len(task.Attempts) != 1 {
		t.Fatalf("self-attach lost initial linkage: %+v %v", task, err)
	}
	req.AttachmentID, req.ExpectedAttachmentRevision, req.ExpectedTaskRevision = "self-review", 0, task.Revision
	if _, err := f.server.ManageTaskEnvironment(ctx, p, snap.ID, req); err != nil {
		t.Fatalf("self attach: %v", err)
	}
	acquire.AttachmentID, acquire.ExpectedAttachmentRevision = "self-review", 1
	own, err := f.server.ManageTaskEnvironment(ctx, p, snap.ID, acquire)
	if err != nil || own.Lease.ConsumerID != snap.ID {
		t.Fatalf("current Swarm acquire: %v", err)
	}
	use.AttachmentID, use.LeaseID, use.Action = "self-review", own.Lease.ID, "exec"
	if _, err := f.server.ManageTaskEnvironment(ctx, p, snap.ID, use); err != nil {
		t.Fatalf("Swarm exec: %v", err)
	}
	use.Action = "release"
	if _, err := f.server.ManageTaskEnvironment(ctx, p, snap.ID, use); err != nil {
		t.Fatalf("Swarm release: %v", err)
	}
}

type taskAttachmentDefinitionFixture struct {
	manageEnvironmentStore
	env environments.Environment
}

func (f *taskAttachmentDefinitionFixture) Get(string, string, string) (environments.Environment, bool, error) {
	return f.env, true, nil
}

// Purpose: task get_deployment must expose useful local access without leaking
// credentials or introducing public-network endpoints. The pure projection layer
// is sufficient to prove redaction and the fixed result bound.
func TestTaskEnvironmentSafeEndpoints(t *testing.T) {
	d := environments.Deployment{Runtime: environments.RuntimeMetadata{Endpoint: "http://127.0.0.1:8080"}}
	for _, raw := range []string{"https://user:password@127.0.0.1:8080", "http://127.0.0.1:8080?token=secret", "https://example.invalid", "file:///etc/config", "http://127.0.0.1:8080"} {
		d.Runtime.AssignedPorts = append(d.Runtime.AssignedPorts, environments.AssignedPort{EndpointURL: raw})
	}
	got := taskDeploymentEndpoints(d)
	if len(got) != 1 || got[0] != d.Runtime.Endpoint {
		t.Fatalf("unsafe endpoint projection: %v", got)
	}
}

type taskPreparationFixture struct {
	manageDeploymentLifecycleService
	op      environments.EnvironmentOperation
	lease   environments.DeploymentLease
	effects int
	request lifecycle.SubmitOperationRequest
}

func (f *taskPreparationFixture) Get(context.Context, string, string, string) (environments.EnvironmentOperation, bool, error) {
	return f.op, true, nil
}
func (f *taskPreparationFixture) GetLease(string, string, string) (environments.DeploymentLease, bool, error) {
	return f.lease, true, nil
}
func (f *taskPreparationFixture) Submit(_ context.Context, r lifecycle.SubmitOperationRequest) (*environments.EnvironmentOperation, error) {
	f.effects++
	f.request = r
	return &environments.EnvironmentOperation{Action: r.Action, LeaseID: r.LeaseID}, nil
}

// Purpose: the original preparer must explicitly release only its own exclusive
// receipt before independent attachment consumers acquire. This API boundary
// test prevents a cross-consumer takeover and receipt disclosure without effects.
func TestTaskEnvironmentReleasePreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f := &taskPreparationFixture{op: environments.EnvironmentOperation{Action: "deploy", Status: environments.OperationStatusSucceeded, OperationID: "operation", DeploymentID: "deployment", LeaseID: "private", Attribution: environments.OperationAttribution{Actor: "user", SessionID: "parent"}}, lease: environments.DeploymentLease{ID: "private", DeploymentID: "deployment", ConsumerType: environments.ConsumerTypeSession, ConsumerID: "parent"}}
	s := &Server{deployments: f}
	p := identity.Principal{Type: "user", UserID: "user", AccountScopeID: "account"}
	task := &pebblestore.ProjectTaskRecord{Revision: 1}
	req := tool.TaskEnvironmentRequest{Action: "release_preparation", WorkspaceID: "workspace", OperationID: "operation"}
	if _, err := s.taskPreparationOperation(ctx, p, "other", task, req); err == nil || f.effects != 0 {
		t.Fatal("foreign preparer released lease")
	}
	f.lease.ConsumerID = "other"
	if _, err := s.taskPreparationOperation(ctx, p, "parent", task, req); err == nil || f.effects != 0 {
		t.Fatal("foreign receipt released")
	}
	f.lease.ConsumerID = "parent"
	out, err := s.taskPreparationOperation(ctx, p, "parent", task, req)
	if err != nil || f.effects != 1 || out.Operation == nil || out.Operation.LeaseID != "" {
		t.Fatalf("own preparation handoff: %+v %v", out, err)
	}
}
