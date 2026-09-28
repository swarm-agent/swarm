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
	runruntime "swarm/packages/swarmd/internal/run"
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

// hydrateTaskPlanDocument populates PlanDocument from Pebble if an exact PlanBinding exists.
// Hydration must honor exact PlanBinding.SessionID, PlanID, and DefinitionRevision, and never
// attach an unrelated active plan.
func hydrateTaskPlanDocument(task *pebblestore.ProjectTaskRecord, db *pebblestore.SessionStore) {
	if task == nil || db == nil {
		return
	}
	if task.PlanBinding == nil || task.PlanBinding.PlanID == "" {
		return
	}
	sessID := task.PlanBinding.SessionID
	if sessID == "" {
		sessID = task.SessionID
	}
	if sessID == "" {
		return
	}
	if plan, ok, _ := db.GetPlan(sessID, task.PlanBinding.PlanID); ok && plan.Document != nil {
		if task.PlanBinding.DefinitionRevision > 0 {
			if plan.ApprovalState == "approved" {
				if task.PlanBinding.Receipt != "" && plan.AcceptedDefinitionReceipt != "" && plan.AcceptedDefinitionReceipt != task.PlanBinding.Receipt {
					return
				}
			} else if plan.Version > 0 && plan.Version != task.PlanBinding.DefinitionRevision {
				return
			}
		}
		task.PlanDocument = plan.Document
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

// CreateProjectTask implements the canonical shared creation pipeline for project tasks.
func (s *Server) CreateProjectTask(ctx context.Context, p identity.Principal, projectID string, input tool.ProjectTaskCreateInput) (*pebblestore.ProjectTaskRecord, error) {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || strings.TrimSpace(p.UserID) == "" || strings.TrimSpace(p.AccountScopeID) == "" {
		return nil, errors.New("user id is required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("project id is required")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = strings.TrimSpace(input.Prompt)
	}
	if title == "" {
		title = strings.TrimSpace(input.Description)
	}
	if title == "" {
		return nil, errors.New("title is required")
	}
	db := s.sessions.Store()
	if db == nil {
		return nil, errors.New("database not available")
	}
	proj, found, err := db.GetProject(p.AccountScopeID, projectID)
	if err != nil {
		return nil, err
	}
	if !found || proj == nil {
		return nil, fmt.Errorf("project %q not found", projectID)
	}
	if proj.AccountID != "" && proj.AccountID != p.AccountScopeID {
		return nil, errors.New("cross-account project access forbidden")
	}

	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(input.Description)
	}
	if prompt == "" {
		prompt = title
	}
	wsPath := strings.TrimSpace(input.WorkspacePath)
	if wsPath == "" && len(proj.Workspaces) > 0 {
		wsPath = strings.TrimSpace(proj.Workspaces[0].Path)
	}
	agentName := strings.TrimSpace(input.Agent)
	if (agentName == "coder" || input.OutcomeType == "code_pr" || input.OutcomeType == "bug_patch") && (wsPath == "" || wsPath == ".") {
		return nil, errors.New("coder task requires a valid repository workspace path (dot '.' is not allowed)")
	}

	structDoc := input.Document
	if structDoc == nil && input.PlanDocument != nil {
		structDoc = input.PlanDocument
	}
	taskProg := input.TaskProgram
	// Program-only proposals use the same executable-plan coordinator authority.
	// The legacy bare coordinator lacks an agent profile and owned integration lane.
	if structDoc == nil && taskProg != nil {
		if err := pebblestore.ValidateTaskProgramDefinition(taskProg); err != nil {
			return nil, err
		}
		structDoc = &pebblestore.SessionPlanDocument{Title: title, Info: pebblestore.SessionPlanInfo{Goal: prompt}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: title, Tasks: []string{"Execute the proposed task program and verify its deliverables"}, AcceptanceCriteria: []string{"All declared jobs deliver their accepted committed outputs"}, TaskProgram: taskProg}}}
	}
	featureSize := strings.TrimSpace(input.FeatureSize)
	outcomeType := strings.TrimSpace(input.OutcomeType)
	tier := strings.TrimSpace(input.Tier)
	if structDoc != nil {
		if err := sessionruntime.ValidateExecutablePlanDocument(structDoc); err != nil {
			return nil, err
		}
		if taskProg != nil {
			bound := false
			for _, cp := range structDoc.Checkpoints {
				if cp.TaskProgram != nil && computeTaskProgramDefinitionHash(*cp.TaskProgram) == computeTaskProgramDefinitionHash(*taskProg) {
					bound = true
				}
			}
			if !bound {
				return nil, errors.New("task_program must be embedded in the executable plan checkpoint; a separate top-level program is not executed by plan approval")
			}
		}
		if featureSize == "" {
			featureSize = "big"
		}
		if outcomeType == "" {
			outcomeType = "plan_spec"
		}
		if tier == "" {
			tier = "complex"
		}
		if agentName == "" {
			agentName = "swarm"
		}
	}

	routed, rErr := pebblestore.RouteAndPlanProjectTaskWithOptions(pebblestore.TaskPlanOptions{
		Prompt:             prompt,
		RequestedWorkspace: wsPath,
		ProjectContext:     proj.ProjectContext,
		Workspaces:         proj.Workspaces,
		FeatureSize:        featureSize,
		Agent:              agentName,
		OutcomeType:        outcomeType,
		Tier:               tier,
		AspectRatio:        input.AspectRatio,
		VariantCount:       input.VariantCount,
		Soundtrack:         input.Soundtrack,
		AttachedMedia:      input.AttachedMedia,
	})
	if rErr != nil {
		return nil, fmt.Errorf("task configuration invalid: %w", rErr)
	}
	if agentName == "" {
		agentName = routed.Agent
	}
	if outcomeType == "" {
		outcomeType = routed.OutcomeType
	}
	if tier == "" {
		tier = routed.Tier
	}
	aspectRatio := input.AspectRatio
	if aspectRatio == "" {
		aspectRatio = routed.AspectRatio
	}
	variantCount := input.VariantCount
	if variantCount <= 0 {
		variantCount = routed.VariantCount
	}
	soundtrack := input.Soundtrack
	if soundtrack == "" {
		soundtrack = routed.Soundtrack
	}
	workerName := strings.TrimSpace(input.WorkerName)
	if workerName == "" {
		workerName = fmt.Sprintf("@%s Worker", strings.Title(agentName))
	}
	worktreeBranch := strings.TrimSpace(input.WorktreeBranch)
	if worktreeBranch == "" {
		worktreeBranch = routed.Branch
	}
	description := strings.TrimSpace(input.Description)
	if description == "" {
		description = routed.Mission
	}
	stages := input.PipelineStages
	if len(stages) == 0 {
		stages = routed.Stages
	}
	deliverables := input.Deliverables
	if len(deliverables) == 0 {
		deliverables = routed.Deliverables
	}
	planSummary := strings.TrimSpace(input.PlanSummary)
	if planSummary == "" {
		planSummary = routed.PlanSummary
	}
	fullPlanMarkdown := strings.TrimSpace(input.FullPlanMarkdown)
	if fullPlanMarkdown == "" {
		fullPlanMarkdown = routed.FullPlanMarkdown
	}
	taskProgID := strings.TrimSpace(input.TaskProgramID)
	if taskProg != nil {
		if taskProg.ID == "" {
			taskProg.ID = fmt.Sprintf("prog-%d", time.Now().UnixMilli())
		}
		if taskProgID == "" {
			taskProgID = taskProg.ID
		}
	} else if routed.TaskProgram != nil {
		taskProg = routed.TaskProgram
		taskProgID = taskProg.ID
	}

	taskID := strings.TrimSpace(input.ID)
	if taskID == "" {
		taskID = fmt.Sprintf("task_%d", time.Now().UnixMilli())
	}

	// User-selected branch names retain conflict checks. Generated names must be
	// task-owned: identical prompts are valid independent tasks, not a collision.
	if strings.TrimSpace(input.WorktreeBranch) == "" && worktreeBranch != "" {
		suffix := sha256.Sum256([]byte(projectID + ":" + taskID))
		worktreeBranch += "-" + hex.EncodeToString(suffix[:4])
	}
	s.projectTaskCreateMu.Lock()
	defer s.projectTaskCreateMu.Unlock()

	// Check if already exists (idempotent create)
	if existing, found, getErr := db.GetProjectTask(p.AccountScopeID, projectID, taskID); getErr == nil && found && existing != nil {
		hydrateTaskPlanDocument(existing, db)
		hydrateTaskProgramStatus(existing, db)
		return existing, nil
	}

	isDirectMedia := agentName == "image" || agentName == "video" || agentName == "sound" || agentName == "audio"
	sessionID := strings.TrimSpace(input.SessionID)
	if !isDirectMedia && sessionID == "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("task-session:%s:%s:%s", p.AccountScopeID, projectID, taskID)))
		sessionID = hex.EncodeToString(sum[:16])
	}

	task := pebblestore.ProjectTaskRecord{
		ID:                 taskID,
		ProjectID:          projectID,
		AccountID:          p.AccountScopeID,
		Title:              title,
		Description:        description,
		SessionID:          sessionID,
		Agent:              agentName,
		WorkerName:         workerName,
		OutcomeType:        outcomeType,
		WorkspacePath:      wsPath,
		WorktreeBranch:     worktreeBranch,
		PipelineStages:     stages,
		Deliverables:       deliverables,
		WorkspacesInvolved: routed.WorkspacesInvolved,
		ContextPoolSummary: routed.ContextPoolSummary,
		PlanSummary:        planSummary,
		FullPlanMarkdown:   fullPlanMarkdown,
		Tier:               tier,
		FeatureSize:        featureSize,
		Revision:           1,
		AspectRatio:        aspectRatio,
		Resolution:         input.Resolution,
		VariantCount:       variantCount,
		DurationSeconds:    input.DurationSeconds,
		Model:              strings.TrimSpace(input.Model),
		Provider:           strings.TrimSpace(input.Provider),
		Thinking:           strings.TrimSpace(input.Thinking),
		ServiceTier:        strings.TrimSpace(input.ServiceTier),
		ContextMode:        strings.TrimSpace(input.ContextMode),
		Scenes:             routed.Scenes,
		Soundtrack:         soundtrack,
		AutoApprove:        input.AutoApprove,
		RouterAlert:        routed.RouterAlert,
		AttachedMedia:      input.AttachedMedia,
		TaskProgram:        taskProg,
		TaskProgramID:      taskProgID,
		CreatedAt:          time.Now().UnixMilli(),
		UpdatedAt:          time.Now().UnixMilli(),
	}
	if len(task.AttachedMedia) == 0 && len(routed.AttachedMedia) > 0 {
		task.AttachedMedia = routed.AttachedMedia
	}
	if task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" {
		task.AspectRatio = ""
		task.VariantCount = 0
	}

	if structDoc != nil {
		task.Status = "pending_approval"
		task.ActionNeeded = "Review plan in task card and click Approve"
	} else if task.Agent == "plan" || (task.Agent == "swarm" && task.FeatureSize == "big") {
		task.Status = "planning"
		task.ActionNeeded = "Plan agent investigating and authoring structured plan..."
		task.WhatDidDo = []string{"Started planning investigation"}
	} else if task.TaskProgram == nil && (task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch") {
		task.Status = "pending_approval"
		task.ActionNeeded = "Review task and click Approve to start Coder execution"
		if input.AutoApprove {
			task.Status = "in_progress"
			task.ActionNeeded = ""
		}
	} else if task.TaskProgram != nil {
		if input.AutoApprove {
			task.Status = "in_progress"
		} else {
			task.Status = "pending_approval"
			task.ActionNeeded = "Review task program and click Approve"
		}
	} else if isDirectMedia {
		if input.AutoApprove {
			task.Status = "in_progress"
		} else {
			task.Status = "pending_approval"
			task.ActionNeeded = "Review media task and click Approve"
		}
	} else {
		if input.AutoApprove {
			task.Status = "in_progress"
		} else {
			task.Status = "pending_approval"
		}
	}

	if err := task.Validate(); err != nil {
		return nil, fmt.Errorf("invalid task definition: %w", err)
	}

	// Persist task reservation FIRST
	if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
		return nil, err
	}
	_, _ = db.UpdateProject(p.AccountScopeID, projectID, func(pr *pebblestore.ProjectRecord) error {
		for _, tid := range pr.ActiveTaskIDs {
			if tid == task.ID {
				return nil
			}
		}
		pr.ActiveTaskIDs = append(pr.ActiveTaskIDs, task.ID)
		return nil
	})

	// Deploy execution
	if structDoc != nil {
		// Supplied plans need the same authenticated isolated integration lane as
		// agent-authored plans, without starting an unapproved provider run.
		if err := s.deployProjectTaskExecution(p, proj, &task, "pending_approval", prompt); err != nil {
			return nil, fmt.Errorf("prepare supplied-plan session: %w", err)
		}
		if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
			return nil, err
		}
		subResult, sErr := s.SubmitProjectTaskPlan(ctx, sessionruntime.ProjectTaskPlanSubmissionInput{
			AccountScopeID:  p.AccountScopeID,
			UserID:          p.UserID,
			ProjectID:       projectID,
			TaskID:          task.ID,
			Document:        structDoc,
			PlanText:        fullPlanMarkdown,
			Title:           title,
			WorkspacePath:   task.WorkspacePath,
			ParentSessionID: proj.PrimarySessionID,
		})
		if sErr != nil {
			return nil, fmt.Errorf("submit structured plan: %w", sErr)
		}
		task = subResult.Task
	} else if task.Status == "planning" {
		if err := s.deployProjectTaskExecution(p, proj, &task, "in_progress", prompt); err != nil {
			return nil, fmt.Errorf("deploy planning session: %w", err)
		}
		_ = db.PutProjectTask(p.AccountScopeID, &task)
	} else if task.TaskProgram == nil && (task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch") {
		if err := s.deployProjectTaskExecution(p, proj, &task, task.Status, prompt); err != nil {
			return nil, fmt.Errorf("deploy coder session: %w", err)
		}
		_ = db.PutProjectTask(p.AccountScopeID, &task)
	} else if task.TaskProgram != nil {
		if task.Status == "in_progress" {
			if err := s.deployProjectTaskProgram(p, proj, &task); err != nil {
				return nil, fmt.Errorf("deploy task program: %w", err)
			}
		}
		_ = db.PutProjectTask(p.AccountScopeID, &task)
	} else if isDirectMedia {
		if task.Status == "in_progress" {
			if err := s.deployProjectTaskExecution(p, proj, &task, "in_progress", prompt); err != nil {
				return nil, fmt.Errorf("deploy media execution: %w", err)
			}
		}
		_ = db.PutProjectTask(p.AccountScopeID, &task)
	} else {
		if task.Status == "in_progress" {
			if err := s.deployProjectTaskExecution(p, proj, &task, "in_progress", prompt); err != nil {
				return nil, fmt.Errorf("deploy task execution: %w", err)
			}
		}
		_ = db.PutProjectTask(p.AccountScopeID, &task)
	}

	freshTask, found, _ := db.GetProjectTask(p.AccountScopeID, projectID, task.ID)
	if found && freshTask != nil {
		hydrateTaskPlanDocument(freshTask, db)
		hydrateTaskProgramStatus(freshTask, db)
		return freshTask, nil
	}
	hydrateTaskPlanDocument(&task, db)
	hydrateTaskProgramStatus(&task, db)
	return &task, nil
}

