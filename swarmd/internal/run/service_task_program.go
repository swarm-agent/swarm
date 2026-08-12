package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func taskProgramDefinitionFromSpec(spec *taskProgramSpec) (pebblestore.TaskProgramDefinition, string, error) {
	if spec == nil {
		return pebblestore.TaskProgramDefinition{}, "", errors.New("task program is required")
	}
	definition := pebblestore.TaskProgramDefinition{}
	if spec.MaxConcurrency != nil {
		definition.MaxConcurrency = *spec.MaxConcurrency
	}
	for _, stage := range spec.Stages {
		definition.Stages = append(definition.Stages, pebblestore.TaskProgramStageSpec{
			ID: stage.ID, DependsOn: append([]string(nil), stage.DependsOn...), DependencyEvidence: stage.DependencyEvidence,
		})
	}
	for _, job := range spec.Jobs {
		definition.Jobs = append(definition.Jobs, pebblestore.TaskProgramJobSpec{
			ID: job.ID, StageID: job.StageID, DependsOn: append([]string(nil), job.DependsOn...), AgentType: job.RequestedSubagentType,
			Title: job.AssignmentLabel, MetaPrompt: job.MetaPrompt, Deliverable: job.Deliverable,
			OwnedScope: append([]string(nil), job.OwnedScope...), AcceptanceCriteria: append([]string(nil), job.AcceptanceCriteria...), DependencyEvidence: job.DependencyEvidence,
		})
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		return pebblestore.TaskProgramDefinition{}, "", err
	}
	sum := sha256.Sum256(raw)
	return definition, hex.EncodeToString(sum[:]), nil
}

func taskProgramInitialRecord(parentSessionID, reservationRunID, reservationCallID string, spec *taskProgramSpec) (pebblestore.TaskProgramRecord, error) {
	definition, hash, err := taskProgramDefinitionFromSpec(spec)
	if err != nil {
		return pebblestore.TaskProgramRecord{}, err
	}
	record := pebblestore.TaskProgramRecord{
		ParentSessionID: strings.TrimSpace(parentSessionID), ProgramID: spec.ID, DefinitionHash: hash, Definition: definition,
		ReservationRunID: strings.TrimSpace(reservationRunID), ReservationCallID: strings.TrimSpace(reservationCallID),
		State: pebblestore.TaskProgramStateDeclared, NextAction: "launch_ready_jobs",
	}
	if len(spec.Stages) > 0 {
		record.ActiveStageID = spec.Stages[0].ID
	}
	for _, job := range spec.Jobs {
		record.Jobs = append(record.Jobs, pebblestore.TaskProgramJobRecord{JobID: job.ID, StageID: job.StageID, State: pebblestore.TaskProgramJobDeclared})
	}
	return record, nil
}

const taskProgramPresentationVersion = 1

