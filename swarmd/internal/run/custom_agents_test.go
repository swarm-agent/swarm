package run

import (
	"encoding/json"
	"reflect"
	"sort"
	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Purpose: custom session contracts must not acquire implicit lease or control
// tools. CompileStoredV3AgentToolContract is the production authority and the
// narrowest layer proving exact resolved names, including media and progress.
func TestCustomAgentExactWorkTools(t *testing.T) {
	names := []string{"read", "list", "find", "search", "write", "edit", "bash", "media_inspect", "manage_media", "git_status", "git_diff", "git_add", "git_commit", "task_progress"}
	tools := map[string]store.AgentToolConfig{}
	for _, n := range names {
		tools[n] = store.AgentToolConfig{Enabled: store.BoolPtr(true)}
	}
	s := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	p := store.AgentProfile{Name: "social-maker", Mode: "subagent", ToolContract: &store.AgentToolContract{Preset: "custom", Tools: tools}}
	resolved, _, err := s.CompileStoredV3AgentToolContract("account", p)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, resolved.AvailableTools) {
		t.Fatalf("unexpected tools: %v", resolved.AvailableTools)
	}
	p.ToolContract.Tools["manage_custom_agents"] = store.AgentToolConfig{Enabled: store.BoolPtr(true)}
	resolved, _, err = s.CompileStoredV3AgentToolContract("account", p)
	if err != nil || resolved.Tools["manage_custom_agents"].Enabled {
		t.Fatalf("custom control authority: %v", err)
	}
}

// Purpose: native worker proposals retain custom agent selection without
// activation. The run-service/store boundary proves durable pending state and
// refusal of missing agents before creating another worker.
func TestCustomAgentWorkerProposal(t *testing.T) {
	s, settings, db := taskFallbackFixture(t)
	s.tools = tool.NewRuntime(1)
	taskFallbackSettings(t, settings, "account-a", "specialist", "account-a-action")
	sessions := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(sessions, nil)
	agents := store.NewAgentStore(db)
	s.agents = agentruntime.NewService(agents, nil)
	names := []string{"bash", "edit", "find", "git_add", "git_commit", "git_diff", "git_status", "list", "manage_media", "media_inspect", "read", "search", "task_progress", "write"}
	tools := map[string]store.AgentToolConfig{}
	for _, name := range names {
		tools[name] = store.AgentToolConfig{Enabled: store.BoolPtr(true)}
	}
	if err := agents.PutProfileForAccount("account-a", store.AgentProfile{Name: "social-maker", Mode: "subagent", Enabled: true, ToolContract: &store.AgentToolContract{Preset: "custom", Tools: tools}}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account-a", UserID: "owner", Metadata: map[string]any{"agent_name": "orchestrator"}, WorkspaceGrants: []store.WorkspaceGrant{{WorkspaceID: "folder", Kind: store.WorkspaceGrantPrimary, Available: store.BoolPtr(true)}}}); err != nil {
		t.Fatal(err)
	}
	output, err := s.executeManageWorkersTool("author", `{"action":"propose","name":"Social","instructions":"Make media","workspace_id":"folder","agent":"social-maker"}`)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Worker store.WorkerRecord `json:"worker"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Worker.Role != store.WorkerRoleCoder || result.Worker.Agent != "social-maker" || result.Worker.LifecycleState != store.WorkerLifecycleStatePending {
		t.Fatalf("unexpected worker: %+v", result.Worker)
	}
	// Follow worker execution's role resolution and custom overlay, then round
	// trip the snapshot as persistence does before compiling its actual tools.
	for _, role := range []string{store.WorkerRoleCoder, store.WorkerRoleSwarm} {
		worker := result.Worker
		worker.Role = role
		id, _ := workerExecutionAgentID(role, nil)
		profile, err := s.agents.ResolveSystemAgent(id, store.AgentProfile{})
		if err != nil {
			t.Fatal(err)
		}
		profile, err = s.ApplyWorkerAgent("account-a", worker, profile)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(profile)
		var persisted store.AgentProfile
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Fatal(err)
		}
		resolved, _, err := s.CompileStoredV3AgentToolContract("account-a", persisted)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(resolved.AvailableTools, names) {
			t.Fatalf("role %s leaked tools: %v", role, resolved.AvailableTools)
		}
	}
	if _, err := s.executeManageWorkersTool("author", `{"action":"propose","name":"Missing","instructions":"Make media","workspace_id":"folder","agent":"missing"}`); err == nil {
		t.Fatal("missing agent accepted")
	}
	pending, err := s.pendingWorkersForSession("account-a", "author")
	if err != nil || len(pending) != 1 {
		t.Fatalf("partial mutation: %v %v", pending, err)
	}
}
