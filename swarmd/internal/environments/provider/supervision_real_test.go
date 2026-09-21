package provider

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func checkRealSupervisorPrerequisites(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("real process supervision tests require Linux")
	}
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("/proc filesystem not available")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not found in PATH")
	}
	if _, err := exec.LookPath("kill"); err != nil {
		t.Skip("kill not found in PATH")
	}
}

// TestRealSupervisor_BasicExecution proves that the supervisor starts a real process
// in its own session/process group via setsid, records verified pid/pgid/stat/starttime,
// and leaves a completion tombstone on clean exit.
func TestRealSupervisor_BasicExecution(t *testing.T) {
	checkRealSupervisorPrerequisites(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseDir := filepath.Join(t.TempDir(), "swarm-ops")
	opID := "op-real-basic"
	opDir := filepath.Join(baseDir, opID)

	cmd := exec.CommandContext(ctx, "sh", "-c", supervisorScript, "swarm-supervisor", opID, "sh", "-c", "echo hello-from-supervisor; exit 0")
	cmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("supervisor failed: %v (stderr: %s)", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "hello-from-supervisor") {
		t.Fatalf("expected output in stdout, got: %q", stdout.String())
	}

	// Verify recorded metadata
	pidBytes, err := os.ReadFile(filepath.Join(opDir, "pid"))
	if err != nil {
		t.Fatalf("failed to read pid file: %v", err)
	}
	pidStr := strings.TrimSpace(string(pidBytes))
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 1 {
		t.Fatalf("invalid recorded PID: %q", pidStr)
	}

	pgidBytes, err := os.ReadFile(filepath.Join(opDir, "pgid"))
	if err != nil {
		t.Fatalf("failed to read pgid file: %v", err)
	}
	pgidStr := strings.TrimSpace(string(pgidBytes))
	pgid, err := strconv.Atoi(pgidStr)
	if err != nil || pgid <= 1 {
		t.Fatalf("invalid recorded PGID: %q", pgidStr)
	}

	if pgid != pid {
		t.Fatalf("expected PGID == PID for setsid session leader, got PGID=%d, PID=%d", pgid, pid)
	}

	statusBytes, err := os.ReadFile(filepath.Join(opDir, "status"))
	if err != nil || strings.TrimSpace(string(statusBytes)) != "exited" {
		t.Fatalf("expected status 'exited', got: %q (err: %v)", string(statusBytes), err)
	}

	exitCodeBytes, err := os.ReadFile(filepath.Join(opDir, "exitcode"))
	if err != nil || strings.TrimSpace(string(exitCodeBytes)) != "0" {
		t.Fatalf("expected exitcode '0', got: %q (err: %v)", string(exitCodeBytes), err)
	}

	// Verify completion tombstone is retained until manager acknowledges
	if _, err := os.Stat(filepath.Join(opDir, "tombstone")); err != nil {
		t.Fatalf("expected completion tombstone to be retained, got err: %v", err)
	}
}

// TestRealSupervisor_NonZeroExit proves that non-zero exit codes from real commands
// are accurately captured in the completion tombstone.
func TestRealSupervisor_NonZeroExit(t *testing.T) {
	checkRealSupervisorPrerequisites(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseDir := filepath.Join(t.TempDir(), "swarm-ops")
	opID := "op-real-nonzero"
	opDir := filepath.Join(baseDir, opID)

	cmd := exec.CommandContext(ctx, "sh", "-c", supervisorScript, "swarm-supervisor", opID, "sh", "-c", "exit 42")
	cmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit code error from cmd.Run()")
	}

	exitCodeBytes, err := os.ReadFile(filepath.Join(opDir, "exitcode"))
	if err != nil || strings.TrimSpace(string(exitCodeBytes)) != "42" {
		t.Fatalf("expected exitcode '42', got: %q (err: %v)", string(exitCodeBytes), err)
	}
}

// TestRealSupervisor_DescendantCleanup proves that cleanupScript reliably terminates
// real background descendant processes in the supervisor's process group.
func TestRealSupervisor_DescendantCleanup(t *testing.T) {
	checkRealSupervisorPrerequisites(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	baseDir := filepath.Join(t.TempDir(), "swarm-ops")
	opID := "op-real-desc-clean"
	opDir := filepath.Join(baseDir, opID)

	// Command spawns background sleep children and waits
	supCmd := exec.CommandContext(ctx, "sh", "-c", supervisorScript, "swarm-supervisor", opID, "sh", "-c", "sleep 60 & sleep 60 & wait")
	supCmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

	err := supCmd.Start()
	if err != nil {
		t.Fatalf("failed to start supervisor: %v", err)
	}
	t.Cleanup(func() {
		_ = supCmd.Process.Kill()
		_ = supCmd.Wait()
	})

	// Wait for supervisor to write pid and pgid
	var pgidStr string
	for i := 0; i < 50; i++ {
		b, err := os.ReadFile(filepath.Join(opDir, "pgid"))
		if err == nil && len(b) > 0 {
			pgidStr = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pgidStr == "" {
		t.Fatal("supervisor did not write pgid within timeout")
	}

	pgid, err := strconv.Atoi(pgidStr)
	if err != nil || pgid <= 1 {
		t.Fatalf("invalid PGID: %q", pgidStr)
	}

	// Verify group exists and has active processes
	cleanCmd := exec.CommandContext(ctx, "sh", "-c", cleanupScript, "swarm-cleanup", opID, "1")
	cleanCmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

	out, err := cleanCmd.CombinedOutput()
	outStr := string(out)
	if err != nil {
		t.Fatalf("cleanup failed: %v (output: %s)", err, outStr)
	}

	if !strings.Contains(outStr, "SWARM_CLEANUP:TERMINATED") {
		t.Fatalf("expected SWARM_CLEANUP:TERMINATED, got: %s", outStr)
	}

	// Verify process group is terminated
	time.Sleep(100 * time.Millisecond)
	entries, err := os.ReadDir("/proc")
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 1 {
				continue
			}
			statBytes, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
			if err != nil {
				continue
			}
			postComm := strings.SplitN(string(statBytes), ") ", 2)
			if len(postComm) == 2 {
				fields := strings.Fields(postComm[1])
				if len(fields) >= 3 && fields[2] == pgidStr && fields[0] != "Z" {
					t.Fatalf("descendant PID %s in PGID %s is still running after cleanup", entry.Name(), pgidStr)
				}
			}
		}
	}
}

// TestRealSupervisor_TERMResistantChild proves that cleanupScript successfully escalates
// to SIGKILL when a process ignores SIGTERM.
func TestRealSupervisor_TERMResistantChild(t *testing.T) {
	checkRealSupervisorPrerequisites(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	baseDir := filepath.Join(t.TempDir(), "swarm-ops")
	opID := "op-real-term-resist"
	opDir := filepath.Join(baseDir, opID)

	// Command traps and ignores SIGTERM, running until SIGKILL
	supCmd := exec.CommandContext(ctx, "sh", "-c", supervisorScript, "swarm-supervisor", opID, "sh", "-c", "trap '' TERM; sleep 60")
	supCmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

	err := supCmd.Start()
	if err != nil {
		t.Fatalf("failed to start supervisor: %v", err)
	}
	t.Cleanup(func() {
		_ = supCmd.Process.Kill()
		_ = supCmd.Wait()
	})

	var pidStr string
	for i := 0; i < 50; i++ {
		b, err := os.ReadFile(filepath.Join(opDir, "pid"))
		if err == nil && len(b) > 0 {
			pidStr = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pidStr == "" {
		t.Fatal("supervisor did not write pid within timeout")
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 1 {
		t.Fatalf("invalid PID: %q", pidStr)
	}

	// Run cleanup with 1 second grace
	cleanCmd := exec.CommandContext(ctx, "sh", "-c", cleanupScript, "swarm-cleanup", opID, "1")
	cleanCmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

	out, err := cleanCmd.CombinedOutput()
	outStr := string(out)
	if err != nil {
		t.Fatalf("cleanup failed: %v (output: %s)", err, outStr)
	}

	if !strings.Contains(outStr, "SWARM_CLEANUP:TERMINATED") {
		t.Fatalf("expected SWARM_CLEANUP:TERMINATED after SIGKILL escalation, got: %s", outStr)
	}

	// Verify PID is no longer running
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join("/proc", pidStr)); err == nil {
		t.Fatalf("TERM-resistant process %s is still running after SIGKILL", pidStr)
	}
}

// TestRealSupervisor_LeaderExitRetainingDescendants proves that when a command leader exits
// cleanly but leaves background descendants running, the completion tombstone retains
// the PGID metadata, allowing cleanupScript to subsequently find and terminate them.
func TestRealSupervisor_LeaderExitRetainingDescendants(t *testing.T) {
	checkRealSupervisorPrerequisites(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	baseDir := filepath.Join(t.TempDir(), "swarm-ops")
	opID := "op-real-leader-exit"
	opDir := filepath.Join(baseDir, opID)

	// Leader exits cleanly with 0, leaving background sleep running in the same process group
	supCmd := exec.CommandContext(ctx, "sh", "-c", supervisorScript, "swarm-supervisor", opID, "sh", "-c", "sleep 60 & exit 0")
	supCmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

	err := supCmd.Run()
	if err != nil {
		t.Fatalf("supervisor failed on leader exit: %v", err)
	}

	// Verify tombstone was retained
	pgidBytes, err := os.ReadFile(filepath.Join(opDir, "pgid"))
	if err != nil {
		t.Fatalf("failed to read pgid after leader exit: %v", err)
	}
	pgidStr := strings.TrimSpace(string(pgidBytes))

	// Now run cleanup on the completed operation
	cleanCmd := exec.CommandContext(ctx, "sh", "-c", cleanupScript, "swarm-cleanup", opID, "1")
	cleanCmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

	out, err := cleanCmd.CombinedOutput()
	outStr := string(out)
	if err != nil {
		t.Fatalf("cleanup failed: %v (output: %s)", err, outStr)
	}

	if !strings.Contains(outStr, "SWARM_CLEANUP:TERMINATED") {
		t.Fatalf("expected SWARM_CLEANUP:TERMINATED for background descendants, got: %s", outStr)
	}

	// Verify no background descendants in that PGID remain
	time.Sleep(100 * time.Millisecond)
	entries, err = os.ReadDir("/proc")
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 1 {
				continue
			}
			statBytes, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
			if err != nil {
				continue
			}
			postComm := strings.SplitN(string(statBytes), ") ", 2)
			if len(postComm) == 2 {
				fields := strings.Fields(postComm[1])
				if len(fields) >= 3 && fields[2] == pgidStr && fields[0] != "Z" {
					t.Fatalf("background descendant PID %s in PGID %s survived cleanup", entry.Name(), pgidStr)
				}
			}
		}
	}
}

// TestRealSupervisor_CancelBeforeExecFence proves that setting the cancellation fence
// before the command launches prevents execution entirely and exits 130 immediately.
func TestRealSupervisor_CancelBeforeExecFence(t *testing.T) {
	checkRealSupervisorPrerequisites(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseDir := filepath.Join(t.TempDir(), "swarm-ops")
	opID := "op-real-fence"
	opDir := filepath.Join(baseDir, opID)

	markerFile := filepath.Join(t.TempDir(), "should_not_exist.txt")

	// Pre-set cancellation fence
	if err := os.MkdirAll(opDir, 0700); err != nil {
		t.Fatalf("failed to create opDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opDir, "cancelled"), []byte("cancelled"), 0600); err != nil {
		t.Fatalf("failed to write cancelled fence: %v", err)
	}

	supCmd := exec.CommandContext(ctx, "sh", "-c", supervisorScript, "swarm-supervisor", opID, "sh", "-c", "touch "+markerFile)
	supCmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

	err := supCmd.Run()
	if err == nil {
		t.Fatal("expected exit status 130 when cancelled before exec")
	}

	// Verify command was NOT executed
	if _, err := os.Stat(markerFile); err == nil {
		t.Fatal("marker file was created; command executed despite cancellation fence!")
	}

	statusBytes, err := os.ReadFile(filepath.Join(opDir, "status"))
	if err != nil || strings.TrimSpace(string(statusBytes)) != "cancelled" {
		t.Fatalf("expected status 'cancelled', got: %q", string(statusBytes))
	}
}

// TestRealSupervisor_PIDReuseProtection proves that cleanupScript verifies starttime
// and does not kill an unrelated process if the PID has been reused.
func TestRealSupervisor_PIDReuseProtection(t *testing.T) {
	checkRealSupervisorPrerequisites(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseDir := filepath.Join(t.TempDir(), "swarm-ops")
	opID := "op-real-reuse"
	opDir := filepath.Join(baseDir, opID)

	// Launch a dummy process
	dummyCmd := exec.CommandContext(ctx, "sleep", "60")
	if err := dummyCmd.Start(); err != nil {
		t.Fatalf("failed to start dummy process: %v", err)
	}
	t.Cleanup(func() {
		_ = dummyCmd.Process.Kill()
		_ = dummyCmd.Wait()
	})

	dummyPID := dummyCmd.Process.Pid

	// Write metadata with dummy PID but bogus starttime
	if err := os.MkdirAll(opDir, 0700); err != nil {
		t.Fatalf("failed to create opDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opDir, "pid"), []byte(strconv.Itoa(dummyPID)), 0600); err != nil {
		t.Fatalf("failed to write pid: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opDir, "pgid"), []byte(strconv.Itoa(dummyPID)), 0600); err != nil {
		t.Fatalf("failed to write pgid: %v", err)
	}
	// Bogus starttime that won't match dummy process's actual starttime
	if err := os.WriteFile(filepath.Join(opDir, "starttime"), []byte("999999999"), 0600); err != nil {
		t.Fatalf("failed to write starttime: %v", err)
	}

	cleanCmd := exec.CommandContext(ctx, "sh", "-c", cleanupScript, "swarm-cleanup", opID, "1")
	cleanCmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

	out, err := cleanCmd.CombinedOutput()
	outStr := string(out)
	if err != nil {
		t.Fatalf("cleanup failed: %v (output: %s)", err, outStr)
	}

	if !strings.Contains(outStr, "SWARM_CLEANUP:PID_REUSE_DETECTED") && !strings.Contains(outStr, "SWARM_CLEANUP:ALREADY_TERMINATED") {
		t.Fatalf("expected PID_REUSE_DETECTED or ALREADY_TERMINATED, got: %s", outStr)
	}

	// Verify dummy process was NOT killed
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(dummyPID))); err != nil {
		t.Fatalf("dummy process %d was killed by PID reuse bug!", dummyPID)
	}
}

// TestRealSupervisor_InvalidMetadataSafety proves that forged, non-numeric, or <= 1 IDs
// are rejected and never result in dangerous system-wide kill invocations.
func TestRealSupervisor_InvalidMetadataSafety(t *testing.T) {
	checkRealSupervisorPrerequisites(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseDir := filepath.Join(t.TempDir(), "swarm-ops")

	testCases := []struct {
		name       string
		opID       string
		pid        string
		pgid       string
		expectedErr string
	}{
		{"invalid op id with traversal", "../traversal", "100", "100", "SWARM_CLEANUP:INVALID_OP_ID"},
		{"invalid op id with shell chars", "op;rm -rf", "100", "100", "SWARM_CLEANUP:INVALID_OP_ID"},
		{"pid 1 protection", "op-pid1", "1", "100", "SWARM_CLEANUP:INVALID_PID"},
		{"pid 0 protection", "op-pid0", "0", "100", "SWARM_CLEANUP:INVALID_PID"},
		{"negative pid", "op-pid-neg", "-100", "100", "SWARM_CLEANUP:INVALID_PID"},
		{"alphabetic pid", "op-pid-alpha", "abc", "100", "SWARM_CLEANUP:INVALID_PID"},
		{"pgid 1 protection", "op-pgid1", "100", "1", "SWARM_CLEANUP:INVALID_PGID"},
		{"pgid 0 protection", "op-pgid0", "100", "0", "SWARM_CLEANUP:INVALID_PGID"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opDir := filepath.Join(baseDir, tc.opID)
			_ = os.MkdirAll(opDir, 0700)
			_ = os.WriteFile(filepath.Join(opDir, "pid"), []byte(tc.pid), 0600)
			_ = os.WriteFile(filepath.Join(opDir, "pgid"), []byte(tc.pgid), 0600)

			cleanCmd := exec.CommandContext(ctx, "sh", "-c", cleanupScript, "swarm-cleanup", tc.opID, "1")
			cleanCmd.Env = append(os.Environ(), "SWARM_OPERATIONS_DIR="+baseDir)

			out, _ := cleanCmd.CombinedOutput()
			outStr := string(out)
			if !strings.Contains(outStr, tc.expectedErr) {
				t.Fatalf("expected error output %q, got: %s", tc.expectedErr, outStr)
			}
		})
	}
}
