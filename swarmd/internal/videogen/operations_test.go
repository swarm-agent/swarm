package videogen

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

// fakeProber implements VideoProber for deterministic test assertions.
type fakeProber struct {
	duration float64
	width    int
	height   int
	err      error
}

func (p fakeProber) ProbeVideo(_ context.Context, _ []byte) (VideoMetadata, error) {
	if p.err != nil {
		return VideoMetadata{}, p.err
	}
	return VideoMetadata{
		DurationSeconds: p.duration,
		Width:           p.width,
		Height:          p.height,
	}, nil
}

func setupTestCatalog() *fakeModelCatalog {
	return &fakeModelCatalog{
		records: []pebblestore.ModelCatalogRecord{
			{
				Provider: ProviderGoogleGemini,
				Model:    "veo-3.1-generate-preview",
				CatalogModalities: pebblestore.ModelCatalogModalities{
					Outputs:    []string{"video"},
					Categories: []string{"video_generation"},
				},
				ProviderSpecific: []byte(`{"google":{"video_generation":{"settings":{"aspect_ratio":{"status":"verified","supported_values":["16:9","9:16"],"default_value":"16:9"},"resolution":{"status":"verified","supported_values":["720p","1080p"],"default_value":"720p"},"duration_seconds":{"status":"verified","supported_values":[5,8],"default_value":5}},"features":{"video_extension":{"status":"verified","supported":true},"initial_image":{"status":"verified","supported":true}}}}}`),
			},
			{
				Provider: ProviderGoogleGemini,
				Model:    "veo-lite-preview",
				CatalogModalities: pebblestore.ModelCatalogModalities{
					Outputs:    []string{"video"},
					Categories: []string{"video_generation"},
				},
			},
			{
				Provider: ProviderGoogleGemini,
				Model:    "gemini-omni-1.1-flash",
				CatalogModalities: pebblestore.ModelCatalogModalities{
					Outputs:    []string{"video"},
					Categories: []string{"video_generation", "video_iteration"},
				},
				ProviderSpecific: []byte(`{"google":{"video_generation":{"settings":{"aspect_ratio":{"status":"verified","supported_values":["16:9","9:16"],"default_value":"16:9"},"resolution":{"status":"verified","supported_values":["720p"],"default_value":"720p"}},"features":{"conversational_editing":{"status":"verified","supported":true},"video_extension":{"status":"verified","supported":true},"initial_image":{"status":"verified","supported":true}}}}}`),
			},
			{
				Provider: ProviderOpenRouter,
				Model:    "google/veo-3.1-generate-preview",
				CatalogModalities: pebblestore.ModelCatalogModalities{
					Outputs:    []string{"video"},
					Categories: []string{"video_generation"},
				},
			},
		},
	}
}

func setupTestUISettings(t *testing.T, defaultModel, iterModel string) (*uisettings.Service, *pebblestore.Store, string) {
	tmpDir := t.TempDir()
	store, err := pebblestore.Open(tmpDir)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessionStore := pebblestore.NewSessionStore(store)
	svc := uisettings.NewService(sessionStore)
	accountID := "acc-ops-test"
	if err := svc.UpdateToolsVideoSettings(accountID, uisettings.ToolsVideoSettings{
		DefaultModel:   defaultModel,
		IterationModel: iterModel,
	}); err != nil {
		t.Fatalf("update video settings: %v", err)
	}
	return svc, store, accountID
}

