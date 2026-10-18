package run

import (
	"context"
	"encoding/json"
	"errors"

	"path/filepath"

	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestParseTaskCallArgumentsRejectsLaunchTimeTrustFields(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: "top level allow_bash",
			args: map[string]any{
				"prompt":        "inspect the repo",
				"subagent_type": "finder",
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
						"subagent_type":     "finder",
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
						"subagent_type": "finder",
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
						"subagent_type": "finder",
						"meta_prompt":   "map the relevant files",
						"tool_scope":    map[string]any{"allow_tools": []any{"bash"}},
					},
				},
			},
		},
		{
			name: "model-selected managed destination",
			args: map[string]any{
				"prompt": "create variant", "subagent_type": "designer", "meta_prompt": "create variant", "collection_id": "untrusted",
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

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(encoded)
}

type taskLaunchWorktreeStub struct {
	allocations       int
	allocation        worktreeruntime.Allocation
	taskBase          worktreeruntime.TaskBase
	config            worktreeruntime.Config
	requestedBase     string
	resolvedPaths     []string
	allocatedPaths    []string
	allocatedScopes   [][]string
	requestedBranch   string
	requestedNameSeed string
	cleanupCalls      []string
	cleanupErr        error
}

func (s *taskLaunchWorktreeStub) AttachBranch(_, _, _ string) (string, error) { return "", nil }

func (s *taskLaunchWorktreeStub) ResolveTaskBase(path string) (worktreeruntime.TaskBase, error) {
	s.resolvedPaths = append(s.resolvedPaths, strings.TrimSpace(path))
	if strings.TrimSpace(s.taskBase.BaseCommit) == "" {
		return worktreeruntime.TaskBase{RepoRoot: "/repo", ParentBranch: "dev", BaseCommit: "base-commit"}, nil
	}
	return s.taskBase, nil
}

func (s *taskLaunchWorktreeStub) AllocateTaskWorkspace(path string, _ worktreeruntime.TaskBase, _ string, ownedScopes []string) (worktreeruntime.Allocation, error) {
	s.allocations++
	s.allocatedPaths = append(s.allocatedPaths, strings.TrimSpace(path))
	s.allocatedScopes = append(s.allocatedScopes, append([]string(nil), ownedScopes...))
	return s.allocation, nil
}

func (s *taskLaunchWorktreeStub) TaskCommitDescendsFrom(_, baseCommit, headCommit string) (bool, error) {
	return strings.TrimSpace(baseCommit) != "" && strings.TrimSpace(headCommit) != "" && baseCommit != headCommit, nil
}

func (s *taskLaunchWorktreeStub) TaskCommitRangeIntegratedInto(_, baseCommit, headCommit, parentHead string) (bool, error) {
	return strings.TrimSpace(baseCommit) != "" && strings.TrimSpace(headCommit) != "" && strings.TrimSpace(parentHead) != "" && baseCommit != headCommit, nil
}

func (s *taskLaunchWorktreeStub) RemoveIntegratedTaskWorkspace(_, childPath, _, _, _, _ string) error {
	s.cleanupCalls = append(s.cleanupCalls, childPath)
	return s.cleanupErr
}

func (s *taskLaunchWorktreeStub) InspectTaskWorkspace(path string) (worktreeruntime.TaskWorkspaceState, error) {
	return worktreeruntime.TaskWorkspaceState{WorkspacePath: path, BranchName: s.allocation.BranchName, HeadCommit: s.taskBase.BaseCommit, Clean: true}, nil
}

func (s *taskLaunchWorktreeStub) GetConfigForPrincipal(_ identity.Principal, workspacePath string) (worktreeruntime.Config, error) {
	if strings.TrimSpace(s.config.WorkspacePath) == "" {
		return worktreeruntime.Config{WorkspacePath: workspacePath, BranchName: "agent"}, nil
	}
	return s.config, nil
}

func (s *taskLaunchWorktreeStub) AllocateDetachedWorkspaceRequestedForPrincipal(_ identity.Principal, _, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	s.allocations++
	s.requestedNameSeed, s.requestedBase, s.requestedBranch = nameSeed, baseBranch, branchName
	return s.allocation, nil
}

func (s *taskLaunchWorktreeStub) RollbackAllocation(_ worktreeruntime.Allocation) error { return nil }

type taskDesignerRecordingRunner struct {
	requests         []provideriface.Request
	inspectImagePath string
	invocationResult provideriface.ToolExecutionResult
	invocationErr    error
}

func (r *taskDesignerRecordingRunner) ID() string { return "codex" }

func (r *taskDesignerRecordingRunner) MediaCapabilityDeclaration(context.Context) (provideriface.MediaAdapterDeclaration, error) {
	return provideriface.MediaAdapterDeclaration{
		AdapterID:             provideriface.MediaAdapterIDCodexChatGPTV1,
		ProviderID:            "codex",
		ProviderSurface:       provideriface.MediaProviderSurfaceCodexChatGPT,
		CredentialSurface:     provideriface.MediaCredentialSurfaceCodexOAuth,
		CredentialFingerprint: "task-designer-test-credential",
		Inputs: []provideriface.MediaAdapterCapability{{
			Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
			MIMETypes: []string{"image/gif", "image/jpeg", "image/png", "image/webp"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 1,
		}},
	}, nil
}

func (r *taskDesignerRecordingRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *taskDesignerRecordingRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.requests = append(r.requests, req)
	if strings.TrimSpace(r.inspectImagePath) != "" && len(r.requests) == 1 {
		if req.ToolInvoker == nil {
			return provideriface.Response{}, errors.New("Designer media test request has no tool invoker")
		}
		r.invocationResult, r.invocationErr = req.ToolInvoker.ExecuteTool(ctx, provideriface.ToolInvocation{
			CallID: "call-designer-media-inspect", Name: mediaInspectToolName,
			Arguments: mustJSONForRunner(map[string]any{"path": r.inspectImagePath}),
		})
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{
			CallID: "call-designer-media-inspect", Name: mediaInspectToolName,
			Arguments: mustJSONForRunner(map[string]any{"path": r.inspectImagePath}),
		}}}, nil
	}
	if onEvent != nil {
		onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "designer completed"})
	}
	return provideriface.Response{Text: "designer completed"}, nil
}

