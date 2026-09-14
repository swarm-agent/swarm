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

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

type fakeVideoGenerationService struct {
	lastReq videogen.ManagedVideoRequest
	calls   int
	result  videogen.ManagedVideoResult
	err     error
}

func (f *fakeVideoGenerationService) GenerateManagedVideo(ctx context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	f.calls++
	f.lastReq = req
	if f.err != nil {
		return videogen.ManagedVideoResult{}, f.err
	}
	res := f.result
	if len(res.Bytes) == 0 {
		res.Bytes = []byte("fake-generated-video-mp4")
		res.MediaType = "video/mp4"
		res.Model = "veo-3.1-generate-preview"
		res.Provider = "google"
	}
	if res.EstimatedCostUSD == 0 && res.PricingSummary == "" {
		cost, summary := videogen.EstimateVideoCost(res.Provider, res.Model, req.DurationSeconds, req.Source != nil, nil)
		res.EstimatedCostUSD = cost
		res.PricingSummary = summary
	}
	return res, nil
}

func TestManageArtifactGenerateVideoRequiresConfiguredService(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID:    "video-call-1",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_video","prompt":"A mountain flyover"}`,
	}

	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err == nil || !strings.Contains(err.Error(), "video generation is not configured") {
		t.Fatalf("expected video generation not configured error, got: %v", err)
	}
}

func TestManageArtifactGenerateVideoRequiresPrompt(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	runtime.SetManagedVideoGenerationService(&fakeVideoGenerationService{})

	ctx, scope := artifactToolContext()
	call := Call{
		CallID:    "video-call-prompt",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_video","prompt":""}`,
	}

	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err == nil || !strings.Contains(err.Error(), "requires prompt") {
		t.Fatalf("expected requires prompt error, got: %v", err)
	}
}

func TestManageArtifactGenerateVideoInitialGeneration(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeVideoGenerationService{
		result: videogen.ManagedVideoResult{
			Bytes:         []byte("veo-output-video"),
			MediaType:     "video/mp4",
			Model:         "veo-3.1-generate-preview",
			Provider:      "google",
			InteractionID: "veo-interaction-none",
		},
	}
	runtime.SetManagedVideoGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID:    "video-gen-initial",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_video","prompt":"A drone flying over Tokyo at night","aspect_ratio":"16:9","resolution":"1080p","duration_seconds":8}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_video: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res["status"] != "ok" {
		t.Fatalf("status = %v, want ok", res["status"])
	}
	if res["prompt"] != "A drone flying over Tokyo at night" {
		t.Fatalf("prompt = %v, want %q", res["prompt"], "A drone flying over Tokyo at night")
	}
	if res["title"] != "A drone flying over Tokyo at night" {
		t.Fatalf("title = %v, want 'A drone flying over Tokyo at night'", res["title"])
	}
	if res["cost_per_video_usd"] == nil || res["estimated_cost_usd"] == nil {
		t.Fatalf("expected cost fields in response: %+v", res)
	}
	if res["pricing_summary"] == "" {
		t.Fatalf("expected non-empty pricing summary in response")
	}
	if refs, ok := res["references"].([]any); !ok || len(refs) != 1 {
		t.Fatalf("expected 1 reference in references array, got: %v", res["references"])
	}

	if generator.calls != 1 {
		t.Fatalf("expected 1 generator call, got %d", generator.calls)
	}
	if generator.lastReq.Prompt != "A drone flying over Tokyo at night" {
		t.Fatalf("prompt mismatch: %q", generator.lastReq.Prompt)
	}
	if generator.lastReq.AspectRatio != "16:9" || generator.lastReq.Resolution != "1080p" || generator.lastReq.DurationSeconds != 8 {
		t.Fatalf("params mismatch: aspect=%s res=%s dur=%d", generator.lastReq.AspectRatio, generator.lastReq.Resolution, generator.lastReq.DurationSeconds)
	}
	if generator.lastReq.Source != nil {
		t.Fatalf("initial generation should have nil source")
	}

	if authority.created.MediaType != "video/mp4" || authority.created.Presentation.Kind != "video" || !authority.created.Presentation.Previewable {
		t.Fatalf("authority created artifact mismatch: %+v", authority.created)
	}
	if authority.created.Presentation.Label != "A drone flying over Tokyo at night" {
		t.Fatalf("presentation label mismatch: %q", authority.created.Presentation.Label)
	}
	if authority.created.Presentation.Description != "A drone flying over Tokyo at night" {
		t.Fatalf("presentation description should contain prompt, got: %q", authority.created.Presentation.Description)
	}
	if authority.created.CollectionDescription != "A drone flying over Tokyo at night" {
		t.Fatalf("collection description should contain prompt, got: %q", authority.created.CollectionDescription)
	}
	if string(authority.created.Body) != "veo-output-video" {
		t.Fatalf("body mismatch: %q", string(authority.created.Body))
	}
}

