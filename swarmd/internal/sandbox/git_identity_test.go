package sandbox

import (
	"context"

	"os/exec"

	"strings"
)

type identityConfigRunner struct {
	fakeRunner
	env []string
	dir string
}

func (r *identityConfigRunner) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	_, _ = r.record(args)
	i := 1
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		i += 2
	}
	i++ // box name
	cmd := exec.CommandContext(ctx, args[i], args[i+1:]...)
	cmd.Dir, cmd.Env = r.dir, r.env
	return cmd.CombinedOutput()
}
