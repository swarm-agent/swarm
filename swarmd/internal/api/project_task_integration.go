package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// projectTaskIntegrationError preserves the HTTP status and delivery payload.
type projectTaskIntegrationError struct {
	status   int
	err      error
	delivery any
}

func (e *projectTaskIntegrationError) Error() string { return e.err.Error() }
func integrationFailure(status int, err error) error {
	return &projectTaskIntegrationError{status: status, err: err}
}
func writeProjectTaskIntegrationError(w http.ResponseWriter, err error) {
	var failure *projectTaskIntegrationError
	if errors.As(err, &failure) {
		if failure.delivery != nil {
			writeJSON(w, failure.status, map[string]any{"ok": false, "error": failure.Error(), "code": strconv.Itoa(failure.status), "delivery": failure.delivery})
			return
		}
		writeError(w, failure.status, failure.err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

var inspectIntegrationGitState = inspectTaskGitStateContext

// IntegrateProjectTask is the shared native-tool and HTTP integration boundary.
// Native callers select the current durable lane; HTTP callers supply their selection.
func (s *Server) IntegrateProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string, selections ...tool.ProjectTaskIntegrationSelection) (map[string]any, error) {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || p.UserID == "" || p.AccountScopeID == "" {
		return nil, integrationFailure(http.StatusForbidden, errors.New("integrate task requires an authenticated user identity"))
	}
	db := s.sessions.Store()
	if db == nil {
		return nil, errors.New("database not available")
	}
	task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !found || task == nil {
		return nil, integrationFailure(http.StatusNotFound, errors.New("task not found"))
	}
	if task.Agent == pebblestore.WorkerRoleReviewer {
		return nil, integrationFailure(http.StatusConflict, errors.New("reviewer jobs are report-only; delivery and integration are refused"))
	}
	selection := tool.ProjectTaskIntegrationSelection{SessionID: task.SessionID, SourceBranch: task.WorktreeBranch, TargetBranch: task.BaseBranch}
	if len(selections) > 0 {
		selection = selections[0]
	}
	if selection.SessionID == "" || selection.SessionID != task.SessionID || selection.SourceBranch == "" || selection.TargetBranch == "" {
		return nil, integrationFailure(http.StatusConflict, errors.New("task selection changed or captured source/target branch is missing; refresh the task"))
	}
	selectedSession, sessionFound, sessionErr := db.GetSession(selection.SessionID)
	if sessionErr != nil || !sessionFound || selectedSession.AccountScopeID != p.AccountScopeID || selectedSession.UserID != p.UserID || !selectedSession.WorktreeEnabled || strings.TrimSpace(selectedSession.WorktreeRootPath) == "" || strings.TrimSpace(selectedSession.WorktreeBranch) != selection.SourceBranch || strings.TrimSpace(selectedSession.WorktreeBaseBranch) != selection.TargetBranch || (task.WorktreeBranch != "" && task.WorktreeBranch != selection.SourceBranch) || (task.BaseBranch != "" && task.BaseBranch != selection.TargetBranch) {
		return nil, integrationFailure(http.StatusConflict, errors.New("selected task is not bound to an owned worktree and captured target branch; refresh its lineage"))
	}
	capturedPath, _ := selectedSession.Metadata["swarm_v3_source_workspace_path"].(string)
	capturedBase, _ := selectedSession.Metadata["base_commit"].(string)
	if strings.TrimSpace(capturedPath) == "" || strings.TrimSpace(capturedBase) == "" || (task.SourceWorkspace.Path != "" && task.SourceWorkspace.Path != capturedPath) || (task.BaseCommit != "" && task.BaseCommit != capturedBase) {
		return nil, integrationFailure(http.StatusConflict, errors.New("captured source repository or fork commit is missing or differs from the task; refresh its lineage"))
	}
	proj, projectFound, projectErr := db.GetProject(p.AccountScopeID, projectID)
	if projectErr != nil || !projectFound || proj == nil {
		return nil, integrationFailure(http.StatusConflict, errors.New("project source catalog is unavailable"))
	}
	if _, err := s.resolveProjectTaskSource(p, proj, task.SourceWorkspace.Path, task.SourceWorkspace.WorkspaceID, task.SourceWorkspace.WorkspaceGeneration, true); err != nil {
		return nil, integrationFailure(http.StatusConflict, err)
	}
	ctx = worktreeruntime.WithDeliveryExecution(ctx)
	gitState := inspectIntegrationGitState(ctx, *task, db)
	warning := ""
	if a := gitState.deliveryAssessment; a != nil && a.ReasonCode == "lane_branch_mismatch" {
		// Only the mutation boundary repairs a validated, exclusively owned lane;
		// ordinary delivery assessments remain read-only.
		warning, err = worktreeruntime.ReattachDeliveryLane(ctx, selectedSession.WorktreeRootPath, selection.SourceBranch, capturedPath, selection.TargetBranch)
		if err != nil {
			return nil, &projectTaskIntegrationError{status: http.StatusConflict, err: fmt.Errorf("cannot reattach delivery lane (%s): %w", a.ReasonCode, err), delivery: a}
		}
		if warning != "" {
			gitState = inspectIntegrationGitState(ctx, *task, db)
		}
	}
	if gitState.deliveryAssessment == nil || gitState.deliveryAssessment.Freshness != "observed" || (gitState.deliveryAssessment.State != "candidate_work" && gitState.deliveryAssessment.State != "integrated") {
		return nil, &projectTaskIntegrationError{status: http.StatusConflict, err: errors.New(taskDeliveryNotActionableMessage(projectID, taskID, gitState.deliveryAssessment)), delivery: gitState.deliveryAssessment}
	}
	if a := gitState.deliveryAssessment; gitState.isDirty || a.SourceDirty || a.TargetDirty {
		return nil, &projectTaskIntegrationError{status: http.StatusConflict, err: errors.New("cannot integrate: " + taskDeliveryDirtyMessage(a)), delivery: a}
	}
	receipt := &pebblestore.ProjectTaskIntegration{State: "in_progress", SessionID: selection.SessionID, SourceBranch: selection.SourceBranch, TargetBranch: selection.TargetBranch, TargetWorkspacePath: capturedPath, Warning: warning}
	if err := pebblestore.BeginProjectTaskIntegration(db, p.AccountScopeID, task, receipt); err != nil {
		return nil, err
	}
	defer func() {
		if receipt.State == "in_progress" {
			receipt.State = "conflict"
			if receipt.Error == "" {
				receipt.Error = "Integration did not complete. Inspect the source and target Git state before retrying."
			}
			if _, err := pebblestore.FinishProjectTaskIntegration(db, p.AccountScopeID, task, receipt); err != nil {
				log.Printf("persist terminal integration receipt failed: %v", err)
			}
		}
	}()
	if gitState.unintegratedCommits == 0 {
		if gitState.isIntegrated {
			checkout, checkoutErr := s.worktrees.InspectTaskWorkspace(capturedPath)
			sourceState, sourceErr := s.worktrees.InspectTaskWorkspace(selectedSession.WorktreeRootPath)
			if checkoutErr != nil || sourceErr != nil || checkout.BranchName != selection.TargetBranch || sourceState.BranchName != selection.SourceBranch || checkout.HeadCommit != gitState.deliveryAssessment.TargetOID || sourceState.HeadCommit != gitState.deliveryAssessment.SourceOID || !checkout.TrackedClean || !sourceState.Clean || sourceState.HeadCommit == capturedBase {
				return nil, integrationFailure(http.StatusConflict, errors.New("captured target or committed source is unavailable; inspect Git before retrying"))
			}
			receipt.SourceHead = sourceState.HeadCommit
			receipt.PreviousTargetHead = checkout.HeadCommit
			ancestorCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if err := sandbox.Command(ancestorCtx, "-C", capturedPath, "merge-base", "--is-ancestor", sourceState.HeadCommit, checkout.HeadCommit).Run(); err != nil {
				receipt.Error = "Integration ancestry changed during verification; inspect the retained source and target before launching repair."
				return nil, integrationFailure(http.StatusConflict, errors.New(receipt.Error))
			}
			receipt.ResultingTargetHead = checkout.HeadCommit
			receipt.State = "already_integrated"
			updated, err := pebblestore.FinishProjectTaskIntegration(db, p.AccountScopeID, task, receipt)
			if err != nil {
				return nil, fmt.Errorf("Git integration verified at %s but task receipt persistence failed; reconcile task state without rerunning Git: %w", receipt.ResultingTargetHead, err)
			}
			return map[string]any{"status": "already_integrated", "task": sanitizeProjectTaskForClient(updated)}, nil
		}
		return nil, integrationFailure(http.StatusBadRequest, errors.New("cannot integrate: worktree has no commits to integrate"))
	}
	targetPath := strings.TrimSpace(task.WorkspacePath)
	var sess *pebblestore.SessionSnapshot
	if task.SessionID != "" {
		if snap, found, _ := db.GetSession(task.SessionID); found {
			sess = &snap
			if sess.WorktreeEnabled && strings.TrimSpace(sess.WorktreeRootPath) != "" {
				targetPath = strings.TrimSpace(sess.WorktreeRootPath)
			}
		}
	}
	parentWs := ""
	if sess != nil && sess.Metadata != nil {
		if src, ok := sess.Metadata["swarm_v3_source_workspace_path"].(string); ok && src != "" {
			parentWs = src
		}
	}
	if parentWs != capturedPath || targetPath != selectedSession.WorktreeRootPath || parentWs == targetPath {
		return nil, integrationFailure(http.StatusConflict, errors.New("task worktree does not match the captured repository and session lane"))
	}
	type workspaceInspector interface {
		InspectTaskWorkspace(string) (worktreeruntime.TaskWorkspaceState, error)
	}
	var inspector workspaceInspector = &worktreeruntime.Service{}
	if s.worktrees != nil {
		inspector = s.worktrees
	}
	parentState, err := inspector.InspectTaskWorkspace(parentWs)
	if err != nil {
		return nil, integrationFailure(http.StatusBadRequest, fmt.Errorf("inspect parent repository %q: %w", parentWs, err))
	}
	// PrepareTaskIntegration rejects untracked files only when they would be overwritten.
	if !parentState.TrackedClean {
		return nil, integrationFailure(http.StatusConflict, fmt.Errorf("captured repository on %s has uncommitted changes to tracked files:\n%s\ncommit or stash them before integrating", parentState.BranchName, parentState.Status))
	}
	if parentState.BranchName != selection.TargetBranch {
		return nil, integrationFailure(http.StatusConflict, fmt.Errorf("captured checkout is on %s, not selected target %s; check out the target and retry", parentState.BranchName, selection.TargetBranch))
	}
	childState, err := inspector.InspectTaskWorkspace(targetPath)
	if err != nil {
		return nil, integrationFailure(http.StatusBadRequest, fmt.Errorf("inspect worktree %q: %w", targetPath, err))
	}
	if childState.HeadCommit != gitState.deliveryAssessment.SourceOID || parentState.HeadCommit != gitState.deliveryAssessment.TargetOID {
		return nil, integrationFailure(http.StatusConflict, errors.New("delivery heads moved; refresh before integration"))
	}
	if !childState.Clean {
		return nil, integrationFailure(http.StatusConflict, errors.New("source worktree is dirty; explicitly commit changes before integrating"))
	}
	if childState.BranchName != selection.SourceBranch {
		return nil, integrationFailure(http.StatusConflict, errors.New("source worktree branch changed; refresh the selected task"))
	}
	if capturedBase == childState.HeadCommit {
		return nil, integrationFailure(http.StatusBadRequest, errors.New("cannot integrate: worktree has no commits beyond its base commit"))
	}
	type taskIntegrator interface {
		PrepareTaskIntegration(string, string, string, []worktreeruntime.TaskIntegrationChild) (worktreeruntime.TaskIntegrationPlan, error)
		ApplyTaskIntegration(string, worktreeruntime.TaskIntegrationPlan) (worktreeruntime.TaskIntegrationResult, error)
	}
	var integrator taskIntegrator = &worktreeruntime.Service{}
	if ti, ok := s.worktrees.(taskIntegrator); ok {
		integrator = ti
	}
	receipt.SourceHead = childState.HeadCommit
	receipt.PreviousTargetHead = parentState.HeadCommit
	if _, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		if err := pebblestore.CheckProjectTaskIntegration(t, receipt); err != nil {
			return err
		}
		copy := *receipt
		t.Integration = &copy
		t.Revision++
		return nil
	}); err != nil {
		return nil, integrationFailure(http.StatusConflict, err)
	}
	plan, err := integrator.PrepareTaskIntegration(parentWs, parentState.BranchName, parentState.HeadCommit, []worktreeruntime.TaskIntegrationChild{{SessionID: selection.SessionID, BaseCommit: capturedBase, HeadCommit: childState.HeadCommit, PreserveAncestry: true}})
	if err != nil {
		receipt.Error = fmt.Sprintf("prepare integration failed: %v", err)
		var conflict *worktreeruntime.TaskIntegrationConflictError
		if errors.As(err, &conflict) {
			a := *gitState.deliveryAssessment
			a.State, a.ReasonCode, a.Reason = "conflict", "merge_conflict", receipt.Error
			return nil, &projectTaskIntegrationError{status: http.StatusConflict, err: errors.New(receipt.Error), delivery: &a}
		}
		return nil, integrationFailure(http.StatusBadRequest, errors.New(receipt.Error))
	}
	if len(plan.Commits) == 0 && plan.FastForwardHead == "" {
		receipt.Error = "Integration requires original commit ancestry: equivalent patches exist on the captured target, but the source history is unmerged. Launch repair to reconcile history in a new isolated worktree, then explicitly integrate."
		return nil, integrationFailure(http.StatusConflict, errors.New(receipt.Error))
	}
	result, err := integrator.ApplyTaskIntegration(parentWs, plan)
	if err != nil {
		receipt.Error = fmt.Sprintf("apply integration failed: %v", err)
		return nil, errors.New(receipt.Error)
	}
	receipt.ResultingTargetHead = result.ResultingParentHead
	verified, verifyErr := inspector.InspectTaskWorkspace(parentWs)
	if verifyErr != nil || verified.BranchName != selection.TargetBranch || verified.HeadCommit != result.ResultingParentHead {
		receipt.Error = "target changed after promotion; inspect Git before retrying"
		return nil, integrationFailure(http.StatusConflict, errors.New(receipt.Error))
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := sandbox.Command(verifyCtx, "-C", parentWs, "merge-base", "--is-ancestor", childState.HeadCommit, verified.HeadCommit).Run(); err != nil {
		receipt.Error = "source commit ancestry on target could not be verified; inspect Git before retrying"
		return nil, integrationFailure(http.StatusConflict, errors.New(receipt.Error))
	}
	receipt.State = "integrated"
	updated, err := pebblestore.FinishProjectTaskIntegration(db, p.AccountScopeID, task, receipt)
	if err != nil {
		return nil, fmt.Errorf("Git integration verified at %s but task receipt persistence failed; reconcile task state without rerunning Git: %w", receipt.ResultingTargetHead, err)
	}
	return map[string]any{"status": "integrated", "task": sanitizeProjectTaskForClient(updated), "integration": map[string]any{"target_branch": parentState.BranchName, "previous_target_head": parentState.HeadCommit, "resulting_target_head": result.ResultingParentHead, "warning": receipt.Warning}}, nil
}
