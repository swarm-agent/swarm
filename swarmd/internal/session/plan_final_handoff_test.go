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
		CopyableCodeBlocks: []pebblestore.PlanFinalHandoffCopyableCodeBlock{{
			Label: " Run this command ", Language: "bash", Code: "printf 'ready\\n'\n",
		}},
		SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{
			Label: " Review ", Prompt: " Review the final handoff and call out any gaps. ",
		}},
		PullRequestURL: "  https://github.com/swarm/repository/pull/42  ",
	}
	normalized, err := NormalizePlanCheckpointHandoff(valid)
	if err != nil {
		t.Fatalf("normalize valid handoff: %v", err)
	}
	if normalized.Title != "Ready to review" || normalized.Overview != "The durable contract is ready." || normalized.ImpactBullets[0] != "Clients share one projection." || normalized.CopyableCodeBlocks[0].Label != "Run this command" || normalized.CopyableCodeBlocks[0].Code != "printf 'ready\\n'\n" || normalized.SuggestedPrompts[0].Label != "Review" || normalized.PullRequestURL != "https://github.com/swarm/repository/pull/42" {
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
		{name: "too many code blocks", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", CopyableCodeBlocks: []pebblestore.PlanFinalHandoffCopyableCodeBlock{{Code: "1"}, {Code: "2"}, {Code: "3"}, {Code: "4"}}}, want: "at most 3"},
		{name: "empty code", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", CopyableCodeBlocks: []pebblestore.PlanFinalHandoffCopyableCodeBlock{{Code: " "}}}, want: "copyable_code_blocks[0].code is required"},
		{name: "oversized code", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", CopyableCodeBlocks: []pebblestore.PlanFinalHandoffCopyableCodeBlock{{Code: strings.Repeat("x", PlanFinalHandoffMaxCodeBlockRunes+1)}}}, want: "copyable_code_blocks[0].code exceeds"},
		{name: "invalid code language", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", CopyableCodeBlocks: []pebblestore.PlanFinalHandoffCopyableCodeBlock{{Language: "bash script", Code: "echo ok"}}}, want: "unsupported character"},
		{name: "too many prompts", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "1", Prompt: "one"}, {Label: "2", Prompt: "two"}, {Label: "3", Prompt: "three"}, {Label: "4", Prompt: "four"}}}, want: "at most 3"},
		{name: "empty prompt label", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: " ", Prompt: "Review it."}}}, want: "suggested_prompts[0].label is required"},
		{name: "oversized prompt label", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: strings.Repeat("x", PlanFinalHandoffMaxPromptLabelRunes+1), Prompt: "Review it."}}}, want: "suggested_prompts[0].label exceeds"},
		{name: "empty prompt", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "Review", Prompt: " "}}}, want: "suggested_prompts[0].prompt is required"},
		{name: "oversized prompt", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "Review", Prompt: strings.Repeat("x", PlanFinalHandoffMaxSuggestedPromptRunes+1)}}}, want: "suggested_prompts[0].prompt exceeds"},
		{name: "command prompt", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "Run", Prompt: "/commit"}}}, want: "not an executable directive"},
		{name: "tool json", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{Label: "Run", Prompt: `{"tool":"bash"}`}}}, want: "not an executable directive"},
		{name: "non github PR", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", PullRequestURL: "https://example.com/owner/repo/pull/1"}, want: "pull_request_url"},
		{name: "github issue", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", PullRequestURL: "https://github.com/owner/repo/issues/1"}, want: "pull_request_url"},
		{name: "github PR query", handoff: pebblestore.SessionPlanCheckpointHandoff{Overview: "done", PullRequestURL: "https://github.com/owner/repo/pull/1?diff=split"}, want: "pull_request_url"},
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
		CopyableCodeBlocks: []pebblestore.PlanFinalHandoffCopyableCodeBlock{{
			Label: "Run focused check", Language: "bash", Code: "go test ./internal/session -run TestApplyPlanCheckpointOutcome",
		}},
		SuggestedPrompts: []pebblestore.PlanFinalHandoffSuggestedPrompt{{
			Label: "Review contract", Prompt: "Review the final-handoff contract for gaps.",
		}},
		PullRequestURL: "https://github.com/swarm/repository/pull/42",
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
	if projection.SchemaVersion != PlanFinalHandoffSchemaVersion || projection.Title != "Contract" || len(projection.CopyableCodeBlocks) != 1 || projection.CopyableCodeBlocks[0].Language != "bash" || projection.Recommendation == nil || projection.Recommendation.Decision != "ship" || projection.PullRequestURL != handoff.PullRequestURL {
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
	if restoredProjection == nil || restoredProjection.Overview != handoff.Overview || restoredProjection.Details.Report != "complete report" || restoredProjection.Recommendation == nil || restoredProjection.Recommendation.Decision != "ship" || restoredProjection.PullRequestURL != handoff.PullRequestURL {
		t.Fatalf("restored projection = %#v", restoredProjection)
	}
}

func TestProjectPlanFinalHandoffArtifactsFiltersAndHidesPaths(t *testing.T) {
	artifacts := []pebblestore.SessionPlanArtifactReference{
		{Path: "gallery/index.html", Role: "deliverable", Description: "Interactive gallery", MediaType: "text/html; charset=utf-8"},
		{Path: "gallery/overview.png", Role: "deliverable"},
		{Path: "notes/idea.txt", Role: "input", MediaType: "text/plain"},
		{Path: "bundle/source.zip", Role: "deliverable", MediaType: "application/zip"},
		{Path: "gallery/disguised.txt", Role: "deliverable", MediaType: "text/html"},
	}
	projected := ProjectPlanFinalHandoffArtifacts("plan-1", "cp-1", artifacts)
	if len(projected) != 2 {
		t.Fatalf("projected artifacts = %#v", projected)
	}
	if projected[0].ID == "" || projected[0].ID == "gallery/index.html" || projected[0].Label != "Interactive gallery" || projected[0].Filename != "index.html" || projected[0].MediaType != "text/html" || projected[0].Kind != "html" || !projected[0].Previewable {
		t.Fatalf("html descriptor = %#v", projected[0])
	}
	if projected[1].Filename != "overview.png" || projected[1].MediaType != "image/png" || projected[1].Kind != "image" {
		t.Fatalf("image descriptor = %#v", projected[1])
	}
	repeated := ProjectPlanFinalHandoffArtifacts("plan-1", "cp-1", artifacts)
	if repeated[0].ID != projected[0].ID {
		t.Fatalf("artifact id is not deterministic: %q != %q", repeated[0].ID, projected[0].ID)
	}
	otherCheckpoint := ProjectPlanFinalHandoffArtifacts("plan-1", "cp-2", artifacts)
	if otherCheckpoint[0].ID == projected[0].ID {
		t.Fatal("artifact id must bind checkpoint identity")
	}
}

func TestProjectPlanFinalHandoffArtifactsProjectsVideoSource(t *testing.T) {
	const sourceRef = "videosrc_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	projected := ProjectPlanFinalHandoffArtifacts("plan-1", "cp-1", []pebblestore.SessionPlanArtifactReference{{
		SourceRef: sourceRef, Label: "Final intro", Role: "deliverable", MediaType: "video/mp4",
	}})
	if len(projected) != 1 || projected[0].ID != sourceRef || projected[0].SourceRef != sourceRef || projected[0].Kind != "video" || projected[0].MediaType != "video/mp4" || !projected[0].Previewable {
		t.Fatalf("video source descriptor = %#v", projected)
	}
}