// taskProgramPresentationPayload is the privacy-bounded client contract for one
// durable Task Program. It intentionally omits prompts, reports, owned paths,
// Git details, and other execution-only fields.
func taskProgramPresentationPayload(record pebblestore.TaskProgramRecord) map[string]any {
	jobDefinitions := make(map[string]pebblestore.TaskProgramJobSpec, len(record.Definition.Jobs))
	jobOrder := make(map[string]int, len(record.Definition.Jobs))
	for i, definition := range record.Definition.Jobs {
		jobDefinitions[definition.ID] = definition
		jobOrder[definition.ID] = i + 1
	}

	jobsByStage := make(map[string][]map[string]any, len(record.Definition.Stages))
	counts := map[string]int{
		"declared": 0, "running": 0, "handoff_ready": 0, "integrated": 0,
		"blocked": 0, "failed": 0, "cancelled": 0, "completed": 0,
	}
	jobStates := make(map[string]string, len(record.Jobs))
	for _, job := range record.Jobs {
		jobStates[job.JobID] = strings.TrimSpace(job.State)
		if _, ok := counts[job.State]; ok {
			counts[job.State]++
		}
	}
	for _, job := range record.Jobs {
		definition := jobDefinitions[job.JobID]
		dependencyState := "satisfied"
		for _, dependency := range definition.DependsOn {
			state := jobStates[dependency]
			if state != pebblestore.TaskProgramJobIntegrated && state != pebblestore.TaskProgramJobCompleted {
				dependencyState = "waiting"
				break
			}
		}
		row := map[string]any{
			"job_id":              job.JobID,
			"stage_id":            job.StageID,
			"order":               jobOrder[job.JobID],
			"title":               boundedTaskProgramPresentationText(definition.Title),
			"agent_type":          strings.TrimSpace(definition.AgentType),
			"depends_on":          append([]string(nil), definition.DependsOn...),
			"dependency_state":    dependencyState,
			"dependency_evidence": boundedTaskProgramPresentationText(definition.DependencyEvidence),
			"state":               strings.TrimSpace(job.State),
			"attempt_number":      job.AttemptNumber,
		}
		if job.ChildSessionID != "" {
			row["child_session_id"] = strings.TrimSpace(job.ChildSessionID)
		}
		jobsByStage[job.StageID] = append(jobsByStage[job.StageID], row)
	}

	stages := make([]map[string]any, 0, len(record.Definition.Stages))
	for i, stage := range record.Definition.Stages {
		jobs := jobsByStage[stage.ID]
		stageState := taskProgramPresentationStageState(record, stage.ID, jobs)
		stages = append(stages, map[string]any{
			"stage_id":            stage.ID,
			"order":               i + 1,
			"depends_on":          append([]string(nil), stage.DependsOn...),
			"dependency_evidence": boundedTaskProgramPresentationText(stage.DependencyEvidence),
			"state":               stageState,
			"jobs":                jobs,
		})
	}

	return map[string]any{
		"kind":              "task_program",
		"version":           taskProgramPresentationVersion,
		"program_id":        record.ProgramID,
		"task_call_id":      record.ReservationCallID,
		"state":             record.State,
		"active_stage_id":   record.ActiveStageID,
		"revision":          record.Revision,
		"resume_generation": record.ResumeGeneration,
		"stages":            stages,
		"counts":            counts,
		"details_truncated": false,
	}
}

func taskProgramPresentationStageState(record pebblestore.TaskProgramRecord, stageID string, jobs []map[string]any) string {
	if stageID == record.ActiveStageID {
		switch record.State {
		case pebblestore.TaskProgramStateBlocked, pebblestore.TaskProgramStateFailed, pebblestore.TaskProgramStateCancelled:
			return record.State
		}
	}
	allComplete := len(jobs) > 0
	hasRunning := false
	for _, job := range jobs {
		state, _ := job["state"].(string)
		switch state {
		case pebblestore.TaskProgramJobFailed, pebblestore.TaskProgramJobBlocked, pebblestore.TaskProgramJobCancelled:
			return state
		case pebblestore.TaskProgramJobRunning, pebblestore.TaskProgramJobHandoffReady:
			hasRunning = true
		}
		if state != pebblestore.TaskProgramJobIntegrated && state != pebblestore.TaskProgramJobCompleted {
			allComplete = false
		}
	}
	if allComplete {
		return "completed"
	}
	if hasRunning {
		return "running"
	}
	if stageID == record.ActiveStageID {
		return "ready"
	}
	return "waiting"
}

func boundedTaskProgramPresentationText(value string) string {
	return truncateRunes(strings.TrimSpace(value), 512)
}

