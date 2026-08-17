package run

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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

func TestParseTaskSwarmVideoIgnoresRegularConcurrencyReasonHint(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","description":"video alternatives","prompt":"Create reusable landscape video alternatives.","agent_type":"designer","count":2,"concurrency_reason":"Independent video variants","output_contract":"One reusable managed video per worker","output_requirements":{"preset":"landscape_video"},"animation_profile":{"profile":"motion_ui"}}`)
	if err != nil {
		t.Fatalf("parse video Iteration Swarm with compatibility hint: %v", err)
	}
	if parsed.Swarm == nil || parsed.Swarm.AgentType != "designer" || parsed.Swarm.Count != 2 || len(parsed.Launches) != 2 {
		t.Fatalf("video Iteration Swarm = %#v", parsed)
	}
	if _, exists := parsed.SourceArguments["concurrency_reason"]; exists {
		t.Fatalf("swarm source arguments preserved regular-only concurrency_reason: %#v", parsed.SourceArguments)
	}
	for i, launch := range parsed.Launches {
		if launch.ConcurrencyReason != "Independent Iteration Swarm alternative" {
			t.Fatalf("launch %d concurrency reason = %q", i, launch.ConcurrencyReason)
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

func TestDirectImageSwarmParsesAndApprovesExactSourceArtifact(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","prompt":"change the lighting","agent_type":"image","count":2,"source_artifact":{"session_id":"source-session","collection_id":"source-collection","variant_id":"source-variant","event_seq":9}}`)
	if err != nil {
		t.Fatalf("parse image remix swarm: %v", err)
	}
	want := &pebblestore.SessionArtifactSelectionReference{SessionID: "source-session", CollectionID: "source-collection", VariantID: "source-variant", EventSeq: 9}
	if parsed.Swarm == nil || !equalTaskImageSourceArtifact(parsed.Swarm.SourceArtifact, want) {
		t.Fatalf("source artifact = %#v", parsed.Swarm)
	}
	for i, launch := range parsed.Launches {
		got, err := parseTaskImageSourceArtifact(launch.SourceArguments["source_artifact"])
		if err != nil || !equalTaskImageSourceArtifact(got, want) {
			t.Fatalf("launch %d source artifact = %#v, %v", i, got, err)
		}
	}
	images := make([]taskImageManifestRow, len(parsed.Launches))
	for i, launch := range parsed.Launches {
		images[i] = taskImageManifestRow{Index: i + 1, StreamKey: launch.StreamKey, SourceArtifact: cloneTaskImageSourceArtifact(want)}
	}
	manifest := taskLaunchManifest{TaskMode: taskModeSwarm, SwarmAgentType: "image", SwarmStrategy: taskSwarmStrategyExplore, ImageCount: len(images), Images: images, ExecutionFormat: taskExecutionFormatImageDirect}
	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatalf("digest image remix manifest: %v", err)
	}
	manifest.ManifestHash = digest
	envelope, _ := json.Marshal(map[string]any{"manifest_hash": digest, "manifest": manifest})
	if err := validateApprovedDirectImageSwarm(string(envelope), parsed); err != nil {
		t.Fatalf("validate image remix manifest: %v", err)
	}
	manifest.Images[0].SourceArtifact.EventSeq++
	digest, _ = taskLaunchManifestDigest(manifest)
	manifest.ManifestHash = digest
	tampered, _ := json.Marshal(map[string]any{"manifest_hash": digest, "manifest": manifest})
	if err := validateApprovedDirectImageSwarm(string(tampered), parsed); err == nil {
		t.Fatal("approved image remix manifest accepted changed source event")
	}
	designer, err := parseTaskCallArguments(`{"mode":"swarm","prompt":"refine the selected design","agent_type":"designer","count":1,"source_artifact":{"session_id":"source-session","collection_id":"source-collection","variant_id":"source-variant","event_seq":9}}`)
	if err != nil {
		t.Fatalf("parse Designer source-artifact swarm: %v", err)
	}
	if designer.Swarm == nil || !equalTaskImageSourceArtifact(designer.Swarm.SourceArtifact, want) {
		t.Fatalf("Designer source artifact = %#v", designer.Swarm)
	}
	request, err := buildTaskSwarmHydrationRequest(designer, designer.Launches)
	if err != nil || !equalTaskImageSourceArtifact(request.SourceArtifact, want) {
		t.Fatalf("Designer hydration source artifact = %#v, %v", request.SourceArtifact, err)
	}
	prompt, err := composeTaskSwarmChildPrompt(request, request.Items[0], taskSwarmHydratedDelta{Index: 1, Title: "Refinement", Theme: "selected design", Role: "Refine the source.", Deliverable: "Derived variant"})
	if err != nil || !strings.Contains(prompt, `"session_id":"source-session"`) || !strings.Contains(prompt, "exact source artifact reference") {
		t.Fatalf("Designer source artifact child prompt = %q, %v", prompt, err)
	}
	for _, raw := range []string{
		`{"mode":"swarm","prompt":"x","agent_type":"image","count":1,"source_artifact":{"session_id":"s","collection_id":"c","variant_id":"v"}}`,
		`{"mode":"swarm","prompt":"x","agent_type":"coder","count":1,"source_artifact":{"session_id":"s","collection_id":"c","variant_id":"v","event_seq":1}}`,
	} {
		if _, err := parseTaskCallArguments(raw); err == nil {
			t.Fatalf("invalid source artifact accepted: %s", raw)
		}
	}
}

func TestDirectImageSwarmApprovedManifestUsesImagesNotLaunches(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","description":"images","prompt":"campaign brief","agent_type":"image","count":2,"themes":["minimal","editorial"]}`)
	if err != nil {
		t.Fatalf("parse direct image swarm: %v", err)
	}
	images := make([]taskImageManifestRow, len(parsed.Launches))
	for i, item := range parsed.Launches {
		images[i] = taskImageManifestRow{Index: i + 1, Theme: parsed.Swarm.Themes[i], StreamKey: item.StreamKey}
	}
	manifest := taskLaunchManifest{TaskMode: taskModeSwarm, SwarmAgentType: "image", SwarmStrategy: taskSwarmStrategyExplore, ImageCount: 2, Images: images, ExecutionFormat: taskExecutionFormatImageDirect}
	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatalf("digest direct image manifest: %v", err)
	}
	manifest.ManifestHash = digest
	envelope, err := json.Marshal(map[string]any{"manifest_hash": digest, "manifest": manifest})
	if err != nil {
		t.Fatalf("marshal direct image manifest: %v", err)
	}
	if err := validateApprovedDirectImageSwarm(string(envelope), parsed); err != nil {
		t.Fatalf("validate direct image manifest: %v", err)
	}
	if manifest.LaunchCount != 0 || len(manifest.Launches) != 0 || manifest.ImageCount != 2 || len(manifest.Images) != 2 {
		t.Fatalf("direct image manifest modeled agent launches: %#v", manifest)
	}
}

func TestDirectImageSwarmStreamPayloadDoesNotModelSubagents(t *testing.T) {
	payload := buildDirectImageSwarmStreamPayload("call", "spawn", "images", 2, 1, "hydrating", "", "minimal", "router", "hydrating", nil)
	if payload["execution_format"] != taskExecutionFormatImageDirect || payload["image_count"] != 2 || payload["path_id"] != "tool.task.image_swarm.stream.v1" {
		t.Fatalf("direct image stream = %#v", payload)
	}
	if _, exists := payload["launch"]; exists {
		t.Fatalf("direct image stream modeled a launch: %#v", payload)
	}
	if _, exists := payload["launch_count"]; exists {
		t.Fatalf("direct image stream modeled launch count: %#v", payload)
	}
	image, ok := payload["image"].(map[string]any)
	if !ok || image["child_session_created"] != false || image["current_stage"] != "router" || image["status"] != "running" || image["current_stage_label"] != "Routing" {
		t.Fatalf("direct image stream item = %#v", image)
	}
	generating := buildDirectImageSwarmStreamPayload("call", "spawn", "images", 2, 1, "generating", "Minimal", "minimal", "image_model", "Generating image", nil)
	generatingImage, _ := generating["image"].(map[string]any)
	stages, _ := generatingImage["stage_history"].([]string)
	if generatingImage["status"] != "running" || generatingImage["current_stage_label"] != "Image creation" || !slices.Equal(stages, []string{"Routing", "Image creation"}) {
		t.Fatalf("direct image generation progress = %#v", generatingImage)
	}
}

func TestParseTaskSwarmDesignerRejectsWorkspaceOutput(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"swarm","description":"objects","prompt":"make objects","agent_type":"designer","count":3,"output_mode":"workspace"}`,
		`{"mode":"swarm","description":"objects","prompt":"make objects","agent_type":"designer","count":3,"owned_scope_template":"web/src/objects/item-{index}.tsx"}`,
	} {
		_, err := parseTaskCallArguments(raw)
		if err == nil {
			t.Fatalf("workspace Designer Iteration Swarm was accepted: %s", raw)
		}
		if !strings.Contains(err.Error(), "regular task launches") && !strings.Contains(err.Error(), "unsupported field") {
			t.Fatalf("workspace Designer Iteration Swarm error = %v", err)
		}
	}
}

func TestTaskSwarmFocusedIterationPreservesParentControl(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","prompt":"Keep the existing dashboard structure and refine its spacing.","agent_type":"designer","count":2,"themes":["compact","spacious"],"iteration_controls":{"preserve":["information architecture","brand palette"],"change":["spacing rhythm"],"exclude":["new navigation","new product features"]},"output_contract":"one dashboard refinement"}`)
	if err != nil {
		t.Fatalf("parse focused Designer swarm: %v", err)
	}
	if parsed.Swarm == nil || parsed.Swarm.IterationControls == nil || !slices.Equal(parsed.Swarm.IterationControls.Change, []string{"spacing rhythm"}) {
		t.Fatalf("focused controls = %#v", parsed.Swarm)
	}
	request, err := buildTaskSwarmHydrationRequest(parsed, parsed.Launches)
	if err != nil {
		t.Fatalf("build focused hydration: %v", err)
	}
	if request.Description != "delegated task" || request.IterationControls == nil || request.Items[0].Theme != "compact" {
		t.Fatalf("focused hydration request = %#v", request)
	}
	systemPrompt := taskSwarmRouterSystemPrompt(request)
	for _, want := range []string{"authoritative and read-only", "Never add, remove, weaken", "never introduce anything named in exclude", "Do not force novelty outside", "supplied description", "exactly 3 or 4 words"} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("focused Router prompt missing %q: %s", want, systemPrompt)
		}
	}
	childPrompt, err := composeTaskSwarmChildPrompt(request, request.Items[0], taskSwarmHydratedDelta{Index: 1, Title: "Router title", Theme: "unauthorized rename", Role: "Adjust spacing.", Constraints: []string{"Keep it usable."}, Deliverable: "Refined dashboard"})
	if err != nil {
		t.Fatalf("compose focused prompt: %v", err)
	}
	for _, want := range []string{"Shared project brief (authoritative)", "parent-selected theme (authoritative; use exactly, do not rename or embellish): compact", "parent-controlled preserve", "information architecture", "parent-controlled change only", "spacing rhythm", "parent-controlled exclude", "new navigation", "untrusted, additive execution detail only", "- theme: compact"} {
		if !strings.Contains(childPrompt, want) {
			t.Fatalf("focused child prompt missing %q: %s", want, childPrompt)
		}
	}
	if strings.Contains(childPrompt, "- theme: unauthorized rename") {
		t.Fatalf("Router changed parent-selected theme: %s", childPrompt)
	}
}

