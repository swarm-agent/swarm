//go:build !windows

package action

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const actionCancelGracePeriod = 250 * time.Millisecond

func prepareActionCommand(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func watchActionCommandCancellation(ctx context.Context, cmd *exec.Cmd) func() {
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
			signalActionCommandGroup(cmd, syscall.SIGTERM)
			timer := time.NewTimer(actionCancelGracePeriod)
			defer timer.Stop()
			select {
			case <-done:
				return
			case <-timer.C:
			}
			signalActionCommandGroup(cmd, syscall.SIGKILL)
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-done:
		}
	}()
	return func() { close(done) }
}

func signalActionCommandGroup(cmd *exec.Cmd, signal syscall.Signal) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, signal)
}

func actionProcessTerminationSignal(state *os.ProcessState) string {
	if state == nil {
		return ""
	}
	waitStatus, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !waitStatus.Signaled() {
		return ""
	}
	return waitStatus.Signal().String()
}
