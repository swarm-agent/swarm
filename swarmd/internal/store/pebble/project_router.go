package pebblestore

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// TaskRouteResult represents the synthesized routing and planning output.
type TaskRouteResult struct {
	Title              string                   `json:"title"`
	Agent              string                   `json:"agent"`
	OutcomeType        string                   `json:"outcome_type"`
	Branch             string                   `json:"branch"`
	Mission            string                   `json:"mission"`
	Stages             []string                 `json:"stages"`
	Deliverables       []ProjectTaskDeliverable `json:"deliverables"`
	WorkspacesInvolved []string                 `json:"workspaces_involved"`
	ContextPoolSummary string                   `json:"context_pool_summary"`
	PlanSummary        string                   `json:"plan_summary"`
	FullPlanMarkdown   string                   `json:"full_plan_markdown"`
	Tier               string                   `json:"tier"`
	AspectRatio        string                   `json:"aspect_ratio,omitempty"`
	VariantCount       int                      `json:"variant_count,omitempty"`
	EnhancePrompt      bool                     `json:"enhance_prompt,omitempty"`
	ImagePrompts       []string                 `json:"image_prompts,omitempty"`
	Scenes             []ProjectTaskScene       `json:"scenes,omitempty"`
	Soundtrack         string                   `json:"soundtrack,omitempty"`
	RouterAlert        string                   `json:"router_alert,omitempty"`
	AttachedMedia      []ProjectTaskMediaRef    `json:"attached_media,omitempty"`
	TaskProgram        *TaskProgramDefinition   `json:"task_program,omitempty"`
}

// TaskPlanOptions encapsulates all inputs for task routing and compilation.
type TaskPlanOptions struct {
	Prompt             string
	RequestedWorkspace string
	ProjectContext     string
	Workspaces         []ProjectWorkspaceRef
	Feedback           string
	LastError          string
	Intent             string // "code", "image", "video", "audit", "sound"
	FeatureSize        string // "small", "big"
	Agent              string // explicit agent override: "coder", "finder", "plan", "swarm", "image", "video", "sound", "designer"
	OutcomeType        string // explicit outcome type: "code_pr", "audit_report", "plan_spec", "media_bundle", etc.
	Tier               string // explicit tier: "direct", "discovery", "complex", "swarm"
	VideoType          string // "single", "multipart"
	EnhancePrompt      bool
	AspectRatio        string // "16:9", "1:1", "9:16", "4:3"
	VariantCount       int    // 1, 2, 4, 5..25
	ScenesCount        int    // 2, 3, 4
	Soundtrack         string // soundtrack prompt or mood
	AttachedMedia      []ProjectTaskMediaRef
}

func appendIfMissing(slice []string, val string) []string {
	for _, item := range slice {
		if item == val {
			return slice
		}
	}
	return append(slice, val)
}

func truncateString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func tierNumber(tier string) string {
	switch tier {
	case "discovery":
		return "2"
	case "complex":
		return "3"
	default:
		return "1"
	}
}

// RouteAndPlanProjectTask is the backward-compatible entrypoint.
func RouteAndPlanProjectTask(prompt string, wsPath string, projectContext string, workspaces []ProjectWorkspaceRef, feedback string, lastError string) (TaskRouteResult, error) {
	return RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
		Prompt:             prompt,
		RequestedWorkspace: wsPath,
		ProjectContext:     projectContext,
		Workspaces:         workspaces,
		Feedback:           feedback,
		LastError:          lastError,
		Agent:              "coder",
	})
}

