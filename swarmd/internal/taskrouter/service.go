package taskrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// LLMInvoker executes an AI generation turn with instructions and input.
type LLMInvoker func(ctx context.Context, instructions string, input string) (string, error)

// TaskRouteOptions encapsulates user request, explicit intent, aspect ratios, and project context.
type TaskRouteOptions struct {
	Prompt             string                     `json:"prompt"`
	RequestedWorkspace string                     `json:"requested_workspace,omitempty"`
	Intent             string                     `json:"intent,omitempty"` // "code", "image", "video", "audit"
	AspectRatio        string                     `json:"aspect_ratio,omitempty"`
	VariantCount       int                        `json:"variant_count,omitempty"`
	ScenesCount        int                        `json:"scenes_count,omitempty"`
	Soundtrack         string                     `json:"soundtrack,omitempty"`
	AutoApprove        bool                       `json:"auto_approve,omitempty"`
	Feedback           string                     `json:"feedback,omitempty"`
	LastError          string                     `json:"last_error,omitempty"`
	Project            *pebblestore.ProjectRecord `json:"project,omitempty"`
}

// Service provides real AI task routing, scene compilation, and context pool formulation.
type Service struct {
	invoker LLMInvoker
}

// NewService instantiates a new Task Router Service with an optional AI invoker.
func NewService(invoker ...LLMInvoker) *Service {
	s := &Service{}
	if len(invoker) > 0 && invoker[0] != nil {
		s.invoker = invoker[0]
	}
	return s
}

// RouteTask evaluates a user task request. When an AI invoker is present, it invokes
// the real configured Router LLM to analyze the request, project workspaces, and guidelines.
// If the LLM is offline, not configured, or fails, it falls back directly to the canonical
// Swarm system agent and attaches a clear RouterAlert warning.
func (s *Service) RouteTask(ctx context.Context, opts TaskRouteOptions) pebblestore.TaskRouteResult {
	var workspaces []pebblestore.ProjectWorkspaceRef
	var projectContext string
	if opts.Project != nil {
		workspaces = opts.Project.Workspaces
		projectContext = opts.Project.ProjectContext
	}

	planOpts := pebblestore.TaskPlanOptions{
		Prompt:             opts.Prompt,
		RequestedWorkspace: opts.RequestedWorkspace,
		ProjectContext:     projectContext,
		Workspaces:         workspaces,
		Feedback:           opts.Feedback,
		LastError:          opts.LastError,
		Intent:             opts.Intent,
		AspectRatio:        opts.AspectRatio,
		VariantCount:       opts.VariantCount,
		ScenesCount:        opts.ScenesCount,
		Soundtrack:         opts.Soundtrack,
	}

	var routerErr error
	if s.invoker != nil {
		aiResult, err := s.invokeAIRouter(ctx, opts, planOpts)
		if err == nil {
			return aiResult
		}
		routerErr = err
	} else {
		routerErr = fmt.Errorf("router agent invoker not configured")
	}

	// AI router failed or unavailable — fallback to default Swarm agent with router alert
	fallback := pebblestore.RouteAndPlanProjectTaskWithOptions(planOpts)
	fallback.RouterAlert = fmt.Sprintf("Router agent failed (%v). Defaulted to Swarm system agent.", routerErr)
	return fallback
}

// RefineTask recalculates plan stages and workspace boundaries based on user feedback or execution errors.
func (s *Service) RefineTask(ctx context.Context, opts TaskRouteOptions) pebblestore.TaskRouteResult {
	return s.RouteTask(ctx, opts)
}

