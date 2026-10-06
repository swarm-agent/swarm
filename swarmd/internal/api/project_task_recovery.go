package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/taskrouter"
	"swarm/packages/swarmd/internal/tool"
)

// reconcileProjectTaskSession resumes only the exact owner of a durable reservation.
// A recovered session must never be reallocated or given a second initial run.
func (s *Server) reconcileProjectTaskSession(p identity.Principal, proj *pebblestore.ProjectRecord, task *pebblestore.ProjectTaskRecord, owned pebblestore.SessionSnapshot, status string) error {
	db := s.sessions.Store()
	if owned.ID != task.SessionID || owned.AccountScopeID != p.AccountScopeID || owned.UserID != p.UserID || owned.Metadata == nil || owned.Metadata["project_id"] != task.ProjectID || owned.Metadata["task_id"] != task.ID || owned.Metadata["swarm_v3_source_workspace_path"] != task.SourceWorkspace.Path || owned.Metadata["swarm_v3_source_workspace_id"] != task.SourceWorkspace.WorkspaceID || fmt.Sprint(owned.Metadata["swarm_v3_source_workspace_generation"]) != fmt.Sprint(task.SourceWorkspace.WorkspaceGeneration) {
		return errors.New("task session ownership does not match reservation")
	}
	if task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" && owned.Metadata["task_attempt_id"] != task.ActiveAttemptID {
		return errors.New("task session attempt does not match reservation")
	}
	if owned.WorktreeEnabled {
		if owned.WorktreeRootPath == "" || owned.WorkspacePath != owned.WorktreeRootPath || owned.Metadata["swarm_v3_runtime_workspace_path"] != owned.WorktreeRootPath || owned.Metadata["swarm_v3_worktree_owner_session_id"] != owned.ID {
			return errors.New("task session worktree owner does not match reservation")
		}
		if task.WorkspacePath != task.SourceWorkspace.Path && task.WorkspacePath != owned.WorktreeRootPath {
			return errors.New("task execution target conflicts with allocated worktree")
		}
		if task.WorkspacePath != task.SourceWorkspace.Path && task.WorktreeBranch != "" && task.WorktreeBranch != owned.WorktreeBranch {
			return errors.New("task branch conflicts with allocated worktree")
		}
		if task.WorkspacePath != task.SourceWorkspace.Path && task.BaseBranch != "" && task.BaseBranch != owned.WorktreeBaseBranch {
			return errors.New("task base branch conflicts with allocated worktree")
		}
		task.WorkspacePath = owned.WorktreeRootPath
		task.WorktreeBranch = owned.WorktreeBranch
		task.BaseBranch = owned.WorktreeBaseBranch
		task.WorktreeName = strings.TrimPrefix(strings.TrimPrefix(owned.WorktreeBranch, "agent/"), "worktree/")
	} else if task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" || task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" || task.TaskProgram != nil || task.PlanBinding != nil {
		return errors.New("coding or plan task session has no isolated worktree")
	} else if owned.WorkspacePath != task.SourceWorkspace.Path {
		return errors.New("task session workspace does not match reservation")
	}
	if err := verifyProjectTaskSession(task, owned, p.AccountScopeID); err != nil {
		return err
	}
	if isProjectTaskFollowup(task) {
		if err := s.reconcileProjectTaskSessionBinding(p, task, &owned); err != nil {
			return err
		}
		if err := s.reconcileTaskFollowupSourceGrants(p, task, &owned); err != nil {
			return err
		}
	}
	messages, err := db.ListMessages(owned.ID, 0, 1000)
	if err != nil {
		return err
	}
	seedFound := false
	for _, msg := range messages {
		if msg.Metadata != nil && msg.Metadata["role"] == "project_context_seed" {
			if msg.Metadata["task_id"] != task.ID || msg.Metadata["project_id"] != task.ProjectID {
				return errors.New("task seed belongs to another reservation")
			}
			if len(msg.Media) != len(task.AttachedMedia) {
				return errors.New("task seed attachment delivery is incomplete; refusing to launch without attachments")
			}
			for i, attachment := range task.AttachedMedia {
				ref := msg.Media[i]
				if attachment.DigestSHA256 != "" && ref.DigestSHA256 != attachment.DigestSHA256 || attachment.SizeBytes != 0 && ref.Size != attachment.SizeBytes || attachment.MediaType != "" && ref.MIMEType != attachment.MediaType {
					return errors.New("task seed attachment does not match the admitted reference")
				}
			}
			seedFound = true
		}
	}
	if !seedFound {
		if len(messages) != 0 {
			return errors.New("task session has messages without an owned seed; manual reconciliation required")
		}
		attachmentPlan, payloads, err := s.prepareProjectTaskAttachments(context.Background(), p, owned, task.AttachedMedia)
		if err != nil {
			return err
		}
		refs, err := s.retainProjectTaskAttachments(attachmentPlan, payloads)
		if err != nil {
			return err
		}
		var router taskrouter.Service
		seed := router.BuildAgentSeedPrompt(task, proj) + projectTaskFollowupContext(task) + projectTaskEnvironmentContext(task)
		if owned.Mode == sessionruntime.ModePlan {
			seed += "\n\n## Planning phase\nInvestigate only as needed, then submit a complete executable structured plan using exit_plan_mode. Include ordered checkpoints, concrete tasks and acceptance criteria. This project task must show the submitted plan for user approval before any implementation. Do not write implementation files or execute the task in this phase. For coding deliverables, include a checkpoint task_program with a Coder job, explicit workspace-relative owned_scope, implementation instructions, deliverable, acceptance_criteria and dependency_evidence. The approved checkpoint must launch that program rather than implementing directly in the planner workspace. Preserve every user requirement, including committing changes. Complete the checkpoint through the plan lifecycle only after verifying the returned deliverable; completing subtasks alone is not checkpoint completion."
		}
		now := time.Now().UnixMilli()
		msg := pebblestore.MessageSnapshot{ID: fmt.Sprintf("msg_%s_recovery", owned.ID), SessionID: owned.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Role: "user", Content: seed, Media: refs, Metadata: map[string]any{"role": "project_context_seed", "task_id": task.ID, "project_id": task.ProjectID, "context_pool_summary": task.ContextPoolSummary}, CreatedAt: now}
		key := fmt.Sprintf("project-task:seed:%s:%s:%s", task.ProjectID, task.ID, task.SessionID)
		_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: owned.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: pebblestore.V3SessionMutationAppendMessage, Message: &msg, NowUnixMs: now})
		if err != nil {
			return fmt.Errorf("recover task seed: %w", err)
		}
	}
	if status == "in_progress" && task.PlanBinding == nil && task.TaskProgram == nil && task.TaskProgramID == "" {
		runID := task.ExecutionRunID()
		active, ok, err := db.GetV3SessionActiveRunIntent(task.SessionID)
		if err != nil {
			return err
		}
		if ok {
			if active.RunID != runID || active.AccountScopeID != p.AccountScopeID {
				return errors.New("task session has unrelated active run")
			}
			if active.Status == pebblestore.V3RunIntentPendingExecutor || active.Status == pebblestore.V3RunIntentRunning {
				if active.Status == pebblestore.V3RunIntentPendingExecutor {
					if task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" {
						if err := s.enqueueProjectTaskFollowup(p, owned.ID, runID, active.ParentSessionID); err != nil {
							return err
						}
					} else {
						s.EnqueueSessionRun(p, owned.ID, runID, active.ParentSessionID)
					}
				}
				return nil
			}
			if task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" {
				return nil
			}
			return errors.New("task initial run already recorded; use explicit retry lifecycle")
		}
		intents, err := db.ListRunIntents(task.SessionID, 1000)
		if err != nil {
			return err
		}
		if len(intents) != 0 {
			if task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" && len(intents) == 1 && intents[0].RunID == runID && intents[0].AccountScopeID == p.AccountScopeID {
				return nil
			}
			return errors.New("task session already has run history; use explicit retry lifecycle")
		}
		now := time.Now().UnixMilli()
		parent := task.OriginSessionID
		if len(task.CoderAssignments) > 0 {
			parent = ""
		}
		run := &pebblestore.V3SessionRunIntent{SessionID: owned.ID, RunID: runID, EpochID: "epoch-00000000000000000001", UserID: p.UserID, AccountScopeID: p.AccountScopeID, ParentSessionID: parent, Status: pebblestore.V3RunIntentPendingExecutor, CreatedAt: now, UpdatedAt: now}
		key := fmt.Sprintf("project-task:run:%s:%s", task.ID, runID)
		_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: owned.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessionruntime.SessionMutationRecordRunIntent, RunIntent: run, NowUnixMs: now})
		if err != nil {
			return err
		}
		if s.runner == nil {
			return errors.New("runner service is not configured")
		}
		if task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" {
			if err := s.enqueueProjectTaskFollowup(p, owned.ID, runID, parent); err != nil {
				return err
			}
		} else {
			s.EnqueueSessionRun(p, owned.ID, runID, parent)
		}
	}
	return nil
}