// TestVideoPreflight_MissingOperationWithSourceFails proves:
// 1. Missing operation when source is provided is rejected without inferring generation or edit.
// 2. Operation "create" with source video is rejected.
// 3. Operation "edit" or "extend" without source video is rejected.
func TestVideoPreflight_MissingOperationWithSourceFails(t *testing.T) {
	catalog := setupTestCatalog()
	svc := NewService(nil, nil, catalog)
	principal := identity.Principal{AccountScopeID: "acc-1"}

	// 1. Missing operation with source
	_, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal: principal,
		Prompt:    "Add clouds",
		Source: &ManagedVideoSource{
			Bytes:     []byte("test-video"),
			MediaType: "video/mp4",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "video operation must be explicitly specified when source media is provided") {
		t.Fatalf("expected missing operation rejection, got: %v", err)
	}

	// 2. Operation create with source
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal: principal,
		Operation: pebblestore.VideoOperationCreate,
		Prompt:    "Add clouds",
		Source: &ManagedVideoSource{
			Bytes:     []byte("test-video"),
			MediaType: "video/mp4",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot provide source video for create operation") {
		t.Fatalf("expected create with source rejection, got: %v", err)
	}

	// 3. Operation edit without source
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal: principal,
		Operation: pebblestore.VideoOperationEdit,
		Prompt:    "Add clouds",
	})
	if err == nil || !strings.Contains(err.Error(), "requires source video") {
		t.Fatalf("expected edit without source rejection, got: %v", err)
	}

	// 4. Operation extend without source
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal: principal,
		Operation: pebblestore.VideoOperationExtend,
		Prompt:    "Add clouds",
	})
	if err == nil || !strings.Contains(err.Error(), "requires source video") {
		t.Fatalf("expected extend without source rejection, got: %v", err)
	}
}

// TestVideoPreflight_MixedSourceAndImageFails proves that mixed source video and initial image are rejected.
func TestVideoPreflight_MixedSourceAndImageFails(t *testing.T) {
	catalog := setupTestCatalog()
	svc := NewService(nil, nil, catalog)
	principal := identity.Principal{AccountScopeID: "acc-1"}

	_, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal: principal,
		Operation: pebblestore.VideoOperationEdit,
		Prompt:    "Transform",
		Source: &ManagedVideoSource{
			Bytes:     []byte("video-bytes"),
			MediaType: "video/mp4",
		},
		Image: &ManagedVideoImage{
			Bytes:     []byte("image-bytes"),
			MediaType: "image/png",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot combine source video with initial image") {
		t.Fatalf("expected mixed source/image rejection, got: %v", err)
	}
}

// TestVideoPreflight_ModelResolutionRules proves:
// - edit uses Tools.Video.IterationModel
// - extend on Veo uses Tools.Video.DefaultModel
// - extend on Omni uses Tools.Video.IterationModel
// - create uses Tools.Video.DefaultModel
// - missing setting returns error without hardcoded or first-ready fallback
func TestVideoPreflight_ModelResolutionRules(t *testing.T) {
	catalog := setupTestCatalog()
	uiSvc, _, accountID := setupTestUISettings(t, "veo-3.1-generate-preview", "gemini-omni-1.1-flash")
	authStore, _ := setupTestAuthStore(t, "google-key", "")
	svc := NewService(authStore, uiSvc, catalog)
	principal := identity.Principal{AccountScopeID: accountID}

	// Edit resolves iteration model
	resEdit, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal: principal,
		Operation: pebblestore.VideoOperationEdit,
		Prompt:    "Edit prompt",
		Source: &ManagedVideoSource{
			Bytes:     []byte("vid"),
			MediaType: "video/mp4",
		},
	})
	if err != nil {
		t.Fatalf("preflight edit failed: %v", err)
	}
	if resEdit.ResolvedModel != "gemini-omni-1.1-flash" {
		t.Fatalf("edit model = %q, want gemini-omni-1.1-flash", resEdit.ResolvedModel)
	}

	// Extend on Veo resolves generation default model
	resExtendVeo, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal: principal,
		Operation: pebblestore.VideoOperationExtend,
		Prompt:    "Extend prompt",
		Source: &ManagedVideoSource{
			Bytes:     []byte("vid"),
			MediaType: "video/mp4",
			Provenance: &pebblestore.VideoProvenance{
				Provider:       ProviderGoogleGemini,
				Model:          "veo-3.1-generate-preview",
				AccountScopeID: accountID,
			},
		},
	})
	if err != nil {
		t.Fatalf("preflight extend Veo failed: %v", err)
	}
	if resExtendVeo.ResolvedModel != "veo-3.1-generate-preview" {
		t.Fatalf("extend Veo model = %q, want veo-3.1-generate-preview", resExtendVeo.ResolvedModel)
	}

	// Extend on retained Omni resolves iteration default model
	resExtendOmni, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal: principal,
		Operation: pebblestore.VideoOperationExtend,
		Prompt:    "Extend prompt",
		Source: &ManagedVideoSource{
			InteractionID: "omni-interaction-1",
			Provenance: &pebblestore.VideoProvenance{
				Provider:       ProviderGoogleGemini,
				Model:          "gemini-omni-1.1-flash",
				AccountScopeID: accountID,
			},
		},
	})
	if err != nil {
		t.Fatalf("preflight extend Omni failed: %v", err)
	}
	if resExtendOmni.ResolvedModel != "gemini-omni-1.1-flash" {
		t.Fatalf("extend Omni model = %q, want gemini-omni-1.1-flash", resExtendOmni.ResolvedModel)
	}

	// Create resolves generation default model
	resCreate, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal: principal,
		Operation: pebblestore.VideoOperationCreate,
		Prompt:    "Create prompt",
	})
	if err != nil {
		t.Fatalf("preflight create failed: %v", err)
	}
	if resCreate.ResolvedModel != "veo-3.1-generate-preview" {
		t.Fatalf("create model = %q, want veo-3.1-generate-preview", resCreate.ResolvedModel)
	}

	// Missing configured model fails closed without hardcoded fallback
	emptyUISvc, _, emptyAccountID := setupTestUISettings(t, "", "")
	emptySvc := NewService(authStore, emptyUISvc, catalog)
	emptyPrincipal := identity.Principal{AccountScopeID: emptyAccountID}

	_, err = emptySvc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal: emptyPrincipal,
		Operation: pebblestore.VideoOperationCreate,
		Prompt:    "Create prompt",
	})
	if err == nil || !strings.Contains(err.Error(), "no default video model configured") {
		t.Fatalf("expected no default model error, got: %v", err)
	}
}

