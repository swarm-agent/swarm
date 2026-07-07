package api

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type sessionsV3ProviderToolsRunner struct {
	definitions                []tool.Definition
	contract                   runruntime.ResolvedAgentToolContract
	disabled                   map[string]bool
	checkpointRequests         []runruntime.RunRequest
	checkpointMetas            []runruntime.RunStartMeta
	checkpointInputReturn      []map[string]any
	checkpointInputReturnOK    bool
	checkpointInputReturnOKSet bool
}

func (r *sessionsV3ProviderToolsRunner) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}

func (r *sessionsV3ProviderToolsRunner) RunTurnStreaming(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta, runruntime.StreamHandler) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, nil
}

func (r *sessionsV3ProviderToolsRunner) StopSessionRun(string, string, string) error { return nil }

func (r *sessionsV3ProviderToolsRunner) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "{}", nil
}

func (r *sessionsV3ProviderToolsRunner) ListAgentToolDefinitions() []tool.Definition {
	return r.definitions
}

func (r *sessionsV3ProviderToolsRunner) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return r.definitions
}

func (r *sessionsV3ProviderToolsRunner) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return r.contract, nil, r.disabled, nil
}

func (r *sessionsV3ProviderToolsRunner) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return r.contract, nil, r.disabled, nil
}

func (r *sessionsV3ProviderToolsRunner) CompileStoredV3AgentToolContract(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, map[string]bool, error) {
	return r.contract, r.disabled, nil
}

func (r *sessionsV3ProviderToolsRunner) BuildPlanCheckpointRunInput(_ string, _ string, request runruntime.RunRequest, meta runruntime.RunStartMeta) ([]map[string]any, bool, error) {
	r.checkpointRequests = append(r.checkpointRequests, request)
	r.checkpointMetas = append(r.checkpointMetas, meta)
	if r.checkpointInputReturnOKSet {
		return r.checkpointInputReturn, r.checkpointInputReturnOK, nil
	}
	return []map[string]any{{"role": "user", "content": "checkpoint"}}, true, nil
}

func TestSessionV3ProviderCheckpointScopeFromFreshPayloadOverridesStaleJobScope(t *testing.T) {
	scope := sessionV3ProviderCheckpointScopeFromPayload(sessionV3ProviderCheckpointScope{
		PlanID:          "old-plan",
		CheckpointID:    "cp-4",
		AttemptID:       "cp-4:attempt-1",
		ParentSessionID: "parent-old",
		FreshContext:    true,
	}, map[string]any{
		"next_action":        "run_checkpoint_with_fresh_context",
		"next_checkpoint_id": "cp-5",
		"run_request": map[string]any{
			"plan_checkpoint_context": map[string]any{
				"plan_id":           "new-plan",
				"checkpoint_id":     "cp-5",
				"attempt_id":        "cp-5:attempt-1",
				"parent_session_id": "parent-new",
			},
		},
	})

	if scope.PlanID != "new-plan" || scope.CheckpointID != "cp-5" || scope.AttemptID != "cp-5:attempt-1" || scope.ParentSessionID != "parent-new" {
		t.Fatalf("scope = %+v, want fresh payload plan/checkpoint/attempt/parent", scope)
	}
	if !scope.FreshContext {
		t.Fatalf("FreshContext = false, want true")
	}
}

func TestSessionV3ProviderCheckpointRestartInputUsesFreshPayloadCheckpointOverJobCheckpoint(t *testing.T) {
	runner := &sessionsV3ProviderToolsRunner{}
	exec := &sessionV3Executor{server: &Server{runner: runner}}
	toolOutput := `{"next_action":"run_checkpoint_with_fresh_context","next_checkpoint_id":"cp-5","run_request":{"plan_checkpoint_context":{"plan_id":"replacement-plan","checkpoint_id":"cp-5","attempt_id":"cp-5:attempt-1","parent_session_id":"parent-new"}}}`

	input, ok, err := exec.sessionV3ProviderCheckpointRestartInput(context.Background(), sessionV3ExecutorJob{
		SessionID:       "session-1",
		RunID:           "run-1",
		PlanID:          "old-plan",
		CheckpointID:    "cp-4",
		AttemptID:       "cp-4:attempt-1",
		ParentSessionID: "parent-old",
	}, sessionV3ResolvedRuntime{}, toolOutput)
	if err != nil {
		t.Fatalf("checkpoint restart input: %v", err)
	}
	if !ok || len(input) == 0 {
		t.Fatalf("checkpoint restart input ok=%v input=%v, want non-empty", ok, input)
	}
	if len(runner.checkpointRequests) != 1 {
		t.Fatalf("checkpoint request count = %d, want 1", len(runner.checkpointRequests))
	}
	ctx := runner.checkpointRequests[0].PlanCheckpointContext
	if ctx == nil {
		t.Fatalf("PlanCheckpointContext is nil")
	}
	if ctx.PlanID != "replacement-plan" || ctx.CheckpointID != "cp-5" || ctx.AttemptID != "cp-5:attempt-1" || ctx.ParentSessionID != "parent-new" {
		t.Fatalf("PlanCheckpointContext = %+v, want fresh payload context", *ctx)
	}
}

