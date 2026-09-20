package tool

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const (
	DestinationKindOwnedLane             = "owned_lane"
	DestinationKindCapturedPromotionOnly = "captured_promotion_only"
)

// CommittedSourceRequest selects an exact prior committed child session as an
// isolated allocation base for a follow-up correction or iteration.
type CommittedSourceRequest struct {
	TaskCallID     string `json:"task_call_id"`
	ChildSessionID string `json:"child_session_id"`
	HeadCommit     string `json:"head_commit"`
}

// Validate ensures all required fields are present and the commit OID is valid.
func (r CommittedSourceRequest) Validate() error {
	if strings.TrimSpace(r.TaskCallID) == "" {
		return errors.New("committed source requires task_call_id")
	}
	if strings.TrimSpace(r.ChildSessionID) == "" {
		return errors.New("committed source requires child_session_id")
	}
	head := strings.TrimSpace(r.HeadCommit)
	if head == "" || !validCommitID(head) {
		return errors.New("committed source requires a valid full hexadecimal head_commit")
	}
	return nil
}

// CommittedSourceBinding is the comparable immutable binding describing an
// authenticated prior committed Coder child source and its target destination.
// It contains value fields only (no pointers, slices, or maps) for safe equality
// comparisons during approval and launch admission.
type CommittedSourceBinding struct {
	TaskCallID                 string `json:"task_call_id"`
	ChildSessionID             string `json:"child_session_id"`
	HeadCommit                 string `json:"head_commit"`
	CanonicalSourcePath        string `json:"canonical_source_path"`
	DestinationKind            string `json:"destination_kind"`
	DestinationPath            string `json:"destination_path"`
	DestinationBranch          string `json:"destination_branch"`
	SourceWorktreePath         string `json:"source_worktree_path"`
	SourceBranch               string `json:"source_branch"`
	SourceBaseCommit           string `json:"source_base_commit"`
	SourceHeadCommit           string `json:"source_head_commit"`
	IntegrationBaseCommit      string `json:"integration_base_commit"`
	RepositoryIdentity         string `json:"repository_identity"`
	CurrentWorkspaceID         string `json:"current_workspace_id"`
	CurrentWorkspaceGeneration string `json:"current_workspace_generation"`
	ChildGeneration            uint64 `json:"child_generation"`
}

