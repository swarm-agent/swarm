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
	if task.SessionID != job.SessionID || (task.AccountID != "" && task.AccountID != accountScopeID) {
		return nil
	}
	// Do not override TaskPrograms, direct media tasks, or completed/integrated tasks
	if task.TaskProgramID != "" || task.TaskProgram != nil {
		return nil
	}
	if task.Agent == "image" || task.Agent == "video" || task.Agent == "sound" || task.Agent == "audio" {
		return nil
	}
	if task.IsIntegrated || task.Status == "completed" || task.Status == "pending_approval" || task.Status == "planning" || task.Status == "queued" {
		return nil
	}

	switch status {
	case sessionruntime.RunIntentCompleted:
		gitState := inspectTaskGitState(*task, db)
		_, err = db.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			if t.Status != "in_progress" || t.SessionID != job.SessionID {
				return nil
			}
			if gitState.unintegratedCommits > 0 {
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
							baseBranch = "dev/main"
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
			if t.Status != "in_progress" || t.SessionID != job.SessionID {
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
			if t.Status != "in_progress" || t.SessionID != job.SessionID {
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
