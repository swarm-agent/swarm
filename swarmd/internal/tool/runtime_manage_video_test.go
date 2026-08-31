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
	for _, required := range []string{"source_root_ref", "relative_path", "video_refs", "audio_refs", "job_refs", "job_ref", "transcript_ref", "analysis_ref", "source_fingerprint", "waveform_resolution_ms", "focus_notes", "start_ms", "end_ms", "timestamps_ms", "ranges", "max_width", "include_index", "index_only", "base_revision_id", "operations", "affected_ranges", "part_id", "selected_candidate_id", "selected_source", "derivative", "propose_html_iteration", "import_storyboard", "storyboard_source", "exports", "inspect_composition", "update_composition", "expected_revision_id", "composition_catalog", "detached_slots", "clear_source", "audio_policy"} {
		if !strings.Contains(text, `"`+required+`"`) {
			t.Fatalf("manage_video schema lacks %q", required)
		}
	}
}

func TestManageVideoDefinitionDescribesStoryboardFirstPreProductionWorkflow(t *testing.T) {
	definition := manageVideoDefinition()
	for _, required := range []string{"prefer a self-contained HTML swarm.storyboard/v1 source", "export_html_stills", "import_storyboard", "filming requirements", "still remains the visible placeholder", "plan.kind=revision", "Do not stop after HTML authoring or still export", "storyboard parts remain pending"} {
		if !strings.Contains(definition.Description, required) {
			t.Fatalf("manage_video description lacks storyboard workflow %q", required)
		}
	}
}

func TestManageVideoSelectionContextRetainsStoryboardLineage(t *testing.T) {
	ref := map[string]any{"session_id": "source-session", "collection_id": "storyboards", "variant_id": "launch", "event_seq": 7}
	still := map[string]any{"session_id": "video-session", "collection_id": "stills", "variant_id": "opening", "event_seq": 12}
	selection := manageVideoSelectionContext(map[string]any{
		"video_storyboard_part_id": "intro", "video_storyboard_capture_state_id": "opening", "video_storyboard_production_state": "pending",
		"video_storyboard_filming_requirements": []any{"Locked camera", "Hold final pose"}, "video_storyboard_source": ref, "video_storyboard_still": still,
	})
	if selection["storyboard_part_id"] != "intro" || selection["storyboard_capture_state_id"] != "opening" || selection["storyboard_production_state"] != "pending" {
		t.Fatalf("selection = %#v", selection)
	}
	if got := selection["storyboard_filming_requirements"].([]string); len(got) != 2 || got[0] != "Locked camera" {
		t.Fatalf("filming requirements = %#v", got)
	}
	if got := selection["storyboard_source"].(*pebblestore.SessionArtifactSelectionReference); got.EventSeq != 7 {
		t.Fatalf("storyboard source = %#v", got)
	}
	if got := selection["storyboard_still"].(*pebblestore.SessionArtifactSelectionReference); got.EventSeq != 12 {
		t.Fatalf("storyboard still = %#v", got)
	}
}

func TestManageVideoDefinitionExposesManagedMP4PlanContract(t *testing.T) {
	raw, err := json.Marshal(manageVideoDefinition().Parameters)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{"video/mp4", "source_start_ms", "source_end_ms", "caption", "transition", "Descriptive on_screen_text and transition_in never create timeline presentation"} {
		if !strings.Contains(text, required) {
			t.Fatalf("manage_video plan schema lacks %q", required)
		}
	}
}

func TestManageVideoDefinitionDescribesAtomicMultiPartHTMLIterations(t *testing.T) {
	raw, err := json.Marshal(manageVideoDefinition().Parameters)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{"accepts one or more stable parts in one atomic proposal", "every part requires 2 to 16 compatible ready text/html", "per-part image-only downgrade"} {
		if !strings.Contains(text, required) {
			t.Fatalf("manage_video schema lacks multi-part HTML contract %q", required)
		}
	}
}

