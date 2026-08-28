package pebblestore

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func exactAudioSourceReference() *AudioSourceReference {
	return &AudioSourceReference{
		Ref:                "audiosrc_" + strings.Repeat("a", 64),
		Name:               "soundtrack.mp3",
		MIMEType:           "audio/mpeg",
		SizeBytes:          12345,
		SourceFingerprint:  strings.Repeat("b", 64),
		FingerprintVersion: AudioSourceFingerprintV1,
	}
}

func validAudioTimelineClip(id string) VideoTimelineClip {
	return VideoTimelineClip{
		ID:              id,
		Name:            "Soundtrack",
		Track:           1,
		Layer:           2,
		Sequence:        0,
		SourceKind:      VideoClipSourceKindSourceAudio,
		AudioSource:     exactAudioSourceReference(),
		MediaType:       "audio/mpeg",
		SourceStartMs:   500,
		SourceEndMs:     2500,
		TimelineStartMs: 1000,
		TimelineEndMs:   3000,
		DurationMs:      2000,
		Visible:         false,
		Volume:          0.75,
	}
}

func TestAudioOnlyTimelineClipValidatesAndRoundTrips(t *testing.T) {
	timeline := VideoProjectTimeline{
		OutputPreset: VideoPresetLandscape1080p,
		Clips: []VideoTimelineClip{
			{ID: "visual", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 3000, DurationMs: 3000, Visible: true},
			validAudioTimelineClip("soundtrack"),
		},
	}
	normalizeVideoTimeline(&timeline)
	if err := validateVideoTimeline(timeline); err != nil {
		t.Fatalf("valid audio-only clip rejected: %v", err)
	}
	if timeline.Clips[1].Visible || timeline.Clips[1].MediaType != "audio/mpeg" || timeline.Clips[1].Track != 1 || timeline.Clips[1].Layer != 2 {
		t.Fatalf("audio semantics changed during normalization: %+v", timeline.Clips[1])
	}
	payload, err := json.Marshal(timeline)
	if err != nil {
		t.Fatal(err)
	}
	var decoded VideoProjectTimeline
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := validateVideoTimeline(decoded); err != nil {
		t.Fatalf("round-tripped audio timeline rejected: %v", err)
	}
	if decoded.Clips[1].AudioSource == nil || decoded.Clips[1].AudioSource.Ref != timeline.Clips[1].AudioSource.Ref || decoded.Clips[1].Muted {
		t.Fatalf("exact audio source or mute state did not round-trip: %+v", decoded.Clips[1])
	}
}

func TestVisualVideoPlanTimelinePreservesSamePlayheadAudio(t *testing.T) {
	const durationMs = int64(224680)
	soundtrack := validAudioTimelineClip("soundtrack")
	soundtrack.Sequence = 1
	soundtrack.SourceStartMs = 0
	soundtrack.SourceEndMs = durationMs
	soundtrack.TimelineStartMs = 0
	soundtrack.TimelineEndMs = durationMs
	soundtrack.DurationMs = durationMs

	plan := VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{{
		ID: "visual", Title: "Nocturnal Pulse", DurationMs: durationMs,
		Visual:          &SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "fallback", VariantID: "visual", EventSeq: 1},
		VisualMediaType: "image/png",
	}}}
	timeline := visualVideoPlanTimeline(VideoProjectTimeline{
		OutputPreset:    VideoPresetLandscape1080p,
		TotalDurationMs: durationMs,
		Clips:           []VideoTimelineClip{soundtrack},
	}, plan)

	if len(timeline.Clips) != 2 {
		t.Fatalf("compiled timeline clips = %+v, want visual plus soundtrack", timeline.Clips)
	}
	preserved := timeline.Clips[1]
	if preserved.SourceKind != VideoClipSourceKindSourceAudio || preserved.Track != soundtrack.Track || preserved.Sequence != soundtrack.Sequence || preserved.TimelineStartMs != 0 || preserved.TimelineEndMs != durationMs {
		t.Fatalf("soundtrack timing or placement changed: got %+v want %+v", preserved, soundtrack)
	}
	if timeline.TotalDurationMs != durationMs {
		t.Fatalf("compiled timeline duration = %d, want max overlapping end %d", timeline.TotalDurationMs, durationMs)
	}
}

