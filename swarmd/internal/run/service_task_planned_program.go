package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

// resolveApprovedCheckpointTaskProgram replaces the zero-copy start shorthand
// with the canonical program stored on the active approved checkpoint. The same
// parser used by explicit task starts revalidates the definition before launch.
func (s *Service) resolveApprovedCheckpointTaskProgram(sessionID string, parsed taskCallArguments) (taskCallArguments, error) {
	if !parsed.PlannedProgram {
		return parsed, nil
	}
	if s == nil || s.sessions == nil {
		return taskCallArguments{}, errors.New("planned task program start requires session service")
	}
	plan, ok, err := s.sessions.GetActivePlan(strings.TrimSpace(sessionID))
	if err != nil {
		return taskCallArguments{}, err
	}
	if !ok || plan.Document == nil {
		return taskCallArguments{}, errors.New("planned task program start requires an active structured plan")
	}
	if plan.ApprovalState != "approved" || plan.Status != "approved" {
		return taskCallArguments{}, errors.New("planned task program requires an approved plan")
	}
	doc := plan.Document
	checkpointID := strings.TrimSpace(doc.ActiveCheckpointID)
	if checkpointID == "" {
		return taskCallArguments{}, errors.New("planned task program start requires a runnable active checkpoint")
	}
	index := findPlanRunCheckpointIndex(doc.Checkpoints, checkpointID)
	if index < 0 {
		return taskCallArguments{}, fmt.Errorf("planned task program checkpoint %q was not found", checkpointID)
	}
	if doc.Checkpoints[index].Status != "in_progress" || (doc.ExecutionState != nil && doc.ExecutionState.Status != "" && doc.ExecutionState.Status != "in_progress") {
		return taskCallArguments{}, errors.New("planned task program start requires an in-progress active checkpoint")
	}
	checkpoint := doc.Checkpoints[index]
	if doc.ExecutionState != nil && doc.ExecutionState.ActiveAttemptID != "" && checkpoint.AttemptID != doc.ExecutionState.ActiveAttemptID {
		return taskCallArguments{}, errors.New("planned task program checkpoint attempt is stale")
	}
	program := checkpoint.TaskProgram
	if program == nil {
		return taskCallArguments{}, fmt.Errorf("active checkpoint %q has no approved task_program", checkpointID)
	}
	if err := sessionruntime.ValidatePlanTaskProgramDefinition(program); err != nil {
		return taskCallArguments{}, fmt.Errorf("approved checkpoint task_program is invalid: %w", err)
	}
	raw, err := json.Marshal(program)
	if err != nil {
		return taskCallArguments{}, fmt.Errorf("marshal approved checkpoint task_program: %w", err)
	}
	var programObject map[string]any
	if err := json.Unmarshal(raw, &programObject); err != nil {
		return taskCallArguments{}, fmt.Errorf("decode approved checkpoint task_program: %w", err)
	}
	prompt := strings.TrimSpace(parsed.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(doc.Checkpoints[index].Objective)
	}
	if prompt == "" {
		prompt = strings.TrimSpace(doc.Checkpoints[index].Title)
	}
	args := map[string]any{
		"action":      taskProgramActionStart,
		"description": parsed.Description,
		"prompt":      prompt,
		"mode":        taskModeRegular,
		"program":     programObject,
	}
	if workspacePath := strings.TrimSpace(parsed.ProgramWorkspacePath); workspacePath != "" {
		args["workspace_path"] = workspacePath
	}
	resolvedProgram, launches, err := parseTaskProgram(args, prompt)
	if err != nil {
		return taskCallArguments{}, fmt.Errorf("approved checkpoint task_program failed launch validation: %w", err)
	}
	parsed.ProgramWorkspacePath = strings.TrimSpace(mapString(args, "workspace_path"))
	parsed.Program = resolvedProgram
	parsed.ProgramID = resolvedProgram.ID
	parsed.Launches = launches
	parsed.Prompt = prompt
	parsed.PlannedProgram = false
	parsed.SourceArguments = args
	return parsed, nil
}
