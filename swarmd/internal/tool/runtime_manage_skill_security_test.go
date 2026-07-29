package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManageSkillRejectsSymlinkedSkillDirectoryAndFile(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	outsideSkill := filepath.Join(outside, "outside-skill.md")
	outsideContent := skillFixture("linked", "outside secret")
	if err := os.WriteFile(outsideSkill, []byte(outsideContent), 0o600); err != nil {
		t.Fatalf("write outside skill: %v", err)
	}
	root := filepath.Join(workspace, ".agents", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create skill root: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Fatalf("create directory symlink: %v", err)
	}
	fileDir := filepath.Join(root, "linked")
	if err := os.Mkdir(fileDir, 0o755); err != nil {
		t.Fatalf("create skill directory: %v", err)
	}
	if err := os.Symlink(outsideSkill, filepath.Join(fileDir, "SKILL.md")); err != nil {
		t.Fatalf("create skill file symlink: %v", err)
	}

	scope := WorkspaceScope{PrimaryPath: workspace}
	listed, err := executeManageSkill(scope, map[string]any{"action": "inspect"})
	if err != nil {
		t.Fatalf("inspect skills: %v", err)
	}
	if strings.Contains(listed, "outside secret") || strings.Contains(listed, "\"canonical_name\":\"linked\"") {
		t.Fatalf("inspect exposed symlinked skill: %s", listed)
	}
	for _, action := range []string{"get", "update", "delete"} {
		args := map[string]any{"action": action, "skill": "linked"}
		if action == "update" {
			args["content"] = skillFixture("linked", "replacement")
		}
		if _, err := executeManageSkill(scope, args); err == nil {
			t.Fatalf("%s unexpectedly accepted symlinked skill", action)
		}
	}
	got, err := os.ReadFile(outsideSkill)
	if err != nil {
		t.Fatalf("read outside skill: %v", err)
	}
	if string(got) != outsideContent {
		t.Fatalf("outside skill changed through symlink: %q", got)
	}
}

func TestManageSkillCreateUpdateDeleteLifecycle(t *testing.T) {
	workspace := t.TempDir()
	scope := WorkspaceScope{PrimaryPath: workspace}
	created := skillFixture("example", "created")
	createOutput, err := executeManageSkill(scope, map[string]any{"action": "create", "skill": "example", "content": created})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}
	if !strings.Contains(createOutput, `"action":"create"`) || !strings.Contains(createOutput, `"applied":true`) || strings.Contains(createOutput, "proposed_create") {
		t.Fatalf("create output = %s, want directly applied create", createOutput)
	}
	path := filepath.Join(workspace, ".agents", "skills", "example", "SKILL.md")
	assertFileContent(t, path, created)

	updated := skillFixture("example", "updated")
	updateArgs := map[string]any{"action": "update", "skill": "example", "content": updated}
	updateProposal, err := executeManageSkill(scope, updateArgs)
	if err != nil {
		t.Fatalf("propose update: %v", err)
	}
	updateArgs["confirm"] = true
	updateArgs["expected_revision"] = proposalRevision(t, updateProposal)
	updateOutput, err := executeManageSkill(scope, updateArgs)
	if err != nil {
		t.Fatalf("apply update: %v", err)
	}
	if !strings.Contains(updateOutput, `"action":"update"`) || !strings.Contains(updateOutput, `"applied":true`) {
		t.Fatalf("update output = %s, want applied update", updateOutput)
	}
	assertFileContent(t, path, updated)

	deleteProposal, err := executeManageSkill(scope, map[string]any{"action": "delete", "skill": "example"})
	if err != nil {
		t.Fatalf("propose delete: %v", err)
	}
	deleteOutput, err := executeManageSkill(scope, map[string]any{"action": "delete", "skill": "example", "confirm": true, "expected_revision": proposalRevision(t, deleteProposal)})
	if err != nil {
		t.Fatalf("apply delete: %v", err)
	}
	if !strings.Contains(deleteOutput, `"action":"delete"`) || !strings.Contains(deleteOutput, `"applied":true`) {
		t.Fatalf("delete output = %s, want applied delete", deleteOutput)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted skill still exists: %v", err)
	}
}

func TestManageSkillConfirmRejectsStaleUpdateAndDelete(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".agents", "skills", "example")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create skill root: %v", err)
	}
	path := filepath.Join(root, "SKILL.md")
	original := skillFixture("example", "original")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	scope := WorkspaceScope{PrimaryPath: workspace}

	updateArgs := map[string]any{"action": "update", "skill": "example", "content": skillFixture("example", "approved")}
	updateProposal, err := executeManageSkill(scope, updateArgs)
	if err != nil {
		t.Fatalf("propose update: %v", err)
	}
	updateRevision := proposalRevision(t, updateProposal)
	changed := skillFixture("example", "changed after approval")
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatalf("change skill after proposal: %v", err)
	}
	updateArgs["confirm"] = true
	updateArgs["expected_revision"] = updateRevision
	if _, err := executeManageSkill(scope, updateArgs); err == nil || !strings.Contains(err.Error(), "proposal is stale") {
		t.Fatalf("stale update error = %v, want stale proposal rejection", err)
	}
	assertFileContent(t, path, changed)

	deleteProposal, err := executeManageSkill(scope, map[string]any{"action": "delete", "skill": "example"})
	if err != nil {
		t.Fatalf("propose delete: %v", err)
	}
	deleteRevision := proposalRevision(t, deleteProposal)
	changedAgain := skillFixture("example", "changed before delete")
	if err := os.WriteFile(path, []byte(changedAgain), 0o644); err != nil {
		t.Fatalf("change skill before delete: %v", err)
	}
	_, err = executeManageSkill(scope, map[string]any{"action": "delete", "skill": "example", "confirm": true, "expected_revision": deleteRevision})
	if err == nil || !strings.Contains(err.Error(), "proposal is stale") {
		t.Fatalf("stale delete error = %v, want stale proposal rejection", err)
	}
	assertFileContent(t, path, changedAgain)
}

func skillFixture(name, body string) string {
	return "---\nname: " + name + "\ndescription: security fixture\n---\n" + body + "\n"
}

func proposalRevision(t *testing.T, raw string) string {
	t.Helper()
	var proposal struct {
		Change struct {
			ExpectedRevision string `json:"expected_revision"`
		} `json:"change"`
	}
	if err := json.Unmarshal([]byte(raw), &proposal); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	if proposal.Change.ExpectedRevision == "" {
		t.Fatal("proposal omitted expected_revision")
	}
	return proposal.Change.ExpectedRevision
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}
