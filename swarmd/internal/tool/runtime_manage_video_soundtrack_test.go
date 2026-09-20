package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
	help := videoHelpText()
	for _, guidance := range []string{"complete exact audio", "registered soundtrack audio must share the initial part playhead", "create_project initial_timeline", "cannot accept a proposal or start a final render"} {
		if !strings.Contains(help, guidance) {
			t.Fatalf("manage_video soundtrack guidance lacks %q", guidance)
		}
	}
}

func TestManageVideoDefinitionExplainsLiveHTMLSoundtrackPreviewBoundary(t *testing.T) {
	help := videoHelpText()
	for _, expected := range []string{
		"immediate live Video Studio preview",
		"selected HTML plays in a sandboxed swarm-player/v1 iframe while soundtrack audio follows the same playhead",
		"no HTML-to-MP4 export is needed for preview",
		"durable acceptance/promotion or final rendering requires an MP4 derivative",
		"never replace a durable timeline artifact_ref with text/html",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("manage_video live HTML help guidance lacks %q", expected)
		}
	}
}

func TestManageVideoDefinitionDocumentsInitialSoundtrackBaseRevision(t *testing.T) {
	definition := manageVideoDefinition()
	initial := definition.Parameters["properties"].(map[string]any)["initial_timeline"].(map[string]any)
	description, _ := initial["description"].(string)
	for _, expected := range []string{"source_audio", "base revision owns that audio", "subsequent propose_plan preserves it"} {
		if !strings.Contains(description, expected) {
			t.Fatalf("initial_timeline guidance lacks %q: %s", expected, description)
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
	for _, args := range [][]string{{"init", "--quiet"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspacePath
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("initialize fixture: %v: %s", err, output)
		}
	}
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

func TestManageVideoImportAudioArtifactWorkflow(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-import-audio.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "studio", UserID: "user-1", AccountScopeID: "account-1"}
	workspacePath := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspacePath
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("initialize fixture: %v: %s", err, output)
		}
	}

	workspaceService := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	workspaceResolution, err := workspaceService.AddForPrincipal(principal, workspacePath, "workspace", "", false)
	if err != nil {
		t.Fatal(err)
	}

	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{
		ID:             "studio",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  workspacePath,
		Mode:           "auto",
		Metadata: map[string]any{
			"lineage_kind": "video_project",
			"workspace_id": workspaceResolution.WorkspaceID,
		},
	}); err != nil {
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

	audioBytes := []byte("RIFF\x0c\x00\x00\x00WAVEfmt \x10\x00\x00\x00")
	authority := &fakeArtifactAuthority{
		readBody: audioBytes,
		variant: pebblestore.SessionArtifactVariant{
			ID:           "var-audio-1",
			CollectionID: "col-audio-1",
			SessionID:    "studio",
			EventSeq:     1,
			Status:       pebblestore.SessionArtifactStatusReady,
			Filename:     "soundtrack.wav",
			MediaType:    "audio/wav",
		},
	}
	runtime.SetArtifactAuthority(authority)

	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "studio", RunID: "run-import-audio"})
	scope := WorkspaceScope{SessionID: "studio", Principal: principal}

	// 1. Success: Import Audio Artifact
	importArgs, _ := json.Marshal(map[string]any{
		"action": "import_audio_artifact",
		"artifact_reference": map[string]any{
			"session_id":    "studio",
			"collection_id": "col-audio-1",
			"variant_id":    "var-audio-1",
			"event_seq":     1,
		},
	})
	importPayload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "import", Name: "manage_video", Arguments: string(importArgs)})
	if err != nil {
		t.Fatalf("import_audio_artifact failed: %v", err)
	}

	var importRes struct {
		Action      string                           `json:"action"`
		Status      string                           `json:"status"`
		AudioSource pebblestore.AudioSourceReference `json:"audio_source"`
		AudioRef    string                           `json:"audio_ref"`
	}
	if err := json.Unmarshal([]byte(importPayload), &importRes); err != nil {
		t.Fatalf("unmarshal import result: %v", err)
	}
	if importRes.Status != "ok" || importRes.AudioSource.Ref == "" {
		t.Fatalf("unexpected import result: %s", importPayload)
	}
	if importRes.AudioSource.MIMEType != "audio/wav" {
		t.Fatalf("expected audio/wav MIME type, got %s", importRes.AudioSource.MIMEType)
	}
	if importRes.AudioSource.SizeBytes != int64(len(audioBytes)) {
		t.Fatalf("expected size %d, got %d", len(audioBytes), importRes.AudioSource.SizeBytes)
	}

	// Verify the AudioSourceRecord exists and can be validated and opened
	record, found, err := sessionStore.GetAudioSourceRecord(principal.AccountScopeID, workspaceResolution.WorkspaceID, importRes.AudioSource.Ref)
	if err != nil || !found {
		t.Fatalf("GetAudioSourceRecord found=%v err=%v", found, err)
	}
	if err := pebblestore.ValidateAudioSourceRecord(record); err != nil {
		t.Fatalf("ValidateAudioSourceRecord failed: %v", err)
	}
	file, err := pebblestore.OpenValidatedAudioSource(record)
	if err != nil {
		t.Fatalf("OpenValidatedAudioSource failed: %v", err)
	}
	file.Close()

	// 2. Use imported AudioSource in Video Studio create_project and create_edit_proposal
	created, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{
		CallID: "create_proj", Name: "manage_video",
		Arguments: `{"action":"create_project","title":"Imported audio project","initial_timeline":{"output_preset":"landscape_1080p","total_duration_ms":3000,"clips":[{"id":"visual","track":0,"sequence":0,"source_kind":"color","duration_ms":3000,"timeline_start_ms":0,"timeline_end_ms":3000,"visible":true}]}}`,
	})
	if err != nil {
		t.Fatalf("create_project failed: %v", err)
	}
	var proj struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(created), &proj); err != nil {
		t.Fatal(err)
	}

	clip := map[string]any{
		"id": "bg-soundtrack", "name": importRes.AudioSource.Name, "track": 1, "layer": 1, "sequence": 0,
		"source_kind": "source_audio", "audio_source": importRes.AudioSource, "media_type": importRes.AudioSource.MIMEType,
		"source_start_ms": 0, "source_end_ms": 3000, "timeline_start_ms": 0, "timeline_end_ms": 3000,
		"duration_ms": 3000, "visible": false, "volume": 0.8,
	}
	proposalArgs, _ := json.Marshal(map[string]any{
		"action": "create_edit_proposal", "project_id": proj.ProjectID, "base_revision_id": proj.RevisionID,
		"title": "Layer imported soundtrack", "affected_ranges": []map[string]any{{"start_ms": 0, "end_ms": 3000}},
		"operations": []map[string]any{{"id": "op-add-soundtrack", "type": "add_clip", "clip": clip}},
	})
	propPayload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "proposal", Name: "manage_video", Arguments: string(proposalArgs)})
	if err != nil {
		t.Fatalf("create_edit_proposal with imported audio failed: %v", err)
	}
	if !strings.Contains(propPayload, `"proposal_status":"pending"`) {
		t.Fatalf("proposal payload lacks pending status: %s", propPayload)
	}

	// 3. Error cases:
	// a) Not ready artifact
	authority.variant.Status = "pending"
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "import_not_ready", Name: "manage_video", Arguments: string(importArgs)}); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("expected not ready error, got %v", err)
	}

	// b) Non-audio artifact
	authority.variant.Status = pebblestore.SessionArtifactStatusReady
	authority.variant.MediaType = "image/png"
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "import_not_audio", Name: "manage_video", Arguments: string(importArgs)}); err == nil || !strings.Contains(err.Error(), "not an audio artifact") {
		t.Fatalf("expected not audio artifact error, got %v", err)
	}

	// c) Missing reference
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "import_missing_ref", Name: "manage_video", Arguments: `{"action":"import_audio_artifact"}`}); err == nil || !strings.Contains(err.Error(), "requires an exact artifact reference") {
		t.Fatalf("expected missing reference error, got %v", err)
	}
}
