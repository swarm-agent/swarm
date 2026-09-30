package api

import (
	"fmt"
	"log"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ConfigureProjectRealtime wires startup publication for project and task updates to the durable V3 hub.
// Missed wakeups are repaired by scoped outbox replay, never by polling.
func (s *Server) ConfigureProjectRealtime(store *pebblestore.Store) {
	if s == nil || store == nil {
		return
	}
	store.SetProjectPublisher(func(record pebblestore.V3RealtimeOutboxRecord) {
		if err := s.publishCommittedV3RealtimeOutbox(record); err != nil {
			log.Print("project realtime wake failed; durable replay required")
		}
	})
}

// reconcileProjectTaskRunLifecycle is the in-process bridge from a managed V3 run terminal
// mutation to the durable project task model, ensuring project tasks transition out of
// in_progress and emit project.updated invalidation.
func (s *Server) reconcileProjectTaskRunLifecycle(job sessionV3ExecutorJob, status, reason string) error {
	if s == nil || s.sessions == nil {
		return nil
	}
	db := s.sessions.Store()
	if db == nil {
		return nil
	}
	session, ok, err := s.sessions.GetSession(job.SessionID)
	if err != nil || !ok {
		return err
	}
	projectID := sessionsV3MetadataString(session.Metadata, "project_id")
	taskID := sessionsV3MetadataString(session.Metadata, "task_id")
	if taskID == "" {
		taskID = sessionsV3MetadataString(session.Metadata, "project_task_id")
	}
	if projectID == "" || taskID == "" {
		return nil
	}
	accountScopeID := strings.TrimSpace(job.Principal.AccountScopeID)
	if accountScopeID == "" {
		accountScopeID = strings.TrimSpace(session.AccountScopeID)
	}
	if accountScopeID == "" || (session.AccountScopeID != "" && accountScopeID != session.AccountScopeID) {
		return nil
	}

	task, ok, err := db.GetProjectTask(accountScopeID, projectID, taskID)
	if err != nil || !ok || task == nil {
		return err
	}
	isPrimarySession := task.SessionID == job.SessionID
	if isPrimarySession && task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" {
		state, found, err := db.GetV3SessionRunState(job.SessionID)
		if err != nil {
			return err
		}
		if !found || state.AccountScopeID != accountScopeID || state.RunID != job.RunID {
			return nil
		}
	}
	isTaskProgramSession := false
	progID := task.TaskProgramID
	if progID == "" && task.TaskProgram != nil {
		progID = task.TaskProgram.ID
	}
	if progID != "" && task.SessionID != "" {
		if prog, ok, _ := db.GetTaskProgram(task.SessionID, progID); ok {
			for _, j := range prog.Jobs {
				if j.ChildSessionID == job.SessionID || j.CurrentSessionID == job.SessionID {
					isTaskProgramSession = true
					break
				}
			}
		}
	}

	if (!isPrimarySession && !isTaskProgramSession) || (task.AccountID != "" && task.AccountID != accountScopeID) {
		return nil
	}
	if task.Archived || task.Agent == "image" || task.Agent == "video" || task.Agent == "sound" || task.Agent == "audio" {
		return nil
	}
	if task.IsIntegrated || task.Status == "completed" || task.Status == "rejected" || task.Status == "pending_approval" || (task.Status == "queued" && status == sessionruntime.RunIntentCompleted) {
		return nil
	}

	// Task Programs: sync TaskProgramStatus and task status, then emit project invalidation
	if task.TaskProgramID != "" || task.TaskProgram != nil {
		if progID != "" && task.SessionID != "" {
			if prog, ok, _ := db.GetTaskProgram(task.SessionID, progID); ok {
				_, err = db.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
					if t.SessionID != task.SessionID {
						return nil
					}
					currentProgID := t.TaskProgramID
					if currentProgID == "" && t.TaskProgram != nil {
						currentProgID = t.TaskProgram.ID
					}
					if currentProgID != progID {
						return nil
					}
					t.TaskProgramStatus = &prog
					if t.IsIntegrated || t.Status == "completed" || t.Status == "rejected" {
						return nil
					}
					switch prog.State {
					case pebblestore.TaskProgramStateRunning:
						t.Status = "in_progress"
					case pebblestore.TaskProgramStateCompleted:
						if !t.IsIntegrated {
							t.Status = "needs_review"
							if t.ActionNeeded == "" || strings.HasPrefix(t.ActionNeeded, "Action Needed: 0") {
								t.ActionNeeded = "Action Needed: All task program jobs finished. Verify promotion into the captured target."
							}
						} else {
							t.Status = "completed"
						}
					case pebblestore.TaskProgramStateBlocked:
						t.Status = "needs_review"
						if prog.Blocker != nil && prog.Blocker.Message != "" {
							t.ActionNeeded = prog.Blocker.Message
							t.LastError = prog.Blocker.Message
						}
					case pebblestore.TaskProgramStateFailed, pebblestore.TaskProgramStateCancelled:
						t.Status = "failed"
						if prog.Blocker != nil && prog.Blocker.Message != "" {
							t.LastError = prog.Blocker.Message
						}
					}
					return nil
				})
				return err
			}
		}
		return nil
	}

	switch status {
	case sessionruntime.RunIntentCompleted:
		if task.Status == "planning" {
			active, hasActive, planErr := db.GetActivePlan(task.SessionID)
			if planErr == nil && hasActive && active.PlanID != "" {
				plan, found, pErr := db.GetPlan(task.SessionID, active.PlanID)
				if pErr == nil && found && plan.Document != nil && len(plan.Document.Checkpoints) > 0 {
					_, err = db.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
						if t.SessionID != job.SessionID || t.Status != "planning" {
							return nil
						}
						t.Status = "pending_approval"
						t.PlanBinding = &pebblestore.ProjectTaskPlanBinding{
							PlanID:             plan.ID,
							SessionID:          task.SessionID,
							DefinitionRevision: plan.Version,
						}
						t.PlanDocument = plan.Document
						t.ActionNeeded = "Review plan in task card and click Approve"
						t.WhatDidDo = append(t.WhatDidDo, "Plan agent authored structured plan")
						return nil
					})
					return err
				}
			}
			_, err = db.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
				if t.SessionID != job.SessionID || t.Status != "planning" {
					return nil
				}
				t.Status = "needs_review"
				t.ActionNeeded = "Action Needed: Plan agent finished investigation. Review session findings."
				t.WhatDidDo = append(t.WhatDidDo, "Completed planning investigation")
				return nil
			})
			return err
		}
		// A provider turn ending does not complete an approved checkpoint plan.
		if task.PlanBinding != nil && task.PlanBinding.PlanID != "" {
			plan, found, planErr := db.GetPlan(task.SessionID, task.PlanBinding.PlanID)
			if planErr != nil {
				return planErr
			}
			if !found || plan.Document == nil || len(plan.Document.Checkpoints) == 0 {
				return nil
			}
			for _, checkpoint := range plan.Document.Checkpoints {
				if checkpoint.Status != "completed" {
					return nil
				}
			}
		}
		gitState := inspectTaskGitState(*task, db)
		_, err = db.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if t.SessionID != job.SessionID {
				return nil
			}
			if t.IsIntegrated || t.Status == "completed" || t.Status == "rejected" {
				return nil
			}
			if t.Status != "in_progress" {
				return nil
			}
			if gitState.gitStatus != "unknown" {
				t.UnintegratedCommits = gitState.unintegratedCommits
				t.GitStatus = gitState.gitStatus
				t.IsIntegrated = gitState.isIntegrated
				t.BehindCommits = gitState.behindCommits
				t.DiffSummary = gitState.diffSummary
				t.IsDirty = gitState.isDirty
				t.DirtyCount = gitState.dirtyCount
				t.SyncWarning = gitState.syncWarning
			}
			if gitState.worktreeBranch != "" {
				t.WorktreeBranch = gitState.worktreeBranch
				t.WorktreeName = gitState.worktreeName
			}
			if gitState.baseBranch != "" {
				t.BaseBranch = gitState.baseBranch
			}
			if !t.IsIntegrated {
				t.Status = "needs_review"
				if t.ActionNeeded == "" || strings.HasPrefix(t.ActionNeeded, "Action Needed: 0") || t.ActionNeeded == "Executing reopened task" {
					if t.UnintegratedCommits > 0 {
						baseBranch := t.BaseBranch
						if baseBranch == "" {
							baseBranch = "captured target"
						}
						t.ActionNeeded = fmt.Sprintf("Action Needed: Review changes and integrate %d commit(s) into %s", t.UnintegratedCommits, baseBranch)
					} else {
						t.ActionNeeded = "Action Needed: Review agent deliverables and verify outcomes"
					}
				}
			} else {
				t.Status = "completed"
			}
			return nil
		})
		return err
	case sessionruntime.RunIntentCancelled:
		_, err = db.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if t.SessionID != job.SessionID {
				return nil
			}
			if t.IsIntegrated || t.Status == "completed" || t.Status == "rejected" {
				return nil
			}
			if t.Status != "in_progress" && t.Status != "planning" {
				return nil
			}
			t.Status = "failed"
			if t.LastError == "" && reason != "" {
				t.LastError = reason
			}
			t.ActionNeeded = "Action Needed: Run was cancelled. Retry or reassign task."
			return nil
		})
		return err
	case sessionruntime.RunIntentFailed, sessionruntime.RunIntentExpired, sessionruntime.RunIntentInterrupted, sessionruntime.RunIntentDispatchBlocked:
		_, err = db.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if t.SessionID != job.SessionID {
				return nil
			}
			if t.IsIntegrated || t.Status == "completed" || t.Status == "rejected" {
				return nil
			}
			if t.Status != "in_progress" && t.Status != "planning" {
				return nil
			}
			t.Status = "failed"
			if t.LastError == "" && reason != "" {
				t.LastError = reason
			}
			if reason != "" {
				t.ActionNeeded = fmt.Sprintf("Action Needed: Run failed (%s). Retry task.", reason)
			} else {
				t.ActionNeeded = "Action Needed: Run failed. Review error and retry task."
			}
			return nil
		})
		return err
	}
	return nil
}
