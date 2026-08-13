package run

import (
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBuildFinalPlanExecutionHandoffProjectsStructuredMetadataAndConciseContent(t *testing.T) {
	recommendation := &pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Action: "review", Reason: "targeted checks passed", ActionState: "ready"}
	doc := &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, LastCheckpointID: "cp-1"},
		Artifacts:       []pebblestore.SessionPlanArtifactReference{{Path: "docs/shared-index.json", Role: "input"}},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Durable handoff", Status: sessionruntime.PlanCheckpointStatusCompleted, Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "out/visible-list.md", Role: "deliverable", Description: "Visible list"}},
			Report: "full report sentinel", Result: "result sentinel", ChangedFiles: []string{"file.go"}, Validation: []string{"focused test"},
			Recommendation: recommendation,
			Handoff: &pebblestore.SessionPlanCheckpointHandoff{
				Title: "Ready to review", Overview: "The shared contract is ready.", ImpactBullets: []string{"Clients receive the same projection."},
				CopyableCodeBlocks: []pebblestore.PlanFinalHandoffCopyableCodeBlock{{Label: "Run this command", Language: "bash", Code: "swarm status"}},
				SuggestedPrompts:   []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "Review", Prompt: "Review the final handoff."}},
				PullRequestURL:     "https://github.com/swarm/repository/pull/42",
			},
		}},
		ActiveCheckpointID: "cp-1",
	}
	message, ok := BuildFinalPlanExecutionHandoffSystemMessage(PlanExecutionLifecycleMessageInput{
		Action: "complete_checkpoint",
		Plan:   pebblestore.SessionPlanSnapshot{ID: "plan-1", Title: "Plan", Document: doc},
		Payload: map[string]any{
			"next_action": "await_review", "checkpoint_id": "cp-1", "report": "full report sentinel", "result": "result sentinel",
		},
	})
	if !ok {
		t.Fatal("final handoff was not built")
	}
	if message.Content != "Ready to review\n\nThe shared contract is ready.\n- Clients receive the same projection.\n\nRun this command\n```bash\nswarm status\n```" {
		t.Fatalf("content = %q", message.Content)
	}
	if strings.Contains(message.Content, "full report sentinel") || strings.Contains(message.Content, "result sentinel") || strings.Contains(message.Content, "swarm-handoff-summary") {
		t.Fatalf("content leaked details or legacy marker: %q", message.Content)
	}
	projection, ok := message.Metadata["final_handoff"].(*pebblestore.PlanFinalHandoff)
	if !ok || projection == nil {
		t.Fatalf("final_handoff metadata = %#v", message.Metadata["final_handoff"])
	}
	if projection.SchemaVersion != 1 || len(projection.CopyableCodeBlocks) != 1 || projection.CopyableCodeBlocks[0].Code != "swarm status" || projection.Recommendation == nil || projection.Recommendation.Action != "review" || projection.Details.Report != "full report sentinel" || projection.Details.Result != "result sentinel" || projection.PullRequestURL != "https://github.com/swarm/repository/pull/42" {
		t.Fatalf("projection = %#v", projection)
	}
	if len(projection.Artifacts) != 1 || projection.Artifacts[0].ID == "" || projection.Artifacts[0].Label != "Visible list" || projection.Artifacts[0].Filename != "visible-list.md" || projection.Artifacts[0].MediaType != "text/markdown" {
		t.Fatalf("final handoff artifacts = %#v", projection.Artifacts)
	}
	artifacts, ok := message.Metadata["artifacts"].([]pebblestore.SessionPlanArtifactReference)
	if !ok || len(artifacts) != 2 || artifacts[0].Path != "docs/shared-index.json" || artifacts[1].Path != "out/visible-list.md" || artifacts[1].Role != "deliverable" {
		t.Fatalf("artifact metadata = %#v", message.Metadata["artifacts"])
	}
}