func validCommitID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// ResolveCommittedSource authenticates and resolves an immutable binding for a
// prior committed Coder child session. Source commit C serves as allocation base;
// inherited base B serves as delivery base; commit H is the final child HEAD.
func (r *Runtime) ResolveCommittedSource(scope WorkspaceScope, req CommittedSourceRequest) (CommittedSourceBinding, error) {
	if err := req.Validate(); err != nil {
		return CommittedSourceBinding{}, err
	}
	if r == nil || r.sessions == nil || r.worktrees == nil {
		return CommittedSourceBinding{}, errors.New("committed source services unavailable")
	}

	parent, err := r.manageWorktreeRecoveryParent(scope)
	if err != nil {
		return CommittedSourceBinding{}, err
	}

	child, err := r.manageWorktreeRecoveryChild(parent, map[string]any{"child_session_id": req.ChildSessionID})
	if err != nil {
		return CommittedSourceBinding{}, err
	}

	selectedRow, isProgram, sourceBaseCommit, err := r.authenticateCommittedSourceLineage(parent, child, req)
	if err != nil {
		return CommittedSourceBinding{}, err
	}

	childGen, err := r.checkDelegatedChildRotation(parent, child)
	if err != nil {
		return CommittedSourceBinding{}, err
	}

	lifecycleAuthority, ok := r.sessions.(interface {
		GetLifecycle(string) (pebblestore.SessionLifecycleSnapshot, bool, error)
	})
	if !ok {
		return CommittedSourceBinding{}, errors.New("committed source lifecycle authority unavailable")
	}
	lifecycle, found, err := lifecycleAuthority.GetLifecycle(child.ID)
	if err != nil {
		return CommittedSourceBinding{}, err
	}
	if !found {
		return CommittedSourceBinding{}, errors.New("committed source child lifecycle missing")
	}
	if lifecycle.Active || lifecycle.EndedAt == 0 || lifecycle.Phase != "completed" {
		return CommittedSourceBinding{}, errors.New("committed source requires an inactive ended completed child lifecycle")
	}

	if runIntentAuthority, ok := r.sessions.(interface {
		GetSessionActiveRunIntent(string) (pebblestore.V3SessionRunIntent, bool, error)
	}); ok {
		_, active, err := runIntentAuthority.GetSessionActiveRunIntent(child.ID)
		if err != nil {
			return CommittedSourceBinding{}, err
		}
		if active {
			return CommittedSourceBinding{}, errors.New("committed source child has an active run intent")
		}
	} else {
		return CommittedSourceBinding{}, errors.New("committed source active run intent authority unavailable")
	}

	childState, err := r.worktrees.InspectTaskWorkspace(child.WorktreeRootPath)
	if err != nil {
		return CommittedSourceBinding{}, fmt.Errorf("inspect child workspace: %w", err)
	}
	if !childState.Clean {
		return CommittedSourceBinding{}, fmt.Errorf("committed source child worktree is dirty:\n%s", childState.Status)
	}
	if childState.BranchName != child.WorktreeBranch {
		return CommittedSourceBinding{}, errors.New("child worktree branch disagrees with durable session")
	}
	if childState.HeadCommit != req.HeadCommit {
		return CommittedSourceBinding{}, errors.New("child live HEAD disagrees with requested head commit")
	}
	if sourceBaseCommit == "" || !validCommitID(sourceBaseCommit) {
		return CommittedSourceBinding{}, errors.New("committed source child has invalid base commit")
	}
	if sourceBaseCommit == req.HeadCommit {
		return CommittedSourceBinding{}, errors.New("committed source child has no new commits (base equals head)")
	}

	descends, err := r.worktrees.TaskCommitDescendsFrom(child.WorktreeRootPath, sourceBaseCommit, req.HeadCommit)
	if err != nil {
		return CommittedSourceBinding{}, fmt.Errorf("verify commit ancestry: %w", err)
	}
	if !descends {
		return CommittedSourceBinding{}, errors.New("child HEAD does not descend from base commit")
	}

	deliveryBase := sourceBaseCommit
	hasInheritedBase := false
	inheritedBase := strings.TrimSpace(firstNonEmptyString(
		asString(child.Metadata["integration_base_commit"]),
		asString(selectedRow["integration_base_commit"]),
	))
	if inheritedBase != "" {
		hasInheritedBase = true
	} else if _, ok := child.Metadata["committed_source"]; ok {
		hasInheritedBase = true
	} else if _, ok := selectedRow["committed_source"]; ok {
		hasInheritedBase = true
	} else if _, ok := child.Metadata["committed_source_binding"]; ok {
		hasInheritedBase = true
	} else if _, ok := selectedRow["committed_source_binding"]; ok {
		hasInheritedBase = true
	}

	if hasInheritedBase {
		if inheritedBase == "" {
			if bMap, ok := child.Metadata["committed_source_binding"].(map[string]any); ok {
				inheritedBase = strings.TrimSpace(asString(bMap["integration_base_commit"]))
			} else if bMap, ok := selectedRow["committed_source_binding"].(map[string]any); ok {
				inheritedBase = strings.TrimSpace(asString(bMap["integration_base_commit"]))
			}
		}
		if inheritedBase == "" || !validCommitID(inheritedBase) {
			return CommittedSourceBinding{}, errors.New("invalid committed source binding: invalid or missing integration_base_commit")
		}
		bDescends, bErr := r.worktrees.TaskCommitDescendsFrom(child.WorktreeRootPath, inheritedBase, sourceBaseCommit)
		if bErr != nil || !bDescends {
			return CommittedSourceBinding{}, errors.New("invalid committed source binding: allocation base does not descend from delivery base")
		}
		deliveryBase = inheritedBase
	}

	destKind, destPath, destBranch, canonicalSource, err := r.resolveCommittedSourceDestination(scope, parent, child, selectedRow, isProgram)
	if err != nil {
		return CommittedSourceBinding{}, err
	}

	_, err = r.worktrees.VerifyTaskIntegrationWorkspace(destPath, child.WorktreeRootPath, child.ID, child.WorktreeBranch, sourceBaseCommit, req.HeadCommit)
	if err != nil {
		return CommittedSourceBinding{}, fmt.Errorf("verify child task integration workspace: %w", err)
	}

	repoID, err := worktreeruntime.RepositoryIdentity(canonicalSource)
	if err != nil {
		return CommittedSourceBinding{}, fmt.Errorf("resolve repository identity: %w", err)
	}
	childRepoID, err := worktreeruntime.RepositoryIdentity(child.WorktreeRootPath)
	if err != nil {
		return CommittedSourceBinding{}, fmt.Errorf("resolve child repository identity: %w", err)
	}
	if repoID != childRepoID {
		return CommittedSourceBinding{}, errors.New("child worktree repository identity does not match source repository")
	}

	var currentWorkspaceID, currentWorkspaceGen string
	if r.workspace != nil {
		saved, sErr := r.workspace.ScopeForPathForPrincipal(scope.Principal, canonicalSource)
		if sErr == nil && saved.Matched {
			currentWorkspaceID = saved.WorkspaceID
			if saved.WorkspaceGeneration != 0 {
				currentWorkspaceGen = strconv.FormatInt(saved.WorkspaceGeneration, 10)
			}
		}
	}

	return CommittedSourceBinding{
		TaskCallID:                 req.TaskCallID,
		ChildSessionID:             req.ChildSessionID,
		HeadCommit:                 req.HeadCommit,
		CanonicalSourcePath:        canonicalSource,
		DestinationKind:            destKind,
		DestinationPath:            destPath,
		DestinationBranch:          destBranch,
		SourceWorktreePath:         child.WorktreeRootPath,
		SourceBranch:               child.WorktreeBranch,
		SourceBaseCommit:           sourceBaseCommit,
		SourceHeadCommit:           req.HeadCommit,
		IntegrationBaseCommit:      deliveryBase,
		RepositoryIdentity:         repoID,
		CurrentWorkspaceID:         currentWorkspaceID,
		CurrentWorkspaceGeneration: currentWorkspaceGen,
		ChildGeneration:            childGen,
	}, nil
}