func TestManageVideoActionRegistryAndNearestSuggestions(t *testing.T) {
	definition := manageVideoDefinition()
	properties := definition.Parameters["properties"].(map[string]any)
	actions := properties["action"].(map[string]any)["enum"].([]string)
	if len(actions) != len(manageVideoActionRegistry) || actions[0] != "capabilities" || actions[1] != "inspect_context" || actions[2] != "inspect_frames" {
		t.Fatalf("schema actions = %#v", actions)
	}
	nearest := nearestManageVideoActions("inspect_attachment", 2)
	if len(nearest) == 0 || nearest[0] != "inspect_attachments" {
		t.Fatalf("nearest actions = %#v", nearest)
	}
	studio := manageVideoActionNames(true)
	for _, required := range []string{"propose_html_iteration", "import_storyboard", "select_animation_candidate", "promote_animation_derivative", "inspect_composition", "update_composition"} {
		if !containsString(studio, required) {
			t.Fatalf("studio actions lack %q: %#v", required, studio)
		}
	}
	for _, forbidden := range []string{"create_revision", "restore_revision", "start_render"} {
		for _, action := range studio {
			if action == forbidden {
				t.Fatalf("studio actions expose %q", forbidden)
			}
		}
	}
	studioNearest := nearestManageVideoActionsFrom("start_rendr", studio, len(studio))
	for _, action := range studioNearest {
		if action == "start_render" {
			t.Fatalf("studio nearest actions expose forbidden action: %#v", studioNearest)
		}
	}
}

func TestParseManageVideoArtifactReferenceRequiresCompleteExactReference(t *testing.T) {
	got, err := parseManageVideoArtifactReference(map[string]any{"session_id": "session", "collection_id": "collection", "variant_id": "variant", "event_seq": 7}, "selected_source")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "session" || got.CollectionID != "collection" || got.VariantID != "variant" || got.EventSeq != 7 {
		t.Fatalf("reference = %+v", got)
	}
	for _, incomplete := range []map[string]any{
		{"collection_id": "collection", "variant_id": "variant", "event_seq": 7},
		{"session_id": "session", "variant_id": "variant", "event_seq": 7},
		{"session_id": "session", "collection_id": "collection", "event_seq": 7},
		{"session_id": "session", "collection_id": "collection", "variant_id": "variant"},
	} {
		if _, err := parseManageVideoArtifactReference(incomplete, "selected_source"); err == nil || !strings.Contains(err.Error(), "exact session_id") {
			t.Fatalf("incomplete reference error = %v", err)
		}
	}
}

func TestParseMinimalVideoEditsNormalizesCanonicalClips(t *testing.T) {
	timeline := pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "clip", Track: 2, Sequence: 3, SourceKind: pebblestore.VideoClipSourceKindSourceVideo, SourceRef: "videosrc_original", SourceStartMs: 100, SourceEndMs: 1100, TimelineStartMs: 500, TimelineEndMs: 1500, DurationMs: 1000, Visible: true, Volume: 1}}}
	operations, err := parseVideoEditOperations([]map[string]any{{"id": "volume", "type": "set_volume", "clip_id": "clip", "volume": 0.25}, {"id": "move", "type": "move_clip", "clip_id": "clip", "timeline_start_ms": 2000}}, timeline)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 || operations[0].Type != pebblestore.VideoEditOperationUpdateClip || operations[0].Clip.Track != 2 || operations[0].Clip.SourceRef != "videosrc_original" || operations[0].Clip.Volume != .25 || operations[1].Clip.TimelineEndMs != 3000 {
		t.Fatalf("normalized operations = %#v", operations)
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
	if !strings.Contains(definition.Description, "trusted video and audio sources") || !strings.Contains(definition.Description, "registered source-media folders") || !strings.Contains(definition.Description, "selected opaque video or audio references") || !strings.Contains(definition.Description, "triggering-message video attachments") {
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
	if err := os.WriteFile(filepath.Join(mediaPath, "soundtrack.mp3"), append([]byte("ID3"), make([]byte, 16)...), 0o600); err != nil {
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
			Ref       string `json:"ref"`
			MediaKind string `json:"media_kind"`
		} `json:"videos"`
		Audio []struct {
			Ref                string `json:"ref"`
			MediaKind          string `json:"media_kind"`
			SourceFingerprint  string `json:"source_fingerprint"`
			FingerprintVersion string `json:"fingerprint_version"`
		} `json:"audio"`
	}
	if err := json.Unmarshal([]byte(payload), &browseResponse); err != nil || len(browseResponse.Videos) != 1 || len(browseResponse.Audio) != 1 {
		t.Fatalf("decode browse payload=%s err=%v", payload, err)
	}
	if browseResponse.Videos[0].MediaKind != videosource.MediaKindVideo || browseResponse.Audio[0].MediaKind != videosource.MediaKindAudio || !strings.HasPrefix(browseResponse.Audio[0].Ref, "audiosrc_") || browseResponse.Audio[0].SourceFingerprint == "" || browseResponse.Audio[0].FingerprintVersion != pebblestore.AudioSourceFingerprintV1 {
		t.Fatalf("browse source metadata=%+v", browseResponse)
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
	audioArgs, _ := json.Marshal(map[string]any{"action": "start_transcription", "audio_refs": []string{browseResponse.Audio[0].Ref}})
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, WorkspaceScope{SessionID: "session-1", Principal: principal}, Call{CallID: "call-4", Name: "manage_video", Arguments: string(audioArgs)}); err != nil {
		t.Fatal(err)
	}
	if videoService.audioSourceCount != 1 {
		t.Fatalf("selected audio source count=%d, want 1", videoService.audioSourceCount)
	}
	mixedArgs, _ := json.Marshal(map[string]any{"action": "start_transcription", "video_refs": []string{browseResponse.Videos[0].Ref}, "audio_refs": []string{browseResponse.Audio[0].Ref}})
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, WorkspaceScope{SessionID: "session-1", Principal: principal}, Call{CallID: "call-5", Name: "manage_video", Arguments: string(mixedArgs)}); err == nil || !strings.Contains(err.Error(), "mixed") {
		t.Fatalf("mixed media error=%v", err)
	}
}

