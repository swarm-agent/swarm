package launcher

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Requirement: first-run root bootstrap must reject attacker-owned/writable
// artifacts before executing swarmsetup. RunFirstInstall/firstInstallArtifact
// own admission; metadata and filesystem unit tests are the narrowest layer
// for trust rejection without mutating a host account or service.
func TestFirstInstallTrust(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  uint32
		mode os.FileMode
		want bool
	}{
		{"root executable", 0, 0755, true},
		{"root private directory", 0, os.ModeDir | 0700, true},
		{"nonroot owner", 1000, 0755, false},
		{"group writable", 0, 0775, false},
		{"world writable", 0, 0777, false},
		{"symlink", 0, os.ModeSymlink | 0755, false},
		{"root sticky world-writable directory", 0, os.ModeDir | os.ModeSticky | 0777, true},
		{"nonroot sticky world-writable directory", 1000, os.ModeDir | os.ModeSticky | 0777, false},
		{"root sticky world-writable regular file", 0, os.ModeSticky | 0777, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := trustedFirstInstallMetadata(firstInstallInfo{tc.mode, tc.uid}); got != tc.want {
				t.Fatalf("admission=%v want %v", got, tc.want)
			}
		})
	}
}

type firstInstallInfo struct {
	mode os.FileMode
	uid  uint32
}

func (i firstInstallInfo) Name() string       { return "fixture" }
func (i firstInstallInfo) Size() int64        { return 0 }
func (i firstInstallInfo) Mode() os.FileMode  { return i.mode }
func (i firstInstallInfo) ModTime() time.Time { return time.Time{} }
func (i firstInstallInfo) IsDir() bool        { return i.mode.IsDir() }
func (i firstInstallInfo) Sys() any {
	var mode uint32
	if i.mode&os.ModeSticky != 0 {
		mode |= syscall.S_ISVTX
	}
	return &syscall.Stat_t{Uid: i.uid, Mode: mode}
}

// Requirement: ordinary installed launchers and incomplete native bundles must
// not be treated as runnable installers; rejection must not create state.
func TestFirstInstallTrustedPathAcceptsTmp(t *testing.T) {
	if err := trustedFirstInstallPath("/tmp"); err != nil {
		t.Fatalf("trustedFirstInstallPath(/tmp) failed: %v", err)
	}
}

func TestFirstInstallArtifactAdmission(t *testing.T) {
	root := t.TempDir()
	if got, err := firstInstallArtifact(filepath.Join(root, "libexec", "swarm")); err != nil || got != "" {
		t.Fatalf("installed launcher admitted: %q %v", got, err)
	}
	if got, err := firstInstallArtifact(filepath.Join(root, "linux-amd64", "root", "swarm")); err == nil || got != "" {
		t.Fatalf("incomplete artifact admitted: %q %v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("admission mutated filesystem: %v %v", entries, err)
	}
}
