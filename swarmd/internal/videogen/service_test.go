package videogen

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func setupTestAuthStore(t *testing.T, googleKey, openRouterKey string) (*pebblestore.AuthStore, string) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "auth.pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	authStore := pebblestore.NewAuthStore(store)
	accountScopeID := "acc-test"

	if googleKey != "" {
		if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{
			ID:             "cred-google",
			AccountScopeID: accountScopeID,
			Provider:       "google",
			Type:           "api_key",
			APIKey:         googleKey,
		}); err != nil {
			t.Fatalf("upsert google cred: %v", err)
		}
		if _, err := authStore.SetActiveCredentialForAccount(accountScopeID, "google", "cred-google"); err != nil {
			t.Fatalf("set active google: %v", err)
		}
	}

	if openRouterKey != "" {
		if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{
			ID:             "cred-openrouter",
			AccountScopeID: accountScopeID,
			Provider:       "openrouter",
			Type:           "api_key",
			APIKey:         openRouterKey,
		}); err != nil {
			t.Fatalf("upsert openrouter cred: %v", err)
		}
		if _, err := authStore.SetActiveCredentialForAccount(accountScopeID, "openrouter", "cred-openrouter"); err != nil {
			t.Fatalf("set active openrouter: %v", err)
		}
	}

	return authStore, accountScopeID
}

func TestGenerateGoogleVeoVideo(t *testing.T) {
	fakeMP4 := []byte("fake-veo-mp4-video-bytes")

	var predictCalled, pollCalled, downloadCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case stringsContains(r.URL.Path, "predictLongRunning"):
			predictCalled = true
			if r.Header.Get("x-goog-api-key") != "test-google-key" {
				http.Error(w, "bad key", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "operations/op-123",
				"done": false,
			})
		case stringsContains(r.URL.Path, "operations/op-123"):
			pollCalled = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "operations/op-123",
				"done": true,
				"response": map[string]any{
					"generateVideoResponse": map[string]any{
						"generatedSamples": []map[string]any{
							{"video": map[string]string{"uri": "/download/video-123.mp4"}},
						},
					},
				},
			})
		case stringsContains(r.URL.Path, "download/video-123.mp4"):
			downloadCalled = true
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(fakeMP4)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key", "")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURLs(server.URL, "")
	svc.SetPollTiming(10*time.Millisecond, 2*time.Second)
	svc.SetVideoProber(fakeProber{duration: 8.0, width: 1280, height: 720})

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Prompt:    "A drone shot over redwood trees",
		Principal: principal,
	})
	if err != nil {
		t.Fatalf("GenerateManagedVideo failed: %v", err)
	}

	if !predictCalled || !pollCalled || !downloadCalled {
		t.Fatalf("expected predict, poll, and download calls; got predict=%v poll=%v download=%v", predictCalled, pollCalled, downloadCalled)
	}
	if string(res.Bytes) != string(fakeMP4) {
		t.Fatalf("video bytes mismatch: got %q", string(res.Bytes))
	}
	if res.Model != DefaultVideoGenerationModel {
		t.Fatalf("model = %q, want %q", res.Model, DefaultVideoGenerationModel)
	}
	if res.Provider != ProviderGoogleGemini {
		t.Fatalf("provider = %q, want %q", res.Provider, ProviderGoogleGemini)
	}
	if res.PriceStatus != "unknown" {
		t.Fatalf("price_status = %q, want unknown", res.PriceStatus)
	}
	if res.EstimatedCostUSD != 0.0 {
		t.Fatalf("estimated_cost_usd = %f, want 0.0", res.EstimatedCostUSD)
	}
}

func TestGenerateGoogleOmniInitialAndConversationalEdit(t *testing.T) {
	fakeMP4Turn1 := []byte("turn-1-video-bytes")
	fakeMP4Turn2 := []byte("turn-2-edited-video-bytes")

	var turn1Called, turn2Called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !stringsContains(r.URL.Path, "/interactions") {
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)

		prevID, hasPrev := req["previous_interaction_id"].(string)
		w.Header().Set("Content-Type", "application/json")

		if hasPrev && prevID == "v1_turn1" {
			turn2Called = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     "v1_turn2",
				"status": "completed",
				"model":  "gemini-omni-1.1-flash",
				"steps": []map[string]any{
					{
						"type": "model_output",
						"content": []map[string]any{
							{"type": "video", "mime_type": "video/mp4", "data": base64.StdEncoding.EncodeToString(fakeMP4Turn2)},
						},
					},
				},
			})
			return
		}

		turn1Called = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "v1_turn1",
			"status": "completed",
			"model":  "gemini-omni-1.1-flash",
			"steps": []map[string]any{
				{
					"type": "model_output",
					"content": []map[string]any{
						{"type": "video", "mime_type": "video/mp4", "data": base64.StdEncoding.EncodeToString(fakeMP4Turn1)},
					},
				},
			},
		})
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key", "")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURLs(server.URL, "")
	svc.SetVideoProber(fakeProber{duration: 8.0, width: 1280, height: 720})

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}

	// Turn 1: Initial generation with Omni explicitly requested
	res1, err := svc.generateGoogleOmni(context.Background(), "test-google-key", "gemini-omni-1.1-flash", "A violinist in the park", "16:9", "720p", pebblestore.VideoOperationCreate, nil, nil)
	if err != nil {
		t.Fatalf("Turn 1 failed: %v", err)
	}
	if !turn1Called || res1.InteractionID != "v1_turn1" {
		t.Fatalf("turn 1 failed: called=%v id=%q", turn1Called, res1.InteractionID)
	}
	if string(res1.Bytes) != string(fakeMP4Turn1) {
		t.Fatalf("turn 1 bytes mismatch")
	}

	// Turn 2: Conversational edit passing previous interaction ID
	h1 := sha256.Sum256(res1.Bytes)
	res1Prov := &pebblestore.VideoProvenance{
		AccountScopeID:      accountScopeID,
		CredentialID:        "cred-google",
		Provider:            ProviderGoogleGemini,
		Model:               "gemini-omni-1.1-flash",
		Transport:           pebblestore.VideoTransportGoogleInteractions,
		InteractionID:       res1.InteractionID,
		OutputDigestSHA256:  hex.EncodeToString(h1[:]),
		CreatedAt:           time.Now().UnixMilli(),
		ExpiresAt:           time.Now().Add(48 * time.Hour).UnixMilli(),
		ObservedDurationMs:  8000,
		ObservedWidth:       1280,
		ObservedHeight:      720,
		ExtensionCountKnown: true,
	}
	res2, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Operation: pebblestore.VideoOperationEdit,
		Prompt:    "Make the violin invisible",
		Principal: principal,
		Source: &ManagedVideoSource{
			Bytes:         res1.Bytes,
			MediaType:     "video/mp4",
			InteractionID: res1.InteractionID,
			Model:         "gemini-omni-1.1-flash",
			Provenance:    res1Prov,
		},
	})
	if err != nil {
		t.Fatalf("Turn 2 failed: %v", err)
	}
	if !turn2Called || res2.InteractionID != "v1_turn2" {
		t.Fatalf("turn 2 failed: called=%v id=%q", turn2Called, res2.InteractionID)
	}
	if string(res2.Bytes) != string(fakeMP4Turn2) {
		t.Fatalf("turn 2 bytes mismatch: got %q", string(res2.Bytes))
	}
}

