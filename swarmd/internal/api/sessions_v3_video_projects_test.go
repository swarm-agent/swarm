package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videoproject"
)

func TestSessionsV3VideoProjectWorkflow(t *testing.T) {
	server, sessionSvc, _, _, _, _, _ := newLegacyArtifactImportFixture(t, "clip.mp4", "")
	store := sessionSvc.Store()
	videoProjectSvc := videoproject.NewService(store)
	server.SetVideoProjectService(videoProjectSvc)

	principal := testPrincipal()

	createdSession, err := store.CreateSession(pebblestore.CreateSessionInput{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		Title:          "Video Session",
		WorkspacePath:  "/workspace/test",
		Metadata: map[string]any{
			"workspace_id": "ws-1",
		},
	})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// 1. POST /v3/sessions/{id}/video/projects -> create video project
	createProjectBody, _ := json.Marshal(sessionV3CreateVideoProjectRequest{
		ProjectID:    "vproj-1",
		Title:        "Intro Video",
		Description:  "Marketing launch video",
		OutputPreset: "landscape_1080p",
		InitialTimeline: &pebblestore.VideoProjectTimeline{
			OutputPreset: "landscape_1080p",
			Clips: []pebblestore.VideoTimelineClip{
				{
					ID:            "clip-1",
					Name:          "intro.mp4",
					SourceKind:    "source_video",
					SourceRef:     "vref-1",
					SourceStartMs: 0,
					SourceEndMs:   5000,
					DurationMs:    5000,
					Visible:       true,
				},
			},
		},
	})

	createReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+createdSession.ID+"/video/projects", bytes.NewReader(createProjectBody))
	createRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(createRec, createReq.WithContext(ContextWithPrincipal(createReq.Context(), principal)))

	if createRec.Code != http.StatusCreated {
		t.Fatalf("create video project status = %d, want 201. body = %s", createRec.Code, createRec.Body.String())
	}

	var createResp struct {
		OK       bool                                      `json:"ok"`
		Project  pebblestore.VideoProjectSnapshot          `json:"project"`
		Revision *pebblestore.VideoProjectRevisionSnapshot `json:"revision"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if !createResp.OK || createResp.Project.ID != "vproj-1" || createResp.Project.Title != "Intro Video" {
		t.Fatalf("unexpected project in response: %+v", createResp.Project)
	}
	if createResp.Revision == nil || createResp.Revision.RevisionNumber != 1 {
		t.Fatalf("expected initial revision number 1, got: %+v", createResp.Revision)
	}

	// 2. GET /v3/sessions/{id}/video/projects -> list projects
	listReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+createdSession.ID+"/video/projects", nil)
	listRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(listRec, listReq.WithContext(ContextWithPrincipal(listReq.Context(), principal)))

	if listRec.Code != http.StatusOK {
		t.Fatalf("list projects status = %d, want 200", listRec.Code)
	}
	var listResp struct {
		OK       bool                               `json:"ok"`
		Projects []pebblestore.VideoProjectSnapshot `json:"projects"`
		Count    int                                `json:"count"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if listResp.Count != 1 || listResp.Projects[0].ID != "vproj-1" {
		t.Fatalf("unexpected listed projects: %+v", listResp.Projects)
	}

	// 3. GET /v3/sessions/{id}/video/projects/{project_id} -> get project detail
	getReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+createdSession.ID+"/video/projects/vproj-1", nil)
	getRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(getRec, getReq.WithContext(ContextWithPrincipal(getReq.Context(), principal)))

	if getRec.Code != http.StatusOK {
		t.Fatalf("get project status = %d, want 200", getRec.Code)
	}

	// 4. POST /v3/sessions/{id}/video/projects/{project_id}/revisions -> create new revision
	createRevBody, _ := json.Marshal(sessionV3CreateVideoProjectRevisionRequest{
		Description:   "Added outro clip",
		ChangeSummary: "Trimmed intro to 4s and added outro",
		Timeline: pebblestore.VideoProjectTimeline{
			OutputPreset: "landscape_1080p",
			Clips: []pebblestore.VideoTimelineClip{
				{
					ID:            "clip-1",
					Name:          "intro.mp4",
					SourceKind:    "source_video",
					SourceRef:     "vref-1",
					SourceStartMs: 0,
					SourceEndMs:   4000,
					DurationMs:    4000,
					Visible:       true,
				},
				{
					ID:            "clip-2",
					Name:          "outro.mp4",
					SourceKind:    "source_video",
					SourceRef:     "vref-2",
					SourceStartMs: 0,
					SourceEndMs:   3000,
					DurationMs:    3000,
					Visible:       true,
				},
			},
		},
	})
	revReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+createdSession.ID+"/video/projects/vproj-1/revisions", bytes.NewReader(createRevBody))
	revRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(revRec, revReq.WithContext(ContextWithPrincipal(revReq.Context(), principal)))

	if revRec.Code != http.StatusCreated {
		t.Fatalf("create revision status = %d, want 201. body = %s", revRec.Code, revRec.Body.String())
	}
	var revResp struct {
		OK       bool                                     `json:"ok"`
		Revision pebblestore.VideoProjectRevisionSnapshot `json:"revision"`
		Project  pebblestore.VideoProjectSnapshot         `json:"project"`
	}
	if err := json.Unmarshal(revRec.Body.Bytes(), &revResp); err != nil {
		t.Fatalf("unmarshal revision response: %v", err)
	}
	if revResp.Revision.RevisionNumber != 2 || len(revResp.Revision.Timeline.Clips) != 2 {
		t.Fatalf("unexpected revision: %+v", revResp.Revision)
	}

	// 5. GET /v3/sessions/{id}/video/projects/{project_id}/revisions -> list revisions
	listRevsReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+createdSession.ID+"/video/projects/vproj-1/revisions", nil)
	listRevsRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(listRevsRec, listRevsReq.WithContext(ContextWithPrincipal(listRevsReq.Context(), principal)))

	if listRevsRec.Code != http.StatusOK {
		t.Fatalf("list revisions status = %d, want 200", listRevsRec.Code)
	}
	var listRevsResp struct {
		OK        bool                                       `json:"ok"`
		Revisions []pebblestore.VideoProjectRevisionSnapshot `json:"revisions"`
		Count     int                                        `json:"count"`
	}
	if err := json.Unmarshal(listRevsRec.Body.Bytes(), &listRevsResp); err != nil {
		t.Fatalf("unmarshal list revisions response: %v", err)
	}
	if listRevsResp.Count != 2 {
		t.Fatalf("expected 2 revisions, got: %d", listRevsResp.Count)
	}

	// Restoring an exact immutable revision creates a new head and records its source.
	restoreReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+createdSession.ID+"/video/projects/vproj-1/revisions/"+createResp.Revision.ID+"/restore", bytes.NewReader([]byte(`{"change_summary":"Restore original cut"}`)))
	restoreRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(restoreRec, restoreReq.WithContext(ContextWithPrincipal(restoreReq.Context(), principal)))
	if restoreRec.Code != http.StatusCreated {
		t.Fatalf("restore revision status = %d, want 201. body = %s", restoreRec.Code, restoreRec.Body.String())
	}
	var restoreResp struct {
		Revision pebblestore.VideoProjectRevisionSnapshot `json:"revision"`
		Project  pebblestore.VideoProjectSnapshot         `json:"project"`
	}
	if err := json.Unmarshal(restoreRec.Body.Bytes(), &restoreResp); err != nil {
		t.Fatal(err)
	}
	if restoreResp.Revision.RestoredFromRevisionID != createResp.Revision.ID || restoreResp.Revision.ParentRevisionID != revResp.Revision.ID || restoreResp.Project.CurrentRevisionID != restoreResp.Revision.ID {
		t.Fatalf("unexpected restored revision: %+v project=%+v", restoreResp.Revision, restoreResp.Project)
	}

	// 6. POST /v3/sessions/{id}/video/projects/{project_id}/render -> start render job
	renderReqBody, _ := json.Marshal(sessionV3StartVideoRenderRequest{
		JobID: "job-1",
	})
	renderReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+createdSession.ID+"/video/projects/vproj-1/render", bytes.NewReader(renderReqBody))
	renderRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(renderRec, renderReq.WithContext(ContextWithPrincipal(renderReq.Context(), principal)))

	if renderRec.Code != http.StatusAccepted {
		t.Fatalf("start render status = %d, want 202. body = %s", renderRec.Code, renderRec.Body.String())
	}
	var renderResp struct {
		OK        bool                               `json:"ok"`
		RenderJob pebblestore.VideoRenderJobSnapshot `json:"render_job"`
	}
	if err := json.Unmarshal(renderRec.Body.Bytes(), &renderResp); err != nil {
		t.Fatalf("unmarshal render response: %v", err)
	}
	if renderResp.RenderJob.ID != "job-1" || renderResp.RenderJob.Status != pebblestore.VideoRenderJobStatusQueued {
		t.Fatalf("unexpected render job: %+v", renderResp.RenderJob)
	}

	// 7. GET /v3/sessions/{id}/video/render-jobs/{job_id} -> get render job
	jobReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+createdSession.ID+"/video/render-jobs/job-1", nil)
	jobRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(jobRec, jobReq.WithContext(ContextWithPrincipal(jobReq.Context(), principal)))

	if jobRec.Code != http.StatusOK {
		t.Fatalf("get render job status = %d, want 200", jobRec.Code)
	}
}

