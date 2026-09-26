package taskrouter

import (
	"context"
	"errors"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestService_RouteTask_FallbackRoutingWithAlert(t *testing.T) {
	// Purpose:
	// - Invariant: When the AI Router is not configured (offline / unit tests), RouteTask
	//   routes through the deterministic classifier, accurately targeting coder, finder, image,
	//   video, plan, or swarm, and attaches RouterAlert.
	// - Boundary/authority: Service.RouteTask in taskrouter/service.go.
	// - Threat/regression: Forcing all fallbacks to blind swarm breaks media generation and agent specialization.

	svc := NewService() // No invoker
	project := &pebblestore.ProjectRecord{
		ID:   "proj-test-1",
		Name: "Swarm Platform",
		Workspaces: []pebblestore.ProjectWorkspaceRef{
			{Path: "/workspace/backend", Label: "Backend Daemon", Role: "primary_code"},
			{Path: "/workspace/web", Label: "Desktop UI", Role: "desktop"},
		},
		ProjectContext: "Local-first AI coding platform with React desktop and Go daemon.",
	}

	res := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt:             "fix the sidebar layout and theme colors",
		RequestedWorkspace: "/workspace/web",
		Project:            project,
	})
	if res.Agent != "coder" {
		t.Fatalf("expected agent coder on fallback for code fix, got %q", res.Agent)
	}
	if res.Tier != "direct" {
		t.Fatalf("expected direct tier for fallback, got %q", res.Tier)
	}
	if len(res.WorkspacesInvolved) != 1 || res.WorkspacesInvolved[0] != "/workspace/web" {
		t.Fatalf("expected 1 workspace involved (/workspace/web), got %v", res.WorkspacesInvolved)
	}
	if res.RouterAlert == "" {
		t.Fatalf("expected RouterAlert to be populated when router invoker is missing")
	}
	if !strings.Contains(res.RouterAlert, "router agent invoker not configured") {
		t.Fatalf("expected RouterAlert to mention invoker not configured, got %q", res.RouterAlert)
	}
}

func TestService_RouteTask_RouterInvokerError_FallbackWithAlert(t *testing.T) {
	// Purpose:
	// - Invariant: When the AI Router invoker fails (times out, returns error, or fails to parse),
	//   RouteTask must cleanly fall back to deterministic routing and attach a clear RouterAlert.
	// - Boundary/authority: Service.RouteTask in taskrouter/service.go.
	// - Threat/regression: Failing router causing uncaught error or unhelpful crash instead of graceful fallback.

	failingInvoker := func(ctx context.Context, instructions string, input string) (string, error) {
		return "", errors.New("upstream LLM timeout (504)")
	}

	svc := NewService(failingInvoker)
	res := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt: "Implement distributed locking for pebble store",
	})

	if res.Agent != "coder" {
		t.Fatalf("expected agent coder on router error, got %q", res.Agent)
	}
	if res.Tier != "direct" {
		t.Fatalf("expected direct tier on router error, got %q", res.Tier)
	}
	if res.RouterAlert == "" {
		t.Fatalf("expected RouterAlert to be set on router error")
	}
	if !strings.Contains(res.RouterAlert, "upstream LLM timeout") {
		t.Fatalf("expected RouterAlert to contain error details, got %q", res.RouterAlert)
	}
}