func TestVisualVideoPlanTimelineStillShiftsPreservedNonAudioClips(t *testing.T) {
	preservedVisual := VideoTimelineClip{
		ID: "outro", Track: 0, Sequence: 1, SourceKind: VideoClipSourceKindColor,
		TimelineStartMs: 1000, TimelineEndMs: 2000, DurationMs: 1000, Visible: true,
		Captions: []VideoTextOverlay{{ID: "outro-caption", Text: "Outro", StartMs: 1100, EndMs: 1900}},
	}
	plan := VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{{
		ID: "visual", Title: "Opening", DurationMs: 3000,
		Visual:          &SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "slides", VariantID: "opening", EventSeq: 1},
		VisualMediaType: "image/png",
	}}}
	timeline := visualVideoPlanTimeline(VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, Clips: []VideoTimelineClip{preservedVisual}}, plan)

	if len(timeline.Clips) != 2 {
		t.Fatalf("compiled timeline clips = %+v, want plan clip plus preserved visual", timeline.Clips)
	}
	preserved := timeline.Clips[1]
	if preserved.TimelineStartMs != 3000 || preserved.TimelineEndMs != 4000 || len(preserved.Captions) != 1 || preserved.Captions[0].StartMs != 3100 || preserved.Captions[0].EndMs != 3900 {
		t.Fatalf("preserved non-audio clip did not retain append/shift behavior: %+v", preserved)
	}
	if timeline.TotalDurationMs != 4000 {
		t.Fatalf("compiled timeline duration = %d, want 4000", timeline.TotalDurationMs)
	}
}

func TestVisualPlanProposalPreservesExactAudioThroughWorkingAndAcceptedRevisions(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")

	soundtrack := validAudioTimelineClip("soundtrack")
	soundtrack.Sequence = 7
	soundtrack.Layer = 4
	soundtrack.Muted = true
	baseTimeline := &VideoProjectTimeline{
		OutputPreset:    VideoPresetLandscape1080p,
		TotalDurationMs: soundtrack.TimelineEndMs,
		Clips:           []VideoTimelineClip{soundtrack},
	}
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Soundtrack plan",
		InitialTimeline: baseTimeline, NowUnixMs: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	part := videoPlanTestPart("visual", "Visual", "fallback")
	part.DurationMs = soundtrack.TimelineEndMs
	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: "visual-plan",
		BaseRevisionID: base.ID, Plan: &VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{part}}, NowUnixMs: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	working, ok, err := store.GetVideoProjectRevision("acc", "sess", project.ID, proposal.WorkingRevisionID)
	if err != nil || !ok || len(working.Timeline.Clips) != 2 || !reflect.DeepEqual(working.Timeline.Clips[1], soundtrack) {
		t.Fatalf("working revision changed exact soundtrack: revision=%+v ok=%v err=%v", working, ok, err)
	}
	_, accepted, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{
		AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: proposal.ID, RevisionID: "accepted", NowUnixMs: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted.Timeline.Clips) != 2 || !reflect.DeepEqual(accepted.Timeline.Clips[1], soundtrack) {
		t.Fatalf("accepted revision changed exact soundtrack: %+v", accepted.Timeline.Clips)
	}
}

func TestVisualPlanProposalRejectsAudioClipIDCollisionBeforeCreatingWorkingRevision(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")

	soundtrack := validAudioTimelineClip("shared-id")
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Collision",
		InitialTimeline: &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, Clips: []VideoTimelineClip{soundtrack}}, NowUnixMs: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	part := videoPlanTestPart("shared-id", "Visual", "fallback")
	_, err = store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: "collision",
		BaseRevisionID: base.ID, Plan: &VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{part}}, NowUnixMs: 200,
	})
	if err == nil || !strings.Contains(err.Error(), "soundtrack topology invalid") || !strings.Contains(err.Error(), "collides with a source_audio") {
		t.Fatalf("audio topology collision error = %v", err)
	}
	current, _, readErr := store.GetVideoProject("acc", "sess", project.ID)
	if readErr != nil || current.CurrentRevisionID != base.ID || current.RevisionCount != 1 {
		t.Fatalf("rejected topology created working state: project=%+v err=%v", current, readErr)
	}
}

