package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/audiogen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

type fakeAudioGenerationService struct {
	lastReq audiogen.ManagedAudioRequest
	allReqs []audiogen.ManagedAudioRequest
	calls   int
	result  audiogen.ManagedAudioResult
	err     error
}

func (f *fakeAudioGenerationService) GenerateManagedAudio(ctx context.Context, req audiogen.ManagedAudioRequest) (audiogen.ManagedAudioResult, error) {
	f.calls++
	f.lastReq = req
	f.allReqs = append(f.allReqs, req)
	if f.err != nil {
		return audiogen.ManagedAudioResult{}, f.err
	}
	res := f.result
	if len(res.Bytes) == 0 {
		res.Bytes = []byte("fake-generated-audio-mp3")
		res.MediaType = "audio/mp3"
		res.Model = "lyria-3-clip-preview"
		res.Provider = "google"
		res.DurationMs = 30000
		res.InteractionID = fmt.Sprintf("interaction-%d", f.calls)
		res.Metadata = audiogen.AudioMetadata{
			Prompt:           req.Prompt,
			Model:            res.Model,
			Provider:         res.Provider,
			ActualDurationMs: res.DurationMs,
			InteractionID:    res.InteractionID,
		}
	}
	if res.EstimatedCostUSD == 0 && res.PricingSummary == "" {
		cost, summary := audiogen.EstimateAudioCost(res.Provider, res.Model, req.Source != nil, nil)
		res.EstimatedCostUSD = cost
		res.PricingSummary = summary
	}
	return res, nil
}

func TestManageArtifactGenerateAudioRequiresConfiguredService(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID:    "audio-call-1",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_audio","prompt":"Jazz piano solo"}`,
	}

	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err == nil || !strings.Contains(err.Error(), "audio generation is not configured") {
		t.Fatalf("expected audio generation not configured error, got: %v", err)
	}
}

func TestManageArtifactGenerateAudioRequiresPrompt(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	runtime.SetManagedAudioGenerationService(&fakeAudioGenerationService{})

	ctx, scope := artifactToolContext()
	call := Call{
		CallID:    "audio-call-prompt",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_audio","prompt":""}`,
	}

	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err == nil || !strings.Contains(err.Error(), "requires prompt") {
		t.Fatalf("expected requires prompt error, got: %v", err)
	}
}

func TestManageArtifactGenerateAudioInitialGeneration(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeAudioGenerationService{
		result: audiogen.ManagedAudioResult{
			Bytes:         []byte("lyria-audio-bytes"),
			MediaType:     "audio/mp3",
			Model:         "lyria-3-clip-preview",
			Provider:      "google",
			InteractionID: "lyria-interaction-1",
			DurationMs:    30000,
			Lyrics:        "instrumental",
			Metadata: audiogen.AudioMetadata{
				Prompt:           "Fast synthwave beats with 80s arpeggio",
				Model:            "lyria-3-clip-preview",
				Provider:         "google",
				InteractionID:    "lyria-interaction-1",
				ActualDurationMs: 30000,
			},
		},
	}
	runtime.SetManagedAudioGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "audio-gen-initial",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_audio",
			"prompt": "Fast synthwave beats with 80s arpeggio",
			"duration_seconds": 28
		}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_audio: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}

	if res["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", res["status"])
	}
	if res["prompt"] != "Fast synthwave beats with 80s arpeggio" {
		t.Fatalf("prompt mismatch: %v", res["prompt"])
	}
	if res["model"] != "lyria-3-clip-preview" {
		t.Fatalf("model mismatch: %v", res["model"])
	}
	if res["provider"] != "google" {
		t.Fatalf("provider mismatch: %v", res["provider"])
	}
	if fmt.Sprint(res["duration_seconds"]) != "28" {
		t.Fatalf("duration_seconds mismatch: %v", res["duration_seconds"])
	}
	if fmt.Sprint(res["duration_ms"]) != "30000" {
		t.Fatalf("duration_ms mismatch: %v", res["duration_ms"])
	}
	if res["lyrics"] != "instrumental" {
		t.Fatalf("lyrics mismatch: %v", res["lyrics"])
	}
	if res["estimated_cost_usd"] == nil || res["cost_per_audio_usd"] == nil {
		t.Fatalf("expected cost fields, got: %+v", res)
	}

	// Verify artifact creation in authority
	if authority.created.MediaType != "audio/mp3" {
		t.Fatalf("expected media type audio/mp3, got %q", authority.created.MediaType)
	}
	if authority.created.Presentation.Kind != "audio" || !authority.created.Presentation.Previewable {
		t.Fatalf("expected previewable audio presentation, got %+v", authority.created.Presentation)
	}
	if authority.created.Filename != "generated-audio.mp3" {
		t.Fatalf("expected filename generated-audio.mp3, got %q", authority.created.Filename)
	}
	if authority.created.IterationID != "lyria-interaction-1" {
		t.Fatalf("expected iteration id lyria-interaction-1, got %q", authority.created.IterationID)
	}

	// Verify request sent to generator
	if generator.calls != 1 {
		t.Fatalf("expected 1 generator call, got %d", generator.calls)
	}
	if generator.lastReq.Prompt != "Fast synthwave beats with 80s arpeggio" {
		t.Fatalf("prompt sent to generator mismatch: %q", generator.lastReq.Prompt)
	}
	if generator.lastReq.DurationSeconds != 28 {
		t.Fatalf("duration sent to generator mismatch: %d", generator.lastReq.DurationSeconds)
	}
}

