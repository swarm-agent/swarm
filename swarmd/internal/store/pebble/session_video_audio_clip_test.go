package pebblestore

import (
	"encoding/json"
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
