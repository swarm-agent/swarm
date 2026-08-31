package run

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestFocusedDesignerSwarmContextCarriesExactPartAuthorityAndCountOneAutoAccept(t *testing.T) {
	parent := pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}
	source := &pebblestore.SessionArtifactSelectionReference{SessionID: "source", CollectionID: "source-collection", VariantID: "source-variant", EventSeq: 9}
	composition := pebblestore.SessionArtifactComposition{ID: "composition-1", ArtifactChainID: "chain-1", OwnerSessionID: "source"}
	definition := pebblestore.SessionArtifactPartDefinition{ID: "hero", Label: "Hero", ArtifactChainID: "chain-1", OwnerSessionID: "source"}
	revision := pebblestore.SessionArtifactPartRevisionReference{ArtifactChainID: "chain-1", PartID: "hero", PartRevisionID: "hero-r1", OwnerSessionID: "source"}
	spec := taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, SwarmMode: true, SourceArtifact: source, SourceArguments: map[string]any{"swarm_index": 1, "swarm_count": 1, "section_target": &taskSwarmSectionTarget{ID: "hero", Label: "Hero", Kind: "semantic"}, "source_composition": composition, "source_part_definition": definition, "source_part_revision": revision}}
	run := managedDesignerArtifactContext(parent, "task-call", spec, 1)
	if run == nil || run.SourceComposition == nil || run.SourcePartDefinition == nil || run.SourcePartRevision == nil {
		t.Fatalf("focused part run context = %#v", run)
	}
	if !run.AutoAccept || run.PartID != "hero" || *run.SourcePartRevision != revision {
		t.Fatalf("focused single candidate context = %#v", run)
	}

	spec.SourceArguments["swarm_count"] = 3
	multi := managedDesignerArtifactContext(parent, "task-call-multi", spec, 1)
	if multi == nil || multi.AutoAccept {
		t.Fatalf("multi-candidate focused context = %#v", multi)
	}
	if multi.ArtifactStepID == "" || multi.CandidateIndex != 1 {
		t.Fatalf("multi-candidate turn identity = %#v", multi)
	}
}

func TestMultiLocatorDesignerRunContextCarriesBoundedTargetIDs(t *testing.T) {
	parent := pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}
	source := &pebblestore.SessionArtifactSelectionReference{SessionID: "source", CollectionID: "collection", VariantID: "variant", EventSeq: 9}
	spec := taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, SwarmMode: true, SourceArtifact: source, SourceArguments: map[string]any{
		"swarm_index": 1, "swarm_count": 2,
		"section_targets": []*taskSwarmSectionTarget{
			{ID: "part-1", Label: "Signal", Kind: "temporal", StartMs: 0, EndMs: 4000},
			{ID: "part-3", Label: "Resolve", Kind: "temporal", StartMs: 8000, EndMs: 12000},
		},
	}}
	run := managedDesignerArtifactContext(parent, "task-call", spec, 1)
	if run == nil || run.PartID != "" || len(run.SelectedReviewTargets) != 2 {
		t.Fatalf("multi-locator run context = %#v", run)
	}
	if run.SelectedReviewTargets[0].ID != "part-1" || run.SelectedReviewTargets[1].ID != "part-3" {
		t.Fatalf("review targets = %#v", run.SelectedReviewTargets)
	}
	if got := taskReviewTargetIDs(run.SelectedReviewTargets); got != "part-1,part-3" {
		t.Fatalf("review target ids = %q", got)
	}
}

func TestFocusedDesignerPromptRequiresPartReadAndPublishOnly(t *testing.T) {
	request := taskSwarmHydrationRequest{Prompt: "change hero", AgentType: "designer", SwarmStrategy: taskSwarmStrategyExplore, OutputMode: taskOutputModeManaged, SectionTarget: &taskSwarmSectionTarget{ID: "hero", Label: "Hero", Kind: "semantic"}, FocusedParts: true}
	prompt, err := composeTaskSwarmChildPrompt(request, taskSwarmHydrationItem{Index: 1, OutputMode: taskOutputModeManaged}, taskSwarmHydratedDelta{Index: 1, Title: "Hero Alternative", Theme: "quiet", Role: "edit hero", Deliverable: "replacement hero"})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"action=read_part", "action=publish_part", "Do not use create/create_package", "preserves every untouched exact part revision"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("focused prompt missing %q:\n%s", required, prompt)
		}
	}
}

