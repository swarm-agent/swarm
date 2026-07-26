package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanScopeRejectsWorkspaceInstructionSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside-agents.md")
	if err := os.WriteFile(outside, []byte("outside instruction"), 0o600); err != nil {
		t.Fatalf("write outside instruction: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "AGENTS.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	report, err := NewService().ScanScope(workspace, nil)
	if err != nil {
		t.Fatalf("scan scope: %v", err)
	}
	for _, rule := range report.Rules {
		if rule.Path == filepath.Join(workspace, "AGENTS.md") {
			t.Fatalf("workspace instruction symlink was accepted: %+v", rule)
		}
	}
}

func TestScanScopeRejectsWorkspaceSkillSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	skillRoot := filepath.Join(workspace, ".agents", "skills")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatalf("create skill root: %v", err)
	}
	outsideSkill := filepath.Join(t.TempDir(), "outside-skill")
	if err := os.MkdirAll(outsideSkill, 0o755); err != nil {
		t.Fatalf("create outside skill: %v", err)
	}
	content := []byte("---\nname: escaped\ndescription: escaped skill\n---\noutside\n")
	if err := os.WriteFile(filepath.Join(outsideSkill, "SKILL.md"), content, 0o600); err != nil {
		t.Fatalf("write outside skill: %v", err)
	}
	if err := os.Symlink(outsideSkill, filepath.Join(skillRoot, "escaped")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	report, err := NewService().ScanScope(workspace, nil)
	if err != nil {
		t.Fatalf("scan scope: %v", err)
	}
	for _, skill := range report.Skills {
		if skill.CanonicalName == "escaped" {
			t.Fatalf("workspace skill symlink was accepted: %+v", skill)
		}
	}
}

func TestScanScopeCapturesRootedContentSnapshot(t *testing.T) {
	workspace := t.TempDir()
	instruction := []byte("rooted instruction")
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), instruction, 0o600); err != nil {
		t.Fatalf("write instruction: %v", err)
	}
	skillDir := filepath.Join(workspace, ".agents", "skills", "safe")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	skillContent := []byte("---\nname: safe\ndescription: safe skill\n---\nbody\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillContent, 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	report, err := NewService().ScanScope(workspace, nil)
	if err != nil {
		t.Fatalf("scan scope: %v", err)
	}
	if len(report.Rules) == 0 || string(report.Rules[0].Content) != string(instruction) {
		t.Fatalf("rooted rule snapshot missing: %+v", report.Rules)
	}
	found := false
	for _, skill := range report.Skills {
		if skill.CanonicalName == "safe" {
			found = true
			if string(skill.Content) != string(skillContent) {
				t.Fatalf("rooted skill snapshot = %q, want %q", skill.Content, skillContent)
			}
		}
	}
	if !found {
		t.Fatal("safe rooted skill was not discovered")
	}
}
