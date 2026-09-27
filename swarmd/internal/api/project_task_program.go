package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// computeTaskProgramDefinitionHash computes the SHA256 hex digest of a TaskProgramDefinition.
func computeTaskProgramDefinitionHash(def pebblestore.TaskProgramDefinition) string {
	raw, err := json.Marshal(def)
	if err != nil {
		return fmt.Sprintf("hash_%d", time.Now().UnixNano())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// sanitizeBranchSlug turns arbitrary strings into a safe git branch segment.
func sanitizeBranchSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var out []rune
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			out = append(out, r)
		} else if r == ' ' || r == '_' || r == '/' || r == '.' {
			out = append(out, '-')
		}
	}
	res := strings.Trim(string(out), "-")
	if len(res) > 30 {
		res = res[:30]
	}
	if res == "" {
		res = "task"
	}
	return res
}

// hydrateTaskProgramStatus populates TaskProgramStatus from Pebble if a program ID exists.
func hydrateTaskProgramStatus(task *pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) {
	if task == nil || db == nil {
		return
	}
	if task.TaskProgramID != "" && task.SessionID != "" {
		if prog, ok, _ := db.GetTaskProgram(task.SessionID, task.TaskProgramID); ok {
			task.TaskProgramStatus = &prog
		}
	}
}

// hydrateTaskPlanDocument populates PlanDocument from Pebble if a PlanBinding exists.
func hydrateTaskPlanDocument(task *pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) {
	if task == nil || db == nil {
		return
	}
	if task.PlanDocument == nil && task.PlanBinding != nil && task.PlanBinding.PlanID != "" && task.SessionID != "" {
		if plan, ok, _ := db.GetPlan(task.SessionID, task.PlanBinding.PlanID); ok && plan.Document != nil {
			task.PlanDocument = plan.Document
		}
	}
}

func findJobRecord(jobs []pebblestore.TaskProgramJobRecord, jobID string) *pebblestore.TaskProgramJobRecord {
	for i := range jobs {
		if jobs[i].JobID == jobID {
			return &jobs[i]
		}
	}
	return nil
}