// taskProgramStreamMetadata adapts the privacy-bounded presentation contract to
// the existing task stream v2 client shape. Keeping one stream protocol lets
// Desktop and TUI merge stage snapshots with child launch patches as they arrive.
func taskProgramStreamMetadata(record pebblestore.TaskProgramRecord) (map[string]any, map[string]any) {
	stages := make([]map[string]any, 0, len(record.Definition.Stages))
	for _, stage := range record.Definition.Stages {
		stages = append(stages, map[string]any{
			"id":                  stage.ID,
			"depends_on":          append([]string(nil), stage.DependsOn...),
			"dependency_evidence": boundedTaskProgramPresentationText(stage.DependencyEvidence),
		})
	}
	jobs := make([]map[string]any, 0, len(record.Definition.Jobs))
	for _, job := range record.Definition.Jobs {
		jobs = append(jobs, map[string]any{
			"id":                  job.ID,
			"stage_id":            job.StageID,
			"title":               boundedTaskProgramPresentationText(job.Title),
			"agent_type":          strings.TrimSpace(job.AgentType),
			"depends_on":          append([]string(nil), job.DependsOn...),
			"dependency_evidence": boundedTaskProgramPresentationText(job.DependencyEvidence),
		})
	}
	statusJobs := make([]map[string]any, 0, len(record.Jobs))
	for _, job := range record.Jobs {
		row := map[string]any{
			"job_id":         job.JobID,
			"stage_id":       job.StageID,
			"state":          strings.TrimSpace(job.State),
			"attempt_number": job.AttemptNumber,
		}
		if job.ChildSessionID != "" {
			row["child_session_id"] = strings.TrimSpace(job.ChildSessionID)
		}
		statusJobs = append(statusJobs, row)
	}
	program := map[string]any{
		"id":     record.ProgramID,
		"stages": stages,
		"jobs":   jobs,
	}
	status := map[string]any{
		"program_id":      record.ProgramID,
		"program_state":   record.State,
		"active_stage_id": record.ActiveStageID,
		"revision":        record.Revision,
		"next_action":     record.NextAction,
		"jobs":            statusJobs,
	}
	return program, status
}

func taskProgramStatusPayload(record pebblestore.TaskProgramRecord, created bool) map[string]any {
	jobs := make([]map[string]any, 0, len(record.Jobs))
	counts := map[string]int{
		"declared": 0, "running": 0, "handoff_ready": 0, "integrated": 0, "blocked": 0, "failed": 0, "cancelled": 0, "completed": 0,
	}
	for _, job := range record.Jobs {
		state := strings.TrimSpace(job.State)
		if _, ok := counts[state]; ok {
			counts[state]++
		}
		row := map[string]any{
			"job_id": job.JobID, "stage_id": job.StageID, "state": state, "attempt_number": job.AttemptNumber,
			"resume_generation": job.ResumeGeneration, "integration_state": job.IntegrationState,
		}
		if job.ChildSessionID != "" {
			row["child_session_id"] = job.ChildSessionID
		}
		if job.WorkspacePath != "" {
			row["workspace_path"] = job.WorkspacePath
		}
		if job.WorktreeBranch != "" {
			row["worktree_branch"] = job.WorktreeBranch
		}
		if job.ParentBranch != "" {
			row["parent_branch"] = job.ParentBranch
		}
		if job.ImmutableStageBase != "" {
			row["immutable_stage_base"] = job.ImmutableStageBase
		}
		if job.ChildHead != "" {
			row["child_head"] = job.ChildHead
		}
		if job.Blocker != nil {
			row["blocker"] = job.Blocker
		}
		jobs = append(jobs, row)
	}
	payload := map[string]any{
		"tool": "task", "action": "status", "status": "ok", "program_id": record.ProgramID,
		"task_call_id": record.ReservationCallID, "parent_session_id": record.ParentSessionID, "created": created, "revision": record.Revision,
		"reservation_run_id": record.ReservationRunID, "reservation_call_id": record.ReservationCallID,
		"resume_generation": record.ResumeGeneration, "program_state": record.State, "active_stage_id": record.ActiveStageID,
		"parent_head": record.ParentHead, "next_action": record.NextAction, "jobs": jobs, "counts": counts,
		"program_presentation": taskProgramPresentationPayload(record), "details_truncated": false, "path_id": "tool.task_program.status.v1",
	}
	if record.Blocker != nil {
		payload["blocker"] = record.Blocker
	}
	return payload
}

func marshalTaskProgramStatus(record pebblestore.TaskProgramRecord, created bool) (string, error) {
	raw, err := json.Marshal(taskProgramStatusPayload(record, created))
	return string(raw), err
}

