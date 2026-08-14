package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
			OwnedScope: append([]string(nil), job.OwnedScope...), OutputMode: job.OutputMode, OutputRequirements: cloneTaskOutputRequirements(job.OutputRequirements), AcceptanceCriteria: append([]string(nil), job.AcceptanceCriteria...), DependencyEvidence: job.DependencyEvidence,
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
		definition, definitionFound := jobDefinitions[job.JobID]
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
			"output_requirements": definition.OutputRequirements,
			"state":               strings.TrimSpace(job.State),
			"attempt_number":      job.AttemptNumber,
		}
		if job.ChildSessionID != "" {
			row["child_session_id"] = strings.TrimSpace(job.ChildSessionID)
		}
		if job.CurrentSessionID != "" {
			row["current_session_id"] = strings.TrimSpace(job.CurrentSessionID)
			row["current_generation"] = job.CurrentGeneration
		}
		if definitionFound {
			if reference := taskProgramReadyArtifactReference(record, definition, job); reference != nil {
				row["artifact_reference"] = reference
				row["artifact_status"] = reference.Status
			}
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
	artifactReferences := make([]*taskArtifactReference, 0)
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
		if job.CurrentSessionID != "" {
			row["current_session_id"] = strings.TrimSpace(job.CurrentSessionID)
			row["current_generation"] = job.CurrentGeneration
		}
		if definitionIndex := taskProgramDefinitionJobIndex(record, job.JobID); definitionIndex >= 0 {
			if reference := taskProgramReadyArtifactReference(record, record.Definition.Jobs[definitionIndex], job); reference != nil {
				row["artifact_reference"] = reference
				row["artifact_status"] = reference.Status
				artifactReferences = append(artifactReferences, reference)
			}
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
		"artifact_count":  len(artifactReferences),
	}
	if len(artifactReferences) > 0 {
		status["artifact_references"] = artifactReferences
	}
	return program, status
}

