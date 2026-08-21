package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videoproject"
	"swarm/packages/swarmd/internal/videorender"
	"swarm/packages/swarmd/internal/videosource"
	"swarm/packages/swarmd/internal/videotranscription"
	"swarm/packages/swarmd/internal/workspace"
)

func TestManageVideoDefinitionExposesOnlyOpaqueReferences(t *testing.T) {
	raw, err := json.Marshal(manageVideoDefinition().Parameters)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"workspace_path", "root_path", "file_path", "provider_uri", "provider", "model"} {
		if strings.Contains(text, `"`+forbidden+`"`) {
			t.Fatalf("manage_video schema exposes forbidden field %q", forbidden)
		}
	}
	for _, required := range []string{"source_root_ref", "relative_path", "video_refs", "job_refs", "job_ref", "transcript_ref", "source_fingerprint", "focus_notes", "start_ms", "end_ms", "include_index", "index_only", "base_revision_id", "operations", "affected_ranges"} {
		if !strings.Contains(text, `"`+required+`"`) {
			t.Fatalf("manage_video schema lacks %q", required)
		}
	}
}

func TestManageVideoDefinitionExposesAdaptiveJobInstructions(t *testing.T) {
	definition := manageVideoDefinition()
	raw, err := json.Marshal(definition.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{"job-specific instructions from the initiating user or AI", "Silent software demo", "dense play-by-play"} {
		if !strings.Contains(text, required) {
			t.Fatalf("manage_video focus_notes description missing %q: %s", required, text)
		}
	}
}

func TestManageVideoDefinitionExposesSourceNavigationWorkflow(t *testing.T) {
	definition := manageVideoDefinition()
	if !strings.Contains(definition.Description, "registered source-video folders") || !strings.Contains(definition.Description, "selected opaque video references") || !strings.Contains(definition.Description, "inspect triggering-message attachments") {
		t.Fatalf("description does not expose source workflow: %s", definition.Description)
	}
	raw, err := json.Marshal(definition.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, action := range []string{"list_source_roots", "browse_source", "start_transcription"} {
		if !strings.Contains(text, `"`+action+`"`) {
			t.Fatalf("schema lacks action %q", action)
		}
	}
}

func TestManageVideoListsRegisteredSourcesWithoutTriggerAttachment(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-source.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "session-1", UserID: "user-1", AccountScopeID: "account-1"}
	workspacePath, mediaPath := t.TempDir(), t.TempDir()
	workspaceService := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	if _, err := workspaceService.AddForPrincipal(principal, workspacePath, "workspace", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceService.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, mediaPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaPath, "clip.mp4"), []byte("synthetic video"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "session-1", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: workspacePath, Mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	videoService := &fakeManageVideoService{}
	runtime.video = videoService
	runtime.videoSources = videosource.NewService(workspaceService, sessionStore)
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "session-1", RunID: "run-1"})
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, WorkspaceScope{SessionID: "session-1", Principal: principal}, Call{CallID: "call-1", Name: "manage_video", Arguments: `{"action":"list_source_roots"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"count":1`) || !strings.Contains(payload, "videosource_root_") || strings.Contains(payload, `"message_id"`) {
		t.Fatalf("payload=%s", payload)
	}
	var rootsResponse struct {
		Roots []struct {
			Ref string `json:"ref"`
		} `json:"roots"`
	}
	if err := json.Unmarshal([]byte(payload), &rootsResponse); err != nil || len(rootsResponse.Roots) != 1 {
		t.Fatalf("decode roots payload=%s err=%v", payload, err)
	}
	browseArgs, _ := json.Marshal(map[string]any{"action": "browse_source", "source_root_ref": rootsResponse.Roots[0].Ref})
	payload, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, WorkspaceScope{SessionID: "session-1", Principal: principal}, Call{CallID: "call-2", Name: "manage_video", Arguments: string(browseArgs)})
	if err != nil {
		t.Fatal(err)
	}
	var browseResponse struct {
		Videos []struct {
			Ref string `json:"ref"`
		} `json:"videos"`
	}
	if err := json.Unmarshal([]byte(payload), &browseResponse); err != nil || len(browseResponse.Videos) != 1 {
		t.Fatalf("decode browse payload=%s err=%v", payload, err)
	}
	focusNotes := "Silent software demo; narrate each visible cursor action and UI state change"
	startArgs, _ := json.Marshal(map[string]any{"action": "start_transcription", "video_refs": []string{browseResponse.Videos[0].Ref}, "focus_notes": focusNotes})
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, WorkspaceScope{SessionID: "session-1", Principal: principal}, Call{CallID: "call-3", Name: "manage_video", Arguments: string(startArgs)}); err != nil {
		t.Fatal(err)
	}
	if videoService.sourceCount != 1 {
		t.Fatalf("selected source count=%d, want 1", videoService.sourceCount)
	}
	if videoService.focusNotes != focusNotes {
		t.Fatalf("focus notes=%q, want %q", videoService.focusNotes, focusNotes)
	}
}

