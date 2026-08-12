package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type taskProgramIntegrationService interface {
	PrepareTaskIntegration(parentPath, expectedParentHead string, children []worktreeruntime.TaskIntegrationChild) (worktreeruntime.TaskIntegrationPlan, error)
	ApplyTaskIntegration(parentPath string, plan worktreeruntime.TaskIntegrationPlan) (worktreeruntime.TaskIntegrationResult, error)
}

type taskProgramScheduler struct {
	service            *Service
	ctx                context.Context
	sessionMode        string
	step               int
	call               tool.Call
	emit               StreamHandler
	req                taskExecutionRequest
	parentSession      pebblestore.SessionSnapshot
	description        string
	prompt             string
	parsed             taskCallArguments
	record             pebblestore.TaskProgramRecord
	reservationCap     int
	allOutcomes        []map[string]any
	expectedParentHead string
	barrierJobID       string
}

func (s *Service) executeTaskProgram(ctx context.Context, sessionMode string, step int, call tool.Call, emit StreamHandler, req taskExecutionRequest, parentSession pebblestore.SessionSnapshot, parsed taskCallArguments, record pebblestore.TaskProgramRecord, description, prompt string) (string, error) {
	if s.permissions == nil || strings.TrimSpace(req.RunID) == "" {
		return "", errors.New("task program scheduler requires a durable permission reservation")
	}
	reservation, ok, err := s.permissions.GetSubagentReservation(parentSession.ID, req.RunID, call.CallID)
	if err != nil {
		return "", err
	}
	if !ok || !reservation.Program || reservation.ActiveCount < 1 {
		return "", errors.New("task program scheduler reservation is missing active capacity")
	}
	scheduler := taskProgramScheduler{service: s, ctx: ctx, sessionMode: sessionMode, step: step, call: call, emit: emit, req: req, parentSession: parentSession, description: description, prompt: prompt, parsed: parsed, record: record, reservationCap: reservation.ActiveCount}
	scheduler.emitProgramProgress("program.started", "Task Program started")
	return scheduler.run()
}

func (p *taskProgramScheduler) run() (string, error) {
	for {
		if p.record.State == pebblestore.TaskProgramStateFailed || p.record.State == pebblestore.TaskProgramStateCancelled || p.record.State == pebblestore.TaskProgramStateBlocked {
			return marshalTaskProgramStatus(p.record, false)
		}
		stageIndex := taskProgramStageIndex(p.record)
		if stageIndex < 0 {
			return "", errors.New("task program active stage is invalid")
		}
		ready := taskProgramReadyJobIndexes(p.record, stageIndex)
		for len(ready) > 0 {
			cohortSize := p.reservationCap
			if cohortSize > len(ready) {
				cohortSize = len(ready)
			}
			if _, err := p.service.permissions.UpdateSubagentProgramCohort(p.parentSession.ID, p.req.RunID, p.call.CallID, cohortSize); err != nil {
				return "", err
			}
			if err := p.runCohort(ready[:cohortSize]); err != nil {
				if p.record.State == pebblestore.TaskProgramStateBlocked {
					return p.finishProgramError(err)
				}
				return p.finishFailed(err)
			}
			stageIndex = taskProgramStageIndex(p.record)
			ready = taskProgramReadyJobIndexes(p.record, stageIndex)
		}
		if taskProgramStageHasRunningOrDeclared(p.record, stageIndex) {
			return p.finishBlocked(errors.New("task program stage has no schedulable declared job; additional semantic work requires a new declared program revision"))
		}
		if err := p.integrateStage(stageIndex); err != nil {
			return p.finishBlocked(err)
		}
		if stageIndex+1 >= len(p.record.Definition.Stages) {
			return p.finishCompleted()
		}
		if err := p.advanceStage(stageIndex + 1); err != nil {
			return "", err
		}
	}
}

func taskProgramStageIndex(record pebblestore.TaskProgramRecord) int {
	for i, stage := range record.Definition.Stages {
		if stage.ID == record.ActiveStageID {
			return i
		}
	}
	return -1
}

func taskProgramJobIndex(record pebblestore.TaskProgramRecord, jobID string) int {
	for i := range record.Jobs {
		if record.Jobs[i].JobID == jobID {
			return i
		}
	}
	return -1
}

