package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// recordingCatalogActions is the account's catalog action service
// (api.Server.ManageCatalogAction) behind manage_fleet status on this machine.
type recordingCatalogActions struct {
	recordingWorkflowLifecycle
	catalogCalls int
	who, name    string
}

func (r *recordingCatalogActions) ManageCatalogAction(_ context.Context, _ identity.Principal, who, toolName string, args map[string]any) (any, error) {
	r.catalogCalls++
	r.who, r.name = who, toolName
	return map[string]any{"status": "ok", "action": args["action"]}, nil
}

// Purpose:
//   - Requirement: the Orchestrator's manage_fleet (status for this machine),
//     which the run executes on its generic tool path (no control-plane case),
//     reaches the catalog service: the run passes the session's agent to the
//     tool runtime, whose Orchestrator-only gate (catalogOrchestratorOnly)
//     reads it. Another agent's same call is refused and never reaches it.
//   - Threat/regression: the generic path built its runtime context without
//     the agent, so the real Orchestrator was refused every manage_fleet,
//     manage_designs, linked manage_media and task work read; tests that
//     injected the agent themselves hid it.
//   - Boundary/authority: Service.executeProviderManagedToolCall (the
//     provider tool path every provider-managed run uses) with the real
//     permission service, session store and tool.Runtime, and a recording
//     CatalogActionService.
//   - Narrowest layer: the run's own tool entry point; no provider runs.
func TestRunToolPathPassesTheSessionAgentToCatalogTools(t *testing.T) {
	s, _, db := taskFallbackFixture(t)
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	ss := pebblestore.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(ss, events)
	runtime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = runtime.Close() })
	s.tools = runtime
	permissions := permission.NewService(pebblestore.NewPermissionStore(db), events, nil)
	if _, err := permissions.UpsertRule(permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: tool.ManageFleetToolName}); err != nil {
		t.Fatal(err)
	}
	s.permissions = permissions
	service := &recordingCatalogActions{}
	s.SetWorkflowLifecycleService(service)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner"}
	workspace := t.TempDir()
	if err := ss.CreateSession(pebblestore.SessionSnapshot{ID: "orch", AccountScopeID: p.AccountScopeID, UserID: p.UserID, WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}
	call := func(agent string) tool.Result {
		t.Helper()
		ctx, cancel := context.WithTimeout(identity.ContextWithPrincipal(context.Background(), p), 20*time.Second)
		defer cancel()
		config := providerToolInvokerConfig{sessionID: "orch", runID: "run-1", step: 1, sessionMode: "auto+bypass_permissions", workspacePath: workspace, workspaceRoots: []string{workspace}, principal: p, agentProfile: pebblestore.AgentProfile{Name: agent}}
		result, _, err := s.executeProviderManagedToolCall(ctx, config, tool.Call{CallID: "c-" + agent, Name: tool.ManageFleetToolName, Arguments: `{"action":"status"}`}, nil)
		if err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		return result
	}
	result := call(agentruntime.SwarmOrchestratorAgentID)
	if result.Error != "" || service.catalogCalls != 1 || service.who != "orch" || service.name != tool.ManageFleetToolName {
		t.Fatalf("the Orchestrator's manage_fleet status: %+v (service calls %d, who %q)", result, service.catalogCalls, service.who)
	}
	if err := tool.CheckEnvelope(result.Output); err != nil {
		t.Fatalf("answer is not a catalog envelope: %v\n%s", err, result.Output)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result.Output), &envelope); err != nil || envelope["tool"] != tool.ManageFleetToolName || envelope["action"] != "status" {
		t.Fatalf("envelope: %s %v", result.Output, err)
	}
	for _, agent := range []string{"swarm", agentruntime.DesignerAgentID, "my-custom-agent"} {
		if refused := call(agent); !strings.Contains(refused.Error, "Orchestrator's") {
			t.Fatalf("%s ran manage_fleet status: %+v", agent, refused)
		}
	}
	if service.catalogCalls != 1 {
		t.Fatalf("a refused agent reached the catalog service (%d calls)", service.catalogCalls)
	}
}
