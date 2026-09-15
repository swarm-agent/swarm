package audiogen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func setupTestAuthStore(t *testing.T, googleKey string) (*pebblestore.AuthStore, string) {
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

	return authStore, accountScopeID
}

type fakeCommandRunner struct {
	lookPathErr error
	runErr      error
	lastCmd     string
	lastArgs    []string
	runHook     func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (f *fakeCommandRunner) LookPath(file string) (string, error) {
	if f.lookPathErr != nil {
		return "", f.lookPathErr
	}
	return "/usr/bin/" + file, nil
}

func (f *fakeCommandRunner) RunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.lastCmd = name
	f.lastArgs = args
	if f.runHook != nil {
		return f.runHook(ctx, name, args...)
	}
	if f.runErr != nil {
		return nil, f.runErr
	}
	// For ffmpeg, if an output path is provided in args, write fake trimmed output
	if name == "ffmpeg" && len(args) > 0 {
		outPath := args[len(args)-1]
		_ = os.WriteFile(outPath, []byte("trimmed-fake-audio-bytes"), 0o600)
	}
	return []byte("ok"), nil
}

func TestRouteModel(t *testing.T) {
	tests := []struct {
		name           string
		durationSec    int
		requestedModel string
		wantModel      string
		wantProvider   string
	}{
		{
			name:         "duration 28s routes to clip preview",
			durationSec:  28,
			wantModel:    ModelLyriaClip,
			wantProvider: ProviderGoogleGemini,
		},
		{
			name:         "duration 30s routes to clip preview",
			durationSec:  30,
			wantModel:    ModelLyriaClip,
			wantProvider: ProviderGoogleGemini,
		},
		{
			name:         "duration 31s routes to full song 3.5",
			durationSec:  31,
			wantModel:    ModelLyriaSong,
			wantProvider: ProviderGoogleGemini,
		},
		{
			name:         "duration 120s routes to full song 3.5",
			durationSec:  120,
			wantModel:    ModelLyriaSong,
			wantProvider: ProviderGoogleGemini,
		},
		{
			name:         "zero duration defaults to clip preview",
			durationSec:  0,
			wantModel:    DefaultAudioClipModel,
			wantProvider: ProviderGoogleGemini,
		},
		{
			name:           "explicit lyria-3.5 with 15s overrides duration",
			durationSec:    15,
			requestedModel: "google/lyria-3.5",
			wantModel:      ModelLyriaSong,
			wantProvider:   ProviderGoogleGemini,
		},
		{
			name:           "explicit lyria-3-clip-preview with 90s overrides duration",
			durationSec:    90,
			requestedModel: "lyria-3-clip-preview",
			wantModel:      ModelLyriaClip,
			wantProvider:   ProviderGoogleGemini,
		},
		{
			name:           "openrouter model",
			durationSec:    30,
			requestedModel: "openrouter/meta/music",
			wantModel:      "openrouter/meta/music",
			wantProvider:   "openrouter",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotModel, gotProvider := RouteModel(tc.durationSec, tc.requestedModel)
			if gotModel != tc.wantModel {
				t.Errorf("RouteModel() model = %q, want %q", gotModel, tc.wantModel)
			}
			if gotProvider != tc.wantProvider {
				t.Errorf("RouteModel() provider = %q, want %q", gotProvider, tc.wantProvider)
			}
		})
	}
}