// deployProjectTaskProgram initializes and deploys a TaskProgram on a project task coordinator session.
func (s *Server) deployProjectTaskProgram(p identity.Principal, proj *pebblestore.ProjectRecord, task *pebblestore.ProjectTaskRecord) error {
	if !p.Valid() || p.Type != "user" || p.UserID == "" || p.AccountScopeID == "" {
		return errors.New("deploy task program: user id is required")
	}
	if task == nil {
		return errors.New("task is required")
	}
	db := s.sessions.Store()
	if db == nil {
		return errors.New("session store not available")
	}
	if task.TaskProgram == nil && task.TaskProgramID != "" && task.SessionID != "" {
		if existing, ok, _ := db.GetTaskProgram(task.SessionID, task.TaskProgramID); ok {
			task.TaskProgram = &existing.Definition
		}
	}
	if task.TaskProgram == nil {
		return errors.New("task program is required")
	}
	if err := pebblestore.ValidateTaskProgramDefinition(task.TaskProgram); err != nil {
		return fmt.Errorf("task program validation failed: %w", err)
	}

	// 1. Ensure coordinator session exists
	now := time.Now().UnixMilli()
	if task.SessionID == "" {
		sessionID := sessionruntime.NewSessionID()
		task.SessionID = sessionID
		wsPath := strings.TrimSpace(task.WorkspacePath)
		if wsPath == "" && len(task.WorkspacesInvolved) > 0 {
			wsPath = task.WorkspacesInvolved[0]
			task.WorkspacePath = wsPath
		}
		if wsPath == "" && proj != nil && len(proj.Workspaces) > 0 {
			wsPath = proj.Workspaces[0].Path
			task.WorkspacePath = wsPath
		}
		if wsPath == "" || wsPath == "." {
			return errors.New("deploy task program requires a valid repository workspace path (dot '.' is not allowed)")
		}

		// Resolve default preference for coordinator
		var pref pebblestore.ModelPreference
		if s.agentModelSettings != nil && p.AccountScopeID != "" {
			if settings, err := s.agentModelSettings.GetForAccount(p.AccountScopeID); err == nil {
				pref = pebblestore.ModelPreference{
					Provider:    strings.TrimSpace(settings.Swarm.Action.Provider),
					Model:       strings.TrimSpace(settings.Swarm.Action.Model),
					Thinking:    strings.TrimSpace(settings.Swarm.Action.Thinking),
					ServiceTier: strings.TrimSpace(settings.Swarm.Action.ServiceTier),
					ContextMode: strings.TrimSpace(settings.Swarm.Action.ContextMode),
				}
			}
		}
		if (pref.Provider == "" || pref.Model == "") && s.model != nil {
			if def, err := s.model.ResolvePreference(pebblestore.ModelPreference{}); err == nil {
				pref = def.Preference
			}
		}

		sessionSnapshot := pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         p.UserID,
			AccountScopeID: p.AccountScopeID,
			WorkspacePath:  wsPath,
			WorkspaceName:  filepath.Base(wsPath),
			Title:          fmt.Sprintf("[%s] Coordinator: %s", proj.Name, task.Title),
			Mode:           sessionruntime.ModeAuto,
			Preference:     pref,
			Metadata: map[string]any{
				"project_id":      task.ProjectID,
				"task_id":         task.ID,
				"task_title":      task.Title,
				"role":            "task_program_coordinator",
				"task_program_id": task.TaskProgram.ID,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}

		createKey := fmt.Sprintf("project-task:coordinator:%s:%d", sessionID, now)
		_, createErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
			SessionID:       sessionID,
			UserID:          p.UserID,
			AccountScopeID:  p.AccountScopeID,
			ClientRequestID: createKey,
			IdempotencyKey:  createKey,
			PayloadHash:     createKey,
			RequestHash:     createKey,
			Kind:            sessionruntime.SessionMutationCreateSession,
			Session:         &sessionSnapshot,
		})
		if createErr != nil {
			return fmt.Errorf("create coordinator session: %w", createErr)
		}
	}

	// 2. Initialize or get TaskProgramRecord in Pebble
	progID := strings.TrimSpace(task.TaskProgram.ID)
	if progID == "" {
		progID = fmt.Sprintf("prog_%s", task.ID)
		task.TaskProgram.ID = progID
	}

	initialRecord := pebblestore.TaskProgramRecord{
		ParentSessionID: task.SessionID,
		ProgramID:       progID,
		DefinitionHash:  computeTaskProgramDefinitionHash(*task.TaskProgram),
		Definition:      *task.TaskProgram,
		State:           pebblestore.TaskProgramStateRunning,
		ActiveStageID:   task.TaskProgram.Stages[0].ID,
		Jobs:            make([]pebblestore.TaskProgramJobRecord, len(task.TaskProgram.Jobs)),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	for i, j := range task.TaskProgram.Jobs {
		initialRecord.Jobs[i] = pebblestore.TaskProgramJobRecord{
			JobID:         j.ID,
			StageID:       j.StageID,
			State:         pebblestore.TaskProgramJobDeclared,
			AttemptNumber: 1,
			UpdatedAt:     now,
		}
	}

	record, _, err := db.CreateTaskProgram(initialRecord)
	if err != nil {
		if existing, ok, getErr := db.GetTaskProgram(task.SessionID, progID); getErr == nil && ok {
			record = existing
		} else {
			return fmt.Errorf("create task program record: %w", err)
		}
	}

	task.TaskProgramID = progID
	task.TaskProgramStatus = &record
	task.Status = "in_progress"
	task.ActionNeeded = "Task program executing parallel cohort..."
	if len(task.WhatDidDo) == 0 {
		task.WhatDidDo = []string{
			fmt.Sprintf("Deployed Task Program with %d job(s) across %d stage(s)", len(task.TaskProgram.Jobs), len(task.TaskProgram.Stages)),
		}
	}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		return err
	}

	// 3. Start canonical Task Program scheduler through runner with durable RunIntent
	if s.runner == nil {
		return errors.New("runner service is not configured")
	}
	runID := fmt.Sprintf("desktop-v3-run:tp-%s", task.ID)
	parentSessionID := ""
	if proj != nil {
		parentSessionID = proj.PrimarySessionID
	}
	runIntent := &pebblestore.V3SessionRunIntent{
		SessionID:       task.SessionID,
		RunID:           runID,
		EpochID:         "epoch-00000000000000000001",
		UserID:          p.UserID,
		AccountScopeID:  p.AccountScopeID,
		ParentSessionID: parentSessionID,
		Status:          pebblestore.V3RunIntentPendingExecutor,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	runKey := fmt.Sprintf("project-task:run:%s:%s", task.ID, runID)
	_, mutationErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       task.SessionID,
		UserID:          p.UserID,
		AccountScopeID:  p.AccountScopeID,
		ClientRequestID: runKey,
		IdempotencyKey:  runKey,
		PayloadHash:     runKey,
		RequestHash:     runKey,
		Kind:            sessionruntime.SessionMutationStartRun,
		RunIntent:       runIntent,
		NowUnixMs:       now,
	})
	if mutationErr != nil {
		return fmt.Errorf("start coordinator session run intent: %w", mutationErr)
	}

	go func() {
		_, execErr := s.runner.ExecuteTaskProgramForCoordinator(context.Background(), p, task.SessionID, runID, record)
		if execErr != nil {
			_ = db.UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Status = "failed"
				t.LastError = execErr.Error()
				t.ActionNeeded = fmt.Sprintf("Task program execution failed: %v", execErr)
				t.WhatNotDone = append(t.WhatNotDone, execErr.Error())
				return nil
			})
			failedStatus := pebblestore.V3RunIntentFailed
			nowMs := time.Now().UnixMilli()
			_ = s.sessions.Store().TransitionV3SessionRunIntentState(task.SessionID, runID, failedStatus, nowMs, p.AccountScopeID, p.UserID, execErr.Error())
		}
	}()
	return nil
}

