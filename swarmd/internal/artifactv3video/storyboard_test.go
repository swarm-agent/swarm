package artifactv3video

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: explicit temporal order/state entries must reach rendering and
// durable native references unchanged. Threat: silently treating spatial Parts
// as shots, unknown targets, or partial rendering publishing a half storyboard.
// Service tests observe renderer requests and storage postconditions directly.
func TestTemporalStoryboardOrderStatesAndFailure(t *testing.T) {
	project := testProject()
	board := Storyboard{SchemaVersion: "swarm.artifact-storyboard/v3", Sections: []TemporalSection{
		{ID: "opening", Title: "Opening", CaptureStateID: "opening-state", Entrypoint: "opening.html", DurationMs: 4000, ProductionState: "pending", FilmingRequirements: []string{"Film the opening"}},
		{ID: "closing", Title: "Closing", CaptureStateID: "closing-state", Entrypoint: "closing.html", DurationMs: 4000, ProductionState: "ready", FilmingRequirements: []string{"Retain closing motion"}},
	}}
	body, _ := json.Marshal(board)
	project.Files[StoryboardFilename] = body
	project.Files["opening.html"], project.Files["closing.html"] = []byte("opening"), []byte("closing")
	renderer := &sectionRenderer{fakeRenderer: *validFakeRenderer()}
	storage := &fakeStore{data: map[string][]byte{}}
	service := New(&fakeAuthority{project: project}, renderer, storage)
	conversion, err := service.Convert(context.Background(), "account", testSelection())
	if err != nil {
		t.Fatal(err)
	}
	if len(conversion.Plan.Parts) != 2 || conversion.Plan.Parts[0].ID != "opening" || conversion.Plan.Parts[1].ID != "closing" {
		t.Fatalf("order lost: %+v", conversion.Plan)
	}
	first, last := conversion.Plan.Parts[0], conversion.Plan.Parts[1]
	if first.ProductionState != "pending" || first.VisualMediaType != "image/png" || first.AnimationCandidates != nil || first.ArtifactV3Visual.CaptureStateID != "opening-state" || last.VisualMediaType != "video/mp4" || renderer.entries[0] != "opening.html" || renderer.entries[1] != "closing.html" {
		t.Fatalf("state or production policy lost: %+v", conversion.Plan)
	}
	if err := pebblestore.ValidateVideoPlanForIntent(pebblestore.VideoEditProposalIntentArtifactV3Convert, conversion.Plan); err != nil {
		t.Fatal(err)
	}
	for _, selection := range []Selection{func() Selection { s := testSelection(); s.PartID = "hero"; return s }(), func() Selection { s := testSelection(); s.CaptureStateID = "unknown"; return s }()} {
		fresh := &fakeStore{data: map[string][]byte{}}
		if _, err := New(&fakeAuthority{project: project}, renderer, fresh).Convert(context.Background(), "account", selection); err == nil || len(fresh.data) != 0 {
			t.Fatalf("ignored target: err=%v writes=%d", err, len(fresh.data))
		}
	}
	fresh := &fakeStore{data: map[string][]byte{}}
	failing := &sectionRenderer{fakeRenderer: *validFakeRenderer(), failAt: 2}
	if _, err := New(&fakeAuthority{project: project}, failing, fresh).Convert(context.Background(), "account", testSelection()); err == nil || len(fresh.data) != 0 {
		t.Fatalf("partial storyboard published: err=%v writes=%d", err, len(fresh.data))
	}
	selected := testSelection()
	selected.CaptureStateID = "closing-state"
	one, err := service.Convert(context.Background(), "account", selected)
	if err != nil || len(one.Plan.Parts) != 1 || one.Plan.Parts[0].ID != "closing" {
		t.Fatalf("exact state selection=%+v err=%v", one, err)
	}
}

type sectionRenderer struct {
	fakeRenderer
	entries []string
	failAt  int
}

func (r *sectionRenderer) Render(ctx context.Context, req RenderRequest) (RenderResult, error) {
	r.entries = append(r.entries, req.Entrypoint)
	if len(r.entries) == r.failAt {
		return RenderResult{}, errors.New("injected second-state failure")
	}
	return r.fakeRenderer.Render(ctx, req)
}