func TestSessionsV3PrimaryVideoProjectDiscovery(t *testing.T) {
	server, sessionSvc, _, _, _, _, _ := newLegacyArtifactImportFixture(t, "clip.mp4", "")
	store := sessionSvc.Store()
	server.SetVideoProjectService(videoproject.NewService(store))
	principal := testPrincipal()
	created, err := store.CreateSession(pebblestore.CreateSessionInput{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Title: "Video Tool", WorkspacePath: "/workspace/test"})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewReader([]byte(`{"title":"Shared project","output_preset":"landscape_1080p"}`))
	createReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/video/projects/primary", body)
	createRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(createRec, createReq.WithContext(ContextWithPrincipal(createReq.Context(), principal)))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create primary status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	getReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID+"/video/projects/primary", nil)
	getRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(getRec, getReq.WithContext(ContextWithPrincipal(getReq.Context(), principal)))
	if getRec.Code != http.StatusOK || !bytes.Contains(getRec.Body.Bytes(), []byte(`"project_kind":"video_tool"`)) {
		t.Fatalf("discover primary status=%d body=%s", getRec.Code, getRec.Body.String())
	}
}

func TestSessionsV3VideoProjectSecurityAndValidation(t *testing.T) {
	server, sessionSvc, _, _, _, _, _ := newLegacyArtifactImportFixture(t, "clip.mp4", "")
	store := sessionSvc.Store()
	videoProjectSvc := videoproject.NewService(store)
	server.SetVideoProjectService(videoProjectSvc)

	principal := testPrincipal()

	// 1. Unauthorized principal
	req := httptest.NewRequest(http.MethodGet, "/v3/sessions/session-1/video/projects", nil)
	rec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	// 2. Nonexistent session
	req = httptest.NewRequest(http.MethodGet, "/v3/sessions/nonexistent-session/video/projects", nil)
	rec = httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(rec, req.WithContext(ContextWithPrincipal(req.Context(), principal)))
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want error status", rec.Code)
	}

	// 3. Export escaping workspace
	exportBody, _ := json.Marshal(sessionV3ExportVideoRequest{
		DestinationPath: "/etc/forbidden.mp4",
	})
	exportReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/session-1/video/projects/vproj-1/export", bytes.NewReader(exportBody))
	exportRec := httptest.NewRecorder()
	server.handleSessionV3PrimaryByID(exportRec, exportReq.WithContext(ContextWithPrincipal(exportReq.Context(), principal)))
	if exportRec.Code != http.StatusBadRequest && exportRec.Code != http.StatusNotFound {
		t.Fatalf("export path escaping workspace status = %d, want error", exportRec.Code)
	}
}
