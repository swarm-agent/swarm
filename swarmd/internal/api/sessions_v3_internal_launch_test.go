package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	toolruntime "swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestLaunchV3SessionCommitsCanonicalMetadataOutboxAndExecutorLifecycle(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspacePath)
	binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, bindingID)
	if err != nil || !ok {
		t.Fatalf("load workspace binding ok=%t err=%v", ok, err)
	}
	runner := installSessionsV3TestProvider(server, "deployed response")
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	request := runruntime.V3SessionLaunchRequest{
		Principal: testPrincipal(), SessionID: "deployed-session", RunID: "deployed-run",
		CreateClientRequestID: "deploy-create", MessageClientRequestID: "deploy-message", MessageID: "deployed-message",
		Title: "Deployed session", Prompt: "do deployed work", Mode: sessionruntime.ModeAuto, AgentName: "swarm",
		Preference:        pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
		SourceWorkspaceID: binding.SourceWorkspaceID, SourceWorkspaceGeneration: binding.SourceWorkspaceGeneration,
		SourceWorkspacePath: binding.SourceWorkspacePath, SourceWorkspaceName: binding.SourceWorkspaceName,
		WorkspaceBindingID: binding.BindingID, ParentSessionID: "parent-session",
		DeploymentManifestDigest: "manifest-digest", DeploymentProposalID: "proposal-1",
	}
	launched, err := server.LaunchV3Session(context.Background(), request)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !launched.Enqueued || launched.Replayed {
		t.Fatalf("launch result = %+v", launched)
	}
	if launched.Session.Metadata["swarm_v3_workspace_binding_id"] != bindingID || launched.Session.Metadata["parent_session_id"] != "parent-session" || launched.Session.Metadata["lineage_kind"] != "session_deploy" {
		t.Fatalf("canonical launch metadata = %+v", launched.Session.Metadata)
	}
	waitForSessionsV3MessageCount(t, sessions, request.SessionID, 2)
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatal("deployed executor run did not drain")
	}
	intent, ok, err := sessions.GetSessionRunIntent(request.SessionID, request.RunID)
	if err != nil || !ok || intent.Status != sessionruntime.RunIntentCompleted {
		t.Fatalf("terminal intent = %+v ok=%t err=%v", intent, ok, err)
	}
	if runner.callCount != 1 {
		t.Fatalf("provider calls = %d, want 1", runner.callCount)
	}
	if active, ok, activeErr := sessions.GetSessionActiveRunIntent(request.SessionID); activeErr != nil || ok {
		t.Fatalf("completed deployment remains active: %+v ok=%t err=%v", active, ok, activeErr)
	}
	events, err := sessions.ListSessionEvents(request.SessionID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.EventType] = true
	}
	for _, eventType := range []string{"session.created", "session.message.appended", "session.assistant.started", "session.assistant.completed"} {
		if !seen[eventType] {
			t.Fatalf("missing %s in events %+v", eventType, events)
		}
	}
	outboxes, err := sessions.ListRealtimeOutboxForSessionAfterEndpoint(request.SessionID, 0, 50)
	if err != nil {
		t.Fatalf("list outboxes: %v", err)
	}
	if len(outboxes) < 4 {
		t.Fatalf("outboxes = %d, want canonical create/message/lifecycle records", len(outboxes))
	}

	replayed, err := server.LaunchV3Session(context.Background(), request)
	if err != nil {
		t.Fatalf("replay launch: %v", err)
	}
	if !replayed.Replayed || replayed.Enqueued {
		t.Fatalf("replay result = %+v", replayed)
	}
	if runner.callCount != 1 {
		t.Fatalf("replay duplicated provider execution: calls=%d", runner.callCount)
	}
	terminal, ok, err := sessions.GetSession(request.SessionID)
	if err != nil || !ok {
		t.Fatalf("load terminal deployment ok=%t err=%v", ok, err)
	}
	manageRuntime := toolruntime.NewRuntime(1)
	manageRuntime.SetManageSessionService(sessions)
	archiveOutput, archiveErr := manageRuntime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), toolruntime.WorkspaceScope{
		SessionID: "parent-session", PrimaryPath: workspacePath, Roots: []string{workspacePath}, Principal: testPrincipal(),
	}, toolruntime.Call{Name: "manage-sessions", Arguments: fmt.Sprintf(`{"action":"archive","session_id":%q,"expected_updated_at":%d}`, request.SessionID, terminal.UpdatedAt)})
	if archiveErr != nil || !strings.Contains(archiveOutput, request.SessionID) {
		t.Fatalf("normal archive preflight/output = %q err=%v", archiveOutput, archiveErr)
	}
	if tombstone, found, tombstoneErr := sessions.GetSessionTombstone(request.SessionID); tombstoneErr != nil || !found || !tombstone.Archived {
		t.Fatalf("archive tombstone = %+v found=%t err=%v", tombstone, found, tombstoneErr)
	}
}

