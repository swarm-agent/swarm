package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/videogen"
)

// mockPreflightVideoService implements managedVideoService and videoPreflightService for testing.
type mockPreflightVideoService struct {
	mu           sync.Mutex
	lastReq      videogen.ManagedVideoRequest
	lastPfReq    videogen.VideoPreflightRequest
	failGen      bool
	failGenErr   error
	genResult    videogen.ManagedVideoResult
	preflightErr error
	uiSettings   *uisettings.Service
}

func (m *mockPreflightVideoService) GenerateManagedVideo(ctx context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastReq = req
	if m.failGen {
		if m.failGenErr != nil {
			return videogen.ManagedVideoResult{}, m.failGenErr
		}
		return videogen.ManagedVideoResult{}, fmt.Errorf("mock upstream generation error")
	}
	res := m.genResult
	if len(res.Bytes) == 0 {
		res.Bytes = []byte("fake-video-mp4-payload")
		res.MediaType = "video/mp4"
	}
	if res.Model == "" {
		res.Model = req.Model
	}
	if res.Provider == "" {
		res.Provider = "google"
	}
	if res.Provenance == nil {
		res.Provenance = &pebblestore.VideoProvenance{
			AccountScopeID: req.Principal.AccountScopeID,
			Provider:       res.Provider,
			Model:          res.Model,
			Operation:      req.Operation,
			CreatedAt:      time.Now().UnixMilli(),
		}
	}
	return res, nil
}

func (m *mockPreflightVideoService) PreflightVideoOperation(ctx context.Context, req videogen.VideoPreflightRequest) (*videogen.VideoPreflightResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastPfReq = req
	if m.preflightErr != nil {
		return nil, m.preflightErr
	}

	// Check UI settings iteration model if configured
	if m.uiSettings != nil && req.Principal.AccountScopeID != "" {
		ui, err := m.uiSettings.GetForAccount(req.Principal.AccountScopeID)
		if err == nil {
			if req.Operation == pebblestore.VideoOperationEdit && req.ExplicitModel == "" && ui.Tools.Video.IterationModel == "" {
				return nil, errors.New("no default video iteration model configured for account; select a model or configure one in Settings")
			}
		}
	}

	model := req.ExplicitModel
	if model == "" {
		if req.Operation == pebblestore.VideoOperationEdit {
			model = "gemini-omni-video"
		} else if req.Operation == pebblestore.VideoOperationExtend {
			isOmni := (req.Source != nil && req.Source.InteractionID != "") || (req.SourceProvenance != nil && videogen.IsOmniModel(req.SourceProvenance.Model))
			if isOmni {
				model = "gemini-omni-video"
			} else {
				model = "veo-3.1-generate-preview"
			}
		} else {
			model = "veo-3.1-generate-preview"
		}
	}

	if req.Operation == pebblestore.VideoOperationEdit && videogen.IsVeoModel(model) {
		return nil, errors.New("Veo models do not support video editing; use Gemini Omni for video editing or extend for Veo continuation")
	}

	dur := req.DurationSeconds
	if videogen.IsOmniModel(model) {
		dur = 0
	} else if dur <= 0 {
		dur = 8
	}
	ar := req.AspectRatio
	if ar == "" {
		ar = "16:9"
	}
	res := req.Resolution
	if res == "" {
		res = "720p"
	}

	return &videogen.VideoPreflightResult{
		Operation:        req.Operation,
		ResolvedModel:    model,
		ResolvedProvider: "google",
		DurationSeconds:  dur,
		AspectRatio:      ar,
		Resolution:       res,
	}, nil
}

// seedOmniVideoCatalogRecord adds a Gemini Omni model record to the model catalog.
func seedOmniVideoCatalogRecord(t *testing.T, server *Server) {
	t.Helper()
	catStore := pebblestore.NewModelCatalogStore(server.sessions.Store().Underlying())
	omniRecord := pebblestore.ModelCatalogRecord{
		Provider:          "google",
		Model:             "gemini-omni-video",
		DisplayName:       "Gemini Omni Video",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text", "video", "image"}, Outputs: []string{"video"}},
		Media: &pebblestore.ModelCatalogMediaCapabilities{
			State:           pebblestore.ModelCatalogMediaStateSupported,
			ProviderSurface: "predict",
		},
		ProviderSpecific:      json.RawMessage(`{"google":{"model_api_surface":"predict","video_generation":{"status":"verified","settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16","1:1","4:3"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p"]}},"features":{"conversational_iteration":true,"initial_image":{"status":"verified","supported":true,"max_inputs":1}}}}}`),
		Pricing:               json.RawMessage(`{"input_per_million":1.25,"output_per_million":5}`),
		SourceSnapshotID:      "snap-1",
		SourceSnapshotVersion: "1",
	}
	if err := catStore.SetRecord(omniRecord); err != nil {
		t.Fatalf("seed omni catalog record: %v", err)
	}
}

