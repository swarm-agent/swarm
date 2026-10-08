package pebblestore

import (
	"strings"
	"testing"
)

func TestRouteAndPlanProjectTask(t *testing.T) {
	// Purpose:
	// - Invariant: Explicit structured intent and agent override must control task routing contract.
	//   Keyword string matching on prompts is eliminated; media keywords in code prompts
	//   must NOT hijack engineering tasks into media generation.
	// - Boundary/authority: RouteAndPlanProjectTaskWithOptions in project_router.go.
	// - Threat/regression: Fragile keyword matching producing wrong agents or image generation for code tasks.

	workspaces := []ProjectWorkspaceRef{
		{Path: "/workspace/backend", Label: "Backend", Role: "primary_code"},
		{Path: "/workspace/web", Label: "Desktop UI", Role: "desktop"},
		{Path: "/workspace/ops", Label: "Critical Operations", Role: "ops"},
	}

	t.Run("explicit agent coder with media keywords produces code PR not media", func(t *testing.T) {
		prompt := "Allow profile PNG upload or selection from media, remove acct_* label, improve project layout"
		hero := "/workspace/web"
		result, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt:             prompt,
			RequestedWorkspace: hero,
			Workspaces:         workspaces,
			Agent:              "coder",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Agent != "coder" {
			t.Fatalf("expected agent coder, got %q", result.Agent)
		}
		if result.Tier != "direct" {
			t.Fatalf("expected direct tier, got %q", result.Tier)
		}
		if result.OutcomeType != "code_pr" {
			t.Fatalf("expected outcome_type code_pr, got %q", result.OutcomeType)
		}
		if len(result.Deliverables) == 0 || result.Deliverables[0].Kind != "pr" {
			t.Fatalf("expected code PR deliverable, got %+v", result.Deliverables)
		}
		for _, d := range result.Deliverables {
			if d.Kind == "image" || d.Kind == "video" || d.Kind == "audio" {
				t.Fatalf("code task must not have media deliverable: %+v", d)
			}
		}
		if !strings.HasPrefix(result.Branch, "agent/") {
			t.Fatalf("expected isolated worktree branch prefix agent/, got %q", result.Branch)
		}
	})

	t.Run("explicit intent code with feature_size small produces coder agent", func(t *testing.T) {
		result, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt:             "Fix memory leak in pebble iterator and handle close errors",
			RequestedWorkspace: "/workspace/backend",
			Workspaces:         workspaces,
			Intent:             "code",
			FeatureSize:        "small",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Agent != "coder" {
			t.Fatalf("expected agent coder, got %q", result.Agent)
		}
		if result.Tier != "direct" {
			t.Fatalf("expected tier direct, got %q", result.Tier)
		}
		if result.OutcomeType != "code_pr" {
			t.Fatalf("expected outcome code_pr, got %q", result.OutcomeType)
		}
	})

	// Purpose: RouteAndPlanProjectTaskWithOptions must default big code work to
	// Swarm, not a read-only Plan run. This pure routing test checks agent and output;
	// explicit Plan and invalid configurations remain separate cases below.
	t.Run("explicit intent code with feature_size big produces swarm agent with complex tier", func(t *testing.T) {
		result, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt:             "Architect and implement multi-region sync engine",
			RequestedWorkspace: "/workspace/backend",
			Workspaces:         workspaces,
			Intent:             "code",
			FeatureSize:        "big",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Agent != "swarm" {
			t.Fatalf("expected agent swarm, got %q", result.Agent)
		}
		if result.Tier != "complex" {
			t.Fatalf("expected tier complex, got %q", result.Tier)
		}
		if result.OutcomeType != "code_pr" {
			t.Fatalf("expected outcome code_pr, got %q", result.OutcomeType)
		}
		if len(result.Deliverables) == 0 || result.Deliverables[0].Kind != "pr" {
			t.Fatalf("expected code deliverable, got %+v", result.Deliverables)
		}
	})

	t.Run("explicit intent audit produces finder agent with discovery tier", func(t *testing.T) {
		result, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt:             "Review and audit credential encryption boundaries",
			RequestedWorkspace: "/workspace/backend",
			Workspaces:         workspaces,
			Intent:             "audit",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Agent != "finder" {
			t.Fatalf("expected agent finder, got %q", result.Agent)
		}
		if result.Tier != "discovery" {
			t.Fatalf("expected tier discovery, got %q", result.Tier)
		}
		if result.OutcomeType != "audit_report" {
			t.Fatalf("expected outcome audit_report, got %q", result.OutcomeType)
		}
		if len(result.Deliverables) == 0 || result.Deliverables[0].Kind != "report" {
			t.Fatalf("expected audit report deliverable, got %+v", result.Deliverables)
		}
	})

	t.Run("explicit intent image produces image agent and preserves aspect ratio and variants", func(t *testing.T) {
		result, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt:       "Cyberpunk city skyline at dusk",
			Intent:       "image",
			AspectRatio:  "16:9",
			VariantCount: 4,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Agent != "image" {
			t.Fatalf("expected agent image, got %q", result.Agent)
		}
		if result.OutcomeType != "media_bundle" {
			t.Fatalf("expected outcome media_bundle, got %q", result.OutcomeType)
		}
		if result.AspectRatio != "16:9" {
			t.Fatalf("expected aspect ratio 16:9, got %q", result.AspectRatio)
		}
		if result.VariantCount != 4 {
			t.Fatalf("expected variant count 4, got %d", result.VariantCount)
		}
		if len(result.Deliverables) != 4 {
			t.Fatalf("expected 4 image deliverables, got %d", len(result.Deliverables))
		}
		if result.Branch != "" {
			t.Fatalf("media generation should not allocate worktree branch, got %q", result.Branch)
		}
	})

	t.Run("explicit intent video produces video agent", func(t *testing.T) {
		result, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt:      "Cinematic fly-through of server room",
			Intent:      "video",
			AspectRatio: "16:9",
			ScenesCount: 3,
			Soundtrack:  "Deep ambient synth",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Agent != "video" {
			t.Fatalf("expected agent video, got %q", result.Agent)
		}
		if result.OutcomeType != "video_story" {
			t.Fatalf("expected outcome video_story, got %q", result.OutcomeType)
		}
		if len(result.Scenes) != 3 {
			t.Fatalf("expected 3 scenes compiled, got %d", len(result.Scenes))
		}
	})

	t.Run("explicit intent sound produces sound agent", func(t *testing.T) {
		result, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt: "Upbeat electronic theme song",
			Intent: "sound",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Agent != "sound" {
			t.Fatalf("expected agent sound, got %q", result.Agent)
		}
		if result.OutcomeType != "audio_clip" {
			t.Fatalf("expected outcome audio_clip, got %q", result.OutcomeType)
		}
		if len(result.Deliverables) == 0 || result.Deliverables[0].Kind != "audio" {
			t.Fatalf("expected audio deliverable, got %+v", result.Deliverables)
		}
	})

	t.Run("fails closed on missing agent and intent", func(t *testing.T) {
		_, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt: "Do something random",
		})
		if err == nil || !strings.Contains(err.Error(), "missing structured configuration") {
			t.Fatalf("expected missing structured configuration error, got %v", err)
		}
	})

	t.Run("fails closed on conflicting coder with big feature", func(t *testing.T) {
		_, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt:      "Overhaul persistence",
			Agent:       "coder",
			FeatureSize: "big",
		})
		if err == nil || !strings.Contains(err.Error(), "cannot use coder agent") {
			t.Fatalf("expected conflict error for coder with big feature, got %v", err)
		}
	})

	t.Run("fails closed on unknown agent", func(t *testing.T) {
		_, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt: "Hack database",
			Agent:  "magic_hacker",
		})
		if err == nil || !strings.Contains(err.Error(), "unknown task agent") {
			t.Fatalf("expected unknown agent error, got %v", err)
		}
	})

	t.Run("fails closed on unknown intent", func(t *testing.T) {
		_, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt: "Do weird stuff",
			Intent: "teleportation",
		})
		if err == nil || !strings.Contains(err.Error(), "unknown task intent") {
			t.Fatalf("expected unknown intent error, got %v", err)
		}
	})

	t.Run("designer produces artifact deliverables not image deliverables", func(t *testing.T) {
		result, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{
			Prompt:       "Modern pricing table with toggle",
			Intent:       "design",
			VariantCount: 3,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Agent != "designer" {
			t.Fatalf("expected agent designer, got %q", result.Agent)
		}
		if result.OutcomeType != "artifact" {
			t.Fatalf("expected outcome artifact, got %q", result.OutcomeType)
		}
		if len(result.Deliverables) != 3 {
			t.Fatalf("expected 3 deliverables, got %d", len(result.Deliverables))
		}
		for _, d := range result.Deliverables {
			if d.Kind != "artifact" {
				t.Fatalf("designer deliverable must be kind 'artifact', got %q", d.Kind)
			}
		}
	})
}