func TestNormalizeDuration(t *testing.T) {
	tests := []struct {
		name        string
		durationSec int
		modelID     string
		want        int
	}{
		{"clip zero defaults to 30", 0, ModelLyriaClip, 30},
		{"clip negative defaults to 30", -5, ModelLyriaClip, 30},
		{"clip 28 stays 28", 28, ModelLyriaClip, 28},
		{"clip 45 caps at 30", 45, ModelLyriaClip, 30},
		{"song zero defaults to 120", 0, ModelLyriaSong, 120},
		{"song 150 stays 150", 150, ModelLyriaSong, 150},
		{"song 500 caps at 300", 500, ModelLyriaSong, 300},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeDuration(tc.durationSec, tc.modelID)
			if got != tc.want {
				t.Errorf("NormalizeDuration() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestShapePromptWithDuration(t *testing.T) {
	t.Run("injects 28s clip arrangement timestamp", func(t *testing.T) {
		shaped := ShapePromptWithDuration("funky bassline groove", 28)
		if !strings.Contains(shaped, "funky bassline groove") {
			t.Errorf("expected prompt text in shaped prompt: %s", shaped)
		}
		if !strings.Contains(shaped, "[Arrangement: 00:00 start") {
			t.Errorf("expected arrangement header: %s", shaped)
		}
		if !strings.Contains(shaped, "00:28]") {
			t.Errorf("expected end timestamp 00:28: %s", shaped)
		}
	})

	t.Run("injects 120s song arrangement timestamp", func(t *testing.T) {
		shaped := ShapePromptWithDuration("epic synthwave track", 120)
		if !strings.Contains(shaped, "02:00]") {
			t.Errorf("expected end timestamp 02:00: %s", shaped)
		}
	})

	t.Run("preserves existing timestamp cues", func(t *testing.T) {
		orig := "cool beat [00:00 - 00:20] intro"
		shaped := ShapePromptWithDuration(orig, 28)
		if shaped != orig {
			t.Errorf("expected existing timestamp prompt preserved, got: %s", shaped)
		}
	})

	t.Run("zero duration returns prompt unchanged", func(t *testing.T) {
		orig := "simple piano"
		shaped := ShapePromptWithDuration(orig, 0)
		if shaped != orig {
			t.Errorf("expected unchanged prompt for zero duration, got: %s", shaped)
		}
	})
}

func TestEstimateAudioCost(t *testing.T) {
	costClip, descClip := EstimateAudioCost("google", ModelLyriaClip, false, nil)
	if costClip != 0.04 || !strings.Contains(descClip, "$0.04") {
		t.Errorf("unexpected clip cost: %f, %s", costClip, descClip)
	}

	costSong, descSong := EstimateAudioCost("google", ModelLyriaSong, false, nil)
	if costSong != 0.08 || !strings.Contains(descSong, "$0.08") {
		t.Errorf("unexpected song cost: %f, %s", costSong, descSong)
	}

	costIter, descIter := EstimateAudioCost("google", "custom", true, nil)
	if costIter != 0.04 || !strings.Contains(descIter, "iteration") {
		t.Errorf("unexpected iteration cost: %f, %s", costIter, descIter)
	}

	catalog := []byte(`{"audio_output": 0.06}`)
	costCat, descCat := EstimateAudioCost("google", ModelLyriaClip, false, catalog)
	if costCat != 0.06 || !strings.Contains(descCat, "catalog") {
		t.Errorf("unexpected catalog cost: %f, %s", costCat, descCat)
	}
}

func TestGenerateManagedAudio_ClipPreview(t *testing.T) {
	fakeAudio := []byte("fake-mp3-audio-clip-data")
	encodedAudio := base64.StdEncoding.EncodeToString(fakeAudio)

	var requestMethod, authHeader, requestModel string
	var hasAudioFormat bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/interactions") {
			http.NotFound(w, r)
			return
		}
		requestMethod = r.Method
		authHeader = r.Header.Get("x-goog-api-key")

		var body lyriaInteractionRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		requestModel = body.Model
		hasAudioFormat = body.ResponseFormat != nil && body.ResponseFormat.Type == "audio"

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lyriaInteractionResponse{
			ID:     "inter_12345",
			Status: "completed",
			Model:  body.Model,
			Steps: []lyriaStep{
				{
					Type: "model_output",
					Content: []lyriaContent{
						{
							Type:     "audio",
							MIMEType: "audio/mp3",
							Data:     encodedAudio,
						},
						{
							Type: "text",
							Text: "[Intro beat]\nYeah, Swarm Lyria clip",
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURL(server.URL)

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
		Prompt:          "electronic drum groove",
		DurationSeconds: 28,
		Principal:       principal,
	})
	if err != nil {
		t.Fatalf("GenerateManagedAudio failed: %v", err)
	}

	if requestMethod != http.MethodPost {
		t.Errorf("requestMethod = %q, want POST", requestMethod)
	}
	if authHeader != "test-google-key" {
		t.Errorf("authHeader = %q, want test-google-key", authHeader)
	}
	if requestModel != ModelLyriaClip {
		t.Errorf("requestModel = %q, want %q", requestModel, ModelLyriaClip)
	}
	if !hasAudioFormat {
		t.Error("expected response_format type=audio in request")
	}

	if res.Model != ModelLyriaClip {
		t.Errorf("res.Model = %q, want %q", res.Model, ModelLyriaClip)
	}
	if res.Provider != ProviderGoogleGemini {
		t.Errorf("res.Provider = %q, want %q", res.Provider, ProviderGoogleGemini)
	}
	if res.InteractionID != "inter_12345" {
		t.Errorf("res.InteractionID = %q, want inter_12345", res.InteractionID)
	}
	if res.MediaType != DefaultAudioMIMEType {
		t.Errorf("res.MediaType = %q, want %q", res.MediaType, DefaultAudioMIMEType)
	}
	if !strings.Contains(res.Lyrics, "Swarm Lyria clip") {
		t.Errorf("expected lyrics in result, got: %q", res.Lyrics)
	}
	if res.EstimatedCostUSD != 0.04 {
		t.Errorf("res.EstimatedCostUSD = %f, want 0.04", res.EstimatedCostUSD)
	}
	if res.Metadata.Model != ModelLyriaClip {
		t.Errorf("res.Metadata.Model = %q, want %q", res.Metadata.Model, ModelLyriaClip)
	}
	if res.Metadata.InteractionID != "inter_12345" {
		t.Errorf("res.Metadata.InteractionID = %q, want inter_12345", res.Metadata.InteractionID)
	}
}

func TestGenerateManagedAudio_Song(t *testing.T) {
	fakeSong := []byte("fake-full-song-audio-bytes")
	encodedSong := base64.StdEncoding.EncodeToString(fakeSong)

	var requestModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body lyriaInteractionRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		requestModel = body.Model

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lyriaInteractionResponse{
			ID:     "inter_song_999",
			Status: "completed",
			Model:  body.Model,
			Steps: []lyriaStep{
				{
					Type: "model_output",
					Content: []lyriaContent{
						{
							Type:     "audio",
							MIMEType: "audio/mp3",
							Data:     encodedSong,
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURL(server.URL)

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
		Prompt:          "full synthwave song with vocals",
		DurationSeconds: 90,
		Principal:       principal,
	})
	if err != nil {
		t.Fatalf("GenerateManagedAudio failed: %v", err)
	}

	if requestModel != ModelLyriaSong {
		t.Errorf("requestModel = %q, want %q", requestModel, ModelLyriaSong)
	}
	if res.Model != ModelLyriaSong {
		t.Errorf("res.Model = %q, want %q", res.Model, ModelLyriaSong)
	}
	if res.DurationMs != 90000 {
		t.Errorf("res.DurationMs = %d, want 90000", res.DurationMs)
	}
	if res.EstimatedCostUSD != 0.08 {
		t.Errorf("res.EstimatedCostUSD = %f, want 0.08", res.EstimatedCostUSD)
	}
}

func TestGenerateManagedAudio_Iteration(t *testing.T) {
	fakeAudio := []byte("fake-iteration-audio")
	encoded := base64.StdEncoding.EncodeToString(fakeAudio)

	var receivedPrevID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body lyriaInteractionRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		receivedPrevID = body.PreviousInteractionID

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lyriaInteractionResponse{
			ID:     "inter_turn_2",
			Status: "completed",
			Model:  body.Model,
			Steps: []lyriaStep{
				{
					Type: "model_output",
					Content: []lyriaContent{
						{
							Type:     "audio",
							MIMEType: "audio/mp3",
							Data:     encoded,
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURL(server.URL)

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
		Prompt:    "add warmer reverb and acoustic guitar",
		Principal: principal,
		Source: &ManagedAudioSource{
			InteractionID: "inter_turn_1",
		},
	})
	if err != nil {
		t.Fatalf("GenerateManagedAudio failed: %v", err)
	}

	if receivedPrevID != "inter_turn_1" {
		t.Errorf("receivedPrevID = %q, want inter_turn_1", receivedPrevID)
	}
	if res.Metadata.PreviousInteractionID != "inter_turn_1" {
		t.Errorf("Metadata.PreviousInteractionID = %q, want inter_turn_1", res.Metadata.PreviousInteractionID)
	}
	if res.InteractionID != "inter_turn_2" {
		t.Errorf("res.InteractionID = %q, want inter_turn_2", res.InteractionID)
	}
}

func TestGenerateManagedAudio_ImageToMusic(t *testing.T) {
	fakeAudio := []byte("fake-image-music-audio")
	encodedAudio := base64.StdEncoding.EncodeToString(fakeAudio)
	fakePNG := []byte("\x89PNG\r\n\x1a\nfakeimagebytes")

	var inputSlice []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if slice, ok := raw["input"].([]any); ok {
			for _, item := range slice {
				if m, ok := item.(map[string]any); ok {
					inputSlice = append(inputSlice, m)
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lyriaInteractionResponse{
			ID:     "inter_img_1",
			Status: "completed",
			Model:  ModelLyriaClip,
			Steps: []lyriaStep{
				{
					Type: "model_output",
					Content: []lyriaContent{
						{
							Type:     "audio",
							MIMEType: "audio/mp3",
							Data:     encodedAudio,
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURL(server.URL)

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
		Prompt:    "serene meadow ambiance",
		Principal: principal,
		Image: &ManagedAudioImage{
			Bytes:     fakePNG,
			MediaType: "image/png",
		},
	})
	if err != nil {
		t.Fatalf("GenerateManagedAudio failed: %v", err)
	}

	if len(inputSlice) != 2 {
		t.Fatalf("expected 2 input parts (image + text), got %d: %#v", len(inputSlice), inputSlice)
	}
	if inputSlice[0]["type"] != "image" {
		t.Errorf("inputSlice[0] type = %v, want image", inputSlice[0]["type"])
	}
	if inputSlice[1]["type"] != "text" {
		t.Errorf("inputSlice[1] type = %v, want text", inputSlice[1]["type"])
	}
	if !res.Metadata.HasImageInspiration {
		t.Error("expected Metadata.HasImageInspiration to be true")
	}
}

func TestGenerateManagedAudio_AudioDownloadURI(t *testing.T) {
	fakeDownloadedAudio := []byte("downloaded-audio-via-uri")

	var downloadCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/interactions"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(lyriaInteractionResponse{
				ID:     "inter_uri_1",
				Status: "completed",
				Model:  ModelLyriaClip,
				Steps: []lyriaStep{
					{
						Type: "model_output",
						Content: []lyriaContent{
							{
								Type: "audio",
								URI:  "/download/audio.mp3",
							},
						},
					},
				},
			})
		case strings.Contains(r.URL.Path, "/download/audio.mp3"):
			downloadCalled = true
			if r.Header.Get("x-goog-api-key") != "test-google-key" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "audio/mp3")
			_, _ = w.Write(fakeDownloadedAudio)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURL(server.URL)

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
		Prompt:    "acoustic guitar strumming",
		Principal: principal,
	})
	if err != nil {
		t.Fatalf("GenerateManagedAudio failed: %v", err)
	}

	if !downloadCalled {
		t.Error("expected download endpoint to be called")
	}
	if string(res.Bytes) != string(fakeDownloadedAudio) {
		t.Errorf("res.Bytes = %q, want %q", string(res.Bytes), string(fakeDownloadedAudio))
	}
}

func TestGenerateManagedAudio_TrimmingWithCommandRunner(t *testing.T) {
	fakeRawAudio := []byte("fake-30s-raw-clip")
	encoded := base64.StdEncoding.EncodeToString(fakeRawAudio)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lyriaInteractionResponse{
			ID:     "inter_trim_1",
			Status: "completed",
			Model:  ModelLyriaClip,
			Steps: []lyriaStep{
				{
					Type: "model_output",
					Content: []lyriaContent{
						{
							Type:     "audio",
							MIMEType: "audio/mp3",
							Data:     encoded,
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURL(server.URL)

	runner := &fakeCommandRunner{}
	svc.SetCommandRunner(runner)

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
		Prompt:          "make a 28 sound clip",
		DurationSeconds: 28,
		FadeOutSeconds:  0.5,
		Principal:       principal,
	})
	if err != nil {
		t.Fatalf("GenerateManagedAudio failed: %v", err)
	}

	if runner.lastCmd != "ffmpeg" {
		t.Errorf("runner.lastCmd = %q, want ffmpeg", runner.lastCmd)
	}
	argsJoined := strings.Join(runner.lastArgs, " ")
	if !strings.Contains(argsJoined, "-t 28.000") {
		t.Errorf("expected -t 28.000 in ffmpeg args: %s", argsJoined)
	}
	if !strings.Contains(argsJoined, "afade=t=out:st=27.500:d=0.500") {
		t.Errorf("expected afade in ffmpeg args: %s", argsJoined)
	}

	if !res.Metadata.Trimmed {
		t.Error("expected Metadata.Trimmed to be true")
	}
	if res.DurationMs != 28000 {
		t.Errorf("res.DurationMs = %d, want 28000", res.DurationMs)
	}
	if res.Metadata.TargetDurationSeconds != 28.0 {
		t.Errorf("res.Metadata.TargetDurationSeconds = %f, want 28.0", res.Metadata.TargetDurationSeconds)
	}
	if string(res.Bytes) != "trimmed-fake-audio-bytes" {
		t.Errorf("res.Bytes = %q, want trimmed-fake-audio-bytes", string(res.Bytes))
	}
}

func TestGenerateManagedAudio_TrimmingMissingFFmpegGracefulFallback(t *testing.T) {
	fakeRawAudio := []byte("fake-30s-raw-clip")
	encoded := base64.StdEncoding.EncodeToString(fakeRawAudio)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lyriaInteractionResponse{
			ID:     "inter_fallback_1",
			Status: "completed",
			Model:  ModelLyriaClip,
			Steps: []lyriaStep{
				{
					Type: "model_output",
					Content: []lyriaContent{
						{
							Type:     "audio",
							MIMEType: "audio/mp3",
							Data:     encoded,
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURL(server.URL)

	// Runner where LookPath fails
	runner := &fakeCommandRunner{lookPathErr: errors.New("ffmpeg not found")}
	svc.SetCommandRunner(runner)

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
		Prompt:          "make a 28 sound clip",
		DurationSeconds: 28,
		Principal:       principal,
	})
	if err != nil {
		t.Fatalf("GenerateManagedAudio failed: %v", err)
	}

	// Falls back to untrimmed audio without failing
	if res.Metadata.Trimmed {
		t.Error("expected Metadata.Trimmed to be false when ffmpeg is missing")
	}
	if string(res.Bytes) != string(fakeRawAudio) {
		t.Errorf("res.Bytes = %q, want %q", string(res.Bytes), string(fakeRawAudio))
	}
}

func TestGenerateManagedAudio_Errors(t *testing.T) {
	authStore, accountScopeID := setupTestAuthStore(t, "test-key")
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}

	t.Run("nil service returns error", func(t *testing.T) {
		var nilSvc *Service
		_, err := nilSvc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{Prompt: "test"})
		if err == nil {
			t.Fatal("expected error for nil service")
		}
	})

	t.Run("empty prompt returns error", func(t *testing.T) {
		svc := NewService(authStore, nil, nil)
		_, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
			Prompt:    "   ",
			Principal: principal,
		})
		if err == nil || !strings.Contains(err.Error(), "requires prompt") {
			t.Fatalf("expected requires prompt error, got: %v", err)
		}
	})

	t.Run("missing google api key returns error", func(t *testing.T) {
		emptyStore, emptyScope := setupTestAuthStore(t, "")
		svc := NewService(emptyStore, nil, nil)
		_, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
			Prompt:    "guitar",
			Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: emptyScope},
		})
		if err == nil || !strings.Contains(err.Error(), "google api key is not configured") {
			t.Fatalf("expected missing google api key error, got: %v", err)
		}
	})

	t.Run("401 unauthorized returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid api key", http.StatusUnauthorized)
		}))
		defer server.Close()

		svc := NewService(authStore, nil, nil)
		svc.SetBaseURL(server.URL)

		_, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
			Prompt:    "guitar",
			Principal: principal,
		})
		if err == nil || !strings.Contains(err.Error(), "google lyria api error (401)") {
			t.Fatalf("expected 401 error, got: %v", err)
		}
	})

	t.Run("rpc error message extracted", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    400,
					"message": "Prompt contains policy violation",
					"status":  "INVALID_ARGUMENT",
				},
			})
		}))
		defer server.Close()

		svc := NewService(authStore, nil, nil)
		svc.SetBaseURL(server.URL)

		_, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
			Prompt:    "restricted prompt",
			Principal: principal,
		})
		if err == nil || !strings.Contains(err.Error(), "Prompt contains policy violation") {
			t.Fatalf("expected RPC error message, got: %v", err)
		}
	})

	t.Run("empty audio output returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(lyriaInteractionResponse{
				ID:     "inter_empty",
				Status: "completed",
				Steps:  []lyriaStep{},
			})
		}))
		defer server.Close()

		svc := NewService(authStore, nil, nil)
		svc.SetBaseURL(server.URL)

		_, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
			Prompt:    "guitar",
			Principal: principal,
		})
		if err == nil || !strings.Contains(err.Error(), "contained no audio output") {
			t.Fatalf("expected contained no audio output error, got: %v", err)
		}
	})
}

