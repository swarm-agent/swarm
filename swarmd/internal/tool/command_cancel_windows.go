//go:build windows

package tool

import (
	"context"
	"os/exec"
)

func prepareCommandForCancellation(cmd *exec.Cmd) {}

func watchCommandCancellation(ctx context.Context, cmd *exec.Cmd) func() {
	return func() {}
}