// Requirement: immutable derivative A survives head advancement to B, while new
// conversions of A fail and foreign/digest/event substitutions never read bytes.
// The service has distinct current and immutable authority methods; this fixture
// deliberately rejects the old selected head to prove reads use the right one.
func TestImmutableDerivativeSurvivesSelection(t *testing.T) {
	authority := &advancingAuthority{fakeAuthority: fakeAuthority{project: testProject()}}
	storage := &fakeStore{data: map[string][]byte{}}
	service := New(authority, validFakeRenderer(), storage)
	conversion, err := service.Convert(context.Background(), "account", testSelection())
	if err != nil {
		t.Fatal(err)
	}
	authority.advanced = true
	if body, err := service.ReadVideoReference(context.Background(), "account", "user", conversion.MP4); err != nil || string(body) != string(validMP4()) {
		t.Fatalf("old derivative unavailable: %v", err)
	}
	if _, err := service.Convert(context.Background(), "account", testSelection()); err == nil {
		t.Fatal("new stale conversion accepted")
	}
	for _, mutate := range []func(*pebblestore.ArtifactV3VideoReference){func(r *pebblestore.ArtifactV3VideoReference) { r.EventSeq++ }, func(r *pebblestore.ArtifactV3VideoReference) { r.DigestSHA256 = strings.Repeat("f", 64) }, func(r *pebblestore.ArtifactV3VideoReference) { r.CommitOID = strings.Repeat("f", 40) }} {
		ref := conversion.MP4
		mutate(&ref)
		if _, err := service.ReadVideoReference(context.Background(), "account", "user", ref); err == nil {
			t.Fatal("substitution accepted")
		}
	}
	if _, err := service.ReadVideoReference(context.Background(), "foreign", "user", conversion.MP4); err == nil {
		t.Fatal("foreign account accepted")
	}
	if len(storage.data) != 2 {
		t.Fatal("failed reads changed derivative state")
	}
}

type advancingAuthority struct {
	fakeAuthority
	advanced bool
}

func (a *advancingAuthority) ReadSelectedHead(ctx context.Context, account string, s Selection) (Project, error) {
	if a.advanced {
		return Project{}, errors.New("selected head advanced")
	}
	return a.fakeAuthority.ReadSelectedHead(ctx, account, s)
}
func (a *advancingAuthority) ReadImmutableRevision(ctx context.Context, account string, s Selection) (Project, error) {
	return a.fakeAuthority.ReadSelectedHead(ctx, account, s)
}

// Requirement: head changes during rendering reject conversion before derivative
// publication. This is not a claim of atomicity across derivative/proposal stores.
func TestHeadAdvanceDuringRenderPublishesNothing(t *testing.T) {
	authority := &advancingAuthority{fakeAuthority: fakeAuthority{project: testProject()}}
	renderer := validFakeRenderer()
	renderer.afterRender = func() { authority.advanced = true }
	storage := &fakeStore{data: map[string][]byte{}}
	if _, err := New(authority, renderer, storage).Convert(context.Background(), "account", testSelection()); err == nil || len(storage.data) != 0 {
		t.Fatalf("stale publication err=%v writes=%d", err, len(storage.data))
	}
}

// Requirement: all temporal targets are validated before any renderer/storage
// work. Threat: a malformed or unselected section evades validation, aliases a
// spatial Part, escapes the project, or exceeds the total capture budget.
func TestTemporalStoryboardRejectsInvalidManifestWithoutRendering(t *testing.T) {
	cases := map[string]func(*Storyboard){
		"duplicate section":  func(b *Storyboard) { b.Sections[1].ID = b.Sections[0].ID },
		"duplicate state":    func(b *Storyboard) { b.Sections[1].CaptureStateID = b.Sections[0].CaptureStateID },
		"traversal":          func(b *Storyboard) { b.Sections[1].Entrypoint = "../outside.html" },
		"missing entry":      func(b *Storyboard) { b.Sections[1].Entrypoint = "missing.html" },
		"missing filming":    func(b *Storyboard) { b.Sections[1].FilmingRequirements = nil },
		"blank filming":      func(b *Storyboard) { b.Sections[1].FilmingRequirements = []string{" "} },
		"invalid production": func(b *Storyboard) { b.Sections[1].ProductionState = "complete" },
		"zero duration":      func(b *Storyboard) { b.Sections[1].DurationMs = 0 },
		"total budget":       func(b *Storyboard) { b.Sections[1].DurationMs = 60000 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			project := testProject()
			board := Storyboard{SchemaVersion: "swarm.artifact-storyboard/v3", Sections: []TemporalSection{
				{ID: "first", Title: "First", CaptureStateID: "first", Entrypoint: "index.html", DurationMs: 4000, ProductionState: "ready", FilmingRequirements: []string{"Keep first"}},
				{ID: "last", Title: "Last", CaptureStateID: "last", Entrypoint: "index.html", DurationMs: 4000, ProductionState: "pending", FilmingRequirements: []string{"Film last"}},
			}}
			mutate(&board)
			project.Files[StoryboardFilename], _ = json.Marshal(board)
			renderer := &sectionRenderer{fakeRenderer: *validFakeRenderer()}
			storage := &fakeStore{data: map[string][]byte{}}
			selection := testSelection()
			selection.CaptureStateID = "first" // Invalid unselected state must still fail.
			if _, err := New(&fakeAuthority{project: project}, renderer, storage).Convert(context.Background(), "account", selection); err == nil || len(renderer.entries) != 0 || len(storage.data) != 0 {
				t.Fatalf("invalid manifest reached rendering/publication: err=%v renders=%v writes=%d", err, renderer.entries, len(storage.data))
			}
		})
	}
}
