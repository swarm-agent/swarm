package videorender

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestResolveDimensions(t *testing.T) {
	tests := []struct {
		name     string
		timeline pebblestore.VideoProjectTimeline
		expected RenderDimensions
	}{
		{
			name:     "Landscape 1080p preset",
			timeline: pebblestore.VideoProjectTimeline{OutputPreset: pebblestore.VideoPresetLandscape1080p},
			expected: RenderDimensions{Width: 1920, Height: 1080},
		},
		{
			name:     "Landscape 720p preset",
			timeline: pebblestore.VideoProjectTimeline{OutputPreset: pebblestore.VideoPresetLandscape720p},
			expected: RenderDimensions{Width: 1280, Height: 720},
		},
		{
			name:     "Portrait 1080p preset",
			timeline: pebblestore.VideoProjectTimeline{OutputPreset: pebblestore.VideoPresetPortrait1080p},
			expected: RenderDimensions{Width: 1080, Height: 1920},
		},
		{
			name:     "Square 1080p preset",
			timeline: pebblestore.VideoProjectTimeline{OutputPreset: pebblestore.VideoPresetSquare1080p},
			expected: RenderDimensions{Width: 1080, Height: 1080},
		},
		{
			name:     "X Header preset",
			timeline: pebblestore.VideoProjectTimeline{OutputPreset: pebblestore.VideoPresetXHeader},
			expected: RenderDimensions{Width: 1500, Height: 500},
		},
		{
			name:     "Custom even dimensions",
			timeline: pebblestore.VideoProjectTimeline{Width: 800, Height: 600},
			expected: RenderDimensions{Width: 800, Height: 600},
		},
		{
			name:     "Custom odd dimensions rounded to even",
			timeline: pebblestore.VideoProjectTimeline{Width: 801, Height: 599},
			expected: RenderDimensions{Width: 800, Height: 598},
		},
		{
			name:     "Custom dimensions clamped to min/max",
			timeline: pebblestore.VideoProjectTimeline{Width: 20, Height: 5000},
			expected: RenderDimensions{Width: 64, Height: 3840},
		},
		{
			name:     "Default fallback",
			timeline: pebblestore.VideoProjectTimeline{},
			expected: RenderDimensions{Width: 1920, Height: 1080},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveDimensions(tt.timeline)
			if got != tt.expected {
				t.Fatalf("expected %+v, got %+v", tt.expected, got)
			}
		})
	}
}

func TestResolveFPS(t *testing.T) {
	tests := []struct {
		name     string
		fps      float64
		expected float64
	}{
		{"Default zero", 0, 30.0},
		{"Negative", -10, 30.0},
		{"Clamped below min", 0.5, 1.0},
		{"Valid 24 fps", 24.0, 24.0},
		{"Valid 29.97 fps", 29.97, 29.97},
		{"Valid 60 fps", 60.0, 60.0},
		{"Clamped above max", 120.0, 60.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveFPS(pebblestore.VideoProjectTimeline{FPS: tt.fps})
			if got != tt.expected {
				t.Fatalf("expected %f, got %f", tt.expected, got)
			}
		})
	}
}