func (r *Runtime) authenticateCommittedSourceLineage(parent, child pebblestore.SessionSnapshot, req CommittedSourceRequest) (map[string]any, bool, string, error) {
	programID := strings.TrimSpace(asString(child.Metadata["task_program_id"]))
	if programID != "" {
		authority, ok := r.sessions.(interface {
			InspectTaskProgram(string, string) (pebblestore.TaskProgramRecord, bool, error)
		})
		if !ok {
			return nil, true, "", errors.New("committed source Task Program authority unavailable")
		}
		program, found, err := authority.InspectTaskProgram(parent.ID, programID)
		if err != nil {
			return nil, true, "", err
		}
		callID := program.ReservationCallID
		if !found || program.ParentSessionID != parent.ID || program.ProgramID != programID || callID == "" ||
			asString(child.Metadata["parent_task_call_id"]) != callID || req.TaskCallID != callID {
			return nil, true, "", errors.New("committed source child is not in the selected parent Task Program")
		}
		if program.State != pebblestore.TaskProgramStateCompleted {
			return nil, true, "", errors.New("committed source requires a terminal completed Task Program")
		}
		var selectedJob *pebblestore.TaskProgramJobRecord
		for i := range program.Jobs {
			job := &program.Jobs[i]
			if job.JobID != asString(child.Metadata["task_program_job_id"]) || firstNonEmptyString(job.CurrentSessionID, job.ChildSessionID) != child.ID {
				continue
			}
			if selectedJob != nil {
				return nil, true, "", errors.New("ambiguous committed source child job")
			}
			selectedJob = job
		}
		if selectedJob == nil {
			return nil, true, "", errors.New("committed source child is not the current canonical Task Program job child")
		}
		if selectedJob.CurrentRunID == "" || selectedJob.WorkspacePath == "" || selectedJob.WorktreeBranch == "" || selectedJob.ImmutableStageBase == "" {
			return nil, true, "", errors.New("committed source requires complete job lineage")
		}
		if selectedJob.ChildHead != req.HeadCommit {
			return nil, true, "", errors.New("committed source job head disagrees with requested head commit")
		}
		if selectedJob.State != pebblestore.TaskProgramJobCompleted &&
			selectedJob.State != pebblestore.TaskProgramJobIntegrated &&
			selectedJob.State != pebblestore.TaskProgramJobHandoffReady {
			return nil, true, "", errors.New("committed source requires a successful job outcome")
		}
		row := map[string]any{
			"child_session_id":   child.ID,
			"task_call_id":       callID,
			"job_state":          selectedJob.State,
			"current_run_id":     selectedJob.CurrentRunID,
			"worktree_root_path": selectedJob.WorkspacePath,
			"worktree_branch":    selectedJob.WorktreeBranch,
			"parent_branch":      selectedJob.ParentBranch,
			"base_commit":        selectedJob.ImmutableStageBase,
			"head_commit":        selectedJob.ChildHead,
		}
		return row, true, selectedJob.ImmutableStageBase, nil
	}

	recordedCallID := asString(child.Metadata["parent_task_call_id"])
	if recordedCallID != "" && recordedCallID != req.TaskCallID {
		return nil, false, "", errors.New("committed source child task call disagrees with durable session")
	}
	launches, _ := parent.Metadata["task_launches"].(map[string]any)
	rawEntry, exists := launches[req.TaskCallID]
	if !exists {
		return nil, false, "", errors.New("task call is not present in parent task_launches")
	}

	for cID, rEntry := range launches {
		if cID == req.TaskCallID {
			continue
		}
		entry, _ := rEntry.(map[string]any)
		for _, raw := range manageWorktreeLaunchRows(entry) {
			row, _ := raw.(map[string]any)
			if asString(row["child_session_id"]) == child.ID {
				return nil, false, "", errors.New("child session appears in multiple parent task calls")
			}
		}
	}

	entry, _ := rawEntry.(map[string]any)
	var selectedRow map[string]any
	for _, raw := range manageWorktreeLaunchRows(entry) {
		row, _ := raw.(map[string]any)
		if asString(row["child_session_id"]) != child.ID {
			continue
		}
		if selectedRow != nil {
			return nil, false, "", errors.New("ambiguous committed source child lineage")
		}
		selectedRow = row
	}
	if selectedRow == nil {
		return nil, false, "", errors.New("committed source child is not in the selected parent task call")
	}
	if errStr := asString(selectedRow["error"]); errStr != "" {
		return nil, false, "", fmt.Errorf("committed source child recorded error: %s", errStr)
	}
	if asString(selectedRow["head_commit"]) != req.HeadCommit {
		return nil, false, "", errors.New("committed source recorded head commit disagrees with request")
	}
	baseCommit := strings.TrimSpace(firstNonEmptyString(asString(selectedRow["base_commit"]), asString(child.Metadata["base_commit"])))
	return selectedRow, false, baseCommit, nil
}

