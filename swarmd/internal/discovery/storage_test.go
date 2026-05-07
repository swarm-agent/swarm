package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedSkillsDirUsesSystemDataConfigRoot(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	dataRoot := filepath.Join(t.TempDir(), "swarmd-data")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("STATE_DIRECTORY", dataRoot)
	t.Setenv("SWARM_CONFIG", "")

	got, err := ManagedSkillsDir()
	if err != nil {
		t.Fatalf("ManagedSkillsDir: %v", err)
	}
	want := filepath.Join(dataRoot, "config", managedSkillsDirName)
	if got != want {
		t.Fatalf("ManagedSkillsDir = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, home) {
		t.Fatalf("managed skills dir %q is under HOME", got)
	}
}

func TestManagedSkillsDirRejectsUnsafeSwarmConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)

	cases := []string{
		"relative-config",
		filepath.Join(home, ".config", "swarm"),
	}
	for _, value := range cases {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SWARM_CONFIG", value)
			if _, err := ManagedSkillsDir(); err == nil {
				t.Fatalf("ManagedSkillsDir accepted unsafe SWARM_CONFIG %q", value)
			}
		})
	}
}

func TestScanScopeDoesNotReadHomeDefaultRulesOrSkills(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	dataRoot := filepath.Join(t.TempDir(), "swarmd-data")
	workspace := filepath.Join(t.TempDir(), "repo")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("STATE_DIRECTORY", dataRoot)
	t.Setenv("SWARM_CONFIG", "")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills", "unsafe"), 0o755); err != nil {
		t.Fatalf("mkdir home skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte("home rule"), 0o644); err != nil {
		t.Fatalf("write home rule: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "skills", "unsafe", "SKILL.md"), []byte("---\nname: unsafe\ndescription: home skill\n---\n"), 0o644); err != nil {
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