func TestAudioOnlyTimelineClipRejectsAmbiguousOrVisualSemantics(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*VideoTimelineClip)
		wantErr string
	}{
		{name: "missing exact reference", mutate: func(c *VideoTimelineClip) { c.AudioSource = nil }, wantErr: "requires audio_source"},
		{name: "arbitrary source ref", mutate: func(c *VideoTimelineClip) { c.SourceRef = "/music/song.mp3" }, wantErr: "must use only"},
		{name: "visible audio", mutate: func(c *VideoTimelineClip) { c.Visible = true }, wantErr: "must not be visible"},
		{name: "visual captions", mutate: func(c *VideoTimelineClip) {
			c.Captions = []VideoTextOverlay{{ID: "caption", Text: "no", StartMs: 0, EndMs: 1}}
		}, wantErr: "cannot carry visual captions"},
		{name: "source duration mismatch", mutate: func(c *VideoTimelineClip) { c.SourceEndMs++ }, wantErr: "duration must match its source trim"},
		{name: "timeline duration mismatch", mutate: func(c *VideoTimelineClip) { c.TimelineEndMs++ }, wantErr: "duration must match its timeline range"},
		{name: "gain out of range", mutate: func(c *VideoTimelineClip) { c.Volume = 2.1 }, wantErr: "gain must be between"},
		{name: "bad exact ref digest", mutate: func(c *VideoTimelineClip) { c.AudioSource.Ref = "audiosrc_" + strings.Repeat("z", 64) }, wantErr: "complete exact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clip := validAudioTimelineClip("soundtrack")
			tc.mutate(&clip)
			timeline := VideoProjectTimeline{Width: 1920, Height: 1080, FPS: 30, TotalDurationMs: clip.TimelineEndMs, Clips: []VideoTimelineClip{clip}}
			err := validateVideoTimeline(timeline)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateVideoTimeline() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestAudioOnlyClipCannotParticipateInVisualTransition(t *testing.T) {
	timeline := VideoProjectTimeline{
		Width: 1920, Height: 1080, FPS: 30, TotalDurationMs: 3000,
		Clips: []VideoTimelineClip{
			{ID: "visual", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 3000, DurationMs: 3000, Visible: true},
			validAudioTimelineClip("soundtrack"),
		},
		Transitions: []VideoTimelineTransition{{ID: "bad", Kind: VideoTransitionKindCut, FromClipID: "visual", ToClipID: "soundtrack"}},
	}
	if err := validateVideoTimeline(timeline); err == nil || !strings.Contains(err.Error(), "cannot reference audio-only clip") {
		t.Fatalf("expected visual transition rejection, got %v", err)
	}
}

func TestAudioClipEditOperationsAddUpdateReplaceAndRemove(t *testing.T) {
	base := VideoProjectTimeline{Width: 1920, Height: 1080, FPS: 30, TotalDurationMs: 3000, Clips: []VideoTimelineClip{
		{ID: "visual", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 3000, DurationMs: 3000, Visible: true},
	}}
	added := validAudioTimelineClip("soundtrack")
	updated := added
	updated.Volume = 0.4
	replacement := updated
	replacement.ID = "music"
	operations := []VideoEditOperation{
		{ID: "add", Type: VideoEditOperationAddClip, Clip: &added},
		{ID: "update", Type: VideoEditOperationUpdateClip, Clip: &updated},
		{ID: "replace", Type: VideoEditOperationReplaceClip, ClipID: "soundtrack", Clip: &replacement},
		{ID: "remove", Type: VideoEditOperationRemoveClip, ClipID: "music"},
	}
	for _, operation := range operations {
		if err := validateVideoEditOperations([]VideoEditOperation{operation}); err != nil {
			t.Fatalf("operation %q rejected: %v", operation.ID, err)
		}
	}
	timeline, err := applyVideoEditOperations(base, operations, []string{"add", "update", "replace"})
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Clips) != 2 || timeline.Clips[1].ID != "music" || timeline.Clips[1].Volume != 0.4 || timeline.Clips[1].Visible {
		t.Fatalf("audio operations produced unexpected timeline: %+v", timeline.Clips)
	}
	removed, err := applyVideoEditOperations(timeline, operations, []string{"remove"})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.Clips) != 1 || removed.Clips[0].ID != "visual" {
		t.Fatalf("audio removal changed visual timeline: %+v", removed.Clips)
	}
}
