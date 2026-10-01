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
	Title              string                            `json:"title,omitempty"`
	Prompt             string                            `json:"prompt"`
	RequestedWorkspace string                            `json:"requested_workspace,omitempty"`
	Intent             string                            `json:"intent,omitempty"`       // "code", "image", "video", "audit", "sound"
	FeatureSize        string                            `json:"feature_size,omitempty"` // "small", "big"
	Agent              string                            `json:"agent,omitempty"`        // explicit agent
	OutcomeType        string                            `json:"outcome_type,omitempty"`
	Tier               string                            `json:"tier,omitempty"`
	VideoType          string                            `json:"video_type,omitempty"` // "single", "multipart"
	EnhancePrompt      bool                              `json:"enhance_prompt,omitempty"`
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

// RouteTask evaluates a user task request. The execution contract (agent, tier, outcome type)
// is determined strictly by explicit structured intent, feature size, and agent overrides.
// When an AI invoker is present, it elaborates high-context task details (title, mission, stages,
// plan summary, full plan markdown, scenes, soundtrack). The AI Router CANNOT alter or overwrite
// the execution contract. If the AI Router fails, it fails explicitly without silent mutation.
func (s *Service) RouteTask(ctx context.Context, opts TaskRouteOptions) (pebblestore.TaskRouteResult, error) {
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
		FeatureSize:        opts.FeatureSize,
		Agent:              opts.Agent,
		OutcomeType:        opts.OutcomeType,
		Tier:               opts.Tier,
		VideoType:          opts.VideoType,
		EnhancePrompt:      opts.EnhancePrompt,
		AspectRatio:        opts.AspectRatio,
		VariantCount:       opts.VariantCount,
		ScenesCount:        opts.ScenesCount,
		Soundtrack:         opts.Soundtrack,
		AttachedMedia:      opts.AttachedMedia,
	}

	// Base authoritative contract derived strictly from explicit structured parameters.
	baseContract, err := pebblestore.RouteAndPlanProjectTaskWithOptions(planOpts)
	if err != nil {
		return pebblestore.TaskRouteResult{}, fmt.Errorf("task configuration invalid: %w", err)
	}

	if baseContract.Agent == "image" {
		return s.routeImages(ctx, opts, baseContract)
	}

	// Direct media (sound, single video without prompt enhancement) does not require AI routing
	isDirectSound := opts.Intent == "sound" || opts.Intent == "audio" || opts.Agent == "sound" || opts.Agent == "audio"
	isDirectVideo := (opts.Intent == "video" || opts.Agent == "video") && !opts.EnhancePrompt && (opts.VideoType == "single" || opts.ScenesCount <= 1)
	if isDirectSound || isDirectVideo {
		if s.invoker != nil || strings.TrimSpace(opts.Title) != "" {
			baseContract.Title, err = s.NameTask(ctx, opts.Prompt, opts.Title)
			if err != nil {
				return pebblestore.TaskRouteResult{}, fmt.Errorf("router agent failed: %w", err)
			}
		}
		return baseContract, nil
	}

	if s.invoker != nil {
		aiResult, err := s.invokeAIRouter(ctx, opts, planOpts, baseContract)
		if err != nil {
			return pebblestore.TaskRouteResult{}, fmt.Errorf("router agent failed: %w", err)
		}
		// Overlay AI-elaborated fields ON TOP of the authoritative execution contract.
		// Contract fields (Agent, OutcomeType, Tier, Branch, Deliverables) remain code-governed.
		res := baseContract
		if aiResult.Title != "" {
			res.Title = aiResult.Title
		}
		if aiResult.Mission != "" {
			res.Mission = aiResult.Mission
		}
		if len(aiResult.Stages) > 0 {
			res.Stages = aiResult.Stages
		}
		if aiResult.PlanSummary != "" {
			res.PlanSummary = aiResult.PlanSummary
		}
		if aiResult.FullPlanMarkdown != "" {
			res.FullPlanMarkdown = aiResult.FullPlanMarkdown
		}
		if len(aiResult.Scenes) > 0 && baseContract.Agent == "video" {
			res.Scenes = aiResult.Scenes
		}
		if aiResult.Soundtrack != "" && (baseContract.Agent == "video" || baseContract.Agent == "sound") {
			res.Soundtrack = aiResult.Soundtrack
		}
		if strings.TrimSpace(opts.Title) != "" {
			res.Title = strings.TrimSpace(opts.Title)
		}
		return res, nil
	}

	// Deterministic compilation without AI invoker
	if strings.TrimSpace(opts.Title) != "" {
		baseContract.Title = strings.TrimSpace(opts.Title)
	}
	return baseContract, nil
}

