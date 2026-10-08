package permission

import "testing"

// Purpose: managed builds execute reviewed recipes, not definition-only writes.
// ManageEnvironmentsPolicyIdentity must require deployment execution authority;
// this policy-layer test prevents a definition-edit grant classifying builds.
func TestManagedBuildRequiresDeploymentAuthority(t *testing.T) {
	for _, args := range []string{`{"action":"build"}`, `{"action":" BUILD "}`} {
		id, sensitive := ManageEnvironmentsPolicyIdentity(args)
		if id != "deployment_deploy" || !sensitive {
			t.Fatalf("build authority: %q %v", id, sensitive)
		}
	}
	id, sensitive := ManageEnvironmentsPolicyIdentity(`{"action":"create"}`)
	if id != "environment_change" || !sensitive {
		t.Fatal("definition authority changed")
	}
	id, sensitive = ManageEnvironmentsPolicyIdentity(`{"action":"get_operation"}`)
	if id != "manage_environments" || sensitive {
		t.Fatal("inspection authority changed")
	}
}