func TestBuildFFmpegCommandLineSingleInput(t *testing.T) {
	timeline := pebblestore.VideoProjectTimeline{
		OutputPreset: pebblestore.VideoPresetLandscape1080p,
		FPS:          30,
		Clips: []pebblestore.VideoTimelineClip{
			{
				ID:         "c1",
				DurationMs: 5000,
				Visible:    true,
			},
		},
	}

	inputs := []MaterializedInput{
		{
			Index:      0,
			ClipID:     "c1",
			FilePath:   "/tmp/input_0.mp4",
			DurationMs: 5000,
			StartMs:    1000,
			EndMs:      4000,
			IsVideo:    true,
			HasAudio:   true,
			Volume:     1.5,
		},
	}

	outputPath := "/tmp/output.mp4"
	plan, err := BuildFFmpegCommandLine(timeline, inputs, outputPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if plan.Dimensions.Width != 1920 || plan.Dimensions.Height != 1080 {
		t.Fatalf("expected 1920x1080, got %dx%d", plan.Dimensions.Width, plan.Dimensions.Height)
	}

	joinedArgs := strings.Join(plan.FFmpegArgs, " ")
	if !strings.Contains(joinedArgs, "-c:v libx264") {
		t.Errorf("expected libx264 in args: %s", joinedArgs)
	}
	if !strings.Contains(joinedArgs, "-pix_fmt yuv420p") {
		t.Errorf("expected yuv420p in args: %s", joinedArgs)
	}
	if !strings.Contains(joinedArgs, "-movflags +faststart") {
		t.Errorf("expected +faststart in args: %s", joinedArgs)
	}
	if !strings.Contains(joinedArgs, "-c:a aac") {
		t.Errorf("expected aac in args: %s", joinedArgs)
	}
	if !strings.Contains(joinedArgs, "trim=start=1.000:duration=3.000") {
		t.Errorf("expected trim in args: %s", joinedArgs)
	}
	if !strings.Contains(joinedArgs, "volume=1.50") {
		t.Errorf("expected volume filter in args: %s", joinedArgs)
	}
	if !strings.Contains(joinedArgs, outputPath) {
		t.Errorf("expected outputPath in args: %s", joinedArgs)
	}
}

func TestBuildFFmpegCommandLineMultipleInputsAndCaptions(t *testing.T) {
	timeline := pebblestore.VideoProjectTimeline{
		OutputPreset: pebblestore.VideoPresetPortrait1080p,
		FPS:          60,
	}

	inputs := []MaterializedInput{
		{
			Index:      0,
			ClipID:     "c1",
			FilePath:   "/tmp/in0.mp4",
			DurationMs: 3000,
			IsVideo:    true,
			HasAudio:   true,
			Captions: []pebblestore.VideoTextOverlay{
				{
					Text:      "Hello world: 100% test's \"escaped\"",
					Position:  "bottom",
					FontSize:  32,
					FontColor: "#ffffff",
					StartMs:   500,
					EndMs:     2500,
				},
			},
		},
		{
			Index:      1,
			ClipID:     "c2",
			FilePath:   "/tmp/in1.jpg",
			DurationMs: 2000,
			IsImage:    true,
			HasAudio:   false,
		},
	}

	outputPath := "/tmp/output_multi.mp4"
	plan, err := BuildFFmpegCommandLine(timeline, inputs, outputPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if plan.Dimensions.Width != 1080 || plan.Dimensions.Height != 1920 {
		t.Fatalf("expected 1080x1920, got %dx%d", plan.Dimensions.Width, plan.Dimensions.Height)
	}

	joined := strings.Join(plan.FFmpegArgs, " ")
	if !strings.Contains(joined, "concat=n=2:v=1:a=0[v_join_1]") {
		t.Errorf("expected video concat in filter complex: %s", joined)
	}
	if !strings.Contains(joined, "concat=n=2:v=0:a=1[a_join_1]") {
		t.Errorf("expected audio concat in filter complex: %s", joined)
	}
	if !strings.Contains(joined, "drawtext=") {
		t.Errorf("expected drawtext filter in args: %s", joined)
	}
	if !strings.Contains(joined, "between(t,0.500,2.500)") {
		t.Errorf("expected caption enable between in args: %s", joined)
	}
	if !strings.Contains(joined, "aevalsrc=0") {
		t.Errorf("expected silence audio generator for image clip: %s", joined)
	}
}

func TestBuildFFmpegCommandLineTransitions(t *testing.T) {
	inputs := []MaterializedInput{
		{Index: 0, ClipID: "one", FilePath: "one.mp4", DurationMs: 3000, HasAudio: true},
		{Index: 1, ClipID: "two", FilePath: "two.mp4", DurationMs: 2000, HasAudio: true},
		{Index: 2, ClipID: "three", FilePath: "three.mp4", DurationMs: 4000, HasAudio: true},
	}
	timeline := pebblestore.VideoProjectTimeline{Transitions: []pebblestore.VideoTimelineTransition{
		{ID: "dissolve", Kind: pebblestore.VideoTransitionKindCrossfade, FromClipID: "one", ToClipID: "two", DurationMs: 500},
		{ID: "black", Kind: pebblestore.VideoTransitionKindFadeToBlack, FromClipID: "two", ToClipID: "three", DurationMs: 250},
	}}
	plan, err := BuildFFmpegCommandLine(timeline, inputs, "output.mp4")
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine() error = %v", err)
	}
	for _, want := range []string{
		"xfade=transition=fade:duration=0.500:offset=2.500",
		"acrossfade=d=0.500:c1=tri:c2=tri",
		"xfade=transition=fadeblack:duration=0.250:offset=4.250",
		"acrossfade=d=0.250:c1=tri:c2=tri",
	} {
		if !strings.Contains(plan.FilterComplex, want) {
			t.Errorf("filter complex missing %q: %s", want, plan.FilterComplex)
		}
	}
	if plan.TotalDurationMs != 8250 {
		t.Fatalf("TotalDurationMs = %d, want 8250", plan.TotalDurationMs)
	}
}

func TestBuildFFmpegCommandLinePreservesDeclaredDurationAcrossTransitionOverlap(t *testing.T) {
	timeline := pebblestore.VideoProjectTimeline{
		TotalDurationMs: 8000,
		FPS:             30,
		Transitions: []pebblestore.VideoTimelineTransition{{
			ID: "dissolve", Kind: pebblestore.VideoTransitionKindCrossfade,
			FromClipID: "one", ToClipID: "two", DurationMs: 300,
		}},
	}
	inputs := []MaterializedInput{
		{Index: 0, ClipID: "one", FilePath: "one.png", IsImage: true, DurationMs: 4000, TimelineEndMs: 4000},
		{Index: 1, ClipID: "two", FilePath: "two.png", IsImage: true, DurationMs: 4000, TimelineStartMs: 4000, TimelineEndMs: 8000},
	}

	plan, err := BuildFFmpegCommandLine(timeline, inputs, "output.mp4")
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine() error = %v", err)
	}
	if plan.TotalDurationMs != 8000 {
		t.Fatalf("TotalDurationMs = %d, want 8000", plan.TotalDurationMs)
	}
	for _, want := range []string{
		"tpad=stop_mode=clone:stop_duration=0.300,trim=duration=8.000",
		"apad=pad_dur=0.300,atrim=duration=8.000",
	} {
		if !strings.Contains(plan.FilterComplex, want) {
			t.Fatalf("filter complex missing %q: %s", want, plan.FilterComplex)
		}
	}
}