func taskProgramReadyJobIndexes(record pebblestore.TaskProgramRecord, stageIndex int) []int {
	if stageIndex < 0 || stageIndex >= len(record.Definition.Stages) {
		return nil
	}
	stageID := record.Definition.Stages[stageIndex].ID
	out := make([]int, 0)
	for i, job := range record.Jobs {
		if job.StageID != stageID || job.State != pebblestore.TaskProgramJobDeclared {
			continue
		}
		definition := record.Definition.Jobs[i]
		ready := true
		for _, dependency := range definition.DependsOn {
			depIndex := taskProgramJobIndex(record, dependency)
			if depIndex < 0 || (record.Jobs[depIndex].State != pebblestore.TaskProgramJobIntegrated && record.Jobs[depIndex].State != pebblestore.TaskProgramJobCompleted) {
				ready = false
				break
			}
		}
		if ready {
			out = append(out, i)
		}
	}
	return out
}

func taskProgramStageHasRunningOrDeclared(record pebblestore.TaskProgramRecord, stageIndex int) bool {
	stageID := record.Definition.Stages[stageIndex].ID
	for _, job := range record.Jobs {
		if job.StageID == stageID && (job.State == pebblestore.TaskProgramJobDeclared || job.State == pebblestore.TaskProgramJobRunning) {
			return true
		}
	}
	return false
}

func (p *taskProgramScheduler) emitProgramProgress(event, summary string) {
	program, status := taskProgramStreamMetadata(p.record)
	payload := map[string]any{
		"tool":                 "task",
		"action":               p.parsed.Action,
		"status":               p.record.State,
		"phase":                strings.TrimSpace(event),
		"description":          p.description,
		"goal":                 p.description,
		"parent_session_id":    p.parentSession.ID,
		"task_call_id":         p.record.ReservationCallID,
		"program_id":           p.record.ProgramID,
		"program_state":        p.record.State,
		"active_stage_id":      p.record.ActiveStageID,
		"next_action":          p.record.NextAction,
		"event":                "program.snapshot",
		"path_id":              taskStreamPathIDV2,
		"stream_version":       2,
		"summary":              strings.TrimSpace(summary),
		"program":              program,
		"program_status":       status,
		"program_presentation": taskProgramPresentationPayload(p.record),
		"details_truncated":    false,
	}
	if payload["task_call_id"] == "" {
		payload["task_call_id"] = strings.TrimSpace(p.call.CallID)
	}
	emitTaskStreamPayload(p.emit, p.step, "task", fmt.Sprint(payload["task_call_id"]), payload)
}

func taskProgramLaunchPatch(launch map[string]any, jobID, stageID, phase string) map[string]any {
	patch := cloneGenericMap(launch)
	if patch == nil {
		patch = map[string]any{}
	}
	patch["job_id"] = jobID
	patch["program_job_id"] = jobID
	patch["stage_id"] = stageID
	patch["program_stage_id"] = stageID
	patch["state"] = taskProgramPresentationJobState(phase)
	return patch
}

