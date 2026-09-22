package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
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

// Validate ensures all required fields are present without surrounding whitespace
// and the commit OID is a valid full hexadecimal SHA.
func (r CommittedSourceRequest) Validate() error {
	taskCall := strings.TrimSpace(r.TaskCallID)
	if taskCall == "" || taskCall != r.TaskCallID {
		return errors.New("committed source requires task_call_id without surrounding whitespace")
	}
	childID := strings.TrimSpace(r.ChildSessionID)
	if childID == "" || childID != r.ChildSessionID {
		return errors.New("committed source requires child_session_id without surrounding whitespace")
	}
	head := strings.TrimSpace(r.HeadCommit)
	if head == "" || head != r.HeadCommit || !validCommitID(head) {
		return errors.New("committed source requires a valid full hexadecimal head_commit without surrounding whitespace")
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

func parseCommittedSourceBinding(raw any) (CommittedSourceBinding, error) {
	if b, ok := raw.(CommittedSourceBinding); ok {
		return b, nil
	}
	if b, ok := raw.(*CommittedSourceBinding); ok && b != nil {
		return *b, nil
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return CommittedSourceBinding{}, err
	}
	var binding CommittedSourceBinding
	if err := json.Unmarshal(bytes, &binding); err != nil {
		return CommittedSourceBinding{}, err
	}
	return binding, nil
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
	if lifecycle.Active || lifecycle.EndedAt == 0 || lifecycle.Phase != "completed" || lifecycle.Error != "" {
		return CommittedSourceBinding{}, errors.New("committed source requires an inactive ended completed child lifecycle without errors")
	}
	if isProgram {
		jobRunID := strings.TrimSpace(asString(selectedRow["current_run_id"]))
		if jobRunID == "" || lifecycle.RunID == "" || jobRunID != lifecycle.RunID {
			return CommittedSourceBinding{}, errors.New("committed source program job run ID disagrees with child lifecycle run ID")
		}
	} else {
		if rowRunID := strings.TrimSpace(asString(selectedRow["child_run_id"])); rowRunID != "" && lifecycle.RunID != "" && rowRunID != lifecycle.RunID {
			return CommittedSourceBinding{}, errors.New("committed source launch run ID disagrees with child lifecycle run ID")
		}
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

	destKind, destPath, destBranch, canonicalSource, err := r.resolveCommittedSourceDestination(scope, parent, child, selectedRow, isProgram)
	if err != nil {
		return CommittedSourceBinding{}, err
	}

	deliveryBase, err := r.authenticateLineageDeliveryBase(parent, child, selectedRow, child.WorktreeRootPath, sourceBaseCommit, req.HeadCommit, 0)
	if err != nil {
		return CommittedSourceBinding{}, fmt.Errorf("authenticate delivery base: %w", err)
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

	saved, sErr := r.workspace.ScopeForPathForPrincipal(scope.Principal, canonicalSource)
	if sErr != nil || !saved.Matched || saved.WorkspacePath != canonicalSource || saved.ResolvedPath != canonicalSource || saved.WorkspaceID == "" {
		return CommittedSourceBinding{}, errors.New("canonical source catalog identity became unavailable")
	}
	currentWorkspaceID := saved.WorkspaceID
	currentWorkspaceGen := strconv.FormatInt(saved.WorkspaceGeneration, 10)
	if recorded := asString(child.Metadata["swarm_v3_source_workspace_generation"]); recorded != "" && recorded != currentWorkspaceGen {
		return CommittedSourceBinding{}, errors.New("source workspace generation is stale")
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
		callID := strings.TrimSpace(program.ReservationCallID)
		childCallID := strings.TrimSpace(asString(child.Metadata["parent_task_call_id"]))
		if !found || program.ParentSessionID != parent.ID || program.ProgramID != programID || callID == "" ||
			childCallID == "" || childCallID != callID || req.TaskCallID != callID {
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
		if filepath.Clean(selectedJob.WorkspacePath) != filepath.Clean(child.WorktreeRootPath) {
			return nil, true, "", errors.New("committed source child worktree path disagrees with Task Program job")
		}
		if selectedJob.WorktreeBranch != child.WorktreeBranch {
			return nil, true, "", errors.New("committed source child worktree branch disagrees with Task Program job")
		}
		childBase := strings.TrimSpace(asString(child.Metadata["base_commit"]))
		if childBase == "" || selectedJob.ImmutableStageBase != childBase {
			return nil, true, "", errors.New("committed source child base commit disagrees with Task Program job")
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

	recordedCallID := strings.TrimSpace(asString(child.Metadata["parent_task_call_id"]))
	if recordedCallID == "" || recordedCallID != req.TaskCallID {
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
	if asString(selectedRow["phase"]) != "completed" {
		return nil, false, "", errors.New("committed source requires a successfully completed recorded handoff")
	}
	if errStr := asString(selectedRow["error"]); errStr != "" {
		return nil, false, "", fmt.Errorf("committed source child recorded error: %s", errStr)
	}
	if childErr := asString(child.Metadata["error"]); childErr != "" {
		return nil, false, "", fmt.Errorf("committed source child metadata error: %s", childErr)
	}
	if asString(selectedRow["head_commit"]) != req.HeadCommit {
		return nil, false, "", errors.New("committed source recorded head commit disagrees with request")
	}
	if childHead := strings.TrimSpace(asString(child.Metadata["head_commit"])); childHead != "" && childHead != req.HeadCommit {
		return nil, false, "", errors.New("committed source child metadata head commit disagrees with request")
	}

	rowPath := strings.TrimSpace(asString(selectedRow["worktree_root_path"]))
	if rowPath == "" || filepath.Clean(rowPath) != filepath.Clean(child.WorktreeRootPath) {
		return nil, false, "", errors.New("committed source child worktree path disagrees with parent launch record")
	}
	rowBranch := strings.TrimSpace(asString(selectedRow["worktree_branch"]))
	if rowBranch == "" || rowBranch != child.WorktreeBranch {
		return nil, false, "", errors.New("committed source child worktree branch disagrees with parent launch record")
	}
	rowBase := strings.TrimSpace(asString(selectedRow["base_commit"]))
	childBase := strings.TrimSpace(asString(child.Metadata["base_commit"]))
	if rowBase == "" || childBase == "" || rowBase != childBase {
		return nil, false, "", errors.New("committed source child base commit disagrees with parent launch record")
	}
	if rowBase == req.HeadCommit {
		return nil, false, "", errors.New("committed source child has no new commits (base equals head)")
	}

	return selectedRow, false, rowBase, nil
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
	if genRecord.Generation <= 0 {
		return 0, errors.New("delegated child generation must be positive")
	}
	logicalTaskID := strings.TrimSpace(genRecord.LogicalTaskID)
	if recorded := asString(child.Metadata["logical_task_id"]); recorded != "" && recorded != logicalTaskID {
		return 0, errors.New("child logical task disagrees with generation authority")
	}
	if logicalTaskID == "" {
		return 0, errors.New("delegated child generation record missing logical_task_id")
	}
	if genRecord.ParentSessionID != parent.ID {
		return 0, errors.New("delegated child generation record parent disagrees")
	}
	if filepath.Clean(genRecord.WorkspacePath) != filepath.Clean(child.WorktreeRootPath) {
		return 0, errors.New("delegated child generation workspace path disagrees")
	}
	if genRecord.WorktreeBranch != child.WorktreeBranch {
		return 0, errors.New("delegated child generation worktree branch disagrees")
	}
	childBase := strings.TrimSpace(asString(child.Metadata["base_commit"]))
	if genRecord.ImmutableBaseCommit == "" || childBase == "" || genRecord.ImmutableBaseCommit != childBase {
		return 0, errors.New("delegated child generation immutable base commit disagrees")
	}
	if genRecord.SuccessorSessionID != "" {
		return 0, errors.New("delegated child generation has been superseded")
	}

	lineageRecord, found, err := genAuth.GetDelegatedChildLineage(parent.AccountScopeID, logicalTaskID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, errors.New("delegated child lineage record missing")
	}
	if lineageRecord.AccountScopeID != parent.AccountScopeID || lineageRecord.LogicalTaskID != logicalTaskID {
		return 0, errors.New("delegated child lineage record scope or logical task disagrees")
	}
	if lineageRecord.CurrentSessionID != child.ID || lineageRecord.CurrentGeneration != genRecord.Generation || lineageRecord.CurrentGeneration <= 0 {
		return 0, errors.New("delegated child lineage has been superseded or is inconsistent")
	}

	ownerLease, found, err := genAuth.GetDelegatedWorktreeOwner(parent.AccountScopeID, child.WorktreeRootPath)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, errors.New("delegated worktree owner lease missing")
	}
	if ownerLease.AccountScopeID != parent.AccountScopeID || ownerLease.LogicalTaskID != logicalTaskID {
		return 0, errors.New("delegated worktree owner lease scope or logical task disagrees")
	}
	if filepath.Clean(ownerLease.WorkspacePath) != filepath.Clean(child.WorktreeRootPath) {
		return 0, errors.New("delegated worktree owner lease path disagrees")
	}
	if ownerLease.WorktreeBranch != child.WorktreeBranch {
		return 0, errors.New("delegated worktree owner lease branch disagrees")
	}
	if ownerLease.SessionID != child.ID || ownerLease.Generation != genRecord.Generation || ownerLease.Generation <= 0 {
		return 0, errors.New("delegated worktree owner lease is inconsistent")
	}

	return uint64(genRecord.Generation), nil
}

func (r *Runtime) resolveCommittedSourceDestination(scope WorkspaceScope, parent, child pebblestore.SessionSnapshot, selectedRow map[string]any, isProgram bool) (string, string, string, string, error) {
	laneDest, laneErr := r.manageWorktreeRecoveryDestination(scope, parent, child, selectedRow)
	if laneErr == nil {
		destKind := DestinationKindOwnedLane
		destPath := laneDest
		var destBranch, canonicalSource, recordedWorkspaceID, recordedWorkspaceGeneration string
		if parent.WorktreeEnabled && filepath.Clean(laneDest) == filepath.Clean(parent.WorktreeRootPath) {
			destBranch = parent.WorktreeBranch
			recordedWorkspaceID = asString(parent.Metadata["swarm_v3_source_workspace_id"])
			recordedWorkspaceGeneration = asString(parent.Metadata["swarm_v3_source_workspace_generation"])
			canonicalSource = strings.TrimSpace(asString(parent.Metadata["swarm_v3_source_workspace_path"]))
			if canonicalSource == "" {
				canonicalSource = strings.TrimSpace(parent.WorkspacePath)
			}
		} else {
			history, _ := parent.Metadata["swarm_v3_worktree_history"].([]any)
			for _, raw := range history {
				item, _ := raw.(map[string]any)
				if filepath.Clean(asString(item["path"])) == filepath.Clean(laneDest) {
					destBranch = strings.TrimSpace(asString(item["branch"]))
					canonicalSource = strings.TrimSpace(asString(item["source_workspace_path"]))
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
								destBranch = strings.TrimSpace(lane.Branch)
								canonicalSource = strings.TrimSpace(lane.SourcePath)
								recordedWorkspaceID = lane.WorkspaceID
								if lane.WorkspaceGeneration != 0 {
									recordedWorkspaceGeneration = strconv.FormatInt(lane.WorkspaceGeneration, 10)
								}
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

		if r.workspace == nil {
			return "", "", "", "", errors.New("workspace authority unavailable")
		}
		saved, scopeErr := r.workspace.ScopeForPathForPrincipal(scope.Principal, canonicalSource)
		if scopeErr != nil {
			return "", "", "", "", fmt.Errorf("authorize canonical source workspace: %w", scopeErr)
		}
		if !saved.Matched || saved.WorkspacePath != canonicalSource || saved.ResolvedPath != canonicalSource {
			return "", "", "", "", errors.New("canonical source is not an exact account-owned workspace")
		}
		if saved.WorkspaceID == "" {
			return "", "", "", "", errors.New("canonical source missing workspace ID in catalog")
		}
		// A secondary or retained lane has its own catalog identity. The primary
		// parent's source fields cannot authenticate a different repository.
		if recordedWorkspaceID != "" && recordedWorkspaceID != saved.WorkspaceID {
			return "", "", "", "", errors.New("owned lane source workspace ID disagrees with catalog")
		}
		if recordedWorkspaceGeneration != "" && recordedWorkspaceGeneration != strconv.FormatInt(saved.WorkspaceGeneration, 10) {
			return "", "", "", "", errors.New("owned lane source workspace generation disagrees with catalog")
		}
		if childSrcID := strings.TrimSpace(asString(child.Metadata["swarm_v3_source_workspace_id"])); childSrcID != "" && childSrcID != saved.WorkspaceID {
			if filepath.Clean(asString(child.Metadata["swarm_v3_source_workspace_path"])) == filepath.Clean(canonicalSource) {
				return "", "", "", "", errors.New("child source workspace ID disagrees with catalog")
			}
		}
		if bRaw, ok := child.Metadata["committed_source_binding"]; ok && bRaw != nil {
			binding, err := parseCommittedSourceBinding(bRaw)
			if err != nil {
				return "", "", "", "", fmt.Errorf("invalid committed source binding: %w", err)
			}
			if filepath.Clean(binding.CanonicalSourcePath) != filepath.Clean(canonicalSource) ||
				binding.DestinationKind != destKind ||
				filepath.Clean(binding.DestinationPath) != filepath.Clean(destPath) ||
				binding.DestinationBranch != destBranch ||
				(binding.CurrentWorkspaceID != "" && binding.CurrentWorkspaceID != saved.WorkspaceID) {
				return "", "", "", "", errors.New("child committed_source_binding disagrees with resolved owned destination")
			}
		}

		return destKind, destPath, destBranch, canonicalSource, nil
	}

	if isProgram {
		return "", "", "", "", fmt.Errorf("task program child destination resolution failed: %w", laneErr)
	}

	childTarget := strings.TrimSpace(asString(child.Metadata["target_workspace_path"]))
	if asString(child.Metadata["swarm_v3_source_workspace_path"]) != childTarget {
		return "", "", "", "", errors.New("captured destination disagrees with canonical source")
	}
	rowDest := strings.TrimSpace(asString(selectedRow["parent_workspace_path"]))
	if childTarget == "" || rowDest == "" || filepath.Clean(childTarget) != filepath.Clean(rowDest) {
		return "", "", "", "", fmt.Errorf("resolve destination: %w", laneErr)
	}
	if !filepath.IsAbs(childTarget) {
		return "", "", "", "", errors.New("target workspace path must be absolute")
	}

	if parent.WorktreeEnabled && filepath.Clean(childTarget) == filepath.Clean(parent.WorktreeRootPath) {
		return "", "", "", "", fmt.Errorf("owned parent lane destination failed: %w", laneErr)
	}
	history, _ := parent.Metadata["swarm_v3_worktree_history"].([]any)
	for _, raw := range history {
		item, _ := raw.(map[string]any)
		if filepath.Clean(asString(item["path"])) == filepath.Clean(childTarget) {
			return "", "", "", "", fmt.Errorf("owned history lane destination failed: %w", laneErr)
		}
	}
	if authority, ok := r.sessions.(interface {
		TaskProgramRepositoryLanes(string) ([]pebblestore.TaskProgramRepositoryLane, error)
	}); ok {
		lanes, _ := authority.TaskProgramRepositoryLanes(parent.ID)
		for _, lane := range lanes {
			if filepath.Clean(lane.WorkspacePath) == filepath.Clean(childTarget) {
				return "", "", "", "", fmt.Errorf("owned repository lane destination failed: %w", laneErr)
			}
		}
	}

	if r.workspace == nil {
		return "", "", "", "", errors.New("workspace authority unavailable")
	}
	saved, scopeErr := r.workspace.ScopeForPathForPrincipal(scope.Principal, childTarget)
	if scopeErr != nil {
		return "", "", "", "", fmt.Errorf("authorize recovery source workspace: %w", scopeErr)
	}
	if !saved.Matched || saved.WorkspacePath != childTarget || saved.ResolvedPath != childTarget {
		return "", "", "", "", errors.New("authorize recovery source: recorded source is not an exact canonical account-owned workspace")
	}
	if saved.WorkspaceID == "" {
		return "", "", "", "", errors.New("authorize recovery source: catalog workspace ID missing")
	}

	recordedBranch := strings.TrimSpace(firstNonEmptyString(asString(selectedRow["parent_branch"]), asString(child.Metadata["parent_branch"])))
	if recordedBranch == "" {
		return "", "", "", "", errors.New("captured destination requires recorded parent_branch")
	}

	targetState, iErr := r.worktrees.InspectTaskWorkspace(childTarget)
	if iErr != nil {
		return "", "", "", "", fmt.Errorf("inspect target workspace: %w", iErr)
	}
	if targetState.BranchName != recordedBranch {
		return "", "", "", "", fmt.Errorf("captured target live branch %q disagrees with recorded parent branch %q", targetState.BranchName, recordedBranch)
	}

	destKindRec := strings.TrimSpace(firstNonEmptyString(asString(child.Metadata["destination_kind"]), asString(selectedRow["destination_kind"])))
	if destKindRec != "" && destKindRec != DestinationKindCapturedPromotionOnly {
		return "", "", "", "", fmt.Errorf("recorded destination kind %q disagrees with captured destination", destKindRec)
	}

	if recID := strings.TrimSpace(asString(child.Metadata["swarm_v3_source_workspace_id"])); recID != "" && recID != saved.WorkspaceID {
		return "", "", "", "", errors.New("recorded workspace ID disagrees with current catalog")
	}
	if recGen := strings.TrimSpace(asString(child.Metadata["swarm_v3_source_workspace_generation"])); recGen != "" && saved.WorkspaceGeneration != 0 && recGen != strconv.FormatInt(saved.WorkspaceGeneration, 10) {
		return "", "", "", "", errors.New("recorded workspace generation disagrees with current catalog")
	}

	sourceRepoID, rErr := worktreeruntime.RepositoryIdentity(childTarget)
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

	if bRaw, ok := child.Metadata["committed_source_binding"]; ok && bRaw != nil {
		binding, err := parseCommittedSourceBinding(bRaw)
		if err != nil {
			return "", "", "", "", fmt.Errorf("invalid committed source binding: %w", err)
		}
		if filepath.Clean(binding.CanonicalSourcePath) != filepath.Clean(childTarget) ||
			binding.DestinationKind != DestinationKindCapturedPromotionOnly ||
			filepath.Clean(binding.DestinationPath) != filepath.Clean(childTarget) ||
			binding.DestinationBranch != recordedBranch ||
			(binding.CurrentWorkspaceID != "" && binding.CurrentWorkspaceID != saved.WorkspaceID) {
			return "", "", "", "", errors.New("child committed_source_binding disagrees with resolved captured destination")
		}
	}

	return DestinationKindCapturedPromotionOnly, childTarget, recordedBranch, childTarget, nil
}

func committedCorrectionMetadataPresent(metadata map[string]any) bool {
	for _, key := range []string{"integration_base_commit", "committed_source", "committed_source_binding"} {
		if _, exists := metadata[key]; exists {
			return true
		}
	}
	return false
}

// authenticateLineageDeliveryBase is the canonical strict helper for ResolveCommittedSource,
// manageWorktreeIntegrate, and manageWorktreePromote. It validates the full committed_source
// and committed_source_binding tuple, compares HEAD C == allocation base, verifies inherited B,
// cross-checks parent-owned durable prior source records, and recurses bounded depth to authenticate B.
func (r *Runtime) authenticateLineageDeliveryBase(
	parent pebblestore.SessionSnapshot,
	child pebblestore.SessionSnapshot,
	row map[string]any,
	childWorktreePath string,
	sourceBaseCommit string,
	headCommit string,
	depth int,
) (string, error) {
	if depth > 16 {
		return "", errors.New("exceeded maximum committed source correction depth")
	}

	hasMarker := committedCorrectionMetadataPresent(child.Metadata) || committedCorrectionMetadataPresent(row)

	if !hasMarker {
		if sourceBaseCommit == "" || !validCommitID(sourceBaseCommit) {
			return "", errors.New("invalid or missing base commit")
		}
		return sourceBaseCommit, nil
	}

	rawInteg := child.Metadata["integration_base_commit"]
	if rawInteg == nil {
		return "", errors.New("committed source correction requires explicit integration_base_commit in child metadata")
	}
	integBase := asString(rawInteg)
	if integBase == "" || !validCommitID(integBase) || asString(rawInteg) != strings.TrimSpace(integBase) {
		return "", errors.New("committed source correction requires a valid full hexadecimal integration_base_commit without whitespace")
	}

	rawCS := child.Metadata["committed_source"]
	if rawCS == nil {
		return "", errors.New("committed source correction requires full typed committed_source in child metadata")
	}
	csMap, ok := rawCS.(map[string]any)
	if !ok || csMap == nil {
		return "", errors.New("committed source correction requires full typed committed_source in child metadata")
	}
	csTaskCallID := asString(csMap["task_call_id"])
	csChildSessionID := asString(csMap["child_session_id"])
	csHeadCommit := asString(csMap["head_commit"])
	if csTaskCallID == "" || csChildSessionID == "" || csHeadCommit == "" || !validCommitID(csHeadCommit) {
		return "", errors.New("committed source correction requires complete valid committed_source tuple in child metadata")
	}
	if csTaskCallID != strings.TrimSpace(csTaskCallID) || csChildSessionID != strings.TrimSpace(csChildSessionID) || csHeadCommit != strings.TrimSpace(csHeadCommit) {
		return "", errors.New("committed_source tuple fields must not contain surrounding whitespace")
	}

	rawBinding := child.Metadata["committed_source_binding"]
	if rawBinding == nil {
		return "", errors.New("committed source correction requires full typed committed_source_binding in child metadata")
	}
	binding, bErr := parseCommittedSourceBinding(rawBinding)
	if bErr != nil {
		return "", fmt.Errorf("parse committed source binding: %w", bErr)
	}

	if binding.TaskCallID != csTaskCallID || binding.ChildSessionID != csChildSessionID || binding.HeadCommit != csHeadCommit {
		return "", errors.New("committed_source_binding tuple disagrees with committed_source")
	}
	if binding.SourceHeadCommit != csHeadCommit {
		return "", errors.New("committed_source_binding source_head_commit disagrees with head_commit")
	}
	if binding.IntegrationBaseCommit != integBase {
		return "", errors.New("committed_source_binding integration_base_commit disagrees with child metadata")
	}
	if binding.DestinationKind == "" || binding.DestinationPath == "" || binding.DestinationBranch == "" || binding.CanonicalSourcePath == "" || binding.RepositoryIdentity == "" {
		return "", errors.New("committed_source_binding has missing required fields")
	}

	if row == nil {
		return "", errors.New("correction delivery requires exact parent launch membership")
	}
	if row != nil {
		rowInteg := asString(row["integration_base_commit"])
		if rowInteg == "" || rowInteg != integBase {
			return "", errors.New("parent row integration_base_commit missing or disagrees with child metadata")
		}
		rowCS, ok := row["committed_source"].(map[string]any)
		if !ok || rowCS == nil {
			return "", errors.New("parent row committed_source missing or invalid")
		}
		if asString(rowCS["task_call_id"]) != csTaskCallID ||
			asString(rowCS["child_session_id"]) != csChildSessionID ||
			asString(rowCS["head_commit"]) != csHeadCommit {
			return "", errors.New("parent row committed_source tuple disagrees with child metadata")
		}
		rowBindingRaw := row["committed_source_binding"]
		if rowBindingRaw == nil {
			return "", errors.New("parent row committed_source_binding missing")
		}
		rowBinding, rbErr := parseCommittedSourceBinding(rowBindingRaw)
		if rbErr != nil || rowBinding != binding {
			return "", errors.New("parent row committed_source_binding disagrees with child metadata")
		}
	}

	if csHeadCommit != sourceBaseCommit {
		return "", fmt.Errorf("committed source HEAD C (%s) does not match child allocation base (%s)", csHeadCommit, sourceBaseCommit)
	}
	if childWorktreePath != "" {
		descends, err := r.worktrees.TaskCommitDescendsFrom(childWorktreePath, integBase, sourceBaseCommit)
		if err != nil || !descends {
			return "", fmt.Errorf("invalid committed source: allocation base %s does not descend from delivery base %s", sourceBaseCommit, integBase)
		}
		if headCommit != "" && headCommit != sourceBaseCommit {
			hDescends, err := r.worktrees.TaskCommitDescendsFrom(childWorktreePath, sourceBaseCommit, headCommit)
			if err != nil || !hDescends {
				return "", fmt.Errorf("invalid committed source: child head %s does not descend from allocation base %s", headCommit, sourceBaseCommit)
			}
		}
	}

	priorChild, found, err := r.sessions.GetSession(csChildSessionID)
	if err != nil || !found {
		return "", fmt.Errorf("prior committed source child session %q not found", csChildSessionID)
	}
	if priorChild.AccountScopeID != parent.AccountScopeID || priorChild.UserID != parent.UserID {
		return "", errors.New("prior committed source child belongs to different principal")
	}
	if asString(priorChild.Metadata["parent_session_id"]) != parent.ID {
		return "", errors.New("prior committed source child does not belong to parent")
	}
	priorRow, priorProgram, priorBase, err := r.authenticateCommittedSourceLineage(parent, priorChild, CommittedSourceRequest{TaskCallID: csTaskCallID, ChildSessionID: csChildSessionID, HeadCommit: csHeadCommit})
	if err != nil {
		return "", fmt.Errorf("authenticate prior committed handoff: %w", err)
	}
	if binding.SourceBaseCommit != priorBase || binding.SourceWorktreePath != priorChild.WorktreeRootPath || binding.SourceBranch != priorChild.WorktreeBranch {
		return "", errors.New("prior source identity disagrees with binding")
	}
	ownerScope := WorkspaceScope{SessionID: parent.ID, Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, SessionID: parent.ID}}
	kind, destination, branch, canonical, err := r.resolveCommittedSourceDestination(ownerScope, parent, priorChild, priorRow, priorProgram)
	if err != nil {
		return "", err
	}
	if kind != binding.DestinationKind || destination != binding.DestinationPath || branch != binding.DestinationBranch || canonical != binding.CanonicalSourcePath || asString(child.Metadata["target_workspace_path"]) != destination || child.WorktreeBaseBranch != branch {
		return "", errors.New("inherited delivery destination disagrees with authenticated source")
	}
	genAuth, ok := r.sessions.(interface {
		GetDelegatedChildGenerationBySession(accountScopeID, sessionID string) (pebblestore.DelegatedChildGenerationRecord, bool, error)
	})
	if !ok {
		return "", errors.New("prior generation authority unavailable")
	}
	{
		priorGen, found, gErr := genAuth.GetDelegatedChildGenerationBySession(parent.AccountScopeID, csChildSessionID)
		if gErr != nil || !found {
			return "", errors.New("prior committed source child generation record missing")
		}
		if priorGen.SessionID != priorChild.ID || priorGen.AccountScopeID != parent.AccountScopeID || priorGen.ParentSessionID != parent.ID || priorGen.ImmutableBaseCommit != priorBase || priorGen.WorkspacePath != binding.SourceWorktreePath || priorGen.WorktreeBranch != binding.SourceBranch || uint64(priorGen.Generation) != binding.ChildGeneration || priorGen.Generation <= 0 {
			return "", errors.New("prior committed source child generation disagrees with binding")
		}
	}
	if childWorktreePath != "" {
		childRepoID, err := worktreeruntime.RepositoryIdentity(childWorktreePath)
		if err != nil || childRepoID != binding.RepositoryIdentity {
			return "", errors.New("child repository identity disagrees with binding")
		}
	}
	priorRecordedHead := asString(priorRow["head_commit"])
	if priorRecordedHead != csHeadCommit {
		return "", errors.New("prior committed source recorded HEAD disagrees with committed_source tuple")
	}

	priorHasMarker := committedCorrectionMetadataPresent(priorChild.Metadata)
	if !priorHasMarker {
		priorBase := strings.TrimSpace(asString(priorChild.Metadata["base_commit"]))
		if priorBase == "" || priorBase != integBase {
			return "", fmt.Errorf("inherited delivery base B (%s) widened beyond prior child base (%s)", integBase, priorBase)
		}
		return integBase, nil
	}

	priorIntegBase := strings.TrimSpace(asString(priorChild.Metadata["integration_base_commit"]))
	if priorIntegBase == "" || priorIntegBase != integBase {
		return "", fmt.Errorf("inherited delivery base B (%s) widened or altered from prior child delivery base (%s)", integBase, priorIntegBase)
	}

	// Verify immutable ancestor objects in the current delivery tree, never read
	// a prior producer's mutable checkout merely to deliver the authenticated stack.
	return r.authenticateLineageDeliveryBase(parent, priorChild, priorRow, childWorktreePath, priorBase, csHeadCommit, depth+1)
}
