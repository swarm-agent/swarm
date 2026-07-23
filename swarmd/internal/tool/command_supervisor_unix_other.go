//go:build !linux && !windows

package tool

import (
	"errors"
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
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	return cmd.Process.Kill()
}

func filesystemFreeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