func TestBuildFFmpegCommandLineNormalizesTransitionInputs(t *testing.T) {
	inputs := []MaterializedInput{
		{Index: 0, ClipID: "one", FilePath: "one.mp4", DurationMs: 1200},
		{Index: 1, ClipID: "two", FilePath: "two.mp4", DurationMs: 1200},
		{Index: 2, ClipID: "three", FilePath: "three.mp4", DurationMs: 1200},
	}
	timeline := pebblestore.VideoProjectTimeline{FPS: 30, Transitions: []pebblestore.VideoTimelineTransition{
		{ID: "cut", Kind: pebblestore.VideoTransitionKindCut, FromClipID: "one", ToClipID: "two"},
		{ID: "dissolve", Kind: pebblestore.VideoTransitionKindCrossfade, FromClipID: "two", ToClipID: "three", DurationMs: 200},
	}}

	plan, err := BuildFFmpegCommandLine(timeline, inputs, "output.mp4")
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine() error = %v", err)
	}
	const normalizedTail = "fps=30.00,format=pix_fmts=yuv420p,settb=AVTB,setpts=PTS-STARTPTS"
	if got := strings.Count(plan.FilterComplex, normalizedTail); got != len(inputs) {
		t.Fatalf("normalized video input count = %d, want %d: %s", got, len(inputs), plan.FilterComplex)
	}
	if !strings.Contains(plan.FilterComplex, "[v_join_1][v2]xfade=") {
		t.Fatalf("test graph does not cover concat followed by xfade: %s", plan.FilterComplex)
	}
}

func TestBuildFFmpegCommandLineOrdersPrimaryClipsByTimelinePosition(t *testing.T) {
	timeline := pebblestore.VideoProjectTimeline{
		TotalDurationMs: 6000,
		Transitions: []pebblestore.VideoTimelineTransition{
			{ID: "one-to-two", Kind: pebblestore.VideoTransitionKindCut, FromClipID: "one", ToClipID: "two"},
			{ID: "two-to-three", Kind: pebblestore.VideoTransitionKindCut, FromClipID: "two", ToClipID: "three"},
		},
	}
	inputs := []MaterializedInput{
		{Index: 0, ClipID: "one", FilePath: "one.mp4", DurationMs: 1000, TimelineStartMs: 0, TimelineEndMs: 1000},
		{Index: 1, ClipID: "three", FilePath: "three.mp4", DurationMs: 3000, TimelineStartMs: 3000, TimelineEndMs: 6000},
		{Index: 2, ClipID: "two", FilePath: "two.mp4", DurationMs: 2000, TimelineStartMs: 1000, TimelineEndMs: 3000},
	}

	plan, err := BuildFFmpegCommandLine(timeline, inputs, "output.mp4")
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine() error = %v", err)
	}
	if !strings.Contains(plan.FilterComplex, "[v0][v2]concat=n=2:v=1:a=0[v_join_2]") {
		t.Fatalf("first timeline boundary was not rendered first: %s", plan.FilterComplex)
	}
	if !strings.Contains(plan.FilterComplex, "[v_join_2][v1]concat=n=2:v=1:a=0[v_join_1]") {
		t.Fatalf("second timeline boundary was not rendered second: %s", plan.FilterComplex)
	}
}