// deployProjectTaskProgram initializes and deploys a TaskProgram on a project task coordinator session.
func (s *Server) deployProjectTaskProgram(p identity.Principal, proj *pebblestore.ProjectRecord, task *pebblestore.ProjectTaskRecord) error {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || p.UserID == "" || p.AccountScopeID == "" {
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
	progID := strings.TrimSpace(task.TaskProgram.ID)
	if progID == "" {
		progID = fmt.Sprintf("prog_%s", task.ID)
		task.TaskProgram.ID = progID
	}
	if err := pebblestore.ValidateTaskProgramDefinition(task.TaskProgram); err != nil {
		return fmt.Errorf("task program validation failed: %w", err)
	}

	// 1. Ensure coordinator session exists
	now := time.Now().UnixMilli()
	_, sessionExists, sessionErr := db.GetSession(task.SessionID)
	if sessionErr != nil {
		return sessionErr
	}
	if !sessionExists {
		sessionID := task.SessionID
		if sessionID == "" {
			return errors.New("task coordinator requires a reserved session identity")
		}
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

		createKey := fmt.Sprintf("project-task:coordinator:%s", sessionID)
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
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		RunIntent:       runIntent,
		NowUnixMs:       now,
	})
	if mutationErr != nil {
		return fmt.Errorf("start coordinator session run intent: %w", mutationErr)
	}

	if s.runner == nil {
		return errors.New("runner service is not configured")
	}
	s.EnqueueSessionRun(p, task.SessionID, runID, parentSessionID)
	return nil
}

