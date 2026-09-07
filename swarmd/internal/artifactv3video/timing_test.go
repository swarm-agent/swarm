package artifactv3video

import (
	"context"
	"reflect"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: Convert must propagate authenticated authored timing into render
// requests, receipts and pending plan ranges. Conflicts/invalid declarations
// must fail before rendering or storage; service fakes isolate these boundaries.
func TestConvertAuthoredTiming(t *testing.T) {
	const manifest = `<script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":8000,"fps":60}</script>`
	for _, tc := range []struct {
		name, body string
		duration   int64
		fps        float64
		fail       bool
	}{
		{"authored", manifest, 0, 0, false}, {"matching", manifest, 8000, 60, false},
		{"duration-conflict", manifest, 4000, 0, true}, {"fps-conflict", manifest, 0, 30, true},
		{"duplicate", manifest + manifest, 0, 0, true}, {"invalid", `<script id="swarm-animation-manifest" type="application/json">{}</script>`, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := testProject()
			project.Files["index.html"] = []byte(tc.body)
			original := cloneProject(project)
			selection := testSelection()
			selection.DurationMs, selection.FPS = tc.duration, tc.fps
			before := selection
			renderer := validFakeRenderer()
			renderer.durationMs, renderer.fps = 8000, 60
			store := &fakeStore{data: map[string][]byte{}}
			result, err := New(&fakeAuthority{project: project}, renderer, store).Convert(context.Background(), "account", selection)
			if !reflect.DeepEqual(project, original) || selection != before {
				t.Fatal("source or selection mutated")
			}
			if tc.fail {
				if err == nil || len(store.data) != 0 || renderer.request.DurationMs != 0 {
					t.Fatalf("failure side effects: %v %#v", err, renderer.request)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if renderer.request.DurationMs != 8000 || renderer.request.FPS != 60 {
				t.Fatal("render timing lost")
			}
			for _, ref := range []pebblestore.ArtifactV3VideoReference{result.Source, result.Fallback, result.MP4} {
				if ref.DurationMs != 8000 || ref.FPS != 60 || ref.CommitOID != project.CommitOID || ref.TreeOID != project.TreeOID {
					t.Fatalf("receipt drift: %#v", ref)
				}
			}
			if !store.receipts[result.MP4] || result.Plan.Parts[0].DurationMs != 8000 || result.Plan.Parts[0].SourceEndMs != 8000 {
				t.Fatal("plan/receipt timing lost")
			}
		})
	}
}

// Profile metadata commits a source to the authored timing/runtime contract;
// deleting its HTML declaration must not downgrade it to legacy defaults.
func TestConvertProfiledMissingDeclarationRejectsBeforeRender(t *testing.T) {
	project := testProject()
	project.Files[pebblestore.ArtifactV3ManifestFilename] = []byte(`{"entrypoint":"index.html","animation_profile":{}}`)
	renderer := validFakeRenderer()
	store := &fakeStore{data: map[string][]byte{}}
	_, err := New(&fakeAuthority{project: project}, renderer, store).Convert(context.Background(), "account", testSelection())
	if err == nil || len(store.data) != 0 || renderer.request.DurationMs != 0 {
		t.Fatalf("profile downgraded: %v", err)
	}
}