func TestGenerateManagedAudio_SongTrimmingWithCommandRunner(t *testing.T) {
	fakeRawSong := []byte("fake-full-song-raw-bytes")
	encoded := base64.StdEncoding.EncodeToString(fakeRawSong)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(lyriaInteractionResponse{
			ID:     "inter_song_trim_1",
			Status: "completed",
			Model:  ModelLyriaSong,
			Steps: []lyriaStep{
				{
					Type: "model_output",
					Content: []lyriaContent{
						{
							Type:     "audio",
							MIMEType: "audio/mp3",
							Data:     encoded,
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	authStore, accountScopeID := setupTestAuthStore(t, "test-google-key")
	svc := NewService(authStore, nil, nil)
	svc.SetBaseURL(server.URL)

	runner := &fakeCommandRunner{}
	svc.SetCommandRunner(runner)

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u1", AccountScopeID: accountScopeID}
	res, err := svc.GenerateManagedAudio(context.Background(), ManagedAudioRequest{
		Prompt:          "make a 45 sound clip",
		DurationSeconds: 45,
		FadeOutSeconds:  0.5,
		Principal:       principal,
	})
	if err != nil {
		t.Fatalf("GenerateManagedAudio failed: %v", err)
	}

	if runner.lastCmd != "ffmpeg" {
		t.Errorf("runner.lastCmd = %q, want ffmpeg", runner.lastCmd)
	}
	argsJoined := strings.Join(runner.lastArgs, " ")
	if !strings.Contains(argsJoined, "-t 45.000") {
		t.Errorf("expected -t 45.000 in ffmpeg args: %s", argsJoined)
	}
	if !strings.Contains(argsJoined, "afade=t=out:st=44.500:d=0.500") {
		t.Errorf("expected afade in ffmpeg args: %s", argsJoined)
	}
	if !res.Metadata.Trimmed {
		t.Error("expected Metadata.Trimmed to be true")
	}
	if res.DurationMs != 45000 {
		t.Errorf("res.DurationMs = %d, want 45000", res.DurationMs)
	}
	if res.Metadata.TargetDurationSeconds != 45.0 {
		t.Errorf("res.Metadata.TargetDurationSeconds = %f, want 45.0", res.Metadata.TargetDurationSeconds)
	}
}

func TestProbeAudioDurationMs(t *testing.T) {
	ctx := context.Background()

	t.Run("empty bytes returns error", func(t *testing.T) {
		runner := &fakeCommandRunner{}
		_, err := ProbeAudioDurationMs(ctx, runner, nil)
		if err == nil {
			t.Fatal("expected error for nil audio bytes")
		}
	})

	t.Run("missing ffprobe returns error", func(t *testing.T) {
		runner := &fakeCommandRunner{lookPathErr: errors.New("ffprobe not found")}
		_, err := ProbeAudioDurationMs(ctx, runner, []byte("audio-bytes"))
		if err == nil || !strings.Contains(err.Error(), "ffprobe not found") {
			t.Fatalf("expected ffprobe not found error, got: %v", err)
		}
	})

	t.Run("parses ffprobe duration output", func(t *testing.T) {
		runner := &fakeCommandRunner{
			runHook: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name != "ffprobe" {
					return nil, fmt.Errorf("unexpected command: %s", name)
				}
				return []byte("28.500000\n"), nil
			},
		}
		durationMs, err := ProbeAudioDurationMs(ctx, runner, []byte("fake-audio-bytes"))
		if err != nil {
			t.Fatalf("ProbeAudioDurationMs failed: %v", err)
		}
		if durationMs != 28500 {
			t.Errorf("ProbeAudioDurationMs = %d, want 28500", durationMs)
		}
	})
}
