package taskrouter

import (
	"context"
	"errors"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestService_RouteTask_DeterministicCompilationWithoutInvoker(t *testing.T) {
	// Purpose:
	// - Invariant: When AI Router is not configured (offline / unit tests), RouteTask
	//   deterministically compiles the task plan based on explicit structured intent and agent.
	// - Boundary/authority: Service.RouteTask in taskrouter/service.go.
	// - Threat/regression: Fragile keyword matching produces wrong agents on fallback.

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

	res, err := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt:             "Fix memory leak in pebble iterator and handle close errors",
		RequestedWorkspace: "/workspace/backend",
		Intent:             "code",
		FeatureSize:        "small",
		Project:            project,
	})
	if err != nil {
		t.Fatalf("unexpected error on deterministic compilation: %v", err)
	}
	if res.Agent != "coder" {
		t.Fatalf("expected agent coder, got %q", res.Agent)
	}
	if res.Tier != "direct" {
		t.Fatalf("expected direct tier, got %q", res.Tier)
	}
	if res.OutcomeType != "code_pr" {
		t.Fatalf("expected outcome_type code_pr, got %q", res.OutcomeType)
	}
	if len(res.WorkspacesInvolved) != 1 || res.WorkspacesInvolved[0] != "/workspace/backend" {
		t.Fatalf("expected 1 workspace involved (/workspace/backend), got %v", res.WorkspacesInvolved)
	}
}

func TestService_RouteTask_ExplicitAgentCoderWithMediaKeywords(t *testing.T) {
	// Purpose:
	// - Invariant: When an explicit agent="coder" is specified, mentioning media keywords
	//   (PNG, image, audio, video) in the prompt must NOT turn the task into image generation.
	// - Threat/regression: Regression where Orchestrator proposed PNG upload UI and router turned it into media_bundle.

	svc := NewService()
	res, err := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt:      "Allow profile PNG upload or selection from media, remove acct_* label, improve project layout",
		Agent:       "coder",
		FeatureSize: "small",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Agent != "coder" {
		t.Fatalf("expected agent coder, got %q", res.Agent)
	}
	if res.OutcomeType != "code_pr" {
		t.Fatalf("expected outcome_type code_pr, got %q", res.OutcomeType)
	}
	if len(res.Deliverables) == 0 || res.Deliverables[0].Kind != "pr" {
		t.Fatalf("expected PR deliverable, got %+v", res.Deliverables)
	}
	for _, d := range res.Deliverables {
		if d.Kind == "image" || d.Kind == "video" || d.Kind == "audio" {
			t.Fatalf("coder task must not have media deliverable: %+v", d)
		}
	}
}

func TestService_RouteTask_RouterInvokerError_FailsExplicitly(t *testing.T) {
	// Purpose:
	// - Invariant: When the AI Router invoker fails (times out, returns error, or fails to parse),
	//   RouteTask must fail explicitly with the error rather than silently creating an unrelated task.
	// - Boundary/authority: Service.RouteTask in taskrouter/service.go.
	// - Threat/regression: Silent fallback with keyword heuristics corrupting user intent.

	failingInvoker := func(ctx context.Context, instructions string, input string) (string, error) {
		return "", errors.New("upstream LLM timeout (504)")
	}

	svc := NewService(failingInvoker)
	_, err := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt: "Implement distributed locking for pebble store",
		Intent: "code",
	})
	if err == nil {
		t.Fatalf("expected explicit error when AI router fails")
	}
	if !strings.Contains(err.Error(), "upstream LLM timeout") {
		t.Fatalf("expected error to contain upstream failure details, got %v", err)
	}
}

func TestService_RouteTask_WithAIRouterInvoker_PreservesContract(t *testing.T) {
	// Purpose:
	// - Invariant: When AI Router invoker succeeds, it elaborates task data (title, stages, plan markdown)
	//   but CANNOT change or overwrite the authoritative execution contract (agent, outcome, tier, branch).
	// - Boundary/authority: Service.RouteTask in taskrouter/service.go.

	mockAIInvoker := func(ctx context.Context, instructions string, input string) (string, error) {
		return `
		{
			"title": "Build AI Video Generator Panel",
			"agent": "image",
			"tier": "complex",
			"outcome_type": "media_bundle",
			"mission": "Autonomous coder mission to build video panel in desktop UI",
			"stages": ["Design Component", "Implement State", "Verify In Browser"],
			"plan_summary": "1. Build VideoPanel.tsx\n2. Hook to video API\n3. Verify render",
			"full_plan_markdown": "# Plan: Build Video Panel\nVerified by AI router."
		}`, nil
	}

	svc := NewService(mockAIInvoker)
	res, err := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt:      "Build AI video panel in desktop UI",
		Intent:      "code",
		FeatureSize: "small",
		Agent:       "coder",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Title != "Build AI Video Generator Panel" {
		t.Fatalf("expected title from AI router, got %q", res.Title)
	}
	// Contract must NOT be hijacked to "image" or "media_bundle" despite the AI router output
	if res.Agent != "coder" {
		t.Fatalf("expected agent coder preserved, got %q", res.Agent)
	}
	if res.OutcomeType != "code_pr" {
		t.Fatalf("expected outcome_type code_pr preserved, got %q", res.OutcomeType)
	}
	if res.Tier != "direct" {
		t.Fatalf("expected tier direct preserved, got %q", res.Tier)
	}
	if !strings.HasPrefix(res.Branch, "agent/") {
		t.Fatalf("expected sanitized worktree branch starting with 'agent/', got %q", res.Branch)
	}
	if len(res.Stages) != 3 {
		t.Fatalf("expected 3 stages from AI router, got %d", len(res.Stages))
	}
}

func TestService_RouteTask_AuditIntent_RoutesToFinder(t *testing.T) {
	// Purpose:
	// - Invariant: Intent="audit" must route to finder agent with discovery tier and audit_report outcome.
	svc := NewService()
	res, err := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt: "Review and audit credential encryption boundaries",
		Intent: "audit",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Agent != "finder" {
		t.Fatalf("expected agent 'finder', got %q", res.Agent)
	}
	if res.Tier != "discovery" {
		t.Fatalf("expected tier 'discovery', got %q", res.Tier)
	}
	if res.OutcomeType != "audit_report" {
		t.Fatalf("expected outcome 'audit_report', got %q", res.OutcomeType)
	}
}

func TestService_RouteTask_BigFeature_RoutesToPlan(t *testing.T) {
	// Purpose:
	// - Invariant: Intent="code" with feature_size="big" must route to plan agent with complex tier and plan_spec outcome.
	svc := NewService()
	res, err := svc.RouteTask(context.Background(), TaskRouteOptions{
		Prompt:      "Overhaul the entire persistence layer to support multi-database transactions",
		Intent:      "code",
		FeatureSize: "big",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Agent != "plan" {
		t.Fatalf("expected agent 'plan' for big feature, got %q", res.Agent)
	}
	if res.Tier != "complex" {
		t.Fatalf("expected tier 'complex', got %q", res.Tier)
	}
	if res.OutcomeType != "plan_spec" {
		t.Fatalf("expected outcome 'plan_spec', got %q", res.OutcomeType)
	}
}
