//go:build linux

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func developmentSupervisor(t *testing.T) *commandSupervisor {
	t.Helper()
	root := filepath.Join(t.TempDir(), "command-root")
	return &commandSupervisor{
		config: commandSupervisorConfig{
			Policy:           "degraded",
			TempRoot:         root,
			MemoryBytes:      defaultCommandMemoryBytes,
			PIDs:             defaultCommandPIDs,
			CPUQuotaUS:       defaultCommandCPUQuotaUS,
			CPUPeriodUS:      defaultCommandCPUPeriodUS,
			TempReserveBytes: 0,
			WorkspaceReserve: 0,
			PollInterval:     10 * time.Millisecond,
			WaitDelay:        100 * time.Millisecond,
			StaleAge:         time.Hour,
		},
		now: time.Now,
	}
}

func TestCommandSupervisorRequiredContainmentFailsClosed(t *testing.T) {
	workspace := t.TempDir()
	supervisor := developmentSupervisor(t)
	supervisor.config.Policy = "required"
	supervisor.preparePlatform = func(*exec.Cmd, commandSupervisorConfig) (platformCommandHandle, error) {
		return nil, errors.New("delegation unavailable")
	}
	result := supervisor.run(context.Background(), WorkspaceScope{PrimaryPath: workspace}, "printf should-not-run", &strings.Builder{}, &strings.Builder{})
	if result.TerminationReason != "containment_unavailable" || result.Containment.State != "failed_closed" || result.Err == nil {
		t.Fatalf("fail-closed result = %#v", result)
	}
}

func TestCommandSupervisorPrivateTempLifecycle(t *testing.T) {
	workspace := t.TempDir()
	supervisor := developmentSupervisor(t)
	var output strings.Builder
	result := supervisor.run(context.Background(), WorkspaceScope{PrimaryPath: workspace},
		`printf '%s\n%s\n%s\n' "$TMPDIR" "$TMP" "$TEMP"; stat -c '%a' "$TMPDIR"`, &output, &output)
	if result.Err != nil {
		t.Fatalf("run: %v", result.Err)
	}
	lines := strings.Fields(output.String())
	if len(lines) != 4 || lines[0] != lines[1] || lines[1] != lines[2] || lines[3] != "700" {
		t.Fatalf("private temp environment = %q", output.String())
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatalf("command temp still exists after terminal cleanup: %v", err)
	}
	if result.Containment.State != "degraded" && result.Containment.State != "contained" {
		t.Fatalf("containment = %#v", result.Containment)
	}
}

func TestCommandTempRootRejectsSymlinkAndUnsafePermissions(t *testing.T) {
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real-root")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkRoot := filepath.Join(parent, "symlink-root")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if err := ensureCommandTempRoot(symlinkRoot); err == nil {
		t.Fatal("symlink command temp root was accepted")
	}
	unsafeRoot := filepath.Join(parent, "unsafe-root")
	if err := os.Mkdir(unsafeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureCommandTempRoot(unsafeRoot); err == nil {
		t.Fatal("unsafe command temp root permissions were accepted")
	}
}

func TestCommandSupervisorTimeoutKillsProcessTreeAndBoundsWait(t *testing.T) {
	workspace := t.TempDir()
	childPID := filepath.Join(workspace, "child.pid")
	supervisor := developmentSupervisor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := supervisor.run(ctx, WorkspaceScope{PrimaryPath: workspace},
		`sleep 30 & echo $! > child.pid; wait`, &strings.Builder{}, &strings.Builder{})
	if !result.TimedOut || result.TerminationReason != "timeout" {
		t.Fatalf("timeout result = %#v", result)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("wait was not bounded: %s", elapsed)
	}
	data, err := os.ReadFile(childPID)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid := strings.TrimSpace(string(data))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join("/proc", pid)); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %s survived supervisor timeout", pid)
}

func TestCommandTempStaleSweepRequiresOwnershipMarker(t *testing.T) {
	supervisor := developmentSupervisor(t)
	root := supervisor.config.TempRoot
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(root, "command-owned")
	unowned := filepath.Join(root, "command-unowned")
	for _, dir := range []string{owned, unowned} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(owned, commandTempMarker), []byte("owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, dir := range []string{owned, unowned} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := supervisor.sweepStaleCommandTemps(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned stale temp was not removed: %v", err)
	}
	if _, err := os.Stat(unowned); err != nil {
		t.Fatalf("unowned directory was removed: %v", err)
	}
}

func TestCommandSupervisorDiskReserveClassification(t *testing.T) {
	supervisor := developmentSupervisor(t)
	supervisor.config.TempReserveBytes = ^uint64(0)
	temp, err := supervisor.createCommandTemp()
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.cleanupCommandTemp(temp)
	reason, err := supervisor.reserveViolation(t.TempDir(), temp)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "temp_disk_reserve" {
		t.Fatalf("reserve reason = %q", reason)
	}
}

func TestBashResultIncludesActionableContainmentMetadata(t *testing.T) {
	t.Setenv("SWARMD_BASH_CONTAINMENT_POLICY", "degraded")
	t.Setenv("SWARMD_COMMAND_TEMP_ROOT", filepath.Join(t.TempDir(), "command-root"))
	output, err := executeBashCommand(context.Background(), WorkspaceScope{PrimaryPath: t.TempDir()}, map[string]any{"timeout_ms": 1000}, "printf ok", nil)
	if err != nil {
		t.Fatalf("execute bash: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"termination_reason", "containment", "temp_guarantee", "disk_reserve_guarantee"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("result missing %q: %s", key, output)
		}
	}
}

func TestCgroupEventClassification(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "memory.events"), []byte("oom 1\noom_kill 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readCgroupEvent(root, "memory.events", "oom_kill"); got != 2 {
		t.Fatalf("oom_kill = %d", got)
	}
	handle := &cgroupCommandHandle{path: root, memoryOOM: 1}
	if got := handle.classifyTermination(); got != "memory_limit" {
		t.Fatalf("classification = %q", got)
	}
}
