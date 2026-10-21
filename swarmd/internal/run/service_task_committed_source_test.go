package run

import (
	"context"

	"os/exec"

	"strings"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func runCommittedSourceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type committedSourceFakeRunner struct {
	t     *testing.T
	calls int
	reqs  []provideriface.Request
}

func (r *committedSourceFakeRunner) ID() string { return "codex" }
func (r *committedSourceFakeRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	r.calls++
	r.reqs = append(r.reqs, req)
	runCommittedSourceGit(r.t, req.WorkspacePath, "commit", "--allow-empty", "-m", "synthetic corrective handoff")
	return provideriface.Response{Text: "Committed correction; parent validation required."}, nil
}
func (r *committedSourceFakeRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.CreateResponse(ctx, req)
}
