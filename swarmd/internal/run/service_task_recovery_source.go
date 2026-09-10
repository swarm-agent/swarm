package run

import (
	"encoding/hex"
	"errors"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/taskscope"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func (s *Service) allocateRecoveryTaskWorkspace(parent pebblestore.SessionSnapshot, launch taskLaunchPrepared, target, childID string) (worktreeruntime.Allocation, error) {
	if launch.RecoverySourceDigest == "" {
		return s.worktrees.AllocateTaskWorkspace(target, *launch.TaskBase, childID, launch.OwnedScope)
	}
	allocator, ok := s.worktrees.(interface {
		AllocateTaskWorkspaceWithSource(string, worktreeruntime.TaskBase, string, []string, func(string) error) (worktreeruntime.Allocation, error)
	})
	if !ok || s.tools == nil {
		return worktreeruntime.Allocation{}, errors.New("recovery allocation authority unavailable")
	}
	scope := tool.WorkspaceScope{SessionID: parent.ID, PrimaryPath: parent.WorkspacePath, Principal: launch.RecoveryPrincipal}
	source, err := s.tools.ReadRecoverySource(scope, launch.RecoverySourceDigest)
	if err != nil {
		return worktreeruntime.Allocation{}, err
	}
	if err := s.tools.ValidateRecoverySourceTarget(scope, source, target); err != nil {
		return worktreeruntime.Allocation{}, err
	}
	if err := validateRecoveryLaunch(launch.RecoverySourceDigest, launch.RequestedSubagent, launch.OwnedScope); err != nil {
		return worktreeruntime.Allocation{}, err
	}
	// Require the original base. Recovery is explicit source replacement, not a
	// three-way merge that could overwrite subsequently integrated parent work.
	if source.BaseCommit != launch.TaskBase.BaseCommit {
		return worktreeruntime.Allocation{}, errors.New("recovery base differs from replacement base; parent must reconcile selected source against the current committed base")
	}
	return allocator.AllocateTaskWorkspaceWithSource(target, *launch.TaskBase, childID, launch.OwnedScope, func(destination string) error {
		if _, err := s.tools.ReadRecoverySource(scope, launch.RecoverySourceDigest); err != nil {
			return err
		}
		if err := tool.MaterializeRecoverySource(destination, source, launch.OwnedScope); err != nil {
			return err
		}
		// Detect a producer restart or changed bytes across materialization before
		// returning the lane. The worker receives retained bytes, never sibling access.
		_, err := s.tools.ReadRecoverySource(scope, launch.RecoverySourceDigest)
		return err
	})
}

func validateRecoveryLaunch(digest, agent string, scopes []string) error {
	if digest == "" {
		return nil
	}
	if b, err := hex.DecodeString(digest); err != nil || len(b) != 32 {
		return errors.New("recovery_source_digest must be an exact retained SHA-256")
	}
	if !agentruntime.IsCoderAgentName(agent) || len(scopes) == 0 {
		return errors.New("recovery source requires Coder with explicit owned_scope")
	}
	for _, scope := range scopes {
		if err := taskscope.ValidateProgram(scope); err != nil {
			return err
		}
	}
	return nil
}
