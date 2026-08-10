package api

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
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
	compiledProfiles           []pebblestore.AgentProfile
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

func (r *sessionsV3ProviderToolsRunner) CompileStoredV3AgentToolContract(_ string, profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, map[string]bool, error) {
	r.compiledProfiles = append(r.compiledProfiles, cloneSessionsV3AgentProfile(profile))
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

func TestSessionV3SystemSidechatResolutionUsesRegistryAndRejectsSpoofing(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "system-sidechat-resolution.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	exec := &sessionV3Executor{server: &Server{agents: agents}}

	for _, test := range []struct {
		kind string
		id   string
	}{
		{kind: agentruntime.SystemSidechatKindPlan, id: agentruntime.PlanSidechatAgentID},
		{kind: agentruntime.SystemSidechatKindAI, id: agentruntime.AISidechatAgentID},
	} {
		t.Run(test.kind, func(t *testing.T) {
			snapshot, err := agents.ResolveSystemSidechat(test.kind, pebblestore.AgentProfile{Provider: "codex", Model: "parent-model"})
			if err != nil {
				t.Fatalf("materialize sidechat: %v", err)
			}
			if test.kind == agentruntime.SystemSidechatKindPlan {
				snapshot.Prompt = agentruntime.PlanSidechatAgentPromptWithContext(`{"plan_id":"plan-1"}`)
			}
			// Simulate an old or tampered persisted snapshot. Reconciliation must
			// retain dynamic context/model fields while restoring code-owned policy.
			snapshot.RuntimeMode = pebblestore.AgentRuntimeModeReadWrite
			snapshot.ExitPlanModeEnabled = pebblestore.BoolPtr(true)
			snapshot.ToolContract = &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{
				"exit_plan_mode": {Enabled: pebblestore.BoolPtr(true)},
				"manage_agent":   {Enabled: pebblestore.BoolPtr(true)},
			}}
			metadata := sessionsV3SystemSidechatMetadata("parent-1", test.kind, snapshot)

			resolved, err := exec.resolveSessionV3CurrentAgentToolContract("account-1", metadata, snapshot)
			if err != nil {
				t.Fatalf("resolve persisted system sidechat: %v", err)
			}
			if resolved.Name != test.id || resolved.ExitPlanModeEnabled == nil || *resolved.ExitPlanModeEnabled {
				t.Fatalf("resolved identity/exit policy = %+v", resolved)
			}
			for _, toolName := range []string{"exit_plan_mode", "manage_agent"} {
				state := resolved.ToolContract.Tools[toolName]
				if state.Enabled == nil || *state.Enabled {
					t.Fatalf("resolved %s policy = %+v, want explicitly disabled", toolName, state)
				}
			}
			if resolved.Provider != "codex" || resolved.Model != "parent-model" {
				t.Fatalf("resolved provider/model = %q/%q", resolved.Provider, resolved.Model)
			}
			if test.kind == agentruntime.SystemSidechatKindPlan && !strings.Contains(resolved.Prompt, `"plan_id":"plan-1"`) {
				t.Fatalf("Plan prompt lost authoritative context: %q", resolved.Prompt)
			}

			runner := &sessionsV3ProviderToolsRunner{}
			exec.server.runner = runner
			if _, err := exec.resolveSessionV3ProviderTools("account-1", resolved); err != nil {
				t.Fatalf("compile provider tools: %v", err)
			}
			if len(runner.compiledProfiles) != 1 {
				t.Fatalf("compiled profile count = %d, want 1", len(runner.compiledProfiles))
			}
			compiled := runner.compiledProfiles[0]
			if compiled.ExitPlanModeEnabled == nil || *compiled.ExitPlanModeEnabled {
				t.Fatalf("provider compiler received enabled exit_plan_mode: %+v", compiled)
			}
			for _, toolName := range []string{"exit_plan_mode", "manage_agent"} {
				state := compiled.ToolContract.Tools[toolName]
				if state.Enabled == nil || *state.Enabled {
					t.Fatalf("provider compiler received enabled %s: %+v", toolName, state)
				}
			}
		})
	}

	plan, err := agents.ResolveSystemSidechat(agentruntime.SystemSidechatKindPlan, pebblestore.AgentProfile{})
	if err != nil {
		t.Fatalf("materialize Plan: %v", err)
	}
	ai, err := agents.ResolveSystemSidechat(agentruntime.SystemSidechatKindAI, pebblestore.AgentProfile{})
	if err != nil {
		t.Fatalf("materialize AI: %v", err)
	}
	for _, test := range []struct {
		name     string
		metadata map[string]any
		snapshot pebblestore.AgentProfile
		want     string
	}{
		{name: "reserved name without authority", metadata: nil, snapshot: plan, want: "requires authenticated system sidechat metadata"},
		{name: "unknown reserved name without authority", metadata: nil, snapshot: pebblestore.AgentProfile{Name: "system-future", Enabled: true}, want: "unknown reserved system agent"},
		{name: "kind and name mismatch", metadata: sessionsV3SystemSidechatMetadata("parent-1", "plan", ai), snapshot: ai, want: "metadata mismatch"},
		{name: "unknown future kind", metadata: sessionsV3SystemSidechatMetadata("parent-1", "future", pebblestore.AgentProfile{Name: "system-future", Enabled: true}), snapshot: pebblestore.AgentProfile{Name: "system-future", Enabled: true}, want: "unknown system sidechat kind"},
		{name: "visible system agent cannot spoof sidechat", metadata: sessionsV3SystemSidechatMetadata("parent-1", "finder", pebblestore.AgentProfile{Name: agentruntime.FinderAgentID, Enabled: true}), snapshot: pebblestore.AgentProfile{Name: agentruntime.FinderAgentID, Enabled: true}, want: "not authorized for system sidechat metadata"},
		{name: "partial metadata", metadata: map[string]any{"system_sidechat": true, "system_sidechat_kind": "plan"}, snapshot: plan, want: "invalid system sidechat metadata"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := exec.resolveSessionV3CurrentAgentToolContract("account-1", test.metadata, test.snapshot)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSessionV3VisibleSystemAgentResolutionDoesNotRequireSidechatMetadata(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "visible-system-agent-resolution.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	exec := &sessionV3Executor{server: &Server{agents: agents}}

	for _, test := range []struct {
		name string
		id   string
	}{
		{name: "Swarm", id: agentruntime.SwarmAgentID},
		{name: "Finder", id: agentruntime.FinderAgentID},
		{name: "Coder", id: agentruntime.CoderAgentID},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := agents.ResolveSystemAgent(test.id, pebblestore.AgentProfile{Provider: "codex", Model: "parent-model"})
			if err != nil {
				t.Fatalf("materialize %s: %v", test.id, err)
			}
			snapshot.Prompt = "tampered"
			snapshot.Enabled = false
			resolved, err := exec.resolveSessionV3CurrentAgentToolContract("account-1", nil, snapshot)
			if err != nil {
				t.Fatalf("resolve ordinary system agent: %v", err)
			}
			if resolved.Name != test.id || !resolved.Enabled || resolved.Prompt == "tampered" {
				t.Fatalf("resolved system profile = %+v", resolved)
			}
		})
	}
}

func TestSessionV3OrdinaryAgentResolutionKeepsCurrentAccountToolContract(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ordinary-agent-resolution.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	const accountScopeID = "account-ordinary"
	if err := agents.EnsureDefaultsForAccount(accountScopeID); err != nil {
		t.Fatalf("ensure defaults: %v", err)
	}
	current, ok, err := agents.GetProfileForAccount(accountScopeID, "finder")
	if err != nil || !ok {
		t.Fatalf("get finder: ok=%t err=%v", ok, err)
	}
	snapshot := current
	snapshot.Prompt = "persisted session prompt"
	snapshot.ToolContract = &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{
		"bash": {Enabled: pebblestore.BoolPtr(true)},
	}}
	exec := &sessionV3Executor{server: &Server{agents: agents}}
	resolved, err := exec.resolveSessionV3CurrentAgentToolContract(accountScopeID, nil, snapshot)
	if err != nil {
		t.Fatalf("resolve ordinary agent: %v", err)
	}
	if resolved.Prompt != snapshot.Prompt {
		t.Fatalf("prompt = %q, want persisted snapshot prompt", resolved.Prompt)
	}
	if !reflect.DeepEqual(resolved.ToolContract, current.ToolContract) {
		t.Fatalf("tool contract = %+v, want current account contract %+v", resolved.ToolContract, current.ToolContract)
	}
}

