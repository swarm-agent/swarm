package pebblestore

import (
	"reflect"
	"testing"
)

// Requirement: native temporal sections remain ordered, pending filming blocks
// acceptance without changing the confirmed cut, and a later exact section
// replacement preserves all other shots. Threat: implicit acceptance, duplicate
// shots or lineage loss. Real Pebble mutations are the narrowest durable layer.
func TestNativeStoryboardPendingReplacementPreservesStableSections(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "account", "user", "studio")
	makePart := func(id, state string) VideoPlanPart {
		source, still, visual := testNativeV3VideoReference("text/html", "", "d"), testNativeV3VideoReference("image/png", "still-"+id, "e"), testNativeV3VideoReference("video/mp4", "mp4-"+id, "f")
		for _, ref := range []*ArtifactV3VideoReference{source, still, visual} {
			ref.PartID = ""
			ref.CaptureStateID = id
		}
		part := VideoPlanPart{ID: id, Title: id, DurationMs: 2000, CaptureStateID: id, FilmingRequirements: []string{"Film " + id}, ProductionState: state, ArtifactV3Source: source, ArtifactV3Still: still, ArtifactV3Visual: visual, VisualMediaType: "video/mp4", SourceEndMs: 2000, AnimationCandidates: &VideoAnimationCandidateSet{Status: VideoAnimationCandidateStatusReady, SelectedCandidateID: id, V3SelectedSource: source, V3Derivative: visual, Candidates: []VideoAnimationCandidate{{ID: id, V3Source: source}}}}
		if state == VideoProductionStatePending {
			part.ArtifactV3Visual = still
			part.VisualMediaType = "image/png"
			part.SourceEndMs = 0
			part.AnimationCandidates = nil
		}
		return part
	}
	opening, closing := makePart("opening", VideoProductionStatePending), makePart("closing", VideoProductionStateReady)
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: "project", Title: "Storyboard", InitialTimeline: &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p}, NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: "initial", BaseRevisionID: base.ID, Intent: VideoEditProposalIntentArtifactV3Convert, Plan: &VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{opening, closing}}, NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: initial.ID, NowUnixMs: 250}); err == nil {
		t.Fatal("pending filming accepted")
	}
	unchanged, _, err := store.GetVideoProject("account", "studio", project.ID)
	if err != nil || unchanged.ConfirmedRevisionID != base.ID || unchanged.CurrentRevisionID != initial.WorkingRevisionID {
		t.Fatalf("failed acceptance mutated project: %+v %v", unchanged, err)
	}
	ready := makePart("opening", VideoProductionStateReady)
	next, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: "replace-opening", BaseRevisionID: initial.WorkingRevisionID, Intent: VideoEditProposalIntentArtifactV3Convert, Plan: &VideoPlanProposal{Kind: VideoPlanKindRevision, Parts: []VideoPlanPart{ready}}, NowUnixMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	working, ok, err := store.GetVideoProjectRevision("account", "studio", project.ID, next.WorkingRevisionID)
	if err != nil || !ok || len(working.Timeline.Clips) != 2 || working.Timeline.Clips[0].ID != "opening" || working.Timeline.Clips[1].ID != "closing" {
		t.Fatalf("stable shots lost: %+v %v", working, err)
	}
	plan, err := acceptedVideoPlanFromTimeline(working.Timeline)
	if err != nil || plan == nil || len(plan.Parts) != 2 || plan.Parts[0].ProductionState != VideoProductionStateReady || !reflect.DeepEqual(plan.Parts[1], closing) || *plan.Parts[0].ArtifactV3Visual != *ready.ArtifactV3Visual {
		t.Fatalf("replacement lineage changed: %+v %v", plan, err)
	}
	unchanged, _, err = store.GetVideoProject("account", "studio", project.ID)
	if err != nil || unchanged.ConfirmedRevisionID != base.ID {
		t.Fatalf("pending replacement accepted itself: %+v %v", unchanged, err)
	}
	old, _, err := store.GetVideoProjectRevision("account", "studio", project.ID, initial.WorkingRevisionID)
	if err != nil || old.Timeline.Clips[0].MediaType != "image/png" || *old.Timeline.Clips[0].ArtifactV3Ref != *opening.ArtifactV3Still {
		t.Fatalf("original pending still changed: %+v %v", old, err)
	}
}
