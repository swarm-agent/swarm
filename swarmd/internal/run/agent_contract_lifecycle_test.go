package run

import (
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestResolveAgentToolContractUsesSavedBuiltInContracts(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	for _, name := range []string{"swarm", "explorer", "memory", "parallel"} {
		profile, ok := agentruntime.DefaultProfileByName(name)
		if !ok {
			t.Fatalf("DefaultProfileByName(%s) missing", name)
		}
		resolved, _, _, err := svc.ResolveAgentToolContract(profile)
		if err != nil {
			t.Fatalf("ResolveAgentToolContract(%s) error = %v", name, err)
		}
		if strings.TrimSpace(resolved.RawPreset) == "" && len(profile.ToolContract.Tools) == 0 {
			t.Fatalf("%s resolved without persisted preset or explicit tools: %+v", name, resolved)
		}
	}
}

func TestResolveAgentToolContractFailsClosedWhenSavedContractMissing(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	_, _, _, err := svc.ResolveAgentToolContract(pebblestore.AgentProfile{
		Name:        "legacy-custom",
		Mode:        agentruntime.ModeSubagent,
		RuntimeMode: pebblestore.AgentRuntimeModeRead,
		Enabled:     true,
	})
	if err == nil || !strings.Contains(err.Error(), "tool_contract is not configured") {
		t.Fatalf("ResolveAgentToolContract() error = %v, want missing tool_contract", err)
	}
}