func (p *taskProgramScheduler) runCohort(indexes []int) error {
	running := make([]pebblestore.TaskProgramJobTransition, 0, len(indexes))
	for _, index := range indexes {
		job := p.record.Jobs[index]
		running = append(running, pebblestore.TaskProgramJobTransition{JobID: job.JobID, ExpectedState: pebblestore.TaskProgramJobDeclared, State: pebblestore.TaskProgramJobRunning, AttemptNumber: job.AttemptNumber + 1, ResumeGeneration: p.record.ResumeGeneration})
	}
	state, next := pebblestore.TaskProgramStateRunning, "await_running_jobs"
	var err error
	p.record, _, err = p.service.sessions.TransitionTaskProgram(p.parentSession.ID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: fmt.Sprintf("running:%d", p.record.Revision), State: &state, NextAction: &next, Jobs: running})
	if err != nil {
		return err
	}
	p.emitProgramProgress("cohort.running", fmt.Sprintf("Stage %s is running", p.record.ActiveStageID))
	cohort := p.parsed
	cohort.Program = nil
	cohort.Launches = make([]taskLaunchSpec, 0, len(indexes))
	jobs := make([]taskProgramJob, 0, len(indexes))
	for _, index := range indexes {
		cohort.Launches = append(cohort.Launches, p.parsed.Launches[index])
		jobs = append(jobs, p.parsed.Program.Jobs[index])
	}
	approved := ""
	if strings.TrimSpace(p.req.ApprovedArguments) != "" {
		var err error
		approved, err = taskProgramApprovedCohort(p.req.ApprovedArguments, cohort.Launches)
		if err != nil {
			return err
		}
	}
	cohortCall := p.call
	// Cohorts are an internal capacity detail. Every child update retains the
	// durable parent task call identity so clients never render separate cards.
	cohortCall.CallID = strings.TrimSpace(p.record.ReservationCallID)
	if cohortCall.CallID == "" {
		cohortCall.CallID = strings.TrimSpace(p.call.CallID)
	}
	cohortCall.Arguments = "{}"
	cohortReq := p.req
	cohortReq.Parsed, cohortReq.ParsedProvided = cohort, true
	cohortReq.ParentSession = &p.parentSession
	cohortReq.ApprovedArguments = approved
	cohortReq.RunID = ""
	cohortEmit := func(event StreamEvent) {
		if event.Type != StreamEventToolDelta || strings.TrimSpace(event.Output) == "" {
			if p.emit != nil {
				p.emit(event)
			}
			return
		}
		var payload map[string]any
		if json.Unmarshal([]byte(event.Output), &payload) != nil || payload["path_id"] != taskStreamPathIDV2 {
			if p.emit != nil {
				p.emit(event)
			}
			return
		}
		launch, _ := payload["launch"].(map[string]any)
		jobID := p.taskProgramCohortJobID(indexes, launch)
		if jobID == "" {
			return
		}
		presentation := taskProgramPresentationPayload(p.record)
		program, status := taskProgramStreamMetadata(p.record)
		patch := taskProgramLaunchPatch(launch, jobID, p.record.ActiveStageID, mapString(payload, "phase"))
		launchKey := "program-job:" + jobID
		patch["launch_key"] = launchKey
		programPayload := map[string]any{
			"tool": "task", "action": p.parsed.Action, "status": p.record.State,
			"phase": mapString(payload, "phase"), "description": p.description, "goal": p.description,
			"parent_session_id": p.parentSession.ID, "task_call_id": cohortCall.CallID,
			"program_id": p.record.ProgramID, "program_state": p.record.State,
			"active_stage_id": p.record.ActiveStageID, "next_action": p.record.NextAction, "event": "launch.patch",
			"path_id": taskStreamPathIDV2, "stream_version": 2,
			"launch_key": launchKey, "launch": patch, "program": program, "program_status": status,
			"program_presentation": presentation,
			"summary":              mapString(payload, "summary"), "details_truncated": false,
		}
		emitTaskStreamPayload(p.emit, p.step, "task", cohortCall.CallID, programPayload)
	}
	output, runErr := p.service.executeTaskToolWithParsed(p.ctx, p.parentSession.ID, p.sessionMode, p.step, cohortCall, cohortEmit, cohortReq)
	var payload map[string]any
	if json.Unmarshal([]byte(output), &payload) == nil {
		p.allOutcomes = append(p.allOutcomes, taskProgramLaunchRows(payload)...)
	}
	outcomes := taskProgramOutcomesFromPayload(payload, len(indexes))
	runErrs := taskProgramErrorsFromPayload(payload, runErr, len(indexes))
	updates := taskProgramOutcomeTransitions(&taskProgramSpec{Jobs: jobs}, outcomes, runErrs)
	state, next = pebblestore.TaskProgramStateRunning, "launch_ready_jobs"
	var blocker *pebblestore.TaskProgramBlocker
	if runErr != nil {
		state, next = pebblestore.TaskProgramStateBlocked, "repair_failed_child_then_resume"
		failedJobID := ""
		for _, update := range updates {
			if update.State == pebblestore.TaskProgramJobFailed || update.State == pebblestore.TaskProgramJobCancelled || update.State == pebblestore.TaskProgramJobBlocked {
				failedJobID = update.JobID
				break
			}
		}
		blockerRecord := p.record
		for _, update := range updates {
			index := taskProgramJobIndex(blockerRecord, update.JobID)
			if index < 0 {
				continue
			}
			job := &blockerRecord.Jobs[index]
			job.State = update.State
			job.ChildSessionID = firstNonEmptyString(update.ChildSessionID, job.ChildSessionID)
			job.WorkspacePath = firstNonEmptyString(update.WorkspacePath, job.WorkspacePath)
			job.WorktreeBranch = firstNonEmptyString(update.WorktreeBranch, job.WorktreeBranch)
			job.ParentBranch = firstNonEmptyString(update.ParentBranch, job.ParentBranch)
			job.ImmutableStageBase = firstNonEmptyString(update.ImmutableStageBase, job.ImmutableStageBase)
			job.ChildHead = firstNonEmptyString(update.ChildHead, job.ChildHead)
			job.IntegrationState = firstNonEmptyString(update.IntegrationState, job.IntegrationState)
		}
		originalRecord := p.record
		p.record = blockerRecord
		value := p.structuredBlocker(taskProgramErrorCode(runErr), runErr, next, failedJobID)
		p.record = originalRecord
		blocker = &value
	}
	p.record, _, err = p.service.sessions.TransitionTaskProgram(p.parentSession.ID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: fmt.Sprintf("cohort:%d", p.record.Revision), State: &state, NextAction: &next, Blocker: blocker, Jobs: updates})
	if err != nil {
		return err
	}
	p.emitProgramProgress("cohort.completed", fmt.Sprintf("Stage %s cohort completed", p.record.ActiveStageID))
	return runErr
}

