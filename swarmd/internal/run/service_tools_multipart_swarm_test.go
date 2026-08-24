package run

import (
	"strings"
	"testing"
)

func TestParseTaskCallArgumentsDesignerSwarmAcceptsBoundedSectionTargets(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","prompt":"change both selected parts","agent_type":"designer","count":2,"source_artifact":{"session_id":"source-session","collection_id":"source-collection","variant_id":"source-variant","event_seq":9},"section_targets":[{"id":"hero","label":"Hero","kind":"semantic"},{"id":"footer","label":"Footer","kind":"semantic"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Swarm == nil || len(parsed.Swarm.SectionTargets) != 2 || parsed.Swarm.SectionTarget != nil {
		t.Fatalf("multipart swarm = %#v", parsed.Swarm)
	}
	for _, launch := range parsed.Launches {
		targets, parseErr := parseTaskSwarmSectionTargets(launch.SourceArguments["section_targets"])
		if parseErr != nil || len(targets) != 2 {
			t.Fatalf("launch section targets = %#v, %v", targets, parseErr)
		}
	}
}

func TestParseTaskCallArgumentsRejectsAmbiguousAndDuplicateSectionTargets(t *testing.T) {
	cases := []string{
		`{"mode":"swarm","prompt":"x","agent_type":"designer","count":1,"source_artifact":{"session_id":"s","collection_id":"c","variant_id":"v","event_seq":1},"section_target":{"id":"hero","label":"Hero","kind":"semantic"},"section_targets":[{"id":"footer","label":"Footer","kind":"semantic"}]}`,
		`{"mode":"swarm","prompt":"x","agent_type":"designer","count":1,"source_artifact":{"session_id":"s","collection_id":"c","variant_id":"v","event_seq":1},"section_targets":[{"id":"hero","label":"Hero","kind":"semantic"},{"id":"hero","label":"Hero","kind":"semantic"}]}`,
	}
	for _, input := range cases {
		if _, err := parseTaskCallArguments(input); err == nil {
			t.Fatalf("expected rejection for %s", input)
		}
	}
}

func TestMultipartDesignerPromptRequiresAtomicReadAndPublish(t *testing.T) {
	request := taskSwarmHydrationRequest{
		Prompt: "change hero and footer", AgentType: "designer", SwarmStrategy: taskSwarmStrategyExplore,
		OutputMode:     taskOutputModeManaged,
		SectionTargets: []*taskSwarmSectionTarget{{ID: "hero", Label: "Hero", Kind: "semantic"}, {ID: "footer", Label: "Footer", Kind: "semantic"}},
	}
	prompt, err := composeTaskSwarmChildPrompt(request, taskSwarmHydrationItem{Index: 1, OutputMode: taskOutputModeManaged}, taskSwarmHydratedDelta{Index: 1, Title: "Combined Alternative", Theme: "quiet", Role: "edit selected parts", Deliverable: "atomic replacement"})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"action=read_parts", "action=publish_parts", "one atomic candidate composition", "preserves every untouched exact part revision"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("multipart prompt missing %q:\n%s", required, prompt)
		}
	}
}