func TestVideoOperations_ExplicitDiscriminatorAcrossProjectTask(t *testing.T) {
	// Purpose:
	// - Invariant: Project video task creation must require or explicitly recognize the
	//   operation discriminator ("create", "edit", "extend") across request parsing, task
	//   record persistence, and execution preflight.
	// - Regression/Threat: Without explicit discrimination, video-input requests were conflated,
	//   prompt wording was guessed ("isContinuation", "isFineTune"), and invalid operations were accepted.
	// - Boundary: Server.handleProjectTasks in projects.go and validateProjectMediaTaskSettings in projects_media.go.
	// - Layer: Full API request routing against Pebble store.
	server, ss, p := setupDirectMediaTestServer(t)
	seedOmniVideoCatalogRecord(t, server)
	mockVideo := &mockPreflightVideoService{}
	server.SetVideoGenerationService(mockVideo)

	uiChatStore := pebblestore.NewUISettingsStore(ss.Underlying())
	uiSvc := uisettings.NewService(uiChatStore)
	_, _ = uiSvc.SetForAccount(p.AccountScopeID, uisettings.UISettings{
		Tools: uisettings.ToolSettings{
			Video: uisettings.ToolVideoSettings{
				DefaultModel:   "veo-3.1-generate-preview",
				IterationModel: "gemini-omni-video",
			},
		},
	})
	server.uiSettings = uiSvc

	proj := &pebblestore.ProjectRecord{
		ID:        "proj-discrim-test",
		AccountID: p.AccountScopeID,
		Name:      "Discriminator Test Project",
	}
	if err := ss.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatalf("put project: %v", err)
	}

	validVideoDataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte("fake-mp4-stream-data"))
	validImageDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15c4\x00\x00\x00\rIDATx\x9cc\xf8\xff\xff?\x00\x05\xfe\x02\xfe\xa74d\xa2\x00\x00\x00\x00IEND\xaeB`\x82"))

	tests := []struct {
		name         string
		payload      map[string]any
		wantStatus   int
		wantErrSub   string
		wantOpInTask string
	}{
		{
			name: "invalid operation string rejected",
			payload: map[string]any{
				"title":        "Invalid Op Video",
				"agent":        "video",
				"operation":    "remix_magic",
				"aspect_ratio": "16:9",
			},
			wantStatus: http.StatusBadRequest,
			wantErrSub: "invalid; must be create, edit, or extend",
		},
		{
			name: "video input with missing operation is rejected (not silently inferred)",
			payload: map[string]any{
				"title": "Video Input Without Op",
				"agent": "video",
				"attached_media": []map[string]any{
					{"id": "vid-1", "kind": "video", "url": validVideoDataURL, "filename": "clip.mp4"},
				},
			},
			wantStatus: http.StatusBadRequest,
			wantErrSub: "video operation must be explicitly specified when source media is provided",
		},
		{
			name: "create operation with video input rejected",
			payload: map[string]any{
				"title":     "Create With Video",
				"agent":     "video",
				"operation": "create",
				"attached_media": []map[string]any{
					{"id": "vid-1", "kind": "video", "url": validVideoDataURL, "filename": "clip.mp4"},
				},
			},
			wantStatus: http.StatusBadRequest,
			wantErrSub: "cannot provide source video for create operation; use edit or extend",
		},
		{
			name: "edit operation with image input rejected",
			payload: map[string]any{
				"title":     "Edit With Image",
				"agent":     "video",
				"operation": "edit",
				"attached_media": []map[string]any{
					{"id": "img-1", "kind": "image", "url": validImageDataURL, "filename": "frame.png"},
				},
			},
			wantStatus: http.StatusBadRequest,
			wantErrSub: "initial image input is not supported for video edit operation",
		},
		{
			name: "edit operation without source rejected",
			payload: map[string]any{
				"title":     "Edit Without Source",
				"agent":     "video",
				"operation": "edit",
			},
			wantStatus: http.StatusBadRequest,
			wantErrSub: "video edit operation requires source video",
		},
		{
			name: "extend operation with multiple attachments rejected",
			payload: map[string]any{
				"title":     "Extend Multi Attachments",
				"agent":     "video",
				"operation": "extend",
				"attached_media": []map[string]any{
					{"id": "vid-1", "kind": "video", "url": validVideoDataURL, "filename": "clip1.mp4"},
					{"id": "vid-2", "kind": "video", "url": validVideoDataURL, "filename": "clip2.mp4"},
				},
			},
			wantStatus: http.StatusBadRequest,
			wantErrSub: "requires exactly 1 source video",
		},
		{
			name: "extend operation with image input rejected",
			payload: map[string]any{
				"title":     "Extend With Image",
				"agent":     "video",
				"operation": "extend",
				"attached_media": []map[string]any{
					{"id": "img-1", "kind": "image", "url": validImageDataURL, "filename": "frame.png"},
				},
			},
			wantStatus: http.StatusBadRequest,
			wantErrSub: "initial image input is not supported for video extend operation",
		},
		{
			name: "create operation with multiple image attachments rejected",
			payload: map[string]any{
				"title":     "Create Multi Image",
				"agent":     "video",
				"operation": "create",
				"attached_media": []map[string]any{
					{"id": "img-1", "kind": "image", "url": validImageDataURL, "filename": "frame1.png"},
					{"id": "img-2", "kind": "image", "url": validImageDataURL, "filename": "frame2.png"},
				},
			},
			wantStatus: http.StatusBadRequest,
			wantErrSub: "at most one initial image attachment is supported",
		},
		{
			name: "valid create operation persisted with operation=create",
			payload: map[string]any{
				"title":        "Valid Create Video",
				"agent":        "video",
				"operation":    "create",
				"aspect_ratio": "16:9",
			},
			wantStatus:   http.StatusCreated,
			wantOpInTask: "create",
		},
		{
			name: "valid edit operation persisted with operation=edit",
			payload: map[string]any{
				"title":        "Valid Edit Video",
				"agent":        "video",
				"operation":    "edit",
				"aspect_ratio": "16:9",
				"attached_media": []map[string]any{
					{"id": "vid-1", "kind": "video", "url": validVideoDataURL, "filename": "clip.mp4"},
				},
			},
			wantStatus:   http.StatusCreated,
			wantOpInTask: "edit",
		},
		{
			name: "valid extend operation persisted with operation=extend",
			payload: map[string]any{
				"title":        "Valid Extend Video",
				"agent":        "video",
				"operation":    "extend",
				"aspect_ratio": "16:9",
				"attached_media": []map[string]any{
					{"id": "vid-1", "kind": "video", "url": validVideoDataURL, "filename": "clip.mp4"},
				},
			},
			wantStatus:   http.StatusCreated,
			wantOpInTask: "extend",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.payload)
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v3/projects/%s/tasks", proj.ID), bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
			rec := httptest.NewRecorder()

			server.handleProjects(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantErrSub != "" {
				if !strings.Contains(rec.Body.String(), tc.wantErrSub) {
					t.Fatalf("expected error containing %q, got: %s", tc.wantErrSub, rec.Body.String())
				}
			}
			if tc.wantOpInTask != "" {
				var resp struct {
					Task pebblestore.ProjectTaskRecord `json:"task"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if resp.Task.Operation != tc.wantOpInTask {
					t.Errorf("task.Operation = %q, want %q", resp.Task.Operation, tc.wantOpInTask)
				}
			}
		})
	}
}

