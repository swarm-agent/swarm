package run

import (
	"testing"

	agent "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: regular Coder's exposed Bash must reach ordinary authorization,
// not the legacy readwrite shell deny observed in real F2.2 delegation. Owners:
// resolveExecutionMode, CoderAgentProfileForParent and permission.ExplainPolicy.
// This boundary test verifies the actual compiled role and policy together;
// provider execution and namespace placement remain separate live gates.
func TestCoderExecutionModeReachesBashPermission(t *testing.T) {
	svc := &Service{}
	p := agent.CoderAgentProfileForParent(store.AgentProfile{})
	mode, _, err := svc.resolveExecutionMode("auto", p)
	if err != nil {
		t.Fatal(err)
	}
	args := `{"command":"node --test sum.test.mjs","category":"read","critical":false}`
	decision := permission.ExplainPolicy(mode, "bash", args, permission.Policy{})
	if decision.Decision != permission.PolicyDecisionAsk {
		t.Fatalf("ordinary Bash must ask, not bypass or deny: mode=%s decision=%+v", mode, decision)
	}
	danger := permission.ExplainPolicy(mode, "bash", `{"command":"rm -rf /","category":"delete","critical":true}`, permission.Policy{})
	if danger.Decision != permission.PolicyDecisionDeny {
		t.Fatalf("dangerous command must remain denied: %+v", danger)
	}
	for _, name := range []string{"task", "manage_agent", "plan_manage"} {
		if profileHasCapability(p, name) {
			t.Fatalf("unexpected Coder authority: %s", name)
		}
	}
	if !profileHasCapability(p, "manage_environments") {
		t.Fatal("Coder lost manage_environments")
	}
}

// Requirement: the Coder correction must not loosen read-only, custom, or
// explicitly Bash-disabled profiles. The same resolver/policy boundary is the
// narrowest layer proving these negative cases without executing commands.
func TestCoderExecutionModePreservesRestrictedProfiles(t *testing.T) {
	svc := &Service{}
	for _, scenario := range []string{"bash-disabled", "read-only", "custom-role", "primary-role"} {
		t.Run(scenario, func(t *testing.T) {
			p := agent.CoderAgentProfileForParent(store.AgentProfile{})
			switch scenario {
			case "bash-disabled":
				p.ToolContract.Tools["bash"] = store.AgentToolConfig{Enabled: store.BoolPtr(false)}
			case "read-only":
				p.RuntimeMode = store.AgentRuntimeModeRead
			case "custom-role":
				p.Name = "custom-coder"
			case "primary-role":
				p.Mode = agent.ModePrimary
			}
			mode, _, err := svc.resolveExecutionMode("auto", p)
			if err != nil {
				t.Fatal(err)
			}
			decision := permission.ExplainPolicy(mode, "bash", `{"command":"node --test sum.test.mjs","category":"read","critical":false}`, permission.Policy{})
			if decision.Decision != permission.PolicyDecisionDeny {
				t.Fatalf("restricted Bash must remain denied: mode=%s decision=%+v", mode, decision)
			}
		})
	}
}