func (p *taskProgramScheduler) taskProgramCohortJobID(indexes []int, launch map[string]any) string {
	launchIndex := 0
	switch value := launch["launch_index"].(type) {
	case int:
		launchIndex = value
	case float64:
		launchIndex = int(value)
	}
	if launchIndex < 1 || launchIndex > len(indexes) {
		return ""
	}
	index := indexes[launchIndex-1]
	if index < 0 || index >= len(p.record.Jobs) {
		return ""
	}
	return p.record.Jobs[index].JobID
}

func taskProgramPresentationJobState(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "completed":
		return pebblestore.TaskProgramJobCompleted
	case "failed", "tool.failed":
		return pebblestore.TaskProgramJobFailed
	case "cancelled":
		return pebblestore.TaskProgramJobCancelled
	case "spawned", "running", "tool.started", "tool.completed":
		return pebblestore.TaskProgramJobRunning
	default:
		return ""
	}
}

func taskProgramApprovedCohort(approved string, specs []taskLaunchSpec) (string, error) {
	manifest, err := parseApprovedTaskLaunchManifest(approved, specs)
	if err != nil {
		return "", err
	}
	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		return "", err
	}
	manifest.ManifestHash = digest
	envelope := map[string]any{"manifest_hash": digest, "manifest": manifest}
	raw, err := json.Marshal(envelope)
	return string(raw), err
}

func taskProgramOutcomesFromPayload(payload map[string]any, count int) []taskLaunchOutcome {
	out := make([]taskLaunchOutcome, count)
	rows := taskProgramLaunchRows(payload)
	for i := 0; i < count && i < len(rows); i++ {
		row := rows[i]
		out[i] = taskLaunchOutcome{
			ChildSessionID: mapString(row, "child_session_id"), WorkspacePath: mapString(row, "workspace_path"),
			WorktreeBranch: mapString(row, "worktree_branch"), ParentBranch: mapString(row, "parent_branch"),
			BaseCommit: mapString(row, "base_commit"), HeadCommit: mapString(row, "head_commit"),
			Phase: mapString(row, "phase"), Error: mapString(row, "error"), Reason: mapString(row, "reason"),
			WorktreeClean: mapBool(row, "worktree_clean"),
		}
	}
	return out
}