func TestBoundedAudioAnalysisSlicesAndAggregates(t *testing.T) {
	levels := []pebblestore.AudioAnalysisLevel{{StartMs: 0, EndMs: 100, RMS: .2, Peak: .4}, {StartMs: 100, EndMs: 200, RMS: .4, Peak: .8}, {StartMs: 200, EndMs: 300, RMS: .6, Peak: .7}}
	got := boundedAudioLevels(levels, 50, 250, 200)
	if len(got) != 2 || got[0].StartMs != 0 || got[0].EndMs != 200 || got[0].RMS != .3 || got[0].Peak != .8 {
		t.Fatalf("aggregated levels=%+v", got)
	}
	beats := boundedAudioBeats([]pebblestore.AudioAnalysisBeat{{TimeMs: 50}, {TimeMs: 100}, {TimeMs: 250}}, 100, 250)
	if len(beats) != 1 || beats[0].TimeMs != 100 {
		t.Fatalf("bounded beats=%+v", beats)
	}
}

func TestManageVideoReadsBoundedDeterministicAudioAnalysis(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-audio-analysis.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "session-1", UserID: "user-1", AccountScopeID: "account-1"}
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "session-1", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: "/workspace", Mode: "auto", Metadata: map[string]any{"workspace_id": "workspace-1"}}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.video = &fakeManageVideoService{audioAnalysis: pebblestore.AudioAnalysisSnapshot{
		Ref: "audanalysis_test", SchemaVersion: pebblestore.AudioAnalysisSchemaVersion, SourceRef: "audiosrc_test", SourceFingerprint: strings.Repeat("a", 64),
		AnalyzerVersion: videotranscription.AudioAnalyzerVersion, DurationMs: 1_000, SampleIntervalMs: 100,
		Levels: []pebblestore.AudioAnalysisLevel{{StartMs: 0, EndMs: 100, RMS: .2, Peak: .4}}, Onsets: []pebblestore.AudioAnalysisOnset{{TimeMs: 50, Strength: .8}}, Beats: []pebblestore.AudioAnalysisBeat{{TimeMs: 50, Confidence: .7, BarBeat: 1}}, Sections: []pebblestore.AudioAnalysisSection{{StartMs: 0, EndMs: 1_000, Label: "moderate", Confidence: .65}},
	}}
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "session-1", RunID: "run-1"})
	scope := WorkspaceScope{SessionID: "session-1", Principal: principal}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "bad", Name: "manage_video", Arguments: `{"action":"read_audio_analysis","analysis_ref":"audanalysis_test","waveform_resolution_ms":60001}`}); err == nil || !strings.Contains(err.Error(), "waveform_resolution_ms") {
		t.Fatalf("invalid resolution error=%v", err)
	}
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "read", Name: "manage_video", Arguments: `{"action":"read_audio_analysis","analysis_ref":"audanalysis_test","start_ms":0,"end_ms":500}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"timing_authority":"deterministic_pcm_dsp"`, `"model_generated":false`, `"levels_truncated":false`, `"sections_truncated":false`, `"end_ms":500`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("analysis payload lacks %s: %s", want, payload)
		}
	}
	if strings.Contains(payload, "/workspace") {
		t.Fatalf("analysis response leaked private workspace path: %s", payload)
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
	focusNotes       string
	sourceCount      int
	audioSourceCount int
	audioAnalysis    pebblestore.AudioAnalysisSnapshot
}