// redeployTaskProgramJob redeploys a conflicted or failed job within the task program.
func (s *Server) redeployTaskProgramJob(p identity.Principal, projectID, taskID, jobID, feedback string) error {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || p.UserID == "" || p.AccountScopeID == "" {
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
		t.LastError = ""
		t.ActionNeeded = fmt.Sprintf("Redeploying job %s (Attempt %d)...", jobID, newAttempt)
		if fb != "" {
			t.FeedbackHistory = append(t.FeedbackHistory, fmt.Sprintf("Job %s retry feedback: %s", jobID, fb))
			t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Redeployed job %s with conflict resolution instructions", jobID))
		} else {
			t.WhatDidDo = append(t.WhatDidDo, fmt.Sprintf("Redeployed job %s (Attempt %d)", jobID, newAttempt))
		}
		return nil
	})

	runID := fmt.Sprintf("desktop-v3-run:tp-%s-retry-%d", task.ID, newAttempt)
	parentSessionID := ""
	if proj, pFound, _ := db.GetProject(p.AccountScopeID, projectID); pFound && proj != nil {
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
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		RunIntent:       runIntent,
		NowUnixMs:       now,
	})
	if mutationErr != nil {
		return fmt.Errorf("start retry run intent: %w", mutationErr)
	}
	if s.runner == nil {
		return errors.New("runner service is not configured")
	}
	s.EnqueueSessionRun(p, task.SessionID, runID, parentSessionID)
	return nil
}

