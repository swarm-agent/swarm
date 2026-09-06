package run

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// repositoryLane authenticates the requested source before allocating or reusing
// a parent-owned destination. The immutable binding is persisted before launch.
func (p *taskProgramScheduler) repositoryLane(requested string) (string, error) {
	if p.service == nil || p.service.sessions == nil || p.service.worktrees == nil {
		return "", errors.New("Task Program repository lane authorities unavailable")
	}
	target, _, err := p.service.resolveTaskTargetWorkspace(p.parentSession, p.req.Principal, taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: requested})
	if err != nil {
		return "", err
	}
	// Preflight runs before program persistence and must not allocate resources.
	if p.record.Revision == 0 {
		if _, err := p.service.worktrees.ResolveTaskBase(target); err != nil {
			return "", err
		}
		return target, nil
	}
	digest := sha256.Sum256([]byte(p.parentSession.ID + "\x00" + target))
	seed := "program-lane-" + hex.EncodeToString(digest[:12])
	lane := p.record.RepositoryLane
	if lane != nil && !sameTaskProgramPath(lane.SourcePath, target) {
		return "", errors.New("Task Program repository lane source mismatch")
	}
	if lane == nil {
		lanes, err := p.service.sessions.TaskProgramRepositoryLanes(p.parentSession.ID)
		if err != nil {
			return "", err
		}
		for _, saved := range lanes {
			if sameTaskProgramPath(saved.SourcePath, target) {
				copy := saved
				lane = &copy
				break
			}
		}
	}
	if lane == nil {
		base, err := p.service.worktrees.ResolveTaskBase(target)
		if err != nil {
			return "", err
		}
		allocation, err := p.service.worktrees.AllocateTaskWorkspace(target, base, seed, nil)
		if err != nil {
			return "", err
		}
		lane = &pebblestore.TaskProgramRepositoryLane{SourcePath: target, WorkspacePath: allocation.WorkspacePath, Branch: allocation.BranchName, BaseCommit: base.BaseCommit}
		record, _, persistErr := p.service.sessions.TransitionTaskProgram(p.parentSession.ID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: fmt.Sprintf("lane:%d", p.record.Revision), RepositoryLane: lane})
		if persistErr != nil {
			return "", errors.Join(persistErr, p.service.worktrees.RollbackAllocation(allocation))
		}
		p.record = record
	} else if p.record.RepositoryLane == nil {
		record, _, err := p.service.sessions.TransitionTaskProgram(p.parentSession.ID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: fmt.Sprintf("lane:%d", p.record.Revision), RepositoryLane: lane})
		if err != nil {
			return "", err
		}
		p.record = record
	}
	validator, ok := p.service.worktrees.(interface {
		ValidateTaskRepositoryLane(string, string, string, string) error
	})
	if !ok {
		return "", errors.New("task repository lane ownership validator unavailable")
	}
	if err := validator.ValidateTaskRepositoryLane(target, lane.WorkspacePath, seed, lane.Branch); err != nil {
		return "", err
	}
	state, err := p.service.worktrees.InspectTaskWorkspace(lane.WorkspacePath)
	if err != nil {
		return "", err
	}
	if !state.Clean || state.BranchName != lane.Branch || sameTaskProgramPath(lane.SourcePath, lane.WorkspacePath) {
		return "", errors.New("Task Program repository lane is stale or dirty")
	}
	descends, err := p.service.worktrees.TaskCommitDescendsFrom(lane.WorkspacePath, lane.BaseCommit, state.HeadCommit)
	if err != nil {
		return "", err
	}
	if !descends {
		return "", errors.New("Task Program repository lane no longer descends from its captured base")
	}
	return lane.WorkspacePath, nil
}
