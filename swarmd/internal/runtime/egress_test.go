//go:build !windows

package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestWriteCombinedBundlePermissions exercises writeCombinedBundle at its file
// boundary: fresh and legacy bundles must be readable by sandbox users despite
// a restrictive service umask. Checking the output and directory modes here is
// the narrowest regression test; host trust-store contents are not assumed.
func TestWriteCombinedBundlePermissions(t *testing.T) {
	// Umask is process-wide: do not parallelize this test or its subtests.
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	for _, existing := range []bool{false, true} {
		name := "fresh"
		if existing {
			name = "existing0600"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			before, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "ca-bundle.pem")
			if existing {
				if err := os.WriteFile(path, []byte("old bundle"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			caPEM := []byte("test public CA\n")
			gotPath, err := writeCombinedBundle(dir, caPEM)
			if err != nil {
				t.Fatal(err)
			}
			if gotPath != path {
				t.Fatalf("bundle path = %q, want %q", gotPath, path)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o644 {
				t.Errorf("bundle mode = %04o, want 0644", got)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasSuffix(data, append([]byte{'\n'}, caPEM...)) {
				t.Error("bundle does not end with the supplied public CA")
			}
			after, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if after.Mode() != before.Mode() {
				t.Error("bundle write changed directory mode")
			}
		})
	}
}