func TestGenerateGoogleOmniBridgeEditFromExternalVideo(t *testing.T) {
	fakeSourceBytes := []byte("source-external-video-bytes")
	fakeOmniEdited := []byte("omni-edited-bridge-video-bytes")

	var uploadStarted, uploadFinalized, interactionCalled bool
	var uploadServerURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && stringsContains(r.URL.Path, "/upload/v1beta/files"):
			uploadStarted = true
			w.Header().Set("X-Goog-Upload-URL", uploadServerURL+"/upload-finalize-target")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && stringsContains(r.URL.Path, "/upload-finalize-target"):
			uploadFinalized = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file": map[string]any{
					"name":  "files/external-file-1",
					"uri":   "https://generativelanguage.googleapis.com/v1beta/files/external-file-1",
					"state": "ACTIVE",
				},
			})
		case r.Method == http.MethodPost && stringsContains(r.URL.Path, "/interactions"):
			interactionCalled = true
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			inputs, ok := req["input"].([]any)
			if !ok || len(inputs) < 2 {
				http.Error(w, "expected multimodal array input", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     "v1_bridge_res",
				"status": "completed",
				"model":  "gemini-omni-1.1-flash",
				"steps": []map[string]any{
					{
						"type": "model_output",
						"content": []map[string]any{
							{"type": "video", "mime_type": "video/mp4", "data": base64.StdEncoding.EncodeToString(fakeOmniEdited)},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	uploadServerURL = server.URL

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key", "")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURLs(server.URL, "")
	svc.SetVideoProber(fakeProber{duration: 8.0, width: 1280, height: 720})

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Operation: pebblestore.VideoOperationEdit,
		Prompt:    "Change lighting to sunset",
		Principal: principal,
		Source: &ManagedVideoSource{
			Bytes:         fakeSourceBytes,
			MediaType:     "video/mp4",
			InteractionID: "", // External source without Omni interaction ID!
			Model:         "veo-3.1-generate-preview",
		},
	})
	if err != nil {
		t.Fatalf("Bridge edit failed: %v", err)
	}

	if !uploadStarted || !uploadFinalized || !interactionCalled {
		t.Fatalf("expected upload start, finalize, and interaction; got start=%v finalize=%v interaction=%v", uploadStarted, uploadFinalized, interactionCalled)
	}
	if res.InteractionID != "v1_bridge_res" {
		t.Fatalf("expected interaction id v1_bridge_res, got %q", res.InteractionID)
	}
	if string(res.Bytes) != string(fakeOmniEdited) {
		t.Fatalf("edited video bytes mismatch: got %q", string(res.Bytes))
	}
}

func TestGenerateGoogleOmniContentBlockedDiagnostic(t *testing.T) {
	fakeSourceBytes := []byte("source-external-video-bytes")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && stringsContains(r.URL.Path, "/upload/v1beta/files"):
			w.Header().Set("X-Goog-Upload-URL", "http://"+r.Host+"/upload-finalize")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && stringsContains(r.URL.Path, "/upload-finalize"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file": map[string]any{
					"name":  "files/external-file-2",
					"uri":   "https://generativelanguage.googleapis.com/v1beta/files/external-file-2",
					"state": "ACTIVE",
				},
			})
		case r.Method == http.MethodPost && stringsContains(r.URL.Path, "/interactions"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    400,
					"message": "content_blocked",
					"status":  "INVALID_ARGUMENT",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key", "")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURLs(server.URL, "")
	svc.SetVideoProber(fakeProber{duration: 8.0, width: 1280, height: 720})

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	_, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Operation: pebblestore.VideoOperationEdit,
		Prompt:    "Change lighting to sunset",
		Principal: principal,
		Source: &ManagedVideoSource{
			Bytes:         fakeSourceBytes,
			MediaType:     "video/mp4",
			InteractionID: "", // External source without Omni interaction ID
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !stringsContains(err.Error(), "content_blocked") {
		t.Fatalf("expected content_blocked error from provider, got: %v", err)
	}
}

func TestGenerateOpenRouterVideo(t *testing.T) {
	fakeMP4 := []byte("openrouter-video-bytes")

	var postCalled, pollCalled, downloadCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && stringsContains(r.URL.Path, "/api/v1/videos"):
			postCalled = true
			if r.Header.Get("Authorization") != "Bearer test-or-key" {
				http.Error(w, "bad auth", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":          "job-or-1",
				"status":      "pending",
				"polling_url": "/api/v1/videos/job-or-1",
			})
		case r.Method == http.MethodGet && stringsContains(r.URL.Path, "/api/v1/videos/job-or-1"):
			pollCalled = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":            "job-or-1",
				"status":        "completed",
				"unsigned_urls": []string{"/download/or-video.mp4"},
			})
		case stringsContains(r.URL.Path, "download/or-video.mp4"):
			downloadCalled = true
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(fakeMP4)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authStore, _ := setupTestAuthStore(t, "", "test-or-key")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURLs("", server.URL)
	svc.SetPollTiming(10*time.Millisecond, 2*time.Second)

	res, err := svc.generateOpenRouter(context.Background(), "test-or-key", "google/veo-3.1", "A serene lake", "16:9", "720p", 8, nil)
	if err != nil {
		t.Fatalf("generateOpenRouter failed: %v", err)
	}
	if !postCalled || !pollCalled || !downloadCalled {
		t.Fatalf("expected all openrouter steps called; got post=%v poll=%v download=%v", postCalled, pollCalled, downloadCalled)
	}
	if string(res.Bytes) != string(fakeMP4) {
		t.Fatalf("bytes mismatch: %q", string(res.Bytes))
	}
	if res.Provider != ProviderOpenRouter {
		t.Fatalf("provider = %q, want %q", res.Provider, ProviderOpenRouter)
	}
}

