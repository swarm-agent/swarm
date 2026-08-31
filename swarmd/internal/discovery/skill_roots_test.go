package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceSkillRootsEnumeratesBoundaryThroughActiveScope(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	active := filepath.Join(project, "packages", "api")
	linked := filepath.Join(t.TempDir(), "linked")

	got := workspaceSkillRoots(active, []string{active, project, linked, project})
	want := []string{
		linked,
		project,
		filepath.Join(project, "packages"),
		active,
	}
	assertSkillRootPaths(t, got, want)
}

// TestScanScopeUsesNestedProjectSkillPrecedenceAndDeduplicatesRoots proves the
// active nested workspace wins over physical lower-precedence copies without
// duplicate roots; the package layer directly observes ScanScope resolution.
func TestScanScopeUsesNestedProjectSkillPrecedenceAndDeduplicatesRoots(t *testing.T) {
	home := configureDiscoveryTestStorage(t)
	project := filepath.Join(t.TempDir(), "project")
	active := filepath.Join(project, "packages", "api")

	for _, root := range []string{home, project, filepath.Join(project, "packages"), active} {
		writeSkillFixture(t, root, "shared", root)
	}

	report, err := NewServiceWithUserHome(home).ScanScope(active, []string{project, active, project})
	if err != nil {
		t.Fatalf("ScanScope: %v", err)
	}
	if len(report.Skills) != 1 {
		t.Fatalf("skills = %#v, want one active skill", report.Skills)
	}
	wantPath := filepath.Join(active, ".agents", "skills", "shared", "SKILL.md")
	if report.Skills[0].Path != wantPath || report.Skills[0].Scope != "workspace-local" {
		t.Fatalf("active skill = %#v, want nested workspace skill %q", report.Skills[0], wantPath)
	}
	if len(report.Overrides) != 3 {
		t.Fatalf("overrides = %#v, want one per lower-precedence physical skill", report.Overrides)
	}
}

// TestScanScopeRejectsSharedUserSkillSymlinkEscape proves shared user skills
// remain a read-only rooted source and cannot escape through a symlink; this
// package test is the narrowest layer covering the discovery boundary.
func TestScanScopeRejectsSharedUserSkillSymlinkEscape(t *testing.T) {
	home := configureDiscoveryTestStorage(t)
	workspace := filepath.Join(t.TempDir(), "workspace")

	skillRoot := filepath.Join(home, ".agents", "skills")
	outside := filepath.Join(t.TempDir(), "escaped")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatalf("create shared skill root: %v", err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	writeSkillFile(t, outside, "escaped", "outside")
	if err := os.Symlink(outside, filepath.Join(skillRoot, "escaped")); err != nil {
		t.Skipf("shared skill symlink unavailable: %v", err)
	}

	report, err := NewServiceWithUserHome(home).ScanScope(workspace, nil)
	if err != nil {
		t.Fatalf("ScanScope: %v", err)
	}
	for _, skill := range report.Skills {
		if skill.CanonicalName == "escaped" {
			t.Fatalf("shared user skill symlink was accepted: %#v", skill)
		}
	}
}

func writeSkillFixture(t *testing.T, root, name, body string) {
	t.Helper()
	writeSkillFile(t, filepath.Join(root, ".agents", "skills", name), name, body)
}

func writeSkillFile(t *testing.T, skillDir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create skill directory %q: %v", skillDir, err)
	}
	content := []byte("---\nname: " + name + "\ndescription: test skill\n---\n" + body + "\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), content, 0o600); err != nil {
		t.Fatalf("write skill %q: %v", skillDir, err)
	}
}

func assertSkillRootPaths(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
	for index := range want {
		if filepath.Clean(got[index]) != filepath.Clean(want[index]) {
			t.Fatalf("paths = %#v, want %#v", got, want)
		}
	}
}