func TestManageArtifactGenerateVideoIterationWithSource(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{
		variant: pebblestore.SessionArtifactVariant{
			ID:           "source-var-1",
			CollectionID: "coll-1",
			SessionID:    "session-1",
			EventSeq:     42,
			Status:       pebblestore.SessionArtifactStatusReady,
			MediaType:    "video/mp4",
			Lineage: pebblestore.SessionArtifactLineage{
				IterationID: "v1_omni_turn1",
			},
		},
		readBody: []byte("initial-video-mp4-data"),
	}
	runtime.SetArtifactAuthority(authority)

	generator := &fakeVideoGenerationService{
		result: videogen.ManagedVideoResult{
			Bytes:         []byte("omni-refined-video-mp4"),
			MediaType:     "video/mp4",
			Model:         "gemini-omni-1.1-flash",
			Provider:      "google",
			InteractionID: "v1_omni_turn2",
		},
	}
	runtime.SetManagedVideoGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "video-iteration-call",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_video",
			"prompt": "Add heavy rain and glowing neon puddles",
			"source_session_id": "session-1",
			"source_collection_id": "coll-1",
			"source_variant_id": "source-var-1",
			"source_event_seq": 42
		}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_video iteration: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}

	if res["prompt"] != "Add heavy rain and glowing neon puddles" {
		t.Fatalf("prompt mismatch: %v", res["prompt"])
	}
	if res["cost_per_video_usd"] == nil || res["estimated_cost_usd"] == nil {
		t.Fatalf("expected cost fields in iteration response: %+v", res)
	}
	if authority.created.Presentation.Description != "Add heavy rain and glowing neon puddles" {
		t.Fatalf("presentation description mismatch: %q", authority.created.Presentation.Description)
	}

	if generator.lastReq.Source == nil {
		t.Fatalf("expected non-nil source in iteration request")
	}
	if generator.lastReq.Source.InteractionID != "v1_omni_turn1" {
		t.Fatalf("expected interaction id v1_omni_turn1, got %q", generator.lastReq.Source.InteractionID)
	}
	if string(generator.lastReq.Source.Bytes) != "initial-video-mp4-data" {
		t.Fatalf("source bytes mismatch: got %q", string(generator.lastReq.Source.Bytes))
	}

	if authority.created.SourceSessionID != "session-1" || authority.created.SourceVariantID != "source-var-1" || authority.created.SourceEventSeq != 42 {
		t.Fatalf("lineage not passed to create: %+v", authority.created)
	}
	if authority.created.IterationID != "v1_omni_turn2" {
		t.Fatalf("expected iteration id v1_omni_turn2, got %q", authority.created.IterationID)
	}
}

func TestManageArtifactGenerateVideoCountBatch(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeVideoGenerationService{}
	runtime.SetManagedVideoGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID:    "video-gen-count",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_video","prompt":"A speeding red sports car","count":3}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_video count: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if generator.calls != 3 {
		t.Fatalf("expected 3 generator calls, got %d", generator.calls)
	}
	if variants, ok := res["variants"].([]any); !ok || len(variants) != 3 {
		t.Fatalf("expected 3 variants in variants array, got: %v", res["variants"])
	}
	if refs, ok := res["references"].([]any); !ok || len(refs) != 3 {
		t.Fatalf("expected 3 references in references array, got: %v", res["references"])
	}
	if authority.created.IterationIndex != 3 {
		t.Fatalf("expected last variant iteration index 3, got: %d", authority.created.IterationIndex)
	}
	if !strings.Contains(authority.created.IterationLabel, "Variant 3") {
		t.Fatalf("expected iteration label to contain Variant 3, got: %q", authority.created.IterationLabel)
	}
}

