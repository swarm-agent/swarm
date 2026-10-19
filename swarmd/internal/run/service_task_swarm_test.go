package run

import (
	"errors"
	"fmt"
	"reflect"

	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestParseTaskSwarmIdeaRepeatsExactQuestionWithoutRouterFields(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","description":"Ask the swarm","prompt":"Which name is clearest?","agent_type":"idea","count":3}`)
	if err != nil {
		t.Fatalf("parse Idea swarm: %v", err)
	}
	if parsed.Mode != taskModeSwarm || parsed.Swarm == nil || parsed.Swarm.AgentType != "idea" || len(parsed.Launches) != 3 {
		t.Fatalf("unexpected Idea swarm: %#v", parsed)
	}
	for i, launch := range parsed.Launches {
		if launch.RequestedSubagentType != "idea" || launch.MetaPrompt != parsed.Prompt || launch.AssignmentLabel != fmt.Sprintf("Agent #%d", i+1) || launch.StreamKey != fmt.Sprintf("swarm:%d", i+1) || !launch.SwarmMode {
			t.Fatalf("launch %d = %#v", i, launch)
		}
	}
}

func TestParseTaskSwarmRejectsFinderExplicitLaunchesAndTrustFields(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"swarm","prompt":"x","agent_type":"finder","count":2}`,
		`{"mode":"swarm","prompt":"x","agent_type":"coder","count":2,"launches":[{"subagent_type":"coder"}]}`,
		`{"mode":"swarm","prompt":"x","agent_type":"coder","count":2,"always_ask":false}`,
		`{"mode":"swarm","prompt":"x","agent_type":"coder","count":2,"groups":[{"name":"a","count":2,"trusted":true}]}`,
	} {
		if _, err := parseTaskCallArguments(raw); err == nil {
			t.Fatalf("expected rejection for %s", raw)
		}
	}
}

func TestParseTaskSwarmImageBuildsDirectRouterHydratedItems(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","description":"images","prompt":"create campaign art","agent_type":"image","count":2,"themes":["minimal","maximal"],"output_contract":"one ready image"}`)
	if err != nil {
		t.Fatalf("parse image swarm: %v", err)
	}
	if parsed.Swarm == nil || parsed.Swarm.AgentType != "image" || parsed.Swarm.OutputMode != taskOutputModeManaged || len(parsed.Launches) != 2 {
		t.Fatalf("image swarm = %#v", parsed)
	}
	for i, item := range parsed.Launches {
		if item.RequestedSubagentType != "image" || item.OutputMode != taskOutputModeManaged || len(item.OwnedScope) != 0 {
			t.Fatalf("image item %d = %#v", i, item)
		}
	}
	prompt := composeDirectImageSwarmPrompt("create campaign art", "minimal", nil, taskSwarmHydratedDelta{Index: 1, Title: "Minimal Image", Theme: "quiet geometry", Role: "Compose a minimal visual.", Deliverable: "Ready image"})
	for _, want := range []string{"create campaign art", "Parent-selected base theme", "minimal", "Router-hydrated image direction", "Compose a minimal visual."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("direct image prompt missing %q: %s", want, prompt)
		}
	}
	context := managedDesignerArtifactContext(pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}, "call", parsed.Launches[0], 1)
	if context == nil || context.CollectionID == "" || context.VariantID == "" {
		t.Fatalf("image managed destination = %#v", context)
	}
	for _, raw := range []string{
		`{"mode":"swarm","prompt":"x","agent_type":"image","count":1,"output_mode":"workspace"}`,
		`{"mode":"swarm","prompt":"x","agent_type":"image","count":1,"owned_scope_template":"images/{index}.png"}`,
		`{"mode":"swarm","swarm_strategy":"assembly","prompt":"x","agent_type":"image","count":1,"assembly_parts":[{"name":"image","owned_scope":["images/1.png"]}],"integration_contract":"image"}`,
	} {
		if _, err := parseTaskCallArguments(raw); err == nil {
			t.Fatalf("invalid image swarm was accepted: %s", raw)
		}
	}
}

