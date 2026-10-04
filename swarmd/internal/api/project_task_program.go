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
	"swarm/packages/swarmd/internal/taskrouter"
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
	if sessID == "" || (task.SessionID != "" && sessID != task.SessionID) {
		return
	}
	if plan, ok, _ := db.GetPlan(sessID, task.PlanBinding.PlanID); ok && plan.Document != nil {
		if plan.AccountScopeID != task.AccountID || plan.SessionID != sessID {
			return
		}
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

	origin, err := s.projectTaskOrigin(ctx, p, projectID)
	if err != nil {
		return nil, err
	}
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(input.Description)
	}
	if prompt == "" {
		prompt = title
	}
	// Preserve the caller's exact proposal for idempotent replay; routing below
	// supplies the small-task parent identity without changing submission bytes.
	submittedInput := input
	if (input.Agent == "image" || input.Intent == "image") && strings.TrimSpace(input.Prompt) != "" {
		prompt = input.Prompt
	}
	if len(input.CoderAssignments) > 0 {
		if input.Document != nil || input.PlanDocument != nil || input.TaskProgram != nil || input.TaskProgramID != "" || (input.Agent != "" && input.Agent != "swarm") || (input.FeatureSize != "" && input.FeatureSize != "small") || (input.OutcomeType != "" && input.OutcomeType != "code_pr" && input.OutcomeType != "bug_patch" && input.OutcomeType != "code") {
			return nil, errors.New("coder_assignments require a small Swarm coding task without a plan or task program")
		}
		input.Agent, input.FeatureSize = "swarm", "small"
		if strings.TrimSpace(input.Description) == "" {
			input.Description = prompt
		}
		if input.OutcomeType == "" {
			input.OutcomeType = "code_pr"
		}
	}
	taskID := strings.TrimSpace(input.ID)
	if taskID == "" {
		taskID = "task_" + sessionruntime.NewSessionID()
	}
	unlockAdmission := s.lockProjectTaskAdmission(p.AccountScopeID, projectID, taskID)
	defer unlockAdmission()
	// A durable admission wins over fresh AI output on every retry.
	s.projectTaskCreateMu.Lock()
	existing, found, getErr := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
	if getErr != nil {
		s.projectTaskCreateMu.Unlock()
		return nil, getErr
	}
	if found && existing != nil {
		result, replayErr := s.replayProjectTaskSubmission(ctx, p, proj, existing, submittedInput)
		s.projectTaskCreateMu.Unlock()
		return result, replayErr
	}
	s.projectTaskCreateMu.Unlock()
	var contextSources []pebblestore.ProjectTaskSource
	// Source identity is resolved before any reservation. Only direct media can use a non-repository catalog root.
	reqOp := strings.ToLower(strings.TrimSpace(input.Operation))
	isDirectVideo := input.Agent == "video" || reqOp == pebblestore.VideoOperationEdit || reqOp == pebblestore.VideoOperationExtend || (reqOp == pebblestore.VideoOperationCreate && input.Agent == "video")
	requiresRepo := input.Document != nil || input.PlanDocument != nil || input.TaskProgram != nil || (!isDirectVideo && input.Agent != "image" && input.Agent != "video" && input.Agent != "sound" && input.Agent != "audio")
	isImage := input.Agent == "image" || input.Intent == "image"
	var source pebblestore.ProjectTaskSource
	if isImage {
		if input.Document != nil || input.PlanDocument != nil || input.TaskProgram != nil || input.TaskProgramID != "" || len(input.CoderAssignments) > 0 || input.SessionID != "" || input.Operation != "" {
			return nil, errors.New("image tasks cannot carry source execution, sessions, plans, or task programs")
		}
		if _, err := pebblestore.RouteAndPlanProjectTaskWithOptions(pebblestore.TaskPlanOptions{Prompt: prompt, Agent: input.Agent, Intent: input.Intent, OutcomeType: input.OutcomeType, Tier: input.Tier, FeatureSize: input.FeatureSize, VariantCount: input.VariantCount}); err != nil {
			return nil, err
		}
	} else {
		if strings.TrimSpace(input.WorkspacePath) == "" && strings.TrimSpace(input.WorkspaceID) == "" && input.WorkspaceGeneration == 0 {
			source, contextSources, err = s.routeProjectTaskSource(ctx, p, proj, prompt, requiresRepo)
		} else {
			source, err = s.resolveProjectTaskSource(p, proj, input.WorkspacePath, input.WorkspaceID, input.WorkspaceGeneration, requiresRepo)
		}
		if err != nil {
			return nil, err
		}
	}
	// Resolve every assignment before reserving any task/session. Never trust a
	// client-supplied source binding or infer a different repository from prose.
	input.CoderAssignments = append([]pebblestore.ProjectTaskCoderAssignment(nil), input.CoderAssignments...)
	for i := range input.CoderAssignments {
		a := &input.CoderAssignments[i]
		path, id, generation := a.WorkspacePath, a.WorkspaceID, a.WorkspaceGeneration
		if strings.TrimSpace(path) == "" && strings.TrimSpace(id) == "" {
			path, id = source.Path, source.WorkspaceID
			if generation == 0 {
				generation = source.WorkspaceGeneration
			}
		}
		bound, err := s.resolveProjectTaskSource(p, proj, path, id, generation, true)
		if err != nil {
			return nil, fmt.Errorf("coder assignment %d: %w", i+1, err)
		}
		a.WorkspacePath, a.WorkspaceID, a.WorkspaceGeneration = bound.Path, bound.WorkspaceID, bound.WorkspaceGeneration
		a.SourceWorkspace = bound
	}
	wsPath := source.Path
	agentName := strings.TrimSpace(input.Agent)
	if isDirectVideo {
		agentName = "video"
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
	programSources, err := s.resolveProjectPlanSources(p, proj, structDoc, source)
	if err != nil {
		return nil, err
	}
	featureSize := strings.TrimSpace(input.FeatureSize)
	outcomeType := strings.TrimSpace(input.OutcomeType)
	if isDirectVideo && outcomeType == "" {
		outcomeType = "video_clip"
	}
	tier := strings.TrimSpace(input.Tier)
	if structDoc != nil {
		if err := sessionruntime.ValidateProjectPlanReview(structDoc); err != nil {
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
		Intent:             input.Intent,
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
	if source.Path != "" {
		routed.WorkspacesInvolved = []string{source.Path}
		routed.ContextPoolSummary = "Execution source: " + source.Path
		involved := map[string]bool{source.Path: true}
		for _, executionSource := range programSources {
			if !involved[executionSource.Path] {
				involved[executionSource.Path] = true
				routed.WorkspacesInvolved = append(routed.WorkspacesInvolved, executionSource.Path)
			}
		}
		for _, assignment := range input.CoderAssignments {
			if !involved[assignment.SourceWorkspace.Path] {
				involved[assignment.SourceWorkspace.Path] = true
				routed.WorkspacesInvolved = append(routed.WorkspacesInvolved, assignment.SourceWorkspace.Path)
			}
		}
		for _, contextSource := range contextSources {
			if !involved[contextSource.Path] {
				involved[contextSource.Path] = true
				routed.WorkspacesInvolved = append(routed.WorkspacesInvolved, contextSource.Path)
			}
			routed.ContextPoolSummary += "; read-only context: " + contextSource.Path
		}
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
		description = prompt
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
			copy := *taskProg
			copy.ID = "prog-" + sanitizeBranchSlug(title)
			taskProg = &copy
			if input.Document == nil && input.PlanDocument == nil && structDoc != nil {
				structDoc.Checkpoints[0].TaskProgram = taskProg
			}
		}
		if taskProgID == "" {
			taskProgID = taskProg.ID
		}
	} else if routed.TaskProgram != nil {
		taskProg = routed.TaskProgram
		taskProgID = taskProg.ID
	}

	// User-selected branch names retain conflict checks. Generated names must be
	// task-owned: identical prompts are valid independent tasks, not a collision.
	if strings.TrimSpace(input.WorktreeBranch) == "" && worktreeBranch != "" {
		suffix := sha256.Sum256([]byte(projectID + ":" + taskID))
		worktreeBranch += "-" + hex.EncodeToString(suffix[:4])
	}
	// Reservation identity and submission bytes are independent of generated plan prose.
	submissionHash, err := projectTaskSubmissionHash(projectID, submittedInput, source)
	if err != nil {
		return nil, err
	}
	// Name only after idempotent replay and source checks. The deterministic
	// execution contract and full original prompt must not be elaborated here.
	if isImage {
		preflight := pebblestore.ProjectTaskRecord{Agent: "image", Model: input.Model, AspectRatio: aspectRatio, Resolution: input.Resolution, VariantCount: variantCount, Deliverables: routed.Deliverables}
		if err := validateProjectMediaTaskSettings(s, &preflight, p); err != nil {
			return nil, err
		}
		for _, attachment := range input.AttachedMedia {
			if hasConflictingMediaDeclaration(attachment) {
				return nil, errors.New("conflicting image attachment declarations")
			}
			if _, _, err := s.resolveSourceMediaBytes(ctx, p, attachment, "image"); err != nil {
				return nil, fmt.Errorf("image attachment: %w", err)
			}
		}
		router := taskrouter.NewService(func(ctx context.Context, instructions, input string) (string, error) {
			res, err := s.invokeConfiguredRouterOnce(ctx, p, instructions, input, 64<<10)
			return res.Text, err
		})
		routed, err = router.RouteTask(ctx, taskrouter.TaskRouteOptions{Prompt: prompt, Title: input.Title, Agent: agentName, Intent: input.Intent, OutcomeType: outcomeType, Tier: tier, AspectRatio: aspectRatio, VariantCount: variantCount, EnhancePrompt: input.EnhancePrompt})
		if err != nil {
			return nil, err
		}
		title = routed.Title
		description = prompt
		deliverables = routed.Deliverables
		stages = routed.Stages
		planSummary = routed.PlanSummary
		fullPlanMarkdown = routed.FullPlanMarkdown
		worktreeBranch = ""
	} else if strings.TrimSpace(input.Title) == "" {
		router := taskrouter.NewService(func(ctx context.Context, instructions, input string) (string, error) {
			res, err := s.invokeConfiguredRouterOnce(ctx, p, instructions, input, 4<<10)
			return res.Text, err
		})
		title, err = router.NameTask(ctx, prompt, "")
		if err != nil {
			return nil, fmt.Errorf("task router naming: %w", err)
		}
	}

	isDirectMedia := agentName == "image" || agentName == "video" || agentName == "sound" || agentName == "audio"
	sessionID := strings.TrimSpace(input.SessionID)
	if !isDirectMedia && sessionID == "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("task-session:%s:%s:%s", p.AccountScopeID, projectID, taskID)))
		sessionID = hex.EncodeToString(sum[:16])
	}

	task := pebblestore.ProjectTaskRecord{
		OriginSessionID:    origin,
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
		SourceWorkspace:    source,
		ProgramSources:     programSources,
		ContextSources:     contextSources,
		ClientRequestID:    strings.TrimSpace(input.ClientRequestID),
		SubmissionHash:     submissionHash,
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
		EnhancePrompt:      routed.EnhancePrompt,
		ImagePrompts:       routed.ImagePrompts,
		DurationSeconds:    input.DurationSeconds,
		Model:              strings.TrimSpace(input.Model),
		Provider:           strings.TrimSpace(input.Provider),
		Thinking:           strings.TrimSpace(input.Thinking),
		ServiceTier:        strings.TrimSpace(input.ServiceTier),
		ContextMode:        strings.TrimSpace(input.ContextMode),
		Operation:          reqOp,
		Scenes:             routed.Scenes,
		Soundtrack:         soundtrack,
		AutoApprove:        input.AutoApprove,
		RouterAlert:        routed.RouterAlert,
		AttachedMedia:      input.AttachedMedia,
		CoderAssignments:   input.CoderAssignments,
		TaskProgram:        taskProg,
		TaskProgramID:      taskProgID,
		CreatedAt:          time.Now().UnixMilli(),
		UpdatedAt:          time.Now().UnixMilli(),
	}
	if len(task.CoderAssignments) > 0 {
		// The assignment list, not router boilerplate, is the executable small-task
		// contract; keep the visible card from presenting a generic plan.
		task.PlanSummary = fmt.Sprintf("One small task: %d parallel Coders supervised by Swarm", len(task.CoderAssignments))
		task.FullPlanMarkdown = ""
		task.PipelineStages = []string{"Parallel Coder implementation", "Parent validation and integration"}
	}
	if len(task.AttachedMedia) == 0 && len(routed.AttachedMedia) > 0 {
		task.AttachedMedia = routed.AttachedMedia
	}
	if isDirectVideo && len(task.AttachedMedia) > 0 {
		srcRec, err := s.resolveSourceMediaRecord(ctx, p, task.AttachedMedia[0], "video", projectID)
		if err == nil && srcRec != nil && srcRec.SourceLink != nil {
			task.SourceDigestSHA256 = srcRec.SourceLink.DigestSHA256
			task.AttachedMedia[0].DigestSHA256 = srcRec.SourceLink.DigestSHA256
			task.AttachedMedia[0].SourceLink = srcRec.SourceLink
		}
	}
	if isDirectMediaTask(&task) {
		if err := validateProjectMediaTaskSettings(s, &task, p); err != nil {
			return nil, err
		}
	}
	if task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" {
		task.AspectRatio = ""
		task.VariantCount = 0
	}

	if structDoc != nil {
		task.Status = "pending_approval"
		task.ActionNeeded = "Review plan in task card and click Approve"
	} else if task.Agent == "plan" {
		task.Status = "planning"
		task.ActionNeeded = "Plan agent investigating and authoring structured plan..."
		task.WhatDidDo = []string{"Started planning investigation"}
	} else if task.Agent == "coder" && task.TaskProgram == nil && len(task.CoderAssignments) == 0 && task.FeatureSize != "big" {
		// A direct Coder request is already an execution request, not a plan
		// proposal. Structured documents and planning requests are handled above.
		task.Status = "in_progress"
		task.ActionNeeded = ""
	} else if task.TaskProgram == nil && (task.Agent == "coder" || (len(task.CoderAssignments) == 0 && (task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch"))) {
		task.Status = "pending_approval"
		task.ActionNeeded = "Review task and click Approve to start execution"
		if input.AutoApprove {
			task.Status = "in_progress"
			task.ActionNeeded = ""
		}
	} else if len(task.CoderAssignments) > 0 {
		task.Status = "pending_approval"
		task.ActionNeeded = "Review parallel Coder assignments and click Approve"
		if input.AutoApprove {
			task.Status, task.ActionNeeded = "in_progress", ""
		}
	} else if task.TaskProgram != nil {
		if input.AutoApprove {
			task.Status = "in_progress"
		} else {
			task.Status = "pending_approval"
			task.ActionNeeded = "Review task program and click Approve"
		}
	} else if isDirectMedia {
		if err := admitProjectMediaTask(&task); err != nil {
			return nil, err
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

	// Serialize only admission/execution bookkeeping, never provider calls.
	s.projectTaskCreateMu.Lock()
	defer s.projectTaskCreateMu.Unlock()
	if existing, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID); err != nil {
		return nil, err
	} else if found && existing != nil {
		return s.replayProjectTaskSubmission(ctx, p, proj, existing, submittedInput)
	}
	currentProject, found, err := db.GetProject(p.AccountScopeID, projectID)
	if err != nil {
		return nil, err
	}
	if !found || currentProject == nil || (currentProject.AccountID != "" && currentProject.AccountID != p.AccountScopeID) {
		return nil, errors.New("project no longer authorized for task admission")
	}
	proj = currentProject
	if err := s.revalidateProjectTaskSource(p, proj, &task); err != nil {
		return nil, err
	}
	if structDoc == nil && task.TaskProgram != nil && len(task.AttachedMedia) > 0 {
		return nil, errors.New("task attachments require a session seed; submit an executable plan instead of a bare task program")
	}
	if err := s.preflightProjectTaskAttachments(ctx, p, &task); err != nil {
		return nil, err
	}
	// Persist task reservation only after attachment admission.
	claimed, err := db.ReserveProjectTaskIfAbsent(p.AccountScopeID, &task)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, errors.New("task reservation was claimed concurrently; retry using the same task ID and payload")
	}
	_, err = db.UpdateProject(p.AccountScopeID, projectID, func(pr *pebblestore.ProjectRecord) error {
		for _, tid := range pr.ActiveTaskIDs {
			if tid == task.ID {
				return nil
			}
		}
		pr.ActiveTaskIDs = append(pr.ActiveTaskIDs, task.ID)
		return nil
	})
	if err != nil {
		return nil, err
	}

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
			ParentSessionID: task.OriginSessionID,
		})
		if sErr != nil {
			return nil, fmt.Errorf("submit structured plan: %w", sErr)
		}
		task = subResult.Task
	} else if task.Status == "planning" {
		if err := s.deployProjectTaskExecution(p, proj, &task, "in_progress", prompt); err != nil {
			return nil, fmt.Errorf("deploy planning session: %w", err)
		}
		if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
			return nil, err
		}
	} else if task.TaskProgram == nil && (task.Agent == "coder" || task.OutcomeType == "code_pr" || task.OutcomeType == "bug_patch" || len(task.CoderAssignments) > 0) {
		if err := s.deployProjectTaskExecution(p, proj, &task, task.Status, prompt); err != nil {
			return nil, fmt.Errorf("deploy coder session: %w", err)
		}
		if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
			return nil, err
		}
	} else if task.TaskProgram != nil {
		if task.Status == "in_progress" {
			if err := s.deployProjectTaskProgram(p, proj, &task); err != nil {
				return nil, fmt.Errorf("deploy task program: %w", err)
			}
		}
		if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
			return nil, err
		}
	} else if isDirectMedia {
		if task.Status == "in_progress" {
			if err := s.deployProjectTaskExecution(p, proj, &task, "in_progress", prompt); err != nil {
				return nil, fmt.Errorf("deploy media execution: %w", err)
			}
		}
		if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
			return nil, err
		}
	} else {
		if task.Status == "in_progress" {
			if err := s.deployProjectTaskExecution(p, proj, &task, "in_progress", prompt); err != nil {
				return nil, fmt.Errorf("deploy task execution: %w", err)
			}
		}
		// Direct media deployment already persisted the slots before launching
		// workers. Never overwrite their live results with this reservation.
		if !isDirectMedia || task.Status != "in_progress" {
			if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
				return nil, err
			}
		}
	}

	freshTask, found, err := db.GetProjectTask(p.AccountScopeID, projectID, task.ID)
	if err != nil {
		return nil, err
	}
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
	if err := s.revalidateProjectTaskSource(p, proj, task); err != nil {
		return err
	}

	// 1. Ensure coordinator session exists
	now := time.Now().UnixMilli()
	ownedSession, sessionExists, sessionErr := db.GetSession(task.SessionID)
	if sessionErr != nil {
		return sessionErr
	}
	if !sessionExists {
		sessionID := task.SessionID
		if sessionID == "" {
			return errors.New("task coordinator requires a reserved session identity")
		}
		wsPath := task.SourceWorkspace.Path
		if task.WorkspacePath != wsPath {
			return errors.New("task program source differs from reserved workspace")
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
				"project_id":                           task.ProjectID,
				"task_id":                              task.ID,
				"task_title":                           task.Title,
				"role":                                 "task_program_coordinator",
				"task_program_id":                      task.TaskProgram.ID,
				"swarm_v3_source_workspace_id":         task.SourceWorkspace.WorkspaceID,
				"swarm_v3_source_workspace_generation": task.SourceWorkspace.WorkspaceGeneration,
				"swarm_v3_source_workspace_path":       task.SourceWorkspace.Path,
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
	} else {
		if ownedSession.AccountScopeID != p.AccountScopeID || ownedSession.UserID != p.UserID || ownedSession.Metadata == nil || ownedSession.Metadata["project_id"] != task.ProjectID || ownedSession.Metadata["task_id"] != task.ID || ownedSession.Metadata["task_program_id"] != progID || ownedSession.Metadata["swarm_v3_source_workspace_path"] != task.SourceWorkspace.Path || ownedSession.Metadata["swarm_v3_source_workspace_id"] != task.SourceWorkspace.WorkspaceID || fmt.Sprint(ownedSession.Metadata["swarm_v3_source_workspace_generation"]) != fmt.Sprint(task.SourceWorkspace.WorkspaceGeneration) {
			return errors.New("task program coordinator ownership does not match reservation")
		}
		if ownedSession.WorkspacePath != task.SourceWorkspace.Path {
			return errors.New("task program coordinator target does not match reservation")
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
	if record.ParentSessionID != task.SessionID || record.DefinitionHash != initialRecord.DefinitionHash {
		return errors.New("task program definition conflicts with reservation")
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
	if active, ok, err := db.GetV3SessionActiveRunIntent(task.SessionID); err != nil {
		return err
	} else if ok {
		if active.RunID != runID || active.AccountScopeID != p.AccountScopeID {
			return errors.New("task program has unrelated active run")
		}
		if active.Status == pebblestore.V3RunIntentPendingExecutor || active.Status == pebblestore.V3RunIntentRunning {
			return nil
		}
		return errors.New("task program run already recorded; use explicit retry lifecycle")
	}
	if history, err := db.ListRunIntents(task.SessionID, 1000); err != nil {
		return err
	} else if len(history) != 0 {
		return errors.New("task program already has run history; use explicit retry lifecycle")
	}
	parentSessionID := task.OriginSessionID
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
	parentSessionID := task.OriginSessionID
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
	// Publication stores the canonical document on the session, not the task.
	// Revalidate its repository lanes before any acceptance or dispatch.
	hydrateTaskPlanDocument(existingTask, db)
	if err := s.revalidateProjectTaskSource(p, proj, existingTask); err != nil {
		return nil, err
	}
	// Plan outcomes must never fall through to direct-agent dispatch when their
	// durable definition binding is absent (including damaged/recovered records).
	if (existingTask.Agent == "plan" || existingTask.OutcomeType == "plan_spec") && (existingTask.PlanBinding == nil || existingTask.PlanBinding.PlanID == "") {
		return nil, errors.New("cannot approve plan task without a submitted structured plan binding")
	}
	if existingTask.SessionID != "" {
		if owned, ok, err := db.GetSession(existingTask.SessionID); err != nil {
			return nil, err
		} else if ok {
			if existingTask.WorkspacePath == existingTask.SourceWorkspace.Path && owned.WorktreeEnabled {
				if err := s.reconcileProjectTaskSession(p, proj, existingTask, owned, "pending_approval"); err != nil {
					return nil, err
				}
				if err := db.PutProjectTask(p.AccountScopeID, existingTask); err != nil {
					return nil, err
				}
			} else if err := verifyProjectTaskSession(existingTask, owned, p.AccountScopeID); err != nil {
				return nil, err
			}
		} else if !isDirectMediaTask(existingTask) {
			if existingTask.WorkspacePath != existingTask.SourceWorkspace.Path {
				return nil, errors.New("task reservation has allocated runtime but no session")
			}
			if err := s.deployProjectTaskExecution(p, proj, existingTask, "pending_approval", existingTask.Title); err != nil {
				return nil, err
			}
			if err := db.PutProjectTask(p.AccountScopeID, existingTask); err != nil {
				return nil, err
			}
		}
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
		if err := sessionruntime.ValidateProjectPlanReview(plan.Document); err != nil {
			return nil, fmt.Errorf("plan review unavailable: %w", err)
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
		activeIntent, ok, intentErr := db.GetV3SessionActiveRunIntent(existingTask.SessionID)
		if intentErr != nil {
			return nil, intentErr
		}
		if ok && activeIntent.AccountScopeID == p.AccountScopeID && activeIntent.RunID == fmt.Sprintf("desktop-v3-run:task-%s", existingTask.ID) && (activeIntent.Status == pebblestore.V3RunIntentPendingExecutor || activeIntent.Status == pebblestore.V3RunIntentRunning) {
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
				TaskProgramSources:        existingTask.ProgramSources,
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
			// A prior attempt may have committed the intent but lost its scheduler
			// wakeup or task update. Re-enqueue the same durable owner, never a new run.
			if active.Status == pebblestore.V3RunIntentPendingExecutor {
				s.EnqueueSessionRun(p, existingTask.SessionID, active.RunID, active.ParentSessionID)
			}
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
		parentSessionID := existingTask.OriginSessionID
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
		// Confirmation is a one-way transition. Retries must not recreate slots
		// or dispatch the same generation again, including after completion/failure.
		if existingTask.Status != "pending_approval" {
			return existingTask, nil
		}
		if err := validateProjectMediaTaskSettings(s, existingTask, p); err != nil {
			return nil, err
		}
		if err := s.deployProjectTaskExecution(p, proj, existingTask, "in_progress", ""); err != nil {
			return nil, fmt.Errorf("deploy media execution: %w", err)
		}
		// Deployment persisted before dispatch. Read, rather than overwrite,
		// the result: workers may already have completed individual images.
		fresh, found, err := db.GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errors.New("deployed media task not found")
		}
		return fresh, nil
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
		if err := verifyProjectTaskSession(existingTask, session, p.AccountScopeID); err != nil {
			return nil, err
		}
	}

	now := time.Now().UnixMilli()
	runID := fmt.Sprintf("desktop-v3-run:task-%s", existingTask.ID)
	parentSessionID := existingTask.OriginSessionID
	if len(existingTask.CoderAssignments) > 0 {
		parentSessionID = "" // This Swarm session is the delegation parent, not a delegated child.
	}
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
	if err := s.revalidateProjectTaskSource(p, proj, task); err != nil {
		return err
	}
	if task.SessionID != "" {
		if owned, ok, err := db.GetSession(task.SessionID); err != nil {
			return err
		} else if ok {
			if err := verifyProjectTaskSession(task, owned, p.AccountScopeID); err != nil {
				return err
			}
		}
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

	// Direct media has no session run intent. Its persisted execution status is
	// authoritative; generic deploy retries cannot regenerate an admitted batch.
	if isOrdinaryMediaAgent(task.Agent) && task.Status != "queued" {
		return nil
	}

	// Idempotent retry: if active run intent exists, avoid duplicate runs
	if task.Status == "in_progress" && task.SessionID != "" && task.TaskProgram == nil && task.TaskProgramID == "" {
		activeIntent, ok, intentErr := db.GetV3SessionActiveRunIntent(task.SessionID)
		if intentErr != nil {
			return intentErr
		}
		if ok {
			if activeIntent.AccountScopeID != p.AccountScopeID || activeIntent.RunID != fmt.Sprintf("desktop-v3-run:task-%s", task.ID) {
				return errors.New("task session has unrelated active run")
			}
			if activeIntent.Status == pebblestore.V3RunIntentPendingExecutor || activeIntent.Status == pebblestore.V3RunIntentRunning {
				return nil
			}
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
			owned, found, err := db.GetSession(task.SessionID)
			if err != nil {
				return err
			}
			if !found {
				if task.WorkspacePath != task.SourceWorkspace.Path {
					return errors.New("reserved task runtime has no session")
				}
				if err := s.deployProjectTaskExecution(p, proj, task, "in_progress", task.Title); err != nil {
					return err
				}
			} else {
				if err := s.reconcileProjectTaskSession(p, proj, task, owned, "in_progress"); err != nil {
					return err
				}
			}
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
