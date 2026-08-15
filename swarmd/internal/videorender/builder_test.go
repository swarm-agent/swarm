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
	if !strings.Contains(joined, "concat=n=2:v=1:a=0[v_concat]") {
		t.Errorf("expected video concat in filter complex: %s", joined)
	}
	if !strings.Contains(joined, "concat=n=2:v=0:a=1[a_concat]") {
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
