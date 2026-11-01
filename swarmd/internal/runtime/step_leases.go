package runtime

import (
	"context"
	"errors"
	"fmt"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/sandbox"
)

// stepLeaseOutputCap bounds what a step's lease command keeps in memory;
// the step's output itself streams through OnOutput into its log tail.
const stepLeaseOutputCap = 1 << 20

// stepLeases gives workflow run steps with in: environment the folder
// environment leases agents' bash uses (sandbox.Manager.TakeStepLease):
// the same image, isolation, lease user and network policy, with the folder
// and its worktrees mounted read-only. The manager is read on every call,
// so a daemon whose environments are off refuses with the reason.
type stepLeases struct{}

var _ jobautomation.EnvironmentLeases = stepLeases{}

func (stepLeases) manager() (*sandbox.Manager, error) {
	manager := sandbox.Leases()
	if manager == nil {
		return nil, errors.New(sandbox.EnvironmentsOffMessage)
	}
	return manager, nil
}

func (s stepLeases) TakeStepLease(ctx context.Context, folder, owner string) (string, error) {
	manager, err := s.manager()
	if err != nil {
		return "", err
	}
	info, err := manager.LeaseInfo(folder)
	if err != nil {
		return "", err
	}
	if info.Max <= 0 {
		return "", fmt.Errorf("%s has no environment set up for leases: the person's AI sets one up with manage_projects update_environment and max_leases", info.Root)
	}
	lease, err := manager.TakeStepLease(ctx, folder, owner)
	var full *sandbox.LeasesFullError
	if errors.As(err, &full) {
		return "", fmt.Errorf("%w (%d of %d in use)", jobautomation.ErrEnvironmentBusy, full.InUse, full.Max)
	}
	if err != nil {
		return "", err
	}
	return lease.ID, nil
}

func (s stepLeases) ExecLease(ctx context.Context, lease, owner string, cmd jobautomation.LeaseCommand) (jobautomation.LeaseResult, error) {
	manager, err := s.manager()
	if err != nil {
		return jobautomation.LeaseResult{}, err
	}
	result, err := manager.ExecLease(ctx, lease, owner, sandbox.ExecRequest{
		Command: cmd.Argv, WorkingDir: cmd.Dir, Env: cmd.Env, Stdin: cmd.Stdin,
		Timeout: cmd.Timeout, MaxTimeout: cmd.Timeout, MaxOutput: stepLeaseOutputCap, OnOutput: cmd.OnOutput,
	})
	if errors.Is(err, provider.ErrOperationTimedOut) {
		return jobautomation.LeaseResult{ExitCode: 124, TimedOut: true}, nil
	}
	if err != nil {
		return jobautomation.LeaseResult{}, err
	}
	if result == nil {
		return jobautomation.LeaseResult{}, errors.New("the lease returned no result")
	}
	return jobautomation.LeaseResult{ExitCode: result.ExitCode}, nil
}

func (s stepLeases) ReleaseLease(ctx context.Context, lease, owner string) error {
	manager, err := s.manager()
	if err != nil {
		return err
	}
	return manager.ReleaseLease(ctx, lease, owner)
}
