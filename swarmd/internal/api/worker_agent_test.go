package api

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"strings"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the public MCP and REST save boundaries must persist explicit tool
// switches and reject unsafe reviewer profiles without creating a worker.
// controlMCPDefineAgent, handleWorkerCollection and ValidateWorkerAgent own this
// contract; real API dispatch and Pebble are the narrowest integration layer.
func TestMCPWorkerCustomAgentSave(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	c.request = requestWithScopedToken(c.request, &store.ScopedTokenRecord{Scopes: []string{"admin"}})
	account := testPrincipal().AccountScopeID
	define := func(off bool) store.AgentProfile {
		t.Helper()
		args := map[string]any{"name": "custom-tools", "instructions": "Custom worker guidance", "tools": "build"}
		if off {
			args["tools_off"] = []any{"bash"}
			args["tools_on"] = []any{"websearch"}
		}
		if _, err := controlMCPDefineAgent(c, args); err != nil {
			t.Fatal(err)
		}
		profile, found, err := c.server.agents.GetProfileForAccount(account, "custom-tools")
		if err != nil || !found {
			t.Fatalf("profile missing %v", err)
		}
		return profile
	}
	baseline := define(false)
	if baseline.ToolContract.Tools["bash"].Enabled == nil || !*baseline.ToolContract.Tools["bash"].Enabled {
		t.Fatal("legacy build contract changed")
	}
	narrowed := define(true)
	if narrowed.ToolContract.Tools["bash"].Enabled == nil || *narrowed.ToolContract.Tools["bash"].Enabled {
		t.Fatal("bash switch not persisted")
	}
	if reflect.DeepEqual(baseline.ToolContract, narrowed.ToolContract) {
		t.Fatal("switch had no effect")
	}
	_, err := c.dispatch(http.MethodPost, "/v3/workers", nil, map[string]any{"idempotency_key": "unsafe-reviewer", "name": "unsafe-reviewer", "role": "reviewer", "agent": "custom-tools", "instructions": "Review only"})
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Fatalf("reviewer save not refused %v", err)
	}
	result, err := ws.ListWorkers(account, store.ListWorkersQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, worker := range result.Workers {
		if worker.Name == "unsafe-reviewer" {
			t.Fatal("rejected reviewer persisted")
		}
	}
	_, err = c.dispatch(http.MethodPost, "/v3/workers", nil, map[string]any{"idempotency_key": "custom-coder", "name": "custom-coder", "role": "coder", "agent": "custom-tools", "instructions": "Implement"})
	if err != nil {
		t.Fatal(err)
	}
	result, err = ws.ListWorkers(account, store.ListWorkersQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var saved store.WorkerRecord
	for _, worker := range result.Workers {
		if worker.Name == "custom-coder" {
			saved = worker
		}
	}
	if saved.Agent != "custom-tools" {
		t.Fatal("worker agent was not persisted")
	}
	base, err := c.server.agents.ResolveSystemAgent("system-worker-coder", store.AgentProfile{})
	if err != nil {
		t.Fatal(err)
	}
	executor := &sessionV3Executor{server: c.server}
	metadata := map[string]any{"worker_id": saved.ID, "worker_revision": saved.Revision, "worker_execution_run_id": "fixture-run"}
	current, err := executor.resolveSessionV3CurrentAgentToolContract(account, metadata, base)
	if err != nil {
		t.Fatal(err)
	}
	if current.Name != base.Name || *current.ToolContract.Tools["bash"].Enabled {
		t.Fatal("reconciliation lost worker overlay")
	}
	// Purpose: prove the provider-facing definitions and real invoker enforce
	// the saved worker overlay, rather than merely checking compiler flags.
	definitions, err := executor.resolveSessionV3ProviderTools(account, current)
	if err != nil {
		t.Fatal(err)
	}
	offered := sessionV3OfferedToolNames(definitions)
	if slices.Contains(offered, "bash") || !slices.Contains(offered, "websearch") {
		t.Fatalf("offered tools %v", offered)
	}
	session, _, err := c.server.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{Preference: &store.ModelPreference{Provider: "fixture-provider", Model: "fixture-model", Thinking: "high"}, SessionID: "worker-tools-fixture", UserID: testPrincipal().UserID, AccountScopeID: account, WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var refusal string
	invoker := c.server.runner.(*runruntime.Service).NewProviderManagedToolInvoker(runruntime.ProviderManagedToolInvokerConfig{SessionID: session.ID, OfferedTools: offered, Emit: func(event runruntime.StreamEvent) {
		if event.Metadata != nil {
			refusal, _ = event.Metadata["refusal"].(string)
		}
	}})
	toolResult, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{Name: "bash", CallID: "denied-bash", Arguments: `{"command":"exit 99"}`})
	if err != nil || toolResult.Error == "" || refusal != runruntime.ToolNotOfferedRefusal {
		t.Fatalf("bash refusal result=%+v err=%v refusal=%q", toolResult, err, refusal)
	}
	define(false)
	current, err = executor.resolveSessionV3CurrentAgentToolContract(account, metadata, base)
	if err != nil || !*current.ToolContract.Tools["bash"].Enabled {
		t.Fatalf("next run did not refresh tools %v", err)
	}
	own, err := executor.resolveSessionV3CurrentAgentToolContract(account, nil, baseline)
	if err != nil || !reflect.DeepEqual(own.ToolContract, baseline.ToolContract) {
		t.Fatalf("custom agent session changed %v", err)
	}
}

// Purpose: automation v2 shares worker_id but not worker execution authority.
// The current-contract resolver must preserve its system contract without
// consulting worker revisions; this is the narrowest regression boundary.
func TestAutomationV2WorkerIDDoesNotOverlayAgent(t *testing.T) {
	c, _ := newMCPPresetTestCall(t)
	base, err := c.server.agents.ResolveSystemAgent("swarm", store.AgentProfile{})
	if err != nil {
		t.Fatal(err)
	}
	executor := &sessionV3Executor{server: c.server}
	expected, err := executor.resolveSessionV3CurrentAgentToolContract(testPrincipal().AccountScopeID, nil, base)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := executor.resolveSessionV3CurrentAgentToolContract(testPrincipal().AccountScopeID, map[string]any{"worker_id": "automation-fixture", "automation_v2_run_id": "automation-run"}, base)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("automation changed %v", err)
	}
	_, err = executor.resolveSessionV3CurrentAgentToolContract(testPrincipal().AccountScopeID, map[string]any{"worker_id": "worker-fixture", "worker_execution_run_id": "worker-run"}, base)
	if err == nil || !strings.Contains(err.Error(), "invalid worker revision") {
		t.Fatalf("malformed actual worker accepted %v", err)
	}
}