func TestManageVideoRequiresTrustedRunContext(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "session-1", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: "/workspace", Mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(sessionStore, events)
	runtime := NewRuntime(1)
	runtime.sessions = sessions
	runtime.video = &fakeManageVideoService{}
	runtime.videoSources = videosource.NewService(workspace.NewService(pebblestore.NewWorkspaceStore(store)), sessionStore)
	scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "session-1", UserID: "user-1", AccountScopeID: "account-1"}}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, Call{CallID: "call-1", Name: "manage_video", Arguments: `{"action":"inspect_attachments"}`})
	if err == nil || !strings.Contains(err.Error(), "trusted run authority") {
		t.Fatalf("error = %v, want trusted run context rejection", err)
	}
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "session-1", RunID: "run-1"})
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-2", Name: "manage_video", Arguments: `{"action":"inspect_attachments"}`})
	if err == nil || !strings.Contains(err.Error(), "trusted triggering message authority") {
		t.Fatalf("error = %v, want attachment action to require triggering message", err)
	}
}

type fakeManageVideoService struct {
	focusNotes  string
	sourceCount int
}

func (f *fakeManageVideoService) StartRegisteredSources(_ context.Context, _ identity.Principal, _ string, sources []pebblestore.SessionVideoAttachmentReference, focusNotes string) (videotranscription.StartResult, error) {
	f.focusNotes = focusNotes
	f.sourceCount = len(sources)
	return videotranscription.StartResult{}, nil
}

func (f *fakeManageVideoService) StartWithFocus(_ context.Context, _ identity.Principal, _, _, focusNotes string) (videotranscription.StartResult, error) {
	f.focusNotes = focusNotes
	return videotranscription.StartResult{}, nil
}
func (*fakeManageVideoService) Status(identity.Principal, string, []string) ([]pebblestore.TranscriptionJob, error) {
	return nil, nil
}
func (*fakeManageVideoService) Read(identity.Principal, string, string) (pebblestore.NormalizedTranscript, error) {
	return pebblestore.NormalizedTranscript{}, nil
}
func (*fakeManageVideoService) ReadByWorkspace(identity.Principal, string, string) (pebblestore.NormalizedTranscript, error) {
	return pebblestore.NormalizedTranscript{}, nil
}
func (*fakeManageVideoService) ReadBySourceFingerprint(identity.Principal, string, string) (pebblestore.NormalizedTranscript, error) {
	return pebblestore.NormalizedTranscript{}, nil
}
func (*fakeManageVideoService) Cancel(identity.Principal, string, string) (pebblestore.TranscriptionJob, error) {
	return pebblestore.TranscriptionJob{}, nil
}
func (*fakeManageVideoService) SourceName(identity.Principal, string, string) (string, error) {
	return "", nil
}

