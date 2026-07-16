package run

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestParseTaskCallArgumentsRequiresExplicitLaunchAssignment(t *testing.T) {
	_, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"prompt":        "inspect the repo",
		"subagent_type": "explorer",
	}))
	if err == nil || !strings.Contains(err.Error(), "requires meta_prompt or role assignment") {
		t.Fatalf("expected missing assignment error, got %v", err)
	}
}

func TestParseTaskCallArgumentsRequiresExplicitLaunchAgent(t *testing.T) {
	_, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"prompt":      "inspect the repo",
		"meta_prompt": "map the relevant files",
	}))
	if err == nil || !strings.Contains(err.Error(), "requires subagent_type, agent, or purpose") {
		t.Fatalf("expected missing subagent error, got %v", err)
	}
}

func TestParseTaskCallArgumentsRequiresPerLaunchAgentAndAssignment(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "missing assignment",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{
					map[string]any{"subagent_type": "explorer"},
				},
			},
			want: "task launches[0] requires meta_prompt or role assignment",
		},
		{
			name: "missing agent",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{
					map[string]any{"meta_prompt": "map the relevant files"},
				},
			},
			want: "task launches[0] requires subagent_type, agent, or purpose",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTaskCallArguments(mustJSON(t, tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestParseTaskCallArgumentsRejectsLaunchTimeTrustFields(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: "top level allow_bash",
			args: map[string]any{
				"prompt":        "inspect the repo",
				"subagent_type": "explorer",
				"meta_prompt":   "map the relevant files",
				"allow_bash":    true,
			},
		},
		{
			name: "per launch execution setting",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{
					map[string]any{
						"subagent_type":     "explorer",
						"meta_prompt":       "map the relevant files",
						"execution_setting": "readwrite",
					},
				},
			},
		},
		{
			name: "per launch tool contract",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{
					map[string]any{
						"subagent_type": "explorer",
						"meta_prompt":   "map the relevant files",
						"tool_contract": map[string]any{"preset": "all"},
					},
				},
			},
		},
		{
			name: "per launch tool scope",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{
					map[string]any{
						"subagent_type": "explorer",
						"meta_prompt":   "map the relevant files",
						"tool_scope":    map[string]any{"allow_tools": []any{"bash"}},
					},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTaskCallArguments(mustJSON(t, tc.args))
			if err == nil || !strings.Contains(err.Error(), "cannot set launch-time trust, execution, or tool field") {
				t.Fatalf("expected trust-field rejection, got %v", err)
			}
		})
	}
}

func TestParseTaskCallArgumentsValidLaunches(t *testing.T) {
	parsed, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"description": "repo map",
		"prompt":      "inspect the repo",
		"launches": []any{
			map[string]any{"subagent_type": "explorer", "meta_prompt": "map backend files"},
			map[string]any{"agent": "clone", "role": "map frontend files", "deliverable": "frontend map", "concurrency_reason": "independent tree", "owned_scope": []any{"web/src/**"}, "dependency_evidence": "read-only mapping"},
		},
	}))
	if err != nil {
		t.Fatalf("parse valid launches: %v", err)
	}
	if len(parsed.Launches) != 2 {
		t.Fatalf("launch count = %d, want 2", len(parsed.Launches))
	}
	if parsed.Launches[0].RequestedSubagentType != "explorer" || parsed.Launches[0].MetaPrompt != "map backend files" {
		t.Fatalf("unexpected first launch: %#v", parsed.Launches[0])
	}
	if parsed.Launches[1].RequestedSubagentType != "clone" || parsed.Launches[1].MetaPrompt != "map frontend files" || parsed.Launches[1].Deliverable != "frontend map" || len(parsed.Launches[1].OwnedScope) != 1 {
		t.Fatalf("unexpected second launch: %#v", parsed.Launches[1])
	}
}

func TestTaskAssignmentLabelPreservesMoreTitleContext(t *testing.T) {
	label := taskAssignmentLabel("", "Write a quick poem about the sea with a bright moon and quiet tide", "", "memory")
	want := "Write a quick poem about the sea with a bright moon and quiet tide"
	if label != want {
		t.Fatalf("label = %q, want %q", label, want)
	}
	if strings.Contains(label, "...") {
		t.Fatalf("label should preserve 12-word title without ellipsis: %q", label)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(encoded)
}

func TestParseTaskCallArgumentsRejectsMalformedParameterMarkupBeforeLaunchValidation(t *testing.T) {
	_, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"description": "Test 2 — Valid multi-subagent launch",
		"prompt":      "Execute your assigned meta_prompt/role. </怡parameter>",
		"launches": []any{
			map[string]any{"subagent_type": "explorer", "meta_prompt": "Map top-level directories."},
		},
	}))
	if err == nil || !strings.Contains(err.Error(), "malformed XML markup in tool call") {
		t.Fatalf("expected malformed XML markup error, got %v", err)
	}
	if strings.Contains(err.Error(), "requires subagent_type, agent, or purpose") {
		t.Fatalf("expected parser-level error before launch validation, got %v", err)
	}
}

func TestPermissionArgumentsForCallRejectsMalformedTaskLaunch(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	_, err := svc.permissionArgumentsForCall(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description": "Test 2 — Valid multi-subagent launch",
			"prompt":      "Execute your assigned meta_prompt/role. </怡parameter>",
			"launches": []any{
				map[string]any{"subagent_type": "reviewer", "meta_prompt": "Map top-level directories."},
			},
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "malformed XML markup in tool call") {
		t.Fatalf("expected malformed XML markup error, got %v", err)
	}
}

func TestPermissionArgumentsForCallFormatsValidTaskLaunchManifest(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	formatted, err := svc.permissionArgumentsForCall(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description": "repo map",
			"prompt":      "inspect the repo",
			"launches": []any{
				map[string]any{"subagent_type": "reviewer", "meta_prompt": "map backend files"},
				map[string]any{"purpose": "purpose-review", "role": "review frontend files"},
			},
		}),
	})
	if err != nil {
		t.Fatalf("format permission arguments: %v", err)
	}
	var manifest taskLaunchManifest
	if err := json.Unmarshal([]byte(formatted), &manifest); err != nil {
		t.Fatalf("formatted manifest json invalid: %v", err)
	}
	if manifest.PathID != taskLaunchPermissionPathID {
		t.Fatalf("path id = %q, want %q", manifest.PathID, taskLaunchPermissionPathID)
	}
	if manifest.LaunchCount != 2 || len(manifest.Launches) != 2 {
		t.Fatalf("launch count = %d len=%d, want 2", manifest.LaunchCount, len(manifest.Launches))
	}
	if manifest.Launches[0].MetaPrompt != "map backend files" || manifest.Launches[1].MetaPrompt != "review frontend files" {
		t.Fatalf("unexpected launch assignments: %#v", manifest.Launches)
	}
	if manifest.Launches[0].ResolvedAgentName == "" || manifest.Launches[0].SubagentProvider == "" || manifest.Launches[0].SubagentModel == "" || manifest.Launches[0].ResolvedTools == nil {
		t.Fatalf("first launch missing resolved display data: %#v", manifest.Launches[0])
	}
}