func taskProgramStatusPayload(record pebblestore.TaskProgramRecord, created bool) map[string]any {
	jobs := make([]map[string]any, 0, len(record.Jobs))
	artifactReferences := make([]*taskArtifactReference, 0)
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
			"integration_state": job.IntegrationState,
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
		if job.HandoffRef != nil {
			row["handoff_ref"] = job.HandoffRef
		}
		if job.Blocker != nil {
			row["blocker"] = job.Blocker
		}
		if definitionIndex := taskProgramDefinitionJobIndex(record, job.JobID); definitionIndex >= 0 {
			if reference := taskProgramReadyArtifactReference(record, record.Definition.Jobs[definitionIndex], job); reference != nil {
				row["artifact_reference"] = reference
				row["artifact_status"] = reference.Status
				artifactReferences = append(artifactReferences, reference)
			}
		}
		jobs = append(jobs, row)
	}
	payload := map[string]any{
		"tool": "task", "action": "status", "status": "ok", "program_id": record.ProgramID,
		"task_call_id": record.ReservationCallID, "parent_session_id": record.ParentSessionID, "created": created, "revision": record.Revision,
		"reservation_run_id": record.ReservationRunID, "reservation_call_id": record.ReservationCallID,
		"program_state": record.State, "active_stage_id": record.ActiveStageID,
		"parent_head": record.ParentHead, "next_action": record.NextAction, "jobs": jobs, "counts": counts,
		"artifact_count": len(artifactReferences), "program_presentation": taskProgramPresentationPayload(record), "details_truncated": false, "path_id": "tool.task_program.status.v1",
	}
	if len(artifactReferences) > 0 {
		payload["artifact_references"] = artifactReferences
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

func taskProgramRunningTransitions(spec *taskProgramSpec, prepared []taskLaunchPrepared) []pebblestore.TaskProgramJobTransition {
	updates := make([]pebblestore.TaskProgramJobTransition, 0, len(prepared))
	for i, launch := range prepared {
		if i >= len(spec.Jobs) {
			break
		}
		updates = append(updates, pebblestore.TaskProgramJobTransition{
			JobID: spec.Jobs[i].ID, ExpectedState: pebblestore.TaskProgramJobDeclared, State: pebblestore.TaskProgramJobRunning,
			AttemptNumber: 1, ChildSessionID: launch.ChildSession.ID,
			CurrentSessionID: launch.ChildSession.ID, CurrentGeneration: 1,
			GenerationHistory: []pebblestore.TaskProgramJobGeneration{{Generation: 1, SessionID: launch.ChildSession.ID, State: pebblestore.DelegatedChildGenerationActive}},
			WorkspacePath:     launch.ChildSession.WorkspacePath, WorktreeBranch: launch.ChildSession.WorktreeBranch,
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

func taskProgramDefinitionUsesManagedDesigner(definition pebblestore.TaskProgramJobSpec) bool {
	if !agentruntime.IsDesignerAgentName(definition.AgentType) {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(definition.OutputMode))
	return mode == taskOutputModeManaged || (mode == "" && len(definition.OwnedScope) == 0)
}

func taskProgramSpecUsesManagedDesigner(job taskProgramJob) bool {
	if !agentruntime.IsDesignerAgentName(job.RequestedSubagentType) {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(job.OutputMode))
	return mode == taskOutputModeManaged || (mode == "" && len(job.OwnedScope) == 0)
}

func taskProgramExpectedArtifactReference(record pebblestore.TaskProgramRecord, jobID string) *taskArtifactReference {
	routingID := taskManagedArtifactRoutingID(record.ReservationCallID, record.ProgramID)
	return &taskArtifactReference{
		SessionID:    strings.TrimSpace(record.ParentSessionID),
		CollectionID: taskManagedArtifactID("collection", record.ParentSessionID, routingID, 0),
		VariantID:    taskManagedArtifactID("variant", record.ParentSessionID, routingID+"\x00job:"+strings.TrimSpace(jobID), 0),
		Status:       pebblestore.SessionArtifactStatusReady,
	}
}

func taskProgramReadyArtifactReference(record pebblestore.TaskProgramRecord, definition pebblestore.TaskProgramJobSpec, job pebblestore.TaskProgramJobRecord) *taskArtifactReference {
	if !taskProgramDefinitionUsesManagedDesigner(definition) || job.State != pebblestore.TaskProgramJobCompleted || job.IntegrationState != "artifact_ready" {
		return nil
	}
	return taskProgramExpectedArtifactReference(record, job.JobID)
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
			code := "child_handoff_failed"
			if taskProgramSpecUsesManagedDesigner(spec.Jobs[i]) && !strings.EqualFold(strings.TrimSpace(outcome.Phase), "cancelled") {
				code, integration = "managed_artifact_invalid", "artifact_invalid"
			}
			blocker = &pebblestore.TaskProgramBlocker{Code: code, Message: firstNonEmptyString(outcome.Reason, outcome.Error, runErrs[i].Error()), NextAction: "author_new_program_for_remaining_work"}
		} else if agentruntime.IsCoderAgentName(spec.Jobs[i].RequestedSubagentType) {
			state, integration = pebblestore.TaskProgramJobHandoffReady, "pending"
		} else if agentruntime.IsFinderAgentName(spec.Jobs[i].RequestedSubagentType) && taskProgramDurableHandoffRef(outcome.ReportRef) == nil {
			state, integration = pebblestore.TaskProgramJobFailed, "handoff_missing"
			blocker = &pebblestore.TaskProgramBlocker{Code: "finder_handoff_missing", Message: "Finder completed without a valid durable report reference", NextAction: "author_new_program_for_remaining_work"}
		} else if taskProgramSpecUsesManagedDesigner(spec.Jobs[i]) {
			if outcome.ArtifactReference == nil || outcome.ArtifactReference.Status != pebblestore.SessionArtifactStatusReady {
				state, integration = pebblestore.TaskProgramJobFailed, "artifact_invalid"
				blocker = &pebblestore.TaskProgramBlocker{Code: "managed_artifact_invalid", Message: "managed Designer completed without a validated ready artifact", NextAction: "author_new_program_for_remaining_work"}
			} else {
				integration = "artifact_ready"
			}
		}
		var handoffRef *pebblestore.TaskProgramHandoffRef
		if agentruntime.IsFinderAgentName(spec.Jobs[i].RequestedSubagentType) {
			handoffRef = taskProgramDurableHandoffRef(outcome.ReportRef)
		}
		updates = append(updates, pebblestore.TaskProgramJobTransition{
			JobID: spec.Jobs[i].ID, ExpectedState: pebblestore.TaskProgramJobRunning, State: state,
			ChildSessionID: outcome.ChildSessionID, CurrentSessionID: outcome.ChildSessionID, WorkspacePath: outcome.WorkspacePath, WorktreeBranch: outcome.WorktreeBranch,
			ParentBranch: outcome.ParentBranch, ImmutableStageBase: outcome.BaseCommit, ChildHead: outcome.HeadCommit,
			IntegrationState: integration, HandoffRef: handoffRef, Blocker: blocker,
		})
	}
	return updates
}

func taskProgramDurableHandoffRef(ref *taskReportRef) *pebblestore.TaskProgramHandoffRef {
	if ref == nil || strings.TrimSpace(ref.SessionID) == "" || strings.TrimSpace(ref.MessageID) == "" || ref.GlobalSeq == 0 {
		return nil
	}
	return &pebblestore.TaskProgramHandoffRef{SessionID: strings.TrimSpace(ref.SessionID), MessageID: strings.TrimSpace(ref.MessageID), GlobalSeq: ref.GlobalSeq}
}
