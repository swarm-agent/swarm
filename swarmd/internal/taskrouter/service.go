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
	Prompt             string                            `json:"prompt"`
	RequestedWorkspace string                            `json:"requested_workspace,omitempty"`
	Intent             string                            `json:"intent,omitempty"` // "code", "image", "video", "audit"
	AspectRatio        string                            `json:"aspect_ratio,omitempty"`
	VariantCount       int                               `json:"variant_count,omitempty"`
	ScenesCount        int                               `json:"scenes_count,omitempty"`
	Soundtrack         string                            `json:"soundtrack,omitempty"`
	AutoApprove        bool                              `json:"auto_approve,omitempty"`
	Feedback           string                            `json:"feedback,omitempty"`
	LastError          string                            `json:"last_error,omitempty"`
	AttachedMedia      []pebblestore.ProjectTaskMediaRef `json:"attached_media,omitempty"`
	Project            *pebblestore.ProjectRecord        `json:"project,omitempty"`
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
		AttachedMedia:      opts.AttachedMedia,
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
	instructions := strings.TrimSpace(`You are the Swarm AI Task Router. Your role is to analyze user requests, attached media references, project guidelines (PROJECT.md), and project workspaces to produce an authoritative, high-context execution plan and routing contract.

CRITICAL INSTRUCTIONS:
- You are a routing coordinator, NOT a vision/image processing agent. Do NOT attempt image clipping, pixel viewing, or computer vision operations. You only inspect metadata (filename, title, kind, media_type, doc text snippet) and forward the media references to downstream specialist agents or generative engines.
- If attached_media contains a document (pasted text, markdown, doc, pdf) and the user asks a question, analysis, or inquiry:
  Route to agent="finder" (or agent="swarm"), tier="discovery" (or tier="direct"), outcome_type="audit_report". The finder will read and inspect the attached document.
- If attached_media contains video(s) and the user asks for iterations, fine-tuning, changes, or next scenes:
  Route to agent="video", tier="direct", outcome_type="video_story". Downstream video engine will extend, remix, or iterate the video sequence based on the source video.
- If attached_media contains image(s) and the user asks for fine-tuning or targeted edits ("change this to y", "modify...", "replace..."):
  Route to agent="image", tier="direct", outcome_type="media_bundle", variant_count=1 (or user count).
- If attached_media contains image(s) and the user asks for iterations, variations, or video:
  * For 1-4 variants: tier="direct", agent="image", outcome_type="media_bundle".
  * For 5-25 variants or explicit swarm generation: tier="swarm", agent="designer", outcome_type="media_bundle", variant_count=N.
  * For video stories based on the images: tier="direct", agent="video", outcome_type="video_story".
- If large iteration counts (e.g. 10 to 25 prompts or variants) are requested:
  Route to agent="designer", tier="swarm", outcome_type="media_bundle". Do not attempt to output 25 individual scene storyboards or prompts in one turn; downstream deployed non-blocking workers will generate the variants in parallel.

Rules:
1. Intent Classification & Agent Routing:
   - "image": Generative visual assets, illustrations, photo generation. tier="direct", agent="image", outcome_type="media_bundle". (Direct generation without an LLM chat session).
   - "video":
     * Generative multi-part video stories, teasers, or AI video clips: tier="direct", agent="video", outcome_type="video_story". Compile 2-4 scenes with durations, visual prompts, camera directions, and soundtrack. (Direct generation without an LLM chat session).
     * Standalone HTML/motion UI animations (Canvas, SVG, CSS animations without project source/running app): tier="direct", agent="designer", outcome_type="media_bundle".
     * Recorded video / App demo / Recording live app or requiring project source/bash execution: tier="direct", agent="swarm", outcome_type="video_story". (Designers have no bash/source execution and cannot run or record local running applications; Swarm executes with full bash tools).
   - "code":
     * Simple, bounded bug fix, typo, or single component/file: tier="direct", agent="coder", outcome_type="code_pr" (or "bug_patch" if bug).
     * Complex, multi-stage, high-risk, broad, or architectural features/refactors: tier="complex", agent="plan", outcome_type="plan_spec", stages=["Plan & Architecture Formulation", "Plan Execution"]. (Requires Swarm in Plan mode to propose a structured plan before execution).
   - "audit": Investigation, architecture questions, diagnostics, codebase exploration. tier="discovery", agent="finder", outcome_type="audit_report".
2. Return ONLY one JSON object matching this schema:
{
  "title": "string",
  "agent": "coder|plan|finder|designer|swarm|image|video",
  "tier": "direct|discovery|complex|swarm",
  "outcome_type": "code_pr|bug_patch|audit_report|media_bundle|video_story|plan_spec",
  "hero_workspace": "string",
  "workspaces_involved": ["string"],
  "branch": "string",
  "mission": "string",
  "stages": ["string"],
  "plan_summary": "string",
  "full_plan_markdown": "string",
  "variant_count": 1,
  "scenes": [{"scene_number": 1, "title": "string", "duration_sec": 4, "prompt": "string", "visual_notes": "string"}],
  "soundtrack": "string"
}`)

	var attachedSummary []map[string]any
	for _, m := range opts.AttachedMedia {
		item := map[string]any{
			"id":         m.ID,
			"title":      m.Title,
			"filename":   m.Filename,
			"kind":       m.Kind,
			"media_type": m.MediaType,
		}
		if m.Data != "" {
			snip := m.Data
			if len(snip) > 2000 {
				snip = snip[:2000] + "..."
			}
			item["data_snippet"] = snip
		}
		attachedSummary = append(attachedSummary, item)
	}

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
		"attached_media":      attachedSummary,
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

	variantCount := opts.VariantCount
	if variantCount <= 0 {
		variantCount = 1
	}
	aspectRatio := opts.AspectRatio
	if aspectRatio == "" {
		if output.Agent == "image" {
			aspectRatio = "1:1"
		} else {
			aspectRatio = "16:9"
		}
	}
	var deliverables []pebblestore.ProjectTaskDeliverable
	switch output.Agent {
	case "image":
		if variantCount <= 0 {
			variantCount = 1
		}
		if aspectRatio == "" {
			aspectRatio = "1:1"
		}
		for i := 1; i <= variantCount; i++ {
			deliverables = append(deliverables, pebblestore.ProjectTaskDeliverable{
				ID:          fmt.Sprintf("deliv_img_slot_%d", i),
				Title:       fmt.Sprintf("%s (Variant %d, %s)", output.Title, i, aspectRatio),
				Kind:        "image",
				Status:      "pending",
				Description: fmt.Sprintf("Autonomous image deliverable for %s in aspect ratio %s", output.Title, aspectRatio),
			})
		}
	case "video":
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_vid", Title: fmt.Sprintf("Video Story (%s)", aspectRatio), Kind: "video", Status: "pending"},
		}
	case "designer":
		if variantCount > 1 || output.Tier == "swarm" || output.OutcomeType == "media_bundle" {
			for i := 1; i <= variantCount; i++ {
				deliverables = append(deliverables, pebblestore.ProjectTaskDeliverable{
					ID:          fmt.Sprintf("deliv_swarm_slot_%d", i),
					Title:       fmt.Sprintf("%s (Variant %d, %s)", output.Title, i, aspectRatio),
					Kind:        "image",
					Status:      "pending",
					Description: fmt.Sprintf("Autonomous deliverable for %s in aspect ratio %s", output.Title, aspectRatio),
				})
			}
		} else {
			deliverables = []pebblestore.ProjectTaskDeliverable{
				{ID: "deliv_design", Title: "Interactive HTML / Motion UI Artifact", Kind: "artifact", Status: "pending"},
			}
		}
	case "finder":
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_audit", Title: "Comprehensive Audit Report", Kind: "report", Status: "pending"},
		}
	case "plan":
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_plan", Title: "Structured Execution Plan", Kind: "report", Status: "pending"},
		}
	default:
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_task", Title: "Completed Task & Verification", Kind: "pr", Status: "pending"},
		}
	}

	return pebblestore.TaskRouteResult{
		Title:              output.Title,
		Agent:              output.Agent,
		OutcomeType:        output.OutcomeType,
		Branch:             output.Branch,
		Mission:            output.Mission,
		Stages:             output.Stages,
		Deliverables:       deliverables,
		WorkspacesInvolved: wsInvolved,
		ContextPoolSummary: strings.Join(cpParts, " • "),
		PlanSummary:        output.PlanSummary,
		FullPlanMarkdown:   output.FullPlanMarkdown,
		Tier:               output.Tier,
		AspectRatio:        aspectRatio,
		VariantCount:       variantCount,
		Scenes:             output.Scenes,
		Soundtrack:         output.Soundtrack,
		AttachedMedia:      opts.AttachedMedia,
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

	if len(task.AttachedMedia) > 0 {
		sb.WriteString("\n## Attached / Tagged Media Context\n")
		for _, m := range task.AttachedMedia {
			sb.WriteString(fmt.Sprintf("- **%s** (kind: `%s`, type: `%s`)\n", m.Title, m.Kind, m.MediaType))
			if m.URL != "" {
				sb.WriteString(fmt.Sprintf("  - URL: `%s`\n", m.URL))
			}
			if m.Data != "" {
				sb.WriteString(fmt.Sprintf("  - Attached Content:\n```\n%s\n```\n", m.Data))
			}
		}
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
