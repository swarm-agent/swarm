//go:build windows

package action

import (
	"context"
	"os"
	"os/exec"
)

func prepareActionCommand(cmd *exec.Cmd) {}

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
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-done:
		}
	}()
	return func() { close(done) }
}

func actionProcessTerminationSignal(state *os.ProcessState) string {
	return ""
}
