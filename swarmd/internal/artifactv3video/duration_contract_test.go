package artifactv3video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
)

// Requirement: Convert/projectTiming must accept the complete native authored
// frame-based, integral 1..60 FPS contract, including 15 minutes/30 FPS. Threat:
// a ready source is rejected, silently shortened, or published with altered Git
// receipts. Service fakes are the narrowest layer observing requests, immutable
// receipts, and no publication on timing/authority/render/commit failures.
func TestNativeDurationConversionBoundaries(t *testing.T) {
	for _, duration := range []int64{100, 60000, 60001, 85000, 120000, 900000, 1200000} {
		t.Run(fmt.Sprint(duration), func(t *testing.T) {
			project := durationProject(duration, 30)
			original := cloneProject(project)
			renderer := validFakeRenderer()
			renderer.durationMs, renderer.fps = duration, 30
			store := &fakeStore{data: map[string][]byte{}}
			got, err := New(&fakeAuthority{project: project}, renderer, store).Convert(context.Background(), "account", testSelection())
			if err != nil {
				t.Fatal(err)
			}
			if renderer.request.DurationMs != duration || renderer.request.FPS != 30 || renderer.request.AnimationAdapter != animationAdapterVersion || !reflect.DeepEqual(original, project) {
				t.Fatal("render/source contract drift")
			}
			for _, ref := range []struct {
				id       string
				duration int64
				fps      float64
			}{{got.Source.CommitOID, got.Source.DurationMs, got.Source.FPS}, {got.Fallback.CommitOID, got.Fallback.DurationMs, got.Fallback.FPS}, {got.MP4.CommitOID, got.MP4.DurationMs, got.MP4.FPS}} {
				if ref.id != project.CommitOID || ref.duration != duration || ref.fps != 30 {
					t.Fatalf("receipt drift: %+v", ref)
				}
			}
			if got.MP4.TreeOID != project.TreeOID || got.MP4.RevisionID != project.RevisionID || got.MP4.ManifestDigestSHA256 != project.ManifestDigestSHA256 || got.MP4.BuildID != project.BuildID || got.MP4.ValidationID != project.ValidationID || got.MP4.EventSeq != project.EventSeq || !store.receipts[got.MP4] || len(store.data) != 2 {
				t.Fatal("exact lineage/publication lost")
			}
			part := got.Plan.Parts[0]
			if part.DurationMs != duration || part.SourceStartMs != 0 || part.SourceEndMs != duration || part.ArtifactV3Source == nil || part.Visual != nil || part.ArtifactV2Source != nil {
				t.Fatal("native plan range lost")
			}
		})
	}
}

func durationProject(duration int64, fps int) Project {
	p := testProject()
	p.Files["index.html"] = []byte(fmt.Sprintf(`<script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":%d,"fps":%d}</script>`, duration, fps))
	return p
}

// Requirement: invalid declarations and explicit timing assertions fail before
// renderer work; zero selection values alone retain historical defaulting.
// normalizedTiming and projectTiming own this pre-publication boundary.
func TestNativeDurationRejectsInvalidTimingBeforeRender(t *testing.T) {
	for _, tc := range []struct {
		name     string
		duration int64
		fps      float64
		authored bool
	}{
		{"below-min", 99, 60, true}, {"above-max", 600001, 60, true}, {"zero-authored", 0, 60, true}, {"fps-zero", 85000, 0, true}, {"fps-over", 85000, 61, true},
		{"negative", -1, 60, false}, {"explicit-short", 99, 60, false}, {"explicit-over", 600001, 60, false}, {"fractional", 85000, 59.5, false}, {"nan", 85000, math.NaN(), false}, {"infinity", 85000, math.Inf(1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := durationProject(85000, 60)
			selection := testSelection()
			if tc.authored {
				p = durationProject(tc.duration, int(tc.fps))
			} else {
				selection.DurationMs, selection.FPS = tc.duration, tc.fps
			}
			renderer := validFakeRenderer()
			store := &fakeStore{data: map[string][]byte{}}
			got, err := New(&fakeAuthority{project: p}, renderer, store).Convert(context.Background(), "account", selection)
			if err == nil || renderer.request.DurationMs != 0 || len(store.data) != 0 || len(got.Plan.Parts) != 0 {
				t.Fatalf("invalid input had effects: %v", err)
			}
		})
	}
}

