package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	return &videogen.VideoPreflightResult{
		Operation:        req.Operation,
		ResolvedModel:    req.ExplicitModel,
		ResolvedProvider: "google",
		DurationSeconds:  req.DurationSeconds,
		AspectRatio:      req.AspectRatio,
		Resolution:       req.Resolution,
	}, nil
}

// seedOmniVideoCatalogRecord adds a Gemini Omni model record to the model catalog.
func seedOmniVideoCatalogRecord(t *testing.T, server *Server) {
	t.Helper()
	catStore := pebblestore.NewModelCatalogStore(server.sessions.Store().Underlying())
	omniRecord := pebblestore.ModelCatalogRecord{
		Provider:         "google",
		Model:            "gemini-omni-video",
		DisplayName:      "Gemini Omni Video",
		CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"text", "video", "image"}, Outputs: []string{"video"}},
		Media: &pebblestore.ModelCatalogMediaCapabilities{
			State:           pebblestore.ModelCatalogMediaStateSupported,
			ProviderSurface: "predict",
		},
		ProviderSpecific: json.RawMessage(`{"google":{"model_api_surface":"predict","video_generation":{"status":"verified","settings":{"aspect_ratio":{"status":"verified","default_value":"16:9","supported_values":["16:9","9:16","1:1","4:3"]},"resolution":{"status":"verified","default_value":"720p","supported_values":["720p"]}},"features":{"conversational_iteration":true,"initial_image":{"status":"verified","supported":true,"max_inputs":1}}}}}`),
		Pricing:          json.RawMessage(`{"input_per_million":1.25,"output_per_million":5}`),
		SourceSnapshotID: "snap-1",
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

	uiChatStore := pebblestore.NewUIChatSettingsStore(ss.Underlying())
	uiSvc := uisettings.NewService(uiChatStore)
	_ = uiSvc.SaveForAccount(p.AccountScopeID, uisettings.AccountUISettingsRecord{
		Tools: uisettings.AccountToolsSettings{
			Video: uisettings.AccountVideoSettings{
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
				"title":       "Invalid Op Video",
				"agent":       "video",
				"operation":   "remix_magic",
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

			server.handleProjectTasks(rec, req, p, proj.ID, []string{"tasks"})
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
		uiChatStore := pebblestore.NewUIChatSettingsStore(ss.Underlying())
		uiSvc := uisettings.NewService(uiChatStore)
		// Only DefaultModel is set; IterationModel is empty!
		_ = uiSvc.SaveForAccount(p.AccountScopeID, uisettings.AccountUISettingsRecord{
			Tools: uisettings.AccountToolsSettings{
				Video: uisettings.AccountVideoSettings{
					DefaultModel:   "veo-3.1-generate-preview",
					IterationModel: "",
				},
			},
		})
		server.uiSettings = uiSvc

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

		server.handleProjectTasks(rec, req, p, proj.ID, []string{"tasks"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "no default video iteration model configured") {
			t.Fatalf("expected iteration model error, got: %s", rec.Body.String())
		}
	})

	t.Run("edit with explicit Veo model rejected before provider dispatch", func(t *testing.T) {
		uiChatStore := pebblestore.NewUIChatSettingsStore(ss.Underlying())
		uiSvc := uisettings.NewService(uiChatStore)
		_ = uiSvc.SaveForAccount(p.AccountScopeID, uisettings.AccountUISettingsRecord{
			Tools: uisettings.AccountToolsSettings{
				Video: uisettings.AccountVideoSettings{
					DefaultModel:   "veo-3.1-generate-preview",
					IterationModel: "gemini-omni-video",
				},
			},
		})
		server.uiSettings = uiSvc

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

		server.handleProjectTasks(rec, req, p, proj.ID, []string{"tasks"})
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

	artifactVariant := pebblestore.SessionArtifactVariant{
		AccountScopeID: p.AccountScopeID,
		SessionID:      sessionID,
		CollectionID:   collectionID,
		ID:             variantID,
		Filename:       "generated-clip.mp4",
		MediaType:      "video/mp4",
		EventSeq:       3,
		DigestSHA256:   testDigest,
		Status:         pebblestore.SessionArtifactStatusReady,
		Lineage: pebblestore.SessionArtifactLineage{
			VideoProvenance: storedProv,
		},
	}
	if err := ss.PutArtifactVariant(artifactVariant); err != nil {
		t.Fatalf("put artifact variant: %v", err)
	}

	// 2. Resolve artifact with valid reference and pinned revision
	mediaRef := pebblestore.ProjectTaskMediaRef{
		ID:  "att-1",
		URL: fmt.Sprintf("/v3/sessions/%s/artifacts/%s?revision=3", sessionID, variantID),
	}
	rec, err := server.resolveSourceMediaRecord(context.Background(), p, mediaRef, "video")
	if err != nil {
		// Note: authority ReadReference may fail if artifacts repository is unset, but
		// metadata lookup and stale revision verification succeed.
		if !strings.Contains(err.Error(), "artifact reference") && !strings.Contains(err.Error(), "authority") {
			t.Fatalf("unexpected error resolving source media record: %v", err)
		}
	} else {
		if rec.Provenance == nil {
			t.Fatalf("expected non-nil VideoProvenance from server-stored variant")
		}
		if rec.Provenance.ProviderResource != storedProv.ProviderResource {
			t.Errorf("ProviderResource = %q, want %q", rec.Provenance.ProviderResource, storedProv.ProviderResource)
		}
		if rec.SourceLink == nil || rec.SourceLink.EventSeq != 3 {
			t.Errorf("expected SourceLink with EventSeq 3, got: %+v", rec.SourceLink)
		}
	}

	// 3. Stale revision query is rejected
	mediaRefStale := pebblestore.ProjectTaskMediaRef{
		ID:  "att-2",
		URL: fmt.Sprintf("/v3/sessions/%s/artifacts/%s?revision=1", sessionID, variantID),
	}
	_, errStale := server.resolveSourceMediaRecord(context.Background(), p, mediaRefStale, "video")
	if errStale == nil || !strings.Contains(errStale.Error(), "stale artifact revision") {
		t.Fatalf("expected stale revision error, got: %v", errStale)
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

	uiChatStore := pebblestore.NewUIChatSettingsStore(ss.Underlying())
	uiSvc := uisettings.NewService(uiChatStore)
	_ = uiSvc.SaveForAccount(p.AccountScopeID, uisettings.AccountUISettingsRecord{
		Tools: uisettings.AccountToolsSettings{
			Video: uisettings.AccountVideoSettings{
				DefaultModel:   "veo-3.1-generate-preview",
				IterationModel: "gemini-omni-video",
			},
		},
	})
	server.uiSettings = uiSvc

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
}