func (s *Service) invokeAIRouter(ctx context.Context, opts TaskRouteOptions, planOpts pebblestore.TaskPlanOptions) (pebblestore.TaskRouteResult, error) {
	instructions := strings.TrimSpace(`You are the Swarm AI Task Router. Your role is to analyze user requests, project guidelines (PROJECT.md), and project workspaces to produce an authoritative, high-context execution plan and routing contract.

Rules:
1. Intent Classification:
   - "code": Software features, bug fixes, refactoring.
     * If 1 workspace affected: tier="direct", agent="coder", outcome_type="code_pr" (or "bug_patch" if bug).
     * If multiple workspaces affected: tier="complex", agent="coder", stage 1="Cross-Workspace Discovery (Finder)".
   - "audit": Investigation, architecture questions, diagnostics. tier="discovery", agent="finder", outcome_type="audit_report".
   - "image": Visual assets, UI designs, mockups. tier="direct", agent="designer", outcome_type="media_bundle".
   - "video": Multi-part video stories, demos, teasers. tier="direct", agent="video", outcome_type="video_story". Compile 2-4 scenes with durations, visual prompts, camera directions, and soundtrack.
2. Return ONLY one JSON object matching this schema:
{
  "title": "string",
  "agent": "coder|finder|designer|video",
  "tier": "direct|discovery|complex",
  "outcome_type": "code_pr|bug_patch|audit_report|media_bundle|video_story",
  "hero_workspace": "string",
  "workspaces_involved": ["string"],
  "branch": "string",
  "mission": "string",
  "stages": ["string"],
  "plan_summary": "string",
  "full_plan_markdown": "string",
  "scenes": [{"scene_number": 1, "title": "string", "duration_sec": 4, "prompt": "string", "visual_notes": "string"}],
  "soundtrack": "string"
}`)

	inputPayload := map[string]any{
		"prompt":              opts.Prompt,
		"intent":              opts.Intent,
		"requested_workspace": opts.RequestedWorkspace,
		"aspect_ratio":        opts.AspectRatio,
		"variant_count":       opts.VariantCount,
		"scenes_count":        opts.ScenesCount,
		"soundtrack":          opts.Soundtrack,
		"feedback":            opts.Feedback,
		"last_error":          opts.LastError,
		"project_workspaces":  planOpts.Workspaces,
		"project_guidelines":  planOpts.ProjectContext,
	}

	payloadBytes, err := json.Marshal(inputPayload)
	if err != nil {
		return pebblestore.TaskRouteResult{}, err
	}

	rawText, err := s.invoker(ctx, instructions, string(payloadBytes))
	if err != nil {
		return pebblestore.TaskRouteResult{}, err
	}

	// Clean Markdown code fence if wrapped
	cleaned := strings.TrimSpace(rawText)
	if strings.HasPrefix(cleaned, "```") {
		firstNL := strings.IndexByte(cleaned, '\n')
		if firstNL >= 0 {
			cleaned = cleaned[firstNL+1:]
		}
		if lastFence := strings.LastIndex(cleaned, "```"); lastFence >= 0 {
			cleaned = cleaned[:lastFence]
		}
		cleaned = strings.TrimSpace(cleaned)
	}

	var output struct {
		Title              string                         `json:"title"`
		Agent              string                         `json:"agent"`
		Tier               string                         `json:"tier"`
		OutcomeType        string                         `json:"outcome_type"`
		HeroWorkspace      string                         `json:"hero_workspace"`
		WorkspacesInvolved []string                       `json:"workspaces_involved"`
		Branch             string                         `json:"branch"`
		Mission            string                         `json:"mission"`
		Stages             []string                       `json:"stages"`
		PlanSummary        string                         `json:"plan_summary"`
		FullPlanMarkdown   string                         `json:"full_plan_markdown"`
		Scenes             []pebblestore.ProjectTaskScene `json:"scenes"`
		Soundtrack         string                         `json:"soundtrack"`
	}

	if err := json.Unmarshal([]byte(cleaned), &output); err != nil {
		return pebblestore.TaskRouteResult{}, err
	}

	if output.Title == "" || output.Agent == "" {
		return pebblestore.TaskRouteResult{}, fmt.Errorf("invalid AI router response")
	}

	wsInvolved := output.WorkspacesInvolved
	if len(wsInvolved) == 0 && output.HeroWorkspace != "" {
		wsInvolved = []string{output.HeroWorkspace}
	}
	if len(wsInvolved) == 0 && len(planOpts.Workspaces) > 0 {
		wsInvolved = []string{planOpts.Workspaces[0].Path}
	}

	var cpParts []string
	if len(wsInvolved) > 0 {
		cpParts = append(cpParts, fmt.Sprintf("Primary: %s", filepath.Base(wsInvolved[0])))
	}
	if len(wsInvolved) > 1 {
		var sec []string
		for _, w := range wsInvolved[1:] {
			sec = append(sec, filepath.Base(w))
		}
		cpParts = append(cpParts, fmt.Sprintf("Secondary: %s", strings.Join(sec, ", ")))
	}
	if planOpts.ProjectContext != "" {
		cpParts = append(cpParts, "PROJECT.md guidelines injected")
	}
	cpParts = append(cpParts, fmt.Sprintf("Contract: %s", output.OutcomeType))

	return pebblestore.TaskRouteResult{
		Title:              output.Title,
		Agent:              output.Agent,
		OutcomeType:        output.OutcomeType,
		Branch:             output.Branch,
		Mission:            output.Mission,
		Stages:             output.Stages,
		WorkspacesInvolved: wsInvolved,
		ContextPoolSummary: strings.Join(cpParts, " • "),
		PlanSummary:        output.PlanSummary,
		FullPlanMarkdown:   output.FullPlanMarkdown,
		Tier:               output.Tier,
		AspectRatio:        opts.AspectRatio,
		VariantCount:       opts.VariantCount,
		Scenes:             output.Scenes,
		Soundtrack:         output.Soundtrack,
	}, nil
}