func (r *Runtime) checkDelegatedChildRotation(parent, child pebblestore.SessionSnapshot) (uint64, error) {
	genAuth, ok := r.sessions.(interface {
		GetDelegatedChildGenerationBySession(accountScopeID, sessionID string) (pebblestore.DelegatedChildGenerationRecord, bool, error)
		GetDelegatedChildLineage(accountScopeID, logicalTaskID string) (pebblestore.DelegatedChildLineageRecord, bool, error)
		GetDelegatedWorktreeOwner(accountScopeID, workspacePath string) (pebblestore.ManagedWorktreeOwnerLease, bool, error)
	})
	if !ok {
		return 0, errors.New("delegated child rotation authority unavailable")
	}
	genRecord, found, err := genAuth.GetDelegatedChildGenerationBySession(parent.AccountScopeID, child.ID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, errors.New("delegated child generation record missing")
	}
	if genRecord.SessionID != child.ID || genRecord.AccountScopeID != parent.AccountScopeID {
		return 0, errors.New("delegated child generation record is inconsistent")
	}
	if genRecord.ParentSessionID != "" && genRecord.ParentSessionID != parent.ID {
		return 0, errors.New("delegated child generation record parent disagrees")
	}
	if genRecord.WorkspacePath != "" && filepath.Clean(genRecord.WorkspacePath) != filepath.Clean(child.WorktreeRootPath) {
		return 0, errors.New("delegated child generation workspace path disagrees")
	}
	if genRecord.WorktreeBranch != "" && genRecord.WorktreeBranch != child.WorktreeBranch {
		return 0, errors.New("delegated child generation worktree branch disagrees")
	}
	if genRecord.SuccessorSessionID != "" {
		return 0, errors.New("delegated child generation has been superseded")
	}

	lineageRecord, found, err := genAuth.GetDelegatedChildLineage(parent.AccountScopeID, genRecord.LogicalTaskID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, errors.New("delegated child lineage record missing")
	}
	if lineageRecord.CurrentSessionID != child.ID || lineageRecord.CurrentGeneration != genRecord.Generation {
		return 0, errors.New("delegated child lineage has been superseded or is inconsistent")
	}

	ownerLease, found, err := genAuth.GetDelegatedWorktreeOwner(parent.AccountScopeID, child.WorktreeRootPath)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, errors.New("delegated worktree owner lease missing")
	}
	if ownerLease.SessionID != child.ID || ownerLease.Generation != genRecord.Generation {
		return 0, errors.New("delegated worktree owner lease is inconsistent")
	}
	if ownerLease.WorktreeBranch != "" && ownerLease.WorktreeBranch != child.WorktreeBranch {
		return 0, errors.New("delegated worktree owner lease branch disagrees")
	}

	return uint64(genRecord.Generation), nil
}