func TestLaunchV3SessionExecutorRejectionPersistsTerminalIntent(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspacePath)
	binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, bindingID)
	if err != nil || !ok {
		t.Fatalf("load workspace binding ok=%t err=%v", ok, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exec := newSessionV3Executor(server)
	exec.ctx = ctx
	server.v3SessionExecutor = exec
	request := deployedSessionLaunchRequest(binding)
	_, err = server.LaunchV3Session(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "executor rejected") {
		t.Fatalf("launch error = %v", err)
	}
	intent, ok, getErr := sessions.GetSessionRunIntent(request.SessionID, request.RunID)
	if getErr != nil || !ok || intent.Status != sessionruntime.RunIntentFailed || !strings.Contains(intent.BlockedReason, "executor rejected") {
		t.Fatalf("rejected executor intent = %+v ok=%t err=%v", intent, ok, getErr)
	}
}

func TestLaunchV3SessionPendingIntentRemainsRecoverable(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspacePath)
	binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, bindingID)
	if err != nil || !ok {
		t.Fatalf("load workspace binding ok=%t err=%v", ok, err)
	}
	installSessionsV3TestProvider(server, "recovered response")
	blockingCtx, stopBlocking := context.WithCancel(context.Background())
	blocking := newSessionV3Executor(server)
	blocking.ctx = blockingCtx
	blocking.startDelay = time.Hour
	server.v3SessionExecutor = blocking
	request := deployedSessionLaunchRequest(binding)
	launched, err := server.LaunchV3Session(context.Background(), request)
	if err != nil || !launched.Enqueued {
		t.Fatalf("launch result = %+v err=%v", launched, err)
	}
	intent, ok, getErr := sessions.GetSessionRunIntent(request.SessionID, request.RunID)
	if getErr != nil || !ok || intent.Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("pending intent = %+v ok=%t err=%v", intent, ok, getErr)
	}
	stopBlocking()
	blocking.finish(sessionV3ExecutorJob{SessionID: request.SessionID, RunID: request.RunID})
	recovery := &sessionV3Executor{
		server: server, ctx: context.Background(), startDelay: 0,
		runningStaleAfter: sessionV3ExecutorDefaultRunningStaleAfter,
		inFlightRuns:      make(map[string]bool), activeBySession: make(map[string]string), runStates: make(map[string]*sessionV3ExecutorRunState),
	}
	server.v3SessionExecutor = recovery
	recovery.recoverDurableRuns(context.Background())
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatal("recovered deployment run did not drain")
	}
	intent, ok, getErr = sessions.GetSessionRunIntent(request.SessionID, request.RunID)
	if getErr != nil || !ok || intent.Status != sessionruntime.RunIntentCompleted {
		t.Fatalf("recovered terminal intent = %+v ok=%t err=%v", intent, ok, getErr)
	}
}

func TestLaunchV3SessionPreservesManagedWorktreeFacts(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspacePath)
	binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, bindingID)
	if err != nil || !ok {
		t.Fatalf("load workspace binding ok=%t err=%v", ok, err)
	}
	branch := "agent/deployed-worktree"
	fake := &fakeWorktreeService{allocation: worktreeruntime.Allocation{RepoRoot: workspacePath, WorkspacePath: filepath.Join(workspacePath, ".swarm", "worktrees", "deployed"), BaseBranch: "dev", BranchName: branch}}
	server.SetWorktreeService(fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exec := newSessionV3Executor(server)
	exec.ctx = ctx
	server.v3SessionExecutor = exec
	request := deployedSessionLaunchRequest(binding)
	request.ManagedWorktree = true
	request.WorktreeBaseBranch = "dev"
	request.WorktreeBranch = branch
	launched, err := server.LaunchV3Session(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "executor rejected") {
		t.Fatalf("launch error = %v", err)
	}
	if !launched.Session.WorktreeEnabled || launched.Session.WorktreeRootPath == "" || launched.Session.WorktreeRootPath == workspacePath || launched.Session.WorktreeBaseBranch != "dev" || launched.Session.WorktreeBranch != branch {
		t.Fatalf("managed worktree session = %+v", launched.Session)
	}
	if launched.Session.Metadata["swarm_v3_runtime_workspace_path"] != launched.Session.WorktreeRootPath || launched.Session.Metadata["workspace_id"] == "" {
		t.Fatalf("managed worktree metadata = %+v", launched.Session.Metadata)
	}
}

func deployedSessionLaunchRequest(binding pebblestore.TopologyWorkspaceBindingRecord) runruntime.V3SessionLaunchRequest {
	return runruntime.V3SessionLaunchRequest{
		Principal: testPrincipal(), SessionID: "deployed-session", RunID: "deployed-run",
		CreateClientRequestID: "deploy-create", MessageClientRequestID: "deploy-message", MessageID: "deployed-message",
		Title: "Deployed session", Prompt: "do deployed work", Mode: sessionruntime.ModeAuto, AgentName: "swarm",
		Preference:        pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
		SourceWorkspaceID: binding.SourceWorkspaceID, SourceWorkspaceGeneration: binding.SourceWorkspaceGeneration,
		SourceWorkspacePath: binding.SourceWorkspacePath, SourceWorkspaceName: binding.SourceWorkspaceName,
		WorkspaceBindingID: binding.BindingID, ParentSessionID: "parent-session",
		DeploymentManifestDigest: "manifest-digest", DeploymentProposalID: "proposal-1",
	}
}