// RouteAndPlanProjectTaskWithOptions derives the canonical execution contract and task plan
// strictly from explicit structured configuration (agent, intent, feature_size, etc.).
// It fails closed on missing, unknown, or conflicting configuration and never infers or routes
// via keyword heuristics or accidental metadata.
func RouteAndPlanProjectTaskWithOptions(opts TaskPlanOptions) (TaskRouteResult, error) {
	prompt := strings.TrimSpace(opts.Prompt)
	feedback := strings.TrimSpace(opts.Feedback)
	lastError := strings.TrimSpace(opts.LastError)

	explicitAgent := strings.ToLower(strings.TrimSpace(opts.Agent))
	explicitOutcome := strings.ToLower(strings.TrimSpace(opts.OutcomeType))
	explicitTier := strings.ToLower(strings.TrimSpace(opts.Tier))
	intent := strings.ToLower(strings.TrimSpace(opts.Intent))
	featureSize := strings.ToLower(strings.TrimSpace(opts.FeatureSize))

	// 1. Missing structured configuration check: fail closed
	if explicitAgent == "" && intent == "" {
		return TaskRouteResult{}, errors.New("missing structured configuration: task agent or intent is required")
	}

	// 2. Feature size validation: must be small or big if provided
	if featureSize != "" && featureSize != "small" && featureSize != "big" {
		return TaskRouteResult{}, fmt.Errorf("unknown feature_size %q (expected 'small' or 'big')", opts.FeatureSize)
	}

	var agent string
	var tier string
	var outcomeType string

	// 3. Resolve and validate agent / intent with strict conflict detection
	if explicitAgent != "" {
		switch explicitAgent {
		case "coder":
			if featureSize == "big" {
				return TaskRouteResult{}, errors.New("conflicting task configuration: feature_size 'big' cannot use coder agent (use plan agent)")
			}
			if intent != "" && intent != "code" {
				return TaskRouteResult{}, fmt.Errorf("conflicting task configuration: intent %q cannot use coder agent", opts.Intent)
			}
			agent = "coder"
			tier = "direct"
			outcomeType = "code_pr"
		case "finder":
			if intent != "" && intent != "audit" {
				return TaskRouteResult{}, fmt.Errorf("conflicting task configuration: intent %q cannot use finder agent", opts.Intent)
			}
			agent = "finder"
			tier = "discovery"
			outcomeType = "audit_report"
		case "plan":
			if featureSize == "small" {
				return TaskRouteResult{}, errors.New("conflicting task configuration: feature_size 'small' cannot use plan agent (use coder agent)")
			}
			if intent != "" && intent != "code" && intent != "plan" {
				return TaskRouteResult{}, fmt.Errorf("conflicting task configuration: intent %q cannot use plan agent", opts.Intent)
			}
			agent = "plan"
			tier = "complex"
			outcomeType = "plan_spec"
		case "image":
			if intent != "" && intent != "image" {
				return TaskRouteResult{}, fmt.Errorf("conflicting task configuration: intent %q cannot use image agent", opts.Intent)
			}
			agent = "image"
			tier = "direct"
			outcomeType = "media_bundle"
		case "video":
			if intent != "" && intent != "video" {
				return TaskRouteResult{}, fmt.Errorf("conflicting task configuration: intent %q cannot use video agent", opts.Intent)
			}
			agent = "video"
			tier = "direct"
			if opts.VideoType == "single" || opts.ScenesCount == 1 {
				outcomeType = "video_clip"
			} else {
				outcomeType = "video_story"
			}
		case "sound", "audio":
			if intent != "" && intent != "sound" && intent != "audio" {
				return TaskRouteResult{}, fmt.Errorf("conflicting task configuration: intent %q cannot use sound agent", opts.Intent)
			}
			agent = "sound"
			tier = "direct"
			outcomeType = "audio_clip"
		case "designer":
			if intent != "" && intent != "design" {
				return TaskRouteResult{}, fmt.Errorf("conflicting task configuration: intent %q cannot use designer agent", opts.Intent)
			}
			agent = "designer"
			tier = "direct"
			outcomeType = "artifact"
		case "swarm":
			if intent == "image" {
				return TaskRouteResult{}, errors.New("conflicting task configuration: image intent requires image generation")
			}
			agent = "swarm"
			tier = "direct"
			outcomeType = "general"
		default:
			return TaskRouteResult{}, fmt.Errorf("unknown task agent: %q", opts.Agent)
		}
	} else {
		// No explicit agent; resolve strictly from intent
		switch intent {
		case "code":
			if featureSize == "big" {
				agent = "plan"
				tier = "complex"
				outcomeType = "plan_spec"
			} else {
				agent = "coder"
				tier = "direct"
				outcomeType = "code_pr"
			}
		case "audit":
			agent = "finder"
			tier = "discovery"
			outcomeType = "audit_report"
		case "image":
			agent = "image"
			tier = "direct"
			outcomeType = "media_bundle"
		case "video":
			agent = "video"
			tier = "direct"
			if opts.VideoType == "single" || opts.ScenesCount == 1 {
				outcomeType = "video_clip"
			} else {
				outcomeType = "video_story"
			}
		case "sound", "audio":
			agent = "sound"
			tier = "direct"
			outcomeType = "audio_clip"
		case "plan":
			agent = "plan"
			tier = "complex"
			outcomeType = "plan_spec"
		case "design":
			agent = "designer"
			tier = "direct"
			outcomeType = "artifact"
		default:
			return TaskRouteResult{}, fmt.Errorf("unknown task intent: %q", opts.Intent)
		}
	}

	if explicitOutcome != "" {
		switch agent {
		case "coder":
			if explicitOutcome != "code_pr" && explicitOutcome != "bug_patch" && explicitOutcome != "code" {
				return TaskRouteResult{}, fmt.Errorf("incoherent task contract: coder agent cannot have outcome %q", opts.OutcomeType)
			}
		case "finder":
			if explicitOutcome != "audit_report" && explicitOutcome != "audit" && explicitOutcome != "report" {
				return TaskRouteResult{}, fmt.Errorf("incoherent task contract: finder agent cannot have outcome %q", opts.OutcomeType)
			}
		case "plan":
			if explicitOutcome != "plan_spec" && explicitOutcome != "plan" && explicitOutcome != "general" {
				return TaskRouteResult{}, fmt.Errorf("incoherent task contract: plan agent cannot have outcome %q", opts.OutcomeType)
			}
		case "image":
			if explicitOutcome != "media_bundle" && explicitOutcome != "image" {
				return TaskRouteResult{}, fmt.Errorf("incoherent task contract: image agent cannot have outcome %q", opts.OutcomeType)
			}
		case "video":
			if explicitOutcome != "video_clip" && explicitOutcome != "video_story" && explicitOutcome != "video" {
				return TaskRouteResult{}, fmt.Errorf("incoherent task contract: video agent cannot have outcome %q", opts.OutcomeType)
			}
		case "sound", "audio":
			if explicitOutcome != "audio_clip" && explicitOutcome != "sound" && explicitOutcome != "audio" {
				return TaskRouteResult{}, fmt.Errorf("incoherent task contract: sound agent cannot have outcome %q", opts.OutcomeType)
			}
		case "designer":
			if explicitOutcome != "artifact" && explicitOutcome != "ui_design" {
				return TaskRouteResult{}, fmt.Errorf("incoherent task contract: designer agent cannot have outcome %q", opts.OutcomeType)
			}
		}
		outcomeType = explicitOutcome
	}
	if explicitTier != "" {
		tier = explicitTier
	}

	// Exempt only the validated managed-image contract, never source execution.
	heroWorkspace := ""
	var detected []string
	if agent == "image" {
		if opts.VariantCount < 0 || opts.VariantCount > 25 {
			return TaskRouteResult{}, errors.New("image variant count must be between 1 and 25 (or zero for default)")
		}
		if explicitTier != "" && explicitTier != "direct" {
			return TaskRouteResult{}, errors.New("image generation requires direct tier")
		}
	} else {
		heroWorkspace = strings.TrimSpace(opts.RequestedWorkspace)
		if heroWorkspace == "" && len(opts.Workspaces) == 1 {
			heroWorkspace = strings.TrimSpace(opts.Workspaces[0].Path)
		}
		if len(opts.Workspaces) > 1 && heroWorkspace == "" {
			return TaskRouteResult{}, errors.New("execution target is ambiguous; select a project workspace explicitly")
		}
		if heroWorkspace == "" {
			heroWorkspace = "."
		}
		if heroWorkspace != "." && !filepath.IsAbs(heroWorkspace) {
			return TaskRouteResult{}, errors.New("execution target requires an absolute workspace root")
		}
		detected = []string{heroWorkspace}
	}

	isVisualMedia := agent == "image" || agent == "video"

	var aspectRatio string
	var variantCount int
	if isVisualMedia {
		variantCount = opts.VariantCount
		if variantCount <= 0 {
			variantCount = 1
		}
		aspectRatio = strings.TrimSpace(opts.AspectRatio)
		if aspectRatio == "" {
			if agent == "image" {
				aspectRatio = "1:1"
			} else {
				aspectRatio = "16:9"
			}
		}
	}
	var scenes []ProjectTaskScene
	soundtrack := opts.Soundtrack

	clean := prompt
	if clean == "" && feedback != "" {
		clean = feedback
	}
	if len(clean) > 80 {
		clean = clean[:80]
		if idx := strings.LastIndex(clean, " "); idx > 40 {
			clean = clean[:idx]
		}
	}
	clean = strings.TrimSpace(clean)
	var title string
	if len(clean) > 0 {
		title = strings.ToUpper(clean[:1]) + clean[1:]
	} else {
		title = "Autonomous Task"
	}

	var stages []string
	var deliverables []ProjectTaskDeliverable

	switch agent {
	case "image":
		if aspectRatio == "" {
			aspectRatio = "1:1"
		}
		if variantCount <= 0 {
			variantCount = 1
		}
		stages = []string{"Visual Concept Formulation", "Media Generation Pipeline"}
		for i := 1; i <= variantCount; i++ {
			deliverables = append(deliverables, ProjectTaskDeliverable{
				ID:          fmt.Sprintf("deliv_img_slot_%d", i),
				Title:       fmt.Sprintf("%s (Variant %d, %s)", title, i, aspectRatio),
				Kind:        "image",
				Status:      "pending",
				Description: fmt.Sprintf("Autonomous image deliverable for %s in aspect ratio %s", title, aspectRatio),
			})
		}
	case "video":
		if aspectRatio == "" {
			aspectRatio = "16:9"
		}
		if opts.VideoType == "single" || opts.ScenesCount == 1 {
			outcomeType = "video_clip"
			stages = []string{"Video Parameter Configuration", "Model Generative Synthesis"}
			deliverables = []ProjectTaskDeliverable{
				{
					ID:          "deliv_vid",
					Title:       fmt.Sprintf("%s (Single Video, %s)", title, aspectRatio),
					Kind:        "video",
					Status:      "pending",
					Description: fmt.Sprintf("Single video clip (%s): %s", aspectRatio, prompt),
				},
			}
		} else {
			sceneCount := opts.ScenesCount
			if sceneCount <= 0 {
				sceneCount = 2
			}
			stages = []string{"Scene & Storyboard Compilation", "Synchronized Video & Audio Synthesis"}
			for s := 1; s <= sceneCount; s++ {
				scenes = append(scenes, ProjectTaskScene{
					SceneNumber: s,
					Title:       fmt.Sprintf("Scene %d", s),
					// Omission must reach capability validation unchanged. The router
					// has no model authority to select a duration.
					DurationSec: 0,
					Prompt:      fmt.Sprintf("%s - Scene %d", prompt, s),
					VisualNotes: "Cinematic lighting, smooth camera movement",
				})
			}
			deliverables = []ProjectTaskDeliverable{
				{ID: "deliv_vid", Title: fmt.Sprintf("Video Story (%s)", aspectRatio), Kind: "video", Status: "pending"},
			}
		}
	case "designer":
		vCount := opts.VariantCount
		if vCount <= 0 {
			vCount = 1
		}
		stages = []string{"Visual UI & Animation Design", "Interactive Artifact Compilation"}
		if vCount > 1 {
			for i := 1; i <= vCount; i++ {
				deliverables = append(deliverables, ProjectTaskDeliverable{
					ID:          fmt.Sprintf("deliv_design_slot_%d", i),
					Title:       fmt.Sprintf("%s (Variant %d)", title, i),
					Kind:        "artifact",
					Status:      "pending",
					Description: fmt.Sprintf("Autonomous interactive UI artifact for %s (variant %d)", title, i),
				})
			}
		} else {
			deliverables = []ProjectTaskDeliverable{
				{ID: "deliv_design", Title: fmt.Sprintf("%s (Interactive Artifact)", title), Kind: "artifact", Status: "pending", Description: fmt.Sprintf("Interactive UI artifact for %s", prompt)},
			}
		}
	case "finder":
		stages = []string{"Codebase Investigation", "Audit Report Synthesis"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_audit", Title: "Comprehensive Audit Report", Kind: "report", Status: "pending"},
		}
	case "coder":
		stages = []string{"Code Implementation", "Verification & Pull Request"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_code", Title: "Code PR & Verified Tests", Kind: "pr", Status: "pending", Description: "Pull request with tested code modifications"},
		}
	case "sound", "audio":
		stages = []string{"Audio Composition", "Synthesis Pipeline"}
		deliverables = []ProjectTaskDeliverable{
			{
				ID:          "deliv_snd",
				Title:       fmt.Sprintf("%s (Audio Clip)", title),
				Kind:        "audio",
				Status:      "pending",
				Thumbnail:   "sound",
				Duration:    "30s",
				Description: fmt.Sprintf("Autonomous audio soundtrack: %s", prompt),
			},
		}
	case "plan":
		stages = []string{"Plan & Architecture Formulation", "Plan Review & Checkpoint Execution"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_plan", Title: "Structured Execution Plan", Kind: "report", Status: "pending"},
		}
	default:
		stages = []string{"Swarm Execution", "Verification"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_task", Title: "Completed Task & Verification", Kind: "pr", Status: "pending"},
		}
	}

	isMedia := agent == "image" || agent == "video" || agent == "sound" || agent == "audio"
	var branch string
	if !isMedia {
		branch, _ = MakeWorktreeBranch(title, prompt)
	}
	var mission string
	if isMedia {
		mission = fmt.Sprintf("Direct %s generation: %s.", agent, prompt)
	} else {
		mission = fmt.Sprintf("Autonomous %s mission: %s. Target: %s.", agent, prompt, branch)
	}

	var heroLabel string
	for _, w := range opts.Workspaces {
		if w.Path == heroWorkspace {
			heroLabel = w.Label
			break
		}
	}
	if heroLabel == "" {
		heroLabel = filepath.Base(heroWorkspace)
	}

	var cpParts []string
	cpParts = append(cpParts, fmt.Sprintf("Primary: %s", heroLabel))
	if opts.ProjectContext != "" {
		cpParts = append(cpParts, "PROJECT.md guidelines injected")
	}
	cpParts = append(cpParts, "Swarm Default Agent")
	contextPoolSummary := strings.Join(cpParts, " • ")

	var routerAlert string
	if lastError != "" {
		routerAlert = lastError
	}

	planSummary := fmt.Sprintf("1. Execute %s route (@%s) in [%s]\n2. Deliver outcome: %s\n3. Verify results and review deliverables", outcomeType, agent, heroLabel, outcomeType)
	if feedback != "" {
		planSummary += "\n[Refined]: Plan adjusted to incorporate user instructions."
	}
	if lastError != "" {
		planSummary += fmt.Sprintf("\n[Error Recovery]: Re-planning to resolve: %s", truncateString(lastError, 60))
	}

	var fullPlan strings.Builder
	fullPlan.WriteString(fmt.Sprintf("### Task Mission: %s\n\n", title))
	if routerAlert != "" {
		fullPlan.WriteString(fmt.Sprintf("> ⚠️ **Router Agent Alert**: %s\n\n", routerAlert))
	}
	fullPlan.WriteString(fmt.Sprintf("- **Assigned Agent**: `@%s`\n", agent))
	fullPlan.WriteString(fmt.Sprintf("- **Tier**: `%s`\n", strings.Title(tier)))
	fullPlan.WriteString(fmt.Sprintf("- **Expected Outcome**: `%s`\n", outcomeType))
	if aspectRatio != "" {
		fullPlan.WriteString(fmt.Sprintf("- **Aspect Ratio**: `%s`\n", aspectRatio))
	}
	if variantCount > 0 && intent == "image" {
		fullPlan.WriteString(fmt.Sprintf("- **Variant Count**: `%d`\n", variantCount))
	}
	fullPlan.WriteString(fmt.Sprintf("- **Context Pool**: %s\n", contextPoolSummary))
	fullPlan.WriteString(fmt.Sprintf("- **Workspace**: %s\n", heroLabel))
	if !isMedia && branch != "" {
		fullPlan.WriteString(fmt.Sprintf("- **Target Worktree Branch**: `%s`\n\n", branch))
	} else {
		fullPlan.WriteString("\n")
	}

	fullPlan.WriteString("#### Execution Pipeline Stages\n")
	for i, st := range stages {
		fullPlan.WriteString(fmt.Sprintf("%d. **Stage %d**: %s\n", i+1, i+1, st))
	}
	fullPlan.WriteString("\n#### Verification & Acceptance Criteria\n")
	if len(deliverables) > 0 {
		fullPlan.WriteString(fmt.Sprintf("- [ ] Deliverable `%s` ready and verified\n", deliverables[0].Title))
	}
	if !isMedia {
		fullPlan.WriteString("- [ ] Worktree branch clean and ready for integration into dev\n")
	}
	fullPlan.WriteString("- [ ] Regression gates pass with zero broken contracts\n")

	if feedback != "" {
		fullPlan.WriteString("\n#### User Refinement Directives\n")
		fullPlan.WriteString(fmt.Sprintf("> %s\n", feedback))
	}
	if lastError != "" {
		fullPlan.WriteString("\n#### Prior Error Analysis & Fix Strategy\n")
		fullPlan.WriteString(fmt.Sprintf("```\n%s\n```\n", lastError))
		fullPlan.WriteString("The revised plan prioritizes reproducing the root cause and verifying the repair before proceeding.\n")
	}

	return TaskRouteResult{
		Title:              title,
		Agent:              agent,
		OutcomeType:        outcomeType,
		Branch:             branch,
		Mission:            mission,
		Stages:             stages,
		Deliverables:       deliverables,
		WorkspacesInvolved: detected,
		ContextPoolSummary: contextPoolSummary,
		PlanSummary:        planSummary,
		FullPlanMarkdown:   fullPlan.String(),
		Tier:               tier,
		AspectRatio:        aspectRatio,
		VariantCount:       variantCount,
		Scenes:             scenes,
		Soundtrack:         soundtrack,
		RouterAlert:        routerAlert,
		AttachedMedia:      opts.AttachedMedia,
	}, nil
}