func TestVideoOperations_ModelResolutionAndVeoEditRejection(t *testing.T) {
	// Purpose:
	// - Requirement: Video edit tasks must resolve account-configured IterationModel,
	//   never fall back to DefaultModel or generation, and must explicitly reject Veo editing
	//   before any provider request is dispatched.
	// - Threat/regression: In prior versions, edit tasks fell back to the base generator model
	//   (Veo), leading to provider runtime errors or silent fresh generation.
	// - Boundary: Server.handleProjectTasks / validateProjectMediaTaskSettings.
	// - Layer: Direct API preflight with mocked UI settings and Pebble store.
	server, ss, p := setupDirectMediaTestServer(t)
	seedOmniVideoCatalogRecord(t, server)
	mockVideo := &mockPreflightVideoService{}
	server.SetVideoGenerationService(mockVideo)

	proj := &pebblestore.ProjectRecord{
		ID:        "proj-model-res-test",
		AccountID: p.AccountScopeID,
		Name:      "Model Resolution Test",
	}
	if err := ss.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatalf("put project: %v", err)
	}

	validVideoDataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte("fake-mp4-stream-data"))

	t.Run("edit without configured iteration model fails without generation fallback", func(t *testing.T) {
		uiChatStore := pebblestore.NewUISettingsStore(ss.Underlying())
		uiSvc := uisettings.NewService(uiChatStore)
		// Only DefaultModel is set; IterationModel is empty!
		_, _ = uiSvc.SetForAccount(p.AccountScopeID, uisettings.UISettings{
			Tools: uisettings.ToolSettings{
				Video: uisettings.ToolVideoSettings{
					DefaultModel:   "veo-3.1-generate-preview",
					IterationModel: "",
				},
			},
		})
		server.uiSettings = uiSvc
		mockVideo.uiSettings = uiSvc

		b, _ := json.Marshal(map[string]any{
			"title":     "Edit Without Iteration Model",
			"agent":     "video",
			"operation": "edit",
			"attached_media": []map[string]any{
				{"id": "vid-1", "kind": "video", "url": validVideoDataURL, "filename": "clip.mp4"},
			},
		})
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v3/projects/%s/tasks", proj.ID), bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()

		server.handleProjects(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "no default video iteration model configured") {
			t.Fatalf("expected iteration model error, got: %s", rec.Body.String())
		}
	})

	t.Run("edit with explicit Veo model rejected before provider dispatch", func(t *testing.T) {
		uiChatStore := pebblestore.NewUISettingsStore(ss.Underlying())
		uiSvc := uisettings.NewService(uiChatStore)
		_, _ = uiSvc.SetForAccount(p.AccountScopeID, uisettings.UISettings{
			Tools: uisettings.ToolSettings{
				Video: uisettings.ToolVideoSettings{
					DefaultModel:   "veo-3.1-generate-preview",
					IterationModel: "gemini-omni-video",
				},
			},
		})
		server.uiSettings = uiSvc
		mockVideo.uiSettings = uiSvc

		b, _ := json.Marshal(map[string]any{
			"title":     "Edit Explicit Veo",
			"agent":     "video",
			"operation": "edit",
			"model":     "veo-3.1-generate-preview",
			"attached_media": []map[string]any{
				{"id": "vid-1", "kind": "video", "url": validVideoDataURL, "filename": "clip.mp4"},
			},
		})
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v3/projects/%s/tasks", proj.ID), bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()

		server.handleProjects(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Veo models do not support video editing") {
			t.Fatalf("expected Veo edit rejection error, got: %s", rec.Body.String())
		}
	})
}