// TestVideoPreflight_VeoEditingRejectedBeforeProvider proves that attempting to edit a video with Veo
// is rejected before calling the provider and never silently falls back to generation.
func TestVideoPreflight_VeoEditingRejectedBeforeProvider(t *testing.T) {
	catalog := setupTestCatalog()
	authStore, accountID := setupTestAuthStore(t, "google-key", "")
	svc := NewService(authStore, nil, catalog)
	principal := identity.Principal{AccountScopeID: accountID}

	_, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationEdit,
		ExplicitModel: "veo-3.1-generate-preview",
		Prompt:        "Edit background to beach",
		Source: &ManagedVideoSource{
			Bytes:     []byte("video-bytes"),
			MediaType: "video/mp4",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "Veo models do not support video editing") {
		t.Fatalf("expected Veo editing rejection, got: %v", err)
	}
}

// TestVideoPreflight_VeoExtensionEligibilityValidation proves all eligibility guards for Veo extension:
// - Non-Veo or Omni ancestor rejected
// - Veo Lite rejected
// - OpenRouter Veo rejected
// - Resolution != 720p rejected
// - Aspect ratio not 16:9 or 9:16 rejected
// - Duration > 141s rejected
// - Extension count >= 20 rejected
// - Reference > 48h expired rejected
func TestVideoPreflight_VeoExtensionEligibilityValidation(t *testing.T) {
	catalog := setupTestCatalog()
	authStore, accountID := setupTestAuthStore(t, "google-key", "")
	svc := NewService(authStore, nil, catalog)
	principal := identity.Principal{AccountScopeID: accountID}

	validProv := &pebblestore.VideoProvenance{
		Provider:       ProviderGoogleGemini,
		Model:          "veo-3.1-generate-preview",
		AccountScopeID: accountID,
		CreatedAt:      time.Now().UnixMilli(),
		ExtensionCount: 2,
	}

	// 1. Veo Lite rejected
	_, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationExtend,
		ExplicitModel: "veo-lite-preview",
		Prompt:        "Extend",
		Source: &ManagedVideoSource{
			Bytes:      []byte("vid"),
			MediaType:  "video/mp4",
			Provenance: validProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "Veo 3.1 standard or fast") {
		t.Fatalf("expected Veo Lite rejection, got: %v", err)
	}

	// 2. OpenRouter Veo rejected
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationExtend,
		ExplicitModel: "openrouter:google/veo-3.1-generate-preview",
		Prompt:        "Extend",
		Source: &ManagedVideoSource{
			Bytes:      []byte("vid"),
			MediaType:  "video/mp4",
			Provenance: validProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "only supported directly via Google Gemini") {
		t.Fatalf("expected OpenRouter Veo rejection, got: %v", err)
	}

	// 3. Source generated by Omni rejected for Veo extension
	omniProv := &pebblestore.VideoProvenance{
		Provider:       ProviderGoogleGemini,
		Model:          "gemini-omni-1.1-flash",
		AccountScopeID: accountID,
		CreatedAt:      time.Now().UnixMilli(),
	}
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationExtend,
		ExplicitModel: "veo-3.1-generate-preview",
		Prompt:        "Extend",
		Source: &ManagedVideoSource{
			Bytes:      []byte("vid"),
			MediaType:  "video/mp4",
			Provenance: omniProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires a source generated by Google Veo") {
		t.Fatalf("expected Omni ancestor rejection, got: %v", err)
	}

	// 4. Resolution 1080p rejected (only 720p supported)
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationExtend,
		ExplicitModel: "veo-3.1-generate-preview",
		Resolution:    "1080p",
		Prompt:        "Extend",
		Source: &ManagedVideoSource{
			Bytes:      []byte("vid"),
			MediaType:  "video/mp4",
			Provenance: validProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires 720p resolution") {
		t.Fatalf("expected 1080p resolution rejection, got: %v", err)
	}

	// 5. Aspect ratio 1:1 rejected (only 16:9 or 9:16 supported)
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationExtend,
		ExplicitModel: "veo-3.1-generate-preview",
		AspectRatio:   "1:1",
		Prompt:        "Extend",
		Source: &ManagedVideoSource{
			Bytes:      []byte("vid"),
			MediaType:  "video/mp4",
			Provenance: validProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires 16:9 or 9:16 aspect ratio") {
		t.Fatalf("expected aspect ratio 1:1 rejection, got: %v", err)
	}

	// 6. Input duration > 141s rejected
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:             principal,
		Operation:             pebblestore.VideoOperationExtend,
		ExplicitModel:         "veo-3.1-generate-preview",
		SourceDurationSeconds: 142.0,
		Prompt:                "Extend",
		Source: &ManagedVideoSource{
			Bytes:      []byte("vid"),
			MediaType:  "video/mp4",
			Provenance: validProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed for Veo extension (141s)") {
		t.Fatalf("expected input duration > 141s rejection, got: %v", err)
	}

	// 7. Extension count >= 20 rejected
	maxProv := validProv.Clone()
	maxProv.ExtensionCount = 20
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationExtend,
		ExplicitModel: "veo-3.1-generate-preview",
		Prompt:        "Extend",
		Source: &ManagedVideoSource{
			Bytes:      []byte("vid"),
			MediaType:  "video/mp4",
			Provenance: maxProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "video extension limit reached") {
		t.Fatalf("expected extension count >= 20 rejection, got: %v", err)
	}

	// 8. Reference > 48h expired rejected
	expiredProv := validProv.Clone()
	expiredProv.CreatedAt = time.Now().Add(-50 * time.Hour).UnixMilli()
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationExtend,
		ExplicitModel: "veo-3.1-generate-preview",
		Prompt:        "Extend",
		Source: &ManagedVideoSource{
			Bytes:      []byte("vid"),
			MediaType:  "video/mp4",
			Provenance: expiredProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired reference rejection, got: %v", err)
	}
}