func taskProgramSpecFromRecord(record pebblestore.TaskProgramRecord) *taskProgramSpec {
	spec := &taskProgramSpec{ID: record.ProgramID}
	if record.Definition.MaxConcurrency > 0 {
		cap := record.Definition.MaxConcurrency
		spec.MaxConcurrency = &cap
	}
	for _, stage := range record.Definition.Stages {
		spec.Stages = append(spec.Stages, taskProgramStage{ID: stage.ID, DependsOn: append([]string(nil), stage.DependsOn...), DependencyEvidence: stage.DependencyEvidence})
	}
	for _, job := range record.Definition.Jobs {
		spec.Jobs = append(spec.Jobs, taskProgramJob{ID: job.ID, StageID: job.StageID, DependsOn: append([]string(nil), job.DependsOn...), RequestedSubagentType: job.AgentType, MetaPrompt: job.MetaPrompt, AssignmentLabel: job.Title, Deliverable: job.Deliverable, OwnedScope: append([]string(nil), job.OwnedScope...), AcceptanceCriteria: append([]string(nil), job.AcceptanceCriteria...), DependencyEvidence: job.DependencyEvidence})
	}
	return spec
}

func taskProgramLaunchesFromSpec(spec *taskProgramSpec) []taskLaunchSpec {
	launches := make([]taskLaunchSpec, 0, len(spec.Jobs))
	for _, job := range spec.Jobs {
		launches = append(launches, taskLaunchSpec{RequestedSubagentType: job.RequestedSubagentType, MetaPrompt: job.MetaPrompt, AssignmentLabel: job.AssignmentLabel, Deliverable: job.Deliverable, OwnedScope: append([]string(nil), job.OwnedScope...), DependencyEvidence: job.DependencyEvidence, SourceArguments: map[string]any{"program_id": spec.ID, "program_job_id": job.ID, "program_stage_id": job.StageID, "acceptance_criteria": append([]string(nil), job.AcceptanceCriteria...), "depends_on": append([]string(nil), job.DependsOn...)}})
	}
	return launches
}

func taskProgramResumeLaunches(prepared, prior pebblestore.TaskProgramRecord, spec *taskProgramSpec) []taskLaunchSpec {
	launches := taskProgramLaunchesFromSpec(spec)
	for i := range launches {
		if i >= len(prepared.Jobs) || prepared.Jobs[i].State != pebblestore.TaskProgramJobDeclared {
			continue
		}
		job := prepared.Jobs[i]
		if strings.TrimSpace(job.ChildSessionID) == "" {
			continue
		}
		launches[i].ResumeChildSessionID = strings.TrimSpace(job.ChildSessionID)
		launches[i].ResumeWorkspacePath = strings.TrimSpace(job.WorkspacePath)
		launches[i].ResumeWorktreeBranch = strings.TrimSpace(job.WorktreeBranch)
		launches[i].ResumeImmutableBase = strings.TrimSpace(job.ImmutableStageBase)
		launches[i].ResumeAttemptNumber = job.AttemptNumber
		if i < len(prior.Jobs) && prior.Jobs[i].Blocker != nil {
			launches[i].ResumeReason = firstNonEmptyString(prior.Jobs[i].Blocker.Message, prior.Jobs[i].Blocker.Code)
		}
		if launches[i].ResumeReason == "" && prior.Blocker != nil && (prior.Blocker.JobID == "" || prior.Blocker.JobID == job.JobID) {
			launches[i].ResumeReason = firstNonEmptyString(prior.Blocker.Message, prior.Blocker.Code)
		}
	}
	return launches
}

func taskProgramRecoveryPrompt(spec taskLaunchSpec) string {
	return fmt.Sprintf(`Resume the same interrupted Task Program job in this existing child session.

Before doing more work, inspect the durable conversation and current workspace state to determine what completed since the interruption. Preserve valid progress; do not blindly repeat completed steps. Continue under the original assignment and acceptance criteria, then finish with the normal child handoff. If completion is impossible, return one actionable blocker.

Recovery context:
- prior attempt: %d
- interruption: %s
- original assignment: %s`, spec.ResumeAttemptNumber, firstNonEmptyString(strings.TrimSpace(spec.ResumeReason), "the prior run stopped or was interrupted"), strings.TrimSpace(spec.MetaPrompt))
}