// redeployTaskProgramJob redeploys a conflicted or failed job within the task program.
func (s *Server) redeployTaskProgramJob(p identity.Principal, projectID, taskID, jobID, feedback string) error {
	if !p.Valid() || p.Type != "user" || p.UserID == "" || p.AccountScopeID == "" {
		return errors.New("user id is required")
	}
	db := s.sessions.Store()
	if db == nil {
		return errors.New("session store not available")
	}
	task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil || !found || task == nil {
		return errors.New("project task not found")
	}
	if task.TaskProgramID == "" || task.SessionID == "" {
		return errors.New("task has no associated task program")
	}
	record, ok, err := db.GetTaskProgram(task.SessionID, task.TaskProgramID)
	if err != nil || !ok {
		return errors.New("task program not found")
	}
	targetJob := findJobRecord(record.Jobs, jobID)
	if targetJob == nil {
		return fmt.Errorf("job %q not found in task program", jobID)
	}
	now := time.Now().UnixMilli()
	newAttempt := targetJob.AttemptNumber + 1

	// Add generation record
	genHistory := append([]pebblestore.TaskProgramJobGeneration(nil), targetJob.GenerationHistory...)
	genHistory = append(genHistory, pebblestore.TaskProgramJobGeneration{
		Generation: targetJob.AttemptNumber,
		SessionID:  targetJob.ChildSessionID,
		RunID:      targetJob.CurrentRunID,
		State:      targetJob.State,
		StartedAt:  now,
		FinishedAt: now,
	})

	declaredState := pebblestore.TaskProgramJobDeclared
	runningState := pebblestore.TaskProgramStateRunning

	jobTransition := pebblestore.TaskProgramJobTransition{
		JobID:             jobID,
		State:             declaredState,
		AttemptNumber:     newAttempt,
		GenerationHistory: genHistory,
		IntegrationState:  "",
	}
	// Update record
	updatedRecord, _, err := db.TransitionTaskProgram(task.SessionID, task.TaskProgramID, pebblestore.TaskProgramTransition{
		ExpectedRevision: record.Revision,
		MutationID:       fmt.Sprintf("redeploy:%s:%d", jobID, now),
		State:            &runningState,
		ClearBlocker:     true,
		Jobs:             []pebblestore.TaskProgramJobTransition{jobTransition},
	})
	if err != nil {
		return fmt.Errorf("transition task program for redeploy: %w", err)
	}

	fb := strings.TrimSpace(feedback)
	_, _ = db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		t.TaskProgramStatus = &updatedRecord
		t.Status = "in_progress"
		t.ActionNeeded = fmt.Sprintf("Redeploying job %s (Attempt %d)...", jobID, newAttempt)
		if fb != "" {
			t.FeedbackHistory = append(t.FeedbackHistory, fmt.Sprintf("Job %s retry feedback: %s", jobID, fb))
			t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Redeployed job %s with conflict resolution instructions", jobID))
		} else {
			t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Redeployed job %s (Attempt %d)", jobID, newAttempt))
		}
		return nil
	})

	// Restart canonical execution if runner is active
	if s.runner == nil {
		return errors.New("runner service is not configured")
	}
	runID := fmt.Sprintf("desktop-v3-run:tp-%s-retry-%d", task.ID, newAttempt)
	go func() {
		_, execErr := s.runner.ExecuteTaskProgramForCoordinator(context.Background(), p, task.SessionID, runID, updatedRecord)
		if execErr != nil {
			_ = db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
				t.LastError = execErr.Error()
				t.ActionNeeded = fmt.Sprintf("Job %s retry failed: %v", jobID, execErr)
				t.WhatNotDone = append(t.WhatNotDone, execErr.Error())
				return nil
			})
		}
	}()
	return nil
}

