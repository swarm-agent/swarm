//go:build !windows

package egress

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestLoadOrCreateCAPermissions proves at the filesystem boundary owned by
// LoadOrCreateCA that a restrictive service umask cannot make the mounted public
// certificate unreadable, including legacy files, without exposing or replacing
// the private key. No sandbox or provider is needed to prove these file modes.
func TestLoadOrCreateCAPermissions(t *testing.T) {
	// Umask is process-wide: this test and its subtests must not run in parallel.
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	for _, existing := range []bool{false, true} {
		name := "fresh"
		if existing {
			name = "existing0600"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "egress")
			certPath := filepath.Join(dir, caCertFile)
			keyPath := filepath.Join(dir, caKeyFile)
			var originalCert, originalKey []byte
			if existing {
				ca, err := LoadOrCreateCA(dir)
				if err != nil {
					t.Fatal(err)
				}
				originalCert = ca.CertPEM()
				originalKey, err = os.ReadFile(keyPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(certPath, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ca, err := LoadOrCreateCA(dir)
			if err != nil {
				t.Fatal(err)
			}
			for path, want := range map[string]os.FileMode{certPath: 0o644, keyPath: 0o600, dir: 0o700} {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := info.Mode().Perm(); got != want {
					t.Errorf("%s mode = %04o, want %04o", filepath.Base(path), got, want)
				}
			}
			if existing {
				key, err := os.ReadFile(keyPath)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(originalCert, ca.CertPEM()) || !bytes.Equal(originalKey, key) {
					t.Fatal("loading existing CA changed certificate or private key contents")
				}
			}
		})
	}
}
