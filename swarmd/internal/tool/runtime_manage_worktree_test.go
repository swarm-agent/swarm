package tool

import "testing"

func TestManageWorktreeDefinitionKeepsIntegrateInputMinimal(t *testing.T) {
	var definition Definition
	for _, candidate := range NewRuntime(1).Definitions() {
		if candidate.Name == "manage-worktree" {
			definition = candidate
			break
		}
	}
	if definition.Name == "" {
		t.Fatal("manage-worktree definition not found")
	}
	properties, _ := definition.Parameters["properties"].(map[string]any)
	for _, forbidden := range []string{"integration_plan", "expected_parent_head", "preview"} {
		if _, ok := properties[forbidden]; ok {
			t.Fatalf("manage-worktree exposes model-authored %s", forbidden)
		}
	}
	for _, required := range []string{"action", "session_ids"} {
		if _, ok := properties[required]; !ok {
			t.Fatalf("manage-worktree missing %s", required)
		}
	}
}