func TestTaskSwarmIterationControlsRejectInvalidOrUnsupportedCalls(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"swarm","prompt":"x","agent_type":"designer","count":1,"iteration_controls":{"preserve":["layout"]}}`,
		`{"mode":"swarm","prompt":"x","agent_type":"coder","count":1,"iteration_controls":{"change":["spacing"]}}`,
		`{"mode":"swarm","prompt":"x","agent_type":"designer","count":1,"iteration_controls":{"change":["spacing"],"unknown":["x"]}}`,
	} {
		if _, err := parseTaskCallArguments(raw); err == nil {
			t.Fatalf("invalid iteration controls accepted: %s", raw)
		}
	}
}

func TestParseTaskSwarmDesignerDefaultsToManagedWithoutWorkspaceTarget(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","description":"objects","prompt":"make objects","agent_type":"designer","count":2,"output_contract":"one managed object"}`)
	if err != nil {
		t.Fatalf("parse managed Designer swarm: %v", err)
	}
	if parsed.Swarm.OutputMode != taskOutputModeManaged {
		t.Fatalf("output mode = %q, want managed", parsed.Swarm.OutputMode)
	}
	for i, launch := range parsed.Launches {
		if launch.OutputMode != taskOutputModeManaged || len(launch.OwnedScope) != 0 {
			t.Fatalf("managed launch %d = %#v", i, launch)
		}
	}
	request, err := buildTaskSwarmHydrationRequest(parsed, parsed.Launches)
	if err != nil || request.OutputMode != taskOutputModeManaged || len(request.Items) != 2 || request.Items[0].OutputMode != taskOutputModeManaged {
		t.Fatalf("managed hydration request = %#v err=%v", request, err)
	}
	prompt, err := composeTaskSwarmChildPrompt(request, request.Items[0], taskSwarmHydratedDelta{Index: 1, Title: "Managed", Theme: "compact", Role: "Create one variant.", Deliverable: "Managed artifact"})
	if err != nil || !strings.Contains(prompt, "output mode: managed") || strings.Contains(prompt, "owned scope:") {
		t.Fatalf("managed child prompt = %q err=%v", prompt, err)
	}
}

