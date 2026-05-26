package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanScopeLoadsAgentsOnlyFromExplicitRoots(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	dataRoot := filepath.Join(t.TempDir(), "swarmd-data")
	parent := filepath.Join(t.TempDir(), "parent")
	workspace := filepath.Join(parent, "repo")
	linked := filepath.Join(t.TempDir(), "linked")

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("STATE_DIRECTORY", dataRoot)
	t.Setenv("SWARM_CONFIG", "")

	for _, dir := range []string{home, parent, workspace, linked} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeRuleFile(t, filepath.Join(home, "AGENTS.md"), "home rule")
	writeRuleFile(t, filepath.Join(parent, "AGENTS.md"), "parent rule")
	writeRuleFile(t, filepath.Join(workspace, "AGENTS.md"), "workspace rule")
	writeRuleFile(t, filepath.Join(workspace, "CLAUDE.md"), "workspace claude rule")
	writeRuleFile(t, filepath.Join(linked, "AGENTS.md"), "linked rule")

	report, err := NewService().ScanScope(workspace, nil)
	if err != nil {
		t.Fatalf("ScanScope workspace: %v", err)
	}
	assertRulePaths(t, report.Rules, []string{filepath.Join(workspace, "AGENTS.md")})

	report, err = NewService().ScanScope(workspace, []string{linked})
	if err != nil {
		t.Fatalf("ScanScope linked: %v", err)
	}
	assertRulePaths(t, report.Rules, []string{filepath.Join(workspace, "AGENTS.md"), filepath.Join(linked, "AGENTS.md")})
}

func writeRuleFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertRulePaths(t *testing.T, rules []RuleSource, want []string) {
	t.Helper()
	got := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule.Scope != "workspace-local" {
			continue
		}
		got = append(got, filepath.Clean(rule.Path))
		if strings.EqualFold(filepath.Base(rule.Path), "CLAUDE.md") {
			t.Fatalf("unexpected CLAUDE.md rule: %#v", rule)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("workspace-local rules = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != filepath.Clean(want[i]) {
			t.Fatalf("workspace-local rules = %#v, want %#v", got, want)
		}
	}
}