func TestSessionV3ProviderCheckpointOwnershipKeepsProviderContext(t *testing.T) {
	for _, job := range []sessionV3ExecutorJob{{CheckpointID: "cp-1"}, {CheckpointID: "cp-1", ResumeContext: true}} {
		scope := sessionV3ProviderJobCheckpointScope(job)
		if scope.FreshContext || sessionV3ProviderCheckpointFreshContext(job, scope) {
			t.Fatalf("checkpoint ownership unexpectedly requested fresh provider context: job=%#v scope=%#v", job, scope)
		}
	}
	if sessionV3ProviderCheckpointFreshContext(sessionV3ExecutorJob{CheckpointID: "cp-1"}, sessionV3ProviderCheckpointScope{FreshContext: true}) {
		t.Fatal("checkpoint routing metadata must not override execution-epoch lineage")
	}
}

func TestSessionV3ProviderCheckpointResumeFallsBackToParentEpochInput(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "resume-parent-context-create", "resume parent context", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})
	parent, ok, err := sessionSvc.GetActiveExecutionEpoch(created.ID)
	if err != nil || !ok {
		t.Fatalf("get parent epoch: ok=%t err=%v", ok, err)
	}
	redirect := pebblestore.MessageSnapshot{Role: "user", Content: "redirect the paused checkpoint now"}
	markSessionsV3CheckpointResumeRouting(&redirect, "resume-run", "cp-1", "resume_paused_user_message")
	result, err := sessionSvc.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{
		SessionID: created.ID, UserID: created.UserID, AccountScopeID: created.AccountScopeID,
		ClientRequestID: "resume-parent-context", PayloadHash: "resume-parent-context-hash",
		Reason: "resume_checkpoint", PlanID: "plan-1", CheckpointID: "cp-1", AttemptID: "cp-1:attempt-2",
		RunID: "resume-run", RunSessionID: created.ID, ParentSessionID: created.ID, ResumeContext: true,
		TriggerMessage: &redirect,
	})
	if err != nil {
		t.Fatalf("begin resume epoch: %v", err)
	}
	exec := newSessionV3Executor(server)
	messages, err := exec.sessionV3ProviderContextMessages(sessionV3ExecutorJob{SessionID: created.ID, RunID: "resume-run", EpochID: result.Epoch.EpochID, CheckpointID: "cp-1", ResumeContext: true})
	if err != nil {
		t.Fatalf("resume context messages: %v", err)
	}
	input := sessionsV3ProviderInput(messages)
	if len(input) == 0 {
		t.Fatal("resume provider input is empty")
	}
	for _, want := range []string{"resume parent context", "Active checkpoint resumed by this user message", "redirect the paused checkpoint now"} {
		if !sessionsV3ProviderInputContainsContentText(input, want) {
			t.Fatalf("resume provider input = %+v, want %q", input, want)
		}
	}
	if result.Epoch.ParentEpochID != parent.EpochID {
		t.Fatalf("resume parent epoch = %q, want %q", result.Epoch.ParentEpochID, parent.EpochID)
	}
}

