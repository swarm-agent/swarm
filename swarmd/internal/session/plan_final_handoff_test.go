package session

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestNormalizePlanCheckpointHandoffBoundsAndRejectsDirectives(t *testing.T) {
	valid := pebblestore.SessionPlanCheckpointHandoff{
		Title:         "  Ready to review  ",
		Overview:      "  The durable contract is ready.  ",
		ImpactBullets: []string{"  Clients share one projection.  "},
		SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{
			Label: " Review ", Prompt: " Review the final handoff and call out any gaps. ",
		}},
	}
	normalized, err := NormalizePlanCheckpointHandoff(valid)
	if err != nil {
		t.Fatalf("normalize valid handoff: %v", err)
	}
	if normalized.Title != "Ready to review" || normalized.Overview != "The durable contract is ready." || normalized.ImpactBullets[0] != "Clients share one projection." || normalized.SuggestedPrompts[0].Label != "Review" {
		t.Fatalf("normalized handoff = %#v", normalized)
	}

	cases := []struct {
		name    string
		handoff pebblestore.SessionPlanCheckpointHandoff
		want    string
	}{
		{name: "overview required", handoff: pebblestore.SessionPlanCheckpointHandoff{}, want: "overview is required"},
		{name: "oversized title", handoff: pebblestore.SessionPlanCheckpointHandoff{Title: strings.Repeat("x", PlanFinalHandoffMaxTitleRunes+1), Overview: "done"}, want: "title exceeds"},
		{name: "too many impacts", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", ImpactBullets: []string{"1", "2", "3", "4"}}, want: "at most 3"},
		{name: "empty impact", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", ImpactBullets: []string{" "}}, want: "impact_bullets[0] is required"},
		{name: "oversized impact", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", ImpactBullets: []string{strings.Repeat("x", PlanFinalHandoffMaxImpactBulletRunes+1)}}, want: "impact_bullets[0] exceeds"},
		{name: "oversized overview", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: strings.Repeat("x", PlanFinalHandoffMaxOverviewRunes+1)}, want: "overview exceeds"},
		{name: "invalid UTF-8", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: string([]byte{0xff})}, want: "valid UTF-8"},
		{name: "too many prompts", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "1", Prompt: "one"}, {Label: "2", Prompt: "two"}, {Label: "3", Prompt: "three"}, {Label: "4", Prompt: "four"}}}, want: "at most 3"},
		{name: "empty prompt label", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: " ", Prompt: "Review it."}}}, want: "suggested_prompts[0].label is required"},
		{name: "oversized prompt label", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: strings.Repeat("x", PlanFinalHandoffMaxPromptLabelRunes+1), Prompt: "Review it."}}}, want: "suggested_prompts[0].label exceeds"},
		{name: "empty prompt", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "Review", Prompt: " "}}}, want: "suggested_prompts[0].prompt is required"},
		{name: "oversized prompt", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "Review", Prompt: strings.Repeat("x", PlanFinalHandoffMaxSuggestedPromptRunes+1)}}}, want: "suggested_prompts[0].prompt exceeds"},
		{name: "command prompt", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "Run", Prompt: "/commit"}}}, want: "not an executable directive"},
		{name: "tool json", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "Run", Prompt: `{"tool":"bash"}`}}}, want: "not an executable directive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizePlanCheckpointHandoff(tc.handoff)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestApplyPlanCheckpointOutcomePersistsHandoffAndProjectsLosslessEvidence(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Contract", Status: PlanCheckpointStatusInProgress}},
		ActiveCheckpointID: "cp-1",
	}
	recommendation := &pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Action: "review", Reason: "focused checks passed", ActionState: "ready"}
	handoff := &pebblestore.SessionPlanCheckpointHandoff{
		Overview:      "Clients can render one compact contract.",
		ImpactBullets: []string{"Evidence stays collapsed by default."},
		SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{
			Label: "Review contract", Prompt: "Review the final-handoff contract for gaps.",
		}},
	}
	_, err := ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{
		CheckpointID: "cp-1", Outcome: PlanCheckpointStatusCompleted,
		Report: "complete report", Result: "done", ChangedFiles: []string{"contract.go"}, Validation: []string{"focused test"},
		Recommendation: recommendation, Handoff: handoff,
	})
	if err != nil {
		t.Fatalf("apply outcome: %v", err)
	}
	checkpoint := doc.Checkpoints[0]
	if checkpoint.Handoff == nil || checkpoint.Handoff.Overview != handoff.Overview {
		t.Fatalf("persisted handoff = %#v", checkpoint.Handoff)
	}
	projection, err := BuildPlanFinalHandoff(checkpoint)
	if err != nil {
		t.Fatalf("build projection: %v", err)
	}
	if projection.SchemaVersion != PlanFinalHandoffSchemaVersion || projection.Title != "Contract" || projection.Recommendation == nil || projection.Recommendation.Decision != "ship" {
		t.Fatalf("projection identity = %#v", projection)
	}
	if projection.Details.Report != "complete report" || projection.Details.Result != "done" || len(projection.Details.ChangedFiles) != 1 || len(projection.Details.Validation) != 1 {
		t.Fatalf("projection evidence = %#v", projection.Details)
	}
	if checkpoint.Handoff.Overview == checkpoint.Report || len(checkpoint.Handoff.ImpactBullets) != 1 {
		t.Fatalf("handoff duplicated evidence: checkpoint=%#v", checkpoint)
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal durable plan: %v", err)
	}
	var restored pebblestore.SessionPlanDocument
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("unmarshal durable plan: %v", err)
	}
	restoredProjection, err := BuildPlanFinalHandoff(restored.Checkpoints[0])
	if err != nil {
		t.Fatalf("build restored projection: %v", err)
	}
	if restoredProjection == nil || restoredProjection.Overview != handoff.Overview || restoredProjection.Details.Report != "complete report" || restoredProjection.Recommendation == nil || restoredProjection.Recommendation.Decision != "ship" {
		t.Fatalf("restored projection = %#v", restoredProjection)
	}
}

func TestBuildPlanFinalHandoffAllowsLegacyCheckpointWithoutSourceFields(t *testing.T) {
	projection, err := BuildPlanFinalHandoff(pebblestore.SessionPlanCheckpoint{Report: "legacy report"})
	if err != nil || projection != nil {
		t.Fatalf("legacy projection = %#v, err=%v", projection, err)
	}
}
