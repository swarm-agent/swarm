package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestBackendStopCandidatePIDsPrefersDaemonLockBeforeLauncherPID(t *testing.T) {
	dir := t.TempDir()
	profile := Profile{
		LockPath: filepath.Join(dir, "swarmd.lock"),
		PIDFile:  filepath.Join(dir, "swarmd.pid"),
	}
	self := os.Getpid()
	if err := os.WriteFile(profile.LockPath, []byte("{\"pid\":"+strconv.Itoa(self)+"}"), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	if err := os.WriteFile(profile.PIDFile, []byte(strconv.Itoa(self)+"\n"), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	pids, err := backendStopCandidatePIDs(profile)
	if err != nil {
		t.Fatalf("backendStopCandidatePIDs: %v", err)
	}
	if len(pids) != 1 || pids[0] != self {
		t.Fatalf("pids = %v, want [%d]", pids, self)
	}
}

func TestBackendStopCandidatePIDsIncludesLauncherAndDaemonWhenDistinct(t *testing.T) {
	dir := t.TempDir()
	profile := Profile{
		LockPath: filepath.Join(dir, "swarmd.lock"),
		PIDFile:  filepath.Join(dir, "swarmd.pid"),
	}
	self := os.Getpid()
	parent := os.Getppid()
	if parent <= 0 || parent == self {
		t.Skip("distinct parent pid is unavailable")
	}
	if err := os.WriteFile(profile.LockPath, []byte("{\"pid\":"+strconv.Itoa(self)+"}"), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	if err := os.WriteFile(profile.PIDFile, []byte(strconv.Itoa(parent)+"\n"), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	pids, err := backendStopCandidatePIDs(profile)
	if err != nil {
		t.Fatalf("backendStopCandidatePIDs: %v", err)
	}
	want := map[int]bool{self: true, parent: true}
	if len(pids) != len(want) {
		t.Fatalf("pids = %v, want self and parent", pids)
	}
	for _, pid := range pids {
		if !want[pid] {
			t.Fatalf("unexpected pid %d in %v", pid, pids)
		}
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
