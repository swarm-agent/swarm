package session

import (
	"fmt"
	"regexp"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/taskscope"
)

var planTaskProgramIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// ValidatePlanTaskProgramDefinition validates the durable, approval-visible
// implementation graph. It intentionally does not create runtime state.
func ValidatePlanTaskProgramDefinition(program *pebblestore.TaskProgramDefinition) error {
	if program == nil {
		return nil
	}
	if !planTaskProgramIDPattern.MatchString(strings.TrimSpace(program.ID)) {
		return fmt.Errorf("id must match ^[a-z][a-z0-9_-]{0,63}$")
	}
	if len(program.Stages) == 0 {
		return fmt.Errorf("stages must be a non-empty array")
	}
	if len(program.Jobs) == 0 {
		return fmt.Errorf("jobs must be a non-empty array")
	}
	if program.MaxConcurrency < 0 || (program.MaxConcurrency > 0 && program.MaxConcurrency > len(program.Jobs)) {
		return fmt.Errorf("max_concurrency must be positive and cannot exceed total job count")
	}

	stageIndexes := make(map[string]int, len(program.Stages))
	stageJobs := make(map[string]int, len(program.Stages))
	for i, stage := range program.Stages {
		stageID := strings.TrimSpace(stage.ID)
		if !planTaskProgramIDPattern.MatchString(stageID) {
			return fmt.Errorf("stages[%d].id must match ^[a-z][a-z0-9_-]{0,63}$", i)
		}
		if _, exists := stageIndexes[stageID]; exists {
			return fmt.Errorf("stages[%d].id duplicates %q", i, stageID)
		}
		if strings.TrimSpace(stage.DependencyEvidence) == "" {
			return fmt.Errorf("stages[%d].dependency_evidence is required", i)
		}
		if i > 0 && len(stage.DependsOn) == 0 {
			return fmt.Errorf("stages[%d].depends_on must identify an earlier stage", i)
		}
		for _, dependency := range stage.DependsOn {
			dependency = strings.TrimSpace(dependency)
			if index, exists := stageIndexes[dependency]; !exists || index >= i {
				return fmt.Errorf("stages[%d].depends_on %q must identify an earlier stage", i, dependency)
			}
		}
		stageIndexes[stageID] = i
	}

	jobIndexes := make(map[string]int, len(program.Jobs))
	jobStages := make(map[string]int, len(program.Jobs))
	for i, job := range program.Jobs {
		jobID := strings.TrimSpace(job.ID)
		if !planTaskProgramIDPattern.MatchString(jobID) {
			return fmt.Errorf("jobs[%d].id must match ^[a-z][a-z0-9_-]{0,63}$", i)
		}
		if _, exists := jobIndexes[jobID]; exists {
			return fmt.Errorf("jobs[%d].id duplicates %q", i, jobID)
		}
		stageID := strings.TrimSpace(job.StageID)
		stageIndex, exists := stageIndexes[stageID]
		if !exists {
			return fmt.Errorf("jobs[%d].stage_id references unknown stage %q", i, stageID)
		}
		agentType := strings.ToLower(strings.TrimSpace(job.AgentType))
		if agentType != "coder" && agentType != "finder" && agentType != "designer" {
			return fmt.Errorf("jobs[%d].agent_type must be coder, finder, or designer", i)
		}
		if strings.TrimSpace(job.WorkspacePath) != "" && agentType != "coder" && agentType != "finder" {
			return fmt.Errorf("jobs[%d].workspace_path is supported only for Coder or Finder", i)
		}
		if strings.TrimSpace(job.MetaPrompt) == "" || strings.TrimSpace(job.Title) == "" || strings.TrimSpace(job.Deliverable) == "" || strings.TrimSpace(job.DependencyEvidence) == "" {
			return fmt.Errorf("jobs[%d] requires meta_prompt, title, deliverable, and dependency_evidence", i)
		}
		if len(trimStringSlice(job.AcceptanceCriteria)) == 0 {
			return fmt.Errorf("jobs[%d].acceptance_criteria must be a non-empty array", i)
		}
		mode := strings.ToLower(strings.TrimSpace(job.OutputMode))
		if agentType == "designer" {
			if mode == "" {
				mode = "managed"
			}
			if mode != "managed" && mode != "workspace" {
				return fmt.Errorf("jobs[%d].output_mode must be managed or workspace", i)
			}
			if mode == "managed" && len(job.OwnedScope) != 0 {
				return fmt.Errorf("jobs[%d] managed Designer must omit owned_scope", i)
			}
			if mode == "workspace" && len(job.OwnedScope) == 0 {
				return fmt.Errorf("jobs[%d] workspace Designer requires owned_scope", i)
			}
		} else {
			if mode != "" {
				return fmt.Errorf("jobs[%d] non-Designer cannot set output_mode", i)
			}
			if len(job.OwnedScope) == 0 {
				return fmt.Errorf("jobs[%d] requires owned_scope", i)
			}
		}
		for scopeIndex, scope := range job.OwnedScope {
			canonical, _, _ := taskscope.Canonical(scope)
			if agentType == "designer" && mode == "workspace" && (strings.ContainsAny(scope, "*?[]") || canonical != strings.TrimSpace(scope)) {
				return fmt.Errorf("jobs[%d].owned_scope[%d] must be a concrete clean workspace-relative path", i, scopeIndex)
			}
			if err := validatePlanTaskProgramScope(scope); err != nil {
				return fmt.Errorf("jobs[%d].owned_scope[%d]: %w", i, scopeIndex, err)
			}
		}
		for _, dependency := range job.DependsOn {
			dependency = strings.TrimSpace(dependency)
			dependencyIndex, exists := jobIndexes[dependency]
			if !exists || dependencyIndex >= i || jobStages[dependency] >= stageIndex {
				return fmt.Errorf("jobs[%d].depends_on %q must identify an earlier-stage job", i, dependency)
			}
		}
		jobIndexes[jobID] = i
		jobStages[jobID] = stageIndex
		stageJobs[stageID]++
	}
	for i, stage := range program.Stages {
		if stageJobs[strings.TrimSpace(stage.ID)] == 0 {
			return fmt.Errorf("stages[%d] %q has no jobs", i, stage.ID)
		}
	}
	coderWorkspacePath := ""
	coderWorkspaceFound := false
	for _, job := range program.Jobs {
		if !strings.EqualFold(strings.TrimSpace(job.AgentType), "coder") {
			continue
		}
		workspacePath := strings.TrimSpace(job.WorkspacePath)
		if !coderWorkspaceFound {
			coderWorkspacePath, coderWorkspaceFound = workspacePath, true
			continue
		}
		if workspacePath != coderWorkspacePath {
			return fmt.Errorf("Coder jobs must target one workspace so staged integration has one durable parent Git history")
		}
	}
	for i := range program.Jobs {
		for j := i + 1; j < len(program.Jobs); j++ {
			left, right := program.Jobs[i], program.Jobs[j]
			if strings.TrimSpace(left.StageID) != strings.TrimSpace(right.StageID) {
				continue
			}
			if strings.EqualFold(left.AgentType, "coder") && strings.EqualFold(right.AgentType, "coder") && taskProgramJobsShareWorkspace(left, right) && planTaskProgramScopesOverlap(left.OwnedScope, right.OwnedScope) {
				return fmt.Errorf("concurrent Coder owned scopes overlap between jobs %q and %q", left.ID, right.ID)
			}
			if strings.EqualFold(left.AgentType, "designer") && strings.EqualFold(right.AgentType, "designer") && strings.EqualFold(left.OutputMode, "workspace") && strings.EqualFold(right.OutputMode, "workspace") && planTaskProgramScopesOverlap(left.OwnedScope, right.OwnedScope) {
				return fmt.Errorf("concurrent workspace Designer owned scopes overlap between jobs %q and %q", left.ID, right.ID)
			}
		}
	}
	return nil
}

func taskProgramJobsShareWorkspace(left, right pebblestore.TaskProgramJobSpec) bool {
	return strings.TrimSpace(left.WorkspacePath) == strings.TrimSpace(right.WorkspacePath)
}

func validatePlanTaskProgramScope(scope string) error {
	return taskscope.ValidateProgram(scope)
}

func planTaskProgramScopesOverlap(left, right []string) bool {
	for _, leftScope := range left {
		leftScope, _, _ = taskscope.Canonical(leftScope)
		for _, rightScope := range right {
			rightScope, _, _ = taskscope.Canonical(rightScope)
			if leftScope == rightScope || strings.HasPrefix(leftScope, rightScope+"/") || strings.HasPrefix(rightScope, leftScope+"/") {
				return true
			}
		}
	}
	return false
}
