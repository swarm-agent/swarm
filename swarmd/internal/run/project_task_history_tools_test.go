package run

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: a retained Auto Swarm follow-up must expose callable paginated history,
// not project mutation authority. Threats are forged links, foreign principals,
// and mutation calls despite schema restrictions. The temporary durable store,
// resolved provider inventory and real invoker/runtime are the narrowest joined
// boundary; no provider or live-daemon success is claimed by this fixture.
func TestTaskFollowupHistoryProviderAccess(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ss := pebblestore.NewSessionStore(db)
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewService(ss, events)
	rt := tool.NewRuntime(1)
	rt.SetManageSessionService(sessions)
	rt.SetManageProjectStore(ss)
	permissions := permission.NewService(pebblestore.NewPermissionStore(db), events, nil)
	permissions.SetBypassPermissions(true)
	svc := NewService(sessions, nil, nil, rt, permissions, nil, nil, events)
	if err := ss.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if err := ss.PutProjectTask("account", &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "coder", Status: "needs_review", SessionID: "original"}); err != nil {
		t.Fatal(err)
	}
	task, err := ss.ReserveTaskFollowup("account", "project", "task", "user", "request-key", "follow-up", 1, 200)
	if err != nil {
		t.Fatal(err)
	}
	a := task.ActiveAttempt()
	workspace := t.TempDir()
	snapshot := pebblestore.SessionSnapshot{ID: a.SessionID, AccountScopeID: "account", UserID: "user", Mode: "auto", WorkspacePath: workspace, Metadata: map[string]any{"project_id": "project", "task_id": "task", "task_attempt_id": a.ID, "resolved_agent_name": "swarm", "agent_mode": "primary", "role": "project_task"}}
	if err := ss.CreateSession(snapshot); err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{Type: "user", UserID: "user", AccountScopeID: "account"}
	scope := tool.WorkspaceScope{SessionID: snapshot.ID, Principal: principal}
	base := agent.SwarmAgentProfileForContext(pebblestore.AgentProfile{})
	profile, defs, err := svc.ResolveTaskHistoryTools(scope, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ToolContract.Tools["manage_projects"].Enabled == nil || !*profile.ToolContract.Tools["manage_projects"].Enabled {
		t.Fatal("retained follow-up capability absent")
	}
	_, policy, disabled, err := svc.compileResolvedAgentToolContract("account", profile)
	if err != nil {
		t.Fatal(err)
	}
	if disabled["manage_projects"] {
		t.Fatal("compiled history tool disabled")
	}
	found := false
	for _, d := range defs {
		if canonicalToolName(d.Name) == "manage_projects" {
			found = true
			properties := d.Parameters["properties"].(map[string]any)
			enum := properties["action"].(map[string]any)["enum"].([]string)
			if len(enum) != 1 || enum[0] != "get_task" {
				t.Fatal("mutation schema exposed")
			}
		}
	}
	if !found {
		t.Fatal("provider cannot retrieve history")
	}
	boundedCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx := identity.ContextWithPrincipal(boundedCtx, principal)
	config := providerToolInvokerConfig{sessionID: snapshot.ID, sessionMode: "auto", workspacePath: workspace, principal: principal, agentProfile: profile, policy: policy}
	invoker := svc.newProviderToolInvoker(config)
	for cursor := 0; cursor < 2; cursor++ {
		args, _ := json.Marshal(map[string]any{"action": "get_task", "project_id": "project", "task_id": "task", "cursor": cursor, "limit": 1})
		result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: "history", Name: "manage_projects", Arguments: string(args)})
		if err != nil || result.Error != "" {
			t.Fatalf("history invocation: %+v %v", result, err)
		}
		var page struct {
			Task pebblestore.ProjectTaskRecord `json:"task"`
			Next int                           `json:"next_cursor"`
		}
		if err := json.Unmarshal([]byte(result.Output), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Task.Attempts) != 1 || page.Task.Attempts[0].ID != task.Attempts[cursor].ID || (cursor == 0 && page.Next != 1) || (cursor == 1 && page.Next != 0) {
			t.Fatalf("wrong retained page: %+v", page)
		}
	}
	before, _ := json.Marshal(task)
	// Runtime defense must survive bypassing provider schema/invoker filtering.
	runtimeScope := scope
	runtimeScope.TaskHistoryOnly = true
	for _, args := range []string{`{"action":"delete_task","project_id":"project","task_id":"task","expected_revision":2}`, `{"action":"get_task","project_id":"project","task_id":"other"}`, `{"action":"get_task","project_id":"project","task_id":"task","limit":51}`} {
		results := rt.ExecuteBatch(tool.WithWorkspaceScope(ctx, runtimeScope), workspace, []tool.Call{{CallID: "denied-runtime", Name: "manage_projects", Arguments: args}})
		if len(results) != 1 || results[0].Error == "" {
			t.Fatalf("runtime accepted forbidden call: %+v", results)
		}
	}
	for _, args := range []string{`{"action":"delete_task","project_id":"project","task_id":"task","expected_revision":2}`, `{"action":"get_task","project_id":"forged","task_id":"task"}`, `{"action":"get_task","project_id":"project","task_id":"other"}`} {
		result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{Name: "manage_projects", Arguments: args})
		if err != nil || result.Error == "" {
			t.Fatalf("forbidden invocation accepted: %+v %v", result, err)
		}
	}
	spoof := snapshot
	spoof.ID = "unassociated"
	if err := ss.CreateSession(spoof); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"foreign", "missing", "child", "unassociated"} {
		badScope, badProfile := scope, base
		if variant == "foreign" {
			badScope.Principal.AccountScopeID = "foreign"
		}
		if variant == "missing" {
			badScope.SessionID = "unlinked"
		}
		if variant == "child" {
			badProfile.Mode = "subagent"
		}
		if variant == "unassociated" {
			badScope.SessionID = spoof.ID
		}
		if _, ok := svc.taskHistoryProfile(badScope, badProfile); ok {
			t.Fatalf("%s gained history", variant)
		}
		badConfig := config
		badConfig.principal, badConfig.sessionID, badConfig.agentProfile = badScope.Principal, badScope.SessionID, badProfile
		badCtx := identity.ContextWithPrincipal(boundedCtx, badScope.Principal)
		result, err := svc.newProviderToolInvoker(badConfig).ExecuteTool(badCtx, provideriface.ToolInvocation{Name: "manage_projects", Arguments: `{"action":"get_task","project_id":"project","task_id":"task"}`})
		if err != nil || result.Error == "" {
			t.Fatalf("%s invoker accepted: %+v %v", variant, result, err)
		}
	}
	orchestrator := agent.SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{})
	_, _, orchestratorDisabled, err := svc.compileResolvedAgentToolContract("account", orchestrator)
	if err != nil || orchestratorDisabled["manage_projects"] {
		t.Fatal("Orchestrator project authority lost")
	}
	_, _, ordinaryDisabled, err := svc.compileResolvedAgentToolContract("account", base)
	if err != nil || !ordinaryDisabled["manage_projects"] {
		t.Fatal("ordinary Swarm project policy widened")
	}
	after, _, err := ss.GetProjectTask("account", "project", "task")
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, _ := json.Marshal(after)
	if string(before) != string(afterBytes) {
		t.Fatal("history access or rejected calls changed durable task")
	}
}