func TestParseTaskSwarmDefaultsToExploreAndPreservesCompatibilityAlias(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"swarm","prompt":"build","agent_type":"coder","count":2}`,
		`{"swarm_mode":true,"prompt":"build","agent_type":"coder","count":2}`,
	} {
		parsed, err := parseTaskCallArguments(raw)
		if err != nil {
			t.Fatalf("parse compatibility swarm: %v", err)
		}
		if parsed.Swarm == nil || parsed.Swarm.Strategy != taskSwarmStrategyExplore {
			t.Fatalf("strategy = %#v, want Explore", parsed.Swarm)
		}
		for _, launch := range parsed.Launches {
			if launch.SwarmStrategy != taskSwarmStrategyExplore || launch.SourceArguments["swarm_strategy"] != taskSwarmStrategyExplore {
				t.Fatalf("Explore metadata missing: %#v", launch)
			}
		}
	}
}

func TestTaskAssemblySwarmIsDisabledAtLaunchGate(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","swarm_strategy":"assembly","prompt":"build feature","agent_type":"designer","count":2,"assembly_parts":[{"name":"Navigation","instructions":"Build nav","owned_scope":["web/src/nav.tsx"]},{"name":"Content","owned_scope":["web/src/content.tsx","web/src/content.css"]}],"integration_contract":"Parent assembles a complete page"}`)
	if err != nil {
		t.Fatalf("parse dormant Assembly implementation: %v", err)
	}
	if err := validateTaskSwarmLaunchEnabled(parsed); err == nil || !strings.Contains(err.Error(), "Assembly Swarm is not available in this launch") {
		t.Fatalf("Assembly launch gate error = %v", err)
	}
}