func TestGenerateGoogleVeoVideoWithImageInput(t *testing.T) {
	fakeMP4 := []byte("fake-veo-image-video-bytes")
	var receivedImageB64, receivedMIME string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case stringsContains(r.URL.Path, "predictLongRunning"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			instances, _ := body["instances"].([]any)
			if len(instances) > 0 {
				inst := instances[0].(map[string]any)
				if imgMap, ok := inst["image"].(map[string]any); ok {
					receivedImageB64, _ = imgMap["bytesBase64Encoded"].(string)
					receivedMIME, _ = imgMap["mimeType"].(string)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "operations/op-img-1",
				"done": false,
			})
		case stringsContains(r.URL.Path, "operations/op-img-1"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "operations/op-img-1",
				"done": true,
				"response": map[string]any{
					"generateVideoResponse": map[string]any{
						"generatedSamples": []map[string]any{
							{"video": map[string]string{"uri": "/download/video-img.mp4"}},
						},
					},
				},
			})
		case stringsContains(r.URL.Path, "download/video-img.mp4"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(fakeMP4)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key", "")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURLs(server.URL, "")
	svc.SetPollTiming(10*time.Millisecond, 2*time.Second)
	svc.SetVideoProber(fakeProber{duration: 8.0, width: 1280, height: 720})

	rawPNG := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15c4\x00\x00\x00\rIDATx\x9cc`\x00\x00\x00\x02\x00\x01H\xaf\xa4q\x00\x00\x00\x00IEND\xaeB`\x82")
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Prompt:    "Animate this portrait",
		Principal: principal,
		Image: &ManagedVideoImage{
			Bytes:     rawPNG,
			MediaType: "image/png",
		},
	})
	if err != nil {
		t.Fatalf("GenerateManagedVideo with image failed: %v", err)
	}
	if string(res.Bytes) != string(fakeMP4) {
		t.Fatalf("bytes mismatch: %q", string(res.Bytes))
	}
	if receivedMIME != "image/png" {
		t.Fatalf("expected MIME image/png, got %q", receivedMIME)
	}
	if receivedImageB64 != base64.StdEncoding.EncodeToString(rawPNG) {
		t.Fatalf("image base64 mismatch")
	}

	// Test SVG input rejection with clear actionable error message
	_, svgErr := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Prompt:    "Animate SVG",
		Principal: principal,
		Image: &ManagedVideoImage{
			Bytes:     []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10"/></svg>`),
			MediaType: "image/svg+xml",
		},
	})
	if svgErr == nil || !stringsContains(svgErr.Error(), "vector SVG images must be rasterized to PNG or JPEG") {
		t.Fatalf("expected SVG rasterization error, got: %v", svgErr)
	}

	// Test SVG input auto-rasterization when SVGRasterizer is configured
	rasterizer := &fakeSVGRasterizer{rasterizedPNG: rawPNG}
	svc.SetSVGRasterizer(rasterizer)

	resWithRasterizer, errWithRasterizer := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Prompt:    "Animate SVG with rasterizer",
		Principal: principal,
		Image: &ManagedVideoImage{
			Bytes:     []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10"/></svg>`),
			MediaType: "image/svg+xml",
		},
	})
	if errWithRasterizer != nil {
		t.Fatalf("generate video with SVGRasterizer failed: %v", errWithRasterizer)
	}
	if !rasterizer.called {
		t.Fatal("expected SVGRasterizer to be called")
	}
	if len(resWithRasterizer.Bytes) == 0 {
		t.Fatal("expected non-empty video result bytes")
	}
	if receivedMIME != "image/png" {
		t.Fatalf("expected provider received MIME image/png, got %q", receivedMIME)
	}
	if receivedImageB64 != base64.StdEncoding.EncodeToString(rawPNG) {
		t.Fatalf("provider received image base64 mismatch")
	}
}

