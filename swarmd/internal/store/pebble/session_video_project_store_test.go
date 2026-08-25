package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestSessionStoreForVideoProject(t *testing.T) (*SessionStore, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "pebble.db"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	store := NewSessionStore(db)
	return store, func() {
		_ = db.Close()
	}
}

func createTestSession(t *testing.T, store *SessionStore, accountScopeID, userID, sessionID string) {
	t.Helper()
	_, err := store.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:       sessionID,
		UserID:          userID,
		AccountScopeID:  accountScopeID,
		ClientRequestID: "create-session-" + sessionID,
		IdempotencyKey:  "create-session-" + sessionID,
		PayloadHash:     "hash-" + sessionID,
		Kind:            V3SessionMutationCreateSession,
		Session: &SessionSnapshot{
			ID:             sessionID,
			UserID:         userID,
			AccountScopeID: accountScopeID,
			WorkspacePath:  "/workspace",
			WorkspaceName:  "workspace",
			CreatedAt:      time.Now().UnixMilli(),
		},
		NowUnixMs: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
}

func TestListVideoProjectsForAccountCrossSession(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "account", "user", "session-a")
	createTestSession(t, store, "account", "user", "session-b")
	createTestSession(t, store, "foreign", "user", "session-c")
	for _, input := range []CreateVideoProjectInput{
		{AccountScopeID: "account", UserID: "user", SessionID: "session-a", ProjectID: "project-a", Title: "A"},
		{AccountScopeID: "account", UserID: "user", SessionID: "session-b", ProjectID: "project-b", Title: "B"},
		{AccountScopeID: "foreign", UserID: "user", SessionID: "session-c", ProjectID: "project-c", Title: "C"},
	} {
		if _, _, err := store.CreateVideoProject(input); err != nil {
			t.Fatal(err)
		}
	}
	projects, err := store.ListVideoProjectsForAccount("account", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("projects=%+v, want two account projects", projects)
	}
}