func (s *Service) prepareTaskProgramResume(parent pebblestore.SessionSnapshot, record pebblestore.TaskProgramRecord, parsed taskCallArguments) (pebblestore.TaskProgramRecord, int, error) {
	if record.Revision != parsed.ExpectedRevision || record.ResumeGeneration != parsed.ExpectedGeneration {
		if record.LastResumeRevision == parsed.ExpectedRevision && record.LastResumeGeneration == parsed.ExpectedGeneration && record.State == pebblestore.TaskProgramStateRunning && record.NextAction == "resume_program_scheduler" {
			readyCount := len(taskProgramReadyJobIndexes(record, taskProgramStageIndex(record)))
			if readyCount < 1 {
				readyCount = 1 // Capacity is still needed to resume an integration-only barrier.
			}
			if s.permissions == nil {
				return record, 0, errors.New("task program resume requires permission capacity service")
			}
			if _, capacityErr := s.permissions.ResumeSubagentProgramCapacity(parent.AccountScopeID, parent.ID, record.ReservationRunID, record.ReservationCallID, readyCount); capacityErr != nil {
				return record, 0, capacityErr
			}
			return record, readyCount, nil
		}
		return record, 0, fmt.Errorf("task program resume guard mismatch: current revision=%d generation=%d", record.Revision, record.ResumeGeneration)
	}
	if record.State == pebblestore.TaskProgramStateCompleted {
		return record, 0, nil
	}
	if record.State != pebblestore.TaskProgramStateBlocked && record.State != pebblestore.TaskProgramStateFailed && record.State != pebblestore.TaskProgramStateCancelled && record.State != pebblestore.TaskProgramStateRunning {
		return record, 0, fmt.Errorf("task program state %q is not resumable", record.State)
	}
	repairedParentHead := ""
	repairedJobs := make(map[string]bool)
	if s.worktrees != nil {
		base, baseErr := s.worktrees.ResolveTaskBase(parent.WorkspacePath)
		if baseErr != nil {
			return record, 0, fmt.Errorf("task program blocker is not resolved: %w", baseErr)
		}
		if record.ParentHead != "" && record.ParentHead != base.BaseCommit {
			if record.Blocker == nil || record.Blocker.Code != "integration_conflict" || record.Blocker.ExpectedParentHead != record.ParentHead {
				return record, 0, fmt.Errorf("task program blocker is not resolved: expected parent HEAD %s, found %s", record.ParentHead, base.BaseCommit)
			}
			for _, job := range record.Jobs {
				jobIndex := taskProgramJobIndex(record, job.JobID)
				if jobIndex < 0 || job.StageID != record.ActiveStageID || !agentruntime.IsCoderAgentName(record.Definition.Jobs[jobIndex].AgentType) {
					continue
				}
				integrated, integrationErr := s.worktrees.TaskCommitRangeIntegratedInto(parent.WorkspacePath, job.ImmutableStageBase, job.ChildHead, base.BaseCommit)
				if integrationErr != nil {
					return record, 0, fmt.Errorf("verify repaired task program integration for job %q: %w", job.JobID, integrationErr)
				}
				if !integrated {
					return record, 0, fmt.Errorf("task program blocker is not resolved: parent HEAD %s does not contain repaired integration for job %q", base.BaseCommit, job.JobID)
				}
				repairedJobs[job.JobID] = true
			}
			repairedParentHead = base.BaseCommit
		}
	}
	updates := make([]pebblestore.TaskProgramJobTransition, 0)
	readyCount := 0
	for i, job := range record.Jobs {
		if repairedJobs[job.JobID] {
			updates = append(updates, pebblestore.TaskProgramJobTransition{JobID: job.JobID, ExpectedState: job.State, State: pebblestore.TaskProgramJobIntegrated, ClearBlocker: true, IntegrationState: "integrated"})
			continue
		}
		if job.State == pebblestore.TaskProgramJobDeclared {
			readyCount++
			continue
		}
		if job.State != pebblestore.TaskProgramJobRunning && job.State != pebblestore.TaskProgramJobFailed && job.State != pebblestore.TaskProgramJobBlocked && job.State != pebblestore.TaskProgramJobCancelled {
			continue
		}
		definition := record.Definition.Jobs[i]
		childCompleted := false
		if strings.TrimSpace(job.ChildSessionID) != "" {
			lifecycle, ok, lifecycleErr := s.GetSessionLifecycle(job.ChildSessionID)
			if lifecycleErr != nil {
				return record, 0, fmt.Errorf("inspect task program child %q lifecycle: %w", job.JobID, lifecycleErr)
			}
			if ok {
				if lifecycle.Active || isLifecycleActivePhase(lifecycle.Phase) {
					return record, 0, fmt.Errorf("task program job %q child session %s is still active; wait for its durable outcome before resuming", job.JobID, job.ChildSessionID)
				}
				childCompleted = strings.EqualFold(strings.TrimSpace(lifecycle.Phase), lifecyclePhaseCompleted)
			}
		}
		if !agentruntime.IsCoderAgentName(definition.AgentType) || job.ChildSessionID == "" || job.WorkspacePath == "" {
			nextState, integration := pebblestore.TaskProgramJobDeclared, "resume_existing_child"
			if childCompleted {
				nextState, integration = pebblestore.TaskProgramJobCompleted, "not_required"
			}
			updates = append(updates, pebblestore.TaskProgramJobTransition{JobID: job.JobID, ExpectedState: job.State, State: nextState, ClearBlocker: true, IntegrationState: integration})
			if nextState == pebblestore.TaskProgramJobDeclared {
				readyCount++
			}
			continue
		}
		if s.worktrees == nil {
			return record, 0, errors.New("task program blocker is not resolved: worktree service is unavailable")
		}
		state, inspectErr := s.worktrees.InspectTaskWorkspace(job.WorkspacePath)
		if inspectErr != nil {
			return record, 0, fmt.Errorf("task program blocker is not resolved for job %q: %w", job.JobID, inspectErr)
		}
		if !state.Clean {
			updates = append(updates, pebblestore.TaskProgramJobTransition{JobID: job.JobID, ExpectedState: job.State, State: pebblestore.TaskProgramJobDeclared, ClearBlocker: true, IntegrationState: "resume_existing_child"})
			readyCount++
			continue
		}
		if job.WorktreeBranch != "" && state.BranchName != job.WorktreeBranch {
			return record, 0, fmt.Errorf("task program blocker is not resolved for job %q: expected branch %s, found %s", job.JobID, job.WorktreeBranch, state.BranchName)
		}
		if state.HeadCommit == "" || state.HeadCommit == job.ImmutableStageBase {
			updates = append(updates, pebblestore.TaskProgramJobTransition{JobID: job.JobID, ExpectedState: job.State, State: pebblestore.TaskProgramJobDeclared, ClearBlocker: true, IntegrationState: "resume_existing_child"})
			readyCount++
			continue
		}
		descends, ancestryErr := s.worktrees.TaskCommitDescendsFrom(job.WorkspacePath, job.ImmutableStageBase, state.HeadCommit)
		if ancestryErr != nil || !descends {
			return record, 0, fmt.Errorf("task program blocker is not resolved for job %q: repaired HEAD does not descend from immutable base", job.JobID)
		}
		updates = append(updates, pebblestore.TaskProgramJobTransition{JobID: job.JobID, ExpectedState: job.State, State: pebblestore.TaskProgramJobHandoffReady, ChildHead: state.HeadCommit, ClearBlocker: true, IntegrationState: "pending"})
	}
	if readyCount < 1 {
		readyCount = 1
	}
	if s.permissions == nil {
		return record, 0, errors.New("task program resume requires permission capacity service")
	}
	if strings.TrimSpace(record.ReservationRunID) == "" || strings.TrimSpace(record.ReservationCallID) == "" {
		return record, 0, errors.New("task program resume identity is unavailable; the stored program predates guarded resume support")
	}
	if _, capacityErr := s.permissions.ResumeSubagentProgramCapacity(parent.AccountScopeID, parent.ID, record.ReservationRunID, record.ReservationCallID, readyCount); capacityErr != nil {
		return record, 0, capacityErr
	}
	state, next := pebblestore.TaskProgramStateRunning, "resume_program_scheduler"
	mutationID := fmt.Sprintf("resume:%d:%d", parsed.ExpectedRevision, parsed.ExpectedGeneration)
	transition := pebblestore.TaskProgramTransition{ExpectedRevision: parsed.ExpectedRevision, MutationID: mutationID, State: &state, NextAction: &next, ClearBlocker: true, Jobs: updates, IncrementResumeGeneration: true, ResumeFromRevision: parsed.ExpectedRevision, ResumeFromGeneration: parsed.ExpectedGeneration}
	if repairedParentHead != "" {
		transition.ParentHead = &repairedParentHead
	}
	returnRecord, _, err := s.sessions.TransitionTaskProgram(parent.ID, record.ProgramID, transition)
	return returnRecord, readyCount, err
}

