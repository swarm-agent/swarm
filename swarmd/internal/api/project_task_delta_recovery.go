package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// projectTaskDeltaRecovery is the exact selection a task delta recovery is
// pinned to. The /recover-integrate route takes revision, attempt and heads from
// its request; IntegrateProjectTask takes them from the task and its fresh
// assessment. AgentGuidance selects the conflict wording for integrate callers
// (MCP and native tools), who resolve conflicts through the task's agent.
type projectTaskDeltaRecovery struct {
	Task          *pebblestore.ProjectTaskRecord
	Session       pebblestore.SessionSnapshot
	Assessment    *pebblestore.TaskDeliveryAssessment
	Revision      int
	AttemptID     string
	SourceHead    string
	TargetHead    string
	AgentGuidance bool
}

// recoverTaskDelta is the /recover-integrate HTTP adapter over
// recoverProjectTaskDelta.
func (s *Server) recoverTaskDelta(w http.ResponseWriter, r *http.Request, p identity.Principal, task *pebblestore.ProjectTaskRecord, session pebblestore.SessionSnapshot, a *pebblestore.TaskDeliveryAssessment, revision int, attempt, source, target string) {
	result, _, err := s.recoverProjectTaskDelta(r.Context(), p, projectTaskDeltaRecovery{Task: task, Session: session, Assessment: a, Revision: revision, AttemptID: attempt, SourceHead: source, TargetHead: target})
	if err != nil {
		writeProjectTaskIntegrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// recoverProjectTaskDelta squashes the task's recorded base..source net change
// onto the current target and promotes it. Called only after the integrate
// route's (or IntegrateProjectTask's) account/catalog/selected-lane
// authorization. No request field can choose a repository, base or repair ref.
// It returns the response body and the terminal receipt.
func (s *Server) recoverProjectTaskDelta(ctx context.Context, p identity.Principal, req projectTaskDeltaRecovery) (map[string]any, *pebblestore.ProjectTaskIntegration, error) {
	task, session, a := req.Task, req.Session, req.Assessment
	revision, attempt, source, target := req.Revision, req.AttemptID, req.SourceHead, req.TargetHead
	conflictErr := func(err error) error { return integrationFailure(http.StatusConflict, err) }
	// Serialize recovery with the existing task follow-up allocation boundary.
	// A retained in-progress receipt may be resumed after restart, but never by
	// two live requests at once. Fail fast rather than queue duplicate Git work.
	if !s.projectTaskCreateMu.TryLock() {
		return nil, nil, conflictErr(errors.New("task recovery or follow-up is active; retry after its result"))
	}
	defer s.projectTaskCreateMu.Unlock()
	db := s.sessions.Store()
	current, found, readErr := db.GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
	if readErr != nil || !found || current.Revision != task.Revision || current.SessionID != task.SessionID || current.ActiveAttemptID != task.ActiveAttemptID {
		return nil, nil, conflictErr(errors.New("task changed before recovery; refresh task"))
	}
	if a == nil || a.Freshness != "observed" || attempt != task.ActiveAttemptID || source != a.SourceOID || task.Archived {
		return nil, nil, conflictErr(errors.New("recovery attempt/source unavailable or changed; refresh task"))
	}
	if task.Status != "completed" && task.Status != "needs_review" && task.Status != "failed" && task.Status != "blocked" {
		return nil, nil, conflictErr(errors.New("stop task execution before recovering its committed delta"))
	}
	active, activeFound, activeErr := db.GetV3SessionActiveRunIntent(session.ID)
	if activeErr != nil || (activeFound && (active.Status == pebblestore.V3RunIntentRunning || active.Status == pebblestore.V3RunIntentPendingExecutor)) {
		return nil, nil, conflictErr(errors.New("task session still has an active run; stop it before recovery"))
	}
	// Successful exact evidence is retryable even when the response was lost.
	if (a.State == "recovered" || a.State == "equivalent") && task.Integration != nil {
		copy := *task
		receipt := *task.Integration
		if task.Integration.State == "in_progress" {
			receipt.State, receipt.ResultingTargetHead = "recovered", a.TargetOID
			updated, err := pebblestore.FinishProjectTaskIntegration(db, p.AccountScopeID, task, &receipt)
			if err != nil {
				return nil, nil, conflictErr(err)
			}
			copy = *updated
			a.TaskRevision = updated.Revision
		}
		copy.DeliveryAssessment = a
		return map[string]any{"status": a.State, "task": sanitizeProjectTaskForClient(&copy)}, &receipt, nil
	}
	if revision != task.Revision || target != a.TargetOID || source == "" || target == "" {
		return nil, nil, conflictErr(errors.New("recovery revision or target HEAD changed; refresh task"))
	}
	if a.State != "history_rewritten" && a.State != "history_equivalent" && a.State != "candidate_work" {
		return nil, nil, conflictErr(fmt.Errorf("task delta cannot be recovered: %s", a.Reason))
	}
	in := worktree.TaskDeliveryInput{Identity: *a, SourcePath: session.WorktreeRootPath, TargetPath: task.SourceWorkspace.Path}
	origin := &pebblestore.ProjectTaskRecoverySource{SessionID: session.ID, WorkspacePath: session.WorktreeRootPath, Branch: a.SourceBranch, BaseCommit: a.BaseOID, HeadCommit: a.SourceOID, TargetBranch: a.TargetBranch, TargetHead: a.TargetOID}
	if err := s.validateProjectTaskRecovery(p, task, origin); err != nil {
		return nil, nil, conflictErr(err)
	}
	service := &worktree.Service{}
	receipt := &pebblestore.ProjectTaskIntegration{SessionID: session.ID, SourceBranch: a.SourceBranch, SourceHead: a.SourceOID, TargetBranch: a.TargetBranch, TargetWorkspacePath: in.TargetPath, PreviousTargetHead: a.TargetOID, RecoveryBase: a.BaseOID}
	preparedRevision := revision + 1
	if previous := task.Integration; previous != nil && previous.State == "in_progress" {
		if previous.SessionID != session.ID || previous.AttemptID != attempt || previous.SourceHead != source || previous.RecoveryBase != a.BaseOID || previous.PreviousTargetHead != target {
			return nil, nil, conflictErr(errors.New("retained recovery operation has different provenance; review its source and target"))
		}
		copy := *previous
		receipt = &copy
		preparedRevision = revision
	} else if err := pebblestore.BeginProjectTaskIntegration(db, p.AccountScopeID, task, receipt); err != nil {
		return nil, nil, conflictErr(err)
	}
	fail := func(err error, delivery any) error {
		receipt.State, receipt.Error = "conflict", err.Error()
		if _, persistErr := pebblestore.FinishProjectTaskIntegration(db, p.AccountScopeID, task, receipt); persistErr != nil {
			err = fmt.Errorf("%w; retain operation %s for reconciliation: %v", err, receipt.OperationID, persistErr)
		}
		return &projectTaskIntegrationError{status: http.StatusConflict, err: err, delivery: delivery}
	}
	prepared, err := service.PrepareTaskDeltaRecovery(ctx, in)
	if err != nil {
		return nil, nil, fail(err, nil)
	}
	receipt.RecoveredHead, receipt.RecoveryRef = prepared.PreparedHead, prepared.RetainedRef
	// Durable provenance precedes target mutation and survives failed response
	// delivery. A conflict retains a target-parented repair commit, not old history.
	pinned, err := db.UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(current *pebblestore.ProjectTaskRecord) error {
		if current.Revision != preparedRevision {
			return errors.New("task revision changed during recovery preparation; refresh task")
		}
		if err := pebblestore.CheckProjectTaskIntegration(current, receipt); err != nil {
			return err
		}
		copy := *receipt
		current.Integration = &copy
		current.Revision++
		return nil
	})
	if err != nil {
		return nil, nil, fail(err, nil)
	}
	if prepared.Conflict != "" {
		if !req.AgentGuidance {
			return nil, nil, fail(errors.New(prepared.Conflict), nil)
		}
		message := taskDeltaConflictMessage(prepared.ConflictFiles, a.SourceBranch, a.TargetBranch)
		conflict := *a
		conflict.State, conflict.ReasonCode, conflict.Reason = "conflict", "merge_conflict", message
		conflict.Files = prepared.ConflictFiles
		return nil, nil, fail(errors.New(message), &conflict)
	}
	var plan worktree.TaskIntegrationPlan
	if !prepared.Equivalent {
		plan, err = service.PrepareTaskIntegration(in.TargetPath, a.TargetBranch, a.TargetOID, []worktree.TaskIntegrationChild{{SessionID: session.ID, BaseCommit: a.TargetOID, HeadCommit: prepared.PreparedHead, PreserveAncestry: true}})
		if err != nil {
			return nil, nil, fail(err, nil)
		}
	}
	if err := s.validateProjectTaskRecovery(p, task, origin); err != nil {
		return nil, nil, fail(err, nil)
	}
	updated, err := pebblestore.FinishProjectTaskIntegrationGuarded(db, p.AccountScopeID, task, receipt, pinned.Revision, func() error {
		// Do not call store APIs inside this mutation guard. The authenticated
		// source/target Git tuple is rechecked independently immediately before apply.
		if err := service.CheckTaskDeltaRecovery(ctx, in); err != nil {
			return err
		}
		if prepared.Equivalent {
			receipt.State, receipt.ResultingTargetHead = "equivalent", a.TargetOID
			return nil
		}
		result, err := service.ApplyTaskIntegration(in.TargetPath, plan)
		if err != nil {
			return err
		}
		receipt.ResultingTargetHead = result.ResultingParentHead
		verified, err := service.TaskCommitDescendsFrom(in.TargetPath, prepared.PreparedHead, result.ResultingParentHead)
		if err != nil || !verified {
			return errors.New("recovered commit ancestry could not be verified; inspect retained recovery receipt")
		}
		receipt.State = "recovered"
		return nil
	})
	if err != nil {
		return nil, nil, fail(err, nil)
	}
	// Reuse canonical ephemeral mapping; no read-side writes or polling.
	_ = reconcileTaskGitStateContext(ctx, db, updated)
	return map[string]any{"status": receipt.State, "task": sanitizeProjectTaskForClient(updated)}, receipt, nil
}

// taskDeltaConflictMessage is the refusal an integrate caller (often an AI
// through MCP) can act on: which files conflict and how to get the task's
// agent to resolve them.
func taskDeltaConflictMessage(files []string, sourceBranch, targetBranch string) string {
	where := "files merge-tree could not list"
	if len(files) > 0 {
		where = strings.Join(files, ", ")
	}
	return fmt.Sprintf("cannot integrate: the task's net change conflicts with %s in %s. Nothing was changed on %s. Send the task's agent a follow-up (swarm_send_message) asking it to rebase its branch %s onto %s (rebase, not merge) and resolve the conflict in %s, then integrate again.", targetBranch, where, targetBranch, sourceBranch, targetBranch, where)
}

// taskDeltaIntegratedNote says, in words an AI can relay, what integrating a
// recovered task delta did to the target branch.
func taskDeltaIntegratedNote(receipt *pebblestore.ProjectTaskIntegration) string {
	if receipt == nil {
		return ""
	}
	if receipt.State == "equivalent" {
		return fmt.Sprintf("The task's net change was already on %s, so no new commit was made; the task branch's own commit history (including any merge of %s into it) is not kept on %s.", receipt.TargetBranch, receipt.TargetBranch, receipt.TargetBranch)
	}
	return fmt.Sprintf("The task's net change was integrated into %s as one new commit %s; the task branch's own commit history (including its merge of %s into it) is not kept on %s.", receipt.TargetBranch, receipt.RecoveredHead, receipt.TargetBranch, receipt.TargetBranch)
}