func TestVideoOperations_ServerProvenanceBindingNotClientHandles(t *testing.T) {
	// Purpose:
	// - Invariant: Resolving attached media references for video operations must authoritatively
	//   bind server-verified VideoProvenance from Pebble store; client-supplied interaction IDs,
	//   URIs, or timestamps must never be trusted.
	// - Threat/regression: Spoofed provider interaction IDs or URIs could cross tenant boundaries
	//   or cause unauthorized continuation.
	// - Boundary: Server.resolveSourceMediaRecord in projects_media_video_operations.go.
	// - Layer: Direct call against Server with real Pebble artifact variant storage.
	server, ss, p := setupDirectMediaTestServer(t)

	// 1. Seed a canonical session artifact variant with server-authored VideoProvenance
	sessionID := "sess-prov-test"
	variantID := "var-valid-video"
	collectionID := "coll-prov-test"
	testVideoBytes := []byte("valid-mp4-bytes-for-provenance-test")
	h := sha256.Sum256(testVideoBytes)
	testDigest := hex.EncodeToString(h[:])

	storedProv := &pebblestore.VideoProvenance{
		AccountScopeID:     p.AccountScopeID,
		Provider:           "google",
		Model:              "veo-3.1-generate-preview",
		Operation:          "create",
		OutputDigestSHA256: testDigest,
		ProviderResource:   "https://generativelanguage.googleapis.com/v1beta/files/veo-video-12345",
		CreatedAt:          time.Now().UnixMilli(),
		ExpiresAt:          time.Now().Add(48 * time.Hour).UnixMilli(),
		ObservedWidth:      1280,
		ObservedHeight:     720,
	}

	server.artifacts = artifact.NewRegistry(server.sessions, artifact.Limits{})
	auth := artifact.NewAuthority(server.artifacts, server.sessions)
	_ = ss.CreateSession(pebblestore.SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Mode:           "auto",
	})
	createdVariant, err := auth.Create(context.Background(), artifact.Principal{
		SessionID:      sessionID,
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
	}, artifact.CreateInput{
		RequestID:      "req-test-prov",
		CollectionID:   collectionID,
		CollectionName: "Provenance Test Collection",
		VariantID:      variantID,
		Filename:       "generated-clip.mp4",
		MediaType:      "video/mp4",
		Body:           testVideoBytes,
		AutoAccept:     true,
	})
	if err != nil {
		t.Fatalf("create artifact variant: %v", err)
	}
	createdVariant.Lineage.VideoProvenance = storedProv
	if err := ss.PutArtifactVariant(createdVariant); err != nil {
		t.Fatalf("put artifact variant: %v", err)
	}

	// 2. Resolve artifact with valid reference and pinned revision
	mediaRef := pebblestore.ProjectTaskMediaRef{
		ID:  "att-1",
		URL: fmt.Sprintf("/v3/sessions/%s/artifacts/%s?revision=%d", sessionID, variantID, createdVariant.EventSeq),
	}
	rec, err := server.resolveSourceMediaRecord(context.Background(), p, mediaRef, "video")
	if err != nil {
		t.Fatalf("unexpected error resolving source media record: %v", err)
	}
	if rec.Provenance == nil {
		t.Fatalf("expected non-nil VideoProvenance from server-stored variant")
	}
	if rec.Provenance.ProviderResource != storedProv.ProviderResource {
		t.Errorf("ProviderResource = %q, want %q", rec.Provenance.ProviderResource, storedProv.ProviderResource)
	}
	if rec.SourceLink == nil || rec.SourceLink.EventSeq != createdVariant.EventSeq {
		t.Errorf("expected SourceLink with EventSeq %d, got: %+v", createdVariant.EventSeq, rec.SourceLink)
	}

	// 3. Stale revision query is rejected
	mediaRefStale := pebblestore.ProjectTaskMediaRef{
		ID:  "att-2",
		URL: fmt.Sprintf("/v3/sessions/%s/artifacts/%s?revision=%d", sessionID, variantID, createdVariant.EventSeq+99),
	}
	_, errStale := server.resolveSourceMediaRecord(context.Background(), p, mediaRefStale, "video")
	if errStale == nil || !strings.Contains(errStale.Error(), "stale artifact revision") {
		t.Fatalf("expected stale revision error, got: %v", errStale)
	}

	// 3b. Missing revision query is rejected (pinning required)
	mediaRefMissingRev := pebblestore.ProjectTaskMediaRef{
		ID:  "att-missing-rev",
		URL: fmt.Sprintf("/v3/sessions/%s/artifacts/%s", sessionID, variantID),
	}
	_, errMissingRev := server.resolveSourceMediaRecord(context.Background(), p, mediaRefMissingRev, "video")
	if errMissingRev == nil || !strings.Contains(errMissingRev.Error(), "exact pinned revision") {
		t.Fatalf("expected exact pinned revision error, got: %v", errMissingRev)
	}

	// 4. Cross-account access is rejected
	otherPrincipal := identity.Principal{
		Type:           "user",
		UserID:         "other-user",
		AccountScopeID: "other-account-scope",
	}
	_, errCross := server.resolveSourceMediaRecord(context.Background(), otherPrincipal, mediaRef, "video")
	if errCross == nil || !strings.Contains(errCross.Error(), "not found in account scope") {
		t.Fatalf("expected cross-account rejection, got: %v", errCross)
	}

	// 5. Data URL (uploaded/untrusted video) produces Provenance == nil
	dataURLRef := pebblestore.ProjectTaskMediaRef{
		ID:  "att-3",
		URL: "data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte("user-uploaded-mp4")),
	}
	recUploaded, errUploaded := server.resolveSourceMediaRecord(context.Background(), p, dataURLRef, "video")
	if errUploaded != nil {
		t.Fatalf("resolve data url: %v", errUploaded)
	}
	if recUploaded.Provenance != nil {
		t.Errorf("expected Provenance == nil for uploaded video, got %+v", recUploaded.Provenance)
	}
	if recUploaded.SourceLink == nil || recUploaded.SourceLink.DigestSHA256 == "" {
		t.Errorf("expected SourceLink with DigestSHA256 for uploaded video")
	}
}

func TestVideoOperations_ConflictingDeclarationsRejected(t *testing.T) {
	// Purpose:
	// - Invariant: Mixed or conflicting attachment declarations (e.g. kind="image" but media_type="video/mp4")
	//   must be strictly rejected to prevent attachment type spoofing or misrouting.
	// - Boundary: Server.handleProjectTasks and resolveSourceMediaRecord.
	server, ss, p := setupDirectMediaTestServer(t)
	proj := &pebblestore.ProjectRecord{
		ID:        "proj-conflict-test",
		AccountID: p.AccountScopeID,
		Name:      "Conflict Test",
	}
	if err := ss.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatalf("put project: %v", err)
	}

	b, _ := json.Marshal(map[string]any{
		"title":     "Conflicting Media",
		"agent":     "video",
		"operation": "edit",
		"attached_media": []map[string]any{
			{
				"id":         "conf-1",
				"kind":       "image",
				"media_type": "video/mp4",
				"url":        "data:video/mp4;base64,ZmFrZQ==",
			},
		},
	})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v3/projects/%s/tasks", proj.ID), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()

	server.handleProjects(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for conflicting declaration, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "conflicting attachment declarations") {
		t.Fatalf("expected conflicting declaration error message, got: %s", rec.Body.String())
	}
}