func TestBuildFFmpegCommandLinePlacesLayeredClipAtRequestedTimelineRange(t *testing.T) {
	timeline := pebblestore.VideoProjectTimeline{
		TotalDurationMs: 20000,
		Clips: []pebblestore.VideoTimelineClip{
			{ID: "step-1", Track: 0, Sequence: 0, TimelineStartMs: 0, TimelineEndMs: 6000, DurationMs: 6000, Visible: true},
			{ID: "step-2", Track: 0, Sequence: 1, TimelineStartMs: 6000, TimelineEndMs: 13000, DurationMs: 7000, Visible: true},
			{ID: "step-3", Track: 0, Sequence: 2, TimelineStartMs: 13000, TimelineEndMs: 20000, DurationMs: 7000, Visible: true},
			{ID: "step-1-footage", Track: 1, Sequence: 0, Layer: 1, TimelineStartMs: 0, TimelineEndMs: 1000, DurationMs: 1000, Visible: true, Muted: true},
		},
	}
	inputs := []MaterializedInput{
		{Index: 0, ClipID: "step-1", FilePath: "step-1.jpg", IsImage: true, DurationMs: 6000, TimelineEndMs: 6000},
		{Index: 1, ClipID: "step-2", FilePath: "step-2.jpg", IsImage: true, DurationMs: 7000, TimelineStartMs: 6000, TimelineEndMs: 13000},
		{Index: 2, ClipID: "step-3", FilePath: "step-3.jpg", IsImage: true, DurationMs: 7000, TimelineStartMs: 13000, TimelineEndMs: 20000},
		{Index: 3, ClipID: "step-1-footage", FilePath: "footage.mp4", IsVideo: true, DurationMs: 1000, EndMs: 1000, Track: 1, Layer: 1, TimelineEndMs: 1000, Muted: true},
	}

	plan, err := BuildFFmpegCommandLine(timeline, inputs, "output.mp4")
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine() error = %v", err)
	}
	if plan.TotalDurationMs != 20000 {
		t.Fatalf("TotalDurationMs = %d, want 20000", plan.TotalDurationMs)
	}
	if strings.Contains(plan.FilterComplex, "[asilence3]") {
		t.Fatalf("muted overlay created an unconnected silence stream: %s", plan.FilterComplex)
	}
	for _, want := range []string{
		"[v2][v3]", // guard against accidentally appending the overlay after step 3
		"[v3]setpts=PTS-STARTPTS+0.000/TB[v_layer_shift_3]",
		"overlay=eof_action=pass:enable='between(t,0.000,1.000)'[v_layer_3]",
	} {
		if want == "[v2][v3]" {
			if strings.Contains(plan.FilterComplex, want+"concat") {
				t.Fatalf("overlay was appended after step 3: %s", plan.FilterComplex)
			}
			continue
		}
		if !strings.Contains(plan.FilterComplex, want) {
			t.Errorf("filter complex missing %q: %s", want, plan.FilterComplex)
		}
	}
}

func TestBuildFFmpegCommandLineMixesAudioOnlyInputWithoutVideoFilter(t *testing.T) {
	timeline := pebblestore.VideoProjectTimeline{
		TotalDurationMs: 4000,
		AudioPolicy:     &pebblestore.VideoAudioPolicy{MasterVolume: 0.8},
	}
	inputs := []MaterializedInput{
		{Index: 0, ClipID: "visual", FilePath: "visual.png", IsImage: true, DurationMs: 4000, TimelineEndMs: 4000},
		{Index: 1, ClipID: "music", FilePath: "music.wav", IsAudio: true, HasAudio: true, StartMs: 500, EndMs: 2500, DurationMs: 2000, Track: 1, TimelineStartMs: 1000, TimelineEndMs: 3000, Volume: 0.6},
	}

	plan, err := BuildFFmpegCommandLine(timeline, inputs, "output.mp4")
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine() error = %v", err)
	}
	for _, forbidden := range []string{"[1:v]", "[v1]", "v_layer_shift_1"} {
		if strings.Contains(plan.FilterComplex, forbidden) {
			t.Fatalf("audio-only input entered video filter chain via %q: %s", forbidden, plan.FilterComplex)
		}
	}
	for _, want := range []string{
		"[1:a]atrim=start=0.500:duration=2.000,asetpts=PTS-STARTPTS,volume=0.60",
		"[a1]adelay=1000|1000[a_layer_shift_1]",
		"amix=inputs=2:duration=first:dropout_transition=0:normalize=0",
		"volume=0.80,atrim=duration=4.000[a_master]",
	} {
		if !strings.Contains(plan.FilterComplex, want) {
			t.Errorf("filter complex missing %q: %s", want, plan.FilterComplex)
		}
	}
	if plan.TotalDurationMs != 4000 {
		t.Fatalf("TotalDurationMs = %d, want 4000", plan.TotalDurationMs)
	}
}