// ApproveProjectTask implements the single canonical authenticated approval lifecycle for project tasks.
func (s *Server) ApproveProjectTask(ctx context.Context, p identity.Principal, projectID, taskID string, guards ...tool.ProjectTaskApprovalGuards) (*pebblestore.ProjectTaskRecord, error) {
	s.projectTaskApproveMu.Lock()
	defer s.projectTaskApproveMu.Unlock()

	if !p.Valid() || p.Type != identity.PrincipalTypeUser || strings.TrimSpace(p.UserID) == "" || strings.TrimSpace(p.AccountScopeID) == "" {
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
	hasGuards := len(guards) == 1 && strings.TrimSpace(guards[0].SessionID) != "" && strings.TrimSpace(guards[0].PlanID) != "" && guards[0].DefinitionRevision > 0
	if hasGuards {
		guard = guards[0]
	}

	if existingTask.Status == "planning" && (existingTask.PlanBinding == nil || existingTask.PlanBinding.PlanID == "") {
		return nil, errors.New("cannot approve task in planning without a submitted structured plan")
	}

	if existingTask.PlanBinding != nil && existingTask.PlanBinding.PlanID != "" {
		if !hasGuards {
			return nil, errors.New("exact session, plan and definition revision guards are required for plan acceptance")
		}
		planID := existingTask.PlanBinding.PlanID
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

		if hasGuards {
			if strings.TrimSpace(guard.SessionID) == "" || guard.SessionID != existingTask.SessionID {
				return nil, fmt.Errorf("session ID mismatch: expected %q, got %q", existingTask.SessionID, guard.SessionID)
			}
			if strings.TrimSpace(guard.PlanID) == "" || guard.PlanID != planID {
				return nil, fmt.Errorf("plan ID mismatch: expected %q, got %q", planID, guard.PlanID)
			}
			if plan.ApprovalState == "approved" {
				if guard.DefinitionRevision <= 0 || existingTask.PlanBinding.DefinitionRevision != guard.DefinitionRevision || existingTask.PlanBinding.Receipt == "" || plan.AcceptedDefinitionReceipt != existingTask.PlanBinding.Receipt {
					return nil, fmt.Errorf("plan definition is stale (guarded revision %d, current %d)", guard.DefinitionRevision, existingTask.PlanBinding.DefinitionRevision)
				}
			} else {
				if guard.DefinitionRevision <= 0 || plan.Version != guard.DefinitionRevision || existingTask.PlanBinding.DefinitionRevision != guard.DefinitionRevision {
					return nil, fmt.Errorf("plan definition is stale (guarded revision %d, current %d)", guard.DefinitionRevision, existingTask.PlanBinding.DefinitionRevision)
				}
			}
		}
		if plan.ApprovalState != "approved" && existingTask.PlanBinding.DefinitionRevision > 0 && plan.Version != existingTask.PlanBinding.DefinitionRevision {
			return nil, fmt.Errorf("plan definition is stale (task revision %d, current %d)", existingTask.PlanBinding.DefinitionRevision, plan.Version)
		}
	} else if hasGuards {
		if guard.SessionID != "" && existingTask.SessionID != "" && guard.SessionID != existingTask.SessionID {
			return nil, fmt.Errorf("session ID mismatch: expected %q, got %q", existingTask.SessionID, guard.SessionID)
		}
		if guard.PlanID != "" {
			return nil, errors.New("plan ID mismatch: task has no plan binding")
		}
	}

	// Idempotent retry check: if task is in_progress AND execution run is genuinely active, do not duplicate run!
	if existingTask.Status == "in_progress" && existingTask.SessionID != "" && existingTask.PlanBinding == nil {
		activeIntent, ok, _ := db.GetV3SessionActiveRunIntent(existingTask.SessionID)
		if ok && (activeIntent.Status == pebblestore.V3RunIntentPendingExecutor || activeIntent.Status == pebblestore.V3RunIntentRunning) {
			hydrateTaskPlanDocument(existingTask, db)
			hydrateTaskProgramStatus(existingTask, db)
			return existingTask, nil
		}
	}

	if existingTask.PlanBinding != nil && existingTask.PlanBinding.PlanID != "" {
		planID := existingTask.PlanBinding.PlanID
		plan, _, _ := db.GetPlan(existingTask.SessionID, planID)

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
		if s.agents == nil || pref.Provider == "" || pref.Model == "" {
			return nil, errors.New("configured Swarm execution model and agent service are required")
		}
		swarmProfile, profileErr := s.agents.ResolveSystemAgent(agentruntime.SwarmAgentID, pebblestore.AgentProfile{
			Provider: pref.Provider, Model: pref.Model, Thinking: pref.Thinking,
			AutoServiceTier: pref.ServiceTier, ContextMode: pref.ContextMode,
		})
		if profileErr != nil {
			return nil, fmt.Errorf("resolve Swarm execution profile: %w", profileErr)
		}
		summary := sessionruntime.SummarizePlanExecution(plan.Document)
		firstCpID := summary.NextCheckpointID
		if firstCpID == "" && plan.Document != nil && len(plan.Document.Checkpoints) > 0 {
			firstCpID = plan.Document.Checkpoints[0].ID
		}
		if firstCpID == "" {
			return nil, errors.New("plan has no checkpoints to execute")
		}

		lifecycleMsg, _ := runruntime.BuildPlanExecutionLifecycleSystemMessage(runruntime.PlanExecutionLifecycleMessageInput{
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
			expectedRev := existingTask.PlanBinding.DefinitionRevision
			if guard.DefinitionRevision > 0 {
				expectedRev = guard.DefinitionRevision
			}
			originalReceipt := existingTask.PlanBinding.Receipt
			committed, commitErr := s.sessions.CommitV3PlanAcceptance(sessionruntime.PlanAcceptanceCommitInput{
				Session:              session,
				PlanID:               plan.ID,
				Title:                plan.Title,
				Plan:                 plan.Plan,
				Document:             plan.Document,
				ApplySessionMutation: s.sessions.ApplySessionMutation,
				ModeEventFields: map[string]any{
					"preference": pref,
				},
				ModePreference:            pref,
				ModeAgentProfile:          &swarmProfile,
				ExpectedBindingRevision:   expectedRev,
				ExpectedReceipt:           originalReceipt,
				AcceptedDefinitionReceipt: originalReceipt,
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
			receipt = committed.Plan.AcceptedDefinitionReceipt
		} else if existingTask.PlanBinding != nil {
			receipt = existingTask.PlanBinding.Receipt
		}

		if active, ok, err := db.GetV3SessionActiveRunIntent(existingTask.SessionID); err != nil {
			return nil, err
		} else if ok && active.PlanID == plan.ID && (active.Status == pebblestore.V3RunIntentPendingExecutor || active.Status == pebblestore.V3RunIntentRunning) {
			reconciled, err := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
				t.Status, t.Agent, t.ActionNeeded = "in_progress", "swarm", ""
				return nil
			})
			if err != nil {
				return nil, err
			}
			hydrateTaskPlanDocument(reconciled, db)
			return reconciled, nil
		}
		if s.planLifecycle == nil {
			return nil, errors.New("plan lifecycle service is not configured")
		}
		var attemptID, checkpointID string
		if plan.Document != nil && plan.Document.ExecutionState != nil && plan.Document.ExecutionState.Status == sessionruntime.PlanExecutionStateInProgress {
			attemptID = plan.Document.ExecutionState.ActiveAttemptID
			checkpointID = plan.Document.ActiveCheckpointID
		} else {
			runInput, inputErr := s.sessionsV3PlanModeRunInput(existingTask.SessionID, plan.ID, firstCpID)
			if inputErr != nil {
				return nil, inputErr
			}
			startResult, startErr := s.planLifecycle.ApproveAndStartPlanAutomatic(runInput)
			if startErr != nil {
				return nil, fmt.Errorf("approve and start plan: %w", startErr)
			}
			plan = startResult.Plan
			attemptID, checkpointID = startResult.AttemptID, startResult.CheckpointID
		}
		if attemptID == "" || checkpointID == "" {
			return nil, errors.New("approved plan has no durable checkpoint attempt")
		}
		runID := sessionsV3PlanModeRunID(existingTask.SessionID, plan.ID, checkpointID, attemptID)

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
			CheckpointID:    checkpointID,
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
			Kind:            sessionruntime.SessionMutationRecordRunIntent,
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
		existingTask.Status, existingTask.ActionNeeded = "in_progress", ""
		if err := db.PutProjectTask(p.AccountScopeID, existingTask); err != nil {
			return nil, err
		}
		hydrateTaskPlanDocument(existingTask, db)
		return existingTask, nil
	}

	// A reserved session ID does not prove that creation succeeded.
	_, sessionFound, sessionErr := db.GetSession(existingTask.SessionID)
	if sessionErr != nil {
		return nil, sessionErr
	}
	// Coder / agent task:
	if !sessionFound {
		if err := s.deployProjectTaskExecution(p, proj, existingTask, "in_progress", existingTask.Title); err != nil {
			return nil, fmt.Errorf("deploy agent execution: %w", err)
		}
		existingTask.Status, existingTask.ActionNeeded = "in_progress", ""
		if err := db.PutProjectTask(p.AccountScopeID, existingTask); err != nil {
			return nil, err
		}
		hydrateTaskPlanDocument(existingTask, db)
		return existingTask, nil
	}

	// Verify worktree isolation for Coder
	if existingTask.Agent == "coder" || existingTask.OutcomeType == "code_pr" || existingTask.OutcomeType == "bug_patch" {
		session, sessFound, _ := db.GetSession(existingTask.SessionID)
		if !sessFound {
			return nil, fmt.Errorf("session %q not found", existingTask.SessionID)
		}
		if !session.WorktreeEnabled || session.WorktreeRootPath == "" {
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

			// Persist updated worktree metadata to the canonical session record in Pebble
			session.WorktreeEnabled = true
			session.WorktreeRootPath = strings.TrimSpace(alloc.WorkspacePath)
			session.WorktreeBaseBranch = strings.TrimSpace(alloc.BaseBranch)
			session.WorktreeBranch = strings.TrimSpace(alloc.BranchName)
			if session.Metadata == nil {
				session.Metadata = make(map[string]any)
			}
			session.Metadata["base_commit"] = alloc.BaseCommit
			session.Metadata["swarm_v3_source_workspace_path"] = alloc.RepoRoot
			session.Metadata["worktree_branch"] = alloc.BranchName
			session.Metadata["worktree_name"] = strings.TrimPrefix(alloc.BranchName, "agent/")
			available := true
			hasGrant := false
			for _, g := range session.WorkspaceGrants {
				if g.Kind == pebblestore.WorkspaceGrantWorktree && g.Path == alloc.WorkspacePath {
					hasGrant = true
					break
				}
			}
			if !hasGrant {
				session.WorkspaceGrants = append(session.WorkspaceGrants, pebblestore.WorkspaceGrant{
					Kind: pebblestore.WorkspaceGrantWorktree, Path: alloc.WorkspacePath, Available: &available,
				})
			}
			session.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(session.WorkspaceGrants)
			session.UpdatedAt = time.Now().UnixMilli()
			key := fmt.Sprintf("project-task:worktree:%s:%s", existingTask.ID, session.ID)
			if _, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
				SessionID: session.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
				ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
				Kind: sessionruntime.SessionMutationUpdateSettings, Session: &session,
			}); err != nil {
				return nil, fmt.Errorf("update session worktree metadata: %w", err)
			}
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
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
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
		t.WorkspacePath = existingTask.WorkspacePath
		t.WorktreeBranch = existingTask.WorktreeBranch
		t.BaseBranch = existingTask.BaseBranch
		t.BaseCommit = existingTask.BaseCommit
		t.WorktreeName = existingTask.WorktreeName
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
	s.projectTaskApproveMu.Lock()
	defer s.projectTaskApproveMu.Unlock()
	return s.deployProjectTaskLocked(ctx, p, projectID, taskID)
}

func (s *Server) deployProjectTaskLocked(ctx context.Context, p identity.Principal, projectID, taskID string) error {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || strings.TrimSpace(p.UserID) == "" || strings.TrimSpace(p.AccountScopeID) == "" {
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
	if proj.AccountID != "" && proj.AccountID != p.AccountScopeID {
		return errors.New("cross-account project access forbidden")
	}
	task, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		return err
	}
	if !found || task == nil {
		return fmt.Errorf("task %q not found", taskID)
	}
	if task.AccountID != "" && task.AccountID != p.AccountScopeID {
		return errors.New("cross-account task access forbidden")
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
	if task.PlanBinding != nil && task.PlanBinding.PlanID != "" {
		return errors.New("plan-bound tasks require exact task-card acceptance; generic deployment cannot authorize plan execution")
	}
	if task.Status == "planning" || task.Agent == "plan" {
		return errors.New("planning tasks must submit a structured plan before implementation")
	}

	// Idempotent retry: if active run intent exists, avoid duplicate runs
	if task.Status == "in_progress" && task.SessionID != "" {
		activeIntent, ok, _ := db.GetV3SessionActiveRunIntent(task.SessionID)
		if ok && (activeIntent.Status == pebblestore.V3RunIntentPendingExecutor || activeIntent.Status == pebblestore.V3RunIntentRunning) {
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
				Kind:            sessionruntime.SessionMutationRecordRunIntent,
				RunIntent:       runIntent,
				NowUnixMs:       now,
			})
			if mutationErr != nil {
				return fmt.Errorf("start session run intent: %w", mutationErr)
			}
			s.EnqueueSessionRun(p, task.SessionID, runID, proj.PrimarySessionID)
		}
	}
	_, updateErr := db.UpdateProjectTask(p.AccountScopeID, projectID, taskID, func(t *pebblestore.ProjectTaskRecord) error {
		t.Status = "in_progress"
		t.ActionNeeded = ""
		t.SessionID = task.SessionID
		t.WorkspacePath = task.WorkspacePath
		t.WorktreeBranch = task.WorktreeBranch
		t.BaseBranch = task.BaseBranch
		t.BaseCommit = task.BaseCommit
		t.WorktreeName = task.WorktreeName
		return nil
	})
	return updateErr
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
	if s.planLifecycle == nil {
		return sessionruntime.ProjectTaskPlanSubmissionResult{}, errors.New("plan lifecycle service is not configured")
	}
	return s.planLifecycle.SubmitProjectTaskStructuredPlan(input)
}