func TestService_RouteTask_WithAIRouterInvoker(t *testing.T) {
	// Purpose:
	// - Invariant: When an AI Router invoker succeeds, RouteTask uses the LLM's authoritative
	//   structured output and leaves RouterAlert empty.
	// - Boundary/authority: Service.invokeAIRouter in taskrouter/service.go.
	// - Threat/regression: Unneeded fallback or alert when AI Router succeeded.

	mockAIInvoker := func(ctx context.Context, instructions string, input string) (string, error) {
		return `
		{
			"title": "Build AI Video Generator Panel",
			"agent": "coder",
			"tier": "direct",
			"outcome_type": "code_pr",
			"hero_workspace": "/workspace/web",
			"workspaces_involved": ["/workspace/web"],
			"branch": "agent/ai-video-panel",
			"mission": "Autonomous coder mission to build video panel in desktop UI",
			"stages": ["Design Component", "Implement State", "Verify In Browser"],
			"plan_summary": "1. Build VideoPanel.tsx\n2. Hook to video API\n3. Verify render",
			"full_plan_markdown": "# Plan: Build Video Panel\nVerified by AI router."
		}`, nil
	}

	svc := NewService(mockAIInvoker)
	res := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt: "Build AI video panel",
		Intent: "code",
	})

	if res.Title != "Build AI Video Generator Panel" {
		t.Fatalf("expected title from AI router, got %q", res.Title)
	}
	if res.Agent != "coder" {
		t.Fatalf("expected agent coder from AI router, got %q", res.Agent)
	}
	if res.Branch != "agent/ai-video-panel" {
		t.Fatalf("expected branch from AI router, got %q", res.Branch)
	}
	if res.PlanSummary != "1. Build VideoPanel.tsx\n2. Hook to video API\n3. Verify render" {
		t.Fatalf("expected plan summary from AI router, got %q", res.PlanSummary)
	}
	if res.RouterAlert != "" {
		t.Fatalf("expected empty RouterAlert when AI router succeeds, got %q", res.RouterAlert)
	}
}

func TestService_BuildAgentSeedPrompt(t *testing.T) {
	// Purpose:
	// - Invariant: Seed prompt injected into the session must contain user objective,
	//   all workspaces in context pool, PROJECT.md guidelines, outcome contract,
	//   full execution plan, and any RouterAlert warning.
	// - Boundary/authority: Service.BuildAgentSeedPrompt in taskrouter/service.go.
	// - Threat/regression: Omitting context pool or router warning leaves session agents unaware of boundary issues.

	svc := NewService()
	project := &pebblestore.ProjectRecord{
		ID:             "proj-test-3",
		Name:           "Swarm Platform",
		ProjectContext: "Never run whole-disk scans. Preserve local-first architecture.",
	}

	task := &pebblestore.ProjectTaskRecord{
		ID:                 "task-123",
		Title:              "Fix Sidebar Rendering Bug",
		Description:        "The sidebar flickers when resizing in dark mode.",
		WorkspacePath:      "/workspace/web",
		WorkspacesInvolved: []string{"/workspace/web"},
		ContextPoolSummary: "Primary: Desktop UI (desktop) • Contract: bug_patch",
		OutcomeType:        "bug_patch",
		WorktreeBranch:     "agent/fix-sidebar-flicker",
		Tier:               "direct",
		RouterAlert:        "AI Router unavailable (timeout). Defaulted to Swarm.",
		FullPlanMarkdown:   "1. Reproduce flicker with Cypress test\n2. Fix CSS transition in Sidebar.tsx",
	}

	prompt := svc.BuildAgentSeedPrompt(task, project)

	if !strings.Contains(prompt, "Fix Sidebar Rendering Bug") {
		t.Errorf("missing task title in seed prompt")
	}
	if !strings.Contains(prompt, "Router Agent Warning") {
		t.Errorf("missing Router Agent Warning in seed prompt")
	}
	if !strings.Contains(prompt, "AI Router unavailable (timeout)") {
		t.Errorf("missing router alert text in seed prompt")
	}
	if !strings.Contains(prompt, "The sidebar flickers when resizing") {
		t.Errorf("missing description in seed prompt")
	}
	if !strings.Contains(prompt, "/workspace/web") {
		t.Errorf("missing workspace path in seed prompt")
	}
	if !strings.Contains(prompt, "Never run whole-disk scans") {
		t.Errorf("missing PROJECT.md context in seed prompt")
	}
	if !strings.Contains(prompt, "bug_patch") {
		t.Errorf("missing outcome type in seed prompt")
	}
	if !strings.Contains(prompt, "Reproduce flicker with Cypress test") {
		t.Errorf("missing execution plan in seed prompt")
	}
}