func TestGenerateGoogleOmniWithImageInput(t *testing.T) {
	fakeMP4 := []byte("fake-omni-image-video-bytes")
	rawPNG := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15c4\x00\x00\x00\rIDATx\x9cc`\x00\x00\x00\x02\x00\x01H\xaf\xa4q\x00\x00\x00\x00IEND\xaeB`\x82")
	var receivedImageB64, receivedMIME, receivedText string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !stringsContains(r.URL.Path, "/interactions") {
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		inputs, ok := req["input"].([]any)
		if ok {
			for _, item := range inputs {
				m := item.(map[string]any)
				if m["type"] == "image" {
					receivedImageB64, _ = m["data"].(string)
					receivedMIME, _ = m["mime_type"].(string)
				}
				if m["type"] == "text" {
					receivedText, _ = m["text"].(string)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "omni-img-1",
			"status": "completed",
			"model":  "gemini-omni-1.1-flash",
			"steps": []map[string]any{
				{
					"type": "model_output",
					"content": []map[string]any{
						{"type": "video", "mime_type": "video/mp4", "data": base64.StdEncoding.EncodeToString(fakeMP4)},
					},
				},
			},
		})
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key", "")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURLs(server.URL, "")
	svc.SetVideoProber(fakeProber{duration: 8.0, width: 1280, height: 720})

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.generateGoogleOmni(context.Background(), "test-google-key", "gemini-omni-1.1-flash", "Bring this image to life", "16:9", "720p", pebblestore.VideoOperationCreate, nil, &ManagedVideoImage{
		Bytes:     rawPNG,
		MediaType: "image/png",
	})
	if err != nil {
		t.Fatalf("generateGoogleOmni with image failed: %v", err)
	}
	if string(res.Bytes) != string(fakeMP4) {
		t.Fatalf("bytes mismatch: %q", string(res.Bytes))
	}
	if receivedText != "Bring this image to life" {
		t.Fatalf("text mismatch: %q", receivedText)
	}
	if receivedMIME != "image/png" {
		t.Fatalf("MIME mismatch: %q", receivedMIME)
	}
	if receivedImageB64 != base64.StdEncoding.EncodeToString(rawPNG) {
		t.Fatalf("base64 mismatch")
	}
	_ = principal
}

func TestGenerateOpenRouterWithImageInput(t *testing.T) {
	fakeMP4 := []byte("fake-openrouter-image-video-bytes")
	rawPNG := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15c4\x00\x00\x00\rIDATx\x9cc`\x00\x00\x00\x02\x00\x01H\xaf\xa4q\x00\x00\x00\x00IEND\xaeB`\x82")
	var receivedFrameURL, receivedFrameType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && stringsContains(r.URL.Path, "/api/v1/videos"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if frames, ok := body["frame_images"].([]any); ok && len(frames) > 0 {
				f := frames[0].(map[string]any)
				receivedFrameType, _ = f["frame_type"].(string)
				if imgURL, ok := f["image_url"].(map[string]any); ok {
					receivedFrameURL, _ = imgURL["url"].(string)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":          "job-or-img",
				"status":      "pending",
				"polling_url": "/api/v1/videos/job-or-img",
			})
		case r.Method == http.MethodGet && stringsContains(r.URL.Path, "/api/v1/videos/job-or-img"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":            "job-or-img",
				"status":        "completed",
				"unsigned_urls": []string{"/download/or-img.mp4"},
			})
		case stringsContains(r.URL.Path, "download/or-img.mp4"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(fakeMP4)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authStore, _ := setupTestAuthStore(t, "", "test-or-key")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURLs("", server.URL)
	svc.SetPollTiming(10*time.Millisecond, 2*time.Second)

	res, err := svc.generateOpenRouter(context.Background(), "test-or-key", "google/veo-3.1", "Slow pan across image", "16:9", "720p", 4, &ManagedVideoImage{
		Bytes:     rawPNG,
		MediaType: "image/png",
	})
	if err != nil {
		t.Fatalf("generateOpenRouter with image failed: %v", err)
	}
	if string(res.Bytes) != string(fakeMP4) {
		t.Fatalf("bytes mismatch: %q", string(res.Bytes))
	}
	if receivedFrameType != "first_frame" {
		t.Fatalf("expected frame_type first_frame, got %q", receivedFrameType)
	}
	expectedDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawPNG)
	if receivedFrameURL != expectedDataURI {
		t.Fatalf("expected data URI %q, got %q", expectedDataURI, receivedFrameURL)
	}
}

func stringsContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && stringSearch(s, substr)))
}