func TestApplySessionV3AgentPreferenceOverridesPreservesSupportedPriorityServiceTier(t *testing.T) {
	base := pebblestore.ModelPreference{
		Provider:    "codex",
		Model:       "gpt-5.4",
		Thinking:    "high",
		ServiceTier: "priority",
		ContextMode: "1m",
	}
	profile := pebblestore.AgentProfile{
		Provider: "fireworks",
		Model:    "accounts/fireworks/models/glm-5p1",
		Thinking: "xhigh",
	}

	got := applySessionV3AgentPreferenceOverrides(base, profile)
	if got.Provider != "fireworks" || got.Model != "accounts/fireworks/models/glm-5p1" {
		t.Fatalf("preference provider/model = %q/%q, want fireworks/accounts/fireworks/models/glm-5p1", got.Provider, got.Model)
	}
	if got.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want priority", got.ServiceTier)
	}
	if got.ContextMode != "" {
		t.Fatalf("context mode = %q, want cleared for non-codex/gpt-5.4", got.ContextMode)
	}
}

func TestApplySessionV3AgentPreferenceOverridesSplitProfileUsesPlanSettingsForPlanMode(t *testing.T) {
	base := pebblestore.ModelPreference{
		Provider: "codex",
		Model:    "gpt-5.4",
		Thinking: "medium",
	}
	profile := pebblestore.AgentProfile{
		ModelMode:       "split",
		PlanProvider:    "fireworks",
		PlanModel:       "accounts/fireworks/models/glm-5p1",
		PlanThinking:    "high",
		PlanServiceTier: "priority",
		AutoProvider:    "static",
		AutoModel:       "auto-review-model",
		AutoThinking:    "low",
	}

	got := applySessionV3AgentPreferenceOverridesForMode(base, profile, "plan")
	if got.Provider != "fireworks" || got.Model != "accounts/fireworks/models/glm-5p1" {
		t.Fatalf("preference provider/model = %q/%q, want fireworks/accounts/fireworks/models/glm-5p1", got.Provider, got.Model)
	}
	if got.Thinking != "high" {
		t.Fatalf("thinking = %q, want high", got.Thinking)
	}
	if got.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want priority", got.ServiceTier)
	}
}

func TestApplySessionV3AgentPreferenceOverridesSplitProfileKeepsInheritedPriorityServiceTier(t *testing.T) {
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

	got := applySessionV3AgentPreferenceOverridesForMode(base, profile, "auto")
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

func TestApplySessionV3AgentPreferenceOverridesClearsUnsupportedServiceTierProviders(t *testing.T) {
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

	got := applySessionV3AgentPreferenceOverrides(base, profile)
	if got.ServiceTier != "" {
		t.Fatalf("service tier = %q, want cleared for unsupported provider", got.ServiceTier)
	}
	if got.ContextMode != "" {
		t.Fatalf("context mode = %q, want cleared for non-codex/gpt-5.4", got.ContextMode)
	}
}

func TestResolveSessionV3ProviderToolsCanonicalizesDefinitionNames(t *testing.T) {
	runner := &sessionsV3ProviderToolsRunner{
		definitions: []tool.Definition{
			{Type: "function", Name: "ask-user"},
			{Type: "function", Name: "bash"},
			{Type: "function", Name: "manage-agent"},
			{Type: "function", Name: "manage-skill"},
			{Type: "function", Name: "manage-worktree"},
			{Type: "function", Name: "skill-use"},
		},
		contract: runruntime.ResolvedAgentToolContract{Tools: map[string]runruntime.ResolvedAgentTool{
			"ask_user":        {Enabled: true},
			"bash":            {Enabled: true},
			"manage_agent":    {Enabled: true},
			"manage_skill":    {Enabled: true},
			"manage_worktree": {Enabled: true},
			"skill_use":       {Enabled: true},
		}},
		disabled: map[string]bool{},
	}
	exec := &sessionV3Executor{server: &Server{runner: runner}}

	tools, err := exec.resolveSessionV3ProviderTools("acct_test", pebblestore.AgentProfile{Name: "swarm", ToolContract: &pebblestore.AgentToolContract{}})
	if err != nil {
		t.Fatalf("resolveSessionV3ProviderTools: %v", err)
	}
	names := make([]string, 0, len(tools))
	for _, definition := range tools {
		names = append(names, definition.Name)
	}
	sort.Strings(names)
	expected := []string{"ask-user", "bash", "manage-agent", "manage-skill", "manage-worktree", "skill-use"}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("provider tool names mismatch\n got: %v\nwant: %v", names, expected)
	}
}