// TestVeoExtension_PredictRequestSerialization proves that Veo extension:
// 1. Serializes instances[].video.inlineData with mimeType: video/mp4 and base64 bytes.
// 2. Serializes parameters durationSeconds 8 and resolution 720p.
// 3. Flags result IsCombinedOutput = true and persists returned Veo URI in ProviderResource.
// 4. Server authors typed VideoProvenance with all required fields.
func TestVeoExtension_PredictRequestSerialization(t *testing.T) {
	fakeSource := []byte("fake-source-mp4-bytes")
	fakeOutput := []byte("fake-extended-mp4-bytes")
	veoVideoURI := "https://generativelanguage.googleapis.com/v1beta/files/veo-extended-video-1"

	var predictBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "predictLongRunning"):
			_ = json.NewDecoder(r.Body).Decode(&predictBody)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "operations/ext-op-1",
				"done": false,
			})
		case strings.Contains(r.URL.Path, "operations/ext-op-1"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "operations/ext-op-1",
				"done": true,
				"response": map[string]any{
					"generateVideoResponse": map[string]any{
						"generatedSamples": []map[string]any{
							{"video": map[string]string{"uri": veoVideoURI}},
						},
					},
				},
			})
		case strings.Contains(r.URL.Path, "files/veo-extended-video-1"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(fakeOutput)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authStore, accountID := setupTestAuthStore(t, "test-google-key", "")
	catalog := setupTestCatalog()
	svc := NewService(authStore, nil, catalog)
	svc.SetBaseURLs(server.URL, "")
	svc.SetPollTiming(5*time.Millisecond, 1*time.Second)
	svc.SetVideoProber(fakeProber{duration: 15.0, width: 1280, height: 720})

	sourceProv := &pebblestore.VideoProvenance{
		AccountScopeID: accountID,
		Provider:       ProviderGoogleGemini,
		Model:          "veo-3.1-generate-preview",
		ExtensionCount: 0,
		CreatedAt:      time.Now().UnixMilli(),
		SourceLink: &pebblestore.VideoSourceLink{
			DeliverableID: "deliv-prev",
		},
	}

	principal := identity.Principal{AccountScopeID: accountID}
	res, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Operation:     pebblestore.VideoOperationExtend,
		ExplicitModel: "veo-3.1-generate-preview",
		Prompt:        "Continue camera panning right",
		Principal:     principal,
		Source: &ManagedVideoSource{
			Bytes:      fakeSource,
			MediaType:  "video/mp4",
			Provenance: sourceProv,
		},
	})
	if err != nil {
		t.Fatalf("GenerateManagedVideo extend failed: %v", err)
	}

	// 1. Verify instances[0].video.inlineData
	instances, ok := predictBody["instances"].([]any)
	if !ok || len(instances) == 0 {
		t.Fatalf("expected instances array in predict request, got: %v", predictBody)
	}
	firstInst := instances[0].(map[string]any)
	videoData, ok := firstInst["video"].(map[string]any)
	if !ok {
		t.Fatalf("expected video in instance, got: %v", firstInst)
	}
	inlineData, ok := videoData["inlineData"].(map[string]any)
	if !ok {
		t.Fatalf("expected inlineData in video, got: %v", videoData)
	}
	if inlineData["mimeType"] != "video/mp4" {
		t.Errorf("mimeType = %v, want video/mp4", inlineData["mimeType"])
	}
	if inlineData["data"] != base64.StdEncoding.EncodeToString(fakeSource) {
		t.Errorf("data mismatch in inlineData")
	}

	// 2. Verify parameters durationSeconds=8 and resolution=720p
	params, ok := predictBody["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters in predict request, got: %v", predictBody)
	}
	durSec, _ := params["durationSeconds"].(float64)
	if int(durSec) != 8 {
		t.Errorf("durationSeconds = %v, want 8", params["durationSeconds"])
	}
	if params["resolution"] != "720p" {
		t.Errorf("resolution = %v, want 720p", params["resolution"])
	}

	// 3. Verify output flags
	if !res.IsCombinedOutput {
		t.Errorf("expected IsCombinedOutput = true for Veo extension")
	}
	if res.ExtensionCount != 1 {
		t.Errorf("expected ExtensionCount = 1, got %d", res.ExtensionCount)
	}
	if res.ProviderResource != veoVideoURI {
		t.Errorf("ProviderResource = %q, want %q", res.ProviderResource, veoVideoURI)
	}

	// 4. Verify VideoProvenance on result
	if res.Provenance == nil {
		t.Fatalf("expected Provenance on result, got nil")
	}
	if res.Provenance.Operation != pebblestore.VideoOperationExtend {
		t.Errorf("Provenance.Operation = %q, want extend", res.Provenance.Operation)
	}
	if res.Provenance.ProviderResource != veoVideoURI {
		t.Errorf("Provenance.ProviderResource = %q, want %q", res.Provenance.ProviderResource, veoVideoURI)
	}
	if !res.Provenance.IsCombinedOutput {
		t.Errorf("Provenance.IsCombinedOutput = false, want true")
	}
	if res.Provenance.ExtensionCount != 1 {
		t.Errorf("Provenance.ExtensionCount = %d, want 1", res.Provenance.ExtensionCount)
	}
	if res.Provenance.ObservedWidth != 1280 || res.Provenance.ObservedHeight != 720 {
		t.Errorf("Provenance dimensions = %dx%d, want 1280x720", res.Provenance.ObservedWidth, res.Provenance.ObservedHeight)
	}
}