func TestParseTaskAssemblySwarmRejectsInvalidContracts(t *testing.T) {
	cases := []string{
		`{"mode":"swarm","swarm_strategy":"assembly","prompt":"x","agent_type":"idea","count":1,"assembly_parts":[{"name":"a","owned_scope":["a"]}],"integration_contract":"final"}`,
		`{"mode":"swarm","swarm_strategy":"assembly","prompt":"x","agent_type":"coder","count":2,"assembly_parts":[{"name":"a","owned_scope":["a"]}],"integration_contract":"final"}`,
		`{"mode":"swarm","swarm_strategy":"assembly","prompt":"x","agent_type":"coder","count":2,"assembly_parts":[{"name":"a","owned_scope":["src"]},{"name":"b","owned_scope":["src/b"]}],"integration_contract":"final"}`,
		`{"mode":"swarm","swarm_strategy":"assembly","prompt":"x","agent_type":"coder","count":1,"assembly_parts":[{"name":"a","owned_scope":["../escape"]}],"integration_contract":"final"}`,
		`{"mode":"swarm","swarm_strategy":"assembly","prompt":"x","agent_type":"coder","count":1,"assembly_parts":[{"name":"a","owned_scope":["src/a"]}]}`,
		`{"mode":"swarm","prompt":"x","agent_type":"coder","count":1,"assembly_parts":[{"name":"a","owned_scope":["src/a"]}],"integration_contract":"final"}`,
	}
	for _, raw := range cases {
		if _, err := parseTaskCallArguments(raw); err == nil {
			t.Fatalf("expected Assembly rejection for %s", raw)
		}
	}
}