func taskProgramRunningTransitions(spec *taskProgramSpec, prepared []taskLaunchPrepared, generation int) []pebblestore.TaskProgramJobTransition {
	updates := make([]pebblestore.TaskProgramJobTransition, 0, len(prepared))
	for i, launch := range prepared {
		if i >= len(spec.Jobs) {
			break
		}
		updates = append(updates, pebblestore.TaskProgramJobTransition{
			JobID: spec.Jobs[i].ID, ExpectedState: pebblestore.TaskProgramJobDeclared, State: pebblestore.TaskProgramJobRunning,
			AttemptNumber: 1, ResumeGeneration: generation, ChildSessionID: launch.ChildSession.ID,
			WorkspacePath: launch.ChildSession.WorkspacePath, WorktreeBranch: launch.ChildSession.WorktreeBranch,
			ImmutableStageBase: func() string {
				if launch.TaskBase != nil {
					return launch.TaskBase.BaseCommit
				}
				return ""
			}(),
			ParentBranch: func() string {
				if launch.TaskBase != nil {
					return launch.TaskBase.ParentBranch
				}
				return ""
			}(),
			IntegrationState: func() string {
				if agentruntime.IsCoderAgentName(launch.RequestedSubagent) {
					return "pending_handoff"
				}
				return "not_required"
			}(),
		})
	}
	return updates
}