func TestSessionV3ProviderCheckpointScopeFromCurrentContextPayloadOverridesStaleJobScope(t *testing.T) {
	scope := sessionV3ProviderCheckpointScopeFromPayload(sessionV3ProviderCheckpointScope{
		PlanID:          "old-plan",
		CheckpointID:    "cp-4",
		AttemptID:       "cp-4:attempt-1",
		ParentSessionID: "parent-old",
		FreshContext:    true,
	}, map[string]any{
		"next_action":        "run_checkpoint_with_current_context",
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

func TestSessionV3ProviderCheckpointStartupInputUsesCanonicalBuilder(t *testing.T) {
	runner := &sessionsV3ProviderToolsRunner{checkpointInputReturn: []map[string]any{{"role": "user", "content": "CANONICAL_BUILDER_INPUT"}}, checkpointInputReturnOK: true, checkpointInputReturnOKSet: true}
	exec := &sessionV3Executor{server: &Server{runner: runner}}
	input, selection, err := exec.sessionV3ProviderCheckpointStartupInput(sessionV3ExecutorJob{SessionID: "session-1", RunID: "run-1", PlanID: "plan-1", CheckpointID: "cp-1", AttemptID: "cp-1:attempt-1", SourceMessageID: "exit-plan-message"}, sessionV3ResolvedRuntime{})
	if err != nil {
		t.Fatalf("checkpoint startup input: %v", err)
	}
	if selection != sessionV3ProviderContextCheckpointStartup || !sessionsV3ProviderInputContainsContentText(input, "CANONICAL_BUILDER_INPUT") || len(runner.checkpointRequests) != 1 {
		t.Fatalf("checkpoint startup selection=%q input=%+v requests=%+v", selection, input, runner.checkpointRequests)
	}
	ctx := runner.checkpointRequests[0].PlanCheckpointContext
	if ctx == nil || ctx.PlanID != "plan-1" || ctx.CheckpointID != "cp-1" || ctx.AttemptID != "cp-1:attempt-1" || ctx.SourceMessageID != "exit-plan-message" {
		t.Fatalf("canonical checkpoint context = %+v", ctx)
	}
}

func TestSessionV3ProviderResumedCheckpointUsesReconstructedConversation(t *testing.T) {
	runner := &sessionsV3ProviderToolsRunner{checkpointInputReturn: []map[string]any{{"role": "user", "content": "ISOLATED_CHECKPOINT_INPUT"}}, checkpointInputReturnOK: true, checkpointInputReturnOKSet: true}
	exec := &sessionV3Executor{server: &Server{runner: runner}}
	input, selection, err := exec.sessionV3ProviderCheckpointStartupInput(sessionV3ExecutorJob{
		SessionID: "session-1", RunID: "run-2", PlanID: "plan-1", CheckpointID: "cp-1", AttemptID: "cp-1:attempt-2", ResumeContext: true,
	}, sessionV3ResolvedRuntime{})
	if err != nil {
		t.Fatalf("resumed checkpoint context selection: %v", err)
	}
	if selection != sessionV3ProviderContextConversation || len(input) != 0 || len(runner.checkpointRequests) != 0 {
		t.Fatalf("resumed checkpoint selection=%q input=%+v requests=%+v, want reconstructed conversation", selection, input, runner.checkpointRequests)
	}
}

func TestSessionV3ProviderInitialContextInputCheckpointStartupNeverUsesHandoff(t *testing.T) {
	const canonical = "CANONICAL_CHECKPOINT_INPUT"
	const historical = "EXIT_PLAN_MODE_HISTORY"
	exec := &sessionV3Executor{}
	messages := []pebblestore.MessageSnapshot{
		{Role: "assistant", Content: historical},
		{Role: "tool", Content: `{"tool_name":"exit_plan_mode","arguments":"HISTORICAL_PLAN_ARGUMENTS","completed_output":"HISTORICAL_PLAN_RESULT"}`},
	}
	resolved := sessionV3ResolvedRuntime{Preference: pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5"}}
	canonicalInput := []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": canonical}}}}

	for _, tc := range []struct {
		name string
		req  provideriface.Request
	}{
		{name: "same provider model", req: provideriface.Request{PreviousProviderLineageID: "same", ProviderLineageID: "same", BoundaryReason: "session_turn", NativeContinuationAllowed: true}},
		{name: "changed provider model", req: provideriface.Request{PreviousProviderLineageID: "old", ProviderLineageID: "new", BoundaryReason: "provider_model_runtime_handoff", ForceFreshProviderContext: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := exec.sessionV3ProviderInitialContextInput(sessionV3ExecutorJob{}, resolved, messages, tc.req, canonicalInput, sessionV3ProviderContextCheckpointStartup)
			if err != nil {
				t.Fatalf("checkpoint context selection: %v", err)
			}
			if !sessionsV3ProviderInputContainsContentText(input, canonical) {
				t.Fatalf("checkpoint input missing canonical payload: %+v", input)
			}
			for _, forbidden := range []string{"[provider-handoff]", historical, "exit_plan_mode", "HISTORICAL_PLAN_ARGUMENTS", "HISTORICAL_PLAN_RESULT"} {
				if sessionsV3ProviderInputContainsContentText(input, forbidden) {
					t.Fatalf("checkpoint input replayed %q: %+v", forbidden, input)
				}
			}
			if tc.name == "changed provider model" && (!tc.req.ForceFreshProviderContext || tc.req.NativeContinuationAllowed) {
				t.Fatal("changed-model checkpoint test did not preserve fresh-chain policy")
			}
		})
	}
}

func TestSessionV3ProviderInitialContextInputOrdinarySwitchUsesHandoff(t *testing.T) {
	exec := &sessionV3Executor{}
	resolved := sessionV3ResolvedRuntime{Preference: pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5"}}
	messages := []pebblestore.MessageSnapshot{{Role: "user", Content: "ordinary switched request"}}
	input, err := exec.sessionV3ProviderInitialContextInput(sessionV3ExecutorJob{}, resolved, messages, provideriface.Request{
		PreviousProviderLineageID: "old", ProviderLineageID: "new", BoundaryReason: "provider_model_runtime_handoff",
		PreviousProviderID: "anthropic", PreviousModel: "claude", NewProviderID: "codex", NewModel: "gpt-5", ForceFreshProviderContext: true,
	}, nil, sessionV3ProviderContextConversation)
	if err != nil {
		t.Fatalf("ordinary provider/model switch: %v", err)
	}
	if !sessionsV3ProviderInputContainsContentText(input, "[provider-handoff]") || !sessionsV3ProviderInputContainsContentText(input, "ordinary switched request") {
		t.Fatalf("ordinary switch did not use compact handoff input: %+v", input)
	}
}

func TestSessionV3ProviderCheckpointRestartInputUsesFreshPayloadCheckpointOverJobCheckpoint(t *testing.T) {
	runner := &sessionsV3ProviderToolsRunner{}
	exec := &sessionV3Executor{server: &Server{runner: runner}}
	toolOutput := `{"next_action":"run_checkpoint_with_current_context","next_checkpoint_id":"cp-5","run_request":{"plan_checkpoint_context":{"plan_id":"replacement-plan","checkpoint_id":"cp-5","attempt_id":"cp-5:attempt-1","parent_session_id":"parent-new"}}}`

	input, ok, err := exec.sessionV3ProviderCheckpointRestartInput(context.Background(), sessionV3ExecutorJob{
		SessionID:       "session-1",
		RunID:           "run-1",
		PlanID:          "old-plan",
		CheckpointID:    "cp-4",
		AttemptID:       "cp-4:attempt-1",
		ParentSessionID: "parent-old",
		SourceMessageID: "message-new-direction",
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
	if ctx.PlanID != "replacement-plan" || ctx.CheckpointID != "cp-5" || ctx.AttemptID != "cp-5:attempt-1" || ctx.ParentSessionID != "parent-new" || ctx.SourceMessageID != "message-new-direction" {
		t.Fatalf("PlanCheckpointContext = %+v, want fresh payload context with source message provenance", *ctx)
	}
}

func TestSessionV3LatestPlanManageToolPayloadUsesMessageTail(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	created, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     "session-tail-plan-payload",
		Title:         "tail plan payload",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "test-provider",
			Model:    "test-model",
			Thinking: "low",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	stalePayload := `{"next_action":"run_checkpoint_with_current_context","next_checkpoint_id":"followup-1","run_request":{"plan_checkpoint_context":{"plan_id":"plan-1","checkpoint_id":"followup-1","attempt_id":"followup-1:attempt-1","parent_session_id":"parent-stale"}}}`
	latestPayload := `{"next_action":"run_checkpoint_with_current_context","next_checkpoint_id":"cp-3","run_request":{"plan_checkpoint_context":{"plan_id":"plan-1","checkpoint_id":"cp-3","attempt_id":"cp-3:attempt-1","parent_session_id":"parent-current"}}}`
	mutationSeq := 0
	appendMessage := func(role, content string) {
		t.Helper()
		mutationSeq++
		requestID := fmt.Sprintf("append-%03d", mutationSeq)
		if _, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
			SessionID:       created.ID,
			ClientRequestID: requestID,
			IdempotencyKey:  requestID,
			PayloadHash:     requestID,
			Kind:            sessionruntime.SessionMutationAppendMessage,
			Message: &pebblestore.MessageSnapshot{
				Role:    role,
				Content: content,
			},
		}); err != nil {
			t.Fatalf("append %s message: %v", role, err)
		}
	}
	appendToolResult := func(content string) {
		t.Helper()
		appendMessage("tool", content)
	}
	appendToolResult(providerToolResultRecordJSON("plan_manage", stalePayload))
	for i := 0; i < 70; i++ {
		appendMessage("system", fmt.Sprintf("filler %02d", i))
	}
	appendToolResult(providerToolResultRecordJSON("plan_manage", latestPayload))

	exec := &sessionV3Executor{server: &Server{sessions: sessionSvc}}
	payload := exec.sessionV3LatestPlanManageToolPayload(sessionV3ExecutorJob{SessionID: created.ID})
	if got := sessionsV3MapString(payload, "next_checkpoint_id"); got != "cp-3" {
		t.Fatalf("next_checkpoint_id = %q, want cp-3; payload=%v", got, payload)
	}
	request, ok := payload["run_request"].(map[string]any)
	if !ok {
		t.Fatalf("run_request missing from payload: %v", payload)
	}
	context, ok := request["plan_checkpoint_context"].(map[string]any)
	if !ok {
		t.Fatalf("plan_checkpoint_context missing from payload: %v", payload)
	}
	if got := sessionsV3MapString(context, "checkpoint_id"); got != "cp-3" {
		t.Fatalf("checkpoint_id = %q, want cp-3; payload=%v", got, payload)
	}
}

func providerToolResultRecordJSON(toolName, completedOutput string) string {
	payload, err := json.Marshal(sessionV3ProviderToolResultRecord{
		PathID:          "run.v3.provider-tool-result.v1",
		ToolName:        toolName,
		CallID:          "call-" + toolName,
		CompletedOutput: completedOutput,
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func TestSessionV3CancelRunPausesMatchingPlanFromDurableIntentAndPublishes(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "cancel-plan-create", "cancel plan")
	const runID = "run-cancel-plan"
	const planID = "plan-cancel"
	const checkpointID = "cp-1"
	const attemptID = "cp-1:attempt-1"
	_, _, err := sessionSvc.SavePlanWithMetadata(created.ID, planID, "Plan: cancel", "## Plan: cancel", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID: planID, Title: "Plan: cancel",
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: attemptID, ParentSessionID: created.ID, CurrentSessionID: created.ID, CurrentRunID: runID},
		ActiveCheckpointID: checkpointID,
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: checkpointID, Title: "Cancel me", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: attemptID, RunID: runID, SessionID: created.ID, Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: attemptID, CheckpointID: checkpointID, Status: sessionruntime.PlanCheckpointStatusInProgress, RunID: runID, SessionID: created.ID, ParentSessionID: created.ID}}}},
	}})
	if err != nil {
		t.Fatalf("save active plan: %v", err)
	}
	now := time.Now().UnixMilli()
	pending := pebblestore.V3SessionRunIntent{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, RunID: runID, Status: sessionruntime.RunIntentPendingExecutor, PlanID: planID, CheckpointID: checkpointID, AttemptID: attemptID, RunSessionID: created.ID, ParentSessionID: created.ID, UpdatedAt: now}
	payloadHash, err := sessionV3ExecutorPayloadHash(created.ID, runID, pending.Status, "", "session.assistant.queued", "")
	if err != nil {
		t.Fatalf("hash pending intent: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ClientRequestID: "cancel-plan-queued", IdempotencyKey: "cancel-plan-queued", PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.assistant.queued", RunIntent: &pending, NowUnixMs: now}); err != nil {
		t.Fatalf("record pending intent: %v", err)
	}
	exec := &sessionV3Executor{server: server, runStates: make(map[string]*sessionV3ExecutorRunState)}
	server.v3SessionExecutor = exec
	result, cancelled, err := exec.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: runID}, "user stopped")
	if err != nil || !cancelled || result.RunIntent == nil || result.RunIntent.Status != sessionruntime.RunIntentCancelled {
		t.Fatalf("cancel result=%#v cancelled=%t err=%v", result, cancelled, err)
	}
	active, ok, err := sessionSvc.GetActivePlan(created.ID)
	if err != nil || !ok || active.Document == nil {
		t.Fatalf("get reconciled plan: ok=%t err=%v plan=%#v", ok, err, active)
	}
	checkpoint := active.Document.Checkpoints[0]
	if active.Document.ExecutionState.Status != sessionruntime.PlanExecutionStatePaused || checkpoint.Status != sessionruntime.PlanCheckpointStatusPaused || checkpoint.Attempts[0].Status != sessionruntime.PlanCheckpointStatusPaused || checkpoint.Result != "run_paused" {
		t.Fatalf("reconciled plan = %#v", active.Document)
	}
	assertSessionsV3PlanSavedOutboxState(t, sessionSvc, created.ID, sessionruntime.PlanExecutionStatePaused)

	version := active.Version
	if _, cancelled, err := exec.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: runID}, "user stopped"); err != nil || !cancelled {
		t.Fatalf("repeat cancel cancelled=%t err=%v", cancelled, err)
	}
	repeated, _, err := sessionSvc.GetActivePlan(created.ID)
	if err != nil {
		t.Fatalf("get repeated plan: %v", err)
	}
	if repeated.Version != version {
		t.Fatalf("repeat cancellation changed plan version from %d to %d", version, repeated.Version)
	}
}