func TestService_RouteTask_AttachedDoc_RoutesToFinder(t *testing.T) {
	// Invariant: Router should not have vision or parse files itself; it simply attaches
	// the doc reference and routes questions about documents to finder for investigation.
	svc := NewService()
	res := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt: "can you read this pasted design spec and tell me the requirements?",
		AttachedMedia: []pebblestore.ProjectTaskMediaRef{
			{
				ID:        "med_doc_1",
				Title:     "architecture_spec.md",
				Kind:      "doc",
				MediaType: "text/markdown",
				Data:      "# Architecture Spec\n\nMust support 25 swarm variants in real-time.",
			},
		},
	})
	if res.Agent != "finder" {
		t.Fatalf("expected agent 'finder' for attached doc inquiry, got %q", res.Agent)
	}
	if res.Tier != "discovery" {
		t.Fatalf("expected tier 'discovery' for attached doc inquiry, got %q", res.Tier)
	}
	if res.OutcomeType != "audit_report" {
		t.Fatalf("expected outcome 'audit_report', got %q", res.OutcomeType)
	}
	if len(res.AttachedMedia) != 1 {
		t.Fatalf("expected 1 attached media preserved, got %d", len(res.AttachedMedia))
	}

	task := &pebblestore.ProjectTaskRecord{
		Title:         res.Title,
		Agent:         res.Agent,
		OutcomeType:   res.OutcomeType,
		AttachedMedia: res.AttachedMedia,
	}
	seedPrompt := svc.BuildAgentSeedPrompt(task, nil)
	if !strings.Contains(seedPrompt, "architecture_spec.md") {
		t.Errorf("seed prompt missing attached media doc title")
	}
	if !strings.Contains(seedPrompt, "Must support 25 swarm variants") {
		t.Errorf("seed prompt missing attached media doc content")
	}
}

func TestService_RouteTask_AttachedImage_SwarmIterations(t *testing.T) {
	// Invariant: Attached image with request for 5 iterations routes to designer swarm
	// with 5 deliverable slots and preserves attached media reference.
	svc := NewService()
	res := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt:       "make me 5 iterations of this image in cyber lattice style",
		VariantCount: 5,
		AspectRatio:  "1:1",
		AttachedMedia: []pebblestore.ProjectTaskMediaRef{
			{
				ID:        "med_img_1",
				Title:     "cyber_lattice_core.png",
				Kind:      "image",
				MediaType: "image/png",
				URL:       "data:image/png;base64,sample",
			},
		},
	})
	if res.Agent != "designer" {
		t.Fatalf("expected agent 'designer' for 5-variant swarm iterations, got %q", res.Agent)
	}
	if res.Tier != "swarm" {
		t.Fatalf("expected tier 'swarm' for 5-variant iterations, got %q", res.Tier)
	}
	if len(res.Deliverables) != 5 {
		t.Fatalf("expected 5 deliverable slots, got %d", len(res.Deliverables))
	}
	if len(res.AttachedMedia) != 1 {
		t.Fatalf("expected attached media preserved, got %d", len(res.AttachedMedia))
	}
}

func TestService_RouteTask_25Variants_SwarmScaling(t *testing.T) {
	// Invariant: High iteration counts (e.g. 25 variants) scale without crashing
	// or truncating, allocating 25 deliverable slots for non-blocking execution.
	svc := NewService()
	res := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt:       "generate 25 variants for project logo",
		VariantCount: 25,
		AspectRatio:  "1:1",
	})
	if res.VariantCount != 25 {
		t.Fatalf("expected VariantCount 25, got %d", res.VariantCount)
	}
	if len(res.Deliverables) != 25 {
		t.Fatalf("expected 25 deliverable slots, got %d", len(res.Deliverables))
	}
	for i, d := range res.Deliverables {
		if d.Status != "pending" {
			t.Errorf("deliverable slot %d expected status 'pending', got %q", i, d.Status)
		}
	}
}
