//go:build !linux && !windows

package tool

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func preparePlatformCommand(cmd *exec.Cmd, cfg commandSupervisorConfig) (platformCommandHandle, error) {
	return nil, errors.New("cgroup v2 containment is only supported on Linux")
}

func platformKillCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	} else if processErr := cmd.Process.Kill(); processErr != nil && !errors.Is(processErr, os.ErrProcessDone) {
		return fmt.Errorf("kill process group: %v; kill leader: %w", err, processErr)
	} else {
		return fmt.Errorf("kill process group: %w", err)
	}
}

func fallbackCommandContainment(reason string) commandContainment {
	return commandContainment{Mode: "process_group", State: "degraded", Guarantee: "best_effort_process_tree", DegradedReason: reason}
}

func filesystemFreeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