func TestBuildFinalPlanExecutionHandoffKeepsLegacyContentWhenStructuredFieldsAreAbsent(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, LastCheckpointID: "cp-legacy"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-legacy", Title: "Legacy", Status: sessionruntime.PlanCheckpointStatusCompleted,
			Report: "legacy report", Result: "legacy result", Validation: []string{"legacy validation"},
		}},
		ActiveCheckpointID: "cp-legacy",
	}
	message, ok := BuildFinalPlanExecutionHandoffSystemMessage(PlanExecutionLifecycleMessageInput{
		Action: "complete_checkpoint",
		Plan:   pebblestore.SessionPlanSnapshot{ID: "plan-legacy", Title: "Legacy plan", Document: doc},
		Payload: map[string]any{
			"next_action": "await_review", "checkpoint_id": "cp-legacy", "report": "legacy report", "result": "legacy result", "validation": []string{"legacy validation"},
		},
	})
	if !ok {
		t.Fatal("legacy final handoff was not built")
	}
	if _, exists := message.Metadata["final_handoff"]; exists {
		t.Fatalf("legacy message unexpectedly gained structured metadata: %#v", message.Metadata)
	}
	for _, want := range []string{"Final checkpoint handoff", "The last checkpoint is complete", "legacy report", "legacy result", "legacy validation"} {
		if !strings.Contains(message.Content, want) {
			t.Fatalf("legacy content missing %q: %q", want, message.Content)
		}
	}
}

func TestBuildBlockedPlanExecutionHandoffProjectsCopyableCodeBlock(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateBlocked, LastCheckpointID: "cp-1"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Deploy", Status: sessionruntime.PlanCheckpointStatusBlocked,
			Handoff: &pebblestore.SessionPlanCheckpointHandoff{
				Title: "Credentials required", Overview: "Deployment cannot continue until credentials are configured.",
				CopyableCodeBlocks: []pebblestore.PlanFinalHandoffCopyableCodeBlock{{Label: "Configure credentials", Language: "bash", Code: "swarm auth login"}},
			},
		}},
		ActiveCheckpointID: "cp-1",
	}
	message, ok := BuildBlockedPlanExecutionHandoffSystemMessage(PlanExecutionLifecycleMessageInput{
		Action: "mark_blocked", Plan: pebblestore.SessionPlanSnapshot{ID: "plan-1", Document: doc},
		Payload: map[string]any{"next_action": "stopped", "checkpoint_id": "cp-1"},
	})
	if !ok || !strings.Contains(message.Content, "```bash\nswarm auth login\n```") {
		t.Fatalf("blocked handoff = %#v", message)
	}
	projection, ok := message.Metadata["blocked_handoff"].(*pebblestore.PlanFinalHandoff)
	if !ok || len(projection.CopyableCodeBlocks) != 1 || projection.CopyableCodeBlocks[0].Label != "Configure credentials" {
		t.Fatalf("blocked projection = %#v", message.Metadata["blocked_handoff"])
	}
}

func TestPlanDocumentArgsParseAndValidateFinalHandoff(t *testing.T) {
	patch, err := planDocumentPatchFromArgs(map[string]any{
		"action":           "complete_subtask",
		"checkpoint_id":    "cp-1",
		"handoff_title":    "Ready",
		"handoff_overview": "The contract is complete.",
		"impact_bullets":   []any{"One contract."},
		"copyable_code_blocks": []any{map[string]any{
			"label": "Run this command", "language": "bash", "code": "swarm status",
		}},
		"suggested_prompts": []any{map[string]any{
			"label": "Review", "prompt": "Review the contract for gaps.",
		}},
		"pull_request_url": "https://github.com/swarm/repository/pull/42",
		"artifacts": []any{map[string]any{
			"path": "gallery/index.html", "role": "deliverable", "description": "Interactive gallery", "media_type": "text/html",
		}},
	})
	if err != nil {
		t.Fatalf("parse handoff args: %v", err)
	}
	if patch == nil || patch.Handoff == nil || patch.Handoff.Title != "Ready" || len(patch.Handoff.CopyableCodeBlocks) != 1 || patch.Handoff.CopyableCodeBlocks[0].Code != "swarm status" || len(patch.Handoff.SuggestedPrompts) != 1 || patch.Handoff.PullRequestURL != "https://github.com/swarm/repository/pull/42" || len(patch.Artifacts) != 1 || patch.Artifacts[0].Path != "gallery/index.html" || patch.Artifacts[0].Role != "deliverable" {
		t.Fatalf("patch = %#v", patch)
	}
	_, err = planDocumentPatchFromArgs(map[string]any{"action": "complete_checkpoint", "handoff_title": "missing overview"})
	if err == nil || !strings.Contains(err.Error(), "overview is required") {
		t.Fatalf("missing overview error = %v", err)
	}
}
