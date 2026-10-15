package run

import (
	"context"
	"errors"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"

	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type taskRotationWorktreeStub struct {
	*taskLaunchWorktreeStub
	state worktreeruntime.TaskWorkspaceState
}

func (s *taskRotationWorktreeStub) InspectTaskWorkspace(path string) (worktreeruntime.TaskWorkspaceState, error) {
	state := s.state
	state.WorkspacePath = path
	return state, nil
}

type taskRotationHarnessRunner struct {
	responses []provideriface.Response
	requests  []provideriface.Request
	calls     int
}

func (r *taskRotationHarnessRunner) ID() string { return "rotation-fake" }
func (r *taskRotationHarnessRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *taskRotationHarnessRunner) CreateResponseStreaming(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.requests = append(r.requests, req)
	if r.calls >= len(r.responses) {
		return provideriface.Response{}, errors.New("unexpected provider continuation")
	}
	response := r.responses[r.calls]
	r.calls++
	return response, nil
}
