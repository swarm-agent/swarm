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
	"swarm/packages/swarmd/internal/videoproject"
	"swarm/packages/swarmd/internal/videosource"
	"swarm/packages/swarmd/internal/workspace"
)

func TestManageVideoDefinitionExposesTypedSoundtrackProposalContract(t *testing.T) {
	definition := manageVideoDefinition()
	raw, err := json.Marshal(definition.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{"source_audio", "audio_source", "add_clip", "update_clip", "replace_clip", "remove_clip", "source_fingerprint", "fingerprint_version", "affected_ranges"} {
		if !strings.Contains(text, `"`+required+`"`) {
			t.Fatalf("manage_video soundtrack schema lacks %q: %s", required, text)
		}
	}
	for _, forbidden := range []string{"audio_path", "file_path", "root_path", "workspace_path"} {
		if strings.Contains(text, `"`+forbidden+`"`) {
			t.Fatalf("manage_video soundtrack schema exposes forbidden path field %q", forbidden)
		}
	}
	for _, guidance := range []string{"complete exact audio", "cannot accept a proposal or start a final render"} {
		if !strings.Contains(definition.Description, guidance) {
			t.Fatalf("manage_video soundtrack guidance lacks %q", guidance)
		}
	}
}

func TestManageVideoDefinitionExplainsLiveHTMLSoundtrackPreviewBoundary(t *testing.T) {
	definition := manageVideoDefinition()
	raw, err := json.Marshal(definition.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, expected := range []string{
		"immediate live Video Studio preview",
		"selected HTML plays in a sandboxed swarm-player/v1 iframe while soundtrack audio follows the same playhead",
		"no HTML-to-MP4 export is needed for preview",
		"durable acceptance/promotion or final rendering requires an MP4 derivative",
		"never replace a durable timeline artifact_ref with text/html",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("manage_video live HTML schema guidance lacks %q: %s", expected, text)
		}
	}
}

func TestManageVideoCreatesPendingSoundtrackProposalFromExactAudioReference(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-soundtrack.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "studio", UserID: "user-1", AccountScopeID: "account-1"}
	workspacePath, mediaPath := t.TempDir(), t.TempDir()
	workspaceService := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	workspaceResolution, err := workspaceService.AddForPrincipal(principal, workspacePath, "workspace", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceService.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, mediaPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaPath, "soundtrack.wav"), []byte("RIFF\x04\x00\x00\x00WAVE"), 0o600); err != nil {
		t.Fatal(err)
	}

	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "studio", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: workspacePath, Mode: "auto", Metadata: map[string]any{"lineage_kind": "video_project", "workspace_id": workspaceResolution.WorkspaceID}}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.videoSources = videosource.NewService(workspaceService, sessionStore)
	runtime.videoProjects = videoproject.NewService(sessionStore)
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "studio", RunID: "run-soundtrack"})
	scope := WorkspaceScope{SessionID: "studio", Principal: principal}

	rootsPayload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "roots", Name: "manage_video", Arguments: `{"action":"list_source_roots"}`})
	if err != nil {
		t.Fatal(err)
	}
	var roots struct {
		Roots []struct {
			Ref string `json:"ref"`
		} `json:"roots"`
	}
	if err := json.Unmarshal([]byte(rootsPayload), &roots); err != nil || len(roots.Roots) != 1 {
		t.Fatalf("roots payload=%s err=%v", rootsPayload, err)
	}
	browseArgs, _ := json.Marshal(map[string]any{"action": "browse_source", "source_root_ref": roots.Roots[0].Ref})
	browsePayload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "browse", Name: "manage_video", Arguments: string(browseArgs)})
	if err != nil {
		t.Fatal(err)
	}
	var browse struct {
		Audio []pebblestore.AudioSourceReference `json:"audio"`
	}
	if err := json.Unmarshal([]byte(browsePayload), &browse); err != nil || len(browse.Audio) != 1 {
		t.Fatalf("browse payload=%s err=%v", browsePayload, err)
	}

	created, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "create", Name: "manage_video", Arguments: `{"action":"create_project","title":"Soundtrack proposal","initial_timeline":{"output_preset":"landscape_1080p","total_duration_ms":2000,"clips":[{"id":"visual","track":0,"sequence":0,"source_kind":"color","duration_ms":2000,"timeline_start_ms":0,"timeline_end_ms":2000,"visible":true}]}}`})
	if err != nil {
		t.Fatal(err)
	}
	var project struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(created), &project); err != nil {
		t.Fatal(err)
	}
	clip := map[string]any{
		"id": "soundtrack", "name": "soundtrack.wav", "track": 1, "layer": 1, "sequence": 0,
		"source_kind": "source_audio", "audio_source": browse.Audio[0], "media_type": browse.Audio[0].MIMEType,
		"source_start_ms": 0, "source_end_ms": 2000, "timeline_start_ms": 0, "timeline_end_ms": 2000,
		"duration_ms": 2000, "visible": false, "volume": 0.6,
	}
	proposalArgs, _ := json.Marshal(map[string]any{
		"action": "create_edit_proposal", "project_id": project.ProjectID, "base_revision_id": project.RevisionID,
		"title": "Add soundtrack", "affected_ranges": []map[string]any{{"start_ms": 0, "end_ms": 2000}},
		"operations": []map[string]any{{"id": "add-soundtrack", "type": "add_clip", "clip": clip}},
	})
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "proposal", Name: "manage_video", Arguments: string(proposalArgs)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"proposal_status":"pending"`, `"source_kind":"source_audio"`, `"requires_user_acceptance":true`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("proposal payload lacks %s: %s", want, payload)
		}
	}
	if strings.Contains(payload, mediaPath) || strings.Contains(payload, workspacePath) {
		t.Fatalf("proposal response leaked a host path: %s", payload)
	}

	staleArgs := proposalArgs
	var stale map[string]any
	if err := json.Unmarshal(staleArgs, &stale); err != nil {
		t.Fatal(err)
	}
	stale["proposal_id"] = "stale-proposal"
	stale["base_revision_id"] = project.RevisionID
	stalePayload, _ := json.Marshal(stale)
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "stale", Name: "manage_video", Arguments: string(stalePayload)}); err == nil || !strings.Contains(err.Error(), "base revision must be the current project revision") {
		t.Fatalf("stale base error=%v", err)
	}
}

func TestParseVideoEditOperationsRejectsArbitrarySoundtrackPaths(t *testing.T) {
	_, err := parseVideoEditOperations([]map[string]any{{
		"id": "bad", "type": "add_clip", "clip": map[string]any{
			"id": "soundtrack", "track": 1, "sequence": 0, "source_kind": "source_audio", "duration_ms": 1000,
			"timeline_start_ms": 0, "timeline_end_ms": 1000, "visible": false, "file_path": "/outside/song.mp3",
		},
	}}, pebblestore.VideoProjectTimeline{})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("arbitrary soundtrack path error=%v", err)
	}
}
