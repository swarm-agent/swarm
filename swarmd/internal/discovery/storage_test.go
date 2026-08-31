package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScanScopeReadsSharedUserSkillsButNotHomeRules proves Service.ScanScope
// reads the explicit shared-skill root without treating HOME/AGENTS.md as a
// workspace rule; the package layer is the narrowest authority boundary.
func TestScanScopeReadsSharedUserSkillsButNotHomeRules(t *testing.T) {
	home := configureDiscoveryTestStorage(t)
	workspace := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	sharedSkill := filepath.Join(home, ".agents", "skills", "shared")
	if err := os.MkdirAll(sharedSkill, 0o755); err != nil {
		t.Fatalf("mkdir home skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("home rule"), 0o644); err != nil {
		t.Fatalf("write home rule: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sharedSkill, "SKILL.md"), []byte("---\nname: shared\ndescription: shared home skill\n---\n"), 0o644); err != nil {
		t.Fatalf("write home skill: %v", err)
	}

	report, err := NewServiceWithUserHome(home).ScanScope(workspace, nil)
	if err != nil {
		t.Fatalf("ScanScope: %v", err)
	}
	for _, rule := range report.Rules {
		if strings.HasPrefix(rule.Path, home) {
			t.Fatalf("scan included home rule: %#v", rule)
		}
	}
	for _, skill := range report.Skills {
		if skill.CanonicalName == "shared" {
			if skill.Path != filepath.Join(sharedSkill, "SKILL.md") || skill.Scope != "user-local" || skill.Origin != "agents-user-skills" {
				t.Fatalf("shared skill = %#v", skill)
			}
			return
		}
	}
	t.Fatal("shared user skill was not discovered")
}
