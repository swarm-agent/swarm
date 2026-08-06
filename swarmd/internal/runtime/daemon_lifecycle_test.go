package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveStaleUnixSocketRejectsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.sock")
	if err := os.WriteFile(path, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleUnixSocket(path); err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("removeStaleUnixSocket error = %v, want refusal", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "do not delete" {
		t.Fatalf("regular file was changed: data=%q err=%v", data, err)
	}
}

func TestRemoveStaleUnixSocketRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "api.sock")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleUnixSocket(path); err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("removeStaleUnixSocket error = %v, want refusal", err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was changed: info=%v err=%v", info, err)
	}
}

func TestWaitForShutdownReturnsRestartErrorOnlyForReleaseUpdate(t *testing.T) {
	tests := []struct {
		name        string
		reason      string
		wantRestart bool
	}{
		{name: "release update", reason: "api:update-release", wantRestart: true},
		{name: "normal API shutdown", reason: "api:requested", wantRestart: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Daemon{stopCh: make(chan string, 1)}
			d.requestStop(tt.reason)
			err := d.waitForShutdown()
			if got := errors.Is(err, ErrReleaseUpdateRestart); got != tt.wantRestart {
				t.Fatalf("waitForShutdown error = %v, restart=%v, want %v", err, got, tt.wantRestart)
			}
		})
	}
}