func TestBuildPlanFinalHandoffAllowsLegacyCheckpointWithoutSourceFields(t *testing.T) {
	projection, err := BuildPlanFinalHandoff(pebblestore.SessionPlanCheckpoint{Report: "legacy report"})
	if err != nil || projection != nil {
		t.Fatalf("legacy projection = %#v, err=%v", projection, err)
	}
}

func TestProjectPlanFinalHandoffArtifactsManagedAndWorkspaceCoexistence(t *testing.T) {
	artifacts := []pebblestore.SessionPlanArtifactReference{
		{
			SessionID:    "sess-1",
			CollectionID: "col-1",
			VariantID:    "var-html",
			EventSeq:     10,
			Label:        "Interactive Brainstorm Spec",
			Description:  "HTML concept prototype",
			MediaType:    "text/html; charset=utf-8",
			Role:         "deliverable",
		},
		{
			SessionID:    "sess-1",
			CollectionID: "col-1",
			VariantID:    "var-img",
			EventSeq:     11,
			Label:        "Design Image",
			MediaType:    "image/png",
		},
		{
			SessionID:    "sess-1",
			CollectionID: "col-1",
			VariantID:    "var-pkg",
			EventSeq:     12,
			Label:        "Interactive Package",
			MediaType:    "application/zip",
		},
		{
			SessionID:    "sess-1",
			CollectionID: "col-1",
			VariantID:    "var-input",
			EventSeq:     13,
			Label:        "Draft Notes",
			Role:         "input",
		},
		{
			Path:        "docs/summary.md",
			Role:        "deliverable",
			Description: "Workspace Markdown Summary",
			MediaType:   "text/markdown",
		},
		{
			SessionID:    "sess-1",
			CollectionID: "col-1",
			VariantID:    "var-html",
			EventSeq:     10,
			Label:        "Duplicate HTML",
		},
	}

	projected := ProjectPlanFinalHandoffArtifacts("plan-1", "cp-1", artifacts)
	if len(projected) != 4 {
		t.Fatalf("projected count = %d, want 4; projected = %#v", len(projected), projected)
	}

	// 1. Managed HTML
	if projected[0].ID != "var-html" || projected[0].Label != "Interactive Brainstorm Spec" || projected[0].Filename != "var-html" || projected[0].MediaType != "text/html" || projected[0].Kind != "html" || !projected[0].Previewable || projected[0].SessionID != "sess-1" || projected[0].CollectionID != "col-1" || projected[0].VariantID != "var-html" || projected[0].EventSeq != 10 {
		t.Fatalf("managed html descriptor mismatch: %#v", projected[0])
	}
	// 2. Managed PNG
	if projected[1].ID != "var-img" || projected[1].Label != "Design Image" || projected[1].Filename != "var-img" || projected[1].MediaType != "image/png" || projected[1].Kind != "image" || !projected[1].Previewable || projected[1].SessionID != "sess-1" || projected[1].CollectionID != "col-1" || projected[1].VariantID != "var-img" || projected[1].EventSeq != 11 {
		t.Fatalf("managed image descriptor mismatch: %#v", projected[1])
	}
	// 3. Managed ZIP package
	if projected[2].ID != "var-pkg" || projected[2].Label != "Interactive Package" || projected[2].MediaType != "application/zip" || projected[2].Kind != "package" || !projected[2].Previewable || projected[2].SessionID != "sess-1" || projected[2].CollectionID != "col-1" || projected[2].VariantID != "var-pkg" || projected[2].EventSeq != 12 {
		t.Fatalf("managed package descriptor mismatch: %#v", projected[2])
	}
	// 4. Workspace deliverable
	if projected[3].ID == "" || projected[3].Filename != "summary.md" || projected[3].Label != "Workspace Markdown Summary" || projected[3].MediaType != "text/markdown" || projected[3].Kind != "markdown" || !projected[3].Previewable || projected[3].SessionID != "" || projected[3].VariantID != "" {
		t.Fatalf("workspace deliverable descriptor mismatch: %#v", projected[3])
	}
}