// Requirement: admitting long motion must not bypass account/user isolation,
// cancellation, stale-head revalidation or all-or-nothing derivative storage.
// Inject failures at Service.Convert boundaries and assert no output/receipts.
func TestNativeLongDurationFailuresPublishNothing(t *testing.T) {
	for _, name := range []string{"account", "user", "preflight", "render", "timing", "cancel", "head-advance", "store"} {
		t.Run(name, func(t *testing.T) {
			authority := &advancingAuthority{fakeAuthority: fakeAuthority{project: durationProject(85000, 60)}}
			renderer := validFakeRenderer()
			renderer.durationMs, renderer.fps = 85000, 60
			store := &fakeStore{data: map[string][]byte{}}
			selection := testSelection()
			account := "account"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "account":
				account = "foreign"
			case "user":
				selection.UserID = "foreign"
			case "preflight":
				renderer.preflightErr = errors.New("preflight failed")
			case "render":
				renderer.renderErr = errors.New("encode failed")
			case "timing":
				renderer.durationMs = 60000
			case "cancel":
				renderer.afterRender = cancel
			case "head-advance":
				renderer.afterRender = func() { authority.advanced = true }
			case "store":
				store.putErr = errors.New("atomic commit failed")
			}
			got, err := New(authority, renderer, store).Convert(ctx, account, selection)
			if err == nil || len(store.data) != 0 || len(store.receipts) != 0 || len(got.Plan.Parts) != 0 {
				t.Fatalf("failed long conversion published: %v", err)
			}
		})
	}
}

// Requirement: projectSections bounds the entire storyboard, even when one
// state is selected. Test 36000 aggregate frames inclusive and one more before any
// render, preventing unselected sections from escaping the aggregate budget.
func TestNativeStoryboardDurationBudget(t *testing.T) {
	for _, last := range []int64{515000, 515001} {
		for _, state := range []string{"", "first"} {
			t.Run(fmt.Sprintf("%d/%s", last, state), func(t *testing.T) {
				p := durationProject(85000, 60)
				p.Files["last.html"] = durationProject(last, 60).Files["index.html"]
				board := Storyboard{SchemaVersion: "swarm.artifact-storyboard/v3", Sections: []TemporalSection{
					{ID: "first", Title: "First", CaptureStateID: "first", Entrypoint: "index.html", DurationMs: 85000, ProductionState: "ready", FilmingRequirements: []string{"Retain motion"}},
					{ID: "last", Title: "Last", CaptureStateID: "last", Entrypoint: "last.html", DurationMs: last, ProductionState: "pending", FilmingRequirements: []string{"Film last"}},
				}}
				p.Files[StoryboardFilename], _ = json.Marshal(board)
				selection := testSelection()
				selection.CaptureStateID = state
				sections, err := projectSections(p, selection)
				if last == 515000 {
					if err != nil || len(sections) == 0 || sections[0].DurationMs != 85000 || sections[0].FPS != 60 {
						t.Fatalf("valid budget rejected: %v", err)
					}
					return
				}
				renderer := validFakeRenderer()
				store := &fakeStore{data: map[string][]byte{}}
				_, err = New(&fakeAuthority{project: p}, renderer, store).Convert(context.Background(), "account", selection)
				if err == nil || renderer.request.DurationMs != 0 || len(store.data) != 0 {
					t.Fatalf("aggregate budget escaped: %v", err)
				}
			})
		}
	}
}
