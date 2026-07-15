package run

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestParseManageSessionsDeployArgumentsRejectsTrustFields(t *testing.T) {
	for _, field := range []string{"agent_profile", "tool_contract", "runtime_mode", "worktree_path", "manifest_digest", "idempotency_key"} {
		_, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[{"prompt":"work","` + field + `":"untrusted"}]}`)
		if err == nil || !strings.Contains(err.Error(), "rejects untrusted field") {
			t.Fatalf("field %s: err = %v", field, err)
		}
	}
}

func TestParseManageSessionsDeployArgumentsBoundsAndModes(t *testing.T) {
	if _, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[]}`); err == nil {
		t.Fatal("empty proposals accepted")
	}
	if _, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[{"prompt":"work","mode":"read"}]}`); err == nil {
		t.Fatal("unsupported mode accepted")
	}
	parsed, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[{"prompt":"work"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 || parsed[0].Mode != "auto" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestValidateManageSessionsDeployAgentDefaultsAndGating(t *testing.T) {
	active := pebblestore.AgentProfile{Name: "swarm", Mode: agentruntime.ModePrimary, Enabled: true}
	if err := validateManageSessionsDeployAgent(active, active, false); err != nil {
		t.Fatalf("active primary rejected without delegation: %v", err)
	}
	alternate := pebblestore.AgentProfile{Name: "explorer", Mode: agentruntime.ModeSubagent, Enabled: true}
	if err := validateManageSessionsDeployAgent(active, alternate, false); err == nil || !strings.Contains(err.Error(), "requires calling primary") {
		t.Fatalf("alternate agent gating err = %v", err)
	}
	if err := validateManageSessionsDeployAgent(active, alternate, true); err != nil {
		t.Fatalf("delegated subagent rejected: %v", err)
	}
	background := pebblestore.AgentProfile{Name: "background", Mode: agentruntime.ModeBackground, Enabled: true}
	if err := validateManageSessionsDeployAgent(active, background, true); err == nil {
		t.Fatal("background agent accepted")
	}
}

func TestManageSessionsDeployDigestStableAndBound(t *testing.T) {
	manifest := manageSessionsDeployManifest{ManifestVersion: 1, Action: "deploy", ParentSessionID: "parent", AccountScopeID: "account", UserID: "user", Proposals: []manageSessionsDeployProposal{{ID: "proposal-1", Prompt: "work", Mode: "auto", AgentName: "swarm", WorkspaceID: "workspace", Selected: true}}}
	first, err := manageSessionsDeployDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestDigest = "ignored"
	manifest.ApprovedArguments = map[string]any{"ignored": true}
	manifest.AllowedWorkspaces = []manageSessionsDeployWorkspace{{ID: "workspace", Generation: 2, Path: "/workspace", Name: "Workspace"}}
	second, err := manageSessionsDeployDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("digest changed across envelope fields: %s != %s", first, second)
	}
	manifest.Proposals[0].Prompt = "different"
	third, err := manageSessionsDeployDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("digest did not bind proposal")
	}
}

func TestParseApprovedManageSessionsDeployRejectsEmptySelection(t *testing.T) {
	_, err := parseApprovedManageSessionsDeploy(`{"action":"deploy","manifest_version":1,"manifest_digest":"digest","selected_proposal_ids":[],"proposals":[{"id":"proposal-1","prompt":"work","mode":"auto","agent_name":"swarm"}]}`)
	if err == nil || !strings.Contains(err.Error(), "at least one selected") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveManageSessionsDeployBindingPathUsesSourceWorkspaceForNonWorktreeDeploy(t *testing.T) {
	parent := pebblestore.SessionSnapshot{WorkspacePath: "/managed/worktree", WorktreeEnabled: true, Metadata: map[string]any{"swarm_v3_source_workspace_path": "/bound/workspace"}}
	path, err := resolveManageSessionsDeployBindingPath(parent, manageSessionsDeployInput{Worktree: false})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/bound/workspace" {
		t.Fatalf("path = %q, want source workspace", path)
	}
	if explicit, err := resolveManageSessionsDeployBindingPath(parent, manageSessionsDeployInput{WorkspacePath: "/explicit", Worktree: false}); err != nil || explicit != "/explicit" {
		t.Fatalf("explicit path = %q, err = %v", explicit, err)
	}
}

func TestResolveManageSessionsDeployBindingPathNamesMissingSourceField(t *testing.T) {
	_, err := resolveManageSessionsDeployBindingPath(pebblestore.SessionSnapshot{WorkspacePath: "/managed/worktree", WorktreeEnabled: true}, manageSessionsDeployInput{Worktree: false})
	if err == nil || !strings.Contains(err.Error(), "swarm_v3_source_workspace_path") {
		t.Fatalf("err = %v", err)
	}
}

func TestCanonicalDeployWorktreeBranchUsesDesktopStylePrefixAndTitle(t *testing.T) {
	if got := canonicalDeployWorktreeBranch("agent/<id>", canonicalDeployWorktreeBranchSuffix("Test: 2-checkpoint plan exit", "session-1")); got != "agent/test-2-checkpoint-plan-exit" {
		t.Fatalf("branch = %q", got)
	}
	if got := canonicalDeployWorktreeBranch("", canonicalDeployWorktreeBranchSuffix("", "session-2")); got != "agent/session-2" {
		t.Fatalf("fallback branch = %q", got)
	}
}

func TestDeploySessionNavigationUsesActualSourceWorkspace(t *testing.T) {
	session := pebblestore.SessionSnapshot{ID: "session-1", WorkspacePath: "/data/worktrees/ws_fake", WorkspaceName: "ws_fake", Metadata: map[string]any{"swarm_v3_source_workspace_path": "/actual/workspace", "swarm_v3_source_workspace_name": "actual"}}
	navigation := deploySessionNavigation(session)
	if navigation["workspace_path"] != "/actual/workspace" || navigation["workspace_name"] != "actual" || navigation["href"] != "/actual/session-1" {
		t.Fatalf("navigation = %#v", navigation)
	}
}

func TestDeterministicDeployIDStableAndProposalBound(t *testing.T) {
	first := deterministicDeployID("digest", "proposal-1", "session")
	if first != deterministicDeployID("digest", "proposal-1", "session") {
		t.Fatal("deterministic deploy id changed")
	}
	if first == deterministicDeployID("digest", "proposal-2", "session") {
		t.Fatal("deterministic deploy id did not bind proposal")
	}
}

func TestPermissionRequirementAlwaysAsksForSessionDeploy(t *testing.T) {
	args := `{"action":"deploy","proposals":[{"prompt":"work"}]}`
	for _, mode := range []string{"plan", "auto", "auto+bypass_permissions"} {
		requirement, ask := permissionRequirement(mode, "manage-sessions", args)
		if requirement != "session_deploy" || !ask {
			t.Fatalf("mode %s = %q/%v", mode, requirement, ask)
		}
	}
}

type recordingV3SessionLauncher struct {
	requests []V3SessionLaunchRequest
	result   V3SessionLaunchResult
	err      error
}

func (f *recordingV3SessionLauncher) LaunchV3Session(_ context.Context, request V3SessionLaunchRequest) (V3SessionLaunchResult, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return V3SessionLaunchResult{}, f.err
	}
	result := f.result
	if result.Session.ID == "" {
		result.Session = pebblestore.SessionSnapshot{
			ID:            request.SessionID,
			Title:         request.Title,
			WorkspacePath: request.SourceWorkspacePath,
			WorkspaceName: request.SourceWorkspaceName,
			Metadata: map[string]any{
				"swarm_v3_source_workspace_path": request.SourceWorkspacePath,
				"swarm_v3_source_workspace_name": request.SourceWorkspaceName,
			},
		}
	}
	return result, nil
}

func TestExecuteManageSessionsDeployUsesCanonicalV3Launcher(t *testing.T) {
	svc, parentID, workspacePath, cleanup := newManageSessionsDeployExecutionTestService(t)
	defer cleanup()
	launcher := &recordingV3SessionLauncher{result: V3SessionLaunchResult{Enqueued: true}}
	svc.SetV3SessionLauncher(launcher)

	call := tool.Call{Name: "manage-sessions", Arguments: `{"action":"deploy","proposals":[{"title":"Canonical child","prompt":"do the work","mode":"auto"}]}`}
	manifest, err := svc.buildManageSessionsDeployManifest(parentID, call)
	if err != nil {
		t.Fatalf("build deployment manifest: %v", err)
	}
	approved, err := json.Marshal(manifest.ApprovedArguments)
	if err != nil {
		t.Fatalf("marshal approved deployment: %v", err)
	}
	output, err := svc.executeManageSessionsDeploy(context.Background(), parentID, call, string(approved), nil)
	if err != nil {
		t.Fatalf("execute deployment: %v", err)
	}
	if len(launcher.requests) != 1 {
		t.Fatalf("canonical launcher calls = %d, want 1", len(launcher.requests))
	}
	request := launcher.requests[0]
	if request.Prompt != "do the work" || request.Title != "Canonical child" || request.ParentSessionID != parentID {
		t.Fatalf("canonical launch request = %+v", request)
	}
	if request.SourceWorkspacePath != workspacePath || request.SourceWorkspaceID == "" || request.SourceWorkspaceGeneration == 0 || request.WorkspaceBindingID == "" {
		t.Fatalf("canonical workspace binding = %+v", request)
	}
	if request.CreateClientRequestID == "" || request.MessageClientRequestID == "" || request.MessageID == "" || request.RunID == "" || request.DeploymentManifestDigest == "" || request.DeploymentProposalID != "proposal-1" {
		t.Fatalf("canonical durable identity fields = %+v", request)
	}
	var response struct {
		Results []manageSessionsDeployResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode deployment output: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Status != "started" || response.Results[0].SessionID != request.SessionID {
		t.Fatalf("deployment output = %+v", response.Results)
	}
}

func TestExecuteManageSessionsDeploySurfacesCanonicalLauncherFailure(t *testing.T) {
	svc, parentID, _, cleanup := newManageSessionsDeployExecutionTestService(t)
	defer cleanup()
	launcher := &recordingV3SessionLauncher{err: errors.New("executor rejected committed run")}
	svc.SetV3SessionLauncher(launcher)
	call := tool.Call{Name: "manage-sessions", Arguments: `{"action":"deploy","proposals":[{"prompt":"do the work"}]}`}
	manifest, err := svc.buildManageSessionsDeployManifest(parentID, call)
	if err != nil {
		t.Fatalf("build deployment manifest: %v", err)
	}
	approved, _ := json.Marshal(manifest.ApprovedArguments)
	output, err := svc.executeManageSessionsDeploy(context.Background(), parentID, call, string(approved), nil)
	if err != nil {
		t.Fatalf("execute deployment: %v", err)
	}
	if len(launcher.requests) != 1 {
		t.Fatalf("canonical launcher calls = %d, want 1", len(launcher.requests))
	}
	if !strings.Contains(output, `"status":"error"`) || !strings.Contains(output, "executor rejected committed run") {
		t.Fatalf("deployment failure output = %s", output)
	}
}

func newManageSessionsDeployExecutionTestService(t *testing.T) (*Service, string, string, func()) {
	t.Helper()
	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	principal := testRunPrincipal()
	workspacePath := t.TempDir()
	if _, err := workspaceSvc.AddForPrincipal(principal, workspacePath, "Deploy workspace", "", true); err != nil {
		cleanup()
		t.Fatalf("add deployment workspace: %v", err)
	}
	events, err := pebblestore.NewEventLog(rawStore)
	if err != nil {
		cleanup()
		t.Fatalf("open event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(rawStore), events)
	if err := agents.EnsureDefaults(); err != nil {
		cleanup()
		t.Fatalf("ensure default agents: %v", err)
	}
	if err := agents.EnsureDefaultsForAccount(principal.AccountScopeID); err != nil {
		cleanup()
		t.Fatalf("ensure account agents: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(rawStore), events)
	parent, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		Title: "Deployment parent", WorkspacePath: workspacePath, WorkspaceName: "Deploy workspace",
		Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
	})
	if err != nil {
		cleanup()
		t.Fatalf("create deployment parent: %v", err)
	}
	svc := NewService(sessions, nil, nil, tool.NewRuntime(1), nil, agents, nil, events)
	svc.SetWorkspaceService(workspaceSvc)
	return svc, parent.ID, workspacePath, cleanup
}