func stringSearch(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

type fakeSVGRasterizer struct {
	rasterizedPNG []byte
	called        bool
	err           error
}

func (f *fakeSVGRasterizer) RasterizeSVG(ctx context.Context, svgBytes []byte) ([]byte, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	return f.rasterizedPNG, nil
}

type fakeModelCatalog struct {
	records []pebblestore.ModelCatalogRecord
}

func (f *fakeModelCatalog) ListCatalog(providerID string, limit int) ([]pebblestore.ModelCatalogRecord, error) {
	return f.records, nil
}

// Requirement: GenerateManagedVideo must price effective request dimensions from the
// selected catalog record, not a guessed Veo rate. A mock HTTP adapter is the narrowest
// layer exercising normalization, generation, and pricing without paid provider calls.
func TestGenerateGoogleVeoVideoWithSnapshotCatalogPricing(t *testing.T) {
	fakeMP4 := []byte("fake-veo-mp4-video-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case stringsContains(r.URL.Path, "predictLongRunning"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "operations/op-snap-pricing",
				"done": false,
			})
		case stringsContains(r.URL.Path, "operations/op-snap-pricing"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "operations/op-snap-pricing",
				"done": true,
				"response": map[string]any{
					"generateVideoResponse": map[string]any{
						"generatedSamples": []any{
							map[string]any{
								"video": map[string]any{
									"uri": "/v1beta/files/snap-pricing-sample",
								},
							},
						},
					},
				},
			})
		case stringsContains(r.URL.Path, "files/snap-pricing-sample"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(fakeMP4)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key", "")
	catalog := &fakeModelCatalog{
		records: []pebblestore.ModelCatalogRecord{
			{
				Provider:              "google",
				Model:                 DefaultVideoGenerationModel,
				SourceSnapshotID:      "swarm-models-v1-c1ef3f604dea5bc9",
				SourceSnapshotVersion: "v1",
				Pricing: []byte(`{
					"currency": "USD",
					"billing": {
						"status": "verified",
						"lines": [
							{
								"billable": "video_output",
								"unit": "second",
								"price_usd": 0.05,
								"conditions": {
									"resolution": "720p",
									"includes_audio": true,
									"service_tier": "standard"
								}
							},
							{
								"billable": "video_output",
								"unit": "second",
								"price_usd": 0.08,
								"conditions": {
									"resolution": "1080p",
									"includes_audio": true,
									"service_tier": "standard"
								}
							}
						]
					}
				}`),
			},
		},
	}

	svc := NewService(authStore, nil, catalog)
	svc.SetBaseURLs(server.URL, "")
	svc.SetPollTiming(10*time.Millisecond, 2*time.Second)
	svc.SetVideoProber(fakeProber{duration: 8.0, width: 1280, height: 720})

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}

	// 1. Defaults (720p, 8s) => $0.40
	res720p, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Prompt:    "A drone shot over the ocean",
		Principal: principal,
	})
	if err != nil {
		t.Fatalf("GenerateManagedVideo 720p failed: %v", err)
	}
	if res720p.Resolution != "720p" {
		t.Fatalf("resolution = %q, want 720p", res720p.Resolution)
	}
	if res720p.DurationSeconds != 8 {
		t.Fatalf("duration_seconds = %d, want 8", res720p.DurationSeconds)
	}
	if res720p.PriceStatus != "known" {
		t.Fatalf("price_status = %q, want known", res720p.PriceStatus)
	}
	if math.Abs(res720p.EstimatedCostUSD-0.40) > 0.0001 {
		t.Fatalf("estimated_cost_usd = %f, want 0.40", res720p.EstimatedCostUSD)
	}
	if res720p.SnapshotID != "swarm-models-v1-c1ef3f604dea5bc9" {
		t.Fatalf("snapshot_id = %q, want swarm-models-v1-c1ef3f604dea5bc9", res720p.SnapshotID)
	}

	// 2. 1080p, 8s => $0.64
	res1080p, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Prompt:     "A drone shot over the mountains in high resolution",
		Resolution: "1080p",
		Principal:  principal,
	})
	if err != nil {
		t.Fatalf("GenerateManagedVideo 1080p failed: %v", err)
	}
	if res1080p.Resolution != "1080p" {
		t.Fatalf("resolution = %q, want 1080p", res1080p.Resolution)
	}
	if res1080p.PriceStatus != "known" {
		t.Fatalf("price_status = %q, want known", res1080p.PriceStatus)
	}
	if math.Abs(res1080p.EstimatedCostUSD-0.64) > 0.0001 {
		t.Fatalf("estimated_cost_usd = %f, want 0.64", res1080p.EstimatedCostUSD)
	}
}

func TestEstimateVideoCostNoInventedFallback(t *testing.T) {
	// Without catalog pricing: returns 0.0 and unknown summary, not $0.07/sec or $0.05
	cost, summary := EstimateVideoCost("google", "veo-3.1-generate-preview", 8, false, nil)
	if cost != 0.0 {
		t.Fatalf("expected 0.0 for unpriced video, got %f", cost)
	}
	if !strings.Contains(summary, "unknown") {
		t.Fatalf("expected unknown pricing summary, got %q", summary)
	}

	// With verified catalog pricing for 720p
	catalogPricing := []byte(`{
		"currency": "USD",
		"billing": {
			"status": "verified",
			"lines": [
				{
					"billable": "video_output",
					"unit": "second",
					"price_usd": 0.05,
					"conditions": {
						"resolution": "720p",
						"includes_audio": true,
						"service_tier": "standard"
					}
				}
			]
		}
	}`)
	cost720, summary720 := EstimateVideoCostWithResolution("google", "veo-3.1-generate-preview", 8, "720p", false, catalogPricing)
	if math.Abs(cost720-0.40) > 0.0001 {
		t.Fatalf("expected 0.40 for 8s 720p, got %f", cost720)
	}
	if !strings.Contains(summary720, "$0.050/sec") || !strings.Contains(summary720, "$0.40 for 8s") {
		t.Fatalf("unexpected summary: %q", summary720)
	}
}

// Requirement: Media's selected model and settings must reach the provider unchanged.
// Threat: an explicit choice silently bills a different default model or drops the source.
// Authority: GenerateManagedVideo, at the narrow service boundary before any provider I/O.
func TestManagedVideoExplicitSelectionRejectsInvalidWithoutProviderCall(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "unexpected provider call", http.StatusInternalServerError)
	}))
	defer server.Close()
	catalog := &fakeModelCatalog{records: []pebblestore.ModelCatalogRecord{{Provider: "google", Model: "selected-video", CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}}}}}
	svc := NewService(nil, nil, catalog)
	svc.SetBaseURLs(server.URL, server.URL)
	for _, req := range []ManagedVideoRequest{
		{Prompt: "revision", Model: "missing-model"},
		{Prompt: "revision", Model: "selected-video", AspectRatio: "32:9"},
		{Prompt: "revision", Model: "selected-video", Resolution: "8k"},
		{Prompt: "revision", Model: "selected-video", Source: &ManagedVideoSource{Bytes: []byte("source"), MediaType: "video/mp4"}},
	} {
		result, err := svc.GenerateManagedVideo(context.Background(), req)
		if err == nil || len(result.Bytes) != 0 {
			t.Fatalf("expected rejection without output: request=%+v err=%v", req, err)
		}
	}
	if calls != 0 {
		t.Fatalf("rejected settings reached provider %d times", calls)
	}
}