func TestGateToolCallsRejectsMalformedTaskLaunchBeforeApproval(t *testing.T) {
	svc, parentSessionID, permissions, cleanup := newTaskLaunchPermissionServiceWithPermissions(t)
	defer cleanup()

	results, approvedCalls, _, approvedMask, _, err := svc.gateToolCalls(context.Background(), parentSessionID, "run-test", 1, sessionruntime.ModeAuto, []tool.Call{{
		CallID: "call-bad-task",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"description": "Test 2 — Valid multi-subagent launch",
			"prompt":      "Execute your assigned meta_prompt/role. </怡parameter>",
			"launches": []any{
				map[string]any{"subagent_type": "reviewer", "meta_prompt": "Map top-level directories."},
			},
		}),
	}}, nil, nil)
	if err != nil {
		t.Fatalf("gate tool calls returned unexpected error: %v", err)
	}
	if len(approvedCalls) != 0 || len(approvedMask) != 1 || approvedMask[0] {
		t.Fatalf("malformed task launch was approved: calls=%d mask=%v", len(approvedCalls), approvedMask)
	}
	if len(results) != 1 || !strings.Contains(results[0].Error, "invalid tool arguments") || !strings.Contains(results[0].Error, "malformed XML markup in tool call") {
		t.Fatalf("expected parser-level result error, got %#v", results)
	}
	if strings.Contains(results[0].Error, "requires subagent_type, agent, or purpose") {
		t.Fatalf("expected parser-level error before launch validation, got %#v", results)
	}
	pending, err := permissions.ListPending(parentSessionID, 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending permission records, got %#v", pending)
	}
}

func TestBuildTaskLaunchPermissionPayloadRequiresResolvableSavedAgent(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	_, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description":   "repo map",
			"prompt":        "inspect the repo",
			"subagent_type": "missing-agent",
			"meta_prompt":   "map backend files",
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot resolve subagent \"missing-agent\"") {
		t.Fatalf("expected missing saved-agent resolution error, got %v", err)
	}
}

func TestBuildTaskLaunchPermissionPayloadRejectsDisabledSavedAgent(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	_, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description":   "repo map",
			"prompt":        "inspect the repo",
			"subagent_type": "disabled-subagent",
			"meta_prompt":   "map backend files",
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "agent \"disabled-subagent\" is disabled") {
		t.Fatalf("expected disabled saved-agent error, got %v", err)
	}
}

func TestBuildTaskLaunchPermissionPayloadUsesResolvedSavedAgentPreference(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	manifest, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description":   "repo map",
			"prompt":        "inspect the repo",
			"subagent_type": "purpose-review",
			"meta_prompt":   "map backend files",
		}),
	})
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if manifest.ResolvedAgentName != "reviewer" {
		t.Fatalf("resolved agent = %q, want reviewer", manifest.ResolvedAgentName)
	}
	if manifest.ResolvedAgentError != "" {
		t.Fatalf("resolved agent error = %q, want empty", manifest.ResolvedAgentError)
	}
	if len(manifest.Launches) != 1 {
		t.Fatalf("launch count = %d, want 1", len(manifest.Launches))
	}
	row := manifest.Launches[0]
	if row.RequestedSubagentType != "purpose-review" || row.ResolvedAgentName != "reviewer" {
		t.Fatalf("unexpected launch resolution: %#v", row)
	}
	if row.ResolvedAgentError != "" {
		t.Fatalf("row resolved agent error = %q, want empty", row.ResolvedAgentError)
	}
	if row.SubagentProvider != "static" || row.SubagentModel != "review-model" {
		t.Fatalf("row preference = %q/%q, want static/review-model", row.SubagentProvider, row.SubagentModel)
	}
	if got := row.Capabilities["allow_bash"]; got != false {
		t.Fatalf("allow_bash capability = %#v, want false", got)
	}
}

func TestBuildTaskLaunchPermissionPayloadIncludesResolvedToolSummary(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	manifest, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description":   "repo map",
			"prompt":        "inspect the repo",
			"subagent_type": "reviewer",
			"meta_prompt":   "map backend files",
		}),
	})
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if manifest.ResolvedTools == nil {
		t.Fatalf("manifest resolved tools is nil")
	}
	if manifest.ResolvedTools.Preset != "bash_git_only" {
		t.Fatalf("manifest resolved tool preset = %q, want bash_git_only", manifest.ResolvedTools.Preset)
	}
	if len(manifest.Launches) != 1 || manifest.Launches[0].ResolvedTools == nil {
		t.Fatalf("launch resolved tools missing: %#v", manifest.Launches)
	}
	tools := manifest.Launches[0].ResolvedTools
	if tools.Preset != "bash_git_only" {
		t.Fatalf("preset = %q, want bash_git_only", tools.Preset)
	}
	if tools.RuntimeMode != pebblestore.AgentExecutionSettingReadWrite {
		t.Fatalf("runtime mode = %q, want readwrite", tools.RuntimeMode)
	}
	if tools.EffectiveExecutionMode != pebblestore.AgentExecutionSettingReadWrite {
		t.Fatalf("effective execution mode = %q, want readwrite", tools.EffectiveExecutionMode)
	}
	for _, want := range []string{"read", "search", "list", "skill_use"} {
		if !stringSliceContains(tools.AllowedTools, want) {
			t.Fatalf("allowed tools %v missing %q", tools.AllowedTools, want)
		}
	}
	for _, want := range []string{"bash", "task", "plan_manage", "exit_plan_mode"} {
		if !stringSliceContains(tools.DisabledTools, want) {
			t.Fatalf("disabled tools %v missing %q", tools.DisabledTools, want)
		}
	}
	for _, want := range []string{"git status", "git diff --"} {
		if !stringSliceContains(tools.BashPrefixes, want) {
			t.Fatalf("bash prefixes %v missing %q", tools.BashPrefixes, want)
		}
	}
	if stringSliceContains(tools.AllowedTools, "bash") {
		t.Fatalf("allowed tools include bash despite task launch disabled overlay: %v", tools.AllowedTools)
	}
}

type taskLaunchWorktreeStub struct {
	allocations int
	allocation  worktreeruntime.Allocation
	taskBase    worktreeruntime.TaskBase
}

func (s *taskLaunchWorktreeStub) AttachBranch(_, _, _ string) (string, error) { return "", nil }

func (s *taskLaunchWorktreeStub) ResolveTaskBase(_ string) (worktreeruntime.TaskBase, error) {
	if strings.TrimSpace(s.taskBase.BaseCommit) == "" {
		return worktreeruntime.TaskBase{RepoRoot: "/repo", ParentBranch: "dev", BaseCommit: "base-commit"}, nil
	}
	return s.taskBase, nil
}

func (s *taskLaunchWorktreeStub) AllocateTaskWorkspace(_ string, _ worktreeruntime.TaskBase, _ string) (worktreeruntime.Allocation, error) {
	s.allocations++
	return s.allocation, nil
}

func (s *taskLaunchWorktreeStub) TaskCommitDescendsFrom(_, baseCommit, headCommit string) (bool, error) {
	return strings.TrimSpace(baseCommit) != "" && strings.TrimSpace(headCommit) != "" && baseCommit != headCommit, nil
}

func (s *taskLaunchWorktreeStub) InspectTaskWorkspace(path string) (worktreeruntime.TaskWorkspaceState, error) {
	return worktreeruntime.TaskWorkspaceState{WorkspacePath: path, BranchName: s.allocation.BranchName, HeadCommit: s.taskBase.BaseCommit, Clean: true}, nil
}

func (s *taskLaunchWorktreeStub) GetConfigForPrincipal(_ identity.Principal, workspacePath string) (worktreeruntime.Config, error) {
	return worktreeruntime.Config{WorkspacePath: workspacePath, BranchName: "agent"}, nil
}

func (s *taskLaunchWorktreeStub) AllocateDetachedWorkspaceRequestedForPrincipal(_ identity.Principal, _, _, _, _ string) (worktreeruntime.Allocation, error) {
	s.allocations++
	return s.allocation, nil
}

