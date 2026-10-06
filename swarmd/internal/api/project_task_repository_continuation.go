package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func (s *Server) validateTaskRepositoryContinuation(p identity.Principal, task *pebblestore.ProjectTaskRecord, ref pebblestore.ProjectTaskRepositoryContinuation) error {
	if task.AccountID != p.AccountScopeID { return errors.New("continuation account mismatch") }
	_, err := s.sessions.Store().AuthenticateTaskRepositoryContinuation(task, p.UserID, ref)
	if err != nil { return err }
	if s.workspace == nil { return errors.New("continuation catalog unavailable") }
	canonical, err := s.workspace.ScopeForPathForPrincipal(p, ref.Source.Path)
	if err != nil || !canonical.Matched || canonical.WorkspacePath != ref.Source.Path || canonical.ResolvedPath != ref.Source.Path || canonical.WorkspaceID != ref.Source.WorkspaceID || canonical.WorkspaceGeneration != ref.Source.WorkspaceGeneration { return errors.New("continuation catalog identity changed") }
	target, err := s.worktrees.InspectTaskWorkspace(ref.Source.Path)
	if err != nil || !target.Clean || target.BranchName != ref.TargetBranch { return errors.New("continuation captured destination changed or dirty") }
	validator, ok := s.worktrees.(interface {
		ValidateTaskRepositoryLane(string, string, string, string) error
		TaskCommitDescendsFrom(string, string, string) (bool, error)
	})
	if !ok { return errors.New("repository continuation Git authority unavailable") }
	digest := sha256.Sum256([]byte(ref.SessionID + "\x00" + ref.Source.Path))
	seed := "program-lane-" + hex.EncodeToString(digest[:12])
	if err := validator.ValidateTaskRepositoryLane(ref.Source.Path, ref.Lane.WorkspacePath, seed, ref.Lane.Branch); err != nil { return err }
	state, err := s.worktrees.InspectTaskWorkspace(ref.Lane.WorkspacePath)
	if err != nil || !state.Clean || state.BranchName != ref.Lane.Branch || state.HeadCommit != ref.HeadCommit { return errors.New("retained repository lane unavailable, dirty or changed") }
	targetContains, err := validator.TaskCommitDescendsFrom(ref.Source.Path, ref.TargetHead, target.HeadCommit)
	if err != nil || !targetContains { return errors.New("continuation captured destination head unavailable or divergent") }
	descends, err := validator.TaskCommitDescendsFrom(ref.Source.Path, ref.Lane.BaseCommit, ref.HeadCommit)
	if err != nil || !descends { return errors.New("retained repository result ancestry mismatch") }
	// Canonical integration may cherry-pick child commits. LaneHeads is the
	// persisted resulting Git head; child hashes need not be its ancestors.
	return nil
}