func TestGenerateManagedVideo_OmniOmitsDuration(t *testing.T) {
	// Requirement: Gemini Omni does not accept duration selection and its result must preserve duration omission (0s, not defaulted to 8s).
	// Threat/regression: Omni requests defaulting to 8s and reporting 8s duration.
	// Boundary/authority: Service.GenerateManagedVideo in videogen/service.go.
	// Test layer: Unit test with mock Omni HTTP server.

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key", "")
	fakeMP4 := []byte("fake-omni-mp4")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "omni-op-1",
			"status": "completed",
			"model":  "gemini-omni-1.1-flash",
			"steps": []map[string]any{
				{
					"type": "video",
					"content": []map[string]any{
						{
							"type":      "video",
							"mime_type": "video/mp4",
							"data":      base64.StdEncoding.EncodeToString(fakeMP4),
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	catalog := &fakeModelCatalog{records: []pebblestore.ModelCatalogRecord{
		{
			Provider: "google", Model: "gemini-omni-1.1-flash",
			CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}},
		},
	}}

	svc := NewService(authStore, nil, catalog)
	svc.SetBaseURLs(server.URL, "")
	svc.SetVideoProber(fakeProber{duration: 8.0, width: 1280, height: 720})
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}

	res, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Prompt:    "Animate the ocean waves",
		Model:     "gemini-omni-1.1-flash",
		Principal: principal,
	})
	if err != nil {
		t.Fatalf("GenerateManagedVideo for Omni failed: %v", err)
	}
	if res.DurationSeconds != 0 {
		t.Fatalf("expected Omni duration_seconds=0 (omitted), got %d", res.DurationSeconds)
	}
}

func TestGenerateManagedVideo_OmniRejectsDurationSelection(t *testing.T) {
	// Requirement: Gemini Omni does not support duration selection; explicit duration selection must be rejected before provider calls.
	// Threat/regression: Sending duration parameter to Omni which causes provider errors.
	// Boundary/authority: Service.GenerateManagedVideo in videogen/service.go.
	// Test layer: Unit test verifying rejection without provider calls.

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer server.Close()

	catalog := &fakeModelCatalog{records: []pebblestore.ModelCatalogRecord{
		{
			Provider: "google", Model: "gemini-omni-1.1-flash",
			CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}},
		},
	}}

	svc := NewService(nil, nil, catalog)
	svc.SetBaseURLs(server.URL, "")

	_, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Prompt:          "Animate with duration",
		Model:           "gemini-omni-1.1-flash",
		DurationSeconds: 8,
	})
	if err == nil {
		t.Fatalf("expected rejection when selecting duration for Omni model")
	}
	if !strings.Contains(err.Error(), "does not accept duration") {
		t.Fatalf("expected error mentioning duration, got: %v", err)
	}
	if calls != 0 {
		t.Fatalf("provider called %d times on invalid duration selection", calls)
	}
}

func TestGenerateManagedVideo_ProviderQualifiedIDs(t *testing.T) {
	// Requirement: Provider-qualified model IDs (e.g. google:veo-3.1-generate-preview or openrouter:google/veo-3.1)
	// must be parsed and routed to their exact respective providers.
	// Threat/regression: Provider prefix confusion causing model catalog lookup failure.
	// Boundary/authority: Service.GenerateManagedVideo in videogen/service.go.
	// Test layer: Unit test checking catalog lookup and resolution.

	authStore, accountScopeID := setupTestAuthStore(t, "google-k", "openrouter-k")
	catalog := &fakeModelCatalog{records: []pebblestore.ModelCatalogRecord{
		{
			Provider: "google", Model: "veo-3.1-generate-preview",
			CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}},
		},
		{
			Provider: "openrouter", Model: "google/veo-3.1",
			CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}},
		},
	}}

	svc := NewService(authStore, nil, catalog)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}

	// 1. google: prefix
	rec, found := svc.resolveModelRecord("google", "veo-3.1-generate-preview")
	if !found || rec.Model != "veo-3.1-generate-preview" {
		t.Fatalf("expected google:veo-3.1-generate-preview to resolve in catalog")
	}

	// 2. openrouter: prefix
	recOR, foundOR := svc.resolveModelRecord("openrouter", "google/veo-3.1")
	if !foundOR || recOR.Model != "google/veo-3.1" {
		t.Fatalf("expected openrouter:google/veo-3.1 to resolve in catalog")
	}
	_ = principal
}

func TestGenerateManagedVideo_1080pDurationRequires8s(t *testing.T) {
	// Requirement: Veo 1080p resolution requires 8s duration. Durations of 4s or 6s must be rejected before provider call.
	// Threat/regression: Invalid duration reaching provider or being silently converted.
	// Boundary/authority: Service.GenerateManagedVideo in videogen/service.go.
	// Test layer: Unit test verifying rejection without provider calls.

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer server.Close()

	veoProviderSpecific := json.RawMessage(`{
		"google": {
			"video_generation": {
				"settings": {
					"resolution": {
						"status": "verified",
						"default_value": "720p",
						"supported_values": ["720p", "1080p", "4k"],
						"variants": [
							{"mode": "all", "supported_values": ["1080p"], "conditions": {"duration_seconds": 8}}
						]
					},
					"duration_seconds": {
						"status": "verified",
						"default_value": 8,
						"supported_values": [4, 6, 8],
						"variants": [
							{"mode": "hi_res", "supported_values": [8], "conditions": {"resolution": "1080p_or_4k"}}
						]
					}
				}
			}
		}
	}`)
	catalog := &fakeModelCatalog{records: []pebblestore.ModelCatalogRecord{
		{
			Provider: "google", Model: "veo-3.1-generate-preview",
			CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}},
			ProviderSpecific:  veoProviderSpecific,
		},
	}}

	svc := NewService(nil, nil, catalog)
	svc.SetBaseURLs(server.URL, "")

	_, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Prompt:          "A high resolution shot",
		Model:           "veo-3.1-generate-preview",
		Resolution:      "1080p",
		DurationSeconds: 4,
	})
	if err == nil {
		t.Fatalf("expected error for 1080p with 4s duration")
	}
	if !strings.Contains(err.Error(), "requires 8s duration") {
		t.Fatalf("expected error mentioning 'requires 8s duration', got: %v", err)
	}
	if calls != 0 {
		t.Fatalf("provider called %d times on invalid resolution/duration mismatch", calls)
	}
}

