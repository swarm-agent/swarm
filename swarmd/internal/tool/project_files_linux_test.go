//go:build linux

package tool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: rooted file and design writes let umask decide new entry access
// and never chmod existing entries. Owners: mkdirParent, openMutable,
// openLocalMutationParent and designWriteNew. Direct real filesystem calls
// exercise both confinement branches without a provider or sandbox dependency.
func TestProjectFileModes(t *testing.T) {
	out, err := exec.Command("sh", "-c", "umask").Output()
	if err != nil {
		t.Fatal(err)
	}
	var mask uint32
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%o", &mask); err != nil {
		t.Fatal(err)
	}
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprint(local), func(t *testing.T) {
			folder := t.TempDir()
			root, err := os.OpenRoot(folder)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			p := &rootedWorkspacePath{root: root, relative: "nested/file", mutable: true, localConfined: local}
			if err := p.writeFile([]byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
			check := func(path string, mode os.FileMode) {
				t.Helper()
				info, err := os.Stat(filepath.Join(folder, path))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != mode {
					t.Fatalf("%s mode %o want %o", path, info.Mode().Perm(), mode)
				}
			}
			check("nested", 0o777&^os.FileMode(mask))
			check("nested/file", 0o666&^os.FileMode(mask))
			existing, err := root.OpenFile("existing", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			existing.Close()
			before, err := root.Stat("existing")
			if err != nil {
				t.Fatal(err)
			}
			p.relative = "existing"
			if err := p.writeFile([]byte("updated"), 0o666); err != nil {
				t.Fatal(err)
			}
			check("existing", before.Mode().Perm())
			p.relative = "design/page.html"
			if err := designCreateExclusive(p, []byte("design")); err != nil {
				t.Fatal(err)
			}
			check("design", 0o777&^os.FileMode(mask))
			check("design/page.html", 0o666&^os.FileMode(mask))
		})
	}
}