func TestApprovedExplorerInheritsParentWorktreeScopeWithoutAllocation(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(parentSessionID)
	if err != nil || !ok {
		t.Fatalf("load parent: ok=%v err=%v", ok, err)
	}
	parent.WorktreeEnabled = true
	parent.WorktreeRootPath = parent.WorkspacePath
	parent.WorktreeBaseBranch = "dev"
	parent.WorktreeBranch = "agent/parent"
	temporaryRoot := t.TempDir()
	parent.TemporaryWorkspaceRoots = []string{temporaryRoot}
	stub := &taskLaunchWorktreeStub{allocation: worktreeruntime.Allocation{WorkspacePath: t.TempDir()}}
	svc.SetWorktreeService(stub)
	profile, virtual, source, err := svc.resolveTaskLaunchProfile(parent, "explorer")
	if err != nil || virtual {
		t.Fatalf("resolve Explorer profile: virtual=%t source=%q err=%v", virtual, source, err)
	}
	launch, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, sessionruntime.ModeAuto, taskLaunchPrepared{
		LaunchIndex: 1, RequestedSubagent: "explorer", MetaPrompt: "inspect parent", VirtualTarget: virtual,
	}, "inspect", "", &profile, source, nil)
	if err != nil {
		t.Fatalf("prepare approved Explorer: %v", err)
	}
	child := launch.ChildSession
	if stub.allocations != 0 {
		t.Fatalf("Explorer allocated %d worktrees, want 0", stub.allocations)
	}
	if child.WorkspacePath != parent.WorkspacePath || child.WorktreeRootPath != parent.WorktreeRootPath || !child.WorktreeEnabled {
		t.Fatalf("Explorer worktree facts = %#v, want inherited from %#v", child, parent)
	}
	assertStringSliceContains(t, child.TemporaryWorkspaceRoots, temporaryRoot)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "test-user", AccountScopeID: parent.AccountScopeID, SessionID: parent.ID, AccountScopeSource: identity.AccountScopeSourceSession}
	scope, err := svc.resolveRunWorkspaceScope(child, principal)
	if err != nil {
		t.Fatalf("resolve Explorer scope: %v", err)
	}
	if _, needsExpansion, err := tool.ScopeExpansionForCall(scope, tool.Call{Name: "read", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(parent.WorkspacePath, "README.md")})}); err != nil || needsExpansion {
		t.Fatalf("parent worktree read expansion: needed=%t err=%v scope=%#v", needsExpansion, err, scope)
	}
}

func TestApprovedCloneAllocatesIsolatedWorktreeScope(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(parentSessionID)
	if err != nil || !ok {
		t.Fatalf("load parent: ok=%v err=%v", ok, err)
	}
	parent.WorktreeEnabled = true
	parent.WorktreeRootPath = parent.WorkspacePath
	parent.WorktreeBaseBranch = "dev"
	parent.TemporaryWorkspaceRoots = []string{t.TempDir()}
	clonePath := t.TempDir()
	stub := &taskLaunchWorktreeStub{allocation: worktreeruntime.Allocation{WorkspacePath: clonePath, RepoRoot: filepath.Dir(clonePath), BaseBranch: "dev", BranchName: "agent/clone", WorkspaceID: "clone-workspace"}}
	svc.SetWorktreeService(stub)
	profile, virtual, source, err := svc.resolveTaskLaunchProfile(parent, "clone")
	if err != nil || !virtual {
		t.Fatalf("resolve Clone profile: virtual=%t source=%q err=%v", virtual, source, err)
	}
	taskBase, err := stub.ResolveTaskBase(parent.WorkspacePath)
	if err != nil {
		t.Fatalf("resolve task base: %v", err)
	}
	launch, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, sessionruntime.ModeAuto, taskLaunchPrepared{
		LaunchIndex: 1, RequestedSubagent: "clone", MetaPrompt: "implement", VirtualTarget: virtual, TaskBase: &taskBase,
	}, "implement", "", &profile, source, nil)
	if err != nil {
		t.Fatalf("prepare approved Clone: %v", err)
	}
	child := launch.ChildSession
	if stub.allocations != 1 || child.WorkspacePath != clonePath || child.WorktreeRootPath != clonePath || !child.WorktreeEnabled {
		t.Fatalf("Clone isolation facts: allocations=%d child=%#v", stub.allocations, child)
	}
	if len(child.TemporaryWorkspaceRoots) != 0 {
		t.Fatalf("Clone inherited temporary roots: %v", child.TemporaryWorkspaceRoots)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "test-user", AccountScopeID: parent.AccountScopeID, SessionID: parent.ID, AccountScopeSource: identity.AccountScopeSourceSession}
	scope, err := svc.resolveRunWorkspaceScope(child, principal)
	if err != nil {
		t.Fatalf("resolve Clone scope: %v", err)
	}
	if _, needsExpansion, err := tool.ScopeExpansionForCall(scope, tool.Call{Name: "read", Arguments: mustJSON(t, map[string]any{"path": parent.WorkspacePath})}); err != nil || !needsExpansion {
		t.Fatalf("parent worktree read from Clone: needed=%t err=%v scope=%#v", needsExpansion, err, scope)
	}
}

