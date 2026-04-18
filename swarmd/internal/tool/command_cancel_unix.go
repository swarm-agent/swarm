//go:build !windows

package tool

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

const commandCancelGracePeriod = 250 * time.Millisecond

func prepareCommandForCancellation(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func watchCommandCancellation(ctx context.Context, cmd *exec.Cmd) func() {
	if ctx == nil || cmd == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			select {
			case <-done:
				return
			default:
			}
			signalCommandProcessGroup(cmd, syscall.SIGTERM)
			timer := time.NewTimer(commandCancelGracePeriod)
			defer timer.Stop()
			select {
			case <-done:
				return
			case <-timer.C:
			}
			signalCommandProcessGroup(cmd, syscall.SIGKILL)
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-done:
		}
	}()
	return func() {
		close(done)
	}
}

func signalCommandProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, sig)
}
