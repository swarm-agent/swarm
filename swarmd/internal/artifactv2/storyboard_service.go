package artifactv2

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type CreateStoryboardStillsInput struct {
	RequestID               string
	ArtifactID              string
	ExpectedWorkingRevision uint64
	Renderer                htmlcapture.Renderer
}

// CreateStoryboardStills renders every ordered storyboard section from one
// exact validated composition head and records one immutable V2 derivative per
// real source part. No export list is accepted from a caller.
func (s *Service) CreateStoryboardStills(ctx context.Context, principal Principal, input CreateStoryboardStillsInput) (StoryboardHead, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return StoryboardHead{}, err
	}
	if input.ExpectedWorkingRevision != working.Revision || input.Renderer == nil || strings.TrimSpace(input.RequestID) == "" {
		return StoryboardHead{}, errors.New("artifact v2 storyboard render input is incomplete or stale")
	}
	head, err := s.ResolveStoryboardHead(ctx, principal, working.ID, "")
	if err != nil {
		return StoryboardHead{}, err
	}
	build, ok, err := s.store.GetArtifactV2Build(principal.AccountScopeID, working.ID, head.Reference.BuildID)
	if err != nil || !ok || build.Output == nil {
		return StoryboardHead{}, errors.New("artifact v2 storyboard exact build was not found")
	}
	body, err := s.blobs.GetExact(ctx, principal, *build.Output)
	if err != nil {
		return StoryboardHead{}, err
	}
	states := make([]string, 0, len(head.Sections))
	for _, section := range head.Sections {
		states = append(states, section.Section.CaptureStateID)
	}
	results, err := input.Renderer.Capture(ctx, htmlcapture.Request{Entry: "index.html", Files: map[string][]byte{"index.html": body}, StateIDs: states})
	if err != nil || len(results) != len(head.Sections) {
		return StoryboardHead{}, errors.New("artifact v2 storyboard renderer did not return every exact state")
	}
	for index, result := range results {
		section := &head.Sections[index]
		if result.StateID != section.Section.CaptureStateID || len(result.PNG) == 0 {
			return StoryboardHead{}, errors.New("artifact v2 storyboard renderer returned a mismatched state")
		}
		working, err = s.mustWorking(principal, working.ID)
		if err != nil {
			return StoryboardHead{}, err
		}
		derivative := pebblestore.ArtifactV2Derivative{ID: deterministicID("deriv2", working.ID, input.RequestID, section.Part.ID, section.Revision.ID), ArtifactID: working.ID, CompositionID: head.Reference.CompositionID, CompositionDigest: head.Reference.DigestSHA256, BuildID: head.Reference.BuildID, ValidationID: head.Reference.ValidationID, PolicyRevision: head.Reference.PolicyRevision, Kind: "storyboard_still", Status: "ready", SourcePartID: section.Part.ID, SourcePartRevisionID: section.Revision.ID, CaptureStateID: section.Section.CaptureStateID}
		if existing, found, readErr := s.store.GetArtifactV2Derivative(principal.AccountScopeID, working.ID, derivative.ID); readErr != nil {
			return StoryboardHead{}, readErr
		} else if found {
			if existing.SourcePartRevisionID != section.Revision.ID || existing.CaptureStateID != result.StateID {
				return StoryboardHead{}, errors.New("artifact v2 storyboard still idempotency conflict")
			}
			section.Still = &existing
			continue
		}
		receipt, err := s.blobs.PutImmutable(ctx, principal, working.ID, "storyboard-still-"+section.Part.ID, "image/png", result.PNG)
		if err != nil {
			return StoryboardHead{}, err
		}
		derivative.Output = &receipt
		stored, err := s.recordDerivative(ctx, principal, working, fmt.Sprintf("%s:state:%d", strings.TrimSpace(input.RequestID), index+1), derivative, nil)
		if err != nil {
			return StoryboardHead{}, err
		}
		section.Still = &stored
	}
	return head, nil
}

// ResolveStoryboardHead reads one exact current or published Artifact V2 head,
// verifies build/validation evidence, and binds metadata to the selected real
// part revisions. publishedHeadID is optional; an empty value selects the exact
// current composition head without inventing a separate storyboard authority.
func (s *Service) ResolveStoryboardHead(ctx context.Context, principal Principal, artifactID, publishedHeadID string) (StoryboardHead, error) {
	principal, working, err := s.load(principal, artifactID)
	if err != nil {
		return StoryboardHead{}, err
	}
	var ref ReadyReference
	if strings.TrimSpace(publishedHeadID) != "" {
		ref, err = s.ResolveReady(principal, working.ID, publishedHeadID)
	} else {
		if working.CompositionHead == nil || working.LatestBuildID == "" || working.LatestValidationID == "" {
			return StoryboardHead{}, errors.New("artifact v2 storyboard head lacks exact build evidence")
		}
		build, ok, readErr := s.store.GetArtifactV2Build(principal.AccountScopeID, working.ID, working.LatestBuildID)
		if readErr != nil || !ok {
			return StoryboardHead{}, errors.New("artifact v2 storyboard build was not found")
		}
		validation, ok, readErr := s.store.GetArtifactV2Validation(principal.AccountScopeID, working.ID, working.LatestValidationID)
		if readErr != nil || !ok || validation.Status != pebblestore.ArtifactV2ValidationValid || validation.BuildID != build.ID || build.CompositionID != working.CompositionHead.CompositionID || validation.CompositionID != build.CompositionID || build.CompositionDigest != working.CompositionHead.DigestSHA256 || validation.CompositionDigest != build.CompositionDigest {
			return StoryboardHead{}, errors.New("artifact v2 storyboard build and validation do not match the exact head")
		}
		ref = ReadyReference{ArtifactID: working.ID, CompositionID: build.CompositionID, BuildID: build.ID, ValidationID: validation.ID, EventSeq: working.EventSeq, DigestSHA256: build.CompositionDigest, PolicyRevision: working.PolicyRevision}
	}
	if err != nil {
		return StoryboardHead{}, err
	}
	composition, ok, err := s.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, ref.CompositionID)
	if err != nil || !ok {
		return StoryboardHead{}, errors.New("artifact v2 storyboard composition was not found")
	}
	compileParts := make([]CompilePart, 0, len(composition.Parts))
	for _, selected := range composition.Parts {
		part, ok, readErr := s.store.GetArtifactV2Part(principal.AccountScopeID, working.ID, selected.PartID)
		if readErr != nil || !ok {
			return StoryboardHead{}, errors.New("artifact v2 storyboard part was not found")
		}
		revision, ok, readErr := s.store.GetArtifactV2PartRevision(principal.AccountScopeID, working.ID, selected.PartID, selected.PartRevisionID)
		if readErr != nil || !ok || revision.Blob.DigestSHA256 != selected.DigestSHA256 {
			return StoryboardHead{}, errors.New("artifact v2 storyboard part revision is stale")
		}
		body, readErr := s.blobs.GetExact(ctx, principal, revision.Blob)
		if readErr != nil {
			return StoryboardHead{}, readErr
		}
		compileParts = append(compileParts, CompilePart{Definition: part, Revision: revision, Body: body})
	}
	sections, catalog, err := storyboardSectionsFromCompileInput(compileParts)
	if err != nil {
		return StoryboardHead{}, err
	}
	sort.SliceStable(sections, func(i, j int) bool { return sections[i].Part.Order < sections[j].Part.Order })
	return StoryboardHead{Reference: ref, Sections: sections, Catalog: catalog}, nil
}