func TestManageArtifactGenerateAudioMultiPromptVariations(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeAudioGenerationService{}
	runtime.SetManagedAudioGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "audio-multi-prompts",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_audio",
			"prompts": [
				"Upbeat disco funk with slapping bass",
				"Ambient calm piano with rain sounds",
				"High-energy rock guitar solo"
			],
			"duration_seconds": 30
		}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_audio with multi-prompts: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}

	if generator.calls != 3 {
		t.Fatalf("expected 3 generator calls, got %d", generator.calls)
	}
	if len(generator.allReqs) != 3 {
		t.Fatalf("expected 3 requests recorded, got %d", len(generator.allReqs))
	}
	expectedPrompts := []string{
		"Upbeat disco funk with slapping bass",
		"Ambient calm piano with rain sounds",
		"High-energy rock guitar solo",
	}
	for i, exp := range expectedPrompts {
		if generator.allReqs[i].Prompt != exp {
			t.Fatalf("call %d prompt mismatch: want %q, got %q", i, exp, generator.allReqs[i].Prompt)
		}
	}

	variants, ok := res["variants"].([]any)
	if !ok || len(variants) != 3 {
		t.Fatalf("expected 3 variants in result, got: %v", res["variants"])
	}
	references, ok := res["references"].([]any)
	if !ok || len(references) != 3 {
		t.Fatalf("expected 3 references in result, got: %v", res["references"])
	}
	if authority.createCalls != 3 {
		t.Fatalf("expected 3 authority create calls, got %d", authority.createCalls)
	}
}

func TestManageArtifactGenerateAudioMultipleVariantsCount(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeAudioGenerationService{}
	runtime.SetManagedAudioGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "audio-count-test",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_audio",
			"prompt": "Chill acoustic guitar melody",
			"count": 2
		}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_audio with count=2: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}

	if generator.calls != 2 {
		t.Fatalf("expected 2 generator calls, got %d", generator.calls)
	}
	variants, ok := res["variants"].([]any)
	if !ok || len(variants) != 2 {
		t.Fatalf("expected 2 variants, got: %v", res["variants"])
	}
}