func TestVideoOperations_ExecutionPreflightAndProvenancePersistence(t *testing.T) {
	// Purpose:
	// - Requirement: Direct video execution passes operation discriminator and verified
	//   SourceProvenance to GenerateManagedVideo, persists resulting VideoProvenance on
	//   each deliverable and the task, and ensures failures publish no partial ready output.
	// - Threat/regression: Dropped provenance or inconsistent partial ready states on failure.
	// - Boundary: Server.executeDirectMediaTask in projects_media.go.
	// - Layer: Direct task execution with mock service against Pebble store.
	server, ss, p := setupDirectMediaTestServer(t)
	seedOmniVideoCatalogRecord(t, server)

	outputProv := &pebblestore.VideoProvenance{
		AccountScopeID:   p.AccountScopeID,
		Provider:         "google",
		Model:            "gemini-omni-video",
		Operation:        "edit",
		InteractionID:    "interaction-omni-999",
		CreatedAt:        time.Now().UnixMilli(),
		IsCombinedOutput: false,
	}

	mockVideo := &mockPreflightVideoService{
		genResult: videogen.ManagedVideoResult{
			Bytes:           []byte("rendered-video-bytes"),
			MediaType:       "video/mp4",
			Model:           "gemini-omni-video",
			Provider:        "google",
			DurationSeconds: 8,
			Resolution:      "720p",
			Provenance:      outputProv,
		},
	}
	server.SetVideoGenerationService(mockVideo)

	uiChatStore := pebblestore.NewUISettingsStore(ss.Underlying())
	uiSvc := uisettings.NewService(uiChatStore)
	_, _ = uiSvc.SetForAccount(p.AccountScopeID, uisettings.UISettings{
		Tools: uisettings.ToolSettings{
			Video: uisettings.ToolVideoSettings{
				DefaultModel:   "veo-3.1-generate-preview",
				IterationModel: "gemini-omni-video",
			},
		},
	})
	server.uiSettings = uiSvc
	mockVideo.uiSettings = uiSvc

	proj := &pebblestore.ProjectRecord{
		ID:        "proj-exec-test",
		AccountID: p.AccountScopeID,
		Name:      "Execution Test Project",
	}
	if err := ss.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatalf("put project: %v", err)
	}

	validVideoDataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte("fake-mp4-stream-data"))

	task := &pebblestore.ProjectTaskRecord{
		ID:          "task-exec-1",
		ProjectID:   proj.ID,
		AccountID:   p.AccountScopeID,
		Title:       "Execute Video Edit",
		Description: "Make the lighting brighter",
		Status:      "in_progress",
		Agent:       "video",
		OutcomeType: "video_clip",
		Operation:   "edit",
		Model:       "gemini-omni-video",
		Resolution:  "720p",
		AttachedMedia: []pebblestore.ProjectTaskMediaRef{
			{ID: "vid-1", Kind: "video", URL: validVideoDataURL, Filename: "source.mp4"},
		},
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_1", Title: "Edit Take 1", Kind: "video", Status: "pending"},
		},
	}
	if err := ss.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatalf("put project task: %v", err)
	}

	server.executeDirectMediaTask(p, proj, task)

	// Verify task in DB
	updatedTask, found, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
	if err != nil || !found || updatedTask == nil {
		t.Fatalf("get project task: %v, found: %v", err, found)
	}

	if updatedTask.Status != "needs_review" {
		t.Errorf("task status = %q, want needs_review", updatedTask.Status)
	}
	if updatedTask.VideoProvenance == nil {
		t.Fatalf("expected task.VideoProvenance to be persisted")
	}
	if updatedTask.VideoProvenance.InteractionID != "interaction-omni-999" {
		t.Errorf("task VideoProvenance.InteractionID = %q, want interaction-omni-999", updatedTask.VideoProvenance.InteractionID)
	}
	if len(updatedTask.Deliverables) == 0 || updatedTask.Deliverables[0].Status != "ready" {
		t.Fatalf("expected deliverable ready, got: %+v", updatedTask.Deliverables)
	}
	if updatedTask.Deliverables[0].VideoProvenance == nil {
		t.Errorf("expected deliverable.VideoProvenance to be set")
	}

	// Verify mock received Operation == "edit"
	if mockVideo.lastReq.Operation != "edit" {
		t.Errorf("mock received Operation = %q, want edit", mockVideo.lastReq.Operation)
	}

	// Test client-facing redaction: HTTP response does not leak provider handles
	clientSafe := sanitizeProjectTaskForClient(updatedTask)
	if clientSafe.VideoProvenance.InteractionID != "" {
		t.Errorf("expected sanitized VideoProvenance.InteractionID to be empty for client response, got %q", clientSafe.VideoProvenance.InteractionID)
	}
}

func TestVideoOperations_ExecutionRejectsChangedSourceDigest(t *testing.T) {
	// Purpose:
	// - Invariant: If the source media content changes between task submission and execution,
	//   execution must fail instead of operating on mismatched or stale bytes.
	// - Boundary: Server.executeDirectMediaTask.
	server, ss, p := setupDirectMediaTestServer(t)
	seedOmniVideoCatalogRecord(t, server)

	proj := &pebblestore.ProjectRecord{
		ID:        "proj-digest-change-test",
		AccountID: p.AccountScopeID,
		Name:      "Digest Change Project",
	}
	if err := ss.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatalf("put project: %v", err)
	}

	originalBytes := []byte("original-video-stream-content")
	hOrig := sha256.Sum256(originalBytes)
	originalDigest := hex.EncodeToString(hOrig[:])

	modifiedBytes := []byte("modified-video-stream-content-different")
	modifiedVideoDataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(modifiedBytes)

	task := &pebblestore.ProjectTaskRecord{
		ID:                 "task-digest-test",
		ProjectID:          proj.ID,
		AccountID:          p.AccountScopeID,
		Title:              "Execute Video With Modified Digest",
		Status:             "in_progress",
		Agent:              "video",
		OutcomeType:        "video_clip",
		Operation:          "edit",
		Model:              "gemini-omni-video",
		SourceDigestSHA256: originalDigest, // Pinned at submission to originalDigest!
		AttachedMedia: []pebblestore.ProjectTaskMediaRef{
			{ID: "vid-mod", Kind: "video", URL: modifiedVideoDataURL, Filename: "source.mp4"},
		},
		Deliverables: []pebblestore.ProjectTaskDeliverable{
			{ID: "deliv_1", Title: "Edit Take 1", Kind: "video", Status: "pending"},
		},
	}
	if err := ss.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatalf("put project task: %v", err)
	}

	server.executeDirectMediaTask(p, proj, task)

	updatedTask, found, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, task.ID)
	if err != nil || !found || updatedTask == nil {
		t.Fatalf("get project task: %v", err)
	}
	if updatedTask.Status != "failed" {
		t.Errorf("task status = %q, want failed", updatedTask.Status)
	}
	if !strings.Contains(updatedTask.LastError, "source media digest changed since submission") {
		t.Errorf("expected LastError mentioning digest changed, got: %s", updatedTask.LastError)
	}
}