func taskProgramLaunchRows(payload map[string]any) []map[string]any {
	if typed, ok := payload["launches"].([]map[string]any); ok {
		return typed
	}
	raw, _ := payload["launches"].([]any)
	rows := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		if row, ok := value.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func taskProgramErrorsFromPayload(payload map[string]any, fallback error, count int) []error {
	out := make([]error, count)
	rows := taskProgramLaunchRows(payload)
	for i := 0; i < count; i++ {
		if i < len(rows) {
			row := rows[i]
			if message := firstNonEmptyString(mapString(row, "error"), mapString(row, "reason")); message != "" {
				out[i] = errors.New(message)
			}
		}
		if out[i] == nil && fallback != nil {
			out[i] = fallback
		}
	}
	return out
}

func (p *taskProgramScheduler) integrateStage(stageIndex int) error {
	stageID := p.record.Definition.Stages[stageIndex].ID
	children := make([]worktreeruntime.TaskIntegrationChild, 0)
	updates := make([]pebblestore.TaskProgramJobTransition, 0)
	expectedHead := strings.TrimSpace(p.record.ParentHead)
	if expectedHead == "" && p.service.worktrees != nil {
		if base, err := p.service.worktrees.ResolveTaskBase(p.parentSession.WorkspacePath); err == nil {
			expectedHead = strings.TrimSpace(base.BaseCommit)
		}
	}
	for i, job := range p.record.Jobs {
		if job.StageID != stageID {
			continue
		}
		definition := p.record.Definition.Jobs[i]
		if agentruntime.IsCoderAgentName(definition.AgentType) {
			if job.State != pebblestore.TaskProgramJobHandoffReady || job.ImmutableStageBase == "" || job.ChildHead == "" {
				p.barrierJobID = job.JobID
				return fmt.Errorf("Coder job %q is not ready for integration", job.JobID)
			}
			if expectedHead == "" {
				expectedHead = job.ImmutableStageBase
			}
			if job.ImmutableStageBase != expectedHead {
				p.barrierJobID = job.JobID
				p.expectedParentHead = expectedHead
				return fmt.Errorf("Coder job %q immutable base %s does not match stage base %s", job.JobID, job.ImmutableStageBase, expectedHead)
			}
			children = append(children, worktreeruntime.TaskIntegrationChild{SessionID: job.ChildSessionID, BaseCommit: job.ImmutableStageBase, HeadCommit: job.ChildHead})
			updates = append(updates, pebblestore.TaskProgramJobTransition{JobID: job.JobID, ExpectedState: pebblestore.TaskProgramJobHandoffReady, State: pebblestore.TaskProgramJobIntegrated, IntegrationState: "integrated"})
		}
	}
	p.expectedParentHead = expectedHead
	parentHead := expectedHead
	if len(children) > 0 {
		integrator, ok := p.service.worktrees.(taskProgramIntegrationService)
		if !ok {
			return errors.New("worktree service does not support canonical task integration")
		}
		plan, err := integrator.PrepareTaskIntegration(p.parentSession.WorkspacePath, expectedHead, children)
		if err != nil {
			return err
		}
		result, err := integrator.ApplyTaskIntegration(p.parentSession.WorkspacePath, plan)
		if err != nil {
			return err
		}
		parentHead = result.ResultingParentHead
	}
	for i, job := range p.record.Jobs {
		if job.StageID == stageID && !agentruntime.IsCoderAgentName(p.record.Definition.Jobs[i].AgentType) && job.State != pebblestore.TaskProgramJobCompleted {
			return fmt.Errorf("job %q did not complete", job.JobID)
		}
	}
	next := "advance_stage"
	var err error
	p.record, _, err = p.service.sessions.TransitionTaskProgram(p.parentSession.ID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: fmt.Sprintf("integrate:%d", p.record.Revision), ParentHead: &parentHead, NextAction: &next, Jobs: updates})
	return err
}

func (p *taskProgramScheduler) advanceStage(stageIndex int) error {
	stageID, next := p.record.Definition.Stages[stageIndex].ID, "launch_ready_jobs"
	var err error
	p.record, _, err = p.service.sessions.TransitionTaskProgram(p.parentSession.ID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: fmt.Sprintf("stage:%d:%s", p.record.Revision, stageID), ActiveStageID: &stageID, NextAction: &next})
	if err == nil {
		p.emitProgramProgress("stage.advanced", fmt.Sprintf("Advanced to stage %s", stageID))
	}
	return err
}

func (p *taskProgramScheduler) finishCompleted() (string, error) {
	state, next := pebblestore.TaskProgramStateCompleted, "none"
	record, _, err := p.service.sessions.TransitionTaskProgram(p.parentSession.ID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: fmt.Sprintf("complete:%d", p.record.Revision), State: &state, NextAction: &next})
	if err != nil {
		return "", err
	}
	p.record = record
	p.emitProgramProgress("program.completed", "Task Program completed")
	if err := p.service.permissions.FinishSubagentWave(p.parentSession.ID, p.req.RunID, p.call.CallID, "completed"); err != nil {
		return "", err
	}
	payload := taskProgramStatusPayload(record, true)
	payload["action"] = taskProgramActionStart
	payload["launches"] = p.allOutcomes
	payload["resulting_parent_head"] = record.ParentHead
	raw, err := json.Marshal(payload)
	return string(raw), err
}

