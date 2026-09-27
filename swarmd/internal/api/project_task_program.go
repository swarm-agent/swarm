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

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
		if wsPath == "" {
			wsPath = "."
			task.WorkspacePath = wsPath
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
		_, _ = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
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

	// 3. Start canonical Task Program scheduler through runner
	if s.runner != nil {
		runID := fmt.Sprintf("desktop-v3-run:%s", sessionruntime.NewSessionID())
		go func() {
			_, _ = s.runner.ExecuteTaskProgramForCoordinator(context.Background(), p, task.SessionID, runID, record)
		}()
	}
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
	if s.runner != nil {
		runID := fmt.Sprintf("desktop-v3-run:%s", sessionruntime.NewSessionID())
		go func() {
			_, _ = s.runner.ExecuteTaskProgramForCoordinator(context.Background(), p, task.SessionID, runID, updatedRecord)
		}()
	}
	return nil
}

// DeployProjectTaskForPrincipal deploys a project task execution or standalone Task Program for the authenticated principal.
func (s *Server) DeployProjectTaskForPrincipal(p identity.Principal, projectID, taskID string) error {
	if !p.Valid() || p.Type != "user" || p.UserID == "" || p.AccountScopeID == "" {
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
			// Session already exists; start execution run
			now := time.Now().UnixMilli()
			runID := fmt.Sprintf("desktop-v3-run:%s", sessionruntime.NewSessionID())
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
			_, _ = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
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
			s.EnqueueSessionRun(p, task.SessionID, runID, proj.PrimarySessionID)
		}
	}
	return db.PutProjectTask(p.AccountScopeID, task)
}

// DeployProjectTask deploys a project task execution, looking up the authentic user principal from the account scope.
// It never invents identities; if no authentic user is found in the identity store, it fails closed.
func (s *Server) DeployProjectTask(accountScopeID, projectID, taskID string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return errors.New("accountScopeID is required")
	}
	var userID string
	if s.auth != nil && s.auth.Store() != nil {
		if scope, ok, err := s.auth.Store().GetAccountScope(accountScopeID); err == nil && ok {
			userID = strings.TrimSpace(scope.UserID)
			if userID == "" {
				userID = strings.TrimSpace(scope.CreatedByUserID)
			}
		}
	}
	if userID == "" {
		return errors.New("deploy task: user id is required")
	}
	p := identity.Principal{
		Type:           "user",
		UserID:         userID,
		AccountScopeID: accountScopeID,
	}
	return s.DeployProjectTaskForPrincipal(p, projectID, taskID)
}