func TestBuildFFmpegCommandLineMutesSoundtrackAndTimelineMaster(t *testing.T) {
	timeline := pebblestore.VideoProjectTimeline{
		TotalDurationMs: 3000,
		AudioPolicy:     &pebblestore.VideoAudioPolicy{Muted: true},
	}
	inputs := []MaterializedInput{
		{Index: 0, ClipID: "visual", FilePath: "visual-with-audio.mp4", IsVideo: true, HasAudio: true, DurationMs: 3000, TimelineEndMs: 3000, Volume: 0.5},
		{Index: 1, ClipID: "music", FilePath: "music.wav", IsAudio: true, HasAudio: true, DurationMs: 3000, EndMs: 3000, Track: 1, TimelineEndMs: 3000, Muted: true},
	}

	plan, err := BuildFFmpegCommandLine(timeline, inputs, "output.mp4")
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine() error = %v", err)
	}
	if strings.Contains(plan.FilterComplex, "adelay=") || strings.Contains(plan.FilterComplex, "amix=") {
		t.Fatalf("muted soundtrack unexpectedly entered mix: %s", plan.FilterComplex)
	}
	if !strings.Contains(plan.FilterComplex, "[a0]volume=0.00,atrim=duration=3.000[a_master]") {
		t.Fatalf("timeline master mute was not applied: %s", plan.FilterComplex)
	}
}

func TestBuildFFmpegCommandLineRejectsInvalidTransitionOverlap(t *testing.T) {
	inputs := []MaterializedInput{
		{Index: 0, ClipID: "one", FilePath: "one.mp4", DurationMs: 1000, HasAudio: true},
		{Index: 1, ClipID: "two", FilePath: "two.mp4", DurationMs: 500, HasAudio: true},
	}
	timeline := pebblestore.VideoProjectTimeline{Transitions: []pebblestore.VideoTimelineTransition{{
		ID: "too_long", Kind: pebblestore.VideoTransitionKindCrossfade,
		FromClipID: "one", ToClipID: "two", DurationMs: 500,
	}}}
	_, err := BuildFFmpegCommandLine(timeline, inputs, "output.mp4")
	if err == nil || !strings.Contains(err.Error(), "shorter than both adjacent clips") {
		t.Fatalf("BuildFFmpegCommandLine() error = %v, want invalid overlap rejection", err)
	}
}

func TestBuildFFmpegCommandLineRejectsNonAdjacentTransition(t *testing.T) {
	inputs := []MaterializedInput{
		{Index: 0, ClipID: "one", FilePath: "one.mp4", DurationMs: 1000},
		{Index: 1, ClipID: "two", FilePath: "two.mp4", DurationMs: 1000},
		{Index: 2, ClipID: "three", FilePath: "three.mp4", DurationMs: 1000},
	}
	timeline := pebblestore.VideoProjectTimeline{Transitions: []pebblestore.VideoTimelineTransition{{
		ID: "skip", Kind: pebblestore.VideoTransitionKindCrossfade,
		FromClipID: "one", ToClipID: "three", DurationMs: 250,
	}}}
	_, err := BuildFFmpegCommandLine(timeline, inputs, "output.mp4")
	if err == nil || !strings.Contains(err.Error(), "adjacent visible clips") {
		t.Fatalf("BuildFFmpegCommandLine() error = %v, want adjacency rejection", err)
	}
}

func TestEscapeFFmpegDrawText(t *testing.T) {
	input := `Line 1: 50% discount on Tom's \ special`
	escaped := escapeFFmpegDrawText(input)
	if strings.Contains(escaped, `50%`) {
		t.Errorf("%% was not escaped: %s", escaped)
	}
	if strings.Contains(escaped, `Tom's`) {
		t.Errorf("' was not escaped: %s", escaped)
	}
}
