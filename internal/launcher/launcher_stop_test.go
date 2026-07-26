package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestBackendStopCandidatePIDsRejectsUnverifiedLockProcess(t *testing.T) {
	dir := t.TempDir()
	profile := Profile{LockPath: filepath.Join(dir, "swarmd.lock"), PIDFile: filepath.Join(dir, "swarmd.pid")}
	self := os.Getpid()
	if err := os.WriteFile(profile.LockPath, []byte("{\"pid\":"+strconv.Itoa(self)+"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backendStopCandidatePIDs(profile); err == nil || !strings.Contains(err.Error(), "unverified process") {
		t.Fatalf("backendStopCandidatePIDs error = %v, want unverified process refusal", err)
	}
}

func TestWritePIDFileIsPrivate(t *testing.T) {
	profile := Profile{PIDFile: filepath.Join(t.TempDir(), "state", "swarmd.pid")}
	if err := writePIDFile(profile, 42); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(profile.PIDFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("pid file mode = %04o, want 0600", got)
	}
}

func TestBackendExitedAfterTerminateSignal(t *testing.T) {
	cmd := exec.Command("sh", "-c", "while :; do sleep 1; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("signal helper: %v", err)
	}
	err := cmd.Wait()
	if !backendExitedAfterTerminateSignal(err) {
		t.Fatalf("backendExitedAfterTerminateSignal(%v) = false, want true", err)
	}
}