func loadActualSnapshotRecord(t *testing.T, provider, modelID string) pebblestore.ModelCatalogRecord {
	t.Helper()
	snapshotPath := filepath.Join("..", "model", "snapshotdata", "snapshot.json")
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read snapshot.json: %v", err)
	}
	var snap struct {
		SnapshotID      string `json:"snapshot_id"`
		SnapshotVersion string `json:"snapshot_version"`
		Models          []struct {
			ProviderID   string `json:"provider_id"`
			ModelID      string `json:"model_id"`
			DisplayName  string `json:"display_name"`
			Capabilities struct {
				SupportsVideoInput  *bool `json:"supports_video_input"`
				SupportsVideoOutput *bool `json:"supports_video_output"`
				SupportsImageInput  *bool `json:"supports_image_input"`
			} `json:"capabilities"`
			Modalities struct {
				Input  []string `json:"input"`
				Output []string `json:"output"`
			} `json:"modalities"`
			Pricing          json.RawMessage `json:"pricing"`
			ProviderSpecific json.RawMessage `json:"provider_specific"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal snapshot.json: %v", err)
	}
	for _, m := range snap.Models {
		if strings.EqualFold(m.ProviderID, provider) && strings.EqualFold(m.ModelID, modelID) {
			return pebblestore.ModelCatalogRecord{
				Provider:              m.ProviderID,
				Model:                 m.ModelID,
				DisplayName:           m.DisplayName,
				SourceSnapshotID:      snap.SnapshotID,
				SourceSnapshotVersion: snap.SnapshotVersion,
				CatalogModalities: pebblestore.ModelCatalogModalities{
					Inputs:  m.Modalities.Input,
					Outputs: m.Modalities.Output,
				},
				Pricing:          m.Pricing,
				ProviderSpecific: m.ProviderSpecific,
			}
		}
	}
	t.Fatalf("model %s/%s not found in snapshot.json", provider, modelID)
	return pebblestore.ModelCatalogRecord{}
}

func TestExtractVideoOptions_ActualSnapshotModels(t *testing.T) {
	// Requirement: Capabilities and settings must be extracted from actual snapshot records, not fake schemas.
	// Threat/regression: Hardcoded fallbacks or incorrect capability flags diverging from authoritative model snapshot.
	// Boundary/authority: ExtractVideoOptions in videogen/options.go against model/snapshotdata/snapshot.json.

	// 1. Veo 3.1 Generate Preview
	veoRec := loadActualSnapshotRecord(t, "google", "veo-3.1-generate-preview")
	veoOpts := ExtractVideoOptions(veoRec)
	if veoOpts == nil {
		t.Fatalf("expected non-nil options for veo-3.1-generate-preview")
	}
	if !veoOpts.HasVideoOutput {
		t.Errorf("expected HasVideoOutput=true")
	}
	if veoOpts.DefaultRes != "720p" {
		t.Errorf("veo DefaultRes = %q, want 720p", veoOpts.DefaultRes)
	}
	if veoOpts.DefaultRatio != "16:9" {
		t.Errorf("veo DefaultRatio = %q, want 16:9", veoOpts.DefaultRatio)
	}
	if !ContainsStringFold(veoOpts.Resolutions, "720p") || !ContainsStringFold(veoOpts.Resolutions, "1080p") || !ContainsStringFold(veoOpts.Resolutions, "4k") {
		t.Errorf("veo Resolutions = %v, want 720p, 1080p, 4k", veoOpts.Resolutions)
	}
	if !ContainsStringFold(veoOpts.AspectRatios, "16:9") || !ContainsStringFold(veoOpts.AspectRatios, "9:16") {
		t.Errorf("veo AspectRatios = %v, want 16:9, 9:16", veoOpts.AspectRatios)
	}
	// Verify resolution-specific durations
	durs1080p := veoOpts.ResolutionDurations["1080p"]
	if len(durs1080p) != 1 || durs1080p[0] != 8 {
		t.Errorf("veo 1080p durations = %v, want [8]", durs1080p)
	}
	durs4k := veoOpts.ResolutionDurations["4k"]
	if len(durs4k) != 1 || durs4k[0] != 8 {
		t.Errorf("veo 4k durations = %v, want [8]", durs4k)
	}
	durs720p := veoOpts.ResolutionDurations["720p"]
	if len(durs720p) != 3 {
		t.Errorf("veo 720p durations = %v, want [4, 6, 8]", durs720p)
	}
	if !veoOpts.InitialImageSupported || veoOpts.InitialImageMaxInputs != 1 {
		t.Errorf("veo InitialImageSupported=%v MaxInputs=%d, want true/1", veoOpts.InitialImageSupported, veoOpts.InitialImageMaxInputs)
	}
	if veoOpts.ConversationalEditingSupported {
		t.Errorf("veo should not support conversational editing")
	}

	// 2. Gemini Omni 1.1 Flash
	omniRec := loadActualSnapshotRecord(t, "google", "gemini-omni-1.1-flash")
	omniOpts := ExtractVideoOptions(omniRec)
	if omniOpts == nil {
		t.Fatalf("expected non-nil options for gemini-omni-1.1-flash")
	}
	if omniOpts.DefaultRes != "720p" {
		t.Errorf("omni DefaultRes = %q, want 720p", omniOpts.DefaultRes)
	}
	if omniOpts.DefaultRatio != "16:9" {
		t.Errorf("omni DefaultRatio = %q, want 16:9", omniOpts.DefaultRatio)
	}
	if len(omniOpts.Resolutions) != 4 || !ContainsStringFold(omniOpts.Resolutions, "360p") || !ContainsStringFold(omniOpts.Resolutions, "720p") || !ContainsStringFold(omniOpts.Resolutions, "1080p") || !ContainsStringFold(omniOpts.Resolutions, "4k") {
		t.Errorf("omni Resolutions = %v, want 360p, 720p, 1080p, 4k", omniOpts.Resolutions)
	}
	if len(omniOpts.Durations) != 0 {
		t.Errorf("omni Durations = %v, want empty (duration is unknown)", omniOpts.Durations)
	}
	if !omniOpts.InitialImageSupported {
		t.Errorf("omni InitialImageSupported = false, want true")
	}
	if !omniOpts.ConversationalEditingSupported {
		t.Errorf("omni ConversationalEditingSupported = false, want true")
	}

	// 3. Veo 3.1 Lite (extension unsupported)
	liteRec := loadActualSnapshotRecord(t, "google", "veo-3.1-lite-generate-preview")
	liteOpts := ExtractVideoOptions(liteRec)
	if liteOpts == nil {
		t.Fatalf("expected non-nil options for veo-3.1-lite")
	}
	if liteOpts.VideoExtensionSupported {
		t.Errorf("veo 3.1 lite should have VideoExtensionSupported=false")
	}
}

func TestEstimateMediaCost_ActualSnapshotVeoAndOmni(t *testing.T) {
	// Requirement: Pricing for video models from snapshot must compute exact dollar amounts when conditions match,
	// and fail closed (returning 0.0 with unknown price status) when conditions, resolution, or duration are unresolved.
	// Threat/regression: Silent rate fallback or unconditioned rate matching on specialized models.
	// Boundary/authority: EstimateMediaCostFromRecord in store/pebble/usage_limit_store.go.

	veoRec := loadActualSnapshotRecord(t, "google", "veo-3.1-generate-preview")

	// 1. Standard 720p 8s -> $3.20 ($0.40/sec * 8s)
	est720_8s := pebblestore.EstimateMediaCostFromRecord(veoRec, pebblestore.MediaCostEstimateOptions{
		Kind:            "video",
		Resolution:      "720p",
		DurationSeconds: 8,
		IncludesAudio:   true,
		ServiceTier:     "standard",
	})
	if math.Abs(est720_8s.CostUSD-3.20) > 0.0001 {
		t.Fatalf("veo 720p 8s cost = %f, want 3.20", est720_8s.CostUSD)
	}
	if est720_8s.PriceStatus != "known" {
		t.Errorf("veo 720p 8s price_status = %q, want known", est720_8s.PriceStatus)
	}

	// 2. Standard 1080p 8s -> $3.20 ($0.40/sec * 8s)
	est1080_8s := pebblestore.EstimateMediaCostFromRecord(veoRec, pebblestore.MediaCostEstimateOptions{
		Kind:            "video",
		Resolution:      "1080p",
		DurationSeconds: 8,
		IncludesAudio:   true,
		ServiceTier:     "standard",
	})
	if math.Abs(est1080_8s.CostUSD-3.20) > 0.0001 {
		t.Fatalf("veo 1080p 8s cost = %f, want 3.20", est1080_8s.CostUSD)
	}

	// 3. Standard 720p 4s -> $1.60 ($0.40/sec * 4s)
	est720_4s := pebblestore.EstimateMediaCostFromRecord(veoRec, pebblestore.MediaCostEstimateOptions{
		Kind:            "video",
		Resolution:      "720p",
		DurationSeconds: 4,
		IncludesAudio:   true,
		ServiceTier:     "standard",
	})
	if math.Abs(est720_4s.CostUSD-1.60) > 0.0001 {
		t.Fatalf("veo 720p 4s cost = %f, want 1.60", est720_4s.CostUSD)
	}

	// 4. Missing resolution -> fails closed, unknown
	estNoRes := pebblestore.EstimateMediaCostFromRecord(veoRec, pebblestore.MediaCostEstimateOptions{
		Kind:            "video",
		DurationSeconds: 8,
		IncludesAudio:   true,
		ServiceTier:     "standard",
	})
	if estNoRes.CostUSD != 0.0 || estNoRes.PriceStatus != "unknown" {
		t.Fatalf("veo without resolution must fail closed: got cost=%f, status=%q", estNoRes.CostUSD, estNoRes.PriceStatus)
	}

	// 5. Unknown resolution "8k" -> fails closed, unknown
	est8k := pebblestore.EstimateMediaCostFromRecord(veoRec, pebblestore.MediaCostEstimateOptions{
		Kind:            "video",
		Resolution:      "8k",
		DurationSeconds: 8,
		IncludesAudio:   true,
		ServiceTier:     "standard",
	})
	if est8k.CostUSD != 0.0 || est8k.PriceStatus != "unknown" {
		t.Fatalf("veo with unknown resolution 8k must fail closed: got cost=%f, status=%q", est8k.CostUSD, est8k.PriceStatus)
	}

	// 6. Unknown duration 0s -> fails closed, unknown
	est0s := pebblestore.EstimateMediaCostFromRecord(veoRec, pebblestore.MediaCostEstimateOptions{
		Kind:            "video",
		Resolution:      "720p",
		DurationSeconds: 0,
		IncludesAudio:   true,
		ServiceTier:     "standard",
	})
	if est0s.CostUSD != 0.0 || est0s.PriceStatus != "unknown" {
		t.Fatalf("veo with 0s duration must fail closed: got cost=%f, status=%q", est0s.CostUSD, est0s.PriceStatus)
	}

	// 7. Omni with duration 0s (unknown duration) -> fails closed, unknown
	omniRec := loadActualSnapshotRecord(t, "google", "gemini-omni-1.1-flash")
	estOmni := pebblestore.EstimateMediaCostFromRecord(omniRec, pebblestore.MediaCostEstimateOptions{
		Kind:            "video",
		Resolution:      "720p",
		DurationSeconds: 0,
		IncludesAudio:   true,
		ServiceTier:     "standard",
	})
	if estOmni.CostUSD != 0.0 || estOmni.PriceStatus != "unknown" {
		t.Fatalf("omni without duration must fail closed: got cost=%f, status=%q", estOmni.CostUSD, estOmni.PriceStatus)
	}
}
