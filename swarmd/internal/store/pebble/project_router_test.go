package pebblestore

import (
	"strings"
	"testing"
)

func TestRouteAndPlanProjectTask(t *testing.T) {
	// Purpose:
	// - Invariant: Fallback routing without AI Router must directly use the canonical "swarm"
	//   system agent at tier "direct" without fragile keyword scoring heuristics, preserve
	//   the designated workspace, and attach a clear RouterAlert warning.
	// - Boundary/authority: RouteAndPlanProjectTask in project_router.go.
	// - Threat/regression: Fragile keyword matching produces brittle multi-workspace plans or wrong agents on fallback.

	workspaces := []ProjectWorkspaceRef{
		{Path: "/workspace/backend", Label: "Backend", Role: "primary_code"},
		{Path: "/workspace/web", Label: "Desktop UI", Role: "desktop"},
		{Path: "/workspace/ops", Label: "Critical Operations", Role: "ops"},
	}

	t.Run("fallback routes directly to swarm agent with alert", func(t *testing.T) {
		prompt := "Update Desktop UI navigation bar modal styling"
		hero := "/workspace/backend"
		result := RouteAndPlanProjectTask(prompt, hero, "", workspaces, "", "")

		if result.Agent != "swarm" {
			t.Fatalf("expected default agent swarm, got %q", result.Agent)
		}
		if result.Tier != "direct" {
			t.Fatalf("expected direct tier for fallback, got %q", result.Tier)
		}
		if len(result.WorkspacesInvolved) != 1 || result.WorkspacesInvolved[0] != hero {
			t.Fatalf("expected exactly hero workspace %q, got %v", hero, result.WorkspacesInvolved)
		}
		if result.RouterAlert == "" {
			t.Fatalf("expected RouterAlert to be set on fallback, got empty")
		}
		if !strings.Contains(result.FullPlanMarkdown, "Router Agent Alert") {
			t.Fatalf("expected FullPlanMarkdown to include Router Agent Alert warning, got:\n%s", result.FullPlanMarkdown)
		}
	})

	t.Run("fallback without requested workspace defaults to first project workspace", func(t *testing.T) {
		prompt := "Figure out why the Pebble database locks on restart"
		result := RouteAndPlanProjectTask(prompt, "", "", workspaces, "", "")

		if result.Agent != "swarm" {
			t.Fatalf("expected agent swarm, got %q", result.Agent)
		}
		if len(result.WorkspacesInvolved) != 1 || result.WorkspacesInvolved[0] != "/workspace/backend" {
			t.Fatalf("expected first workspace /workspace/backend, got %v", result.WorkspacesInvolved)
		}
		if result.RouterAlert == "" {
			t.Fatalf("expected RouterAlert to be populated")
		}
	})

	t.Run("fallback preserves image options while routing to swarm with alert", func(t *testing.T) {
		result := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt:             "Make 3 promotional banner images for social media",
			RequestedWorkspace: "/workspace/web",
			Workspaces:         workspaces,
			Intent:             "image",
			AspectRatio:        "16:9",
			VariantCount:       4,
		})

		if result.Agent != "swarm" {
			t.Fatalf("expected agent swarm, got %q", result.Agent)
		}
		if result.OutcomeType != "media_bundle" {
			t.Fatalf("expected outcome media_bundle, got %q", result.OutcomeType)
		}
		if result.AspectRatio != "16:9" {
			t.Fatalf("expected aspect ratio 16:9 preserved, got %q", result.AspectRatio)
		}
		if result.VariantCount != 4 {
			t.Fatalf("expected variant count 4 preserved, got %d", result.VariantCount)
		}
		if result.RouterAlert == "" {
			t.Fatalf("expected RouterAlert to be set")
		}
	})
}