func TestSessionV3CancelRunReconcilesCheckpointOwnershipAddedDuringRun(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "cancel-late-owned-create", "cancel late-owned plan")
	const runID = "run-cancel-late-owned"
	const planID = "plan-cancel-late-owned"
	const checkpointID = "cp-1"
	const attemptID = "cp-1:attempt-1"
	now := time.Now().UnixMilli()
	pending := pebblestore.V3SessionRunIntent{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, RunID: runID, Status: sessionruntime.RunIntentPendingExecutor, RunSessionID: created.ID, UpdatedAt: now}
	payloadHash, err := sessionV3ExecutorPayloadHash(created.ID, runID, pending.Status, "", "session.assistant.queued", "")
	if err != nil {
		t.Fatalf("hash pending intent: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ClientRequestID: "cancel-late-owned-queued", IdempotencyKey: "cancel-late-owned-queued", PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.assistant.queued", RunIntent: &pending, NowUnixMs: now}); err != nil {
		t.Fatalf("record pending intent: %v", err)
	}
	_, _, err = sessionSvc.SavePlanWithMetadata(created.ID, planID, "Plan: cancel late-owned", "## Plan: cancel late-owned", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID: planID, Title: "Plan: cancel late-owned",
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: attemptID, ParentSessionID: created.ID, CurrentSessionID: created.ID, CurrentRunID: runID},
		ActiveCheckpointID: checkpointID,
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: checkpointID, Title: "Cancel me", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: attemptID, RunID: runID, SessionID: created.ID, Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: attemptID, CheckpointID: checkpointID, Status: sessionruntime.PlanCheckpointStatusInProgress, RunID: runID, SessionID: created.ID, ParentSessionID: created.ID}}}},
	}})
	if err != nil {
		t.Fatalf("save late-owned plan: %v", err)
	}
	exec := &sessionV3Executor{server: server, runStates: make(map[string]*sessionV3ExecutorRunState)}
	server.v3SessionExecutor = exec
	result, cancelled, err := exec.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: runID}, "user stopped")
	if err != nil || !cancelled || result.RunIntent == nil || result.RunIntent.Status != sessionruntime.RunIntentCancelled {
		t.Fatalf("cancel result=%#v cancelled=%t err=%v", result, cancelled, err)
	}
	active, ok, err := sessionSvc.GetActivePlan(created.ID)
	if err != nil || !ok || active.Document == nil {
		t.Fatalf("get reconciled plan: ok=%t err=%v plan=%#v", ok, err, active)
	}
	checkpoint := active.Document.Checkpoints[0]
	if active.Document.ExecutionState.Status != sessionruntime.PlanExecutionStatePaused || checkpoint.Status != sessionruntime.PlanCheckpointStatusPaused || checkpoint.Attempts[0].Status != sessionruntime.PlanCheckpointStatusPaused || checkpoint.Result != "run_paused" {
		t.Fatalf("late-owned cancellation was not reconciled: %#v", active.Document)
	}
}