func TestPrepareDelegatedSubagentLaunchCreatesCanonicalV3ChildSession(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	const accountScopeID = "account-cp7"
	const userID = "user-cp7"
	if _, _, _, err := svc.agents.UpsertForAccount(accountScopeID, agentruntime.UpsertInput{
		Name:                "reviewer",
		Mode:                agentruntime.ModeSubagent,
		Description:         "Review specialist",
		Provider:            "static",
		Model:               "review-model",
		Prompt:              "Review carefully.",
		RuntimeMode:         pebblestore.AgentRuntimeModeReadWrite,
		ExecutionSetting:    pebblestore.AgentExecutionSettingReadWrite,
		ExitPlanModeEnabled: pebblestore.BoolPtr(false),
		ToolContract:        &pebblestore.AgentToolContract{Preset: "read_write"},
		Enabled:             pebblestore.BoolPtr(true),
	}); err != nil {
		t.Fatalf("create account-scoped reviewer: %v", err)
	}
	if _, _, _, err := svc.agents.SetActiveSubagentForAccount(accountScopeID, "purpose-review", "reviewer"); err != nil {
		t.Fatalf("set account-scoped active subagent: %v", err)
	}
	parent, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         userID,
		AccountScopeID: accountScopeID,
		Title:          "Parent",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "parent-provider",
			Model:    "parent-model",
			Thinking: "high",
		},
	})
	if err != nil {
		t.Fatalf("create account-scoped parent session: %v", err)
	}

	var captured []sessionruntime.SessionMutationInput
	apply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		captured = append(captured, input)
		return svc.sessions.ApplySessionMutation(input)
	}
	launch, err := svc.prepareDelegatedSubagentLaunch(parent, sessionruntime.ModeAuto, taskLaunchPrepared{
		LaunchIndex:       7,
		RequestedSubagent: "purpose-review",
		MetaPrompt:        "Map backend files",
	}, "repo map", "", apply)
	if err != nil {
		t.Fatalf("prepare delegated launch: %v", err)
	}

	childID := strings.TrimSpace(launch.ChildSession.ID)
	if childID == "" {
		t.Fatalf("child session id is empty")
	}
	if childID == parent.ID {
		t.Fatalf("child session reused parent id %q", childID)
	}
	if strings.Contains(childID, "/") || strings.Contains(childID, ":") {
		t.Fatalf("child session id %q is not a raw canonical session id", childID)
	}
	if len(captured) != 1 {
		t.Fatalf("captured create mutations = %d, want 1", len(captured))
	}
	if got := captured[0].Kind; got != sessionruntime.SessionMutationCreateSession {
		t.Fatalf("captured mutation kind = %q, want create session", got)
	}
	if captured[0].Session == nil || captured[0].Session.ID != childID {
		t.Fatalf("captured mutation session = %#v, want child %q", captured[0].Session, childID)
	}
	child, ok, err := svc.sessions.GetSession(childID)
	if err != nil {
		t.Fatalf("load child session: %v", err)
	}
	if !ok {
		t.Fatalf("child session %q was not persisted", childID)
	}
	if child.ID != launch.ChildSession.ID {
		t.Fatalf("persisted child id = %q, launch id = %q", child.ID, launch.ChildSession.ID)
	}
	if child.Mode != sessionruntime.ModeAuto || launch.ChildMode != sessionruntime.ModeAuto {
		t.Fatalf("child mode = %q launch mode = %q, want auto", child.Mode, launch.ChildMode)
	}
	if child.AccountScopeID != parent.AccountScopeID {
		t.Fatalf("child account scope = %q, want parent account scope %q", child.AccountScopeID, parent.AccountScopeID)
	}
	if child.UserID != parent.UserID {
		t.Fatalf("child user id = %q, want parent user id %q", child.UserID, parent.UserID)
	}
	if child.WorkspacePath != parent.WorkspacePath || child.WorkspaceName != parent.WorkspaceName {
		t.Fatalf("child workspace = %q/%q, want parent workspace %q/%q", child.WorkspacePath, child.WorkspaceName, parent.WorkspacePath, parent.WorkspaceName)
	}
	if child.Preference.Provider != "static" || child.Preference.Model != "review-model" {
		t.Fatalf("child preference = %q/%q, want static/review-model", child.Preference.Provider, child.Preference.Model)
	}

	metadata := child.Metadata
	checks := map[string]string{
		"parent_session_id":  parent.ID,
		"parent_title":       parent.Title,
		"lineage_kind":       "delegated_subagent",
		"lineage_label":      "@reviewer",
		"launch_source":      "task",
		"requested_subagent": "purpose-review",
		"subagent":           "reviewer",
		"assignment_label":   "Map backend files",
		"subagent_provider":  "static",
		"subagent_model":     "review-model",
		"runtime_state":      "standby",
	}
	for key, want := range checks {
		if got := strings.TrimSpace(metadataStringForTest(metadata, key)); got != want {
			t.Fatalf("metadata[%q] = %q, want %q; metadata=%#v", key, got, want, metadata)
		}
	}
	if got, ok := metadata["title_locked"].(bool); !ok || !got {
		t.Fatalf("metadata[title_locked] = %#v, want true", metadata["title_locked"])
	}
	if got, ok := metadata["title_pending"].(bool); !ok || got {
		t.Fatalf("metadata[title_pending] = %#v, want false", metadata["title_pending"])
	}
	if got := metadataStringForTest(metadata, "launch_index"); got != "7" {
		t.Fatalf("metadata[launch_index] = %q, want 7", got)
	}
	if got := strings.TrimSpace(metadataStringForTest(metadata, "workspace_id")); got == "" {
		t.Fatalf("metadata[workspace_id] is empty")
	}

	hydrated, ok, err := svc.sessions.HydrateSessionSnapshot(childID, 500, 500)
	if err != nil {
		t.Fatalf("hydrate child session through V3 snapshot path: %v", err)
	}
	if !ok {
		t.Fatalf("child session %q not found through V3 snapshot path", childID)
	}
	if hydrated.Session.ID != childID {
		t.Fatalf("hydrated session id = %q, want %q", hydrated.Session.ID, childID)
	}
	if got := strings.TrimSpace(metadataStringForTest(hydrated.Session.Metadata, "parent_session_id")); got != parent.ID {
		t.Fatalf("hydrated parent_session_id = %q, want %q", got, parent.ID)
	}
	if len(hydrated.Events) != 1 || hydrated.Events[0].EventType != "session.created" || hydrated.Events[0].SessionID != childID {
		t.Fatalf("hydrated V3 events = %#v, want single child session.created", hydrated.Events)
	}
	var createdPayload struct {
		Session *pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(hydrated.Events[0].Payload, &createdPayload); err != nil {
		t.Fatalf("decode V3 session.created payload: %v", err)
	}
	if createdPayload.Session == nil || createdPayload.Session.ID != childID {
		t.Fatalf("V3 session.created payload session = %#v, want child %q", createdPayload.Session, childID)
	}
	if got := strings.TrimSpace(metadataStringForTest(createdPayload.Session.Metadata, "parent_session_id")); got != parent.ID {
		t.Fatalf("V3 session.created parent_session_id = %q, want %q", got, parent.ID)
	}
	if got := strings.TrimSpace(metadataStringForTest(createdPayload.Session.Metadata, "lineage_kind")); got != "delegated_subagent" {
		t.Fatalf("V3 session.created lineage_kind = %q, want delegated_subagent", got)
	}
	outbox, err := svc.sessions.ListRealtimeOutboxForSessionAfterEndpoint(childID, 0, 10)
	if err != nil {
		t.Fatalf("list child realtime outbox: %v", err)
	}
	if len(outbox) != 1 || outbox[0].Event.EventType != "session.created" || outbox[0].SessionID != childID || outbox[0].EndpointSeq == 0 {
		t.Fatalf("child realtime outbox = %#v, want durable session.created outbox row", outbox)
	}
}

func TestPrepareDelegatedSubagentLaunchUsesSplitProfilePlanSettingsInPlanMode(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	const accountScopeID = "account-split-plan"
	const userID = "user-split-plan"
	if _, _, _, err := svc.agents.UpsertForAccount(accountScopeID, agentruntime.UpsertInput{
		Name:                "split-reviewer",
		Mode:                agentruntime.ModeSubagent,
		Description:         "Split review specialist",
		ModelMode:           "split",
		PlanProvider:        "fireworks",
		PlanModel:           "accounts/fireworks/models/glm-5p1",
		PlanThinking:        "high",
		PlanServiceTier:     "priority",
		AutoProvider:        "static",
		AutoModel:           "auto-review-model",
		AutoThinking:        "low",
		Prompt:              "Review according to mode.",
		RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ToolContract:        &pebblestore.AgentToolContract{Preset: "read_only"},
		Enabled:             pebblestore.BoolPtr(true),
	}); err != nil {
		t.Fatalf("create account-scoped split reviewer: %v", err)
	}
	if _, _, _, err := svc.agents.SetActiveSubagentForAccount(accountScopeID, "purpose-split", "split-reviewer"); err != nil {
		t.Fatalf("set account-scoped active split subagent: %v", err)
	}
	parent, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         userID,
		AccountScopeID: accountScopeID,
		Title:          "Parent",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModePlan,
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create plan parent session: %v", err)
	}

	launch, err := svc.prepareDelegatedSubagentLaunch(parent, sessionruntime.ModePlan, taskLaunchPrepared{
		LaunchIndex:       1,
		RequestedSubagent: "purpose-split",
		MetaPrompt:        "Review backend files",
	}, "split review", "", nil)
	if err != nil {
		t.Fatalf("prepare delegated launch: %v", err)
	}

	if launch.ChildMode != sessionruntime.ModePlan || launch.ChildSession.Mode != sessionruntime.ModePlan {
		t.Fatalf("child mode = %q session mode = %q, want plan", launch.ChildMode, launch.ChildSession.Mode)
	}
	if got := launch.ChildSession.Preference.Provider; got != "fireworks" {
		t.Fatalf("child provider = %q, want fireworks", got)
	}
	if got := launch.ChildSession.Preference.Model; got != "accounts/fireworks/models/glm-5p1" {
		t.Fatalf("child model = %q, want accounts/fireworks/models/glm-5p1", got)
	}
	if got := launch.ChildSession.Preference.Thinking; got != "high" {
		t.Fatalf("child thinking = %q, want high", got)
	}
	if got := launch.ChildSession.Preference.ServiceTier; got != "priority" {
		t.Fatalf("child service tier = %q, want priority", got)
	}
	if launch.SubagentProvider != "fireworks" || launch.SubagentModel != "accounts/fireworks/models/glm-5p1" {
		t.Fatalf("launch display preference = %q/%q, want plan profile settings", launch.SubagentProvider, launch.SubagentModel)
	}
	child, ok, err := svc.sessions.GetSession(launch.ChildSession.ID)
	if err != nil {
		t.Fatalf("load child session: %v", err)
	}
	if !ok {
		t.Fatalf("child session %q was not persisted", launch.ChildSession.ID)
	}
	if child.Mode != sessionruntime.ModePlan {
		t.Fatalf("persisted child mode = %q, want plan", child.Mode)
	}
	if child.Preference.Provider != "fireworks" || child.Preference.Model != "accounts/fireworks/models/glm-5p1" || child.Preference.ServiceTier != "priority" {
		t.Fatalf("persisted child preference = %#v, want plan split profile settings", child.Preference)
	}
}