func TestManageArtifactGenerateVideoExplicitTitle(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeVideoGenerationService{}
	runtime.SetManagedVideoGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID:    "video-gen-title",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_video","prompt":"A speeding red sports car on the highway","title":"Crimson Velocity"}`,
	}
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_video: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res["title"] != "Crimson Velocity" {
		t.Fatalf("title = %v, want 'Crimson Velocity'", res["title"])
	}
	if authority.created.CollectionName != "Crimson Velocity" || authority.created.Presentation.Label != "Crimson Velocity" {
		t.Fatalf("created collection name or label mismatch: %+v", authority.created)
	}
	if authority.created.Presentation.Description != "A speeding red sports car on the highway" {
		t.Fatalf("created presentation description mismatch: %q", authority.created.Presentation.Description)
	}
}

func TestManageArtifactGenerateVideoWithWorkspaceImagePath(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeVideoGenerationService{}
	runtime.SetManagedVideoGenerationService(generator)

	tempDir := t.TempDir()
	pngData := testPNGImage()
	imagePath := filepath.Join(tempDir, "input_portrait.png")
	if err := os.WriteFile(imagePath, pngData, 0o644); err != nil {
		t.Fatalf("write test image: %v", err)
	}

	ctx, scope := artifactToolContext()
	scope.PrimaryPath = tempDir

	call := Call{
		CallID: "video-gen-img-path",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{"action":"generate_video","prompt":"Animate this portrait with gentle camera motion","image":"%s"}`,
			filepath.Base(imagePath)),
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_video with image path: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res["status"] != "ok" {
		t.Fatalf("status = %v, want ok", res["status"])
	}
	if res["has_image_input"] != true {
		t.Fatalf("expected has_image_input = true in response: %+v", res)
	}
	if generator.lastReq.Image == nil {
		t.Fatal("expected generator.lastReq.Image to be non-nil")
	}
	if string(generator.lastReq.Image.Bytes) != string(pngData) {
		t.Fatal("generator image bytes mismatch")
	}
	if generator.lastReq.Image.MediaType != "image/png" {
		t.Fatalf("generator image media type = %q, want image/png", generator.lastReq.Image.MediaType)
	}
}

func TestManageArtifactGenerateVideoWithDataURI(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeVideoGenerationService{}
	runtime.SetManagedVideoGenerationService(generator)

	ctx, scope := artifactToolContext()
	pngData := testPNGImage()
	dataURI := fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString(pngData))

	call := Call{
		CallID: "video-gen-data-uri",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{"action":"generate_video","prompt":"Bring painting to life","image":"%s"}`,
			dataURI),
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_video with data URI: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res["has_image_input"] != true {
		t.Fatalf("expected has_image_input = true")
	}
	if generator.lastReq.Image == nil {
		t.Fatal("expected non-nil image in request")
	}
	if string(generator.lastReq.Image.Bytes) != string(pngData) {
		t.Fatal("image bytes mismatch")
	}
	if generator.lastReq.Image.MediaType != "image/png" {
		t.Fatalf("image media type = %q, want image/png", generator.lastReq.Image.MediaType)
	}
}

func TestManageArtifactGenerateVideoWithArtifactReference(t *testing.T) {
	runtime := NewRuntime(1)
	pngData := testPNGImage()
	authority := &fakeArtifactAuthority{
		variant: pebblestore.SessionArtifactVariant{
			ID:           "img-var-1",
			CollectionID: "coll-img",
			SessionID:    "session-1",
			EventSeq:     10,
			Status:       pebblestore.SessionArtifactStatusReady,
			MediaType:    "image/png",
		},
		readBody: pngData,
	}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeVideoGenerationService{}
	runtime.SetManagedVideoGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "video-gen-art-ref",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_video",
			"prompt": "Animate the artwork",
			"image": {
				"session_id": "session-1",
				"collection_id": "coll-img",
				"variant_id": "img-var-1",
				"event_seq": 10
			}
		}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_video with artifact reference: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res["has_image_input"] != true {
		t.Fatalf("expected has_image_input = true")
	}
	if generator.lastReq.Image == nil {
		t.Fatal("expected non-nil image in request")
	}
	if string(generator.lastReq.Image.Bytes) != string(pngData) {
		t.Fatal("image bytes mismatch")
	}
	if authority.created.SourceVariantID != "img-var-1" || authority.created.SourceCollectionID != "coll-img" || authority.created.SourceEventSeq != 10 {
		t.Fatalf("lineage not preserved: %+v", authority.created)
	}
}