// Search recorded attempts, including a useful program preceding a no-op retry.
// Useful histories are ordered only by exact recorded correction edges;
// recency or coincidental Git ancestry is insufficient and ambiguity fails closed.
func (s *Server) resolveTaskRepositoryContinuations(p identity.Principal, task *pebblestore.ProjectTaskRecord, sources []pebblestore.ProjectTaskSource) ([]pebblestore.ProjectTaskRepositoryContinuation, error) {
	copyTask := *task
	copyTask.Attempts = append([]pebblestore.ProjectTaskAttempt(nil), task.Attempts...)
	copyTask.EnsureTaskAttempts()
	copyTask.CaptureActiveAttempt()
	selected := map[string]pebblestore.ProjectTaskRepositoryContinuation{}
	provenLanes := map[string]pebblestore.ProjectTaskRepositoryContinuation{}
	// Reauthenticate already-pinned edges before accepting recorded descendants.
	// This also retains exact target heads after source branch movement.
	for _, attempt := range copyTask.Attempts {
		for _, ref := range attempt.RepositoryContinuations {
			if err := s.validateTaskRepositoryContinuation(p, &copyTask, ref); err != nil { return nil, err }
		}
	}
	for _, attempt := range copyTask.Attempts {
		if len(attempt.RepositoryContinuations) == 0 { continue }
		active := &attempt
		owner, found, err := s.sessions.Store().GetSession(active.SessionID)
		if err != nil || !found { return nil, errors.New("retained correction owner unavailable") }
		for _, ref := range active.RepositoryContinuations {
			path := owner.WorktreeRootPath
			if !ref.Source.SameIdentity(task.SourceWorkspace) {
				path = ""
				rows, _ := owner.Metadata["swarm_v3_worktree_history"].([]any)
				for _, row := range rows {
					item, ok := row.(map[string]any)
					if ok && item["source_workspace_path"] == ref.Source.Path { path, _ = item["path"].(string) }
				}
			}
			validator, ok := s.worktrees.(interface { ValidateSessionRepositoryLane(string, string, string, string) error })
			if !ok { return nil, errors.New("retained correction ownership validator unavailable") }
			state, err := s.worktrees.InspectTaskWorkspace(path)
			if err == nil { err = validator.ValidateSessionRepositoryLane(ref.Source.Path, path, active.SessionID, state.BranchName) }
			if err != nil || !state.Clean { return nil, errors.New("retained correction unavailable or dirty; reconcile before another follow-up") }
			// Direct edits outside a completed program remain a separate,
			// explicit reconciliation boundary; never discard those commits.
			if state.HeadCommit != ref.HeadCommit { return nil, errors.New("direct correction lane changed outside retained program; reconcile before another follow-up") }
		}
	}
	type pendingProgram struct {
		attempt pebblestore.ProjectTaskAttempt
		program pebblestore.TaskProgramRecord
	}
	var pending []pendingProgram
	for _, attempt := range copyTask.Attempts {
		programs, err := s.sessions.Store().ListTaskPrograms(attempt.SessionID)
		if err != nil { return nil, err }
		for _, program := range programs {
			if len(program.RepositoryLanes) == 0 { continue }
			if _, err := s.sessions.Store().AuthenticateTaskRepositoryProgram(&copyTask, p.UserID, attempt.ID, attempt.SessionID, program.ProgramID); err != nil { return nil, err }
			if program.State != pebblestore.TaskProgramStateCompleted {
				pending = append(pending, pendingProgram{attempt, program})
				continue
			}
			for path, lane := range program.RepositoryLanes {
				head := program.LaneHeads[path]
				if head == "" || head == lane.BaseCommit { return nil, errors.New("completed repository program has no authenticated committed result") }
				if len(selected) >= 64 { return nil, errors.New("retained repository result inventory exceeds bound") }
				var source pebblestore.ProjectTaskSource
				for _, admitted := range sources { if admitted.Path == path { source = admitted } }
				if source.Path == "" { return nil, errors.New("retained result has no admitted catalog source") }
				target, err := s.worktrees.InspectTaskWorkspace(source.Path)
				if err != nil || !target.Clean || target.BranchName == "" { return nil, errors.New("continuation target unavailable or dirty") }
				if source.SameIdentity(task.SourceWorkspace) && attempt.BaseBranch != "" && attempt.BaseBranch != target.BranchName { return nil, errors.New("retained program intended destination branch changed") }
				ref := pebblestore.ProjectTaskRepositoryContinuation{Source: source, AttemptID: attempt.ID, SessionID: attempt.SessionID, ProgramID: program.ProgramID, ProgramRevision: program.Revision, Lane: lane, HeadCommit: head, TargetBranch: target.BranchName, TargetHead: target.HeadCommit}
				for _, inherited := range attempt.RepositoryContinuations {
					if inherited.Source.SameIdentity(source) { ref.TargetBranch, ref.TargetHead = inherited.TargetBranch, inherited.TargetHead }
				}
				if err := s.validateTaskRepositoryContinuation(p, &copyTask, ref); err != nil { return nil, err }
				if prior, ok := selected[path]; ok && prior.HeadCommit != head {
					// Only a recorded correction edge, not timestamp or Git ancestry
					// alone, may supersede an earlier result.
					linked := false
					for _, inherited := range attempt.RepositoryContinuations {
						linked = linked || (inherited.Source.SameIdentity(prior.Source) && inherited.AttemptID == prior.AttemptID && inherited.SessionID == prior.SessionID && inherited.ProgramID == prior.ProgramID && inherited.ProgramRevision == prior.ProgramRevision && inherited.Lane == prior.Lane && inherited.HeadCommit == prior.HeadCommit && lane.BaseCommit == prior.HeadCommit && ref.TargetBranch == inherited.TargetBranch)
					}
					if !linked { return nil, fmt.Errorf("ambiguous retained repository results for %q", path) }
				}
				// Preserve a stable identity for equivalent heads; multiple
				// programs sharing one lane cannot reorder correction edges.
				if prior, ok := selected[path]; !ok || prior.HeadCommit != head { selected[path] = ref }
				provenLanes[lane.WorkspacePath] = ref
			}
		}
	}
	for _, item := range pending {
		if err := s.validateEmptyTaskRepositoryProgram(p, &copyTask, item.attempt, item.program, sources, provenLanes); err != nil { return nil, err }
	}
	paths := make([]string, 0, len(selected))
	for path := range selected { paths = append(paths, path) }
	sort.Strings(paths)
	refs := make([]pebblestore.ProjectTaskRepositoryContinuation, 0, len(paths))
	for _, path := range paths { refs = append(refs, selected[path]) }
	return refs, nil
}

