package videocomposition

import (
	"strings"
	"testing"
)

func testSlot(id string, x float64) Slot {
	return Slot{
		ID: id, Requirement: "Portrait phone video",
		Geometry: NormalizedRect{X: x, Y: .1, Width: .25, Height: .8},
		ZIndex: 1, Fit: FitCover, AlignmentX: .5, AlignmentY: .5,
		Mask: Mask{Kind: MaskRoundedRect, Radius: .05}, AspectLock: 9.0 / 16.0,
	}
}

func TestResolveLinkedOverrideDetachAndEvenGeometry(t *testing.T) {
	catalog := &Catalog{SchemaVersion: SchemaVersion, Layouts: []Layout{{
		ID: "phones", Slots: []Slot{testSlot("left", .1), testSlot("right", .65)},
	}}}
	x := .2
	link := &Link{LayoutID: "phones", Overrides: []SlotOverride{{
		SlotID: "left", Geometry: &NormalizedRect{X: x, Y: .2, Width: .3, Height: .6},
	}}}
	resolved, err := Resolve(catalog, link, 1920, 1080, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 || resolved[0].Pixels.X%2 != 0 || resolved[0].Pixels.Width%2 != 0 || resolved[0].Pixels.X != 490 {
		t.Fatalf("resolved = %#v", resolved)
	}
	detached := &Link{Detached: true, DetachedSlots: []Slot{testSlot("private", .3)}}
	resolved, err = Resolve(catalog, detached, 1920, 1080, 5000)
	if err != nil || len(resolved) != 1 || resolved[0].ID != "private" {
		t.Fatalf("detached=%#v err=%v", resolved, err)
	}
}

func TestResolveInheritanceAndDeterministicLayerOrder(t *testing.T) {
	front := testSlot("front", .5)
	front.ZIndex = 2
	back := testSlot("back", 0)
	back.ZIndex = 1
	childBack := back
	childBack.Geometry.X = .2
	catalog := &Catalog{SchemaVersion: SchemaVersion, Layouts: []Layout{
		{ID: "base", Slots: []Slot{front, back}},
		{ID: "child", ExtendsLayoutID: "base", Slots: []Slot{childBack}},
	}}
	resolved, err := Resolve(catalog, &Link{LayoutID: "child"}, 1920, 1080, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].ID != "back" || resolved[0].Geometry.X != .2 || resolved[1].ID != "front" {
		t.Fatalf("resolved=%#v", resolved)
	}
}

func TestValidationRejectsCyclesBoundsAndAmbiguousOverrides(t *testing.T) {
	cases := []struct {
		name    string
		catalog *Catalog
		link    *Link
		want    string
	}{
		{"cycle", &Catalog{SchemaVersion: 1, Layouts: []Layout{{ID: "a", ExtendsLayoutID: "b"}, {ID: "b", ExtendsLayoutID: "a"}}}, nil, "cycle"},
		{"bounds", &Catalog{SchemaVersion: 1, Layouts: []Layout{{ID: "a", Slots: []Slot{{ID: "bad", Requirement: "x", Geometry: NormalizedRect{X: .9, Y: 0, Width: .2, Height: 1}, Fit: FitContain, Mask: Mask{Kind: MaskNone}}}}}, nil, "geometry"},
		{"override", &Catalog{SchemaVersion: 1, Layouts: []Layout{{ID: "a", Slots: []Slot{testSlot("one", 0)}}}}, &Link{LayoutID: "a", Overrides: []SlotOverride{{SlotID: "missing"}}}, "missing slot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.link == nil {
				err = ValidateCatalog(tc.catalog)
			} else {
				_, err = Resolve(tc.catalog, tc.link, 1920, 1080, 1000)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}

func TestSourceTimingFitCropAndAudio(t *testing.T) {
	slot := testSlot("video", .1)
	slot.Crop = Crop{Left: .1, Right: .1}
	slot.Source = &SourceBinding{
		SourceRef: "videosrc_abc", MediaType: "video/mp4",
		SourceStartMs: 100, SourceEndMs: 4100,
		TimelineStartMs: 500, TimelineEndMs: 3500,
		AudioPolicy: AudioInclude, Gain: .75,
	}
	catalog := &Catalog{SchemaVersion: 1, Layouts: []Layout{{ID: "single", Slots: []Slot{slot}}}}
	resolved, err := Resolve(catalog, &Link{LayoutID: "single"}, 1920, 1080, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Fit != FitCover || resolved[0].Source.SourceStartMs != 100 || resolved[0].Source.TimelineEndMs != 3500 {
		t.Fatalf("resolved=%#v", resolved)
	}
	slot.Source.AudioPolicy = AudioMute
	slot.Source.Gain = 1
	catalog.Layouts[0].Slots[0] = slot
	if _, err := Resolve(catalog, &Link{LayoutID: "single"}, 1920, 1080, 4000); err == nil || !strings.Contains(err.Error(), "gain") {
		t.Fatalf("err=%v", err)
	}
}