func taskProgramOutcomeTransitions(spec *taskProgramSpec, outcomes []taskLaunchOutcome, runErrs []error) []pebblestore.TaskProgramJobTransition {
	updates := make([]pebblestore.TaskProgramJobTransition, 0, len(outcomes))
	for i, outcome := range outcomes {
		if i >= len(spec.Jobs) {
			break
		}
		state, integration := pebblestore.TaskProgramJobCompleted, "not_required"
		var blocker *pebblestore.TaskProgramBlocker
		if i < len(runErrs) && runErrs[i] != nil {
			state = pebblestore.TaskProgramJobFailed
			if strings.EqualFold(strings.TrimSpace(outcome.Phase), "cancelled") {
				state = pebblestore.TaskProgramJobCancelled
			}
			integration = "blocked"
			blocker = &pebblestore.TaskProgramBlocker{Code: "child_handoff_failed", Message: firstNonEmptyString(outcome.Reason, outcome.Error, runErrs[i].Error()), NextAction: "repair_or_resume_program"}
		} else if agentruntime.IsCoderAgentName(spec.Jobs[i].RequestedSubagentType) {
			state, integration = pebblestore.TaskProgramJobHandoffReady, "pending"
		}
		updates = append(updates, pebblestore.TaskProgramJobTransition{
			JobID: spec.Jobs[i].ID, ExpectedState: pebblestore.TaskProgramJobRunning, State: state,
			ChildSessionID: outcome.ChildSessionID, WorkspacePath: outcome.WorkspacePath, WorktreeBranch: outcome.WorktreeBranch,
			ParentBranch: outcome.ParentBranch, ImmutableStageBase: outcome.BaseCommit, ChildHead: outcome.HeadCommit,
			IntegrationState: integration, Blocker: blocker,
		})
	}
	return updates
}
