package run

import (
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestMasterHarnessExplainsArtifactOutputRequirementInference(t *testing.T) {
	prompt := masterHarnessPromptWithScope(tool.WorkspaceScope{})
	for _, expected := range []string{"twitter_header", "landscape_video", "portrait_video", "Semantic parent inference supplies this structured preset", "omit output_requirements", "publish once"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("master harness missing %q", expected)
		}
	}
}

func TestTaskOutputRequirementsAllDesignerModes(t *testing.T) {
	regular, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"prompt": "design", "subagent_type": "designer", "meta_prompt": "create it", "output_requirements": map[string]any{"preset": "twitter_header"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertXHeaderRequirements(t, regular.Launches[0].OutputRequirements)
	assertXHeaderRequirements(t, taskOutputRequirementsFromAny(t, regular.Launches[0].SourceArguments["output_requirements"]))
	assertXHeaderRequirements(t, taskOutputRequirementsFromAny(t, regular.SourceArguments["output_requirements"]))
	clonedSource := cloneGenericMap(regular.Launches[0].SourceArguments)
	taskOutputRequirementsFromAny(t, regular.Launches[0].SourceArguments["output_requirements"]).Width = 1
	assertXHeaderRequirements(t, regular.Launches[0].OutputRequirements)
	assertXHeaderRequirements(t, taskOutputRequirementsFromAny(t, regular.SourceArguments["output_requirements"]))
	assertXHeaderRequirements(t, taskOutputRequirementsFromAny(t, clonedSource["output_requirements"]))
	launches, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"prompt": "design", "launches": []any{map[string]any{"subagent_type": "designer", "meta_prompt": "create it", "output_requirements": map[string]any{"preset": "twitter_header"}}, map[string]any{"subagent_type": "designer", "meta_prompt": "create another", "output_requirements": map[string]any{"preset": "square_1080"}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertXHeaderRequirements(t, launches.Launches[0].OutputRequirements)
	if launches.Launches[1].OutputRequirements == nil || launches.Launches[1].OutputRequirements.PresetID != "square_1080" {
		t.Fatalf("launch requirements = %#v", launches.Launches[1].OutputRequirements)
	}

	workspace, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"prompt": "design", "subagent_type": "designer", "meta_prompt": "create it", "output_mode": "workspace", "owned_scope": []any{"design/header.svg"}, "output_requirements": map[string]any{"preset": "twitter_header"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertXHeaderRequirements(t, workspace.Launches[0].OutputRequirements)
	workspacePrompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{Description: "header", Prompt: "create", RequestedSubagent: "designer", OwnedScope: workspace.Launches[0].OwnedScope, OutputMode: taskOutputModeWorkspace, OutputRequirements: workspace.Launches[0].OutputRequirements})
	if !strings.Contains(workspacePrompt, `"preset_id":"x_header"`) || !strings.Contains(workspacePrompt, "output mode: workspace") {
		t.Fatalf("workspace prompt = %s", workspacePrompt)
	}

	swarm, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "swarm", "prompt": "design", "agent_type": "designer", "count": 2, "output_requirements": map[string]any{"preset": "twitter_header"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertXHeaderRequirements(t, swarm.Swarm.OutputRequirements)
	assertXHeaderRequirements(t, taskOutputRequirementsFromAny(t, swarm.SourceArguments["output_requirements"]))
	for _, launch := range swarm.Launches {
		assertXHeaderRequirements(t, launch.OutputRequirements)
		assertXHeaderRequirements(t, taskOutputRequirementsFromAny(t, launch.SourceArguments["output_requirements"]))
	}
	swarmWorkspace, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "swarm", "prompt": "design", "agent_type": "designer", "count": 2, "output_mode": "workspace", "owned_scope_template": "design/variant-{index}.svg", "output_requirements": map[string]any{"preset": "twitter_header"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, launch := range swarmWorkspace.Launches {
		assertXHeaderRequirements(t, launch.OutputRequirements)
	}

	program, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"prompt": "design", "program": map[string]any{
			"id": "design_program", "stages": []any{map[string]any{"id": "variants", "dependency_evidence": "ready"}},
			"jobs": []any{map[string]any{"id": "header", "stage_id": "variants", "agent_type": "designer", "meta_prompt": "create", "title": "Header", "deliverable": "header", "acceptance_criteria": []any{"ready"}, "dependency_evidence": "ready", "output_requirements": map[string]any{"preset": "twitter_header"}}},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertXHeaderRequirements(t, program.Program.Jobs[0].OutputRequirements)
	definition, _, err := taskProgramDefinitionFromSpec(program.Program)
	if err != nil {
		t.Fatal(err)
	}
	assertXHeaderRequirements(t, definition.Jobs[0].OutputRequirements)
	assertXHeaderRequirements(t, program.Launches[0].OutputRequirements)
	assertXHeaderRequirements(t, taskOutputRequirementsFromAny(t, program.Launches[0].SourceArguments["output_requirements"]))
}

func TestTaskOutputRequirementsPromptHydrationAndManagedContext(t *testing.T) {
	parsed, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "swarm", "prompt": "design", "agent_type": "designer", "count": 1, "output_requirements": map[string]any{"preset": "twitter_header"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	request, err := buildTaskSwarmHydrationRequest(parsed, parsed.Launches)
	if err != nil {
		t.Fatal(err)
	}
	assertXHeaderRequirements(t, request.OutputRequirements)
	prompt, err := composeTaskSwarmChildPrompt(request, request.Items[0], taskSwarmHydratedDelta{Index: 1, Title: "Header", Theme: "clean", Role: "designer", Deliverable: "header"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"preset_id":"x_header"`) || !strings.Contains(prompt, "may not rewrite") || !strings.Contains(prompt, "omit output_requirements") {
		t.Fatalf("prompt = %s", prompt)
	}
	context := managedDesignerArtifactContext(pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}, "call", parsed.Launches[0], 1)
	if context == nil {
		t.Fatal("managed artifact context missing")
	}
	assertXHeaderRequirements(t, context.OutputRequirements)
	prepared := taskLaunchPrepared{OutputRequirements: cloneTaskOutputRequirements(context.OutputRequirements), ArtifactRunContext: context}
	meta := delegatedSubagentRunStartMeta(prepared, "parent", identity.Principal{}, nil)
	assertXHeaderRequirements(t, meta.ArtifactRunContext.OutputRequirements)
	parsed.Launches[0].OutputRequirements.Width = 1
	if context.OutputRequirements.Width != 1500 || request.OutputRequirements.Width != 1500 || meta.ArtifactRunContext.OutputRequirements.Width != 1500 {
		t.Fatalf("requirements snapshots were aliased: context=%#v request=%#v runtime=%#v", context.OutputRequirements, request.OutputRequirements, meta.ArtifactRunContext.OutputRequirements)
	}
	delegated := buildTaskDelegationPrompt(taskDelegationPromptConfig{Description: "header", Prompt: "create", RequestedSubagent: "designer", OutputMode: taskOutputModeManaged, OutputRequirements: context.OutputRequirements, ArtifactRunContext: context})
	if !strings.Contains(delegated, `"preset_id":"x_header"`) || !strings.Contains(delegated, "omit collection_id/variant_id/output_requirements") {
		t.Fatalf("delegated prompt = %s", delegated)
	}
}

func taskOutputRequirementsFromAny(t *testing.T, value any) *pebblestore.SessionArtifactOutputRequirements {
	t.Helper()
	requirements, ok := value.(*pebblestore.SessionArtifactOutputRequirements)
	if !ok {
		t.Fatalf("requirements value = %#v", value)
	}
	return requirements
}

func TestTaskOutputRequirementsPreparedContextClonesSnapshot(t *testing.T) {
	requirements := &pebblestore.SessionArtifactOutputRequirements{PresetID: "x_header", Width: 1500, Height: 500, AspectRatio: "3:1", Orientation: "landscape", ResolutionSource: "preset", RegistryVersion: "2026-08-14.v1"}
	prepared := taskLaunchPrepared{RequestedSubagent: "designer", OutputMode: taskOutputModeManaged, OutputRequirements: requirements, ArtifactRunContext: &tool.ArtifactRunContext{SessionID: "parent", ChildSessionID: "child", TaskCallID: "call", CollectionID: "collection", VariantID: "variant", OutputRequirements: requirements}}
	outcome := buildTaskLaunchOutcome(prepared)
	requirements.Width = 1
	assertXHeaderRequirements(t, outcome.OutputRequirements)
	if outcome.ArtifactReference == nil || outcome.ArtifactReference.CollectionID != "collection" || outcome.ArtifactReference.VariantID != "variant" {
		t.Fatalf("artifact reference = %#v", outcome.ArtifactReference)
	}
	assertXHeaderRequirements(t, outcome.ArtifactReference.OutputRequirements)
}

func TestTaskOutputRequirementsApprovedManifestIsImmutable(t *testing.T) {
	requirements := &pebblestore.SessionArtifactOutputRequirements{PresetID: "x_header", Width: 1500, Height: 500, AspectRatio: "3:1", Orientation: "landscape", ResolutionSource: "preset", RegistryVersion: "2026-08-14.v1"}
	spec := taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, OutputRequirements: cloneTaskOutputRequirements(requirements)}
	manifest := taskLaunchManifest{Launches: []taskLaunchManifestRow{{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, OutputRequirements: cloneTaskOutputRequirements(requirements), ProfileSnapshot: &pebblestore.AgentProfile{}, ResolvedTools: &taskLaunchResolvedToolSummary{AllowedTools: []string{"manage_artifact"}, DisabledTools: []string{"write", "edit"}}, DisabledTools: []string{"write", "edit"}}}}
	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = digest
	raw, err := json.Marshal(map[string]any{"manifest_hash": digest, "manifest": manifest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseApprovedTaskLaunchManifest(string(raw), []taskLaunchSpec{spec}); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	manifestMap := envelope["manifest"].(map[string]any)
	launchMap := manifestMap["launches"].([]any)[0].(map[string]any)
	launchMap["output_requirements"].(map[string]any)["width"] = float64(1)
	tampered, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseApprovedTaskLaunchManifest(string(tampered), []taskLaunchSpec{spec}); err == nil || !strings.Contains(err.Error(), "snapshot hash mismatch") {
		t.Fatalf("tampered approved snapshot err = %v", err)
	}
	spec.OutputRequirements.Width = 1
	if _, err := parseApprovedTaskLaunchManifest(string(raw), []taskLaunchSpec{spec}); err == nil || !strings.Contains(err.Error(), "output requirements mismatch") {
		t.Fatalf("tampered manifest err = %v", err)
	}
}

func TestTaskOutputRequirementsRejectAmbiguousTopLevelWithLaunches(t *testing.T) {
	_, err := parseTaskCallArguments(mustJSON(t, map[string]any{"prompt": "design", "output_requirements": map[string]any{"preset": "x_header"}, "launches": []any{map[string]any{"subagent_type": "designer", "meta_prompt": "create"}}}))
	if err == nil || !strings.Contains(err.Error(), "on each Designer launch") {
		t.Fatalf("ambiguous requirements err = %v", err)
	}
}

func TestTaskOutputRequirementsRejectNonDesigner(t *testing.T) {
	for _, agent := range []string{"coder", "finder"} {
		_, err := parseTaskCallArguments(mustJSON(t, map[string]any{"prompt": "work", "subagent_type": agent, "meta_prompt": "work", "output_requirements": map[string]any{"preset": "square_1080"}}))
		if err == nil || !strings.Contains(err.Error(), "only for Designer") {
			t.Fatalf("%s err = %v", agent, err)
		}
	}
	program := map[string]any{
		"id":     "reject_program",
		"stages": []any{map[string]any{"id": "stage", "dependency_evidence": "ready"}},
		"jobs":   []any{map[string]any{"id": "job", "stage_id": "stage", "agent_type": "coder", "meta_prompt": "work", "title": "Work", "deliverable": "change", "owned_scope": []any{"swarmd/internal/**"}, "acceptance_criteria": []any{"done"}, "dependency_evidence": "ready", "output_requirements": map[string]any{"preset": "square_1080"}}},
	}
	_, err := parseTaskCallArguments(mustJSON(t, map[string]any{"prompt": "work", "program": program}))
	if err == nil || !strings.Contains(err.Error(), "only for Designer") {
		t.Fatalf("program coder err = %v", err)
	}
	_, err = parseTaskCallArguments(mustJSON(t, map[string]any{"mode": "swarm", "prompt": "code", "agent_type": "coder", "count": 2, "output_requirements": map[string]any{"preset": "square_1080"}}))
	if err == nil || !strings.Contains(err.Error(), "only for Designer") {
		t.Fatalf("swarm coder err = %v", err)
	}
	_, err = parseTaskCallArguments(mustJSON(t, map[string]any{"mode": "swarm", "prompt": "ideas", "agent_type": "idea", "count": 2, "output_requirements": map[string]any{"preset": "square_1080"}}))
	if err == nil || !strings.Contains(err.Error(), "only for Designer") {
		t.Fatalf("idea err = %v", err)
	}
}

func TestTaskAnimationProfileAllDesignerModes(t *testing.T) {
	regular, err := parseTaskCallArguments(mustJSON(t, map[string]any{"prompt": "animate", "subagent_type": "designer", "meta_prompt": "create motion", "animation_profile": map[string]any{"profile": "generative_2d"}}))
	if err != nil {
		t.Fatal(err)
	}
	assertGenerative2DProfile(t, regular.Launches[0].AnimationProfile)
	assertGenerative2DProfile(t, taskAnimationProfileFromAny(t, regular.SourceArguments["animation_profile"]))

	swarm, err := parseTaskCallArguments(mustJSON(t, map[string]any{"mode": "swarm", "prompt": "animate", "agent_type": "designer", "count": 2, "animation_profile": map[string]any{"profile": "generative_2d"}}))
	if err != nil {
		t.Fatal(err)
	}
	assertGenerative2DProfile(t, swarm.Swarm.AnimationProfile)
	request, err := buildTaskSwarmHydrationRequest(swarm, swarm.Launches)
	if err != nil {
		t.Fatal(err)
	}
	assertGenerative2DProfile(t, request.AnimationProfile)
	childPrompt, err := composeTaskSwarmChildPrompt(request, request.Items[0], taskSwarmHydratedDelta{Index: 1, Title: "Particles", Theme: "dense", Role: "motion designer", Deliverable: "animated preview"})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"pixi.js", "8.19.0", "no CDN", "pause offscreen", "clean up"} {
		if !strings.Contains(childPrompt, expected) {
			t.Fatalf("animation child prompt missing %q: %s", expected, childPrompt)
		}
	}

	program, err := parseTaskCallArguments(mustJSON(t, map[string]any{"prompt": "animate", "program": map[string]any{"id": "motion_program", "stages": []any{map[string]any{"id": "variants", "dependency_evidence": "ready"}}, "jobs": []any{map[string]any{"id": "motion", "stage_id": "variants", "agent_type": "designer", "meta_prompt": "create", "title": "Motion", "deliverable": "animation", "acceptance_criteria": []any{"ready"}, "dependency_evidence": "ready", "animation_profile": map[string]any{"profile": "generative_2d"}}}}))
	if err != nil {
		t.Fatal(err)
	}
	definition, _, err := taskProgramDefinitionFromSpec(program.Program)
	if err != nil {
		t.Fatal(err)
	}
	assertGenerative2DProfile(t, definition.Jobs[0].AnimationProfile)
	assertGenerative2DProfile(t, program.Launches[0].AnimationProfile)

	context := managedDesignerArtifactContext(pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}, "call", regular.Launches[0], 1)
	if context == nil {
		t.Fatal("managed animation context missing")
	}
	assertGenerative2DProfile(t, context.AnimationProfile)
	delegated := buildTaskDelegationPrompt(taskDelegationPromptConfig{Description: "motion", Prompt: "create", RequestedSubagent: "designer", OutputMode: taskOutputModeManaged, AnimationProfile: context.AnimationProfile, ArtifactRunContext: context})
	for _, expected := range []string{"pixi.js", "8.19.0", "animation_profile", "no CDN", "clean up"} {
		if !strings.Contains(delegated, expected) {
			t.Fatalf("delegated animation prompt missing %q: %s", expected, delegated)
		}
	}
}

func TestTaskAnimationProfileSnapshotsAreImmutable(t *testing.T) {
	profile := &pebblestore.SessionArtifactAnimationProfile{ProfileID: "generative_2d", RegistryVersion: "2026-08-16.v1", RuntimeKind: "canvas_2d_pixi", RuntimePackage: "pixi.js", RuntimeVersion: "8.19.0", Budgets: pebblestore.SessionArtifactAnimationBudgets{MaxSimultaneousLivePreviews: 2, MaxWebGLContexts: 1, MaxDevicePixelRatio: 2, MaxCanvasPixels: 4194304, MaxParticles: 5000, MaxDrawCallsPerFrame: 500, PauseWhenOffscreen: true, StopWhenDocumentHidden: true, ReducedMotionBehavior: "static_first_frame"}}
	spec := taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, AnimationProfile: cloneTaskAnimationProfile(profile)}
	manifest := taskLaunchManifest{Launches: []taskLaunchManifestRow{{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, AnimationProfile: cloneTaskAnimationProfile(profile), ProfileSnapshot: &pebblestore.AgentProfile{}, ResolvedTools: &taskLaunchResolvedToolSummary{AllowedTools: []string{"manage_artifact"}, DisabledTools: []string{"write", "edit"}}, DisabledTools: []string{"write", "edit"}}}}
	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = digest
	raw, err := json.Marshal(map[string]any{"manifest_hash": digest, "manifest": manifest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseApprovedTaskLaunchManifest(string(raw), []taskLaunchSpec{spec}); err != nil {
		t.Fatal(err)
	}
	spec.AnimationProfile.RuntimeVersion = "latest"
	if _, err := parseApprovedTaskLaunchManifest(string(raw), []taskLaunchSpec{spec}); err == nil || !strings.Contains(err.Error(), "animation profile mismatch") {
		t.Fatalf("animation profile mutation err = %v", err)
	}
}

func TestTaskAnimationProfileRejectsUnsupportedAgentsAndOverrides(t *testing.T) {
	for _, args := range []map[string]any{
		{"prompt": "work", "subagent_type": "coder", "meta_prompt": "work", "animation_profile": map[string]any{"profile": "motion_ui"}},
		{"mode": "swarm", "prompt": "images", "agent_type": "image", "count": 1, "animation_profile": map[string]any{"profile": "motion_ui"}},
		{"prompt": "animate", "subagent_type": "designer", "meta_prompt": "work", "animation_profile": map[string]any{"profile": "generative_2d", "runtime_version": "latest"}},
	} {
		if _, err := parseTaskCallArguments(mustJSON(t, args)); err == nil {
			t.Fatalf("expected animation profile rejection for %#v", args)
		}
	}
}

func taskAnimationProfileFromAny(t *testing.T, value any) *pebblestore.SessionArtifactAnimationProfile {
	t.Helper()
	profile, ok := value.(*pebblestore.SessionArtifactAnimationProfile)
	if !ok {
		t.Fatalf("animation profile value = %#v", value)
	}
	return profile
}

func assertGenerative2DProfile(t *testing.T, profile *pebblestore.SessionArtifactAnimationProfile) {
	t.Helper()
	if profile == nil || profile.ProfileID != "generative_2d" || profile.RuntimePackage != "pixi.js" || profile.RuntimeVersion != "8.19.0" || profile.Budgets.NetworkAllowed || !profile.Budgets.PauseWhenOffscreen {
		t.Fatalf("animation profile = %#v", profile)
	}
}

func assertXHeaderRequirements(t *testing.T, requirements *pebblestore.SessionArtifactOutputRequirements) {
	t.Helper()
	if requirements == nil || requirements.PresetID != "x_header" || requirements.Width != 1500 || requirements.Height != 500 || requirements.AspectRatio != "3:1" || requirements.Orientation != "landscape" {
		t.Fatalf("requirements = %#v", requirements)
	}
}