func TestVideoProjectCreationAndRevisions(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	account := "acc_1"
	user := "usr_1"
	sessionID := "sess_1"
	createTestSession(t, store, account, user, sessionID)

	// Create a video project with initial timeline
	initialTimeline := &VideoProjectTimeline{
		OutputPreset: VideoPresetLandscape1080p,
		Clips: []VideoTimelineClip{
			{
				ID:              "clip_1",
				Name:            "Intro Scene",
				Track:           0,
				Sequence:        0,
				SourceKind:      VideoClipSourceKindSourceVideo,
				SourceRef:       "vsrc_raw_intro",
				SourceStartMs:   0,
				SourceEndMs:     5000,
				TimelineStartMs: 0,
				TimelineEndMs:   5000,
				DurationMs:      5000,
				Visible:         true,
				Volume:          1.0,
				Captions: []VideoTextOverlay{
					{
						ID:       "cap_1",
						Text:     "Welcome to Swarm Video",
						Position: "bottom",
						StartMs:  500,
						EndMs:    3000,
					},
				},
			},
		},
	}

	project, rev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  account,
		UserID:          user,
		SessionID:       sessionID,
		ProjectID:       "vproj_intro",
		Title:           "Intro Video Project",
		Description:     "Product walkthrough video",
		OutputPreset:    VideoPresetLandscape1080p,
		InitialTimeline: initialTimeline,
		NowUnixMs:       1000,
	})
	if err != nil {
		t.Fatalf("create video project failed: %v", err)
	}

	if project.ID != "vproj_intro" || project.RevisionCount != 1 || project.CurrentRevisionNumber != 1 {
		t.Fatalf("unexpected project state: %+v", project)
	}
	if rev == nil || rev.RevisionNumber != 1 || rev.ProjectID != "vproj_intro" {
		t.Fatalf("unexpected initial revision: %+v", rev)
	}
	if len(rev.Timeline.Clips) != 1 || rev.Timeline.Width != 1920 || rev.Timeline.Height != 1080 {
		t.Fatalf("unexpected timeline clips or dimensions: %+v", rev.Timeline)
	}

	// Add Revision 2 (e.g. user or AI edits timeline by adding a second clip and caption)
	rev2Timeline := VideoProjectTimeline{
		OutputPreset: VideoPresetLandscape1080p,
		Clips: []VideoTimelineClip{
			{
				ID:              "clip_1",
				Name:            "Intro Scene",
				Track:           0,
				Sequence:        0,
				SourceKind:      VideoClipSourceKindSourceVideo,
				SourceRef:       "vsrc_raw_intro",
				SourceStartMs:   0,
				SourceEndMs:     5000,
				TimelineStartMs: 0,
				TimelineEndMs:   5000,
				DurationMs:      5000,
				Visible:         true,
				Volume:          0.8,
			},
			{
				ID:              "clip_2",
				Name:            "Feature Demo",
				Track:           0,
				Sequence:        1,
				SourceKind:      VideoClipSourceKindSourceVideo,
				SourceRef:       "vsrc_feature_demo",
				SourceStartMs:   1000,
				SourceEndMs:     8000,
				TimelineStartMs: 5000,
				TimelineEndMs:   12000,
				DurationMs:      7000,
				Visible:         true,
				Volume:          1.0,
			},
		},
	}

	rev2, updatedProject, err := store.CreateVideoProjectRevision(CreateVideoProjectRevisionInput{
		AccountScopeID:  account,
		UserID:          user,
		SessionID:       sessionID,
		ProjectID:       project.ID,
		Description:     "Added feature demo clip",
		ChangeSummary:   "Appended 7s feature demo",
		Timeline:        rev2Timeline,
		AuthorPrincipal: "swarm",
		NowUnixMs:       2000,
	})
	if err != nil {
		t.Fatalf("create revision 2 failed: %v", err)
	}

	if rev2.RevisionNumber != 2 || rev2.ParentRevisionID != rev.ID {
		t.Fatalf("revision 2 lineage mismatch: got number %d, parent %s, want parent %s", rev2.RevisionNumber, rev2.ParentRevisionID, rev.ID)
	}
	if updatedProject.CurrentRevisionID != rev2.ID || updatedProject.RevisionCount != 2 {
		t.Fatalf("updated project revision count or current revision mismatch: %+v", updatedProject)
	}

	// Verify revision 1 is immutable and preserved
	readRev1, ok, err := store.GetVideoProjectRevision(account, sessionID, project.ID, rev.ID)
	if err != nil || !ok {
		t.Fatalf("get revision 1 failed: %v, ok=%v", err, ok)
	}
	if readRev1.RevisionNumber != 1 || len(readRev1.Timeline.Clips) != 1 {
		t.Fatalf("revision 1 mutated: %+v", readRev1)
	}

	// List revisions
	revList, err := store.ListVideoProjectRevisions(account, sessionID, project.ID, 10)
	if err != nil || len(revList) != 2 {
		t.Fatalf("list revisions failed: %v, count=%d", err, len(revList))
	}
	if revList[0].RevisionNumber != 1 || revList[1].RevisionNumber != 2 {
		t.Fatalf("list revisions sorting unexpected: %+v", revList)
	}
	limited, err := store.ListVideoProjectRevisions(account, sessionID, project.ID, 1)
	if err != nil || len(limited) != 1 || limited[0].RevisionNumber != 1 {
		t.Fatalf("limited revision history must follow revision-number order: %+v err=%v", limited, err)
	}
}

func TestPrimaryVideoToolProjectAndExactRestore(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")

	timeline := &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, Clips: []VideoTimelineClip{{ID: "original", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 1000, DurationMs: 1000, Visible: true}}}
	project, rev1, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "vproj_primary", Title: "Video Tool", ProjectKind: VideoProjectKindVideoTool, InitialTimeline: timeline, NowUnixMs: 100})
	if err != nil {
		t.Fatalf("create primary: %v", err)
	}
	primary, ok, err := store.GetPrimaryVideoToolProject("acc", "sess")
	if err != nil || !ok || primary.ID != project.ID {
		t.Fatalf("primary discovery = %+v, %v, %v", primary, ok, err)
	}
	if _, _, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "other", Title: "Other", ProjectKind: VideoProjectKindVideoTool, NowUnixMs: 110}); err == nil {
		t.Fatal("expected duplicate primary rejection")
	}

	rev2, _, err := store.CreateVideoProjectRevision(CreateVideoProjectRevisionInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, Timeline: VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, Clips: []VideoTimelineClip{{ID: "changed", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 2000, DurationMs: 2000, Visible: true}}}, NowUnixMs: 200})
	if err != nil {
		t.Fatalf("create changed revision: %v", err)
	}
	restored, updated, err := store.CreateVideoProjectRevision(CreateVideoProjectRevisionInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, Timeline: rev1.Timeline, RestoredFromRevisionID: rev1.ID, NowUnixMs: 300})
	if err != nil {
		t.Fatalf("restore revision: %v", err)
	}
	if restored.ParentRevisionID != rev2.ID || restored.RestoredFromRevisionID != rev1.ID || restored.Timeline.Clips[0].ID != "original" || updated.CurrentRevisionID != restored.ID {
		t.Fatalf("unexpected restore lineage: restored=%+v updated=%+v", restored, updated)
	}
}

