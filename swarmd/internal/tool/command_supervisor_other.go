//go:build windows

package tool

import (
	"errors"
	"os/exec"
)

func preparePlatformCommand(cmd *exec.Cmd, cfg commandSupervisorConfig) (platformCommandHandle, error) {
	return nil, errors.New("cgroup v2 containment is only supported on Linux")
}

func platformKillCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func fallbackCommandContainment(reason string) commandContainment {
	return commandContainment{Mode: "parent_process", State: "degraded", Guarantee: "parent_only_no_descendant_containment", DegradedReason: reason}
}

func filesystemFreeBytes(path string) (uint64, error) {
	return 0, errors.New("filesystem reserve monitoring is unavailable on this platform")
}