// MakeWorktreeBranch derives a clean, isolated worktree branch and slug name from a title or prompt.
func MakeWorktreeBranch(title, prompt string) (string, string) {
	text := strings.TrimSpace(title)
	if text == "" {
		text = strings.TrimSpace(prompt)
	}
	var slugParts []string
	words := strings.Fields(strings.ToLower(text))
	for _, w := range words {
		var filtered strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				filtered.WriteRune(r)
			}
		}
		f := filtered.String()
		if len(f) > 1 && f != "the" && f != "and" && f != "for" && f != "with" && f != "make" && f != "please" && f != "into" && f != "from" && f != "this" && f != "that" {
			slugParts = append(slugParts, f)
			if len(slugParts) >= 4 {
				break
			}
		}
	}
	slug := strings.Join(slugParts, "-")
	if slug == "" {
		slug = fmt.Sprintf("task-%d", time.Now().Unix()%10000)
	}
	return fmt.Sprintf("agent/%s", slug), slug
}

// ValidateImagePrompts verifies the entire slot mapping before any provider work.
func ValidateImagePrompts(prompts []string, count int) error {
	if count < 1 || count > 25 || len(prompts) != count {
		return errors.New("image prompt count must match output count (1..25)")
	}
	for _, prompt := range prompts {
		if strings.TrimSpace(prompt) == "" {
			return errors.New("image prompts must not be empty")
		}
	}
	return nil
}