func TestVideoRenderJobLifecycle(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	account := "acc_1"
	user := "usr_1"
	sessionID := "sess_render"
	createTestSession(t, store, account, user, sessionID)

	project, rev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID: account,
		UserID:         user,
		SessionID:      sessionID,
		ProjectID:      "vproj_render",
		Title:          "Render Test",
		OutputPreset:   VideoPresetLandscape1080p,
		InitialTimeline: &VideoProjectTimeline{
			OutputPreset: VideoPresetLandscape1080p,
			Clips: []VideoTimelineClip{
				{
					ID:              "clip_1",
					Track:           0,
					Sequence:        0,
					SourceKind:      VideoClipSourceKindColor,
					TimelineStartMs: 0,
					TimelineEndMs:   3000,
					DurationMs:      3000,
					Visible:         true,
				},
			},
		},
		NowUnixMs: 1000,
	})
	if err != nil {
		t.Fatalf("create project failed: %v", err)
	}

	// Create render job for Revision 1
	job, err := store.CreateVideoRenderJob(CreateVideoRenderJobInput{
		AccountScopeID: account,
		UserID:         user,
		SessionID:      sessionID,
		ProjectID:      project.ID,
		RevisionID:     rev.ID,
		JobID:          "vren_job_1",
		NowUnixMs:      1100,
	})
	if err != nil {
		t.Fatalf("create render job failed: %v", err)
	}
	if job.Status != VideoRenderJobStatusQueued || job.RevisionNumber != 1 {
		t.Fatalf("unexpected initial job status: %+v", job)
	}

	// Update progress to rendering
	renderingJob, err := store.UpdateVideoRenderJob(UpdateVideoRenderJobInput{
		AccountScopeID: account,
		UserID:         user,
		SessionID:      sessionID,
		JobID:          job.ID,
		Status:         VideoRenderJobStatusRendering,
		ExpectedStatus: VideoRenderJobStatusQueued,
		Progress:       0.45,
		NowUnixMs:      1200,
	})
	if err != nil {
		t.Fatalf("update to rendering failed: %v", err)
	}
	if renderingJob.Status != VideoRenderJobStatusRendering || renderingJob.Progress != 0.45 || renderingJob.StartedAt != 1200 {
		t.Fatalf("unexpected rendering job state: %+v", renderingJob)
	}
	recoverable, err := store.ListRecoverableVideoRenderJobs(10)
	if err != nil || len(recoverable) != 1 || recoverable[0].ID != job.ID {
		t.Fatalf("recoverable jobs = %+v, err=%v", recoverable, err)
	}

	// Complete render job
	artifactRef := &SessionArtifactSelectionReference{
		SessionID:    sessionID,
		CollectionID: "col_video_renders",
		VariantID:    "var_rendered_mp4",
		EventSeq:     42,
		Action:       "use",
	}
	completedJob, err := store.UpdateVideoRenderJob(UpdateVideoRenderJobInput{
		AccountScopeID:     account,
		UserID:             user,
		SessionID:          sessionID,
		JobID:              job.ID,
		Status:             VideoRenderJobStatusReady,
		ExpectedStatus:     VideoRenderJobStatusRendering,
		Progress:           1.0,
		OutputPreset:       VideoPresetLandscape1080p,
		OutputWidth:        1920,
		OutputHeight:       1080,
		OutputFPS:          30.0,
		OutputDurationMs:   3000,
		OutputSizeBytes:    1048576,
		OutputDigestSHA256: strings.Repeat("e", 64),
		OutputArtifact:     artifactRef,
		NowUnixMs:          1300,
	})
	if err != nil {
		t.Fatalf("complete render job failed: %v", err)
	}
	if completedJob.Status != VideoRenderJobStatusReady || completedJob.CompletedAt != 1300 || completedJob.OutputArtifact == nil {
		t.Fatalf("unexpected completed job state: %+v", completedJob)
	}

	// Verify terminal state rejects transition back to rendering
	_, err = store.UpdateVideoRenderJob(UpdateVideoRenderJobInput{
		AccountScopeID: account,
		UserID:         user,
		SessionID:      sessionID,
		JobID:          job.ID,
		Status:         VideoRenderJobStatusRendering,
		NowUnixMs:      1400,
	})
	if err == nil {
		t.Fatalf("expected error transitioning from terminal state, got nil")
	}
}

