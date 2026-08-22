package videoproject

import (
	"context"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestGetOrCreatePrimaryVideoToolProjectInitializesExistingEmptyProject(t *testing.T) {
	store := newFakeSessionStore()
	service := NewService(store)
	principal := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		AccountScopeID: "acc_video",
		UserID:         "user_video",
	}
	const sessionID = "session_video"
	const projectID = "vproj_existing"
	store.sessions[sessionID] = pebblestore.SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
	}
	store.projects[projectID] = pebblestore.VideoProjectSnapshot{
		ID:             projectID,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      sessionID,
		Title:          "Existing empty Video Studio project",
		ProjectKind:    pebblestore.VideoProjectKindVideoTool,
	}
	timeline := pebblestore.VideoProjectTimeline{
		SchemaVersion:   pebblestore.VideoTimelineSchemaVersion,
		OutputPreset:    pebblestore.VideoPresetLandscape1080p,
		Width:           1920,
		Height:          1080,
		FPS:             30,
		TotalDurationMs: 0,
		Clips:           []pebblestore.VideoTimelineClip{},
		Transitions:     []pebblestore.VideoTimelineTransition{},
	}

	project, revision, err := service.GetOrCreatePrimaryVideoToolProject(context.Background(), principal, CreateProjectInput{
		SessionID:       sessionID,
		Title:           "Ignored replacement title",
		OutputPreset:    pebblestore.VideoPresetLandscape1080p,
		InitialTimeline: &timeline,
		NowUnixMs:       123,
	})
	if err != nil {
		t.Fatalf("initialize existing primary project: %v", err)
	}
	if revision == nil {
		t.Fatal("expected an initial revision for the existing empty project")
	}
	if project.ID != projectID || project.CurrentRevisionID != revision.ID || project.CurrentRevisionNumber != 1 || project.RevisionCount != 1 {
		t.Fatalf("unexpected initialized project: %+v; revision: %+v", project, revision)
	}
	if revision.ProjectID != projectID || revision.Timeline.OutputPreset != pebblestore.VideoPresetLandscape1080p || len(revision.Timeline.Clips) != 0 {
		t.Fatalf("unexpected initial revision: %+v", revision)
	}
}
