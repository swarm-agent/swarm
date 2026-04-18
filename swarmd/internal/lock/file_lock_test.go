package lock

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestParseProcessStartTicks(t *testing.T) {
	stat := "1234 (swarmd) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242 21 22"
	ticks, err := parseProcessStartTicks(stat)
	if err != nil {
		t.Fatalf("parseProcessStartTicks returned error: %v", err)
	}
	if ticks != 424242 {
		t.Fatalf("parseProcessStartTicks=%d, want 424242", ticks)
	}
}

func TestStaleLockDetectsPIDReuseByProcessStartTicks(t *testing.T) {
	restore := stubLockProcessFuncs(t, func() (uint64, error) {
		return 0, errors.New("not used")
	}, func(pid int) (uint64, error) {
		return 222, nil
	}, func(pid int) error {
		t.Fatalf("unexpected processAliveCheckFunc call for pid=%d", pid)
		return nil
	})
	defer restore()

	lockPath := writeLockMetadata(t, Metadata{PID: 1234, ProcessStartTicks: 111})
	stale, err := staleLock(lockPath)
	if err != nil {
		t.Fatalf("staleLock returned error: %v", err)
	}
	if !stale {
		t.Fatalf("staleLock=%t, want true", stale)
	}
}

func TestStaleLockKeepsLiveProcessWhenStartTicksMatch(t *testing.T) {
	restore := stubLockProcessFuncs(t, func() (uint64, error) {
		return 0, errors.New("not used")
	}, func(pid int) (uint64, error) {
		return 333, nil
	}, func(pid int) error {
		t.Fatalf("unexpected processAliveCheckFunc call for pid=%d", pid)
		return nil
	})
	defer restore()

	lockPath := writeLockMetadata(t, Metadata{PID: 1234, ProcessStartTicks: 333})
	stale, err := staleLock(lockPath)
	if err != nil {
		t.Fatalf("staleLock returned error: %v", err)
	}
	if stale {
		t.Fatalf("staleLock=%t, want false", stale)
	}
}

func TestStaleLockFallsBackToKillWhenStartTicksUnavailable(t *testing.T) {
	restore := stubLockProcessFuncs(t, func() (uint64, error) {
		return 0, errors.New("not used")
	}, func(pid int) (uint64, error) {
		return 0, errors.New("proc stat unavailable")
	}, func(pid int) error {
		return syscall.ESRCH
	})
	defer restore()

	lockPath := writeLockMetadata(t, Metadata{PID: 1234})
	stale, err := staleLock(lockPath)
	if err != nil {
		t.Fatalf("staleLock returned error: %v", err)
	}
	if !stale {
		t.Fatalf("staleLock=%t, want true", stale)
	}
}

func writeLockMetadata(t *testing.T, meta Metadata) string {
	t.Helper()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "swarmd.lock")
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
	return lockPath
}

func stubLockProcessFuncs(
	t *testing.T,
	current func() (uint64, error),
	process func(int) (uint64, error),
	alive func(int) error,
) func() {
	t.Helper()

	originalCurrent := currentProcessStartTicksFunc
	originalProcess := processStartTicksFunc
	originalAlive := processAliveCheckFunc

	currentProcessStartTicksFunc = current
	processStartTicksFunc = process
	processAliveCheckFunc = alive

	return func() {
		currentProcessStartTicksFunc = originalCurrent
		processStartTicksFunc = originalProcess
		processAliveCheckFunc = originalAlive
	}
}
