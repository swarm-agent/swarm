package videogen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}

	// Turn 1: Initial generation with Omni explicitly requested
	res1, err := svc.generateGoogleOmni(context.Background(), "test-google-key", "gemini-omni-1.1-flash", "A violinist in the park", "16:9", "720p", nil, nil)
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
	res2, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
		Prompt:    "Make the violin invisible",
		Principal: principal,
		Source: &ManagedVideoSource{
			Bytes:         res1.Bytes,
			MediaType:     "video/mp4",
			InteractionID: res1.InteractionID,
			Model:         "gemini-omni-1.1-flash",
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

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
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

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	_, err := svc.GenerateManagedVideo(context.Background(), ManagedVideoRequest{
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
	if !stringsContains(err.Error(), "EU/EEA, UK, and Switzerland") {
		t.Fatalf("expected regional diagnostic in error message, got: %v", err)
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

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.generateGoogleOmni(context.Background(), "test-google-key", "gemini-omni-1.1-flash", "Bring this image to life", "16:9", "720p", nil, &ManagedVideoImage{
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
