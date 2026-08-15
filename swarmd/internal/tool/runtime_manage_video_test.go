package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videosource"
	"swarm/packages/swarmd/internal/videoproject"
	"swarm/packages/swarmd/internal/videorender"
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
	for _, required := range []string{"source_root_ref", "relative_path", "video_refs", "job_refs", "job_ref", "transcript_ref", "source_fingerprint", "focus_notes", "start_ms", "end_ms", "include_index", "index_only"} {
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
	if !strings.Contains(definition.Description, "registered source-video folders") || !strings.Contains(definition.Description, "selected opaque video references") || !strings.Contains(definition.Description, "no triggering-message attachment") {
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

func TestManageVideoDefinitionExposesProjectAndRenderWorkflow(t *testing.T) {
	definition := manageVideoDefinition()
	raw, err := json.Marshal(definition.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, action := range []string{"create_project", "read_project", "get_project", "list_projects", "create_revision", "start_render", "render_status", "cancel_render"} {
		if !strings.Contains(text, `"`+action+`"`) {
			t.Fatalf("schema lacks video project/render action %q", action)
		}
	}
	for _, param := range []string{"project_id", "revision_id", "render_job_id", "title", "description", "output_preset", "change_summary", "timeline", "initial_timeline", "metadata"} {
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

func TestManageVideoProjectLifecycle(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-lifecycle.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

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

	// 1. Create project with initial timeline
	createArgs, _ := json.Marshal(map[string]any{
		"action":        "create_project",
		"title":         "Product Intro",
		"description":   "Showcase key features",
		"output_preset": pebblestore.VideoPresetLandscape1080p,
		"initial_timeline": map[string]any{
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
		},
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

	// 2. Read project
	readArgs, _ := json.Marshal(map[string]any{
		"action":     "read_project",
		"project_id": projectID,
	})
	payload, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-2", Name: "manage_video", Arguments: string(readArgs)})
	if err != nil {
		t.Fatalf("read_project failed: %v", err)
	}
	if !strings.Contains(payload, projectID) || !strings.Contains(payload, "Product Intro") {
		t.Fatalf("unexpected read_project output: %s", payload)
	}

	// 3. Create revision
	revArgs, _ := json.Marshal(map[string]any{
		"action":         "create_revision",
		"project_id":     projectID,
		"change_summary": "Added captions",
		"timeline": map[string]any{
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
		},
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

	// 4. Start render
	renderArgs, _ := json.Marshal(map[string]any{
		"action":      "start_render",
		"project_id":  projectID,
		"revision_id": revRes.RevisionID,
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