// Requirement: Designer Iteration Swarms were replaced by swarm_design (count
// and variations), and Designer has no managed artifact output; every request
// for either is refused with an error that names swarm_design, while a
// Designer launch with a concrete owned_scope stays a valid workspace launch.
// Threat: a Designer swarm or managed Designer launch still reaches the removed
// artifact-authoring path, or the refusal is a generic error the caller cannot
// act on. Owners: parseTaskSwarmArguments, buildTaskSwarmHydrationRequest and
// applyTaskDesignerOutputMode via parseTaskCallArguments; argument parsing and
// hydration are the narrowest layers that see the request before any launch.
func TestParseTaskSwarmDesignerIterationSwarmIsReplacedBySwarmDesign(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"swarm","description":"objects","prompt":"make objects","agent_type":"designer","count":3}`,
		`{"mode":"swarm","description":"objects","prompt":"make objects","agent_type":"designer","count":3,"output_mode":"managed"}`,
		`{"mode":"swarm","description":"objects","prompt":"make objects","agent_type":"designer","count":3,"output_mode":"workspace"}`,
		`{"mode":"swarm","swarm_strategy":"explore","prompt":"make objects","agent_type":"system-designer","count":1}`,
	} {
		parsed, err := parseTaskCallArguments(raw)
		if !errors.Is(err, errDesignerIterationSwarmReplaced) || !strings.Contains(err.Error(), "swarm_design") {
			t.Fatalf("Designer Iteration Swarm %s: err = %v", raw, err)
		}
		if parsed.Swarm != nil || len(parsed.Launches) != 0 {
			t.Fatalf("a refused Designer swarm produced launches: %#v", parsed)
		}
	}
	// A swarm-only field the parser does not know is still refused first.
	if _, err := parseTaskCallArguments(`{"mode":"swarm","prompt":"x","agent_type":"designer","count":3,"owned_scope_template":"web/item-{index}.tsx"}`); err == nil || !strings.Contains(err.Error(), "unsupported field") {
		t.Fatalf("owned_scope_template error = %v", err)
	}
	// The hydration boundary refuses a Designer Iteration Swarm that reaches it.
	parsed := taskCallArguments{Swarm: &taskSwarmSpec{Strategy: taskSwarmStrategyExplore, AgentType: "designer", Count: 1}}
	launches := []taskLaunchSpec{{RequestedSubagentType: "designer", SwarmStrategy: taskSwarmStrategyExplore, OutputMode: taskOutputModeManaged}}
	if _, err := buildTaskSwarmHydrationRequest(parsed, launches); !errors.Is(err, errDesignerIterationSwarmReplaced) {
		t.Fatalf("hydration accepted a Designer Iteration Swarm: %v", err)
	}
	// Regular Designer launches: managed output is refused, a missing
	// owned_scope is refused, and an owned_scope makes a workspace launch.
	if _, err := parseTaskCallArguments(`{"prompt":"design","launches":[{"subagent_type":"designer","meta_prompt":"design a page","output_mode":"managed"}]}`); !errors.Is(err, errDesignerManagedOutputRemoved) {
		t.Fatalf("managed Designer launch error = %v", err)
	}
	if _, err := parseTaskCallArguments(`{"prompt":"design","launches":[{"subagent_type":"designer","meta_prompt":"design a page"}]}`); err == nil || !strings.Contains(err.Error(), "owned_scope") {
		t.Fatalf("Designer launch without owned_scope error = %v", err)
	}
	ok, err := parseTaskCallArguments(`{"prompt":"design","launches":[{"subagent_type":"designer","meta_prompt":"design a page","owned_scope":["designs/landing"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ok.Launches) != 1 || ok.Launches[0].OutputMode != taskOutputModeWorkspace || !reflect.DeepEqual(ok.Launches[0].OwnedScope, []string{"designs/landing"}) {
		t.Fatalf("Designer launch = %#v", ok.Launches)
	}
}

func TestValidateTaskSwarmHydrationFailsClosed(t *testing.T) {
	duplicate := taskSwarmHydratedDelta{Index: 1, Title: "One", Theme: "A", Role: "role", Deliverable: "output"}
	other := duplicate
	other.Index = 2
	other.Title = "Two"
	if err := validateTaskSwarmHydrationResult(taskSwarmHydrationResult{GroupTitle: "Dashboard Layout Studies", Deltas: []taskSwarmHydratedDelta{duplicate, other}}, 2); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("expected duplicate delta failure, got %v", err)
	}
}

func TestIdeaProfileIsCompiledToolFreeAndProtected(t *testing.T) {
	profile := agentruntime.IdeaAgentProfileForParent(pebblestore.AgentProfile{Provider: "codex", Model: "model", Thinking: "high"})
	if !agentruntime.IsIdeaAgentName(profile.Name) || !profile.Protected || profile.ToolContract == nil {
		t.Fatalf("unexpected Idea profile: %#v", profile)
	}
	for name, config := range profile.ToolContract.Tools {
		if config.Enabled != nil && *config.Enabled {
			t.Fatalf("Idea tool %s unexpectedly enabled", name)
		}
	}
}
