package videorender

import (
	"math"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCompositionVideoFiltersContainCropAndRoundedMask(t *testing.T) {
	filters, err := compositionVideoFilters(CompositionPlacement{
		SlotID: "phone-a", X: 120, Y: 80, Width: 480, Height: 820,
		Fit: "contain", AlignmentX: .25, AlignmentY: .75,
		CropTop: .1, CropRight: .05, CropBottom: .1, CropLeft: .05,
		MaskKind: "rounded_rect", MaskRadius: .08,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(filters, ",")
	for _, want := range []string{
		"crop=w=trunc(iw*0.900000/2)*2:h=trunc(ih*0.800000/2)*2",
		"scale=480:820:force_original_aspect_ratio=decrease",
		"pad=480:820:trunc((ow-iw)*0.250000/2)*2:trunc((oh-ih)*0.750000/2)*2:color=black@0",
		"format=pix_fmts=yuva420p", "geq=", "a='if(",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("filters missing %q: %s", want, joined)
		}
	}
}

func TestCompositionVideoFiltersCoverAndEllipse(t *testing.T) {
	filters, err := compositionVideoFilters(CompositionPlacement{
		SlotID: "phone-b", Width: 360, Height: 720, Fit: "cover",
		AlignmentX: 1, AlignmentY: 0, MaskKind: "ellipse",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(filters, ",")
	for _, want := range []string{
		"scale=360:720:force_original_aspect_ratio=increase",
		"crop=360:720:trunc((iw-ow)*1.000000/2)*2:trunc((ih-oh)*0.000000/2)*2",
		"pow((X-W/2)/(W/2),2)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("filters missing %q: %s", want, joined)
		}
	}
}

func TestBuildFFmpegCommandLineComposesThreeSlotsInZOrder(t *testing.T) {
	inputs := []MaterializedInput{
		{Index: 0, ClipID: "background", FilePath: "still.png", IsImage: true, DurationMs: 6000, TimelineEndMs: 6000},
		{Index: 1, ClipID: "shot:left", FilePath: "left.mp4", IsVideo: true, DurationMs: 4000, StartMs: 1000, EndMs: 5000, Track: 1, Layer: 30, TimelineStartMs: 500, TimelineEndMs: 4500, Muted: true, Composition: &CompositionPlacement{SlotID: "left", X: 80, Y: 120, Width: 400, Height: 800, Fit: "cover", AlignmentX: .5, AlignmentY: .5, MaskKind: "rounded_rect", MaskRadius: .05, ZIndex: 30, SourceSpanMs: 4000, TimelineSpanMs: 4000}},
		{Index: 2, ClipID: "shot:center", FilePath: "center.mp4", IsVideo: true, DurationMs: 3000, EndMs: 1500, Track: 1, Layer: 10, TimelineStartMs: 1000, TimelineEndMs: 4000, Muted: true, Composition: &CompositionPlacement{SlotID: "center", X: 520, Y: 120, Width: 400, Height: 800, Fit: "contain", AlignmentX: .5, AlignmentY: .5, MaskKind: "none", ZIndex: 10, SourceSpanMs: 1500, TimelineSpanMs: 3000}},
		{Index: 3, ClipID: "shot:right", FilePath: "right.mp4", IsVideo: true, HasAudio: true, Volume: .4, DurationMs: 2000, StartMs: 500, EndMs: 2500, Track: 1, Layer: 20, TimelineStartMs: 2000, TimelineEndMs: 4000, Composition: &CompositionPlacement{SlotID: "right", X: 960, Y: 120, Width: 400, Height: 800, Fit: "cover", AlignmentX: 1, AlignmentY: 0, MaskKind: "ellipse", ZIndex: 20, SourceSpanMs: 2000, TimelineSpanMs: 2000}},
	}
	plan, err := BuildFFmpegCommandLine(structTimeline(6000), inputs, "output.mp4")
	if err != nil {
		t.Fatal(err)
	}
	filter := plan.FilterComplex
	for _, want := range []string{
		"trim=start=1.000:duration=4.000", "setpts=(PTS-STARTPTS)*2.000000000", "[v2]setpts=PTS+1.000/TB",
		"overlay=x=520:y=120:eof_action=pass:enable='between(t,1.000,4.000)'",
		"overlay=x=960:y=120:eof_action=pass:enable='between(t,2.000,4.000)'",
		"overlay=x=80:y=120:eof_action=pass:enable='between(t,0.500,4.500)'",
		"adelay=2000|2000,apad,atrim=duration=6.000", "volume=0.40",
	} {
		if !strings.Contains(filter, want) {
			t.Errorf("filter missing %q: %s", want, filter)
		}
	}
	center := strings.Index(filter, "overlay=x=520")
	right := strings.Index(filter, "overlay=x=960")
	left := strings.Index(filter, "overlay=x=80")
	if center < 0 || right < center || left < right {
		t.Fatalf("composition z-order is not 10,20,30: %s", filter)
	}
	if strings.Contains(filter, "[1:a]") || strings.Contains(filter, "[2:a]") {
		t.Fatalf("muted composition slots entered audio graph: %s", filter)
	}
}

func structTimeline(duration int64) pebblestore.VideoProjectTimeline {
	return pebblestore.VideoProjectTimeline{Width: 1440, Height: 1080, FPS: 30, TotalDurationMs: duration}
}

func TestBuildFFmpegCommandLineRejectsInvalidCompositionSpans(t *testing.T) {
	_, err := BuildFFmpegCommandLine(structTimeline(1000), []MaterializedInput{
		{Index: 0, ClipID: "background", FilePath: "still.png", IsImage: true, DurationMs: 1000, TimelineEndMs: 1000},
		{Index: 1, ClipID: "slot", FilePath: "slot.mp4", IsVideo: true, Track: 1, TimelineEndMs: 1000, Composition: &CompositionPlacement{SlotID: "slot", Width: 100, Height: 100, Fit: "cover", TimelineSpanMs: 1000}},
	}, "output.mp4")
	if err == nil || !strings.Contains(err.Error(), "positive source and timeline spans") {
		t.Fatalf("invalid composition spans error = %v", err)
	}
}

func TestBuildFFmpegCommandLineRejectsCompositionBeyondTimeline(t *testing.T) {
	_, err := BuildFFmpegCommandLine(structTimeline(1000), []MaterializedInput{
		{Index: 0, ClipID: "background", FilePath: "still.png", IsImage: true, DurationMs: 1000, TimelineEndMs: 1000},
		{Index: 1, ClipID: "slot", FilePath: "slot.mp4", IsVideo: true, Track: 1, TimelineStartMs: 500, TimelineEndMs: 1500, Composition: &CompositionPlacement{SlotID: "slot", Width: 100, Height: 100, Fit: "cover", SourceSpanMs: 1000, TimelineSpanMs: 1000}},
	}, "output.mp4")
	if err == nil || !strings.Contains(err.Error(), "exceeds total timeline duration") {
		t.Fatalf("out-of-range composition error = %v", err)
	}
}

func TestCompositionVideoFiltersRejectUnsafeGeometry(t *testing.T) {
	for _, placement := range []CompositionPlacement{
		{Width: 0, Height: 100, Fit: "cover"},
		{Width: 100, Height: 101, Fit: "cover"},
		{X: 1, Width: 100, Height: 100, Fit: "cover"},
		{Width: 100, Height: 100, Fit: "stretch"},
		{Width: 100, Height: 100, Fit: "cover", CropLeft: .8, CropRight: .2},
		{Width: 100, Height: 100, Fit: "cover", MaskKind: "rounded_rect", MaskRadius: .6},
		{Width: 100, Height: 100, Fit: "cover", AlignmentX: math.NaN()},
		{Width: 100, Height: 100, Fit: "cover", CropTop: math.Inf(1)},
	} {
		if _, err := compositionVideoFilters(placement); err == nil {
			t.Fatalf("unsafe placement accepted: %+v", placement)
		}
	}
}
