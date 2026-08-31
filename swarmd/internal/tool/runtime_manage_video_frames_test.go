package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videoproject"
	"swarm/packages/swarmd/internal/videorender"
)

type fakeFrameInspectionRender struct {
	request videorender.FrameInspectionRequest
}

func (f *fakeFrameInspectionRender) InspectFrames(_ context.Context, _ identity.Principal, request videorender.FrameInspectionRequest) (videorender.FrameInspectionResult, error) {
	f.request = request
	return videorender.FrameInspectionResult{
		ProjectID: request.ProjectID, RevisionID: request.RevisionID, RevisionEventSeq: 17,
		DurationMs: 5_000, Width: 1280, Height: 720,
		Frames: []videorender.InspectedFrame{{TimestampMs: 1_000, Artifact: pebblestore.SessionArtifactSelectionReference{SessionID: request.ArtifactSessionID, CollectionID: "frames", VariantID: "frame-1", EventSeq: 23}}},
	}, nil
}
func (*fakeFrameInspectionRender) StartRenderJob(identity.Principal, videorender.RenderJobRequest) {}
func (*fakeFrameInspectionRender) CancelRenderJob(context.Context, identity.Principal, string, string) (pebblestore.VideoRenderJobSnapshot, error) {
	return pebblestore.VideoRenderJobSnapshot{}, nil
}
func (*fakeFrameInspectionRender) GetRenderJobStatus(context.Context, identity.Principal, string, string) (pebblestore.VideoRenderJobSnapshot, bool, error) {
	return pebblestore.VideoRenderJobSnapshot{}, false, nil
}

func TestManageVideoInspectFramesReturnsOnlyReadyReferencesAndExactLineage(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-frames.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "studio", UserID: "user", AccountScopeID: "account"}
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "studio", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: "/workspace", Mode: "auto", Metadata: map[string]any{"lineage_kind": "video_project"}}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.videoProjects = videoproject.NewService(sessionStore)
	frames := &fakeFrameInspectionRender{}
	runtime.videoRender = frames
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: "studio", RunID: "run-frames"})
	scope := WorkspaceScope{SessionID: "studio", Principal: principal}
	created, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "create", Name: "manage_video", Arguments: `{"action":"create_project","title":"Frames","initial_timeline":{"total_duration_ms":5000,"clips":[{"id":"color","track":0,"sequence":0,"source_kind":"color","duration_ms":5000,"timeline_start_ms":0,"timeline_end_ms":5000,"visible":true}]}}`})
	if err != nil {
		t.Fatal(err)
	}
	var exact struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(created), &exact); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"action": "inspect_frames", "project_id": exact.ProjectID, "revision_id": exact.RevisionID, "timestamps_ms": []int64{1000}, "max_width": 1280})
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "frames", Name: "manage_video", Arguments: string(args)})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"action":"inspect_frames"`, `"revision_event_seq":17`, `"timestamp_ms":1000`, `"session_id":"studio"`, `"collection_id":"frames"`, `"variant_id":"frame-1"`, `"event_seq":23`} {
		if !strings.Contains(payload, required) {
			t.Fatalf("inspect_frames payload lacks %s: %s", required, payload)
		}
	}
	if frames.request.ProjectID != exact.ProjectID || frames.request.RevisionID != exact.RevisionID || frames.request.ArtifactSessionID != "studio" || frames.request.RequestID != "run-frames" || frames.request.WorkspacePath != "/workspace" {
		t.Fatalf("inspection request lost trusted authority: %+v", frames.request)
	}
}