func (f *fakeManageVideoService) StartRegisteredSources(_ context.Context, _ identity.Principal, _ string, sources []pebblestore.SessionVideoAttachmentReference, focusNotes string) (videotranscription.StartResult, error) {
	f.focusNotes = focusNotes
	f.sourceCount = len(sources)
	return videotranscription.StartResult{}, nil
}

func (f *fakeManageVideoService) StartRegisteredAudioSources(_ context.Context, _ identity.Principal, _ string, sources []pebblestore.AudioSourceReference, focusNotes string) (videotranscription.StartResult, error) {
	f.focusNotes = focusNotes
	f.audioSourceCount = len(sources)
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
func (f *fakeManageVideoService) ReadAudioAnalysisByWorkspace(identity.Principal, string, string, string) (pebblestore.AudioAnalysisSnapshot, error) {
	return f.audioAnalysis, nil
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
	for _, action := range []string{"create_project", "read_project", "get_project", "list_projects", "create_edit_proposal", "propose_plan", "import_storyboard", "propose_html_iteration", "select_animation_candidate", "promote_animation_derivative", "create_revision", "restore_revision", "start_render", "render_status", "cancel_render"} {
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
		"render_quality": pebblestore.VideoRenderQualityStandard,
		"render_fps":     30,
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

func TestManageVideoStudioInitialTimelineCreatesDistinctProjectAndPreservesInputs(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-initial-timeline-project.pebble"))
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
	if err := store.PutJSON(pebblestore.KeySessionArtifactCollection(principal.AccountScopeID, "studio", "media"), pebblestore.SessionArtifactCollection{ID: "media", AccountScopeID: principal.AccountScopeID, SessionID: "studio", Status: pebblestore.SessionArtifactStatusReady}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutJSON(pebblestore.KeySessionArtifactVariant(principal.AccountScopeID, "studio", "media", "clip"), pebblestore.SessionArtifactVariant{ID: "clip", CollectionID: "media", AccountScopeID: principal.AccountScopeID, SessionID: "studio", Status: pebblestore.SessionArtifactStatusReady, MediaType: "video/mp4", EventSeq: 7}); err != nil {
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
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "studio", RunID: "run-initial-timeline"})
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
	soundtrackSource := browse.Audio[0]

	primaryPayload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "primary", Name: "manage_video", Arguments: `{"action":"create_project","title":"Primary project"}`})
	if err != nil {
		t.Fatal(err)
	}
	var primary struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(primaryPayload), &primary); err != nil || primary.ProjectID == "" || primary.RevisionID == "" {
		t.Fatalf("primary project payload=%s err=%v", primaryPayload, err)
	}
	primaryAgain, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "primary-again", Name: "manage_video", Arguments: `{"action":"create_project","title":"Ignored by primary ensure"}`})
	if err != nil {
		t.Fatal(err)
	}
	var ensured struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(primaryAgain), &ensured); err != nil || ensured != primary {
		t.Fatalf("primary ensure was not idempotent: first=%+v second=%+v payload=%s err=%v", primary, ensured, primaryAgain, err)
	}

	initialTimeline := map[string]any{
		"output_preset":     "portrait_720p",
		"total_duration_ms": 2000,
		"clips": []map[string]any{
			{
				"id": "visual", "track": 0, "sequence": 0, "source_kind": "managed_artifact", "media_type": "video/mp4",
				"artifact_ref":    map[string]any{"session_id": "studio", "collection_id": "media", "variant_id": "clip", "event_seq": 7},
				"source_start_ms": 0, "source_end_ms": 2000, "timeline_start_ms": 0, "timeline_end_ms": 2000,
				"duration_ms": 2000, "visible": true, "volume": 1,
			},
			{
				"id": "soundtrack", "track": 1, "sequence": 1, "source_kind": "source_audio", "media_type": "audio/wav",
				"audio_source":    soundtrackSource,
				"source_start_ms": 0, "source_end_ms": 2000, "timeline_start_ms": 0, "timeline_end_ms": 2000,
				"duration_ms": 2000, "visible": false, "volume": 0.8,
			},
		},
	}
	createArgs, _ := json.Marshal(map[string]any{
		"action": "create_project", "title": "MP4 plus soundtrack", "description": "Exact authored cut", "output_preset": "portrait_720p", "initial_timeline": initialTimeline,
	})
	createdPayload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "authored", Name: "manage_video", Arguments: string(createArgs)})
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
		Project    struct {
			Title        string `json:"title"`
			Description  string `json:"description"`
			OutputPreset string `json:"output_preset"`
		} `json:"project"`
		Revision struct {
			Timeline pebblestore.VideoProjectTimeline `json:"timeline"`
		} `json:"revision"`
	}
	if err := json.Unmarshal([]byte(createdPayload), &created); err != nil {
		t.Fatalf("decode authored project payload=%s err=%v", createdPayload, err)
	}
	if created.ProjectID == "" || created.ProjectID == primary.ProjectID || created.RevisionID == "" || created.RevisionID == primary.RevisionID {
		t.Fatalf("initial_timeline must create a distinct project: primary=%+v created=%+v", primary, created)
	}
	if created.Project.Title != "MP4 plus soundtrack" || created.Project.Description != "Exact authored cut" || created.Project.OutputPreset != "portrait_720p" {
		t.Fatalf("authored project inputs were not preserved: %+v", created.Project)
	}
	if created.Revision.Timeline.OutputPreset != "portrait_720p" || created.Revision.Timeline.TotalDurationMs != 2000 || len(created.Revision.Timeline.Clips) != 2 {
		t.Fatalf("initial timeline was not preserved: %+v", created.Revision.Timeline)
	}
	visual, soundtrack := created.Revision.Timeline.Clips[0], created.Revision.Timeline.Clips[1]
	if visual.MediaType != "video/mp4" || visual.TimelineStartMs != 0 || visual.TimelineEndMs != 2000 || soundtrack.SourceKind != pebblestore.VideoClipSourceKindSourceAudio || soundtrack.TimelineStartMs != 0 || soundtrack.TimelineEndMs != 2000 {
		t.Fatalf("same-playhead MP4 and source_audio clips were not preserved: visual=%+v soundtrack=%+v", visual, soundtrack)
	}
	projects, err := runtime.videoProjects.ListProjects(principal, "studio", 10)
	if err != nil || len(projects) != 2 {
		t.Fatalf("projects=%+v err=%v", projects, err)
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

func TestManageVideoHTMLIterationHasOneEnforcedProposalPath(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-html-iteration.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "studio-html", UserID: "user-1", AccountScopeID: "account-1"}
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "studio-html", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: "/ws", Mode: "auto", Metadata: map[string]any{"lineage_kind": "video_project"}}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.videoProjects = videoproject.NewService(sessionStore)
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "studio-html", RunID: "run-html"})
	scope := WorkspaceScope{SessionID: "studio-html", Principal: principal}

	created, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "create-html", Name: "manage_video", Arguments: `{"action":"create_project","title":"One HTML iteration"}`})
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

	requirements := &pebblestore.SessionArtifactOutputRequirements{PresetID: "landscape_video", Width: 1920, Height: 1080}
	profile := &pebblestore.SessionArtifactAnimationProfile{ProfileID: "motion_ui"}
	variants := []pebblestore.SessionArtifactVariant{
		{Version: pebblestore.SessionArtifactVersion, ID: "fallback", CollectionID: "html-iteration", AccountScopeID: principal.AccountScopeID, SessionID: "studio-html", Status: pebblestore.SessionArtifactStatusReady, Filename: "fallback.png", MediaType: "image/png", EventSeq: 10},
		{Version: pebblestore.SessionArtifactVersion, ID: "candidate-a", CollectionID: "html-iteration", AccountScopeID: principal.AccountScopeID, SessionID: "studio-html", Status: pebblestore.SessionArtifactStatusReady, Filename: "a.html", MediaType: "text/html", EventSeq: 11, OutputRequirements: requirements, AnimationProfile: profile, Parts: []pebblestore.SessionArtifactPart{{ID: "intro", Kind: "temporal", EndMs: 1000}}},
		{Version: pebblestore.SessionArtifactVersion, ID: "candidate-b", CollectionID: "html-iteration", AccountScopeID: principal.AccountScopeID, SessionID: "studio-html", Status: pebblestore.SessionArtifactStatusReady, Filename: "b.html", MediaType: "text/html", EventSeq: 12, OutputRequirements: requirements, AnimationProfile: profile, Parts: []pebblestore.SessionArtifactPart{{ID: "intro", Kind: "temporal", EndMs: 1000}}},
		{Version: pebblestore.SessionArtifactVersion, ID: "fallback-second", CollectionID: "html-iteration", AccountScopeID: principal.AccountScopeID, SessionID: "studio-html", Status: pebblestore.SessionArtifactStatusReady, Filename: "fallback-second.png", MediaType: "image/png", EventSeq: 13},
		{Version: pebblestore.SessionArtifactVersion, ID: "candidate-c", CollectionID: "html-iteration", AccountScopeID: principal.AccountScopeID, SessionID: "studio-html", Status: pebblestore.SessionArtifactStatusReady, Filename: "c.html", MediaType: "text/html", EventSeq: 14, OutputRequirements: requirements, AnimationProfile: profile, Parts: []pebblestore.SessionArtifactPart{{ID: "second", Kind: "temporal", EndMs: 1000}}},
		{Version: pebblestore.SessionArtifactVersion, ID: "candidate-d", CollectionID: "html-iteration", AccountScopeID: principal.AccountScopeID, SessionID: "studio-html", Status: pebblestore.SessionArtifactStatusReady, Filename: "d.html", MediaType: "text/html", EventSeq: 15, OutputRequirements: requirements, AnimationProfile: profile, Parts: []pebblestore.SessionArtifactPart{{ID: "second", Kind: "temporal", EndMs: 1000}}},
	}
	for _, variant := range variants {
		if err := store.PutJSON(pebblestore.KeySessionArtifactVariant(principal.AccountScopeID, "studio-html", variant.CollectionID, variant.ID), variant); err != nil {
			t.Fatal(err)
		}
	}
	ref := func(id string, eventSeq uint64) map[string]any {
		return map[string]any{"session_id": "studio-html", "collection_id": "html-iteration", "variant_id": id, "event_seq": eventSeq}
	}
	candidates := []map[string]any{
		{"id": "a", "source": ref("candidate-a", 11)},
		{"id": "b", "source": ref("candidate-b", 12)},
	}
	part := map[string]any{
		"id":                   "intro",
		"title":                "Intro",
		"duration_ms":          1000,
		"visual":               ref("fallback", 10),
		"animation_candidates": map[string]any{"status": "awaiting_selection", "candidates": candidates},
	}
	secondCandidates := []map[string]any{
		{"id": "c", "source": ref("candidate-c", 14)},
		{"id": "d", "source": ref("candidate-d", 15)},
	}
	secondPart := map[string]any{
		"id":                   "second",
		"title":                "Second",
		"duration_ms":          1000,
		"visual":               ref("fallback-second", 13),
		"animation_candidates": map[string]any{"status": "awaiting_selection", "candidates": secondCandidates},
	}
	plan := map[string]any{"kind": "initial", "parts": []map[string]any{part, secondPart}}
	genericArgs, _ := json.Marshal(map[string]any{"action": "propose_plan", "project_id": create.ProjectID, "base_revision_id": create.RevisionID, "plan": plan})
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "generic-html", Name: "manage_video", Arguments: string(genericArgs)}); err == nil || !strings.Contains(err.Error(), "purpose-specific html_iteration") {
		t.Fatalf("generic route must reject HTML candidates, got %v", err)
	}

	canonicalArgs, _ := json.Marshal(map[string]any{"action": "propose_html_iteration", "project_id": create.ProjectID, "base_revision_id": create.RevisionID, "title": "Choose each HTML part", "plan": plan})
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "canonical-html", Name: "manage_video", Arguments: string(canonicalArgs)})
	if err != nil {
		t.Fatalf("canonical HTML iteration path failed: %v", err)
	}
	if !strings.Contains(payload, `"action":"propose_html_iteration"`) || !strings.Contains(payload, `"intent":"html_iteration"`) || !strings.Contains(payload, `"proposal_status":"pending"`) || !strings.Contains(payload, `"id":"second"`) {
		t.Fatalf("canonical HTML iteration payload = %s", payload)
	}
}
