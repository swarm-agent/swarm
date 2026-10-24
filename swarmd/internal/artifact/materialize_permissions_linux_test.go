//go:build linux

package artifact

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: workspace artifact files inherit default ACLs or umask without
// restrictive chmod. materializeWorkspaceFile and ensureWorkspaceDirectory own
// these writes; real filesystem calls prove the mask and conflict postcondition.
func TestMaterializeWorkspacePermissions(t *testing.T) {
	for _, acl := range []bool{false, true} {
		t.Run(fmt.Sprint(acl), func(t *testing.T) {
			root := t.TempDir()
			out, err := exec.Command("sh", "-c", "umask").Output()
			if err != nil {
				t.Fatal(err)
			}
			var mask uint32
			if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%o", &mask); err != nil {
				t.Fatal(err)
			}
			if acl {
				for _, tool := range []string{"setfacl", "getfacl"} {
					if _, err := exec.LookPath(tool); err != nil {
						t.Skip(tool + " unavailable")
					}
				}
				if out, err := exec.Command("setfacl", "-m", "d:u::rwx,d:u:65534:rwx,d:g::rwx,d:m::rwx,d:o::---", root).CombinedOutput(); err != nil {
					t.Skipf("ACL unavailable: %s %v", out, err)
				}
			}
			write := func(body string) error {
				return materializeWorkspaceFile(context.Background(), root, "nested/output.txt", strings.NewReader(body), int64(len(body)), "", 1024, false)
			}
			if err := write("original"); err != nil {
				t.Fatal(err)
			}
			if err := write("replacement"); err == nil {
				t.Fatal("accepted conflicting output")
			}
			body, err := os.ReadFile(filepath.Join(root, "nested/output.txt"))
			if err != nil || string(body) != "original" {
				t.Fatalf("conflict changed output: %q %v", body, err)
			}
			for _, entry := range []struct {
				path    string
				mode    os.FileMode
				aclMask string
			}{{"nested", 0777, "rwx"}, {"nested/output.txt", 0666, "rw-"}} {
				path := filepath.Join(root, entry.path)
				if acl {
					out, err := exec.Command("getfacl", "-cpn", path).CombinedOutput()
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(out), "mask::"+entry.aclMask+"\n") || !strings.Contains(string(out), "user:65534:rwx") {
						t.Fatalf("ACL cut access: %s", out)
					}
				} else {
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if info.Mode().Perm() != entry.mode&^os.FileMode(mask) {
						t.Fatalf("mode %o want %o", info.Mode().Perm(), entry.mode&^os.FileMode(mask))
					}
				}
			}
		})
	}
}
