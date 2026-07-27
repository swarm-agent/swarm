package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanScopeDoesNotReadHomeDefaultRulesOrSkills(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	dataRoot := filepath.Join(t.TempDir(), "swarmd-data")
	workspace := filepath.Join(t.TempDir(), "repo")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("STATE_DIRECTORY", dataRoot)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills", "unsafe"), 0o755); err != nil {
		t.Fatalf("mkdir home skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("home rule"), 0o644); err != nil {
		t.Fatalf("write home rule: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agents", "skills", "unsafe", "SKILL.md"), []byte("---\nname: unsafe\ndescription: home skill\n---\n"), 0o644); err != nil {
		t.Fatalf("write home skill: %v", err)
	}

	report, err := NewService().ScanScope(workspace, nil)
	if err != nil {
		t.Fatalf("ScanScope: %v", err)
	}
	for _, rule := range report.Rules {
		if strings.HasPrefix(rule.Path, home) {
			t.Fatalf("scan included home rule: %#v", rule)
		}
	}
	for _, skill := range report.Skills {
		if strings.HasPrefix(skill.Path, home) {
			t.Fatalf("scan included home skill: %#v", skill)
		}
	}
}