func TestProjectTaskValidationCoherence(t *testing.T) {
	// Purpose:
	// - Invariant: ProjectTaskRecord.Validate must reject incoherent agent/outcome combinations.
	// - Boundary/authority: ProjectTaskRecord.Validate in project_store.go.
	// - Threat/regression: Corrupted tasks mixing coder agent with image deliverables or media bundle outcome.

	t.Run("rejects coder with media_bundle outcome", func(t *testing.T) {
		task := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Update UI",
			Agent:       "coder",
			OutcomeType: "media_bundle",
		}
		err := task.Validate()
		if err == nil || !strings.Contains(err.Error(), "incoherent task contract") {
			t.Fatalf("expected incoherent contract error for coder with media_bundle, got %v", err)
		}
	})

	t.Run("rejects coder with media deliverables", func(t *testing.T) {
		task := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Update UI",
			Agent:       "coder",
			OutcomeType: "code_pr",
			Deliverables: []ProjectTaskDeliverable{
				{ID: "d1", Title: "image.png", Kind: "image", Status: "pending"},
			},
		}
		err := task.Validate()
		if err == nil || !strings.Contains(err.Error(), "cannot have media deliverables") {
			t.Fatalf("expected error for coder with image deliverable, got %v", err)
		}
	})

	t.Run("rejects finder with code_pr outcome", func(t *testing.T) {
		task := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Audit auth",
			Agent:       "finder",
			OutcomeType: "code_pr",
		}
		err := task.Validate()
		if err == nil || !strings.Contains(err.Error(), "incoherent task contract") {
			t.Fatalf("expected incoherent contract error for finder with code_pr, got %v", err)
		}
	})

	t.Run("accepts coherent coder task", func(t *testing.T) {
		task := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Fix bug",
			Agent:       "coder",
			OutcomeType: "code_pr",
			Deliverables: []ProjectTaskDeliverable{
				{ID: "d1", Title: "Pull Request", Kind: "pr", Status: "pending"},
			},
		}
		if err := task.Validate(); err != nil {
			t.Fatalf("expected coherent coder task to validate, got %v", err)
		}
	})

	t.Run("rejects coder with feature_size big", func(t *testing.T) {
		task := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Big feature with coder",
			Agent:       "coder",
			FeatureSize: "big",
			OutcomeType: "code_pr",
		}
		err := task.Validate()
		if err == nil || !strings.Contains(err.Error(), "coder agent cannot have feature_size 'big'") {
			t.Fatalf("expected feature_size big conflict error, got %v", err)
		}
	})

	t.Run("rejects plan with feature_size small", func(t *testing.T) {
		task := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Small feature with plan",
			Agent:       "plan",
			FeatureSize: "small",
			OutcomeType: "plan_spec",
		}
		err := task.Validate()
		if err == nil || !strings.Contains(err.Error(), "plan agent cannot have feature_size 'small'") {
			t.Fatalf("expected feature_size small conflict error, got %v", err)
		}
	})

	t.Run("rejects empty agent", func(t *testing.T) {
		task := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "No agent",
			Agent:       "",
			OutcomeType: "code_pr",
		}
		err := task.Validate()
		if err == nil || !strings.Contains(err.Error(), "task agent is required") {
			t.Fatalf("expected agent required error, got %v", err)
		}
	})

	t.Run("rejects unknown agent", func(t *testing.T) {
		task := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Invalid agent",
			Agent:       "alien_worker",
			OutcomeType: "code_pr",
		}
		err := task.Validate()
		if err == nil || !strings.Contains(err.Error(), "unknown task agent") {
			t.Fatalf("expected unknown agent error, got %v", err)
		}
	})

	t.Run("rejects designer with image deliverable", func(t *testing.T) {
		task := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Design UI",
			Agent:       "designer",
			OutcomeType: "artifact",
			Deliverables: []ProjectTaskDeliverable{
				{ID: "d1", Title: "logo.png", Kind: "image", Status: "pending"},
			},
		}
		err := task.Validate()
		if err == nil || !strings.Contains(err.Error(), "cannot have media deliverables") {
			t.Fatalf("expected error for designer with image deliverable, got %v", err)
		}
	})

	t.Run("accepts designer with artifact deliverable", func(t *testing.T) {
		task := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Design UI",
			Agent:       "designer",
			OutcomeType: "artifact",
			Deliverables: []ProjectTaskDeliverable{
				{ID: "d1", Title: "Interactive UI", Kind: "artifact", Status: "pending"},
			},
		}
		if err := task.Validate(); err != nil {
			t.Fatalf("expected valid designer task, got %v", err)
		}
	})

	t.Run("validates parallel coder non-overlapping scopes in task program", func(t *testing.T) {
		// Overlapping scopes in same stage must fail
		overlappingTask := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Parallel task",
			Agent:       "coder",
			OutcomeType: "code_pr",
			TaskProgram: &TaskProgramDefinition{
				Stages: []TaskProgramStageSpec{
					{ID: "stage-1"},
				},
				Jobs: []TaskProgramJobSpec{
					{ID: "job-1", StageID: "stage-1", AgentType: "coder", OwnedScope: []string{"pkg/api/**"}},
					{ID: "job-2", StageID: "stage-1", AgentType: "coder", OwnedScope: []string{"pkg/api/**"}},
				},
			},
		}
		err := overlappingTask.Validate()
		if err == nil || !strings.Contains(err.Error(), "overlap") {
			t.Fatalf("expected scope overlap error, got %v", err)
		}

		// Distinct scopes in same stage must succeed
		distinctTask := &ProjectTaskRecord{
			ProjectID:   "p1",
			Title:       "Parallel task",
			Agent:       "coder",
			OutcomeType: "code_pr",
			TaskProgram: &TaskProgramDefinition{
				Stages: []TaskProgramStageSpec{
					{ID: "stage-1"},
				},
				Jobs: []TaskProgramJobSpec{
					{ID: "job-1", StageID: "stage-1", AgentType: "coder", OwnedScope: []string{"pkg/api/**"}},
					{ID: "job-2", StageID: "stage-1", AgentType: "coder", OwnedScope: []string{"web/src/**"}},
				},
			},
		}
		if err := distinctTask.Validate(); err != nil {
			t.Fatalf("expected distinct scopes to validate, got %v", err)
		}
	})
}

// Purpose: project ordering and the primary_code label cannot select an execution
// repository. Threat: an omitted multi-repository target launches a Coder in the
// coordination checkout. RouteAndPlanProjectTaskWithOptions is the narrowest
// deterministic routing layer; catalog authorization is checked by the API.
func TestProjectRouterDoesNotSelectFirstWorkspace(t *testing.T) {
	workspaces := []ProjectWorkspaceRef{{Path: "/coordination", Role: "primary_code"}, {Path: "/implementation", Role: "auxiliary"}}
	if _, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{Prompt: "Change code", Agent: "coder", Workspaces: workspaces}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("missing target selected project ordering: %v", err)
	}
	routed, err := RouteAndPlanProjectTaskWithOptions(TaskPlanOptions{Prompt: "Change code", Agent: "coder", RequestedWorkspace: "/implementation", Workspaces: workspaces})
	if err != nil || len(routed.WorkspacesInvolved) != 1 || routed.WorkspacesInvolved[0] != "/implementation" {
		t.Fatalf("explicit target lost: %#v %v", routed.WorkspacesInvolved, err)
	}
}