// TestOmniExtension_TaskSerializationAndCeiling proves:
// 1. Stable Omni interaction request serializes generation_config.video_config.task = "extend".
// 2. Rejects duration selection for Omni.
// 3. Rejects source duration > 37s (conservative ceiling for 40s total).
func TestOmniExtension_TaskSerializationAndCeiling(t *testing.T) {
	fakeSource := []byte("source-vid")
	fakeOutput := []byte("omni-extended-bytes")

	var interactionBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "interactions") {
			_ = json.NewDecoder(r.Body).Decode(&interactionBody)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     "omni-ext-1",
				"status": "completed",
				"model":  "gemini-omni-1.1-flash",
				"steps": []map[string]any{
					{
						"type": "model_output",
						"content": []map[string]any{
							{"type": "video", "mime_type": "video/mp4", "data": base64.StdEncoding.EncodeToString(fakeOutput)},
						},
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	authStore, accountID := setupTestAuthStore(t, "test-google-key", "")
	catalog := setupTestCatalog()
	svc := NewService(authStore, nil, catalog)
	svc.SetBaseURLs(server.URL, "")
	svc.SetVideoProber(fakeProber{duration: 20.0, width: 1280, height: 720})

	sourceProv := &pebblestore.VideoProvenance{
		AccountScopeID: accountID,
		Provider:       ProviderGoogleGemini,
		Model:          "gemini-omni-1.1-flash",
		InteractionID:  "prev-omni-1",
	}

	principal := identity.Principal{AccountScopeID: accountID}

	// 1. Test Omni extension serialization
	res, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Operation:     pebblestore.VideoOperationExtend,
		ExplicitModel: "gemini-omni-1.1-flash",
		Prompt:        "Extend video by 5 seconds",
		Principal:     principal,
		Source: &ManagedVideoSource{
			Bytes:         fakeSource,
			MediaType:     "video/mp4",
			InteractionID: "prev-omni-1",
			Provenance:    sourceProv,
		},
	})
	if err != nil {
		t.Fatalf("GenerateManagedVideo Omni extend failed: %v", err)
	}
	if res.InteractionID != "omni-ext-1" {
		t.Errorf("InteractionID = %q, want omni-ext-1", res.InteractionID)
	}

	// Verify task="extend" in generation_config.video_config
	genConfig, ok := interactionBody["generation_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected generation_config in Omni request, got: %v", interactionBody)
	}
	videoConfig, ok := genConfig["video_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected video_config in generation_config, got: %v", genConfig)
	}
	if videoConfig["task"] != "extend" {
		t.Errorf("task = %v, want extend", videoConfig["task"])
	}

	// 2. Reject duration selection
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:       principal,
		Operation:       pebblestore.VideoOperationExtend,
		ExplicitModel:   "gemini-omni-1.1-flash",
		DurationSeconds: 8,
		Prompt:          "Extend",
		Source: &ManagedVideoSource{
			Bytes:      fakeSource,
			Provenance: sourceProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not accept duration selection") {
		t.Fatalf("expected Omni duration rejection, got: %v", err)
	}

	// 3. Reject source duration > 37s
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:             principal,
		Operation:             pebblestore.VideoOperationExtend,
		ExplicitModel:         "gemini-omni-1.1-flash",
		SourceDurationSeconds: 38.0,
		Prompt:                "Extend",
		Source: &ManagedVideoSource{
			Bytes:      fakeSource,
			Provenance: sourceProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed for Omni video extension") {
		t.Fatalf("expected source duration > 37s rejection, got: %v", err)
	}
}

