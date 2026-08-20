package pebblestore

import (
	"strings"
	"testing"
)

func proposalTestTimeline() *VideoProjectTimeline {
	return &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, Clips: []VideoTimelineClip{
		{ID: "clip_a", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 1000, DurationMs: 1000, Visible: true},
		{ID: "clip_b", Track: 0, Sequence: 1, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 1000, TimelineEndMs: 2000, DurationMs: 1000, Visible: true},
	}}
}

func TestVideoEditProposalAcceptsSelectedOperationsWithoutRendering(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Video", InitialTimeline: proposalTestTimeline(), NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: "proposal", BaseRevisionID: base.ID, Operations: []VideoEditOperation{
		{ID: "volume", Type: VideoEditOperationUpdateClip, Clip: &VideoTimelineClip{ID: "clip_a", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 1000, DurationMs: 1000, Visible: true, Volume: .5}},
		{ID: "transition", Type: VideoEditOperationAddTransition, Transition: &VideoTimelineTransition{ID: "transition_a_b", Kind: VideoTransitionKindCrossfade, FromClipID: "clip_a", ToClipID: "clip_b", DurationMs: 250}},
	}, NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	unchanged, _, _ := store.GetVideoProject("acc", "sess", project.ID)
	if unchanged.CurrentRevisionID != base.ID || unchanged.RevisionCount != 1 || unchanged.ActiveRenderJobID != "" {
		t.Fatalf("proposal advanced project or rendered: %+v", unchanged)
	}

	accepted, revision, updated, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: proposal.ID, SelectedOperationIDs: []string{"transition"}, RevisionID: "accepted_revision", AuthorPrincipal: "swarm", NowUnixMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != VideoEditProposalStatusAccepted || accepted.AcceptedRevisionID != revision.ID || revision.AcceptedProposalID != proposal.ID {
		t.Fatalf("proposal lineage missing: proposal=%+v revision=%+v", accepted, revision)
	}
	if updated.RevisionCount != 2 || updated.CurrentRevisionID != revision.ID || updated.ActiveRenderJobID != "" {
		t.Fatalf("accept state unexpected: %+v", updated)
	}
	if len(revision.Timeline.Transitions) != 1 || revision.Timeline.Clips[0].Volume == .5 {
		t.Fatalf("selected operations not applied exactly: %+v", revision.Timeline)
	}
}

func TestVideoEditProposalStaleAcceptanceFailsClosed(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Video", InitialTimeline: proposalTestTimeline(), NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, BaseRevisionID: base.ID, Operations: []VideoEditOperation{{ID: "remove", Type: VideoEditOperationRemoveClip, ClipID: "clip_b"}}, NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateVideoProjectRevision(CreateVideoProjectRevisionInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, Timeline: *proposalTestTimeline(), NowUnixMs: 250}); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: proposal.ID, SelectedOperationIDs: []string{"remove"}, NowUnixMs: 300})
	if err == nil || !strings.Contains(err.Error(), "stale video edit proposal") {
		t.Fatalf("expected stale conflict, got %v", err)
	}
	stored, _, _ := store.GetVideoEditProposal("acc", "sess", project.ID, proposal.ID)
	if stored.Status != VideoEditProposalStatusPending || stored.AcceptedRevisionID != "" {
		t.Fatalf("stale proposal mutated: %+v", stored)
	}
}

func TestVideoTimelineTransitionsAreFirstClassAndValidated(t *testing.T) {
	timeline := *proposalTestTimeline()
	timeline.Width, timeline.Height = 1920, 1080
	timeline.Transitions = []VideoTimelineTransition{{ID: "cut", Kind: VideoTransitionKindCut, FromClipID: "clip_a", ToClipID: "clip_b"}}
	if err := validateVideoTimeline(timeline); err != nil {
		t.Fatalf("valid cut rejected: %v", err)
	}
	timeline.Transitions[0] = VideoTimelineTransition{ID: "bad", Kind: VideoTransitionKindCrossfade, FromClipID: "clip_a", ToClipID: "missing", DurationMs: 250}
	if err := validateVideoTimeline(timeline); err == nil {
		t.Fatal("expected missing transition endpoint rejection")
	}
}