func TestVideoProjectAccountAndSessionIsolation(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	accountA := "acc_a"
	userA := "usr_a"
	sessionA := "sess_a"
	createTestSession(t, store, accountA, userA, sessionA)

	accountB := "acc_b"
	userB := "usr_b"
	sessionB := "sess_b"
	createTestSession(t, store, accountB, userB, sessionB)

	// Create project in Account A
	projectA, _, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID: accountA,
		UserID:         userA,
		SessionID:      sessionA,
		ProjectID:      "vproj_isolated",
		Title:          "Account A Project",
		OutputPreset:   VideoPresetLandscape1080p,
		NowUnixMs:      1000,
	})
	if err != nil {
		t.Fatalf("create project in A failed: %v", err)
	}

	// Account B cannot read Account A's project
	_, ok, err := store.GetVideoProject(accountB, sessionA, projectA.ID)
	if err != nil || ok {
		t.Fatalf("expected not found for cross-account read, got ok=%v, err=%v", ok, err)
	}

	// Account B cannot create revision on Account A's project
	_, _, err = store.CreateVideoProjectRevision(CreateVideoProjectRevisionInput{
		AccountScopeID: accountB,
		UserID:         userB,
		SessionID:      sessionB,
		ProjectID:      projectA.ID,
		Timeline: VideoProjectTimeline{
			OutputPreset: VideoPresetLandscape1080p,
			Clips: []VideoTimelineClip{
				{ID: "c1", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 1000, DurationMs: 1000, Visible: true},
			},
		},
		NowUnixMs: 2000,
	})
	if err == nil {
		t.Fatalf("expected cross-account revision creation to fail, got nil")
	}
}

func TestTimelineValidationRules(t *testing.T) {
	cases := []struct {
		name     string
		timeline VideoProjectTimeline
		wantErr  string
	}{
		{
			name: "negative clip range",
			timeline: VideoProjectTimeline{
				OutputPreset: VideoPresetLandscape1080p,
				Clips: []VideoTimelineClip{
					{
						ID:              "c1",
						Track:           0,
						Sequence:        0,
						SourceKind:      VideoClipSourceKindSourceVideo,
						SourceRef:       "ref_1",
						SourceStartMs:   100,
						SourceEndMs:     50, // invalid: end < start
						TimelineStartMs: 0,
						TimelineEndMs:   1000,
						DurationMs:      1000,
						Visible:         true,
					},
				},
			},
			wantErr: "invalid source range",
		},
		{
			name: "missing source ref for source_video",
			timeline: VideoProjectTimeline{
				OutputPreset: VideoPresetLandscape1080p,
				Clips: []VideoTimelineClip{
					{
						ID:              "c1",
						Track:           0,
						Sequence:        0,
						SourceKind:      VideoClipSourceKindSourceVideo,
						SourceRef:       "", // missing
						SourceStartMs:   0,
						SourceEndMs:     1000,
						TimelineStartMs: 0,
						TimelineEndMs:   1000,
						DurationMs:      1000,
						Visible:         true,
					},
				},
			},
			wantErr: "requires non-empty source_ref",
		},
		{
			name: "duplicate clip IDs",
			timeline: VideoProjectTimeline{
				OutputPreset: VideoPresetLandscape1080p,
				Clips: []VideoTimelineClip{
					{ID: "c1", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 1000, DurationMs: 1000, Visible: true},
					{ID: "c1", Track: 0, Sequence: 1, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 1000, TimelineEndMs: 2000, DurationMs: 1000, Visible: true},
				},
			},
			wantErr: "duplicate clip id",
		},
		{
			name: "excessive caption text",
			timeline: VideoProjectTimeline{
				OutputPreset: VideoPresetLandscape1080p,
				Clips: []VideoTimelineClip{
					{
						ID:              "c1",
						Track:           0,
						Sequence:        0,
						SourceKind:      VideoClipSourceKindColor,
						TimelineStartMs: 0,
						TimelineEndMs:   1000,
						DurationMs:      1000,
						Visible:         true,
						Captions: []VideoTextOverlay{
							{
								ID:      "cap1",
								Text:    strings.Repeat("x", 600), // > MaxTextOverlayLength
								StartMs: 0,
								EndMs:   500,
							},
						},
					},
				},
			},
			wantErr: "caption 0 text exceeds maximum",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.timeline.Width = 1920
			tc.timeline.Height = 1080
			tc.timeline.FPS = 30
			err := validateVideoTimeline(tc.timeline)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateVideoTimeline() error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