// ApproveProjectTask implements the single canonical authenticated approval lifecycle for project tasks.
func (s *Server) ApproveProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string, guards ...tool.ProjectTaskApprovalGuards) (*pebblestore.ProjectTaskRecord, error) {
	if !p.Valid() || p.Type != "user" || strings.TrimSpace(p.UserID) == "" || strings.TrimSpace(p.AccountScopeID) == "" {
		return nil, errors.New("approve task requires an authenticated user identity")
	}
	projectID = strings.TrimSpace(projectID)
	taskID = strings.TrimSpace(taskID)
	if projectID == "" || taskID == "" {
		return nil, errors.New("project_id and task_id are required")
	}
	db := s.sessions.Store()
	if db == nil {
		return nil, errors.New("database not available")
	}
	existingTask, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !found || existingTask == nil {
		return nil, fmt.Errorf("task %q not found", taskID)
	}
	if existingTask.AccountID != "" && existingTask.AccountID != p.AccountScopeID {
		return nil, errors.New("cross-account task approval forbidden")
	}
	if existingTask.ProjectID != "" && existingTask.ProjectID != projectID {
		return nil, errors.New("cross-project task approval forbidden")
	}
	proj, projFound, err := db.GetProject(p.AccountScopeID, projectID)
	if err != nil {
		return nil, err
	}
	if !projFound || proj == nil {
		return nil, fmt.Errorf("project %q not found", projectID)
	}
	if proj.AccountID != "" && proj.AccountID != p.AccountScopeID {
		return nil, errors.New("cross-account project access forbidden")
	}
	if existingTask.Status == "rejected" {
		return nil, errors.New("cannot approve rejected task")
	}
	if existingTask.Status == "completed" {
		hydrateTaskPlanDocument(existingTask, db)
		hydrateTaskProgramStatus(existingTask, db)
		return existingTask, nil
	}

	var guard tool.ProjectTaskApprovalGuards
	if len(guards) > 0 {
		guard = guards[0]
	}
	if guard.SessionID != "" && existingTask.SessionID != "" && guard.SessionID != existingTask.SessionID {
		return nil, fmt.Errorf("session ID mismatch: expected %q, got %q", existingTask.SessionID, guard.SessionID)
	}

	// Idempotent retry check: if task is in_progress AND execution run is genuinely active, do not duplicate run!
	if existingTask.Status == "in_progress" && existingTask.SessionID != "" {
		activeIntent, ok, _ := db.GetV3SessionActiveRunIntent(existingTask.SessionID)
		if ok && activeIntent != nil && (activeIntent.Status == pebblestore.V3RunIntentPendingExecutor || activeIntent.Status == pebblestore.V3RunIntentRunning) {
			hydrateTaskPlanDocument(existingTask, db)
			hydrateTaskProgramStatus(existingTask, db)
			return existingTask, nil
		}
	}

	if existingTask.PlanBinding != nil && existingTask.PlanBinding.PlanID != "" {
		planID := existingTask.PlanBinding.PlanID
		if guard.PlanID != "" && guard.PlanID != planID {
			return nil, fmt.Errorf("plan ID mismatch: expected %q, got %q", planID, guard.PlanID)
		}
		if existingTask.PlanBinding.SessionID != "" && existingTask.SessionID != "" && existingTask.PlanBinding.SessionID != existingTask.SessionID {
			return nil, errors.New("cross-session plan binding forbidden")
		}
		plan, ok, pErr := db.GetPlan(existingTask.SessionID, planID)
		if pErr != nil || !ok {
			return nil, fmt.Errorf("bound plan %q not found", planID)
		}
		if plan.AccountScopeID != p.AccountScopeID {
			return nil, errors.New("cross-account plan approval forbidden")
		}
		if plan.SessionID != existingTask.SessionID {
			return nil, errors.New("cross-session plan approval forbidden")
		}
		if plan.Status == "rejected" || plan.ApprovalState == "rejected" {
			return nil, errors.New("cannot approve rejected plan definition")
		}
		if existingTask.PlanBinding.DefinitionRevision > 0 && plan.Version != existingTask.PlanBinding.DefinitionRevision {
			return nil, fmt.Errorf("plan definition is stale (task revision %d, current %d)", existingTask.PlanBinding.DefinitionRevision, plan.Version)
		}
		if guard.DefinitionRevision > 0 && plan.Version != guard.DefinitionRevision {
			return nil, fmt.Errorf("plan definition is stale (guarded revision %d, current %d)", guard.DefinitionRevision, plan.Version)
		}

		session, sessFound, sErr := db.GetSession(existingTask.SessionID)
		if sErr != nil || !sessFound {
			return nil, fmt.Errorf("session %q not found", existingTask.SessionID)
		}

		var pref pebblestore.ModelPreference
		if s.agentModelSettings != nil && p.AccountScopeID != "" {
			if settings, err := s.agentModelSettings.GetForAccount(p.AccountScopeID); err == nil {
				pref = pebblestore.ModelPreference{
					Provider:    strings.TrimSpace(settings.Swarm.Action.Provider),
					Model:       strings.TrimSpace(settings.Swarm.Action.Model),
					Thinking:    strings.TrimSpace(settings.Swarm.Action.Thinking),
					ServiceTier: strings.TrimSpace(settings.Swarm.Action.ServiceTier),
					ContextMode: strings.TrimSpace(settings.Swarm.Action.ContextMode),
				}
			}
		}
		if (pref.Provider == "" || pref.Model == "") && s.model != nil {
			if def, err := s.model.ResolvePreference(pebblestore.ModelPreference{}); err == nil {
				pref = def.Preference
			}
		}

		var swarmProfile pebblestore.AgentProfile
		if s.agents != nil {
			swarmProfile, _ = s.agents.ResolveSystemAgent(agentruntime.SwarmAgentID, pebblestore.AgentProfile{
				Provider:        pref.Provider,
				Model:           pref.Model,
				Thinking:        pref.Thinking,
				AutoServiceTier: pref.ServiceTier,
				ContextMode:     pref.ContextMode,
			})
		}

		summary := sessionruntime.SummarizePlanExecution(plan.Document)
		firstCpID := summary.NextCheckpointID
		if firstCpID == "" && plan.Document != nil && len(plan.Document.Checkpoints) > 0 {
			firstCpID = plan.Document.Checkpoints[0].ID
		}
		if firstCpID == "" {
			return nil, errors.New("plan has no checkpoints to execute")
		}

		lifecycleMsg, _ := sessionruntime.BuildPlanExecutionLifecycleSystemMessage(sessionruntime.PlanExecutionLifecycleMessageInput{
			Action: "approve_and_start",
			Plan:   plan,
			Payload: map[string]any{
				"action":             "approve_and_start",
				"checkpoint_id":      firstCpID,
				"next_checkpoint_id": firstCpID,
				"next_action":        "run_checkpoint_with_current_context",
				"context_preserved":  true,
			},
		})

		var receipt string
		if plan.ApprovalState != "approved" {
			committed, commitErr := s.sessions.CommitV3PlanAcceptance(sessionruntime.PlanAcceptanceCommitInput{
				Session:              session,
				PlanID:               plan.ID,
				Title:                plan.Title,
				Plan:                 plan.Plan,
				Document:             plan.Document,
				ApplySessionMutation: s.sessions.ApplySessionMutation,
				ModePreference:       pref,
				ModeAgentProfile:     &swarmProfile,
				BuildLifecycleMessage: func(p pebblestore.SessionPlanSnapshot, s sessionruntime.PlanExecutionSummary) *pebblestore.MessageSnapshot {
					if lifecycleMsg.Content == "" {
						return nil
					}
					return &pebblestore.MessageSnapshot{
						Role:     "system",
						Content:  lifecycleMsg.Content,
						Metadata: lifecycleMsg.Metadata,
					}
				},
			})
			if commitErr != nil {
				return nil, fmt.Errorf("commit plan acceptance: %w", commitErr)
			}
			plan = committed.Plan
			receipt = committed.Mutation.PayloadHash
		} else if existingTask.PlanBinding != nil {
			receipt = existingTask.PlanBinding.Receipt
		}

		runID := sessionsV3PlanModeRunID(existingTask.SessionID, plan.ID, firstCpID, "attempt-1")
		attemptID := "attempt-1"
		if s.planLifecycle != nil {
			startResult, startErr := s.planLifecycle.ApproveAndStartPlanAutomatic(sessionruntime.PlanLifecycleExecutionInput{
				SessionID:    existingTask.SessionID,
				PlanID:       plan.ID,
				CheckpointID: firstCpID,
				RunID:        runID,
				AttemptID:    attemptID,
			})
			if startErr == nil && startResult.AttemptID != "" {
				attemptID = startResult.AttemptID
			}
			if startResult.Plan.Document != nil {
				plan = startResult.Plan
			}
		}

		now := time.Now().UnixMilli()
		parentSessionID := proj.PrimarySessionID
		runIntent := &pebblestore.V3SessionRunIntent{
			SessionID:       existingTask.SessionID,
			RunID:           runID,
			EpochID:         "epoch-00000000000000000001",
			UserID:          p.UserID,
			AccountScopeID:  p.AccountScopeID,
			ParentSessionID: parentSessionID,
			PlanID:          plan.ID,
			CheckpointID:    firstCpID,
			AttemptID:       attemptID,
			Status:          pebblestore.V3RunIntentPendingExecutor,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		runKey := fmt.Sprintf("project-task:run:%s:%s", existingTask.ID, runID)
		_, mutationErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
			SessionID:       existingTask.SessionID,
			UserID:          p.UserID,
			AccountScopeID:  p.AccountScopeID,
			ClientRequestID: runKey,
			IdempotencyKey:  runKey,
			PayloadHash:     runKey,
			RequestHash:     runKey,
			Kind:            sessionruntime.SessionMutationStartRun,
			RunIntent:       runIntent,
			NowUnixMs:       now,
		})
		if mutationErr != nil {
			return nil, fmt.Errorf("start plan run intent: %w", mutationErr)
		}
		s.EnqueueSessionRun(p, existingTask.SessionID, runID, parentSessionID)

		updated, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "in_progress"
			t.ActionNeeded = ""
			t.Agent = "swarm"
			t.WorkerName = "@Swarm Worker"
			if t.PlanBinding != nil {
				if receipt != "" {
					t.PlanBinding.Receipt = receipt
				}
			}
			t.PlanDocument = plan.Document
			t.WhatDidDo = append(t.WhatDidDo, "Plan approved; Swarm executing plan checkpoints")
			return nil
		})
		if err != nil {
			return nil, err
		}
		hydrateTaskPlanDocument(updated, db)
		hydrateTaskProgramStatus(updated, db)
		return updated, nil
	}

	if existingTask.TaskProgram != nil || existingTask.TaskProgramID != "" {
		if err := s.deployProjectTaskProgram(p, proj, existingTask); err != nil {
			return nil, fmt.Errorf("deploy task program: %w", err)
		}
		freshTask, _, _ := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		hydrateTaskPlanDocument(freshTask, db)
		hydrateTaskProgramStatus(freshTask, db)
		return freshTask, nil
	}

	if existingTask.Agent == "image" || existingTask.Agent == "video" || existingTask.Agent == "sound" || existingTask.Agent == "audio" {
		if err := s.deployProjectTaskExecution(p, proj, existingTask, "in_progress", ""); err != nil {
			return nil, fmt.Errorf("deploy media execution: %w", err)
		}
		_ = db.PutProjectTask(p.AccountScopeID, existingTask)
		hydrateTaskPlanDocument(existingTask, db)
		return existingTask, nil
	}

	// Coder / agent task:
	if existingTask.SessionID == "" {
		if err := s.deployProjectTaskExecution(p, proj, existingTask, "in_progress", existingTask.Title); err != nil {
			return nil, fmt.Errorf("deploy agent execution: %w", err)
		}
		_ = db.PutProjectTask(p.AccountScopeID, existingTask)
		hydrateTaskPlanDocument(existingTask, db)
		return existingTask, nil
	}

	// Verify worktree isolation for Coder
	if existingTask.Agent == "coder" || existingTask.OutcomeType == "code_pr" || existingTask.OutcomeType == "bug_patch" {
		session, sessFound, _ := db.GetSession(existingTask.SessionID)
		if sessFound && (!session.WorktreeEnabled || session.WorktreeRootPath == "") {
			if s.worktrees == nil {
				return nil, errors.New("worktree service is not configured; coder task requires worktree isolation")
			}
			if existingTask.WorkspacePath == "." || existingTask.WorkspacePath == "" {
				return nil, errors.New("coder task requires a valid repository workspace path (dot '.' is not allowed)")
			}
			alloc, allocErr := s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(p, existingTask.WorkspacePath, existingTask.SessionID, "", existingTask.WorktreeBranch)
			if allocErr != nil || alloc.WorkspacePath == "" {
				return nil, fmt.Errorf("coder worktree allocation failed: %w", allocErr)
			}
			existingTask.WorkspacePath = alloc.WorkspacePath
			existingTask.WorktreeBranch = alloc.BranchName
			existingTask.BaseBranch = alloc.BaseBranch
			existingTask.BaseCommit = alloc.BaseCommit
			existingTask.WorktreeName = strings.TrimPrefix(alloc.BranchName, "agent/")
		}
	}

	now := time.Now().UnixMilli()
	runID := fmt.Sprintf("desktop-v3-run:task-%s", existingTask.ID)
	parentSessionID := proj.PrimarySessionID
	runIntent := &pebblestore.V3SessionRunIntent{
		SessionID:       existingTask.SessionID,
		RunID:           runID,
		EpochID:         "epoch-00000000000000000001",
		UserID:          p.UserID,
		AccountScopeID:  p.AccountScopeID,
		ParentSessionID: parentSessionID,
		Status:          pebblestore.V3RunIntentPendingExecutor,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	runKey := fmt.Sprintf("project-task:run:%s:%s", existingTask.ID, runID)
	_, mutationErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       existingTask.SessionID,
		UserID:          p.UserID,
		AccountScopeID:  p.AccountScopeID,
		ClientRequestID: runKey,
		IdempotencyKey:  runKey,
		PayloadHash:     runKey,
		RequestHash:     runKey,
		Kind:            sessionruntime.SessionMutationStartRun,
		RunIntent:       runIntent,
		NowUnixMs:       now,
	})
	if mutationErr != nil {
		return nil, fmt.Errorf("start session run intent: %w", mutationErr)
	}
	s.EnqueueSessionRun(p, existingTask.SessionID, runID, parentSessionID)

	updatedTask, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		t.Status = "in_progress"
		t.ActionNeeded = ""
		t.WhatDidDo = append(t.WhatDidDo, "Task execution started")
		return nil
	})
	if err != nil {
		return nil, err
	}
	hydrateTaskPlanDocument(updatedTask, db)
	hydrateTaskProgramStatus(updatedTask, db)
	return updatedTask, nil
}