func (p *taskProgramScheduler) finishFailed(runErr error) (string, error) {
	p.emitProgramProgress("program.failed", "Task Program failed")
	_ = p.service.permissions.FinishSubagentWave(p.parentSession.ID, p.req.RunID, p.call.CallID, "failed")
	status, _ := marshalTaskProgramStatus(p.record, false)
	return status, runErr
}

func (p *taskProgramScheduler) finishProgramError(runErr error) (string, error) {
	p.emitProgramProgress("program.blocked", "Task Program blocked")
	_ = p.service.permissions.FinishSubagentWave(p.parentSession.ID, p.req.RunID, p.call.CallID, "blocked")
	status, _ := marshalTaskProgramStatus(p.record, false)
	return status, runErr
}

func (p *taskProgramScheduler) finishBlocked(blockErr error) (string, error) {
	state, next := pebblestore.TaskProgramStateBlocked, "repair_integration_then_resume"
	blocker := p.structuredBlocker(taskProgramErrorCode(blockErr), blockErr, next, p.barrierJobID)
	record, _, err := p.service.sessions.TransitionTaskProgram(p.parentSession.ID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: fmt.Sprintf("blocked:%d", p.record.Revision), State: &state, NextAction: &next, Blocker: &blocker})
	if err != nil {
		return "", err
	}
	p.record = record
	return p.finishProgramError(blockErr)
}

func taskProgramErrorCode(err error) string {
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "dirty"), strings.Contains(message, "uncommitted"):
		return "dirty_worktree"
	case strings.Contains(message, "stale"), strings.Contains(message, "does not match stage base"):
		return "stale_base"
	case strings.Contains(message, "permission"), strings.Contains(message, "denied"):
		return "permission_denied"
	case strings.Contains(message, "conflict"), strings.Contains(message, "cherry-pick"):
		return "integration_conflict"
	case strings.Contains(message, "handoff"), strings.Contains(message, "ancestry"), strings.Contains(message, "descend"), strings.Contains(message, "missing child head"):
		return "invalid_handoff"
	case strings.Contains(message, "worktree"), strings.Contains(message, "allocate"):
		return "worktree_creation_failed"
	case strings.Contains(message, "undeclared"), strings.Contains(message, "no schedulable"), strings.Contains(message, "new declared program"):
		return "planning_required"
	default:
		return "child_execution_failed"
	}
}

func (p *taskProgramScheduler) structuredBlocker(code string, cause error, nextAction, jobID string) pebblestore.TaskProgramBlocker {
	blocker := pebblestore.TaskProgramBlocker{Code: code, Message: cause.Error(), NextAction: nextAction, RepairAction: nextAction, ProgramID: p.record.ProgramID, ProgramRevision: p.record.Revision + 1, ResumeGeneration: p.record.ResumeGeneration, StageID: p.record.ActiveStageID, JobID: jobID, ExpectedParentHead: firstNonEmptyString(p.expectedParentHead, p.record.ParentHead)}
	for _, job := range p.record.Jobs {
		if job.ChildSessionID == "" && job.WorkspacePath == "" && job.ChildHead == "" {
			continue
		}
		blocker.PreservedChildren = append(blocker.PreservedChildren, pebblestore.TaskProgramPreservedChild{JobID: job.JobID, State: job.State, AttemptNumber: job.AttemptNumber, ChildSessionID: job.ChildSessionID, WorkspacePath: job.WorkspacePath, WorktreeBranch: job.WorktreeBranch, ParentBranch: job.ParentBranch, ImmutableStageBase: job.ImmutableStageBase, ChildHead: job.ChildHead, IntegrationState: job.IntegrationState})
	}
	if index := taskProgramJobIndex(p.record, jobID); index >= 0 {
		blocker.AttemptNumber = p.record.Jobs[index].AttemptNumber
	}
	if code == "planning_required" {
		blocker.RepairAction = "submit_a_new_declared_program_revision"
		blocker.NextAction = "planning_required"
	}
	return blocker
}