func (r *Runtime) resolveCommittedSourceDestination(scope WorkspaceScope, parent, child pebblestore.SessionSnapshot, selectedRow map[string]any, isProgram bool) (string, string, string, string, error) {
	laneDest, err := r.manageWorktreeRecoveryDestination(scope, parent, child, selectedRow)
	if err == nil {
		destKind := DestinationKindOwnedLane
		destPath := laneDest
		var destBranch, canonicalSource string
		if parent.WorktreeEnabled && filepath.Clean(laneDest) == filepath.Clean(parent.WorktreeRootPath) {
			destBranch = parent.WorktreeBranch
			canonicalSource = asString(parent.Metadata["swarm_v3_source_workspace_path"])
		} else {
			history, _ := parent.Metadata["swarm_v3_worktree_history"].([]any)
			for _, raw := range history {
				item, _ := raw.(map[string]any)
				if filepath.Clean(asString(item["path"])) == filepath.Clean(laneDest) {
					destBranch = asString(item["branch"])
					canonicalSource = asString(item["source_workspace_path"])
					break
				}
			}
			if canonicalSource == "" {
				if authority, ok := r.sessions.(interface {
					TaskProgramRepositoryLanes(string) ([]pebblestore.TaskProgramRepositoryLane, error)
				}); ok {
					lanes, lErr := authority.TaskProgramRepositoryLanes(parent.ID)
					if lErr == nil {
						for _, lane := range lanes {
							if filepath.Clean(lane.WorkspacePath) == filepath.Clean(laneDest) {
								destBranch = lane.Branch
								canonicalSource = lane.SourcePath
								break
							}
						}
					}
				}
			}
		}
		if canonicalSource == "" || destBranch == "" {
			return "", "", "", "", errors.New("unable to determine owned lane details")
		}
		return destKind, destPath, destBranch, canonicalSource, nil
	}

	targetWorkspace := strings.TrimSpace(firstNonEmptyString(
		asString(child.Metadata["target_workspace_path"]),
		asString(selectedRow["parent_workspace_path"]),
		asString(child.Metadata["swarm_v3_source_workspace_path"]),
	))
	if targetWorkspace == "" {
		return "", "", "", "", fmt.Errorf("resolve destination: %w", err)
	}
	if !filepath.IsAbs(targetWorkspace) {
		return "", "", "", "", errors.New("target workspace path must be absolute")
	}
	if r.workspace == nil {
		return "", "", "", "", errors.New("workspace authority unavailable")
	}
	saved, scopeErr := r.workspace.ScopeForPathForPrincipal(scope.Principal, targetWorkspace)
	if scopeErr != nil {
		return "", "", "", "", fmt.Errorf("authorize recovery source workspace: %w", scopeErr)
	}
	if !saved.Matched || saved.WorkspacePath != targetWorkspace || saved.ResolvedPath != targetWorkspace {
		return "", "", "", "", errors.New("authorize recovery source: recorded source is not an exact canonical account-owned workspace")
	}

	if recID := strings.TrimSpace(asString(child.Metadata["swarm_v3_source_workspace_id"])); recID != "" && saved.WorkspaceID != "" {
		if recID != saved.WorkspaceID {
			return "", "", "", "", errors.New("recorded workspace ID disagrees with current catalog")
		}
	}
	if recGen := strings.TrimSpace(asString(child.Metadata["swarm_v3_source_workspace_generation"])); recGen != "" && saved.WorkspaceGeneration != 0 {
		if recGen != strconv.FormatInt(saved.WorkspaceGeneration, 10) {
			return "", "", "", "", errors.New("recorded workspace generation disagrees with current catalog")
		}
	}

	sourceRepoID, rErr := worktreeruntime.RepositoryIdentity(targetWorkspace)
	if rErr != nil {
		return "", "", "", "", fmt.Errorf("source repository identity: %w", rErr)
	}
	childRepoID, rErr := worktreeruntime.RepositoryIdentity(child.WorktreeRootPath)
	if rErr != nil {
		return "", "", "", "", fmt.Errorf("child repository identity: %w", rErr)
	}
	if sourceRepoID != childRepoID {
		return "", "", "", "", errors.New("child worktree does not belong to the target workspace repository")
	}

	targetState, iErr := r.worktrees.InspectTaskWorkspace(targetWorkspace)
	if iErr != nil {
		return "", "", "", "", fmt.Errorf("inspect target workspace: %w", iErr)
	}

	return DestinationKindCapturedPromotionOnly, targetWorkspace, targetState.BranchName, targetWorkspace, nil
}
