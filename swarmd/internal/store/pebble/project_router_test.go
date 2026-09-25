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

	t.Run("fallback routes code prompt directly to coder agent with alert", func(t *testing.T) {
		prompt := "Update Desktop UI navigation bar modal styling"
		hero := "/workspace/backend"
		result := RouteAndPlanProjectTask(prompt, hero, "", workspaces, "", "")

		if result.Agent != "coder" {
			t.Fatalf("expected agent coder, got %q", result.Agent)
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

	t.Run("fallback preserves image options while routing to direct image generation", func(t *testing.T) {
		result := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt:             "Make 3 promotional banner images for social media",
			RequestedWorkspace: "/workspace/web",
			Workspaces:         workspaces,
			Intent:             "image",
			AspectRatio:        "16:9",
			VariantCount:       4,
		})

		if result.Agent != "image" {
			t.Fatalf("expected agent image, got %q", result.Agent)
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

	t.Run("fallback routes html animation to designer and recorded app to swarm", func(t *testing.T) {
		htmlRes := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt: "Build interactive HTML motion UI card animation",
			Intent: "video",
		})
		if htmlRes.Agent != "designer" {
			t.Fatalf("expected designer for HTML animation, got %q", htmlRes.Agent)
		}

		demoRes := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt: "Record live app demo walkthrough of desktop client",
			Intent: "video",
		})
		if demoRes.Agent != "swarm" {
			t.Fatalf("expected swarm for recorded app demo, got %q", demoRes.Agent)
		}

		genVidRes := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt: "Cinematic drone flyover teaser",
			Intent: "video",
		})
		if genVidRes.Agent != "video" {
			t.Fatalf("expected video for generative video, got %q", genVidRes.Agent)
		}
	})

	t.Run("fallback routes complex features to plan agent", func(t *testing.T) {
		planRes := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt: "Architect and implement an end-to-end multi-phase data pipeline overhaul",
			Intent: "code",
		})
		if planRes.Agent != "plan" {
			t.Fatalf("expected plan agent for complex code, got %q", planRes.Agent)
		}
		if planRes.Tier != "complex" {
			t.Fatalf("expected complex tier, got %q", planRes.Tier)
		}
	})
}