func TestBuildTaskLaunchPermissionPayloadUsesSplitProfilePlanSettingsInPlanMode(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	const accountScopeID = "account-split-manifest"
	if _, _, _, err := svc.agents.UpsertForAccount(accountScopeID, agentruntime.UpsertInput{
		Name:                "split-manifest-reviewer",
		Mode:                agentruntime.ModeSubagent,
		Description:         "Split manifest specialist",
		ModelMode:           "split",
		PlanProvider:        "fireworks",
		PlanModel:           "accounts/fireworks/models/glm-5p1",
		PlanThinking:        "high",
		PlanServiceTier:     "priority",
		AutoProvider:        "static",
		AutoModel:           "auto-review-model",
		AutoThinking:        "low",
		Prompt:              "Review according to mode.",
		RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ToolContract:        &pebblestore.AgentToolContract{Preset: "read_only"},
		Enabled:             pebblestore.BoolPtr(true),
	}); err != nil {
		t.Fatalf("create account-scoped split manifest reviewer: %v", err)
	}
	if _, _, _, err := svc.agents.SetActiveSubagentForAccount(accountScopeID, "purpose-split-manifest", "split-manifest-reviewer"); err != nil {
		t.Fatalf("set account-scoped active split manifest subagent: %v", err)
	}
	parent, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         "user-split-manifest",
		AccountScopeID: accountScopeID,
		Title:          "Parent",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModePlan,
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create manifest parent session: %v", err)
	}

	manifest, err := svc.buildTaskLaunchPermissionPayload(parent.ID, sessionruntime.ModePlan, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description":   "repo map",
			"prompt":        "inspect the repo",
			"subagent_type": "purpose-split-manifest",
			"meta_prompt":   "map backend files",
		}),
	})
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if manifest.EffectiveChildMode != sessionruntime.ModePlan {
		t.Fatalf("manifest child mode = %q, want plan", manifest.EffectiveChildMode)
	}
	if len(manifest.Launches) != 1 {
		t.Fatalf("launch count = %d, want 1", len(manifest.Launches))
	}
	row := manifest.Launches[0]
	if row.ChildMode != sessionruntime.ModePlan {
		t.Fatalf("row child mode = %q, want plan", row.ChildMode)
	}
	if row.SubagentProvider != "fireworks" || row.SubagentModel != "accounts/fireworks/models/glm-5p1" {
		t.Fatalf("row preference = %q/%q, want plan split profile settings", row.SubagentProvider, row.SubagentModel)
	}
}

func TestPrepareDelegatedSubagentLaunchPreservesSupportedPriorityServiceTier(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	const accountScopeID = "account-priority"
	const userID = "user-priority"
	if _, _, _, err := svc.agents.UpsertForAccount(accountScopeID, agentruntime.UpsertInput{
		Name:                "priority-reviewer",
		Mode:                agentruntime.ModeSubagent,
		Description:         "Priority review specialist",
		Provider:            "fireworks",
		Model:               "accounts/fireworks/models/glm-5p1",
		Prompt:              "Review quickly.",
		RuntimeMode:         pebblestore.AgentRuntimeModeReadWrite,
		ExecutionSetting:    pebblestore.AgentExecutionSettingReadWrite,
		ExitPlanModeEnabled: pebblestore.BoolPtr(false),
		ToolContract:        &pebblestore.AgentToolContract{Preset: "read_write"},
		Enabled:             pebblestore.BoolPtr(true),
	}); err != nil {
		t.Fatalf("create account-scoped priority reviewer: %v", err)
	}
	if _, _, _, err := svc.agents.SetActiveSubagentForAccount(accountScopeID, "purpose-priority", "priority-reviewer"); err != nil {
		t.Fatalf("set account-scoped active subagent: %v", err)
	}
	parent, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         userID,
		AccountScopeID: accountScopeID,
		Title:          "Parent",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider:    "codex",
			Model:       "gpt-5.4",
			Thinking:    "high",
			ServiceTier: "priority",
			ContextMode: "1m",
		},
	})
	if err != nil {
		t.Fatalf("create account-scoped parent session: %v", err)
	}

	launch, err := svc.prepareDelegatedSubagentLaunch(parent, sessionruntime.ModeAuto, taskLaunchPrepared{
		LaunchIndex:       1,
		RequestedSubagent: "purpose-priority",
		MetaPrompt:        "Review backend files",
	}, "priority review", "", nil)
	if err != nil {
		t.Fatalf("prepare delegated launch: %v", err)
	}

	if got := launch.ChildSession.Preference.ServiceTier; got != "priority" {
		t.Fatalf("launch child service tier = %q, want priority", got)
	}
	if got := launch.ChildSession.Preference.ContextMode; got != "" {
		t.Fatalf("launch child context mode = %q, want cleared for non-codex/gpt-5.4", got)
	}
	child, ok, err := svc.sessions.GetSession(launch.ChildSession.ID)
	if err != nil {
		t.Fatalf("load child session: %v", err)
	}
	if !ok {
		t.Fatalf("child session %q was not persisted", launch.ChildSession.ID)
	}
	if got := child.Preference.ServiceTier; got != "priority" {
		t.Fatalf("persisted child service tier = %q, want priority", got)
	}
	if got := child.Preference.ContextMode; got != "" {
		t.Fatalf("persisted child context mode = %q, want cleared for non-codex/gpt-5.4", got)
	}
}

func TestPrepareTargetedSubagentLaunchPreservesSupportedPriorityServiceTier(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	const accountScopeID = "account-targeted-priority"
	const userID = "user-targeted-priority"
	if _, _, _, err := svc.agents.UpsertForAccount(accountScopeID, agentruntime.UpsertInput{
		Name:                "targeted-priority-reviewer",
		Mode:                agentruntime.ModeSubagent,
		Description:         "Targeted priority review specialist",
		Provider:            "fireworks",
		Model:               "accounts/fireworks/models/glm-5p1",
		Prompt:              "Review targeted work quickly.",
		RuntimeMode:         pebblestore.AgentRuntimeModeReadWrite,
		ExecutionSetting:    pebblestore.AgentExecutionSettingReadWrite,
		ExitPlanModeEnabled: pebblestore.BoolPtr(false),
		ToolContract:        &pebblestore.AgentToolContract{Preset: "read_write"},
		Enabled:             pebblestore.BoolPtr(true),
	}); err != nil {
		t.Fatalf("create account-scoped targeted priority reviewer: %v", err)
	}
	parent, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         userID,
		AccountScopeID: accountScopeID,
		Title:          "Parent",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider:    "codex",
			Model:       "gpt-5.4",
			Thinking:    "high",
			ServiceTier: "priority",
			ContextMode: "1m",
		},
	})
	if err != nil {
		t.Fatalf("create account-scoped parent session: %v", err)
	}

	launch, err := svc.prepareDelegatedSubagentLaunch(parent, sessionruntime.ModeAuto, taskLaunchPrepared{
		LaunchIndex:       1,
		RequestedSubagent: "targeted-priority-reviewer",
	}, "@targeted-priority-reviewer priority review", "targeted-priority-reviewer", nil)
	if err != nil {
		t.Fatalf("prepare targeted launch: %v", err)
	}

	if got := launch.ChildSession.Preference.ServiceTier; got != "priority" {
		t.Fatalf("launch child service tier = %q, want priority", got)
	}
	if got := launch.ChildSession.Preference.ContextMode; got != "" {
		t.Fatalf("launch child context mode = %q, want cleared for non-codex/gpt-5.4", got)
	}
	if got := metadataStringForTest(launch.ChildSession.Metadata, "launch_source"); got != "targeted_subagent" {
		t.Fatalf("launch source = %q, want targeted_subagent", got)
	}
	if got := metadataStringForTest(launch.ChildSession.Metadata, "targeted_subagent"); got != "targeted-priority-reviewer" {
		t.Fatalf("targeted_subagent metadata = %q, want targeted-priority-reviewer", got)
	}
	child, ok, err := svc.sessions.GetSession(launch.ChildSession.ID)
	if err != nil {
		t.Fatalf("load targeted child session: %v", err)
	}
	if !ok {
		t.Fatalf("targeted child session %q was not persisted", launch.ChildSession.ID)
	}
	if got := child.Preference.ServiceTier; got != "priority" {
		t.Fatalf("persisted targeted child service tier = %q, want priority", got)
	}
	if got := child.Preference.ContextMode; got != "" {
		t.Fatalf("persisted targeted child context mode = %q, want cleared for non-codex/gpt-5.4", got)
	}
}