func TestManageArtifactGenerateVideoWithSourceImageArtifact(t *testing.T) {
	runtime := NewRuntime(1)
	pngData := testPNGImage()
	authority := &fakeArtifactAuthority{
		variant: pebblestore.SessionArtifactVariant{
			ID:           "source-img-var",
			CollectionID: "coll-source",
			SessionID:    "session-1",
			EventSeq:     25,
			Status:       pebblestore.SessionArtifactStatusReady,
			MediaType:    "image/jpeg",
		},
		readBody: pngData,
	}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeVideoGenerationService{}
	runtime.SetManagedVideoGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "video-gen-source-img",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_video",
			"prompt": "Add subtle motion to this image",
			"source_session_id": "session-1",
			"source_collection_id": "coll-source",
			"source_variant_id": "source-img-var",
			"source_event_seq": 25
		}`,
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_video with source image: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res["has_image_input"] != true {
		t.Fatalf("expected has_image_input = true")
	}
	if generator.lastReq.Image == nil {
		t.Fatal("expected non-nil image from source artifact")
	}
	if generator.lastReq.Source != nil {
		t.Fatal("expected nil video source when source is an image")
	}
	if authority.created.SourceVariantID != "source-img-var" || authority.created.SourceEventSeq != 25 {
		t.Fatalf("source lineage mismatch: %+v", authority.created)
	}
}

func TestManageArtifactGenerateVideoWithImagePathArg(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeVideoGenerationService{}
	runtime.SetManagedVideoGenerationService(generator)

	tempDir := t.TempDir()
	pngData := testPNGImage()
	imagePath := filepath.Join(tempDir, "alt_input.png")
	if err := os.WriteFile(imagePath, pngData, 0o644); err != nil {
		t.Fatalf("write test image: %v", err)
	}

	ctx, scope := artifactToolContext()
	scope.PrimaryPath = tempDir

	call := Call{
		CallID: "video-gen-image-path-arg",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{"action":"generate_video","prompt":"Animate image","image_path":"%s"}`,
			filepath.Base(imagePath)),
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_video with image_path arg: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res["has_image_input"] != true {
		t.Fatalf("expected has_image_input = true")
	}
	if generator.lastReq.Image == nil {
		t.Fatal("expected non-nil image")
	}
}

func TestManageArtifactGenerateVideoWithWorkspaceSVGImagePath(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeVideoGenerationService{}
	runtime.SetManagedVideoGenerationService(generator)

	tempDir := t.TempDir()
	svgData := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100"><rect width="100" height="100"/></svg>`)
	imagePath := filepath.Join(tempDir, "logo.svg")
	if err := os.WriteFile(imagePath, svgData, 0o644); err != nil {
		t.Fatalf("write test svg: %v", err)
	}

	ctx, scope := artifactToolContext()
	scope.PrimaryPath = tempDir

	call := Call{
		CallID: "video-gen-svg-path",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{"action":"generate_video","prompt":"Animate svg logo","image_path":"%s"}`,
			filepath.Base(imagePath)),
	}

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("execute generate_video with svg image_path: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res["has_image_input"] != true {
		t.Fatalf("expected has_image_input = true")
	}
	if generator.lastReq.Image == nil {
		t.Fatal("expected non-nil image")
	}
	if generator.lastReq.Image.MediaType != "image/svg+xml" {
		t.Fatalf("expected MediaType image/svg+xml, got %q", generator.lastReq.Image.MediaType)
	}
}

func TestManageArtifactGenerateVideoImageNotFound(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	generator := &fakeVideoGenerationService{}
	runtime.SetManagedVideoGenerationService(generator)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "video-gen-missing-img",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_video",
			"prompt": "Animate image",
			"image": "nonexistent_file_12345.png"
		}`,
	}

	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err == nil || !strings.Contains(err.Error(), "resolve image input") {
		t.Fatalf("expected error containing 'resolve image input', got: %v", err)
	}
}
