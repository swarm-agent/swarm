package pebblestore

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// TaskRouteResult represents the synthesized routing and planning output.
type TaskRouteResult struct {
	Title              string
	Agent              string
	OutcomeType        string
	Branch             string
	Mission            string
	Stages             []string
	Deliverables       []ProjectTaskDeliverable
	WorkspacesInvolved []string
	PlanSummary        string
	FullPlanMarkdown   string
	Tier               string
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

// RouteAndPlanProjectTask uses project workspaces, prompt, feedback, and error context to route and plan a task.
func RouteAndPlanProjectTask(prompt string, wsPath string, projectContext string, workspaces []ProjectWorkspaceRef, feedback string, lastError string) TaskRouteResult {
	prompt = strings.TrimSpace(prompt)
	feedback = strings.TrimSpace(feedback)
	lastError = strings.TrimSpace(lastError)
	combined := strings.ToLower(prompt + " " + feedback + " " + lastError)

	var detected []string
	for _, w := range workspaces {
		wName := strings.ToLower(filepath.Base(w.Path))
		wLabel := strings.ToLower(w.Label)
		if (wName != "" && strings.Contains(combined, wName)) || (wLabel != "" && strings.Contains(combined, wLabel)) {
			detected = appendIfMissing(detected, w.Path)
		}
	}

	frontendKeywords := []string{"sidebar", "nav", "navbar", "css", "theme", "tailwind", "color", "modal", "dialog", "button", "toast", "layout", "chat", "panel", "desktop", "ui", "tsx", "jsx", "react", "view", "component", "canvas"}
	backendKeywords := []string{"daemon", "api", "route", "endpoint", "pebble", "database", "store", "event", "outbox", "sync", "realtime", "auth", "token", "runner", "grpc", "http", "server", "sessions", "mutation"}
	opsKeywords := []string{"gcp", "cloud run", "lease", "broker", "secret", "federation", "critical"}
	testKeywords := []string{"testbench", "nspawn", "benchmark", "matrix"}

	hasFrontend := false
	for _, k := range frontendKeywords {
		if strings.Contains(combined, k) {
			hasFrontend = true
			break
		}
	}
	hasBackend := false
	for _, k := range backendKeywords {
		if strings.Contains(combined, k) {
			hasBackend = true
			break
		}
	}
	hasOps := false
	for _, k := range opsKeywords {
		if strings.Contains(combined, k) {
			hasOps = true
			break
		}
	}
	hasTest := false
	for _, k := range testKeywords {
		if strings.Contains(combined, k) {
			hasTest = true
			break
		}
	}

	for _, w := range workspaces {
		wPathLower := strings.ToLower(w.Path)
		isWeb := strings.Contains(wPathLower, "web") || strings.Contains(wPathLower, "ui") || w.Role == "desktop"
		isBackend := strings.Contains(wPathLower, "swarmd") || strings.Contains(wPathLower, "swarm-go") || w.Role == "primary_code"
		isOps := strings.Contains(wPathLower, "crit") || w.Role == "ops"
		isTest := strings.Contains(wPathLower, "work") || strings.Contains(wPathLower, "test") || w.Role == "testing"

		if hasFrontend && isWeb {
			detected = appendIfMissing(detected, w.Path)
		}
		if hasBackend && isBackend {
			detected = appendIfMissing(detected, w.Path)
		}
		if hasOps && isOps {
			detected = appendIfMissing(detected, w.Path)
		}
		if hasTest && isTest {
			detected = appendIfMissing(detected, w.Path)
		}
	}

	// Feedback overrides: e.g. "don't touch backend" or "only web"
	if strings.Contains(strings.ToLower(feedback), "don't touch backend") || strings.Contains(strings.ToLower(feedback), "only web") || strings.Contains(strings.ToLower(feedback), "only frontend") {
		var filtered []string
		for _, p := range detected {
			if strings.Contains(strings.ToLower(p), "web") || strings.Contains(strings.ToLower(p), "ui") {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) > 0 {
			detected = filtered
		}
	}

	if len(detected) == 0 {
		if wsPath != "" {
			detected = append(detected, wsPath)
		} else if len(workspaces) > 0 {
			detected = append(detected, workspaces[0].Path)
		} else {
			detected = append(detected, ".")
		}
	}

	var tier string
	var agent string
	var outcomeType string
	var stages []string
	var deliverables []ProjectTaskDeliverable

	isDiscovery := strings.Contains(combined, "figure out") || strings.Contains(combined, "why does") || strings.Contains(combined, "is there") || strings.Contains(combined, "audit") || strings.Contains(combined, "investigate") || strings.Contains(combined, "inspect") || strings.Contains(combined, "benchmark") || strings.Contains(combined, "security") || strings.Contains(combined, "find out")
	isMedia := strings.Contains(combined, "video") || strings.Contains(combined, "render") || strings.Contains(combined, "teaser") || strings.Contains(combined, "animation") || strings.Contains(combined, "clip") || strings.Contains(combined, "trailer")
	isBug := strings.Contains(combined, "bug") || strings.Contains(combined, "fix") || strings.Contains(combined, "broken") || strings.Contains(combined, "crash") || strings.Contains(combined, "reproduce") || strings.Contains(combined, "error") || lastError != ""

	if isDiscovery {
		tier = "discovery"
		agent = "finder"
		outcomeType = "audit_report"
		stages = []string{"Discovery & Scope", "Analyze Invariants", "Compile Findings Report", "Review & Recommendations"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_report", Title: "Technical Audit Findings Ledger", Kind: "report", Status: "pending"},
		}
	} else if isMedia {
		tier = "direct"
		agent = "designer"
		outcomeType = "media_bundle"
		stages = []string{"Script & Storyboard", "Scene Generation", "Soundtrack Ingestion", "Final Review"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_1", Title: "Cut 1: Launch Teaser (15s)", Kind: "video", Status: "pending", Duration: "0:15", Thumbnail: "cyber_lattice"},
			{ID: "deliv_2", Title: "Cut 2: Architecture Deep Dive (15s)", Kind: "video", Status: "pending", Duration: "0:15", Thumbnail: "neural_core"},
			{ID: "deliv_3", Title: "Cut 3: Feature Callout (15s)", Kind: "video", Status: "pending", Duration: "0:15", Thumbnail: "orbital_data"},
		}
	} else if len(detected) > 1 || strings.Contains(combined, "plan") || strings.Contains(combined, "refactor") || strings.Contains(combined, "architecture") || strings.Contains(combined, "overhaul") || strings.Contains(combined, "redesign") {
		tier = "complex"
		agent = "coder"
		outcomeType = "code_pr"
		stages = []string{"Cross-Workspace Analysis", "Implement Protocol & State", "Implement UI & Handlers", "Cross-Workspace Verification"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_pr", Title: "Cross-Workspace Feature PR & Integration Tests", Kind: "pr", Status: "pending"},
		}
	} else if isBug {
		tier = "direct"
		agent = "coder"
		outcomeType = "bug_patch"
		stages = []string{"Reproduce with Test", "Author Minimal Fix", "Run Regression Gate", "Review & Integrate"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_patch", Title: "Regression Test & Minimal Patch", Kind: "patch", Status: "pending"},
		}
	} else {
		tier = "direct"
		agent = "coder"
		outcomeType = "code_pr"
		stages = []string{"Inspect Architecture", "Implement Feature", "Run Critical Testbench", "Review & Land"}
		deliverables = []ProjectTaskDeliverable{
			{ID: "deliv_pr", Title: "Feature Branch & Critical Tests", Kind: "pr", Status: "pending"},
		}
	}

	if lastError != "" {
		tier = "complex"
		stages = append([]string{"Diagnose Failure: " + truncateString(lastError, 40)}, stages...)
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
	mission := fmt.Sprintf("Autonomous %s mission: %s. Operates in isolated worktree %s.", agent, prompt, branch)

	wsLabels := make([]string, 0, len(detected))
	for _, p := range detected {
		wsLabels = append(wsLabels, filepath.Base(p))
	}
	wsJoined := strings.Join(wsLabels, ", ")

	var planSummary string
	if tier == "discovery" {
		planSummary = fmt.Sprintf("1. Read-only discovery across [%s]\n2. Analyze relevant invariants & architectural constraints\n3. Return structured findings report without workspace edits", wsJoined)
	} else if tier == "complex" {
		planSummary = fmt.Sprintf("1. Multi-phase execution coordinating [%s]\n2. Implement component and state changes in isolated worktree\n3. Run cross-workspace validation gate before integration", wsJoined)
	} else {
		planSummary = fmt.Sprintf("1. Direct scoped implementation in [%s]\n2. Execute minimal verified patch\n3. Review deliverable and integrate into branch", wsJoined)
	}

	if feedback != "" {
		planSummary += "\n[Refined]: Plan adjusted to incorporate user instructions."
	}
	if lastError != "" {
		planSummary += fmt.Sprintf("\n[Error Recovery]: Re-planning to resolve: %s", truncateString(lastError, 60))
	}

	var fullPlan strings.Builder
	fullPlan.WriteString(fmt.Sprintf("### Task Mission: %s\n\n", title))
	fullPlan.WriteString(fmt.Sprintf("- **Tier**: `%s` (Tier %s)\n", strings.ToUpper(tier[:1])+tier[1:], tierNumber(tier)))
	fullPlan.WriteString(fmt.Sprintf("- **Assigned Agent**: `@%s`\n", agent))
	fullPlan.WriteString(fmt.Sprintf("- **Expected Outcome**: `%s`\n", outcomeType))
	fullPlan.WriteString(fmt.Sprintf("- **Workspaces Involved**: %s\n", strings.Join(wsLabels, ", ")))
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
		PlanSummary:        planSummary,
		FullPlanMarkdown:   fullPlan.String(),
		Tier:               tier,
	}
}
