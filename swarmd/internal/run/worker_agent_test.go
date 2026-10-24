package run

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: ApplyWorkerAgent must retain role authority while reading fresh,
// account-owned tool settings. A real Pebble store plus the production compiler
// is the narrowest boundary proving freshness and reviewer rejection without a provider.
func TestWorkerCustomAgentContract(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "agents"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	agents := store.NewAgentStore(db)
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	svc.agents = agentruntime.NewService(agents, nil)
	custom := store.AgentProfile{Name: "custom-worker", Enabled: true, Mode: "subagent", Prompt: "Custom instructions", ToolContract: &store.AgentToolContract{Preset: "custom", Tools: map[string]store.AgentToolConfig{"bash": {Enabled: store.BoolPtr(false)}, "manage_todos": {Enabled: store.BoolPtr(true)}}}}
	save := func() {
		t.Helper()
		if err := agents.PutProfileForAccount("account-a", custom); err != nil {
			t.Fatal(err)
		}
	}
	save()
	worker := store.WorkerRecord{Role: store.WorkerRoleCoder, Agent: custom.Name}
	base, err := svc.agents.ResolveSystemAgent(workerRoleAgentID(worker.Role), store.AgentProfile{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := svc.ApplyWorkerAgent("account-a", worker, base)
	if err != nil {
		t.Fatal(err)
	}
	resolved, disabled, err := svc.CompileStoredV3AgentToolContract("account-a", profile)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Tools["bash"].Enabled || !disabled["bash"] || !resolved.Tools["manage_todos"].Enabled || !resolved.Tools["task"].Enabled {
		t.Fatalf("incorrect tool overlay %+v", resolved)
	}
	if profile.Name != base.Name || !strings.Contains(profile.Prompt, custom.Prompt) {
		t.Fatal("lost role or instructions")
	}
	custom.ToolContract.Tools["bash"] = store.AgentToolConfig{Enabled: store.BoolPtr(true)}
	save()
	next, err := svc.ApplyWorkerAgent("account-a", worker, base)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, err = svc.CompileStoredV3AgentToolContract("account-a", next)
	if err != nil || !resolved.Tools["bash"].Enabled {
		t.Fatalf("fresh contract not applied %v", err)
	}
	unchanged, err := svc.ApplyWorkerAgent("account-a", store.WorkerRecord{Role: worker.Role}, base)
	if err != nil || !reflect.DeepEqual(unchanged, base) {
		t.Fatal("default worker changed")
	}
	defaultTools, _, err := svc.CompileStoredV3AgentToolContract("account-a", unchanged)
	if err != nil || defaultTools.Tools["manage_todos"].Enabled {
		t.Fatalf("default worker enables additional tool %v", err)
	}
	if _, err := svc.ApplyWorkerAgent("account-b", worker, base); err == nil {
		t.Fatal("cross-account profile accepted")
	}
	worker.Role = store.WorkerRoleReviewer
	if err := svc.ValidateWorkerAgent("account-a", worker); err != nil {
		t.Fatal(err)
	}
	custom.ToolContract.Tools["write"] = store.AgentToolConfig{Enabled: store.BoolPtr(true)}
	save()
	saveErr := svc.ValidateWorkerAgent("account-a", worker)
	reviewer, err := svc.agents.ResolveSystemAgent(workerRoleAgentID(worker.Role), store.AgentProfile{})
	if err != nil {
		t.Fatal(err)
	}
	_, admissionErr := svc.ApplyWorkerAgent("account-a", worker, reviewer)
	if saveErr == nil || admissionErr == nil || saveErr.Error() != admissionErr.Error() || !strings.Contains(saveErr.Error(), "write") {
		t.Fatalf("reviewer guard save=%v admission=%v", saveErr, admissionErr)
	}
	custom.Enabled = false
	save()
	if _, err := svc.ApplyWorkerAgent("account-a", store.WorkerRecord{Role: store.WorkerRoleCoder, Agent: custom.Name}, base); err == nil {
		t.Fatal("disabled agent accepted")
	}
}

// Purpose: startPlan must revalidate mutable tools before any session is
// created. Real profile/session stores prove both built-in and custom write
// effects reject admission after a previously valid reviewer configuration.
func TestReviewerCustomAgentAdmission(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "admission"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := store.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(store.NewSessionStore(db), events)
	agents := store.NewAgentStore(db)
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	svc.agents = agentruntime.NewService(agents, events)
	svc.sessions = sessions
	execution := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: svc}}
	for _, name := range []string{"write", "publish_fixture"} {
		t.Run(name, func(t *testing.T) {
			custom := store.AgentProfile{Name: "review-tools", Enabled: true, ToolContract: &store.AgentToolContract{Preset: "custom", Tools: map[string]store.AgentToolConfig{}}}
			if err := agents.PutProfileForAccount("account-a", custom); err != nil {
				t.Fatal(err)
			}
			worker := store.WorkerRecord{Role: store.WorkerRoleReviewer, Agent: custom.Name}
			if err := svc.ValidateWorkerAgent("account-a", worker); err != nil {
				t.Fatal(err)
			}
			saved, err := store.NewWorkerStore(db).CreateWorker("account-a", "user-a", store.CreateWorkerRequest{Role: worker.Role, Agent: worker.Agent, Name: "Reviewer " + name, IdempotencyKey: name}, nil)
			if err != nil {
				t.Fatal(err)
			}
			worker = saved
			if name != "write" {
				if _, err := svc.agents.PutCustomToolForAccount("account-a", store.AgentCustomToolDefinition{Name: name, Kind: store.AgentCustomToolKindClient, Effect: store.AgentCustomToolEffectWrite, InputSchema: map[string]any{"type": "object"}}); err != nil {
					t.Fatal(err)
				}
			}
			custom.ToolContract.Tools[name] = store.AgentToolConfig{Enabled: store.BoolPtr(true)}
			if err := agents.PutProfileForAccount("account-a", custom); err != nil {
				t.Fatal(err)
			}
			saveErr := svc.ValidateWorkerAgent("account-a", worker)
			run := store.WorkerRunRecord{ID: "admission-fixture", AccountScopeID: "account-a", SessionID: "review-fixture", Role: store.WorkerRoleReviewer}
			err = execution.startPlan(context.Background(), run, worker, store.SessionPlanDocument{})
			if saveErr == nil || err == nil || err.Error() != saveErr.Error() || !strings.Contains(err.Error(), name) {
				t.Fatalf("save=%v admission=%v", saveErr, err)
			}
			if _, found, err := sessions.GetSession(run.SessionID); err != nil || found {
				t.Fatalf("rejected session exists %v %v", found, err)
			}
		})
	}
}

// Purpose: ValidateWorkerAgent must fail closed rather than panic when save or
// admission lacks agent authority. Direct boundary calls are the narrowest
// layer proving both nil receiver and nil authority handling and the unchanged
// no-custom-agent path, without needing store or provider infrastructure.
func TestValidateWorkerAgentMissingAuthority(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service *Service
	}{
		{name: "nil service"}, {name: "nil agents", service: &Service{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker := store.WorkerRecord{Role: store.WorkerRoleCoder, Agent: "custom-worker"}
			if err := tc.service.ValidateWorkerAgent("account-a", worker); err == nil || err.Error() != "agent service is not configured" {
				t.Fatalf("expected missing authority error, got %v", err)
			}
			worker.Agent = ""
			if err := tc.service.ValidateWorkerAgent("account-a", worker); err != nil {
				t.Fatalf("default worker changed %v", err)
			}
		})
	}
}