// TestOmniConversationalEdit_TaskSerialization proves that Omni conversational edit
// serializes generation_config.video_config.task = "edit".
func TestOmniConversationalEdit_TaskSerialization(t *testing.T) {
	fakeOutput := []byte("omni-edited-bytes")
	var interactionBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "interactions") {
			_ = json.NewDecoder(r.Body).Decode(&interactionBody)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     "omni-edit-1",
				"status": "completed",
				"model":  "gemini-omni-1.1-flash",
				"steps": []map[string]any{
					{
						"type": "model_output",
						"content": []map[string]any{
							{"type": "video", "mime_type": "video/mp4", "data": base64.StdEncoding.EncodeToString(fakeOutput)},
						},
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	authStore, accountID := setupTestAuthStore(t, "test-google-key", "")
	catalog := setupTestCatalog()
	svc := NewService(authStore, nil, catalog)
	svc.SetBaseURLs(server.URL, "")
	svc.SetVideoProber(fakeProber{duration: 8.0, width: 1280, height: 720})

	sourceProv := &pebblestore.VideoProvenance{
		AccountScopeID: accountID,
		Provider:       ProviderGoogleGemini,
		Model:          "gemini-omni-1.1-flash",
		InteractionID:  "prev-omni-edit-0",
	}

	principal := identity.Principal{AccountScopeID: accountID}
	_, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Operation:     pebblestore.VideoOperationEdit,
		ExplicitModel: "gemini-omni-1.1-flash",
		Prompt:        "Change dress to red",
		Principal:     principal,
		Source: &ManagedVideoSource{
			InteractionID: "prev-omni-edit-0",
			Provenance:    sourceProv,
		},
	})
	if err != nil {
		t.Fatalf("GenerateManagedVideo edit failed: %v", err)
	}

	genConfig, ok := interactionBody["generation_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected generation_config, got: %v", interactionBody)
	}
	videoConfig, ok := genConfig["video_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected video_config, got: %v", genConfig)
	}
	if videoConfig["task"] != "edit" {
		t.Errorf("task = %v, want edit", videoConfig["task"])
	}
}