func TestApplyAgentPreferenceOverridesSplitProfileKeepsInheritedPriorityServiceTier(t *testing.T) {
	base := pebblestore.ModelPreference{
		Provider:    "codex",
		Model:       "gpt-5.4",
		Thinking:    "high",
		ServiceTier: "priority",
		ContextMode: "1m",
	}
	profile := pebblestore.AgentProfile{
		ModelMode:       "split",
		AutoProvider:    "fireworks",
		AutoModel:       "accounts/fireworks/models/glm-5p1",
		AutoThinking:    "high",
		PlanProvider:    "static",
		PlanModel:       "plan-review-model",
		PlanThinking:    "low",
		PlanServiceTier: "",
		AutoServiceTier: "",
	}

	got := applyAgentPreferenceOverridesForMode(base, profile, sessionruntime.ModeAuto)
	if got.Provider != "fireworks" || got.Model != "accounts/fireworks/models/glm-5p1" {
		t.Fatalf("preference provider/model = %q/%q, want fireworks/accounts/fireworks/models/glm-5p1", got.Provider, got.Model)
	}
	if got.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want inherited priority", got.ServiceTier)
	}
	if got.ContextMode != "" {
		t.Fatalf("context mode = %q, want cleared for non-codex/gpt-5.4", got.ContextMode)
	}
}

func TestApplyAgentPreferenceOverridesClearsUnsupportedServiceTierProviders(t *testing.T) {
	base := pebblestore.ModelPreference{
		Provider:    "codex",
		Model:       "gpt-5.4",
		Thinking:    "high",
		ServiceTier: "priority",
		ContextMode: "1m",
	}
	profile := pebblestore.AgentProfile{
		Provider: "static",
		Model:    "review-model",
		Thinking: "low",
	}

	got := applyAgentPreferenceOverrides(base, profile)
	if got.ServiceTier != "" {
		t.Fatalf("service tier = %q, want cleared for unsupported provider", got.ServiceTier)
	}
	if got.ContextMode != "" {
		t.Fatalf("context mode = %q, want cleared for non-codex/gpt-5.4", got.ContextMode)
	}
}

func TestProviderManagedV3ToolRequiresPrimaryMutationCallback(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	_, err := svc.executeProviderManagedToolCall(context.Background(), providerToolInvokerConfig{
		sessionID:         parentSessionID,
		providerManagedV3: true,
	}, tool.Call{Name: "task", CallID: "call-no-v3-mutation", Arguments: "{}"}, nil)
	if err == nil || !strings.Contains(err.Error(), "requires applySessionV3PrimaryMutation") {
		t.Fatalf("expected missing V3 mutation callback error, got %v", err)
	}
}

func TestTaskDelegationContextUsesLatestCompactAndActivePlan(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	if _, _, err := svc.sessions.SavePlan(parentSessionID, "plan-delegation", "Delegation Plan", "# Active plan\n- Keep this current plan", "approved", "approved", true); err != nil {
		t.Fatalf("save active plan: %v", err)
	}
	messages := []struct {
		role    string
		content string
	}{
		{"user", "old pre-compact request"},
		{"system", contextCompactionMarkerPrefix + " index=1 origin=manual\nold compact checkpoint"},
		{"assistant", "old post-first compact answer"},
		{"system", contextCompactionMarkerPrefix + " index=2 origin=manual\nlatest compact checkpoint"},
		{"user", "tail after latest compact"},
	}
	for _, message := range messages {
		if _, _, _, err := svc.sessions.AppendMessage(parentSessionID, message.role, message.content, nil); err != nil {
			t.Fatalf("append %s message: %v", message.role, err)
		}
	}

	parent, ok, err := svc.sessions.GetSession(parentSessionID)
	if err != nil || !ok {
		t.Fatalf("load parent session ok=%v err=%v", ok, err)
	}
	context, err := svc.loadTaskDelegationContext(parentSessionID)
	if err != nil {
		t.Fatalf("load delegation context: %v", err)
	}
	prompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{
		Description:      "delegated check",
		Prompt:           "Do the work",
		ParentSession:    parent,
		ParentMessages:   context.ParentMessages,
		ParentActivePlan: context.ActivePlan,
	})

	for _, want := range []string{"latest compact checkpoint", "tail after latest compact", "Active session plan:", "# Active plan", "Keep this current plan"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("delegated prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{"old pre-compact request", "old compact checkpoint", "old post-first compact answer"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("delegated prompt included stale context %q:\n%s", forbidden, prompt)
		}
	}
}

func TestTaskDelegationContextIncludesActivePlanWithoutCompact(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	if _, _, err := svc.sessions.SavePlan(parentSessionID, "plan-no-compact", "No Compact Plan", "# Still active\n- Include me", "approved", "approved", true); err != nil {
		t.Fatalf("save active plan: %v", err)
	}
	if _, _, _, err := svc.sessions.AppendMessage(parentSessionID, "user", "visible recent parent message", nil); err != nil {
		t.Fatalf("append parent message: %v", err)
	}
	parent, ok, err := svc.sessions.GetSession(parentSessionID)
	if err != nil || !ok {
		t.Fatalf("load parent session ok=%v err=%v", ok, err)
	}
	context, err := svc.loadTaskDelegationContext(parentSessionID)
	if err != nil {
		t.Fatalf("load delegation context: %v", err)
	}
	prompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{
		Description:      "delegated check",
		Prompt:           "Do the work",
		ParentSession:    parent,
		ParentMessages:   context.ParentMessages,
		ParentActivePlan: context.ActivePlan,
	})
	for _, want := range []string{"visible recent parent message", "Active session plan:", "# Still active", "Include me"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("delegated prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestAppendRunMessageUsesV3MutationCallbackWhenProvided(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	const accountScopeID = "account-cp2"
	const userID = "user-cp2"
	session, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         userID,
		AccountScopeID: accountScopeID,
		Title:          "Child",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"parent_session_id": "parent-cp2",
			"lineage_kind":      "delegated_subagent",
		},
		Preference: &pebblestore.ModelPreference{Provider: "static", Model: "review-model", Thinking: "low"},
	})
	if err != nil {
		t.Fatalf("create child session: %v", err)
	}
	var captured []sessionruntime.SessionMutationInput
	apply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		captured = append(captured, input)
		return svc.sessions.ApplySessionMutation(input)
	}
	message, _, legacyEvent, err := svc.appendRunMessage(runAppendMessageInput{
		SessionID:            session.ID,
		Role:                 "assistant",
		Content:              "child response",
		Metadata:             map[string]any{"source": messageMetadataSourceRunTurn},
		RunID:                "run-cp2",
		Step:                 2,
		LogicalKey:           "assistant:2",
		Principal:            identity.Principal{Type: identity.PrincipalTypeUser, UserID: userID, AccountScopeID: accountScopeID},
		ApplySessionMutation: apply,
	})
	if err != nil {
		t.Fatalf("append run message: %v", err)
	}
	if legacyEvent != nil {
		t.Fatalf("appendRunMessage returned legacy event despite V3 callback: %#v", legacyEvent)
	}
	if len(captured) != 1 {
		t.Fatalf("captured mutations = %d, want 1", len(captured))
	}
	input := captured[0]
	if input.Kind != sessionruntime.SessionMutationAppendMessage {
		t.Fatalf("mutation kind = %q, want append message", input.Kind)
	}
	if input.ClientRequestID == "" || input.ClientRequestID != input.IdempotencyKey {
		t.Fatalf("invalid idempotency fields: client=%q key=%q", input.ClientRequestID, input.IdempotencyKey)
	}
	if input.PayloadHash == "" || input.PayloadHash != input.RequestHash {
		t.Fatalf("invalid payload hash fields: payload=%q request=%q", input.PayloadHash, input.RequestHash)
	}
	if input.Message == nil || input.Message.ID == "" || input.Message.Role != "assistant" || input.Message.Content != "child response" {
		t.Fatalf("unexpected mutation message: %#v", input.Message)
	}

	hydrated, ok, err := svc.sessions.HydrateSessionSnapshot(session.ID, 500, 500)
	if err != nil {
		t.Fatalf("hydrate session: %v", err)
	}
	if !ok {
		t.Fatalf("session %q not found through V3 hydration", session.ID)
	}
	if len(hydrated.Messages) != 1 || hydrated.Messages[0].ID != message.ID || hydrated.Messages[0].Content != "child response" {
		t.Fatalf("hydrated V3 messages = %#v, want appended child response %q", hydrated.Messages, message.ID)
	}
	if len(hydrated.Events) != 1 || hydrated.Events[0].EventType != "session.message.appended" {
		t.Fatalf("hydrated V3 events = %#v, want single message append event", hydrated.Events)
	}
}

