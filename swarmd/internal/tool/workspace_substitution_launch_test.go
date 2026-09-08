package tool

import (
	"os"
	"path/filepath"
	"testing"
)

// Requirement: a path substituted after authorization must not redirect rooted
// I/O to an outside file or hard-link alias. Threat: TOCTOU truncation/exfiltration.
// Authority: openRootedWorkspacePath, rootedWorkspacePath.open/writeFile/openMutable.
// This exact post-open barrier is narrower and deterministic compared with a
// probabilistic racing goroutine. It does not prove root substitution before OpenRoot.
func TestWorkspaceLaunchPostOpenSubstitution(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			inside := filepath.Join(root, "marker")
			victim := filepath.Join(outside, "marker")
			for file, data := range map[string]string{inside: "inside", victim: "outside-preserved"} {
				if err := os.WriteFile(file, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			opened, err := openRootedWorkspacePath(normalizeWorkspaceScope(root, nil), "marker")
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			if err := os.Rename(inside, inside+".retained"); err != nil {
				t.Fatal(err)
			}
			link := os.Symlink
			if kind == "hardlink" {
				link = os.Link
			}
			if err := link(victim, inside); err != nil {
				t.Fatal(err)
			}
			if err := opened.writeFile([]byte("unauthorized"), 0600); err == nil {
				t.Fatal("substituted target accepted mutation")
			}
			if kind == "symlink" {
				file, err := opened.open()
				if err == nil {
					file.Close()
					t.Fatal("outside symlink accepted read")
				}
			}
			for file, want := range map[string]string{victim: "outside-preserved", inside + ".retained": "inside"} {
				got, err := os.ReadFile(file)
				if err != nil || string(got) != want {
					t.Fatalf("postcondition %s: %q %v", file, got, err)
				}
			}
		})
	}
}