func mustJSONForRunner(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
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
		UserID:         "test-user",
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
	service := NewService(sessions, models, nil, tool.NewRuntime(1), nil, agents, nil, events)
	agentSettingsStore := pebblestore.NewAgentModelSettingsStore(store)
	configured := pebblestore.AgentModelAssignment{Provider: "codex", Model: "gpt-5.4", Thinking: "high"}
	if _, err := agentSettingsStore.PutForAccount(pebblestore.AgentModelSettingsRecord{
		AccountScopeID: "test-account",
		Swarm:          pebblestore.SwarmAgentModelAssignments{Action: configured, Plan: configured},
		SystemAgents: pebblestore.SystemAgentModelAssignments{
			Compact: configured, Finder: configured, Coder: configured, Designer: configured, Router: configured,
		},
	}); err != nil {
		cleanup()
		t.Fatalf("configure system-agent models: %v", err)
	}
	service.SetAgentModelSettingsService(agentmodelsettings.NewService(agentSettingsStore))
	return service, parent.ID, cleanup
}

// A managed child consumes its own receipt, while approval stays with the
// parent. Only the trusted same-owner launch may translate execution identity;
// an unrelated scoped principal must still fail the runtime ownership check.
func TestManagedDelegatedExecutionPrincipal(t *testing.T) {
	for _, name := range []string{"managed_parent", "managed_unscoped", "managed_child", "local", "foreign_account", "foreign_user", "foreign_session"} {
		t.Run(name, func(t *testing.T) {
			child := pebblestore.SessionSnapshot{ID: "child", UserID: "user", AccountScopeID: "account", Metadata: map[string]any{"parent_session_id": "parent", tool.ManagedExecutionMetadataKey: tool.ManagedExecutionBinding{WorkspaceID: "workspace", LeaseID: "child-receipt"}}}
			p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account", SessionID: "parent"}
			want := "child"
			switch name {
			case "managed_unscoped":
				p.SessionID = ""
			case "managed_child":
				p.SessionID = "child"
			case "local":
				delete(child.Metadata, tool.ManagedExecutionMetadataKey)
				want = "parent"
			case "foreign_account":
				p.AccountScopeID = "other"
				want = "parent"
			case "foreign_user":
				p.UserID = "other"
				want = "parent"
			case "foreign_session":
				p.SessionID = "other"
				want = "other"
			}
			meta := delegatedSubagentRunStartMeta(taskLaunchPrepared{ChildSession: child}, "parent", p, nil)
			if meta.Principal.SessionID != want || meta.PermissionSessionID != "parent" || meta.Principal.AccountScopeID != p.AccountScopeID || meta.Principal.UserID != p.UserID {
				t.Fatalf("execution/approval authority changed incorrectly: %#v", meta)
			}
		})
	}
}