func TestApprovedTaskManifestPreservesAssemblyContract(t *testing.T) {
	part := taskSwarmAssemblyPart{Name: "Backend", OwnedScope: []string{"swarmd/internal/run"}}
	specs := []taskLaunchSpec{{RequestedSubagentType: "coder", StreamKey: "swarm:1", SwarmMode: true, SwarmStrategy: taskSwarmStrategyAssembly, AssemblyPart: &part, IntegrationContract: "Parent integrates the feature"}}
	manifest := taskLaunchManifest{TaskMode: taskModeSwarm, SwarmStrategy: taskSwarmStrategyAssembly, AssemblyParts: []taskSwarmAssemblyPart{part}, IntegrationContract: "Parent integrates the feature", Launches: []taskLaunchManifestRow{{RequestedSubagentType: "coder", StreamKey: "swarm:1", SwarmMode: true, SwarmStrategy: taskSwarmStrategyAssembly, AssemblyPart: &part, IntegrationContract: "Parent integrates the feature", ProfileSnapshot: &pebblestore.AgentProfile{Name: agentruntime.CoderAgentID}}}}
	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatalf("digest Assembly manifest: %v", err)
	}
	manifest.ManifestHash = digest
	envelope, err := json.Marshal(map[string]any{"manifest_hash": digest, "manifest": manifest})
	if err != nil {
		t.Fatalf("marshal approved manifest: %v", err)
	}
	approved, err := parseApprovedTaskLaunchManifest(string(envelope), specs)
	if err != nil {
		t.Fatalf("parse approved Assembly manifest: %v", err)
	}
	if approved.SwarmStrategy != taskSwarmStrategyAssembly || len(approved.AssemblyParts) != 1 || approved.IntegrationContract != "Parent integrates the feature" {
		t.Fatalf("approved Assembly metadata = %#v", approved)
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

func TestDecodeTaskSwarmHydrationAcceptsSingleJSONFence(t *testing.T) {
	valid := `{"group_title":"Campaign Image Studies","deltas":[{"index":1,"title":"One","theme":"A","role":"specialist","constraints":["bounded"],"deliverable":"focused output"}]}`
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
		if len(result.Deltas) != 1 || result.Deltas[0].Role != "specialist" {
			t.Fatalf("unexpected result for %q: %#v", raw, result)
		}
	}
}

func TestDecodeTaskSwarmHydrationRejectsNonFenceWrappersAndInvalidPayloads(t *testing.T) {
	valid := `{"group_title":"Campaign Image Studies","deltas":[{"index":1,"title":"One","theme":"A","role":"specialist","constraints":["bounded"],"deliverable":"focused output"}]}`
	cases := map[string]string{
		"leading commentary":  "Here is the result:\n" + valid,
		"trailing commentary": valid + "\nDone",
		"fenced commentary":   "```json\n" + valid + "\n```\nDone",
		"unknown field":       `{"group_title":"Campaign Image Studies","deltas":[{"index":1,"title":"One","theme":"A","role":"specialist","deliverable":"output","extra":true}]}`,
		"incomplete delta":    `{"group_title":"Campaign Image Studies","deltas":[{"index":1,"title":"","theme":"A","role":"specialist","deliverable":"output"}]}`,
		"out of order":        `{"group_title":"Campaign Image Studies","deltas":[{"index":2,"title":"One","theme":"A","role":"specialist","deliverable":"output"}]}`,
		"missing group title": `{"deltas":[{"index":1,"title":"One","theme":"A","role":"specialist","deliverable":"output"}]}`,
		"long group title":    `{"group_title":"A Campaign Image Iteration Study","deltas":[{"index":1,"title":"One","theme":"A","role":"specialist","deliverable":"output"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeTaskSwarmHydrationResult(raw, 1); err == nil {
				t.Fatalf("expected %s to fail", name)
			}
		})
	}
}

func TestBuildTaskSwarmHydrationRequestIncludesAssemblyIdentityAndExecution(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","swarm_strategy":"assembly","prompt":"build feature","agent_type":"designer","count":1,"assembly_parts":[{"name":"Navigation","instructions":"Build nav","owned_scope":["web/src/nav.tsx"]}],"integration_contract":"Parent assembles the page"}`)
	if err != nil {
		t.Fatalf("parse Assembly swarm: %v", err)
	}
	request, err := buildTaskSwarmHydrationRequest(parsed, parsed.Launches)
	if err != nil {
		t.Fatalf("build hydration request: %v", err)
	}
	if request.SwarmStrategy != taskSwarmStrategyAssembly || request.IntegrationContract != "Parent assembles the page" || len(request.Items) != 1 {
		t.Fatalf("request = %#v", request)
	}
	item := request.Items[0]
	if item.PartName != "Navigation" || item.PartInstructions != "Build nav" || len(item.OwnedScope) != 1 || item.WorkerExecution != "designer_output_mode_contract" {
		t.Fatalf("item = %#v", item)
	}
}