func TestManageVideoDefinitionExposesProjectAndRenderWorkflow(t *testing.T) {
	definition := manageVideoDefinition()
	for _, required := range []string{"One-shot initial-plan workflow", "create_project without initial_timeline", "propose_plan creates only a pending whole-plan review object"} {
		if !strings.Contains(definition.Description, required) {
			t.Fatalf("manage_video description lacks %q: %s", required, definition.Description)
		}
	}
	raw, err := json.Marshal(definition.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, action := range []string{"create_project", "read_project", "get_project", "list_projects", "create_edit_proposal", "propose_plan", "create_revision", "restore_revision", "start_render", "render_status", "cancel_render"} {
		if !strings.Contains(text, `"`+action+`"`) {
			t.Fatalf("schema lacks video project/render action %q", action)
		}
	}
	for _, param := range []string{"project_id", "revision_id", "source_revision_id", "render_job_id", "queue_grace_ms", "title", "description", "output_preset", "change_summary", "timeline", "initial_timeline", "metadata"} {
		if !strings.Contains(text, `"`+param+`"`) {
			t.Fatalf("schema lacks video project/render parameter %q", param)
		}
	}
	for _, forbidden := range []string{"workspace_path", "root_path", "file_path", "provider_uri", "provider", "model", "command"} {
		if strings.Contains(text, `"`+forbidden+`"`) {
			t.Fatalf("manage_video schema exposes forbidden field %q", forbidden)
		}
	}
}

func TestManageVideoJSONEncodedObjectParsing(t *testing.T) {
	timeline, err := parseTimeline(`{"output_preset":"landscape_720p","total_duration_ms":1000}`)
	if err != nil {
		t.Fatalf("parse JSON-encoded timeline: %v", err)
	}
	if timeline.OutputPreset != pebblestore.VideoPresetLandscape720p || timeline.TotalDurationMs != 1000 {
		t.Fatalf("unexpected timeline: %#v", timeline)
	}
	if timeline.Clips == nil || timeline.Transitions == nil {
		t.Fatalf("timeline arrays must be non-nil for clients: %#v", timeline)
	}
	if _, err := parseTimeline(map[string]any{"output_preset": pebblestore.VideoPresetLandscape1080p}); err != nil {
		t.Fatalf("parse native object timeline: %v", err)
	}
	metadata, err := parseJSONEncodedObject(`{"campaign":"launch"}`, "metadata")
	if err != nil || metadata["campaign"] != "launch" {
		t.Fatalf("parse JSON-encoded metadata: metadata=%v err=%v", metadata, err)
	}
	for _, raw := range []any{"not-json", `[]`, ``} {
		if _, err := parseJSONEncodedObject(raw, "metadata"); err == nil {
			t.Fatalf("expected invalid metadata %q to fail", raw)
		}
	}
}

func TestManageVideoProjectLifecycle(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-lifecycle.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "session-1", UserID: "user-1", AccountScopeID: "account-1"}
	workspacePath := t.TempDir()
	workspaceService := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	if _, err := workspaceService.AddForPrincipal(principal, workspacePath, "workspace", "", false); err != nil {
		t.Fatal(err)
	}

	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "session-1", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: workspacePath, Mode: "auto"}); err != nil {
		t.Fatal(err)
	}

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}

	sessions := sessionruntime.NewService(sessionStore, events)
	videoProjects := videoproject.NewService(sessionStore)
	videoRender := videorender.NewService(videorender.Config{}, sessionStore, nil, nil, workspaceService, nil)

	runtime := NewRuntime(1)
	runtime.sessions = sessions
	runtime.videoProjects = videoProjects
	runtime.videoRender = videoRender

	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "session-1", RunID: "run-1"})
	scope := WorkspaceScope{SessionID: "session-1", Principal: principal}

	// 1. Create project with the JSON-encoded object strings exposed by the Codex tool adapter.
	initialTimeline, _ := json.Marshal(map[string]any{
		"output_preset":     pebblestore.VideoPresetLandscape1080p,
		"total_duration_ms": 5000,
		"clips": []map[string]any{
			{
				"id":                "clip_1",
				"track":             0,
				"sequence":          0,
				"source_kind":       pebblestore.VideoClipSourceKindColor,
				"duration_ms":       5000,
				"timeline_start_ms": 0,
				"timeline_end_ms":   5000,
				"visible":           true,
			},
		},
	})
	createArgs, _ := json.Marshal(map[string]any{
		"action":           "create_project",
		"title":            "Product Intro",
		"description":      "Showcase key features",
		"output_preset":    pebblestore.VideoPresetLandscape1080p,
		"initial_timeline": string(initialTimeline),
		"metadata":         `{"campaign":"launch"}`,
	})
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-1", Name: "manage_video", Arguments: string(createArgs)})
	if err != nil {
		t.Fatalf("create_project failed: %v", err)
	}
	var createRes struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
		Project    struct {
			Title string `json:"title"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(payload), &createRes); err != nil || createRes.ProjectID == "" {
		t.Fatalf("failed to parse create_project response: %s (err=%v)", payload, err)
	}
	projectID := createRes.ProjectID
	var createPayload map[string]any
	if err := json.Unmarshal([]byte(payload), &createPayload); err != nil {
		t.Fatalf("parse create_project presentation: %v", err)
	}
	presentation, ok := createPayload["presentation"].(map[string]any)
	if !ok || presentation["kind"] != "video" || presentation["title"] != "Video project ready" || presentation["activity_label"] != "Setting up video project" || presentation["subject"] != "Product Intro" || presentation["project_id"] != projectID {
		t.Fatalf("unexpected create_project presentation: %#v", createPayload["presentation"])
	}
	browsePresentation := manageVideoPresentation("browse_source", map[string]any{}, map[string]any{
		"status": "ok",
		"videos": []videosource.Clip{{Name: "source-one.mp4"}, {Name: "source-two.mp4"}},
	})
	sourceNames, ok := browsePresentation["source_names"].([]string)
	if !ok || len(sourceNames) != 2 || sourceNames[0] != "source-one.mp4" || sourceNames[1] != "source-two.mp4" {
		t.Fatalf("unexpected browse source presentation: %#v", browsePresentation)
	}
	singleSourcePresentation := manageVideoPresentation("read_transcript", map[string]any{}, map[string]any{
		"status":       "ok",
		"source_names": []string{"ycfinalwithaudio.mp4"},
	})
	if singleSourcePresentation["subject"] != "ycfinalwithaudio.mp4" {
		t.Fatalf("unexpected transcript source presentation: %#v", singleSourcePresentation)
	}

	// 2. Read project
	readArgs, _ := json.Marshal(map[string]any{
		"action":     "read_project",
		"project_id": projectID,
	})
	payload, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-2", Name: "manage_video", Arguments: string(readArgs)})
	if err != nil {
		t.Fatalf("read_project failed: %v", err)
	}
	if !strings.Contains(payload, projectID) || !strings.Contains(payload, "Product Intro") || !strings.Contains(payload, `"campaign":"launch"`) {
		t.Fatalf("unexpected read_project output: %s", payload)
	}

	// 3. Create revision with the JSON-encoded timeline shape exposed to Codex.
	revisionTimeline, _ := json.Marshal(map[string]any{
		"output_preset":     pebblestore.VideoPresetLandscape1080p,
		"total_duration_ms": 6000,
		"clips": []map[string]any{
			{
				"id":                "clip_1",
				"track":             0,
				"sequence":          0,
				"source_kind":       pebblestore.VideoClipSourceKindColor,
				"duration_ms":       6000,
				"timeline_start_ms": 0,
				"timeline_end_ms":   6000,
				"visible":           true,
				"captions": []map[string]any{
					{
						"text":     "Welcome to Swarm",
						"start_ms": 500,
						"end_ms":   3000,
					},
				},
			},
		},
	})
	revArgs, _ := json.Marshal(map[string]any{
		"action":         "create_revision",
		"project_id":     projectID,
		"change_summary": "Added captions",
		"timeline":       string(revisionTimeline),
	})
	payload, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-3", Name: "manage_video", Arguments: string(revArgs)})
	if err != nil {
		t.Fatalf("create_revision failed: %v", err)
	}
	var revRes struct {
		RevisionNumber int    `json:"revision_number"`
		RevisionID     string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(payload), &revRes); err != nil || revRes.RevisionNumber != 2 {
		t.Fatalf("unexpected create_revision response: %s", payload)
	}

	// Restore the exact first revision as a new immutable head.
	restoreArgs, _ := json.Marshal(map[string]any{"action": "restore_revision", "project_id": projectID, "source_revision_id": createRes.RevisionID})
	payload, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-restore", Name: "manage_video", Arguments: string(restoreArgs)})
	if err != nil || !strings.Contains(payload, `"restored_from_revision_id":"`+createRes.RevisionID+`"`) {
		t.Fatalf("restore_revision payload=%s err=%v", payload, err)
	}

	// 4. Start render
	renderArgs, _ := json.Marshal(map[string]any{
		"action":         "start_render",
		"project_id":     projectID,
		"revision_id":    revRes.RevisionID,
		"queue_grace_ms": 5000,
	})
	payload, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-4", Name: "manage_video", Arguments: string(renderArgs)})
	if err != nil {
		t.Fatalf("start_render failed: %v", err)
	}
	var renderRes struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(payload), &renderRes); err != nil || renderRes.JobID == "" {
		t.Fatalf("unexpected start_render response: %s", payload)
	}
	jobID := renderRes.JobID

	// 5. Check render status
	statusArgs, _ := json.Marshal(map[string]any{
		"action":        "render_status",
		"render_job_id": jobID,
	})
	payload, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-5", Name: "manage_video", Arguments: string(statusArgs)})
	if err != nil {
		t.Fatalf("render_status failed: %v", err)
	}
	if !strings.Contains(payload, jobID) {
		t.Fatalf("unexpected render_status output: %s", payload)
	}

	// 6. Cancel render
	cancelArgs, _ := json.Marshal(map[string]any{
		"action":        "cancel_render",
		"render_job_id": jobID,
	})
	payload, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-6", Name: "manage_video", Arguments: string(cancelArgs)})
	if err != nil {
		t.Fatalf("cancel_render failed: %v", err)
	}
	if !strings.Contains(payload, "cancelled") {
		t.Fatalf("unexpected cancel_render output: %s", payload)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	if err := videoRender.WaitForIdle(waitCtx); err != nil {
		t.Fatalf("render goroutine did not stop before test cleanup: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

func TestManageVideoChildSessionUsesParentVideoProject(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-parent.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "child", UserID: "user-1", AccountScopeID: "account-1"}
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "parent", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: "/ws", Mode: "auto", Metadata: map[string]any{"lineage_kind": "video_project"}}); err != nil {
		t.Fatal(err)
	}
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "child", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: "/ws", Mode: "auto", Metadata: map[string]any{"parent_session_id": "parent", "lineage_kind": "system_sidechat"}}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.videoProjects = videoproject.NewService(sessionStore)
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "child", RunID: "run-1"})
	scope := WorkspaceScope{SessionID: "child", Principal: principal}
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-create", Name: "manage_video", Arguments: `{"action":"create_project","title":"Shared"}`})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Project struct {
			SessionID string `json:"session_id"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(payload), &response); err != nil || response.Project.SessionID != "parent" {
		t.Fatalf("child project payload=%s err=%v", payload, err)
	}
}

