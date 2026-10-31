package remote

import (
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Purpose: defining a custom agent needs write: manage_custom_agents define
// and disable are write level and its reads are read level, as
// swarm_define_agent and swarm_list_agents were (the swarmToolLevels record),
// and the relay lists manage_custom_agents for Swarm Control to filter per
// action but never the removed swarm_define_agent. Threat: a read key
// defining agents. Boundary: the catalog declaration, the record and
// filterTools; the pure policy boundary is the narrowest proof.
func TestCustomAgentWriteScope(t *testing.T) {
	catalog, ok := tool.CatalogToolNamed("manage_custom_agents")
	if !ok {
		t.Fatal("manage_custom_agents is not a catalog tool")
	}
	for action, swarm := range map[string]string{"define": "swarm_define_agent", "disable": "swarm_define_agent", "list": "swarm_list_agents", "get": "swarm_list_agents"} {
		declared, ok := catalog.Action(action)
		if !ok || declared.Level.Scope() != swarmToolLevel(swarm) {
			t.Fatalf("manage_custom_agents %s: %+v; %s needed %s", action, declared.Level, swarm, swarmToolLevel(swarm))
		}
	}
	if swarmToolLevel("swarm_define_agent") != ScopeWrite {
		t.Fatal("defining an agent was not write scoped")
	}
	got := string(filterTools([]byte(`{"result":{"tools":[{"name":"swarm_define_agent"},{"name":"manage_custom_agents"}]}}`)))
	if strings.Contains(got, "swarm_define_agent") || !strings.Contains(got, "manage_custom_agents") {
		t.Fatalf("unexpected relay tools: %s", got)
	}
}

// Purpose: credential upgrades must fail closed on persistence/revocation
// failures and preserve the durable old config for retry. upgradeDeviceToken
// is the narrowest transaction boundary; injected stores prove cleanup.
func TestCustomAgentTokenUpgradeFailures(t *testing.T) {
	for _, stage := range []string{"mint", "revoke", "save"} {
		t.Run(stage, func(t *testing.T) {
			fixture := &upgradeFixture{fail: stage}
			svc := NewService(fixture, fixture)
			old := Config{Token: "old-token", TokenID: "old", AllowWrite: true}
			if _, err := svc.upgradeDeviceToken(old); err == nil {
				t.Fatal("upgrade failure hidden")
			}
			if fixture.saved {
				t.Fatal("failed upgrade persisted config")
			}
			if stage != "mint" && !fixture.cleaned {
				t.Fatal("replacement token not revoked")
			}
		})
	}
}

type upgradeFixture struct {
	fail           string
	saved, cleaned bool
}

func (f *upgradeFixture) Mint(string, []string) (string, string, error) {
	if f.fail == "mint" {
		return "", "", fmt.Errorf("mint failed")
	}
	return "new-token", "new", nil
}
func (f *upgradeFixture) Revoke(id string) error {
	if id == "new" {
		f.cleaned = true
		return nil
	}
	if f.fail == "revoke" {
		return fmt.Errorf("revoke failed")
	}
	return nil
}
func (f *upgradeFixture) PutJSON(string, any) error {
	if f.fail == "save" {
		return fmt.Errorf("save failed")
	}
	f.saved = true
	return nil
}
func (f *upgradeFixture) GetJSON(string, any) (bool, error) { return false, nil }
func (f *upgradeFixture) Delete(string) error               { return nil }