func metadataStringForTest(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		return typed
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestClonePermissionSnapshotsCurrentCaller(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(parentSessionID)
	if err != nil || !ok {
		t.Fatalf("load parent: ok=%v err=%v", ok, err)
	}
	profile := pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
		Name: "swarm", Mode: agentruntime.ModePrimary, RuntimeMode: pebblestore.AgentRuntimeModePlanAuto,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true), Prompt: "trusted parent prompt",
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_write"}, Enabled: true,
	})
	metadata := cloneGenericMap(parent.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["agent_name"] = "swarm"
	metadata["agent_profile"] = profile
	if _, _, err := svc.sessions.UpdateMetadata(parentSessionID, metadata); err != nil {
		t.Fatalf("update parent metadata: %v", err)
	}
	manifest, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{Name: "task", Arguments: mustJSON(t, map[string]any{
		"prompt": "implement independent scope", "launches": []any{map[string]any{"subagent_type": "clone", "meta_prompt": "implement backend"}},
	})})
	if err != nil {
		t.Fatalf("build Clone manifest: %v", err)
	}
	if len(manifest.Launches) != 1 || !manifest.Launches[0].ParentCopy || manifest.Launches[0].SourceAgentName != "swarm" {
		t.Fatalf("Clone manifest = %#v", manifest.Launches)
	}
	if manifest.Launches[0].ProfileSnapshot == nil || manifest.Launches[0].ProfileSnapshot.Name != agentruntime.CloneAgentID || manifest.Launches[0].ProfileSnapshot.Prompt != agentruntime.CloneAgentPrompt() || manifest.Launches[0].InheritedRuntimeMode != pebblestore.AgentRuntimeModeReadWrite {
		t.Fatalf("Clone snapshot = %#v", manifest.Launches[0])
	}
	if manifest.Launches[0].ProfileSnapshot.Provider != parent.Preference.Provider || manifest.Launches[0].ProfileSnapshot.Model != parent.Preference.Model || manifest.Launches[0].SubagentThinking != parent.Preference.Thinking || manifest.Launches[0].SubagentServiceTier != parent.Preference.ServiceTier {
		t.Fatalf("Clone did not inherit parent launch preference and service tier: %#v", manifest.Launches[0])
	}
	cloneTools := manifest.Launches[0].ProfileSnapshot.ToolContract.Tools
	for _, name := range []string{"git_status", "git_diff", "git_add", "git_commit"} {
		if cfg, ok := cloneTools[name]; !ok || cfg.Enabled == nil || !*cfg.Enabled {
			t.Fatalf("Clone snapshot omitted enabled %s: %#v", name, cloneTools)
		}
	}
	if cfg := cloneTools["bash"]; cfg.Enabled == nil || *cfg.Enabled {
		t.Fatalf("Clone snapshot must disable generic bash: %#v", cloneTools)
	}
	if manifest.ManifestHash == "" || manifest.ApprovedArguments == nil {
		t.Fatalf("Clone manifest binding missing: %#v", manifest)
	}
}

func TestCloneLaunchDoesNotRequirePersistedProfile(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	if _, ok, err := svc.agents.GetProfileForAccount("test-account", "clone"); err != nil || ok {
		t.Fatalf("persisted Clone profile ok=%v err=%v, want absent", ok, err)
	}
	manifest, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{Name: "task", Arguments: `{"prompt":"x","subagent_type":"clone","meta_prompt":"y"}`})
	if err != nil {
		t.Fatalf("build compiled Clone manifest: %v", err)
	}
	if len(manifest.Launches) != 1 || manifest.Launches[0].ProfileSnapshot == nil || manifest.Launches[0].ProfileSnapshot.Name != agentruntime.CloneAgentID || !manifest.Launches[0].ParentCopy {
		t.Fatalf("compiled Clone manifest = %#v", manifest.Launches)
	}
}