func TestManageArtifactGenerateAudioWithImageInspiration(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeAudioGenerationService{}
	runtime.SetManagedAudioGenerationService(generator)

	tempDir := t.TempDir()
	pngData := testPNGImage()
	imagePath := filepath.Join(tempDir, "inspiration.png")
	if err := os.WriteFile(imagePath, pngData, 0o644); err != nil {
		t.Fatalf("write temp image: %v", err)
	}

	ctx, scope := artifactToolContext()
	scope.PrimaryPath = tempDir

	call := Call{
		CallID: "audio-image-test",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{
			"action": "generate_audio",
			"prompt": "Orchestral piece inspired by painting",
			"image_path": %q
		}`, filepath.Base(imagePath)),
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_audio with image: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}

	if res["has_image_input"] != true {
		t.Fatalf("expected has_image_input=true in response, got %v", res["has_image_input"])
	}
	if generator.lastReq.Image == nil {
		t.Fatalf("expected non-nil Image in generator request")
	}
	if len(generator.lastReq.Image.Bytes) != len(pngData) {
		t.Fatalf("image bytes length mismatch: got %d, want %d", len(generator.lastReq.Image.Bytes), len(pngData))
	}
	if generator.lastReq.Image.MediaType != "image/png" {
		t.Fatalf("expected image/png media type, got %q", generator.lastReq.Image.MediaType)
	}
}

func TestManageArtifactGenerateAudioWithDataURIImage(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeAudioGenerationService{}
	runtime.SetManagedAudioGenerationService(generator)

	ctx, scope := artifactToolContext()
	pngData := testPNGImage()
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)

	call := Call{
		CallID: "audio-datauri-test",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{
			"action": "generate_audio",
			"prompt": "Electronic cyberpunk track",
			"image": %q
		}`, dataURI),
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_audio with data URI: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}

	if res["has_image_input"] != true {
		t.Fatalf("expected has_image_input=true, got %v", res["has_image_input"])
	}
	if generator.lastReq.Image == nil || len(generator.lastReq.Image.Bytes) == 0 {
		t.Fatalf("expected non-empty image in generator request")
	}
}

func TestManageArtifactGenerateAudioIterationWithSourceLineage(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{
		variant: pebblestore.SessionArtifactVariant{
			ID:           "source-audio-var-1",
			CollectionID: "coll-audio-1",
			SessionID:    "session-1",
			MediaType:    "audio/mp3",
			Lineage: pebblestore.SessionArtifactLineage{
				IterationID: "lyria-interaction-turn1",
			},
		},
		readBody: []byte("original-audio-bytes"),
	}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeAudioGenerationService{
		result: audiogen.ManagedAudioResult{
			Bytes:         []byte("iterated-audio-bytes"),
			MediaType:     "audio/mp3",
			Model:         "lyria-3-clip-preview",
			Provider:      "google",
			InteractionID: "lyria-interaction-turn2",
			DurationMs:    30000,
		},
	}
	runtime.SetManagedAudioGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "audio-iteration-call",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_audio",
			"prompt": "Make the tempo faster and add live drums",
			"source_session_id": "session-1",
			"source_collection_id": "coll-audio-1",
			"source_variant_id": "source-audio-var-1",
			"source_event_seq": 5
		}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_audio iteration: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}

	if res["prompt"] != "Make the tempo faster and add live drums" {
		t.Fatalf("prompt mismatch: %v", res["prompt"])
	}

	// Verify generator received the source with correct InteractionID
	if generator.lastReq.Source == nil {
		t.Fatalf("expected non-nil Source in generator request")
	}
	if generator.lastReq.Source.InteractionID != "lyria-interaction-turn1" {
		t.Fatalf("expected InteractionID lyria-interaction-turn1, got %q", generator.lastReq.Source.InteractionID)
	}

	// Verify authority recorded source reference
	if authority.created.SourceSessionID != "session-1" {
		t.Fatalf("expected SourceSessionID session-1, got %q", authority.created.SourceSessionID)
	}
	if authority.created.SourceCollectionID != "coll-audio-1" {
		t.Fatalf("expected SourceCollectionID coll-audio-1, got %q", authority.created.SourceCollectionID)
	}
	if authority.created.SourceVariantID != "source-audio-var-1" {
		t.Fatalf("expected SourceVariantID source-audio-var-1, got %q", authority.created.SourceVariantID)
	}
	if authority.created.SourceEventSeq != 5 {
		t.Fatalf("expected SourceEventSeq 5, got %d", authority.created.SourceEventSeq)
	}
	if authority.created.IterationID != "lyria-interaction-turn2" {
		t.Fatalf("expected new IterationID lyria-interaction-turn2, got %q", authority.created.IterationID)
	}
}

