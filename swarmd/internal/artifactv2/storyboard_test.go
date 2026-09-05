package artifactv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videoproject"
)

type storyboardTestRenderer struct{ results []htmlcapture.Result }

func (r storyboardTestRenderer) Capture(_ context.Context, request htmlcapture.Request) ([]htmlcapture.Result, error) {
	if len(r.results) != 0 {
		return r.results, nil
	}
	out := make([]htmlcapture.Result, 0, len(request.StateIDs))
	for _, id := range request.StateIDs {
		out = append(out, htmlcapture.Result{StateID: id, PNG: []byte("png-" + id)})
	}
	return out, nil
}

// Requirement: storyboard metadata is authored and iterated as normal exact V2
// section parts. Threat: a compiler could reconstruct identity from prose or let
// a section/capture mapping drift from the selected part revision.
func TestStoryboardCompilerUsesStableOrderedExactParts(t *testing.T) {
	section := StoryboardSection{Version: "artifact.storyboard.section/v1", ID: "opening", CaptureStateID: "opening-state", Title: "Opening", DurationMS: 2500, CreativeDirection: "Locked push", FilmingRequirements: []string{"Locked camera"}, ProductionState: "pending", Headline: "Local-first"}
	body, _ := json.Marshal(section)
	part := pebblestore.ArtifactV2Part{ID: "part-opening", Key: "opening", Order: 2}
	product, err := (StoryboardCompiler{}).Compile(context.Background(), CompileInput{Parts: []CompilePart{{Definition: part, Revision: pebblestore.ArtifactV2PartRevision{ID: "revision-opening", Blob: pebblestore.ArtifactV2BlobReceipt{MediaType: StoryboardSectionMediaType}}, Body: body}}})
	if err != nil || product.CompilerVersion != StoryboardCompilerVersion || !strings.Contains(string(product.Bytes), `data-artifact-v2-part-id="part-opening"`) || !strings.Contains(string(product.Bytes), `data-artifact-v2-part-revision-id="revision-opening"`) {
		t.Fatalf("product=%+v err=%v", product, err)
	}
	section.ID = "reconstructed-from-prose"
	bad, _ := json.Marshal(section)
	if _, err := (StoryboardCompiler{}).Compile(context.Background(), CompileInput{Parts: []CompilePart{{Definition: part, Revision: pebblestore.ArtifactV2PartRevision{ID: "revision-bad", Blob: pebblestore.ArtifactV2BlobReceipt{MediaType: StoryboardSectionMediaType}}, Body: bad}}}); err == nil {
		t.Fatal("accepted section identity differing from durable part key")
	}
}

// Requirement: all states are validated together before still publication.
// Threat: a partial renderer result could publish a partial storyboard handoff.
func TestStoryboardValidatorRejectsIncompleteStateSet(t *testing.T) {
	parts := storyboardCompileParts(t)
	compiler := StoryboardCompiler{}
	product, err := compiler.Compile(context.Background(), CompileInput{Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	validator := StoryboardValidator{Renderer: storyboardTestRenderer{results: []htmlcapture.Result{{StateID: "opening-state", PNG: []byte("one")}}}}
	validation, err := validator.Validate(context.Background(), ValidationInput{Product: product, Parts: parts})
	if err != nil || validation.Status != pebblestore.ArtifactV2ValidationInvalid {
		t.Fatalf("validation=%+v err=%v", validation, err)
	}
}

func storyboardCompileParts(t *testing.T) []CompilePart {
	t.Helper()
	sections := []StoryboardSection{{Version: "artifact.storyboard.section/v1", ID: "opening", CaptureStateID: "opening-state", Title: "Opening", DurationMS: 1000, CreativeDirection: "A", FilmingRequirements: []string{"Camera"}, ProductionState: "pending"}, {Version: "artifact.storyboard.section/v1", ID: "proof", CaptureStateID: "proof-state", Title: "Proof", DurationMS: 1000, CreativeDirection: "B", FilmingRequirements: []string{"Screen"}, ProductionState: "ready"}}
	out := make([]CompilePart, 0, len(sections))
	for i, section := range sections {
		body, _ := json.Marshal(section)
		out = append(out, CompilePart{Definition: pebblestore.ArtifactV2Part{ID: "part-" + section.ID, Key: section.ID, Order: i + 1}, Revision: pebblestore.ArtifactV2PartRevision{ID: "revision-" + section.ID, Blob: pebblestore.ArtifactV2BlobReceipt{MediaType: StoryboardSectionMediaType}}, Body: body})
	}
	return out
}

// Requirement: one exact V2 conversion either creates one pending proposal or
// fails before Video Studio mutation. Threat: stale project/head input could
// leave a partial project change.
func TestVideoConversionRejectsStaleBaseWithoutProposalMutation(t *testing.T) {
	projects := &videoConversionProjectFake{project: pebblestore.VideoProjectSnapshot{ID: "project", CurrentRevisionID: "current"}}
	service := NewVideoConversionService(nil, projects, nil)
	_, err := service.ConvertToPendingProposal(context.Background(), identityPrincipal(), ConvertToVideoInput{RequestID: "request", VideoSessionID: "video", ProjectID: "project", BaseRevisionID: "stale", ArtifactID: "artifact", PublishedHeadID: "head"})
	if err == nil || projects.created != 0 {
		t.Fatalf("err=%v created=%d", err, projects.created)
	}
}

type videoConversionProjectFake struct {
	project pebblestore.VideoProjectSnapshot
	created int
}

func (f *videoConversionProjectFake) GetProject(_ interfacePrincipal, _, _ string) (pebblestore.VideoProjectSnapshot, bool, error) {
	return f.project, true, nil
}
func (f *videoConversionProjectFake) CreateEditProposal(context.Context, interfacePrincipal, videoprojectInput) (pebblestore.VideoEditProposalSnapshot, error) {
	f.created++
	return pebblestore.VideoEditProposalSnapshot{}, nil
}

// aliases keep the fake signatures readable while preserving compile-time
// conformance to the real typed conversion boundary.
type interfacePrincipal = identity.Principal
type videoprojectInput = videoproject.CreateEditProposalInput

func identityPrincipal() identity.Principal {
	return identity.Principal{AccountScopeID: "account", UserID: "user"}
}