func TestSessionV3CancelRunDoesNotMutateUnrelatedPlanOwnership(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "cancel-stale-create", "cancel stale")
	saveSessionsV3ActivePlanForFinalizationTest(t, sessionSvc, created.ID, sessionruntime.PlanExecutionPolicyModeAutomatic, []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Active", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: "cp-1:attempt-1", RunID: "run-finalize", SessionID: created.ID, Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-1:attempt-1", CheckpointID: "cp-1", Status: sessionruntime.PlanCheckpointStatusInProgress, RunID: "run-finalize", SessionID: created.ID, ParentSessionID: created.ID}}}})
	before, _, _ := sessionSvc.GetActivePlan(created.ID)
	now := time.Now().UnixMilli()
	pending := pebblestore.V3SessionRunIntent{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, RunID: "stale-run", Status: sessionruntime.RunIntentPendingExecutor, PlanID: "plan-finalize", CheckpointID: "cp-1", AttemptID: "cp-1:attempt-1", RunSessionID: created.ID, ParentSessionID: created.ID, UpdatedAt: now}
	payloadHash, err := sessionV3ExecutorPayloadHash(created.ID, pending.RunID, pending.Status, "", "session.assistant.queued", "")
	if err != nil {
		t.Fatalf("hash stale intent: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ClientRequestID: "cancel-stale-queued", IdempotencyKey: "cancel-stale-queued", PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.assistant.queued", RunIntent: &pending, NowUnixMs: now}); err != nil {
		t.Fatalf("record stale intent: %v", err)
	}
	exec := &sessionV3Executor{server: server, runStates: make(map[string]*sessionV3ExecutorRunState)}
	server.v3SessionExecutor = exec
	if _, cancelled, err := exec.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: pending.RunID}, "stale stop"); err != nil || !cancelled {
		t.Fatalf("cancel stale run cancelled=%t err=%v", cancelled, err)
	}
	after, _, err := sessionSvc.GetActivePlan(created.ID)
	if err != nil {
		t.Fatalf("get plan after stale cancel: %v", err)
	}
	if after.Version != before.Version || after.Document.ExecutionState.Status != sessionruntime.PlanExecutionStateInProgress || after.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusInProgress {
		t.Fatalf("stale cancellation changed plan: before=%#v after=%#v", before, after)
	}
}

