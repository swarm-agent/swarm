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

func TestFocusedDesignerPromptRequiresPartReadAndPublishOnly(t *testing.T) {
	request := taskSwarmHydrationRequest{Prompt: "change hero", AgentType: "designer", SwarmStrategy: taskSwarmStrategyExplore, OutputMode: taskOutputModeManaged, SectionTarget: &taskSwarmSectionTarget{ID: "hero", Label: "Hero", Kind: "semantic"}}
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