// recoverProjectTaskReservation completes a partially committed creation using
// the persisted task as authority, never a freshly routed/generated task.
func (s *Server) recoverProjectTaskReservation(ctx context.Context, p identity.Principal, proj *pebblestore.ProjectRecord, task *pebblestore.ProjectTaskRecord, input tool.ProjectTaskCreateInput) error {
	db := s.sessions.Store()
	if task.SessionID == "" {
		return errors.New("reserved task has no deterministic session identity")
	}
	owned, found, err := db.GetSession(task.SessionID)
	if err != nil {
		return err
	}
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(input.Description)
	}
	if prompt == "" {
		prompt = task.Title
	}
	status := task.Status
	if task.PlanBinding != nil || input.Document != nil || input.PlanDocument != nil || input.TaskProgram != nil {
		status = "pending_approval"
	}
	if !found {
		if task.WorkspacePath != task.SourceWorkspace.Path {
			return errors.New("reserved task has an allocated runtime but no session; manual reconciliation required")
		}
		if task.TaskProgram != nil && input.Document == nil && input.PlanDocument == nil && task.PlanBinding == nil && status == "in_progress" {
			if err := s.deployProjectTaskProgram(p, proj, task); err != nil {
				return err
			}
		} else if err := s.deployProjectTaskExecution(p, proj, task, status, prompt); err != nil {
			return err
		}
	} else if owned.Metadata != nil && owned.Metadata["role"] == "task_program_coordinator" && task.TaskProgram != nil {
		if owned.ID != task.SessionID || owned.AccountScopeID != p.AccountScopeID || owned.UserID != p.UserID || owned.WorkspacePath != task.SourceWorkspace.Path || owned.Metadata["project_id"] != task.ProjectID || owned.Metadata["task_id"] != task.ID || owned.Metadata["task_program_id"] != task.TaskProgram.ID || owned.Metadata["swarm_v3_source_workspace_id"] != task.SourceWorkspace.WorkspaceID || fmt.Sprint(owned.Metadata["swarm_v3_source_workspace_generation"]) != fmt.Sprint(task.SourceWorkspace.WorkspaceGeneration) {
			return errors.New("task program coordinator ownership mismatch")
		}
		if task.Status == "in_progress" {
			if err := s.deployProjectTaskProgram(p, proj, task); err != nil {
				return err
			}
		}
	} else if err := s.reconcileProjectTaskSession(p, proj, task, owned, status); err != nil {
		return err
	}
	// Verify the durable plan link before publishing the recovered task and active
	// project reference. A mismatched plan must not leave a partially completed card.
	if task.PlanBinding != nil {
		if task.PlanBinding.PlanID == "" || task.PlanBinding.SessionID != task.SessionID {
			return errors.New("task plan binding session mismatch")
		}
		plan, ok, err := db.GetPlan(task.SessionID, task.PlanBinding.PlanID)
		if err != nil {
			return err
		}
		if !ok || plan.AccountScopeID != p.AccountScopeID || plan.SessionID != task.SessionID || task.PlanBinding.DefinitionRevision <= 0 {
			return errors.New("task plan binding has no matching durable definition")
		}
		if plan.ApprovalState == "approved" {
			if task.PlanBinding.Receipt == "" || task.PlanBinding.Receipt != plan.AcceptedDefinitionReceipt {
				return errors.New("task plan accepted receipt does not match binding")
			}
		} else if plan.Version != task.PlanBinding.DefinitionRevision {
			return errors.New("task plan definition revision changed")
		}
	}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		return err
	}
	_, err = db.UpdateProject(p.AccountScopeID, task.ProjectID, func(pr *pebblestore.ProjectRecord) error {
		for _, id := range pr.ActiveTaskIDs {
			if id == task.ID {
				return nil
			}
		}
		pr.ActiveTaskIDs = append(pr.ActiveTaskIDs, task.ID)
		return nil
	})
	if err != nil {
		return err
	}
	if task.PlanBinding != nil {
		return nil
	}
	doc := input.Document
	if doc == nil {
		doc = input.PlanDocument
	}
	if doc == nil && task.TaskProgram != nil {
		doc = &pebblestore.SessionPlanDocument{Title: task.Title, Info: pebblestore.SessionPlanInfo{Goal: prompt}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: task.Title, Tasks: []string{"Execute the proposed task program and verify its deliverables"}, AcceptanceCriteria: []string{"All declared jobs deliver their accepted committed outputs"}, TaskProgram: task.TaskProgram}}}
	}
	if doc != nil {
		result, err := s.SubmitProjectTaskPlan(ctx, sessionruntime.ProjectTaskPlanSubmissionInput{AccountScopeID: p.AccountScopeID, UserID: p.UserID, ProjectID: task.ProjectID, TaskID: task.ID, Document: doc, PlanText: task.FullPlanMarkdown, Title: task.Title, WorkspacePath: task.WorkspacePath, ParentSessionID: proj.PrimarySessionID})
		if err != nil {
			return err
		}
		*task = result.Task
	}
	return nil
}