func TestResolveSessionV3EffectivePreferenceUsesImmutableModeSelection(t *testing.T) {
	profile := &pebblestore.SessionModelProfileSnapshot{
		AppliedAt: 77,
		Action:    pebblestore.ModelProfileSelection{Provider: "codex", Model: "action-model", Thinking: "high", ServiceTier: "priority", ContextMode: "1m"},
		Plan:      &pebblestore.ModelProfileSelection{Provider: "openai", Model: "plan-model", Thinking: "medium", ServiceTier: "flex", ContextMode: "compact"},
	}
	for _, test := range []struct {
		mode string
		want pebblestore.ModelPreference
	}{
		{mode: sessionruntime.ModeAuto, want: pebblestore.ModelPreference{Provider: "codex", Model: "action-model", Thinking: "high", ServiceTier: "priority", ContextMode: "1m"}},
		{mode: sessionruntime.ModePlan, want: pebblestore.ModelPreference{Provider: "openai", Model: "plan-model", Thinking: "medium", ServiceTier: "flex", ContextMode: "compact"}},
	} {
		t.Run(test.mode, func(t *testing.T) {
			session := pebblestore.SessionSnapshot{Mode: test.mode, ModelProfile: pebblestore.CloneSessionModelProfileSnapshot(profile), Preference: test.want}
			got, err := resolveSessionV3EffectivePreference(session, pebblestore.AgentProfile{Name: agentruntime.SwarmAgentID})
			if err != nil {
				t.Fatalf("resolve effective preference: %v", err)
			}
			if got.Provider != test.want.Provider || got.Model != test.want.Model || got.Thinking != test.want.Thinking || got.ServiceTier != test.want.ServiceTier || got.ContextMode != test.want.ContextMode {
				t.Fatalf("effective preference = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestResolveSessionV3EffectivePreferenceUsesSystemAgentAssignmentOverParentModelProfile(t *testing.T) {
	session := pebblestore.SessionSnapshot{
		Mode:       sessionruntime.ModeAuto,
		Preference: pebblestore.ModelPreference{Provider: "codex", Model: "parent-action", Thinking: "medium"},
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{
			Action: pebblestore.ModelProfileSelection{Provider: "codex", Model: "parent-action", Thinking: "medium"},
		},
	}
	agent := agentruntime.DesignerAgentProfileForParent(pebblestore.AgentProfile{Provider: "codex", Model: "gpt-5.6-sol", Thinking: "high"})
	got, err := resolveSessionV3EffectivePreference(session, agent)
	if err != nil {
		t.Fatalf("resolve Designer preference: %v", err)
	}
	if got.Provider != "codex" || got.Model != "gpt-5.6-sol" || got.Thinking != "high" {
		t.Fatalf("Designer effective preference = %#v, want canonical system-agent assignment", got)
	}
}

func TestResolveSessionV3EffectivePreferenceRejectsDisabledPlanAndIgnoresPreferenceDrift(t *testing.T) {
	profile := &pebblestore.SessionModelProfileSnapshot{Action: pebblestore.ModelProfileSelection{Provider: "codex", Model: "action-model"}}
	if _, err := resolveSessionV3EffectivePreference(pebblestore.SessionSnapshot{Mode: sessionruntime.ModePlan, ModelProfile: profile}, pebblestore.AgentProfile{Name: agentruntime.SwarmAgentID}); err == nil || !strings.Contains(err.Error(), "Plan mode disabled") {
		t.Fatalf("disabled Plan error = %v", err)
	}
	profile.Plan = &pebblestore.ModelProfileSelection{Provider: "openai", Model: "plan-model"}
	got, err := resolveSessionV3EffectivePreference(pebblestore.SessionSnapshot{Mode: sessionruntime.ModeAuto, ModelProfile: profile, Preference: pebblestore.ModelPreference{Provider: "openai", Model: "drift"}}, pebblestore.AgentProfile{Name: agentruntime.SwarmAgentID})
	if err != nil || got.Provider != "codex" || got.Model != "action-model" {
		t.Fatalf("snapshot-bound preference = %#v err=%v, want codex/action-model", got, err)
	}
}

func TestHydrateSessionV3ExecutorJobKeepsRunIdentityForSnapshotBoundRecovery(t *testing.T) {
	intent := pebblestore.V3SessionRunIntent{SessionID: "session-1", RunID: "run-1", EpochID: "epoch-1", PlanID: "plan-1", CheckpointID: "cp-1", AttemptID: "attempt-1", RunSessionID: "session-1", ParentSessionID: "parent-1", ResumeContext: true}
	job := hydrateSessionV3ExecutorJobFromIntent(sessionV3ExecutorJob{SessionID: intent.SessionID, RunID: intent.RunID}, intent)
	if job.SessionID != intent.SessionID || job.RunID != intent.RunID || job.PlanID != intent.PlanID || job.CheckpointID != intent.CheckpointID || job.AttemptID != intent.AttemptID || !job.ResumeContext {
		t.Fatalf("hydrated recovery job = %#v, want durable intent identity %#v", job, intent)
	}
}

func TestSessionV3ProviderRequestAndMetadataAgreeWithResolvedPreference(t *testing.T) {
	preference := pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "high", ServiceTier: "priority", ContextMode: "1m"}
	response := sessionV3AssistantResponse{ProviderID: preference.Provider, Model: preference.Model, Thinking: preference.Thinking, ServiceTier: preference.ServiceTier, ContextMode: preference.ContextMode}
	metadata := response.metadata("run-1")
	for key, want := range map[string]string{"provider": preference.Provider, "model": preference.Model, "thinking": preference.Thinking, "service_tier": preference.ServiceTier, "context_mode": preference.ContextMode} {
		if got := fmt.Sprint(metadata[key]); got != want {
			t.Fatalf("metadata[%q] = %q, want %q", key, got, want)
		}
	}
	instructions := runruntime.AppendResolvedModelPolicyInstructions("base", sessionruntime.ModePlan, preference)
	for _, want := range []string{"session_mode: plan", "provider: codex", "model: gpt-5.4", "thinking: high", "service_tier: priority", "context_mode: 1m"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q: %s", want, instructions)
		}
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

func TestSessionV3ToolNamesExceptUsesContractCanonicalNames(t *testing.T) {
	tools := []provideriface.ToolDefinition{
		{Name: "ask-user"},
		{Name: "manage-sessions"},
		{Name: "manage-skill"},
		{Name: "manage-theme"},
		{Name: "manage-worktree"},
		{Name: "skill-use"},
		{Name: "compact"},
	}
	allowed := sessionV3ToolNamesExcept(tools, "compact")
	for _, name := range []string{"ask_user", "manage_sessions", "manage_skill", "manage_theme", "manage_worktree", "skill_use"} {
		if _, ok := allowed[name]; !ok {
			t.Fatalf("allowed names omit canonical %q: %v", name, allowed)
		}
	}
	for _, rawName := range []string{"ask-user", "manage-sessions", "manage-skill", "manage-theme", "manage-worktree", "skill-use", "compact"} {
		if _, ok := allowed[rawName]; ok {
			t.Fatalf("allowed names retain raw provider name %q: %v", rawName, allowed)
		}
	}
}

func TestResolveSessionV3ProviderToolsCanonicalizesDefinitionNames(t *testing.T) {
	runner := &sessionsV3ProviderToolsRunner{
		definitions: []tool.Definition{
			{Type: "function", Name: "ask-user"},
			{Type: "function", Name: "bash"},
			{Type: "function", Name: "manage-agent"},
			{Type: "function", Name: "manage-skill"},
			{Type: "function", Name: "manage-sessions"},
			{Type: "function", Name: "manage-worktree"},
			{Type: "function", Name: "skill-use"},
		},
		contract: runruntime.ResolvedAgentToolContract{Tools: map[string]runruntime.ResolvedAgentTool{
			"ask_user":        {Enabled: true},
			"bash":            {Enabled: true},
			"manage_agent":    {Enabled: true},
			"manage_skill":    {Enabled: true},
			"manage_sessions": {Enabled: true},
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
	expected := []string{"ask-user", "bash", "manage-agent", "manage-sessions", "manage-skill", "manage-worktree", "skill-use"}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("provider tool names mismatch\n got: %v\nwant: %v", names, expected)
	}
}