// BuildAgentSeedPrompt constructs the authoritative, high-context seed message injected
// directly into the session agent's transcript. This guarantees that the executing agent
// immediately understands its objective, injected context pool, workspace boundaries,
// deliverable contracts, and verification gates.
func (s *Service) BuildAgentSeedPrompt(task *pebblestore.ProjectTaskRecord, project *pebblestore.ProjectRecord) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Mission: %s\n\n", task.Title))

	if task.RouterAlert != "" {
		sb.WriteString(fmt.Sprintf("> ⚠️ **Router Agent Warning**: %s\n\n", task.RouterAlert))
	}

	sb.WriteString("## User Objective\n")
	desc := strings.TrimSpace(task.Description)
	if desc == "" {
		desc = task.Title
	}
	sb.WriteString(desc)
	sb.WriteString("\n\n")

	sb.WriteString("## Injected Context Pool\n")
	sb.WriteString(fmt.Sprintf("- **Primary Workspace**: `%s`\n", task.WorkspacePath))
	if len(task.WorkspacesInvolved) > 1 {
		sb.WriteString("- **Additional Project Workspaces**:\n")
		for _, w := range task.WorkspacesInvolved {
			if w != task.WorkspacePath {
				sb.WriteString(fmt.Sprintf("  - `%s`\n", w))
			}
		}
	}
	if task.ContextPoolSummary != "" {
		sb.WriteString(fmt.Sprintf("- **Context Pool Summary**: %s\n", task.ContextPoolSummary))
	}

	if project != nil && strings.TrimSpace(project.ProjectContext) != "" {
		sb.WriteString("\n### Project Guidelines & Architecture (PROJECT.md)\n")
		sb.WriteString(strings.TrimSpace(project.ProjectContext))
		sb.WriteString("\n")
	}

	sb.WriteString("\n## Outcome Contract\n")
	sb.WriteString(fmt.Sprintf("- **Outcome Type**: `%s`\n", task.OutcomeType))
	if task.WorktreeBranch != "" {
		sb.WriteString(fmt.Sprintf("- **Target Worktree Branch**: `%s`\n", task.WorktreeBranch))
	}
	if task.Tier != "" {
		sb.WriteString(fmt.Sprintf("- **Execution Tier**: `Tier %s`\n", task.Tier))
	}
	if task.AspectRatio != "" {
		sb.WriteString(fmt.Sprintf("- **Aspect Ratio**: `%s`\n", task.AspectRatio))
	}
	if task.VariantCount > 0 {
		sb.WriteString(fmt.Sprintf("- **Variant Count**: `%d`\n", task.VariantCount))
	}

	if len(task.Scenes) > 0 {
		sb.WriteString("\n## Multi-Scene Production Blueprint\n")
		for _, sc := range task.Scenes {
			sb.WriteString(fmt.Sprintf("### Scene %d: %s (%ds)\n", sc.SceneNumber, sc.Title, sc.DurationSec))
			sb.WriteString(fmt.Sprintf("- **Visual Prompt**: %s\n", sc.Prompt))
			if sc.VisualNotes != "" {
				sb.WriteString(fmt.Sprintf("- **Visual & Camera Direction**: %s\n", sc.VisualNotes))
			}
			sb.WriteString("\n")
		}
	}
	if task.Soundtrack != "" {
		sb.WriteString(fmt.Sprintf("### Soundtrack Specification\n%s\n\n", task.Soundtrack))
	}

	if strings.TrimSpace(task.FullPlanMarkdown) != "" {
		sb.WriteString("\n## Execution Plan & Verification Criteria\n")
		sb.WriteString(strings.TrimSpace(task.FullPlanMarkdown))
		sb.WriteString("\n")
	}

	return sb.String()
}