func TestVideoOperations_OrchestratorCreateThenExtendEndToEnd(t *testing.T) {
	// Purpose:
	// - Product invariant: Orchestrator creates one video task with a harmless brief, retaining
	//   ready output, exact deliverable reference, parameters, and server-verified provenance.
	//   A follow-up request performs native API extension using that exact reference, producing a distinct
	//   derived output with source lineage pointing to the first clip, while preserving the original.
	// - Threat/regression: In prior versions, extension requests were either not discriminated (defaulted
	//   to create/edit), lost provenance on round-trip, failed on Veo edit mismatch, or mutated the original.
	// - Boundary/authority: Server.handleProjectTasks, resolveSourceMediaRecord, executeDirectMediaTask.
	// - Layer: Direct API execution against temporary Pebble store and mock video service.
	server, ss, p := setupDirectMediaTestServer(t)

	// Seed Veo 3.1 model in catalog
	catStore := pebblestore.NewModelCatalogStore(ss.Underlying())
	veoRecord := pebblestore.ModelCatalogRecord{
		Provider:          "google",
		Model:             "veo-3.1-generate-preview",
		DisplayName:       "Google Veo 3.1",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text", "image", "video"}, Outputs: []string{"video"}},
		Media: &pebblestore.ModelCatalogMediaCapabilities{
			State:           pebblestore.ModelCatalogMediaStateSupported,
			ProviderSurface: "predict",
		},
		ProviderSpecific: json.RawMessage(`{"google":{"model_api_surface":"predict","video_generation":{"status":"verified","settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p"]},"duration_seconds":{"status":"verified","default_value":8,"supported_values":[4,6,8]}},"features":{"video_extension":true,"initial_image":{"status":"verified","supported":true,"max_inputs":1}}}}}`),
		Pricing:          json.RawMessage(`{"input_per_million":1.0,"output_per_million":4.0}`),
	}
	if err := catStore.SetRecord(veoRecord); err != nil {
		t.Fatalf("seed veo catalog record: %v", err)
	}

	uiChatStore := pebblestore.NewUISettingsStore(ss.Underlying())
	uiSvc := uisettings.NewService(uiChatStore)
	_, _ = uiSvc.SetForAccount(p.AccountScopeID, uisettings.UISettings{
		Tools: uisettings.ToolSettings{
			Video: uisettings.ToolVideoSettings{
				DefaultModel: "veo-3.1-generate-preview",
			},
		},
	})
	server.uiSettings = uiSvc

	proj := &pebblestore.ProjectRecord{
		ID:        "proj-orch-extend-test",
		AccountID: p.AccountScopeID,
		Name:      "Orchestrator Video Extension Project",
	}
	if err := ss.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatalf("put project: %v", err)
	}

	clip1Bytes := []byte("clip-1-mp4-payload-veo-original-8s")
	h1 := sha256.Sum256(clip1Bytes)
	clip1Digest := hex.EncodeToString(h1[:])
	clip1URI := "https://generativelanguage.googleapis.com/v1beta/files/veo-video-11111"

	clip2Bytes := []byte("clip-2-mp4-payload-veo-extended-16s-combined")
	h2 := sha256.Sum256(clip2Bytes)
	clip2Digest := hex.EncodeToString(h2[:])
	clip2URI := "https://generativelanguage.googleapis.com/v1beta/files/veo-video-22222"

	prov1 := &pebblestore.VideoProvenance{
		AccountScopeID:     p.AccountScopeID,
		Provider:           "google",
		Model:              "veo-3.1-generate-preview",
		Operation:          "create",
		OutputDigestSHA256: clip1Digest,
		ProviderResource:   clip1URI,
		CreatedAt:          time.Now().UnixMilli(),
		ExpiresAt:          time.Now().Add(48 * time.Hour).UnixMilli(),
		ObservedWidth:      1280,
		ObservedHeight:     720,
		ObservedDurationMs: 8000,
	}

	prov2 := &pebblestore.VideoProvenance{
		AccountScopeID:      p.AccountScopeID,
		Provider:            "google",
		Model:               "veo-3.1-generate-preview",
		Operation:           "extend",
		OutputDigestSHA256:  clip2Digest,
		ProviderResource:    clip2URI,
		IsCombinedOutput:    true,
		ExtensionCount:      1,
		ExtensionCountKnown: true,
		CreatedAt:           time.Now().UnixMilli(),
		ExpiresAt:           time.Now().Add(48 * time.Hour).UnixMilli(),
		ObservedWidth:       1280,
		ObservedHeight:      720,
		ObservedDurationMs:  16000,
	}

	mockVideo := &mockPreflightVideoService{
		genResult: videogen.ManagedVideoResult{
			Bytes:           clip1Bytes,
			MediaType:       "video/mp4",
			Model:           "veo-3.1-generate-preview",
			Provider:        "google",
			DurationSeconds: 8,
			Resolution:      "720p",
			Provenance:      prov1,
		},
		uiSettings: uiSvc,
	}
	server.SetVideoGenerationService(mockVideo)

	// Step 1: Orchestrator creates video 1 with a harmless brief
	body1, _ := json.Marshal(map[string]any{
		"title":        "Harmless smoke test blue orb",
		"prompt":       "A gentle glowing blue orb floating smoothly in a tranquil dark cosmos",
		"agent":        "video",
		"operation":    "create",
		"aspect_ratio": "16:9",
		"resolution":   "720p",
		"auto_approve": false,
	})
	req1 := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v3/projects/%s/tasks", proj.ID), bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	req1 = req1.WithContext(identity.ContextWithPrincipal(req1.Context(), p))
	rec1 := httptest.NewRecorder()
	server.handleProjects(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("task 1 creation failed (%d): %s", rec1.Code, rec1.Body.String())
	}
	var resp1 struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("unmarshal task 1 response: %v", err)
	}
	task1ID := resp1.Task.ID

	// Execute task 1 directly
	task1, found, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, task1ID)
	if err != nil || !found {
		t.Fatalf("get task 1: %v", err)
	}
	server.executeDirectMediaTask(p, proj, task1)

	// Verify task 1 output
	task1Updated, found, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, task1ID)
	if err != nil || !found {
		t.Fatalf("get task 1 after exec: %v", err)
	}
	if task1Updated.Status != "needs_review" {
		t.Fatalf("expected task 1 status needs_review, got %s", task1Updated.Status)
	}
	if len(task1Updated.Deliverables) == 0 || task1Updated.Deliverables[0].Status != "ready" {
		t.Fatalf("task 1 deliverable not ready: %+v", task1Updated.Deliverables)
	}
	deliv1 := task1Updated.Deliverables[0]
	if deliv1.VideoProvenance == nil || deliv1.VideoProvenance.ProviderResource != clip1URI {
		t.Fatalf("task 1 deliverable missing expected provenance: %+v", deliv1.VideoProvenance)
	}

	// Step 2: Orchestrator requests native extension of video 1 using its exact deliverable URL
	deliv1URL := fmt.Sprintf("/v3/projects/%s/tasks/%s/deliverables/%s", proj.ID, task1ID, deliv1.ID)

	// Update mock to return extended video 2
	mockVideo.genResult = videogen.ManagedVideoResult{
		Bytes:            clip2Bytes,
		MediaType:        "video/mp4",
		Model:            "veo-3.1-generate-preview",
		Provider:         "google",
		DurationSeconds:  16,
		Resolution:       "720p",
		Operation:        "extend",
		IsCombinedOutput: true,
		ExtensionCount:   1,
		Provenance:       prov2,
	}

	body2, _ := json.Marshal(map[string]any{
		"title":        "Extend smoke test blue orb scene",
		"prompt":       "Camera drifts forward as subtle starlight particles appear around the orb",
		"agent":        "video",
		"operation":    "extend",
		"aspect_ratio": "16:9",
		"resolution":   "720p",
		"attached_media": []map[string]any{
			{
				"id":       deliv1.ID,
				"kind":     "video",
				"url":      deliv1URL,
				"filename": "clip1.mp4",
			},
		},
		"auto_approve": false,
	})
	req2 := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v3/projects/%s/tasks", proj.ID), bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2 = req2.WithContext(identity.ContextWithPrincipal(req2.Context(), p))
	rec2 := httptest.NewRecorder()
	server.handleProjects(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("task 2 creation failed (%d): %s", rec2.Code, rec2.Body.String())
	}
	var resp2 struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("unmarshal task 2 response: %v", err)
	}
	task2ID := resp2.Task.ID

	// Verify task 2 was created with operation=extend and pinned SourceDigestSHA256
	task2, found, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, task2ID)
	if err != nil || !found {
		t.Fatalf("get task 2: %v", err)
	}
	if task2.Operation != "extend" {
		t.Fatalf("expected task 2 operation 'extend', got %q", task2.Operation)
	}
	if task2.SourceDigestSHA256 != clip1Digest {
		t.Fatalf("expected task 2 SourceDigestSHA256 %q, got %q", clip1Digest, task2.SourceDigestSHA256)
	}

	// Execute task 2
	server.executeDirectMediaTask(p, proj, task2)

	// Verify mock received Operation="extend" and Source with Provenance
	if mockVideo.lastReq.Operation != "extend" {
		t.Fatalf("mock expected Operation 'extend', got %q", mockVideo.lastReq.Operation)
	}
	if mockVideo.lastReq.Source == nil || mockVideo.lastReq.Source.Provenance == nil {
		t.Fatal("mock expected Source and SourceProvenance on extend request")
	}
	if mockVideo.lastReq.Source.Provenance.ProviderResource != clip1URI {
		t.Fatalf("expected source resource %q, got %q", clip1URI, mockVideo.lastReq.Source.Provenance.ProviderResource)
	}

	// Verify task 2 updated state
	task2Updated, found, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, task2ID)
	if err != nil || !found {
		t.Fatalf("get task 2 after exec: %v", err)
	}
	if task2Updated.Status != "needs_review" {
		t.Fatalf("expected task 2 status needs_review, got %s", task2Updated.Status)
	}
	if len(task2Updated.Deliverables) == 0 || task2Updated.Deliverables[0].Status != "ready" {
		t.Fatalf("task 2 deliverable not ready: %+v", task2Updated.Deliverables)
	}
	deliv2 := task2Updated.Deliverables[0]
	if deliv2.ParentDeliverableID != deliv1.ID {
		t.Fatalf("expected ParentDeliverableID %q, got %q", deliv1.ID, deliv2.ParentDeliverableID)
	}
	if deliv2.VideoProvenance == nil {
		t.Fatal("expected non-nil VideoProvenance on extended deliverable")
	}
	if deliv2.VideoProvenance.Operation != "extend" {
		t.Fatalf("expected extended provenance operation 'extend', got %q", deliv2.VideoProvenance.Operation)
	}
	if !deliv2.VideoProvenance.IsCombinedOutput {
		t.Fatal("expected IsCombinedOutput true on extended deliverable")
	}
	if deliv2.VideoProvenance.ExtensionCount != 1 {
		t.Fatalf("expected ExtensionCount 1, got %d", deliv2.VideoProvenance.ExtensionCount)
	}

	// Step 3: Invariant - original task 1 and deliverable 1 are completely preserved intact
	task1After, found, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, task1ID)
	if err != nil || !found {
		t.Fatalf("re-read task 1: %v", err)
	}
	if task1After.Status != "needs_review" || len(task1After.Deliverables) != 1 || task1After.Deliverables[0].Status != "ready" {
		t.Fatalf("original task 1 mutated: %+v", task1After)
	}
	if task1After.Deliverables[0].VideoProvenance.Operation != "create" {
		t.Fatalf("original task 1 provenance mutated: %+v", task1After.Deliverables[0].VideoProvenance)
	}

	// Step 4: Verify reload and persistence of both clips
	reloadedTask1, found, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, task1ID)
	if err != nil || !found {
		t.Fatalf("reload task 1 from store: %v", err)
	}
	if reloadedTask1.Deliverables[0].VideoProvenance.OutputDigestSHA256 != clip1Digest {
		t.Fatalf("reloaded task 1 digest mismatch: %q vs %q", reloadedTask1.Deliverables[0].VideoProvenance.OutputDigestSHA256, clip1Digest)
	}

	reloadedTask2, found, err := ss.GetProjectTask(p.AccountScopeID, proj.ID, task2ID)
	if err != nil || !found {
		t.Fatalf("reload task 2 from store: %v", err)
	}
	if reloadedTask2.Deliverables[0].VideoProvenance.OutputDigestSHA256 != clip2Digest {
		t.Fatalf("reloaded task 2 digest mismatch: %q vs %q", reloadedTask2.Deliverables[0].VideoProvenance.OutputDigestSHA256, clip2Digest)
	}
	if reloadedTask2.Deliverables[0].ParentDeliverableID != deliv1.ID {
		t.Fatalf("reloaded task 2 parent deliverable link lost: %q vs %q", reloadedTask2.Deliverables[0].ParentDeliverableID, deliv1.ID)
	}
}

