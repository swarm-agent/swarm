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