func TestApprovedExplorerWaveManifestDigestSurvivesPermissionRoundTrip(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	launches := make([]any, 5)
	for i := range launches {
		launches[i] = map[string]any{
			"subagent_type": "explorer",
			"meta_prompt":   fmt.Sprintf("Inspect scope %d", i+1),
		}
	}
	manifest, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{Name: "task", Arguments: mustJSON(t, map[string]any{
		"prompt":   "Inspect independent scopes.",
		"launches": launches,
	})})
	if err != nil {
		t.Fatalf("build Explorer manifest: %v", err)
	}
	for i, row := range manifest.Launches {
		if row.ParentCopy {
			t.Fatalf("Explorer manifest launch %d incorrectly classified as a parent copy: %#v", i, row)
		}
		if row.ProfileSnapshot == nil {
			t.Fatalf("Explorer manifest launch %d missing trusted profile snapshot", i)
		}
		if row.ResolvedTools == nil || !slices.Contains(row.ResolvedTools.AllowedTools, "read") || slices.Contains(row.ResolvedTools.AllowedTools, "write") {
			t.Fatalf("Explorer manifest launch %d resolved tools = %#v, want compiled read-only system contract", i, row.ResolvedTools)
		}
	}
	raw, err := json.Marshal(manifest.ApprovedArguments)
	if err != nil {
		t.Fatalf("marshal approved arguments: %v", err)
	}
	var envelope struct {
		ManifestHash string             `json:"manifest_hash"`
		Manifest     taskLaunchManifest `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal approved arguments: %v", err)
	}
	digest, err := taskLaunchManifestDigest(envelope.Manifest)
	if err != nil {
		t.Fatalf("digest approved manifest: %v", err)
	}
	if digest != envelope.ManifestHash || digest != envelope.Manifest.ManifestHash {
		t.Fatalf("approved manifest hash mismatch: digest=%q envelope=%q manifest=%q", digest, envelope.ManifestHash, envelope.Manifest.ManifestHash)
	}
}

func TestTaskRejectsClientSuppliedCloneTrustFields(t *testing.T) {
	_, err := parseTaskCallArguments(`{"prompt":"x","subagent_type":"clone","meta_prompt":"y","runtime_mode":"readwrite"}`)
	if err == nil || !strings.Contains(err.Error(), "cannot set launch-time trust") {
		t.Fatalf("parse error = %v", err)
	}
}

func newTaskLaunchPermissionTestService(t *testing.T) (*Service, string, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cleanup := func() { _ = store.Close() }

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		cleanup()
		t.Fatalf("open event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agents.EnsureDefaults(); err != nil {
		cleanup()
		t.Fatalf("ensure agent defaults: %v", err)
	}
	if err := agents.EnsureDefaultsForAccount("test-account"); err != nil {
		cleanup()
		t.Fatalf("ensure account agent defaults: %v", err)
	}
	if _, _, _, err := agents.Upsert(agentruntime.UpsertInput{
		Name:                "reviewer",
		Mode:                agentruntime.ModeSubagent,
		Description:         "Review specialist",
		Provider:            "static",
		Model:               "review-model",
		Prompt:              "Review carefully.",
		RuntimeMode:         pebblestore.AgentRuntimeModeReadWrite,
		ExecutionSetting:    pebblestore.AgentExecutionSettingReadWrite,
		ExitPlanModeEnabled: pebblestore.BoolPtr(false),
		ToolContract: &pebblestore.AgentToolContract{
			Preset: "bash_git_only",
			Tools: map[string]pebblestore.AgentToolConfig{
				"bash": {BashPrefixes: []string{"git status", "git diff --"}},
			},
		},
		Enabled: pebblestore.BoolPtr(true),
	}); err != nil {
		cleanup()
		t.Fatalf("create reviewer: %v", err)
	}
	if _, _, _, err := agents.Upsert(agentruntime.UpsertInput{
		Name:             "disabled-subagent",
		Mode:             agentruntime.ModeSubagent,
		Prompt:           "Disabled.",
		RuntimeMode:      pebblestore.AgentRuntimeModeRead,
		ExecutionSetting: pebblestore.AgentExecutionSettingRead,
		ToolContract:     &pebblestore.AgentToolContract{Preset: "read_only"},
		Enabled:          pebblestore.BoolPtr(false),
	}); err != nil {
		cleanup()
		t.Fatalf("create disabled subagent: %v", err)
	}
	if _, _, _, err := agents.SetActiveSubagent("purpose-review", "reviewer"); err != nil {
		cleanup()
		t.Fatalf("set active subagent mapping: %v", err)
	}

	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parent, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		AccountScopeID: "test-account",
		Title:          "Parent",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "parent-provider",
			Model:    "parent-model",
			Thinking: "high",
		},
	})
	if err != nil {
		cleanup()
		t.Fatalf("create parent session: %v", err)
	}
	catalog := model.NewCatalogService(pebblestore.NewModelCatalogStore(store))
	models := model.NewService(pebblestore.NewModelStore(store), events, catalog)
	if err := models.EnsureBootDefaults(); err != nil {
		cleanup()
		t.Fatalf("ensure model defaults: %v", err)
	}
	if _, _, err := models.SetPreferenceForAccount("test-account", "test-user", "codex", "gpt-5.4", "high"); err != nil {
		cleanup()
		t.Fatalf("set account model preference: %v", err)
	}
	return NewService(sessions, models, nil, tool.NewRuntime(1), nil, agents, nil, events), parent.ID, cleanup
}

func TestGateToolCallsRejectsMalformedXMLToolArgumentsWithoutPermissionService(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	if svc.permissions != nil {
		t.Fatalf("test helper unexpectedly configured permissions")
	}
	arguments := mustJSON(t, map[string]any{
		"prompt": "Execute assigned work. </怡parameter>",
		"launches": []any{
			map[string]any{"subagent_type": "reviewer", "meta_prompt": "Map top-level directories."},
		},
	})

	results, approvedCalls, _, approvedMask, _, err := svc.gateToolCalls(context.Background(), parentSessionID, "run-test", 1, sessionruntime.ModeAuto, []tool.Call{{
		CallID:    "call-bad-task-xml",
		Name:      "task",
		Arguments: arguments,
	}}, nil, nil)
	if err != nil {
		t.Fatalf("gate tool calls returned unexpected error: %v", err)
	}
	if len(approvedCalls) != 0 || len(approvedMask) != 1 || approvedMask[0] {
		t.Fatalf("malformed XML task call was approved: calls=%d mask=%v", len(approvedCalls), approvedMask)
	}
	if len(results) != 1 || !strings.Contains(results[0].Error, "invalid tool arguments") || !strings.Contains(results[0].Error, "malformed XML markup in tool call") {
		t.Fatalf("expected parser-level XML error, got %#v", results)
	}
	if strings.Contains(results[0].Error, "requires subagent_type, agent, or purpose") {
		t.Fatalf("expected parser-level error before launch validation, got %#v", results)
	}
}

func newTaskLaunchPermissionServiceWithPermissions(t *testing.T) (*Service, string, *permission.Service, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cleanup := func() { _ = store.Close() }
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		cleanup()
		t.Fatalf("open event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agents.EnsureDefaults(); err != nil {
		cleanup()
		t.Fatalf("ensure agent defaults: %v", err)
	}
	if err := agents.EnsureDefaultsForAccount("test-account"); err != nil {
		cleanup()
		t.Fatalf("ensure account agent defaults: %v", err)
	}
	if _, _, _, err := agents.Upsert(agentruntime.UpsertInput{
		Name:             "reviewer",
		Mode:             agentruntime.ModeSubagent,
		Prompt:           "Review carefully.",
		RuntimeMode:      pebblestore.AgentRuntimeModeRead,
		ExecutionSetting: pebblestore.AgentExecutionSettingRead,
		ToolContract:     &pebblestore.AgentToolContract{Preset: "read_only"},
		Enabled:          pebblestore.BoolPtr(true),
	}); err != nil {
		cleanup()
		t.Fatalf("create reviewer: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parent, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		AccountScopeID: "test-account",
		Title:          "Parent",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "parent-provider",
			Model:    "parent-model",
			Thinking: "high",
		},
	})
	if err != nil {
		cleanup()
		t.Fatalf("create parent session: %v", err)
	}
	permissions := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc := NewService(sessions, nil, nil, tool.NewRuntime(1), permissions, agents, nil, events)
	return svc, parent.ID, permissions, cleanup
}

func TestProviderAPIDiagnosticRecorderPersistsViaApplySessionMutation(t *testing.T) {
	svc, sessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	session, ok, err := svc.sessions.GetSession(sessionID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if !ok {
		t.Fatalf("session %q not found", sessionID)
	}
	t.Setenv(providerdiagnostics.EnvName, "1")
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: session.UserID, AccountScopeID: session.AccountScopeID}
	apply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		return svc.sessions.ApplySessionMutation(input)
	}

	ctx := svc.contextWithProviderAPIDiagnosticRecorder(context.Background(), sessionID, "run-diagnostic", principal, apply)
	providerdiagnostics.LogWebsocketRequestContext(ctx, "codex", "responses.websocket", "wss://example.invalid/session", nil, []byte(`{"model":"gpt-5.5","service_tier":"priority"}`))

	events, err := svc.sessions.ListSessionEvents(sessionID, 0, 20)
	if err != nil {
		t.Fatalf("list session events: %v", err)
	}
	var found bool
	for _, event := range events {
		if event.EventType != "session.diagnostic.provider.api.websocket_request" {
			continue
		}
		found = true
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal diagnostic payload: %v", err)
		}
		if got := strings.TrimSpace(mapString(payload, "run_id")); got != "run-diagnostic" {
			t.Fatalf("diagnostic run_id = %q, want run-diagnostic", got)
		}
		if got := strings.TrimSpace(mapString(payload, "source")); got != "backend.provider.api" {
			t.Fatalf("diagnostic source = %q, want backend.provider.api", got)
		}
		rawPayload, ok := payload["payload"].(map[string]any)
		if !ok {
			t.Fatalf("diagnostic payload = %#v, want object", payload["payload"])
		}
		if got := strings.TrimSpace(mapString(rawPayload, "body")); !strings.Contains(got, "service_tier") || !strings.Contains(got, "priority") {
			t.Fatalf("diagnostic body = %q, want service_tier priority", got)
		}
	}
	if !found {
		t.Fatalf("missing provider api websocket_request diagnostic in events: %#v", events)
	}
}