func TestManageArtifactGenerateAudioValidationErrors(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	runtime.SetManagedAudioGenerationService(&fakeAudioGenerationService{})

	ctx, scope := artifactToolContext()

	// Unsupported field
	call1 := Call{
		CallID:    "unsupported-field",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_audio","prompt":"test","invalid_key":"value"}`,
	}
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call1)
	if err == nil || !strings.Contains(err.Error(), "unsupported field") {
		t.Fatalf("expected unsupported field error, got: %v", err)
	}

	// Count > 8
	call2 := Call{
		CallID:    "count-too-high",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_audio","prompt":"test","count":9}`,
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call2)
	if err == nil || !strings.Contains(err.Error(), "count exceeds maximum of 8") {
		t.Fatalf("expected count exceeds error, got: %v", err)
	}

	// Prompts > 8
	call3 := Call{
		CallID:    "prompts-too-many",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_audio","prompts":["1","2","3","4","5","6","7","8","9"]}`,
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call3)
	if err == nil || !strings.Contains(err.Error(), "prompts exceeds maximum of 8") {
		t.Fatalf("expected prompts exceeds error, got: %v", err)
	}

	// Incomplete source_* fields
	call4 := Call{
		CallID:    "incomplete-source",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_audio","prompt":"test","source_session_id":"s1"}`,
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call4)
	if err == nil || !strings.Contains(err.Error(), "requires source_session_id, source_collection_id, source_variant_id, and source_event_seq") {
		t.Fatalf("expected incomplete source error, got: %v", err)
	}

	// Prompt exceeds character limit
	longPrompt := strings.Repeat("A", manageArtifactMaxPromptRunes+1)
	call5 := Call{
		CallID:    "long-prompt",
		Name:      "manage_artifact",
		Arguments: fmt.Sprintf(`{"action":"generate_audio","prompt":%q}`, longPrompt),
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call5)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d characters", manageArtifactMaxPromptRunes)) {
		t.Fatalf("expected prompt character limit error, got: %v", err)
	}
}

func TestManageArtifactGenerateAudioAcceptsLabelArgument(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeAudioGenerationService{}
	runtime.SetManagedAudioGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "audio-label-test",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_audio",
			"prompt": "Funky groove",
			"label": "Custom Funky Groove Label"
		}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_audio with label: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res["title"] != "Custom Funky Groove Label" {
		t.Fatalf("expected title Custom Funky Groove Label, got %v", res["title"])
	}
	if authority.created.Presentation.Label != "Custom Funky Groove Label" {
		t.Fatalf("expected presentation label Custom Funky Groove Label, got %q", authority.created.Presentation.Label)
	}
}

func TestManageArtifactGenerateAudioVariantIncludesLineageInVariantPayload(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeAudioGenerationService{}
	runtime.SetManagedAudioGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "audio-lineage-test",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_audio",
			"prompt": "Deep house bassline"
		}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_audio: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}

	artifactMap, ok := res["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("expected artifact map in response, got %T", res["artifact"])
	}
	lineageMap, ok := artifactMap["lineage"].(map[string]any)
	if !ok {
		t.Fatalf("expected lineage in artifact variant, got: %+v", artifactMap)
	}
	if lineageMap["iteration_id"] != "interaction-1" {
		t.Fatalf("expected iteration_id interaction-1 in lineage, got %v", lineageMap["iteration_id"])
	}
}

func TestManageArtifactGenerateAudioAcceptsModelArgument(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeAudioGenerationService{}
	runtime.SetManagedAudioGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "audio-model-test",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_audio",
			"prompt": "Liquid DnB roller",
			"model": "lyria-3.5"
		}`,
	}

	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_audio with model: %v", err)
	}

	if generator.lastReq.Model != "lyria-3.5" {
		t.Errorf("generator.lastReq.Model = %q, want lyria-3.5", generator.lastReq.Model)
	}
}

func TestManageArtifactGenerateAudioResolvesConfiguredUIModel(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeAudioGenerationService{}
	runtime.SetManagedAudioGenerationService(generator)
	runtime.SetManageThemeServices(&fakeImageUISettings{
		settings: uisettings.UISettings{
			Tools: uisettings.ToolSettings{
				Audio: uisettings.ToolAudioSettings{
					DefaultModel: "lyria-3-pro-preview",
				},
			},
		},
	}, nil)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "audio-configured-model-test",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_audio",
			"prompt": "Melodic DnB anthem"
		}`,
	}

	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_audio with configured ui model: %v", err)
	}

	if generator.lastReq.Model != "lyria-3-pro-preview" {
		t.Errorf("generator.lastReq.Model = %q, want lyria-3-pro-preview", generator.lastReq.Model)
	}
}
