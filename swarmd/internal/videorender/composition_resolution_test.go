package videorender

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videocomposition"
)

func TestResolveTimelineCompositionPlacementsUsesAcceptedImmutablePlan(t *testing.T) {
	catalog := &videocomposition.Catalog{SchemaVersion: 1, Layouts: []videocomposition.Layout{{ID: "triple", Slots: []videocomposition.Slot{
		{ID: "left", Requirement: "left phone", Geometry: videocomposition.NormalizedRect{X: .05, Y: .1, Width: .25, Height: .8}, ZIndex: 3, Fit: videocomposition.FitCover, AlignmentX: .5, AlignmentY: .5, Mask: videocomposition.Mask{Kind: videocomposition.MaskRoundedRect, Radius: .05}, Source: &videocomposition.SourceBinding{SourceRef: "videosrc-left", MediaType: "video/mp4", SourceStartMs: 100, SourceEndMs: 2100, TimelineStartMs: 500, TimelineEndMs: 2500, AudioPolicy: videocomposition.AudioMute}},
		{ID: "center", Requirement: "center phone", Geometry: videocomposition.NormalizedRect{X: .375, Y: .1, Width: .25, Height: .8}, ZIndex: 1, Fit: videocomposition.FitContain, AlignmentX: .5, AlignmentY: .5, Mask: videocomposition.Mask{Kind: videocomposition.MaskNone}, Source: &videocomposition.SourceBinding{SourceRef: "videosrc-center", MediaType: "video/mp4", SourceStartMs: 0, SourceEndMs: 1000, TimelineStartMs: 0, TimelineEndMs: 2000, AudioPolicy: videocomposition.AudioMute}},
		{ID: "right", Requirement: "right phone", Geometry: videocomposition.NormalizedRect{X: .7, Y: .1, Width: .25, Height: .8}, ZIndex: 2, Fit: videocomposition.FitCover, AlignmentX: 1, AlignmentY: 0, Mask: videocomposition.Mask{Kind: videocomposition.MaskEllipse}, Source: &videocomposition.SourceBinding{SourceRef: "videosrc-right", MediaType: "video/mp4", SourceStartMs: 300, SourceEndMs: 1800, TimelineStartMs: 1000, TimelineEndMs: 2500, AudioPolicy: videocomposition.AudioInclude, Gain: .4}},
	}}}}
	link := &videocomposition.Link{LayoutID: "triple"}
	timeline := pebblestore.VideoProjectTimeline{Width: 1920, Height: 1080, TotalDurationMs: 4000, Clips: []pebblestore.VideoTimelineClip{{ID: "shot", DurationMs: 4000, TimelineStartMs: 2000}}, Metadata: map[string]any{
		"accepted_video_plan": pebblestore.VideoPlanProposal{CompositionCatalog: catalog, Parts: []pebblestore.VideoPlanPart{{ID: "shot", DurationMs: 4000, Composition: link}}},
	}}
	got, err := resolveTimelineCompositionPlacements(timeline)
	if err != nil { t.Fatal(err) }
	slots := got["shot"]
	if len(slots) != 3 { t.Fatalf("resolved slots = %+v", slots) }
	if slots[0].SlotID != "center" || slots[1].SlotID != "right" || slots[2].SlotID != "left" { t.Fatalf("z-order = %+v", slots) }
	if slots[0].Width != 480 || slots[0].Height != 864 || slots[0].SourceSpanMs != 1000 || slots[0].TimelineSpanMs != 2000 {
		t.Fatalf("center placement = %+v", slots[0])
	}
}

func TestResolveTimelineCompositionPlacementsRejectsMultipleIncludedAudioSources(t *testing.T) {
	source := func(ref string) *videocomposition.SourceBinding {
		return &videocomposition.SourceBinding{SourceRef: ref, MediaType: "video/mp4", SourceStartMs: 0, SourceEndMs: 1000, TimelineStartMs: 0, TimelineEndMs: 1000, AudioPolicy: videocomposition.AudioInclude, Gain: 1}
	}
	catalog := &videocomposition.Catalog{SchemaVersion: 1, Layouts: []videocomposition.Layout{{ID: "layout", Slots: []videocomposition.Slot{
		{ID: "a", Requirement: "a", Geometry: videocomposition.NormalizedRect{Width: .5, Height: 1}, Fit: videocomposition.FitCover, Mask: videocomposition.Mask{Kind: videocomposition.MaskNone}, Source: source("videosrc-a")},
		{ID: "b", Requirement: "b", Geometry: videocomposition.NormalizedRect{X: .5, Width: .5, Height: 1}, Fit: videocomposition.FitCover, Mask: videocomposition.Mask{Kind: videocomposition.MaskNone}, Source: source("videosrc-b")},
	}}}}
	timeline := pebblestore.VideoProjectTimeline{Width: 1920, Height: 1080, TotalDurationMs: 1000, Clips: []pebblestore.VideoTimelineClip{{ID: "shot", DurationMs: 1000}}, Metadata: map[string]any{
		"accepted_video_plan": pebblestore.VideoPlanProposal{CompositionCatalog: catalog, Parts: []pebblestore.VideoPlanPart{{ID: "shot", DurationMs: 1000, Composition: &videocomposition.Link{LayoutID: "layout"}}}},
	}}
	if _, err := resolveTimelineCompositionPlacements(timeline); err == nil {
		t.Fatal("multiple included composition audio sources were accepted")
	}
}

func TestResolveTimelineCompositionPlacementsRejectsUnacceptedOrInvalidPlan(t *testing.T) {
	if got, err := resolveTimelineCompositionPlacements(pebblestore.VideoProjectTimeline{Width: 1920, Height: 1080}); err != nil || len(got) != 0 { t.Fatalf("empty composition = %+v, %v", got, err) }
	catalog := &videocomposition.Catalog{SchemaVersion: 1, Layouts: []videocomposition.Layout{{ID: "layout", Slots: []videocomposition.Slot{{ID: "slot", Requirement: "capture", Geometry: videocomposition.NormalizedRect{Width: 1, Height: 1}, Fit: videocomposition.FitCover, Mask: videocomposition.Mask{Kind: videocomposition.MaskNone}}}}}}
	timeline := pebblestore.VideoProjectTimeline{Width: 1920, Height: 1080, CompositionCatalog: catalog, Clips: []pebblestore.VideoTimelineClip{{ID: "shot", DurationMs: 1000}}}
	if got, err := resolveTimelineCompositionPlacements(timeline); err != nil || len(got) != 0 { t.Fatalf("catalog without accepted links must not render = %+v, %v", got, err) }
}