func TestValidatePlanCheckpointRecommendationPromptSemantics(t *testing.T) {
	valid := pebblestore.SessionPlanCheckpointRecommendation{
		Decision:    "ship",
		Action:      "Review the interactive brainstorming spec and proceed with phase 2.",
		Reason:      "All acceptance criteria and artifact checks passed cleanly.",
		ActionState: "ready",
		Prompt:      "Please review the interactive brainstorming spec and let me know if we can proceed.",
	}
	normalized := normalizePlanCheckpointRecommendation(valid)
	if err := validatePlanCheckpointRecommendation(normalized); err != nil {
		t.Fatalf("validate valid recommendation: %v", err)
	}

	invalidCases := []struct {
		name    string
		rec     pebblestore.SessionPlanCheckpointRecommendation
		wantErr string
	}{
		{
			name:    "unsupported decision",
			rec:     pebblestore.SessionPlanCheckpointRecommendation{Decision: "ignore", Action: "review", Reason: "done", ActionState: "ready"},
			wantErr: "decision \"ignore\" is not supported",
		},
		{
			name:    "unsupported action_state",
			rec:     pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Action: "review", Reason: "done", ActionState: "pending"},
			wantErr: "action_state \"pending\" is not supported",
		},
		{
			name:    "missing action",
			rec:     pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Reason: "done", ActionState: "ready"},
			wantErr: "requires action, reason, and action_state",
		},
		{
			name:    "missing reason",
			rec:     pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Action: "review", ActionState: "ready"},
			wantErr: "requires action, reason, and action_state",
		},
		{
			name:    "executable directive slash in action",
			rec:     pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Action: "/commit and push", Reason: "done", ActionState: "ready"},
			wantErr: "must be display text or an ordinary chat prompt",
		},
		{
			name:    "executable directive tool json in action",
			rec:     pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Action: `{"tool":"bash","command":"git push"}`, Reason: "done", ActionState: "ready"},
			wantErr: "must be display text or an ordinary chat prompt",
		},
		{
			name:    "executable directive code block in reason",
			rec:     pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Action: "review change", Reason: "```bash\nrm -rf /\n```", ActionState: "ready"},
			wantErr: "must be display text or an ordinary chat prompt",
		},
		{
			name:    "executable directive slash in prompt",
			rec:     pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Action: "review", Reason: "done", ActionState: "ready", Prompt: "/commit and push"},
			wantErr: "must be display text or an ordinary chat prompt",
		},
		{
			name:    "executable directive tool json in prompt",
			rec:     pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Action: "review", Reason: "done", ActionState: "ready", Prompt: `{"tool":"bash","command":"git push"}`},
			wantErr: "must be display text or an ordinary chat prompt",
		},
		{
			name:    "oversized prompt",
			rec:     pebblestore.SessionPlanCheckpointRecommendation{Decision: "ship", Action: "review", Reason: "done", ActionState: "ready", Prompt: strings.Repeat("x", PlanFinalHandoffMaxSuggestedPromptRunes+1)},
			wantErr: "recommendation.prompt exceeds",
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			norm := normalizePlanCheckpointRecommendation(tc.rec)
			err := validatePlanCheckpointRecommendation(norm)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
