package remote

import (
	"fmt"
	"strings"
	"testing"
)

// Purpose: relay ToolScope and filterTools must expose definitions to write
// keys, never read keys. The pure scope boundary is the narrowest proof.
func TestCustomAgentWriteScope(t *testing.T) {
	if ToolScope("swarm_define_agent") != ScopeWrite {
		t.Fatal("not write scoped")
	}
	for _, tc := range []struct {
		scopes  []string
		allowed bool
	}{{[]string{ScopeRead}, false}, {[]string{ScopeRead, ScopeWrite}, true}} {
		got := filterTools([]byte(`{"result":{"tools":[{"name":"swarm_define_agent"}]}}`), tc.scopes)
		if strings.Contains(string(got), "swarm_define_agent") != tc.allowed {
			t.Fatalf("unexpected relay tools: %s", got)
		}
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
