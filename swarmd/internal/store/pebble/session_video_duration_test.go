package pebblestore

import "testing"

// Duration is a lower bound on every clip endpoint, including muted footage
// appended to a pending cut. This store-layer regression exercises normalization,
// immutable-base preservation and proposal state without a renderer or provider.
func TestVideoPendingAppendExtendsDuration(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")
	intro := VideoTimelineClip{ID: "intro", SourceKind: VideoClipSourceKindColor, Track: 0, Sequence: 0, TimelineStartMs: 0, TimelineEndMs: 4000, DurationMs: 4000, Visible: true}
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Duration", InitialTimeline: &VideoProjectTimeline{TotalDurationMs: 4000, Clips: []VideoTimelineClip{intro}}, NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	footage := VideoTimelineClip{ID: "footage", SourceKind: VideoClipSourceKindColor, Track: 0, Sequence: 1, TimelineStartMs: 4000, TimelineEndMs: 7900, DurationMs: 3900, Visible: true, Muted: true}
	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, BaseRevisionID: base.ID, Operations: []VideoEditOperation{{ID: "append", Type: VideoEditOperationAddClip, Clip: &footage}}, NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	working, ok, err := store.GetVideoProjectRevision("acc", "sess", project.ID, proposal.WorkingRevisionID)
	if err != nil || !ok {
		t.Fatalf("working revision: %v %v", ok, err)
	}
	if working.Timeline.TotalDurationMs != 7900 || len(working.Timeline.Clips) != 2 || working.Timeline.Clips[1].TimelineStartMs != 4000 || !working.Timeline.Clips[1].Muted {
		t.Fatalf("appended cut truncated or changed: %+v", working.Timeline)
	}
	old, ok, err := store.GetVideoProjectRevision("acc", "sess", project.ID, base.ID)
	if err != nil || !ok || old.Timeline.TotalDurationMs != 4000 || len(old.Timeline.Clips) != 1 {
		t.Fatalf("base changed: %+v %v", old, err)
	}
	current, ok, err := store.GetVideoProject("acc", "sess", project.ID)
	if err != nil || !ok || current.ConfirmedRevisionID != base.ID || current.ActiveRenderJobID != "" || proposal.Status != VideoEditProposalStatusPending {
		t.Fatalf("proposal accepted or render started: %+v %v", current, err)
	}
}

func TestVideoTimelineDurationBoundsAndPadding(t *testing.T) {
	for _, tc := range []struct {
		name                string
		declared, end, want int64
	}{
		{"missing", 0, 7900, 7900}, {"stale", 4000, 7900, 7900}, {"exact", 7900, 7900, 7900}, {"tail-padding", 9000, 7900, 9000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timeline := VideoProjectTimeline{TotalDurationMs: tc.declared, Clips: []VideoTimelineClip{{ID: "clip", SourceKind: VideoClipSourceKindColor, TimelineEndMs: tc.end, DurationMs: tc.end, Visible: true}}}
			normalizeVideoTimeline(&timeline)
			if timeline.TotalDurationMs != tc.want {
				t.Fatalf("duration=%d want %d", timeline.TotalDurationMs, tc.want)
			}
			normalizeVideoTimeline(&timeline)
			if timeline.TotalDurationMs != tc.want {
				t.Fatal("normalization not idempotent")
			}
		})
	}
	timeline := VideoProjectTimeline{TotalDurationMs: 4000, Clips: []VideoTimelineClip{{ID: "oversize", SourceKind: VideoClipSourceKindColor, TimelineEndMs: MaxVideoTimelineDurationMs + 1, DurationMs: MaxVideoTimelineDurationMs + 1, Visible: true}}}
	normalizeVideoTimeline(&timeline)
	if err := validateVideoTimeline(timeline); err == nil {
		t.Fatal("short declared duration concealed oversized clip")
	}
}
