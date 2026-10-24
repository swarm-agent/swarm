package run

import (
	"path/filepath"
	"reflect"
	"strings"
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
	custom := store.AgentProfile{Name: "custom-worker", Enabled: true, Mode: "subagent", Prompt: "Custom instructions", ToolContract: &store.AgentToolContract{Preset: "custom", Tools: map[string]store.AgentToolConfig{"bash": {Enabled: store.BoolPtr(false)}, "websearch": {Enabled: store.BoolPtr(true)}}}}
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
	if resolved.Tools["bash"].Enabled || !disabled["bash"] || !resolved.Tools["websearch"].Enabled || !resolved.Tools["task"].Enabled {
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
