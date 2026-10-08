package api

import (
	"errors"
	"fmt"
	"net/http"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// Called only after the ordinary integrate route's account/catalog/selected-lane
// authorization. No request field can choose a repository, base or repair ref.
func (s *Server) recoverTaskDelta(w http.ResponseWriter, r *http.Request, p identity.Principal, task *pebblestore.ProjectTaskRecord, session pebblestore.SessionSnapshot, a *pebblestore.TaskDeliveryAssessment, revision int, attempt, source, target string) {
	// Serialize recovery with the existing task follow-up allocation boundary.
	// A retained in-progress receipt may be resumed after restart, but never by
	// two live requests at once. Fail fast rather than queue duplicate Git work.
	if !s.projectTaskCreateMu.TryLock() {
		writeError(w, http.StatusConflict, errors.New("task recovery or follow-up is active; retry after its result"))
		return
	}
	defer s.projectTaskCreateMu.Unlock()
	db := s.sessions.Store()
	current, found, readErr := db.GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
	if readErr != nil || !found || current.Revision != task.Revision || current.SessionID != task.SessionID || current.ActiveAttemptID != task.ActiveAttemptID {
		writeError(w, http.StatusConflict, errors.New("task changed before recovery; refresh task"))
		return
	}
	if a == nil || a.Freshness != "observed" || attempt != task.ActiveAttemptID || source != a.SourceOID || task.Archived {
		writeError(w, http.StatusConflict, errors.New("recovery attempt/source unavailable or changed; refresh task"))
		return
	}
	if task.Status != "completed" && task.Status != "needs_review" && task.Status != "failed" && task.Status != "blocked" {
		writeError(w, http.StatusConflict, errors.New("stop task execution before recovering its committed delta"))
		return
	}
	active, activeFound, activeErr := db.GetV3SessionActiveRunIntent(session.ID)
	if activeErr != nil || (activeFound && (active.Status == pebblestore.V3RunIntentRunning || active.Status == pebblestore.V3RunIntentPendingExecutor)) {
		writeError(w, http.StatusConflict, errors.New("task session still has an active run; stop it before recovery"))
		return
	}
	// Successful exact evidence is retryable even when the response was lost.
	if (a.State == "recovered" || a.State == "equivalent") && task.Integration != nil {
		copy := *task
		if task.Integration.State == "in_progress" {
			receipt := *task.Integration
			receipt.State, receipt.ResultingTargetHead = "recovered", a.TargetOID
			updated, err := pebblestore.FinishProjectTaskIntegration(db, p.AccountScopeID, task, &receipt)
			if err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
			copy = *updated
			a.TaskRevision = updated.Revision
		}
		copy.DeliveryAssessment = a
		writeJSON(w, http.StatusOK, map[string]any{"status": a.State, "task": sanitizeProjectTaskForClient(&copy)})
		return
	}
	if revision != task.Revision || target != a.TargetOID || source == "" || target == "" {
		writeError(w, http.StatusConflict, errors.New("recovery revision or target HEAD changed; refresh task"))
		return
	}
	if a.State != "history_rewritten" && a.State != "history_equivalent" && a.State != "candidate_work" {
		writeError(w, http.StatusConflict, fmt.Errorf("task delta cannot be recovered: %s", a.Reason))
		return
	}
	in := worktree.TaskDeliveryInput{Identity: *a, SourcePath: session.WorktreeRootPath, TargetPath: task.SourceWorkspace.Path}
	origin := &pebblestore.ProjectTaskRecoverySource{SessionID: session.ID, WorkspacePath: session.WorktreeRootPath, Branch: a.SourceBranch, BaseCommit: a.BaseOID, HeadCommit: a.SourceOID, TargetBranch: a.TargetBranch, TargetHead: a.TargetOID}
	if err := s.validateProjectTaskRecovery(p, task, origin); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	service := &worktree.Service{}
	receipt := &pebblestore.ProjectTaskIntegration{SessionID: session.ID, SourceBranch: a.SourceBranch, SourceHead: a.SourceOID, TargetBranch: a.TargetBranch, TargetWorkspacePath: in.TargetPath, PreviousTargetHead: a.TargetOID, RecoveryBase: a.BaseOID}
	preparedRevision := revision + 1
	if previous := task.Integration; previous != nil && previous.State == "in_progress" {
		if previous.SessionID != session.ID || previous.AttemptID != attempt || previous.SourceHead != source || previous.RecoveryBase != a.BaseOID || previous.PreviousTargetHead != target {
			writeError(w, http.StatusConflict, errors.New("retained recovery operation has different provenance; review its source and target"))
			return
		}
		copy := *previous
		receipt = &copy
		preparedRevision = revision
	} else if err := pebblestore.BeginProjectTaskIntegration(db, p.AccountScopeID, task, receipt); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	fail := func(err error) {
		receipt.State, receipt.Error = "conflict", err.Error()
		if _, persistErr := pebblestore.FinishProjectTaskIntegration(db, p.AccountScopeID, task, receipt); persistErr != nil {
			err = fmt.Errorf("%w; retain operation %s for reconciliation: %v", err, receipt.OperationID, persistErr)
		}
		writeError(w, http.StatusConflict, err)
	}
	prepared, err := service.PrepareTaskDeltaRecovery(r.Context(), in)
	if err != nil {
		fail(err)
		return
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
		fail(err)
		return
	}
	if prepared.Conflict != "" {
		fail(errors.New(prepared.Conflict))
		return
	}
	var plan worktree.TaskIntegrationPlan
	if !prepared.Equivalent {
		plan, err = service.PrepareTaskIntegration(in.TargetPath, a.TargetBranch, a.TargetOID, []worktree.TaskIntegrationChild{{SessionID: session.ID, BaseCommit: a.TargetOID, HeadCommit: prepared.PreparedHead, PreserveAncestry: true}})
		if err != nil {
			fail(err)
			return
		}
	}
	if err := s.validateProjectTaskRecovery(p, task, origin); err != nil {
		fail(err)
		return
	}
	updated, err := pebblestore.FinishProjectTaskIntegrationGuarded(db, p.AccountScopeID, task, receipt, pinned.Revision, func() error {
		// Do not call store APIs inside this mutation guard. The authenticated
		// source/target Git tuple is rechecked independently immediately before apply.
		if err := service.CheckTaskDeltaRecovery(r.Context(), in); err != nil {
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
		fail(err)
		return
	}
	// Reuse canonical ephemeral mapping; no read-side writes or polling.
	_ = reconcileTaskGitStateContext(r.Context(), db, updated)
	writeJSON(w, http.StatusOK, map[string]any{"status": receipt.State, "task": sanitizeProjectTaskForClient(updated)})
}