func TestAuthenticateTaskReviewTargetsBindsMultipleLocatorsAndRejectsForgery(t *testing.T) {
	source := pebblestore.SessionArtifactVariant{Parts: []pebblestore.SessionArtifactPart{
		{ID: "part-1", Label: "Signal", Kind: "temporal", StartMs: 0, EndMs: 4000},
		{ID: "part-2", Label: "Flow", Kind: "temporal", StartMs: 4000, EndMs: 8000},
		{ID: "part-3", Label: "Resolve", Kind: "temporal", StartMs: 8000, EndMs: 12000},
	}}
	requested := []*taskSwarmSectionTarget{
		{ID: "part-1", Label: "Signal", Kind: "temporal", StartMs: 0, EndMs: 4000},
		{ID: "part-3", Label: "Resolve", Kind: "temporal", StartMs: 8000, EndMs: 12000},
	}
	bound, err := authenticateTaskReviewTargets(source, requested)
	if err != nil || len(bound) != 2 || bound[0].ID != "part-1" || bound[1].ID != "part-3" {
		t.Fatalf("bound review targets = %#v, %v", bound, err)
	}
	forged := cloneTaskSwarmSectionTargets(requested)
	forged[1].EndMs = 11999
	if _, err := authenticateTaskReviewTargets(source, forged); err == nil {
		t.Fatal("forged review target locator was accepted")
	}
	missing := cloneTaskSwarmSectionTargets(requested)
	missing[1].ID = "part-4"
	if _, err := authenticateTaskReviewTargets(source, missing); err == nil {
		t.Fatal("missing review target was accepted")
	}
}

func TestMultiLocatorDesignerPromptPublishesOneCompleteHTMLRevision(t *testing.T) {
	request := taskSwarmHydrationRequest{
		Prompt: "change Signal and Resolve", AgentType: "designer", SwarmStrategy: taskSwarmStrategyExplore, OutputMode: taskOutputModeManaged,
		SectionTargets: []*taskSwarmSectionTarget{
			{ID: "part-1", Label: "Signal", Kind: "temporal", StartMs: 0, EndMs: 4000},
			{ID: "part-3", Label: "Resolve", Kind: "temporal", StartMs: 8000, EndMs: 12000},
		},
	}
	prompt, err := composeTaskSwarmChildPrompt(request, taskSwarmHydrationItem{Index: 1, OutputMode: taskOutputModeManaged}, taskSwarmHydratedDelta{Index: 1, Title: "Signal Resolve", Theme: "quiet", Role: "edit selected regions", Deliverable: "complete HTML revision"})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"monolithic multi-target revision contract", "change all selected regions together", "exactly one complete revised artifact", "do not call read_parts/publish_parts", `"id":"part-1"`, `"id":"part-3"`} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("multi-locator prompt missing %q:\n%s", required, prompt)
		}
	}
	for _, forbidden := range []string{"action=read_parts", "action=publish_parts"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("multi-locator prompt contains multipart byte protocol %q:\n%s", forbidden, prompt)
		}
	}
}

func TestLocatorTargetDesignerPromptPublishesOneCompleteHTMLRevision(t *testing.T) {
	request := taskSwarmHydrationRequest{Prompt: "change hero", AgentType: "designer", SwarmStrategy: taskSwarmStrategyExplore, OutputMode: taskOutputModeManaged, SectionTarget: &taskSwarmSectionTarget{ID: "hero", Label: "Hero", Kind: "selector", Selector: "#hero"}}
	prompt, err := composeTaskSwarmChildPrompt(request, taskSwarmHydrationItem{Index: 1, OutputMode: taskOutputModeManaged}, taskSwarmHydratedDelta{Index: 1, Title: "Hero Alternative", Theme: "quiet", Role: "edit hero", Deliverable: "complete HTML revision"})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"monolithic revision contract", "exactly one manage_artifact create or create_package call", "Keep a single-file text/html source as text/html", "do not convert it to a ZIP", "server derive targets"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("monolithic prompt missing %q:\n%s", required, prompt)
		}
	}
	for _, forbidden := range []string{"action=read_part", "action=publish_part"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("monolithic prompt contains focused byte protocol %q:\n%s", forbidden, prompt)
		}
	}
}
