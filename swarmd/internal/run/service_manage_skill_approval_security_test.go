package run

import "testing"

func TestManageSkillApprovalArgumentsPreservesExpectedRevision(t *testing.T) {
	payload := map[string]any{
		"action": "update",
		"change": map[string]any{
			"path":              "/workspace/.agents/skills/example/SKILL.md",
			"after":             "updated",
			"expected_revision": "revision-token",
		},
	}
	args := manageSkillApprovalArguments(payload)
	if got := mapString(args, "expected_revision"); got != "revision-token" {
		t.Fatalf("expected_revision = %q, want revision-token", got)
	}
}