// Purpose: validateProjectMediaTaskSettings must use the iteration settings slot
// for a server-resolved Omni source, matching videogen preflight rather than the
// creation default. The API validator plus real artifact authority is the narrowest
// layer that proves selection uses stored provenance, not client model claims.
func TestVideoOperations_OmniContinuationSelection(t *testing.T) {
	server, ss, p := setupDirectMediaTestServer(t)
	catalog := pebblestore.NewModelCatalogStore(ss.Underlying())
	if err := catalog.SetRecord(pebblestore.ModelCatalogRecord{
		Provider: "google", Model: "gemini-omni-1.1-flash",
		CatalogModalities: pebblestore.ModelCatalogModalities{Outputs: []string{"video"}},
		ProviderSpecific:  json.RawMessage(`{"google":{"video_generation":{"settings":{"resolution":{"status":"verified","supported_values":["720p"]}},"features":{"video_extension":{"status":"verified","supported":true}}}}}`),
	}); err != nil {
		t.Fatal(err)
	}
	server.artifacts = artifact.NewRegistry(server.sessions, artifact.Limits{})
	auth := artifact.NewAuthority(server.artifacts, server.sessions)
	const sessionID = "continuation-selection-session"
	if err := ss.CreateSession(pebblestore.SessionSnapshot{ID: sessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	v, err := auth.Create(context.Background(), artifact.Principal{SessionID: sessionID, AccountScopeID: p.AccountScopeID, UserID: p.UserID}, artifact.CreateInput{
		RequestID: "continuation-selection", CollectionID: "videos", CollectionName: "Videos", VariantID: "source",
		Filename: "source.mp4", MediaType: "video/mp4", Body: []byte("test-video"), AutoAccept: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	v.Lineage.VideoProvenance = &pebblestore.VideoProvenance{AccountScopeID: p.AccountScopeID, Model: "gemini-omni-1.1-flash", InteractionID: "retained-interaction"}
	if err := ss.PutArtifactVariant(v); err != nil {
		t.Fatal(err)
	}
	ui := uisettings.NewService(pebblestore.NewUISettingsStore(ss.Underlying()))
	server.uiSettings = ui
	for _, tc := range []struct {
		name, iteration, explicit string
		wantErr                   bool
	}{
		{name: "default alone cannot select continuation", wantErr: true},
		{name: "configured iteration", iteration: "gemini-omni-1.1-flash"},
		{name: "explicit selection", explicit: "gemini-omni-1.1-flash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ui.SetForAccount(p.AccountScopeID, uisettings.UISettings{Tools: uisettings.ToolSettings{Video: uisettings.ToolVideoSettings{
				DefaultModel: "veo-3.1-generate-preview", IterationModel: tc.iteration,
			}}}); err != nil {
				t.Fatal(err)
			}
			task := &pebblestore.ProjectTaskRecord{Agent: "video", Operation: "extend", Model: tc.explicit,
				AttachedMedia: []pebblestore.ProjectTaskMediaRef{{ID: "source", Kind: "video", Filename: "source.mp4", URL: fmt.Sprintf("/v3/sessions/%s/artifacts/%s?revision=%d", sessionID, v.ID, v.EventSeq)}},
			}
			err := validateProjectMediaTaskSettings(server, task, p)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "no default video iteration model configured") {
					t.Fatalf("expected iteration configuration rejection, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if task.Model != tc.explicit || task.Operation != "extend" {
				t.Fatalf("validator mutated selection: %+v", task)
			}
		})
	}
}
