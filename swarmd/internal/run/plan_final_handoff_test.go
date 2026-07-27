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
			ID: "cp-1", Title: "Durable handoff", Status: sessionruntime.PlanCheckpointStatusCompleted, Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "out/visible-list.md", Role: "deliverable"}},
			Report: "full report sentinel", Result: "result sentinel", ChangedFiles: []string{"file.go"}, Validation: []string{"focused test"},
			Recommendation: recommendation,
			Handoff: &pebblestore.SessionPlanCheckpointHandoff{
				Title: "Ready to review", Overview: "The shared contract is ready.", ImpactBullets: []string{"Clients receive the same projection."},
				SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "Review", Prompt: "Review the final handoff."}},
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
	if message.Content != "Ready to review\n\nThe shared contract is ready.\n- Clients receive the same projection." {
		t.Fatalf("content = %q", message.Content)
	}
	if strings.Contains(message.Content, "full report sentinel") || strings.Contains(message.Content, "result sentinel") || strings.Contains(message.Content, "swarm-handoff-summary") {
		t.Fatalf("content leaked details or legacy marker: %q", message.Content)
	}
	projection, ok := message.Metadata["final_handoff"].(*pebblestore.PlanFinalHandoff)
	if !ok || projection == nil {
		t.Fatalf("final_handoff metadata = %#v", message.Metadata["final_handoff"])
	}
	if projection.SchemaVersion != 1 || projection.Recommendation == nil || projection.Recommendation.Action != "review" || projection.Details.Report != "full report sentinel" || projection.Details.Result != "result sentinel" {
		t.Fatalf("projection = %#v", projection)
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

func TestPlanDocumentArgsParseAndValidateFinalHandoff(t *testing.T) {
	patch, err := planDocumentPatchFromArgs(map[string]any{
		"action":           "complete_subtask",
		"checkpoint_id":    "cp-1",
		"handoff_title":    "Ready",
		"handoff_overview": "The contract is complete.",
		"impact_bullets":   []any{"One contract."},
		"suggested_prompts": []any{map[string]any{
			"label": "Review", "prompt": "Review the contract for gaps.",
		}},
	})
	if err != nil {
		t.Fatalf("parse handoff args: %v", err)
	}
	if patch == nil || patch.Handoff == nil || patch.Handoff.Title != "Ready" || len(patch.Handoff.SuggestedPrompts) != 1 {
		t.Fatalf("patch = %#v", patch)
	}
	_, err = planDocumentPatchFromArgs(map[string]any{"action": "complete_checkpoint", "handoff_title": "missing overview"})
	if err == nil || !strings.Contains(err.Error(), "overview is required") {
		t.Fatalf("missing overview error = %v", err)
	}
}