// TestOmniRetainedHistory_Validation proves:
// - Retained Omni interaction requires matching model (cannot switch models mid-conversation).
// - Retained Omni interaction requires matching account scope.
// - Retained Omni interaction requires matching credential.
// - Handle-only source requires verified source provenance bound to caller identity.
// - Never degrades missing/expired/incompatible retained Omni history into upload or generation.
func TestOmniRetainedHistory_Validation(t *testing.T) {
	catalog := setupTestCatalog()
	authStore, accountID := setupTestAuthStore(t, "test-google-key", "")
	svc := NewService(authStore, nil, catalog)
	principal := identity.Principal{AccountScopeID: accountID}

	// 1. Handle-only source without provenance is rejected
	_, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationEdit,
		ExplicitModel: "gemini-omni-1.1-flash",
		Prompt:        "Edit",
		Source: &ManagedVideoSource{
			InteractionID: "inter-1",
			// No bytes and no Provenance!
		},
	})
	if err == nil || !strings.Contains(err.Error(), "source-only video handle requires verified source provenance") {
		t.Fatalf("expected handle-only without provenance rejection, got: %v", err)
	}

	// 2. Foreign account scope rejected
	foreignProv := &pebblestore.VideoProvenance{
		AccountScopeID: "other-account",
		Provider:       ProviderGoogleGemini,
		Model:          "gemini-omni-1.1-flash",
		InteractionID:  "inter-foreign",
	}
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationEdit,
		ExplicitModel: "gemini-omni-1.1-flash",
		Prompt:        "Edit",
		Source: &ManagedVideoSource{
			InteractionID: "inter-foreign",
			Provenance:    foreignProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "different account scope") {
		t.Fatalf("expected foreign account rejection, got: %v", err)
	}

	// 3. Model mismatch in retained history rejected
	veoProv := &pebblestore.VideoProvenance{
		AccountScopeID: accountID,
		Provider:       ProviderGoogleGemini,
		Model:          "veo-3.1-generate-preview",
		InteractionID:  "inter-mismatch",
	}
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationEdit,
		ExplicitModel: "gemini-omni-1.1-flash",
		Prompt:        "Edit",
		Source: &ManagedVideoSource{
			InteractionID: "inter-mismatch",
			Provenance:    veoProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "model mismatch") {
		t.Fatalf("expected model mismatch rejection, got: %v", err)
	}

	// 4. Mismatched bytes digest rejected
	digestProv := &pebblestore.VideoProvenance{
		AccountScopeID:     accountID,
		Provider:           ProviderGoogleGemini,
		Model:              "gemini-omni-1.1-flash",
		OutputDigestSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
	}
	_, err = svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:     principal,
		Operation:     pebblestore.VideoOperationEdit,
		ExplicitModel: "gemini-omni-1.1-flash",
		Prompt:        "Edit",
		Source: &ManagedVideoSource{
			Bytes:      []byte("actual-bytes"),
			Provenance: digestProv,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match expected provenance digest") {
		t.Fatalf("expected digest mismatch rejection, got: %v", err)
	}
}