// All evidence is persisted by reservation before allocation. Deterministic
// session-owned branches recover partial allocation without touching old lanes.
func (s *Server) allocateTaskRepositoryContinuations(p identity.Principal, task *pebblestore.ProjectTaskRecord, snapshot *pebblestore.SessionSnapshot) error {
	a := task.ActiveAttempt()
	if a == nil || len(a.RepositoryContinuations) == 0 { return nil }
	allocator, ok := s.worktrees.(interface {
		AllocateProjectTaskFollowup(identity.Principal, string, string, string, string, string) (worktreeruntime.Allocation, error)
	})
	if !ok { return errors.New("repository continuation allocator unavailable") }
	for _, ref := range a.RepositoryContinuations {
		if err := s.validateTaskRepositoryContinuation(p, task, ref); err != nil { return err }
	}
	history := []any{}
	for _, ref := range a.RepositoryContinuations {
		if ref.Source.SameIdentity(task.SourceWorkspace) { continue }
		digest := sha256.Sum256([]byte(task.SessionID + "\x00" + ref.Source.Path))
		branch := "agent/followup-repository-" + hex.EncodeToString(digest[:12])
		alloc, err := allocator.AllocateProjectTaskFollowup(p, ref.Source.Path, task.SessionID, branch, ref.HeadCommit, ref.TargetBranch)
		if err != nil { return err }
		history = append(history, map[string]any{"path": alloc.WorkspacePath, "workspace_id": ref.Source.WorkspaceID, "workspace_generation": ref.Source.WorkspaceGeneration, "source_workspace_path": ref.Source.Path, "owner_session_id": task.SessionID, "branch": alloc.BranchName, "base_branch": ref.TargetBranch, "base_commit": ref.Lane.BaseCommit})
		available := true
		snapshot.WorkspaceGrants = append(snapshot.WorkspaceGrants, pebblestore.WorkspaceGrant{Kind: pebblestore.WorkspaceGrantWorktree, WorkspaceID: ref.Source.WorkspaceID, WorkspaceGeneration: ref.Source.WorkspaceGeneration, Path: alloc.WorkspacePath, Available: &available})
	}
	snapshot.Metadata["swarm_v3_worktree_history"] = history
	snapshot.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(snapshot.WorkspaceGrants)
	return nil
}

// A failed launch is ignorable only when it demonstrably never produced child
// work. Some programs in one attempt share the deterministic repository lane;
// a later completed program may own its new head, but an unexplained head cannot.
func (s *Server) validateEmptyTaskRepositoryProgram(p identity.Principal, task *pebblestore.ProjectTaskRecord, attempt pebblestore.ProjectTaskAttempt, program pebblestore.TaskProgramRecord, sources []pebblestore.ProjectTaskSource, selected map[string]pebblestore.ProjectTaskRepositoryContinuation) error {
	fail := errors.New("retained repository program requires explicit reconciliation")
	if program.State != pebblestore.TaskProgramStateFailed && program.State != pebblestore.TaskProgramStateCancelled { return fail }
	for _, job := range program.Jobs {
		if job.ChildSessionID != "" || job.CurrentSessionID != "" || job.CurrentRunID != "" || job.ChildHead != "" || job.WorkspacePath != "" || job.HandoffRef != nil || job.ArtifactRef != nil || len(job.GenerationHistory) != 0 || (job.IntegrationState != "" && job.IntegrationState != "launch_rejected") || (job.State != pebblestore.TaskProgramJobDeclared && job.State != pebblestore.TaskProgramJobFailed && job.State != pebblestore.TaskProgramJobCancelled && job.State != pebblestore.TaskProgramJobBlocked) { return fail }
	}
	owner, found, err := s.sessions.Store().GetSession(attempt.SessionID)
	if err != nil || !found { return fail }
	validator, ok := s.worktrees.(interface { ValidateTaskRepositoryLane(string, string, string, string) error })
	if !ok { return fail }
	for path, lane := range program.RepositoryLanes {
		var source pebblestore.ProjectTaskSource
		for _, admitted := range sources { if admitted.Path == path { source = admitted } }
		if source.Path == "" || lane.SourcePath != path || lane.WorkspaceID != source.WorkspaceID || lane.WorkspaceGeneration != source.WorkspaceGeneration || lane.BaseCommit == "" { return fail }
		canonical, err := s.workspace.ScopeForPathForPrincipal(p, path)
		if err != nil || !canonical.Matched || canonical.ResolvedPath != path || canonical.WorkspacePath != path || canonical.WorkspaceID != source.WorkspaceID || canonical.WorkspaceGeneration != source.WorkspaceGeneration { return fail }
		granted := source.SameIdentity(task.SourceWorkspace)
		for _, grant := range owner.WorkspaceGrants { granted = granted || (grant.Path == path && grant.WorkspaceID == source.WorkspaceID && grant.WorkspaceGeneration == source.WorkspaceGeneration) }
		if !granted { return fail }
		if head := program.LaneHeads[path]; head != "" && head != lane.BaseCommit { return fail }
		digest := sha256.Sum256([]byte(attempt.SessionID + "\x00" + path))
		seed := "program-lane-" + hex.EncodeToString(digest[:12])
		if err := validator.ValidateTaskRepositoryLane(path, lane.WorkspacePath, seed, lane.Branch); err != nil { return fail }
		state, err := s.worktrees.InspectTaskWorkspace(lane.WorkspacePath)
		if err != nil || !state.Clean || state.BranchName != lane.Branch { return fail }
		if state.HeadCommit != lane.BaseCommit {
			result, matched := selected[lane.WorkspacePath]
			if !matched || result.SessionID != attempt.SessionID || result.Lane != lane || result.HeadCommit != state.HeadCommit { return fail }
		}
	}
	return nil
}
