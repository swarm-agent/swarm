package pebblestore

import (
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
	Scenes             []ProjectTaskScene       `json:"scenes,omitempty"`
	Soundtrack         string                   `json:"soundtrack,omitempty"`
	RouterAlert        string                   `json:"router_alert,omitempty"`
}

// TaskPlanOptions encapsulates all inputs for task routing and compilation.
type TaskPlanOptions struct {
	Prompt             string
	RequestedWorkspace string
	ProjectContext     string
	Workspaces         []ProjectWorkspaceRef
	Feedback           string
	LastError          string
	Intent             string // "code", "image", "video", "audit"
	AspectRatio        string // "16:9", "1:1", "9:16", "4:3"
	VariantCount       int    // 1, 2, 4
	ScenesCount        int    // 2, 3, 4
	Soundtrack         string // soundtrack prompt or mood
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
func RouteAndPlanProjectTask(prompt string, wsPath string, projectContext string, workspaces []ProjectWorkspaceRef, feedback string, lastError string) TaskRouteResult {
	return RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
		Prompt:             prompt,
		RequestedWorkspace: wsPath,
		ProjectContext:     projectContext,
		Workspaces:         workspaces,
		Feedback:           feedback,
		LastError:          lastError,
	})
}

// RouteAndPlanProjectTaskWithOptions synthesizes the fallback execution plan when the AI Task Router
// is unavailable or fails. It defaults directly to the canonical Swarm system agent without fragile
// keyword scoring heuristics, and sets RouterAlert so the system and user interface clearly alert
// the user to the router agent failure.
func RouteAndPlanProjectTaskWithOptions(opts TaskPlanOptions) TaskRouteResult {
	prompt := strings.TrimSpace(opts.Prompt)
	feedback := strings.TrimSpace(opts.Feedback)
	lastError := strings.TrimSpace(opts.LastError)

	// Primary workspace resolution: use requested workspace if supplied; otherwise first project workspace or "."
	heroWorkspace := strings.TrimSpace(opts.RequestedWorkspace)
	if heroWorkspace == "" {
		if len(opts.Workspaces) > 0 {
			heroWorkspace = opts.Workspaces[0].Path
		} else {
			heroWorkspace = "."
		}
	}
	detected := []string{heroWorkspace}

	// Intent and agent resolution
	intent := strings.ToLower(strings.TrimSpace(opts.Intent))
	promptLower := strings.ToLower(prompt)

	agent := "swarm"
	tier := "direct"
	outcomeType := "general"

	isRecordedApp := strings.Contains(promptLower, "record") || strings.Contains(promptLower, "screen record") ||
		strings.Contains(promptLower, "demo app") || strings.Contains(promptLower, "app demo") ||
		strings.Contains(promptLower, "live app")
	isHTMLAnimation := strings.Contains(promptLower, "html") || strings.Contains(promptLower, "motion ui") ||
		strings.Contains(promptLower, "canvas") || strings.Contains(promptLower, "svg animation")
	isComplexCode := strings.Contains(promptLower, "complex") || strings.Contains(promptLower, "overhaul") ||
		strings.Contains(promptLower, "architect") || strings.Contains(promptLower, "multi-phase") ||
		strings.Contains(promptLower, "pipeline") || strings.Contains(promptLower, "platform") ||
		strings.Contains(promptLower, "redesign") || strings.Contains(promptLower, "system architecture")

	switch {
	case intent == "image" || (intent == "" && (strings.Contains(promptLower, "image") || strings.Contains(promptLower, "photo") || strings.Contains(promptLower, "logo") || strings.Contains(promptLower, "graphic") || strings.Contains(promptLower, "illustration"))):
		agent = "image"
		tier = "direct"
		outcomeType = "media_bundle"
	case intent == "video" || (intent == "" && (strings.Contains(promptLower, "video") || strings.Contains(promptLower, "trailer") || strings.Contains(promptLower, "teaser"))):
		if isRecordedApp {
			agent = "swarm"
			tier = "direct"
			outcomeType = "video_story"
		} else if isHTMLAnimation {
			agent = "designer"
			tier = "direct"
			outcomeType = "media_bundle"
		} else {
			agent = "video"
			tier = "direct"
			outcomeType = "video_story"
		}
	case intent == "audit" || (intent == "" && (strings.Contains(promptLower, "audit") || strings.Contains(promptLower, "investigate") || strings.Contains(promptLower, "inspect") || strings.Contains(promptLower, "diagnose"))):
		agent = "finder"
		tier = "discovery"
		outcomeType = "audit_report"
	case intent == "code" || (intent == "" && (strings.Contains(promptLower, "fix") || strings.Contains(promptLower, "bug") || strings.Contains(promptLower, "implement") || strings.Contains(promptLower, "add") || strings.Contains(promptLower, "update") || strings.Contains(promptLower, "create") || strings.Contains(promptLower, "patch"))):
		if isComplexCode {
			agent = "plan"
			tier = "complex"
			outcomeType = "plan_spec"
		} else {
			agent = "coder"
			tier = "direct"
			outcomeType = "code_pr"
		}
	default:
		agent = "swarm"
		tier = "direct"
		outcomeType = "general"
	}

	aspectRatio := opts.AspectRatio
	variantCount := opts.VariantCount
	if variantCount <= 0 {
		variantCount = 1
	}
	var scenes []ProjectTaskScene
	soundtrack := opts.Soundtrack

	var stages []string
	var deliverables []ProjectTaskDeliverable

	switch agent {
	case "image":
		if aspectRatio == "" {
			aspectRatio = "1:1"
		}
		stages = []string{"Visual Concept Formulation", "Media Generation Pipeline"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_img", Title: fmt.Sprintf("%d Image(s) (%s)", variantCount, aspectRatio), Kind: "image", Status: "pending"},
		}
	case "video":
		if aspectRatio == "" {
			aspectRatio = "16:9"
		}
		sceneCount := opts.ScenesCount
		if sceneCount <= 0 {
			sceneCount = 2
		}
		stages = []string{"Scene & Storyboard Compilation", "Synchronized Video & Audio Synthesis"}
		for s := 1; s <= sceneCount; s++ {
			scenes = append(scenes, ProjectTaskScene{
				SceneNumber: s,
				Title:       fmt.Sprintf("Scene %d", s),
				DurationSec: 4,
				Prompt:      fmt.Sprintf("%s - Scene %d", prompt, s),
				VisualNotes: "Cinematic lighting, smooth camera movement",
			})
		}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_vid", Title: fmt.Sprintf("Video Story (%s)", aspectRatio), Kind: "video", Status: "pending"},
		}
	case "designer":
		stages = []string{"HTML & Animation Design", "Artifact Compilation"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_design", Title: "Interactive HTML / Motion UI Artifact", Kind: "artifact", Status: "pending"},
		}
	case "finder":
		stages = []string{"Codebase Investigation", "Audit Report Synthesis"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_audit", Title: "Comprehensive Audit Report", Kind: "report", Status: "pending"},
		}
	case "coder":
		stages = []string{"Code Implementation", "Verification & Pull Request"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_code", Title: "Code Changes & Verified Tests", Kind: "pr", Status: "pending"},
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

	var slugParts []string
	words := strings.Fields(strings.ToLower(prompt))
	for _, w := range words {
		var filtered strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				filtered.WriteRune(r)
			}
		}
		f := filtered.String()
		if len(f) > 2 && f != "the" && f != "and" && f != "for" && f != "with" && f != "make" && f != "please" {
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
	branch := fmt.Sprintf("agent/%s", slug)
	mission := fmt.Sprintf("Autonomous %s mission: %s. Target: %s.", agent, prompt, branch)

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

	routerAlert := fmt.Sprintf("Router agent failed or unavailable. Defaulted to %s route (@%s).", outcomeType, agent)

	planSummary := fmt.Sprintf("1. Execute %s route (@%s) in [%s]\n2. Deliver outcome: %s\n3. Verify results and review deliverables", outcomeType, agent, heroLabel, outcomeType)
	if feedback != "" {
		planSummary += "\n[Refined]: Plan adjusted to incorporate user instructions."
	}
	if lastError != "" {
		planSummary += fmt.Sprintf("\n[Error Recovery]: Re-planning to resolve: %s", truncateString(lastError, 60))
	}

	var fullPlan strings.Builder
	fullPlan.WriteString(fmt.Sprintf("### Task Mission: %s\n\n", title))
	fullPlan.WriteString(fmt.Sprintf("> ⚠️ **Router Agent Alert**: %s\n\n", routerAlert))
	fullPlan.WriteString(fmt.Sprintf("- **Assigned Agent**: `@%s` (Swarm Default)\n", agent))
	fullPlan.WriteString(fmt.Sprintf("- **Tier**: `Direct`\n"))
	fullPlan.WriteString(fmt.Sprintf("- **Expected Outcome**: `%s`\n", outcomeType))
	if aspectRatio != "" {
		fullPlan.WriteString(fmt.Sprintf("- **Aspect Ratio**: `%s`\n", aspectRatio))
	}
	if variantCount > 0 && intent == "image" {
		fullPlan.WriteString(fmt.Sprintf("- **Variant Count**: `%d`\n", variantCount))
	}
	fullPlan.WriteString(fmt.Sprintf("- **Context Pool**: %s\n", contextPoolSummary))
	fullPlan.WriteString(fmt.Sprintf("- **Workspace**: %s\n", heroLabel))
	fullPlan.WriteString(fmt.Sprintf("- **Target Worktree Branch**: `%s`\n\n", branch))

	fullPlan.WriteString("#### Execution Pipeline Stages\n")
	for i, st := range stages {
		fullPlan.WriteString(fmt.Sprintf("%d. **Stage %d**: %s\n", i+1, i+1, st))
	}
	fullPlan.WriteString("\n#### Verification & Acceptance Criteria\n")
	if len(deliverables) > 0 {
		fullPlan.WriteString(fmt.Sprintf("- [ ] Deliverable `%s` ready and verified\n", deliverables[0].Title))
	}
	fullPlan.WriteString("- [ ] Worktree branch clean and ready for integration into dev\n")
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
	}
}