func TestManageVideoStudioCreatesAdditionalProjectWithExplicitID(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-multiple-projects.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "studio", UserID: "user-1", AccountScopeID: "account-1"}
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "studio", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: "/ws", Mode: "auto", Metadata: map[string]any{"lineage_kind": "video_project"}}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.videoProjects = videoproject.NewService(sessionStore)
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "studio", RunID: "run-1"})
	scope := WorkspaceScope{SessionID: "studio", Principal: principal}
	for _, call := range []Call{
		{CallID: "primary", Name: "manage_video", Arguments: `{"action":"create_project","title":"Primary"}`},
		{CallID: "second", Name: "manage_video", Arguments: `{"action":"create_project","project_id":"project_two","title":"Second"}`},
	} {
		if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call); err != nil {
			t.Fatal(err)
		}
	}
	projects, err := runtime.videoProjects.ListProjects(principal, "studio", 10)
	if err != nil || len(projects) != 2 {
		t.Fatalf("projects=%+v err=%v", projects, err)
	}
	if _, ok, err := runtime.videoProjects.GetProject(principal, "studio", "project_two"); err != nil || !ok {
		t.Fatalf("explicit project missing ok=%v err=%v", ok, err)
	}
}

func TestManageVideoChatSessionUpgradesWhenCreatingProposal(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-chat-upgrade.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "chat", UserID: "user-1", AccountScopeID: "account-1"}
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "chat", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: "/ws", Mode: "auto", Metadata: map[string]any{"entry_mode": "chat"}}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.videoProjects = videoproject.NewService(sessionStore)
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "chat", RunID: "run-chat-upgrade"})
	scope := WorkspaceScope{SessionID: "chat", Principal: principal}
	created, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "create", Name: "manage_video", Arguments: `{"action":"create_project","title":"Chat video","initial_timeline":{"output_preset":"landscape_1080p","total_duration_ms":1000,"clips":[{"id":"clip_a","track":0,"sequence":0,"source_kind":"color","duration_ms":1000,"timeline_start_ms":0,"timeline_end_ms":1000,"visible":true}]}}`})
	if err != nil {
		t.Fatal(err)
	}
	var create struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(created), &create); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"action": "create_edit_proposal", "project_id": create.ProjectID, "base_revision_id": create.RevisionID, "title": "Shorter opening", "operations": []map[string]any{{"id": "trim", "type": pebblestore.VideoEditOperationUpdateClip, "clip": map[string]any{"id": "clip_a", "track": 0, "sequence": 0, "source_kind": "color", "duration_ms": 500, "timeline_start_ms": 0, "timeline_end_ms": 500, "visible": true}}}})
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "proposal", Name: "manage_video", Arguments: string(args)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"session_upgraded_to_video_studio":true`) || !strings.Contains(payload, `"proposal_status":"pending"`) {
		t.Fatalf("proposal payload=%s", payload)
	}
	upgraded, ok, err := runtime.sessions.GetSession("chat")
	if err != nil || !ok {
		t.Fatalf("upgraded session missing: ok=%v err=%v", ok, err)
	}
	if upgraded.Metadata["entry_mode"] != "chat" || upgraded.Metadata["experience"] != "video_studio" || upgraded.Metadata["launch_source"] != "chat_upgrade" || upgraded.Metadata["lineage_kind"] != "video_project" || upgraded.Metadata["creative_mode"] != "video" || upgraded.Metadata["video_project_id"] != create.ProjectID {
		t.Fatalf("chat session was not durably upgraded without losing metadata: %+v", upgraded.Metadata)
	}
	if projectSessionID, studio, err := runtime.manageVideoProjectSession(principal, upgraded); err != nil || !studio || projectSessionID != "chat" {
		t.Fatalf("upgraded session is not Studio-capable: projectSessionID=%q studio=%v err=%v", projectSessionID, studio, err)
	}
}

func TestManageVideoStudioCreatesVisibleWorkingRevision(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-proposal.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "studio", UserID: "user-1", AccountScopeID: "account-1"}
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "studio", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: "/ws", Mode: "auto", Metadata: map[string]any{"lineage_kind": "video_project"}}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.videoProjects = videoproject.NewService(sessionStore)
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "studio", RunID: "run-1"})
	scope := WorkspaceScope{SessionID: "studio", Principal: principal}
	created, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "create", Name: "manage_video", Arguments: `{"action":"create_project","title":"Studio","initial_timeline":{"output_preset":"landscape_1080p","total_duration_ms":1000,"clips":[{"id":"clip_a","track":0,"sequence":0,"source_kind":"color","duration_ms":1000,"timeline_start_ms":0,"timeline_end_ms":1000,"visible":true}]}}`})
	if err != nil {
		t.Fatal(err)
	}
	var create struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(created), &create); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"action": "create_edit_proposal", "project_id": create.ProjectID, "base_revision_id": create.RevisionID, "title": "Trim opening", "rationale": "Start faster", "affected_ranges": []map[string]any{{"start_ms": 0, "end_ms": 500}}, "operations": []map[string]any{{"id": "trim", "type": pebblestore.VideoEditOperationUpdateClip, "clip": map[string]any{"id": "clip_a", "track": 0, "sequence": 0, "source_kind": "color", "duration_ms": 500, "timeline_start_ms": 0, "timeline_end_ms": 500, "visible": true}}}})
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "proposal", Name: "manage_video", Arguments: string(args)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"proposal_status":"pending"`) || !strings.Contains(payload, `"working_revision_id":"vrev_`) || !strings.Contains(payload, `"change_notice":"A new change was added`) || !strings.Contains(payload, `"affected_ranges":[{"start_ms":0,"end_ms":500}]`) {
		t.Fatalf("proposal payload=%s", payload)
	}
	project, ok, err := runtime.videoProjects.GetProject(principal, "studio", create.ProjectID)
	if err != nil || !ok || project.CurrentRevisionID == create.RevisionID || project.ConfirmedRevisionID != create.RevisionID || project.RevisionCount != 2 {
		t.Fatalf("proposal did not preserve the confirmed cut while advancing the visible working revision: %+v ok=%v err=%v", project, ok, err)
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "render", Name: "manage_video", Arguments: `{"action":"start_render","project_id":"` + create.ProjectID + `"}`})
	if err == nil || !strings.Contains(err.Error(), "cannot start final render") {
		t.Fatalf("start render error=%v", err)
	}
}