func TestBuildTaskSwarmHydrationRequestRejectsIncompleteWave(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","prompt":"build","agent_type":"coder","count":2}`)
	if err != nil {
		t.Fatalf("parse Coder swarm: %v", err)
	}
	if _, err := buildTaskSwarmHydrationRequest(parsed, parsed.Launches[:1]); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected incomplete wave rejection, got %v", err)
	}
	mutated := append([]taskLaunchSpec(nil), parsed.Launches...)
	mutated[1].SwarmStrategy = taskSwarmStrategyAssembly
	if _, err := buildTaskSwarmHydrationRequest(parsed, mutated); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("expected identity rejection, got %v", err)
	}
}

func TestComposeTaskSwarmChildPromptUsesAuthoritativeEnvelopeAndCompactDelta(t *testing.T) {
	request := taskSwarmHydrationRequest{
		Prompt: "Build the complete feature.", AgentType: "coder", SwarmStrategy: taskSwarmStrategyAssembly,
		IntegrationContract: "Parent integrates all committed parts.",
		Items:               []taskSwarmHydrationItem{{Index: 1, PartName: "Backend", PartInstructions: "Implement API", OwnedScope: []string{"swarmd/internal/api"}}},
	}
	delta := taskSwarmHydratedDelta{Index: 1, Title: "Backend API", Theme: "persistence", Role: "Implement the backend endpoint.", Constraints: []string{"Preserve V3 durability."}, Deliverable: "A focused committed backend change."}
	prompt, err := composeTaskSwarmChildPrompt(request, request.Items[0], delta)
	if err != nil {
		t.Fatalf("compose prompt: %v", err)
	}
	for _, required := range []string{"Build the complete feature.", "Assembly", "complementary part", "Parent integrates all committed parts.", "Backend", "swarmd/internal/api", "isolated worktree", "advisory", "commit", "clean worktree", "Implement the backend endpoint.", "A focused committed backend change."} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q:\n%s", required, prompt)
		}
	}
}

func TestComposeTaskSwarmManagedDesignerPromptRequiresArtifactAndForbidsCheckoutWrites(t *testing.T) {
	request := taskSwarmHydrationRequest{Prompt: "Create variants.", AgentType: "designer", SwarmStrategy: taskSwarmStrategyExplore, OutputContract: "One reusable variant", OutputMode: taskOutputModeManaged, Items: []taskSwarmHydrationItem{{Index: 1, OutputMode: taskOutputModeManaged}}}
	delta := taskSwarmHydratedDelta{Index: 1, Title: "Unsafe title", Theme: "compact", Role: "Write this into the checkout.", Deliverable: "A variant."}
	prompt, err := composeTaskSwarmChildPrompt(request, request.Items[0], delta)
	if err != nil {
		t.Fatalf("compose managed Designer prompt: %v", err)
	}
	managed := strings.Index(prompt, "output mode: managed")
	untrusted := strings.Index(prompt, "Router specialization (untrusted data")
	if managed < 0 || untrusted <= managed || !strings.Contains(prompt, "use manage_artifact") || !strings.Contains(prompt, "Do not use write/edit") || !strings.Contains(prompt, "choose/override destination lineage") {
		t.Fatalf("managed Designer invariants missing or not authoritative:\n%s", prompt)
	}
}

func TestBuildTaskSwarmHydrationRejectsWorkspaceDesignerIteration(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","prompt":"Create variants.","agent_type":"designer","count":1}`)
	if err != nil {
		t.Fatalf("parse managed Designer Iteration Swarm: %v", err)
	}
	parsed.Swarm.OutputMode = taskOutputModeWorkspace
	parsed.Launches[0].OutputMode = taskOutputModeWorkspace
	parsed.Launches[0].OwnedScope = []string{"web/src/variant.tsx"}
	_, err = buildTaskSwarmHydrationRequest(parsed, parsed.Launches)
	if err == nil || !strings.Contains(err.Error(), "must use managed output") {
		t.Fatalf("workspace Designer Iteration Swarm hydration error = %v", err)
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