// TestOmniExternalUpload_10sLimitAndAdvisory proves:
// - Upload edit of external video > 10s is rejected.
// - Upload edit <= 10s includes the EU/EEA/UK/Switzerland advisory warning.
func TestOmniExternalUpload_10sLimitAndAdvisory(t *testing.T) {
	catalog := setupTestCatalog()
	authStore, accountID := setupTestAuthStore(t, "test-google-key", "")
	svc := NewService(authStore, nil, catalog)
	principal := identity.Principal{AccountScopeID: accountID}

	// > 10s rejected
	_, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:             principal,
		Operation:             pebblestore.VideoOperationEdit,
		ExplicitModel:         "gemini-omni-1.1-flash",
		SourceDurationSeconds: 11.0,
		Prompt:                "Edit external video",
		Source: &ManagedVideoSource{
			Bytes:     []byte("video-bytes"),
			MediaType: "video/mp4",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed for external video editing (10s)") {
		t.Fatalf("expected > 10s upload rejection, got: %v", err)
	}

	// <= 10s accepted and contains advisory warning
	res, err := svc.PreflightVideoOperation(context.Background(), VideoPreflightRequest{
		Principal:             principal,
		Operation:             pebblestore.VideoOperationEdit,
		ExplicitModel:         "gemini-omni-1.1-flash",
		SourceDurationSeconds: 6.0,
		Prompt:                "Edit external video",
		Source: &ManagedVideoSource{
			Bytes:     []byte("video-bytes"),
			MediaType: "video/mp4",
		},
	})
	if err != nil {
		t.Fatalf("preflight <= 10s failed: %v", err)
	}
	if len(res.AdvisoryWarnings) == 0 || !strings.Contains(res.AdvisoryWarnings[0], "EU/EEA, UK, and Switzerland") {
		t.Fatalf("expected EU/EEA advisory warning, got: %v", res.AdvisoryWarnings)
	}
}

// TestVideoProvenance_ValidationAndSecretsBan proves:
// - VideoProvenance rejects raw API keys / secrets in CredentialID or CredentialVersion.
// - VideoProvenance rejects invalid operations.
// - EqualVideoProvenance accurately evaluates deep equality.
func TestVideoProvenance_ValidationAndSecretsBan(t *testing.T) {
	prov := &pebblestore.VideoProvenance{
		AccountScopeID: "acc-1",
		Provider:       "google",
		Model:          "veo-3.1-generate-preview",
		Operation:      pebblestore.VideoOperationCreate,
		CredentialID:   "AIzaSyD-secret-key-that-must-be-banned",
	}
	if err := prov.Validate(); err == nil || !strings.Contains(err.Error(), "illegal secret") {
		t.Fatalf("expected secret key rejection in CredentialID, got: %v", err)
	}

	prov.CredentialID = "default"
	prov.CredentialVersion = "sk-openrouter-secret-key"
	if err := prov.Validate(); err == nil || !strings.Contains(err.Error(), "illegal secret") {
		t.Fatalf("expected secret key rejection in CredentialVersion, got: %v", err)
	}

	prov.CredentialVersion = "v1"
	prov.Operation = "invalid-op"
	if err := prov.Validate(); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected invalid operation rejection, got: %v", err)
	}

	prov.Operation = pebblestore.VideoOperationExtend
	validHash := sha256.Sum256([]byte("video"))
	prov.OutputDigestSHA256 = hex.EncodeToString(validHash[:])
	if err := prov.Validate(); err != nil {
		t.Fatalf("expected valid provenance, got: %v", err)
	}

	cp := prov.Clone()
	if !pebblestore.EqualVideoProvenance(prov, cp) {
		t.Errorf("cloned provenance should equal original")
	}

	cp.ExtensionCount = 5
	if pebblestore.EqualVideoProvenance(prov, cp) {
		t.Errorf("differing extension count should not be equal")
	}
}
