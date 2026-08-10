package run

import (
	"context"
	"fmt"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
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
		if launch.RequestedSubagentType != "idea" || launch.MetaPrompt != parsed.Prompt || launch.StreamKey != fmt.Sprintf("swarm:%d", i+1) || !launch.SwarmMode {
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

func TestParseTaskSwarmDesignerBuildsGroupsAndDistinctTargets(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","description":"objects","prompt":"make objects","agent_type":"designer","count":3,"groups":[{"name":"rocks","count":1},{"name":"plants","count":2}],"output_contract":"one object","owned_scope_template":"web/src/objects/item-{index}.tsx"}`)
	if err != nil {
		t.Fatalf("parse Designer swarm: %v", err)
	}
	if len(parsed.Swarm.Groups) != 2 || len(parsed.Launches) != 3 {
		t.Fatalf("unexpected parsed swarm: %#v", parsed.Swarm)
	}
	for i, launch := range parsed.Launches {
		want := fmt.Sprintf("web/src/objects/item-%d.tsx", i+1)
		if len(launch.OwnedScope) != 1 || launch.OwnedScope[0] != want {
			t.Fatalf("launch %d scope = %v, want %s", i, launch.OwnedScope, want)
		}
	}
}

func TestValidateTaskSwarmHydrationFailsClosed(t *testing.T) {
	if err := validateTaskSwarmHydrationResult(taskSwarmHydrationResult{Prompts: []taskSwarmHydratedPrompt{{Index: 1, Title: "One", Theme: "A", Prompt: "same"}, {Index: 2, Title: "Two", Theme: "B", Prompt: "same"}}}, 2); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("expected duplicate prompt failure, got %v", err)
	}
}

func TestDecodeTaskSwarmHydrationAcceptsSingleJSONFence(t *testing.T) {
	valid := `{"prompts":[{"index":1,"title":"One","theme":"A","prompt":"complete prompt"}]}`
	for _, raw := range []string{
		valid,
		"```json\n" + valid + "\n```",
		"```JSON\n" + valid + "\n```",
		"```\n" + valid + "\n```",
	} {
		result, err := decodeTaskSwarmHydrationResult(raw, 1)
		if err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
		if len(result.Prompts) != 1 || result.Prompts[0].Prompt != "complete prompt" {
			t.Fatalf("unexpected result for %q: %#v", raw, result)
		}
	}
}

func TestDecodeTaskSwarmHydrationRejectsNonFenceWrappersAndInvalidPayloads(t *testing.T) {
	valid := `{"prompts":[{"index":1,"title":"One","theme":"A","prompt":"complete prompt"}]}`
	cases := map[string]string{
		"leading commentary":  "Here is the result:\n" + valid,
		"trailing commentary": valid + "\nDone",
		"fenced commentary":   "```json\n" + valid + "\n```\nDone",
		"unknown field":       `{"prompts":[{"index":1,"title":"One","theme":"A","prompt":"complete prompt","extra":true}]}`,
		"incomplete prompt":   `{"prompts":[{"index":1,"title":"","theme":"A","prompt":"complete prompt"}]}`,
		"out of order":        `{"prompts":[{"index":2,"title":"One","theme":"A","prompt":"complete prompt"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeTaskSwarmHydrationResult(raw, 1); err == nil {
				t.Fatalf("expected %s to fail", name)
			}
		})
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

func TestHydrateTaskSwarmFailsWhenRouterUnavailable(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","description":"objects","prompt":"make objects","agent_type":"coder","count":2}`)
	if err != nil {
		t.Fatalf("parse Coder swarm: %v", err)
	}
	if _, err := (&Service{}).hydrateTaskSwarm(context.Background(), pebblestore.SessionSnapshot{ID: "parent"}, parsed, parsed.Launches, 1, "call", nil, identity.Principal{}); err == nil || !strings.Contains(err.Error(), "Router") {
		t.Fatalf("expected fail-closed Router error, got %v", err)
	}
}

func TestHydrateTaskSwarmIdeaBypassesRouter(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","description":"ask","prompt":"same exact question","agent_type":"idea","count":12}`)
	if err != nil {
		t.Fatalf("parse Idea swarm: %v", err)
	}
	got, err := (&Service{}).hydrateTaskSwarm(context.Background(), pebblestore.SessionSnapshot{ID: "parent"}, parsed, parsed.Launches, 1, "call", nil, identity.Principal{})
	if err != nil || len(got) != 12 {
		t.Fatalf("Idea swarm should bypass Router: len=%d err=%v", len(got), err)
	}
	for i, launch := range got {
		if launch.MetaPrompt != parsed.Prompt {
			t.Fatalf("Idea %d prompt = %q, want exact %q", i, launch.MetaPrompt, parsed.Prompt)
		}
	}
}