func TestManageVideoProjectAuthAndSessionOwnership(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-auth.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "session-1", UserID: "user-1", AccountScopeID: "account-1", WorkspacePath: "/ws1", Mode: "auto"}); err != nil {
		t.Fatal(err)
	}

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}

	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.videoProjects = videoproject.NewService(sessionStore)

	// Foreign principal
	foreignPrincipal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "session-1", UserID: "user-other", AccountScopeID: "account-other"}
	foreignScope := WorkspaceScope{SessionID: "session-1", Principal: foreignPrincipal}
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "session-1", RunID: "run-1"})

	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, foreignScope, Call{
		CallID:    "call-unauthorized",
		Name:      "manage_video",
		Arguments: `{"action":"create_project","title":"Hack"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("expected session ownership rejection, got: %v", err)
	}
}

func TestManageVideoStudioCreatesThreePartInitialPlanWithoutInitialTimeline(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-plan.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "studio", UserID: "user-1", AccountScopeID: "account-1"}
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "studio", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: "/ws", Mode: "auto", Metadata: map[string]any{"lineage_kind": "video_project"}}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.videoProjects = videoproject.NewService(sessionStore)
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "studio", RunID: "run-1"})
	scope := WorkspaceScope{SessionID: "studio", Principal: principal}
	created, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "create", Name: "manage_video", Arguments: `{"action":"create_project","title":"How to make dubstep music"}`})
	if err != nil {
		t.Fatal(err)
	}
	var create struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(created), &create); err != nil {
		t.Fatal(err)
	}
	if create.ProjectID == "" || create.RevisionID == "" {
		t.Fatalf("create_project without initial_timeline must return exact project and revision ids: %s", created)
	}
	createdAgain, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "create-again", Name: "manage_video", Arguments: `{"action":"create_project","title":"How to make dubstep music"}`})
	if err != nil {
		t.Fatalf("load existing project: %v", err)
	}
	var createAgain struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(createdAgain), &createAgain); err != nil || createAgain.ProjectID != create.ProjectID || createAgain.RevisionID != create.RevisionID {
		t.Fatalf("existing project did not return its exact base revision: first=%s second=%s err=%v", created, createdAgain, err)
	}
	collection := pebblestore.SessionArtifactCollection{Version: pebblestore.SessionArtifactVersion, ID: "slides", AccountScopeID: principal.AccountScopeID, SessionID: "studio", Status: pebblestore.SessionArtifactStatusReady, Name: "Slides", VariantCount: 1, ReadyCount: 1, EventSeq: 99}
	variant := pebblestore.SessionArtifactVariant{Version: pebblestore.SessionArtifactVersion, ID: "slide", CollectionID: collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: "studio", Status: pebblestore.SessionArtifactStatusReady, Filename: "slide.png", MediaType: "image/png", EventSeq: 99}
	if err := store.PutJSON(pebblestore.KeySessionArtifactCollection(principal.AccountScopeID, "studio", collection.ID), collection); err != nil {
		t.Fatal(err)
	}
	if err := store.PutJSON(pebblestore.KeySessionArtifactVariant(principal.AccountScopeID, "studio", collection.ID, variant.ID), variant); err != nil {
		t.Fatal(err)
	}
	visual := map[string]any{"session_id": "studio", "collection_id": "slides", "variant_id": "slide", "event_seq": 99}
	arguments, _ := json.Marshal(map[string]any{
		"action": "propose_plan", "project_id": create.ProjectID, "base_revision_id": create.RevisionID,
		"title": "How to make dubstep music", "plan": map[string]any{"kind": "initial", "summary": "Review before production", "parts": []map[string]any{
			{"id": "part-1", "title": "Build the beat", "duration_ms": 1000, "visual": visual},
			{"id": "part-2", "title": "Design the bass", "duration_ms": 1000, "visual": visual},
			{"id": "part-3", "title": "Arrange the drop", "duration_ms": 1000, "visual": visual},
		}},
	})
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "plan", Name: "manage_video", Arguments: string(arguments)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"action":"propose_plan"`) || !strings.Contains(payload, `"proposal_status":"pending"`) || !strings.Contains(payload, `"title":"Arrange the drop"`) || strings.Contains(payload, `"operations":[`) {
		t.Fatalf("atomic plan payload=%s", payload)
	}
	project, ok, err := runtime.videoProjects.GetProject(principal, "studio", create.ProjectID)
	if err != nil || !ok || project.CurrentRevisionID == create.RevisionID || project.ConfirmedRevisionID != create.RevisionID || project.RevisionCount != 2 {
		t.Fatalf("pending plan did not expose a working revision while preserving its confirmed checkpoint: %+v ok=%v err=%v", project, ok, err)
	}
	if project.Title != "How to make dubstep music" {
		t.Fatalf("unexpected project title: %+v", project)
	}
}