// RefineTask recalculates plan stages and workspace boundaries based on user feedback or execution errors.
func (s *Service) RefineTask(ctx context.Context, opts TaskRouteOptions) (pebblestore.TaskRouteResult, error) {
	return s.RouteTask(ctx, opts)
}

func (s *Service) invokeAIRouter(ctx context.Context, opts TaskRouteOptions, planOpts pebblestore.TaskPlanOptions, baseContract pebblestore.TaskRouteResult) (pebblestore.TaskRouteResult, error) {
	instructions := strings.TrimSpace(`You are the Swarm AI Task Router. Your role is to analyze user requests, attached media references, project guidelines (PROJECT.md), and project workspaces to produce an authoritative, high-context execution plan and routing contract.

CRITICAL INSTRUCTIONS:
- "title": Generate a brief action/subject task-card label, normally 3–8 words and at most 80 Unicode characters. Summarize the actual requested change or deliverable, not the opening conversational text. For example, "ok can you look into why the sidebar keeps jumping ..." becomes "Fix sidebar layout jumps". Never echo the entire request or mechanically clip its prefix. No conversational filler, explanations, Markdown, or line breaks. Keep full user instructions in the mission, not in the title.
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
     * Single Video Clip (single 8s continuous shot, 1 prompt): tier="direct", agent="video", outcome_type="video_clip", variant_count=1. Refine the user's prompt slightly for cinematic lighting, atmosphere, and camera motion for a SINGLE continuous 8-second video shot. Do NOT compile multiple scenes or a multi-scene blueprint! Set scenes=[] and soundtrack="". Title format: "Single clip of [Subject]" or "Single Video: [Subject]". Branch must be "".
     * Generative multi-part video stories, teasers, or AI video clips: tier="direct", agent="video", outcome_type="video_story". Compile 2-4 scenes with durations, visual prompts, camera directions, and soundtrack. (Direct generation without an LLM chat session). Branch must be "".
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
  "outcome_type": "code_pr|bug_patch|audit_report|media_bundle|video_story|video_clip|plan_spec",
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
}

IMPORTANT TASK PROPERTIES:
- "branch": The proposed isolated git worktree branch for this task, strictly formatted as "agent/<short-kebab-slug>" (e.g. "agent/fix-navbar", "agent/update-agents-md"). NEVER output "main", "dev", or "master" as the branch!
- For code tasks (agent="coder" or outcome_type="code_pr"|"bug_patch") and discovery tasks (agent="finder"):
  Do NOT include aspect_ratio, variant_count, or scenes in your output. Those fields are EXCLUSIVELY for visual generative image and video tasks. A code task produces a code pull request and test suite, NOT an image.`)

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
		"video_type":          opts.VideoType,
		"enhance_prompt":      opts.EnhancePrompt,
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

	if output.Agent == "" {
		return pebblestore.TaskRouteResult{}, fmt.Errorf("invalid AI router response")
	}
	if strings.TrimSpace(opts.Title) != "" {
		output.Title = strings.TrimSpace(opts.Title)
	} else {
		output.Title, err = validateTaskTitle(output.Title, opts.Prompt)
		if err != nil {
			return pebblestore.TaskRouteResult{}, err
		}
	}

	isMedia := output.Agent == "image" || output.Agent == "video" || output.Agent == "sound" || output.Agent == "audio" || output.OutcomeType == "media_bundle" || output.OutcomeType == "video_story" || output.OutcomeType == "video_clip"
	cleanBranch := strings.TrimSpace(output.Branch)
	if isMedia {
		cleanBranch = ""
	} else if cleanBranch == "" || cleanBranch == "main" || cleanBranch == "dev" || cleanBranch == "master" || (!strings.HasPrefix(cleanBranch, "agent/") && !strings.HasPrefix(cleanBranch, "worktree/")) {
		cleanBranch, _ = pebblestore.MakeWorktreeBranch(output.Title, opts.Prompt)
	}
	output.Branch = cleanBranch

	isSingleVideo := output.OutcomeType == "video_clip" || opts.VideoType == "single" || (opts.Intent == "video" && opts.ScenesCount == 1)
	if isSingleVideo {
		output.Agent = "video"
		output.OutcomeType = "video_clip"
		output.Scenes = nil
		output.Soundtrack = ""
		output.Branch = ""
	}

	// AI-proposed workspaces are descriptive only, not target authority.
	wsInvolved := baseContract.WorkspacesInvolved

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

	isVisualMedia := output.Agent == "image" || output.Agent == "video" ||
		(output.Agent == "designer" && (output.Tier == "swarm" || output.OutcomeType == "media_bundle" || opts.VariantCount > 1))

	var aspectRatio string
	var variantCount int
	if isVisualMedia {
		variantCount = opts.VariantCount
		if variantCount <= 0 {
			variantCount = 1
		}
		aspectRatio = strings.TrimSpace(opts.AspectRatio)
		if aspectRatio == "" {
			if output.Agent == "image" {
				aspectRatio = "1:1"
			} else {
				aspectRatio = "16:9"
			}
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
		if isSingleVideo {
			deliverables = []pebblestore.ProjectTaskDeliverable{
				{
					ID:          "deliv_vid",
					Title:       fmt.Sprintf("%s (Single Video, %s)", output.Title, aspectRatio),
					Kind:        "video",
					Status:      "pending",
					Duration:    "8s",
					Description: fmt.Sprintf("Single video clip (%s, 8s): %s", aspectRatio, output.Title),
				},
			}
		} else {
			deliverables = []pebblestore.ProjectTaskDeliverable{
				{ID: "deliv_vid", Title: fmt.Sprintf("Video Story (%s)", aspectRatio), Kind: "video", Status: "pending"},
			}
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
	case "sound", "audio":
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{
				ID:          "deliv_snd",
				Title:       fmt.Sprintf("%s (Audio Clip)", output.Title),
				Kind:        "audio",
				Status:      "pending",
				Thumbnail:   "sound",
				Duration:    "30s",
				Description: fmt.Sprintf("Autonomous audio soundtrack: %s", output.Title),
			},
		}
	case "plan":
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_plan", Title: "Structured Execution Plan", Kind: "report", Status: "pending"},
		}
	case "coder":
		deliverables = []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_code", Title: "Code PR & Verified Tests", Kind: "pr", Status: "pending", Description: "Pull request with tested code modifications"},
		}
	default:
		if output.OutcomeType == "code_pr" || output.OutcomeType == "bug_patch" {
			deliverables = []pebblestore.ProjectTaskDeliverable{
				{ID: "deliv_code", Title: "Code PR & Verified Tests", Kind: "pr", Status: "pending", Description: "Pull request with tested code modifications"},
			}
		} else {
			deliverables = []pebblestore.ProjectTaskDeliverable{
				{ID: "deliv_task", Title: "Completed Task & Verification", Kind: "pr", Status: "pending"},
			}
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
	if task.SourceWorkspace.Path != "" {
		sb.WriteString(fmt.Sprintf("- **Source Repository**: `%s` (workspace `%s`, generation %d)\n", task.SourceWorkspace.Path, task.SourceWorkspace.WorkspaceID, task.SourceWorkspace.WorkspaceGeneration))
	}
	sb.WriteString(fmt.Sprintf("- **Isolated Execution Workspace**: `%s`\n", task.WorkspacePath))
	for _, source := range task.ContextSources {
		sb.WriteString(fmt.Sprintf("- **Read-only Context Workspace**: `%s` (workspace `%s`, generation %d). Context selection grants no write or delegation authority; changes require a separately authorized repository binding.\n", source.Path, source.WorkspaceID, source.WorkspaceGeneration))
	}
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

	if len(task.CoderAssignments) > 0 {
		sb.WriteString("\n## Small-task parallel Coder assignments (execute only after task-card approval)\n")
		for i, assignment := range task.CoderAssignments {
			sb.WriteString(fmt.Sprintf("Authorized source: %s (workspace %s, generation %d). Pass this exact workspace_path on this Coder launch.\n", assignment.SourceWorkspace.Path, assignment.SourceWorkspace.WorkspaceID, assignment.SourceWorkspace.WorkspaceGeneration))
			sb.WriteString(fmt.Sprintf("%d. %s\n   Scope: %s\n   Objective: %s\n   Deliverable: %s\n   Acceptance criteria: %s\n", i+1, assignment.Title, strings.Join(assignment.OwnedScope, ", "), assignment.MetaPrompt, assignment.Deliverable, strings.Join(assignment.AcceptanceCriteria, "; ")))
		}
		sb.WriteString("Use one regular task launch with all Coder assignments in parallel, each in an isolated worktree of its own bound source repository. Preserve each assignment's exact workspace_path; never substitute the parent or first repository. These are independent small changes, not a plan or task program. Do not edit their files yourself or create separate project cards. Supervise each child, inspect committed handoffs, run focused validation, integrate only when requested and only through the authorized per-repository destination, and report any failure honestly. Resolve Coder models through the configured system-agent assignment; never choose a hardcoded model.\n")
	}
	if strings.TrimSpace(task.FullPlanMarkdown) != "" {
		sb.WriteString("\n## Execution Plan & Verification Criteria\n")
		sb.WriteString(strings.TrimSpace(task.FullPlanMarkdown))
		sb.WriteString("\n")
	}

	return sb.String()
}