// DeployProjectTask deploys a project task execution or standalone Task Program for the authenticated principal.
func (s *Server) DeployProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string) error {
	if !p.Valid() || p.Type != "user" || strings.TrimSpace(p.UserID) == "" || strings.TrimSpace(p.AccountScopeID) == "" {
		return errors.New("deploy task: user id is required")
	}
	db := s.sessions.Store()
	if db == nil {
		return errors.New("database not available")
	}
	proj, found, err := db.GetProject(p.AccountScopeID, projectID)
	if err != nil {
		return err
	}
	if !found || proj == nil {
		return fmt.Errorf("project %q not found", projectID)
	}
	task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		return err
	}
	if !found || task == nil {
		return fmt.Errorf("task %q not found", taskID)
	}
	if task.Status == "pending_approval" {
		return errors.New("cannot deploy task awaiting approval; approve task before deployment")
	}
	if task.Status == "rejected" {
		return errors.New("cannot deploy rejected task")
	}
	if task.Status == "completed" {
		return nil
	}

	// Idempotent retry: if active run intent exists, avoid duplicate runs
	if task.Status == "in_progress" && task.SessionID != "" {
		activeIntent, ok, _ := db.GetV3SessionActiveRunIntent(task.SessionID)
		if ok && activeIntent != nil && (activeIntent.Status == pebblestore.V3RunIntentPendingExecutor || activeIntent.Status == pebblestore.V3RunIntentRunning) {
			return nil
		}
	}

	task.Status = "in_progress"
	task.ActionNeeded = ""
	if task.TaskProgram != nil || task.TaskProgramID != "" {
		if err := s.deployProjectTaskProgram(p, proj, task); err != nil {
			return err
		}
		return nil
	} else if task.Agent == "image" || task.Agent == "video" || task.Agent == "sound" || task.Agent == "audio" {
		if err := s.deployProjectTaskExecution(p, proj, task, "in_progress", ""); err != nil {
			return err
		}
	} else {
		if task.SessionID == "" {
			if err := s.deployProjectTaskExecution(p, proj, task, "in_progress", task.Title); err != nil {
				return err
			}
		} else {
			now := time.Now().UnixMilli()
			runID := fmt.Sprintf("desktop-v3-run:task-%s", task.ID)
			runIntent := &pebblestore.V3SessionRunIntent{
				SessionID:       task.SessionID,
				RunID:           runID,
				EpochID:         "epoch-00000000000000000001",
				UserID:          p.UserID,
				AccountScopeID:  p.AccountScopeID,
				ParentSessionID: proj.PrimarySessionID,
				Status:          pebblestore.V3RunIntentPendingExecutor,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			runKey := fmt.Sprintf("project-task:run:%s:%s", task.ID, runID)
			_, mutationErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
				SessionID:       task.SessionID,
				UserID:          p.UserID,
				AccountScopeID:  p.AccountScopeID,
				ClientRequestID: runKey,
				IdempotencyKey:  runKey,
				PayloadHash:     runKey,
				RequestHash:     runKey,
				Kind:            sessionruntime.SessionMutationStartRun,
				RunIntent:       runIntent,
				NowUnixMs:       now,
			})
			if mutationErr != nil {
				return fmt.Errorf("start session run intent: %w", mutationErr)
			}
			s.EnqueueSessionRun(p, task.SessionID, runID, proj.PrimarySessionID)
		}
	}
	return db.PutProjectTask(p.AccountScopeID, task)
}

// DeployProjectTaskForPrincipal deploys a project task execution or standalone Task Program for the authenticated principal.
func (s *Server) DeployProjectTaskForPrincipal(p identity.Principal, projectID, taskID string) error {
	return s.DeployProjectTask(context.Background(), p, projectID, taskID)
}

// SubmitProjectTaskPlan submits a structured plan into the canonical project task plan lifecycle.
func (s *Server) SubmitProjectTaskPlan(ctx context.Context, input sessionruntime.ProjectTaskPlanSubmissionInput) (sessionruntime.ProjectTaskPlanSubmissionResult, error) {
	if s == nil || s.sessions == nil {
		return sessionruntime.ProjectTaskPlanSubmissionResult{}, errors.New("session service not available")
	}
	lifecycle := sessionruntime.NewPlanLifecycleService(s.sessions)
	lifecycle.SetApplySessionMutation(s.sessions.ApplySessionMutation)
	return lifecycle.SubmitProjectTaskStructuredPlan(input)
}
