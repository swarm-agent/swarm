package pebblestore

import (
	"encoding/json"
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
	working, _, _ := store.GetVideoProject("acc", "sess", project.ID)
	if working.CurrentRevisionID != proposal.WorkingRevisionID || working.CurrentRevisionNumber != proposal.WorkingRevisionNumber || working.ConfirmedRevisionID != base.ID || working.RevisionCount != 2 || working.ActiveRenderJobID != "" {
		t.Fatalf("proposal did not create the expected visible working revision: %+v", working)
	}
	workingRevision, ok, err := store.GetVideoProjectRevision("acc", "sess", project.ID, proposal.WorkingRevisionID)
	if err != nil || !ok || workingRevision.Timeline.Clips[0].Volume != .5 || len(workingRevision.Timeline.Transitions) != 1 {
		t.Fatalf("working revision did not apply every proposed change: revision=%+v ok=%v err=%v", workingRevision, ok, err)
	}

	accepted, revision, updated, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: proposal.ID, SelectedOperationIDs: []string{"transition"}, RevisionID: "accepted_revision", AuthorPrincipal: "swarm", NowUnixMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != VideoEditProposalStatusAccepted || accepted.AcceptedRevisionID != revision.ID || revision.AcceptedProposalID != proposal.ID {
		t.Fatalf("proposal lineage missing: proposal=%+v revision=%+v", accepted, revision)
	}
	if updated.RevisionCount != 3 || updated.CurrentRevisionID != revision.ID || updated.ConfirmedRevisionID != revision.ID || updated.ActiveRenderJobID != "" {
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

func videoPlanTestPart(id, title, variant string) VideoPlanPart {
	return VideoPlanPart{ID: id, Title: title, DurationMs: 1000, Visual: &SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "slides", VariantID: variant, EventSeq: 1}, VisualMediaType: "image/png"}
}

func TestVideoPlanProposalRejectsPartialAcceptance(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Video", InitialTimeline: &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, Clips: []VideoTimelineClip{}}, NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, BaseRevisionID: base.ID, Plan: &VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{videoPlanTestPart("part-1", "Hook", "visual-1"), videoPlanTestPart("part-2", "Close", "visual-2")}}, NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: proposal.ID, SelectedOperationIDs: []string{"part-1"}, NowUnixMs: 300})
	if err == nil || !strings.Contains(err.Error(), "accepted as one object") {
		t.Fatalf("expected atomic plan rejection, got %v", err)
	}
	_, _, _, err = store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: proposal.ID, NowUnixMs: 301})
	if err != nil {
		t.Fatalf("expected whole initial visual plan acceptance: %v", err)
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

func TestVideoPlanProposalIsAtomicUntimedContextAndRejectionKeepsFeedback(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Video", InitialTimeline: &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, Clips: []VideoTimelineClip{}}, NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	parts := []VideoPlanPart{
		videoPlanTestPart("part-1", "Hook", "visual-1"),
		videoPlanTestPart("part-2", "Explain", "visual-2"),
		videoPlanTestPart("part-3", "Close", "visual-3"),
	}
	parts[0].Narration = "Open the story"
	parts[1].TransitionIn = "Possible hard cut; user must approve later"
	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: "plan", BaseRevisionID: base.ID, Plan: &VideoPlanProposal{Kind: VideoPlanKindInitial, Summary: "Three-part structure", Parts: parts}, NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	working, _, _ := store.GetVideoProject("acc", "sess", project.ID)
	if working.CurrentRevisionID != proposal.WorkingRevisionID || working.ConfirmedRevisionID != base.ID || working.RevisionCount != 2 {
		t.Fatalf("pending plan did not preserve the confirmed checkpoint while exposing its working revision: %+v", working)
	}
	accepted, revision, updated, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: proposal.ID, RevisionID: "accepted-plan", NowUnixMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != VideoEditProposalStatusAccepted || updated.CurrentRevisionID != revision.ID || len(revision.Timeline.Clips) != 3 || revision.Timeline.Clips[1].ArtifactRef.VariantID != "visual-2" {
		t.Fatalf("visual plan acceptance unexpected: proposal=%+v revision=%+v project=%+v", accepted, revision, updated)
	}
	encodedPlan, err := json.Marshal(revision.Timeline.Metadata["accepted_video_plan"])
	if err != nil || !strings.Contains(string(encodedPlan), `"part-3"`) {
		t.Fatalf("accepted plan context missing: %s err=%v", encodedPlan, err)
	}

	replacement, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: "replacement", BaseRevisionID: revision.ID, Plan: &VideoPlanProposal{Kind: VideoPlanKindRevision, Parts: []VideoPlanPart{videoPlanTestPart("part-2", "Explain", "visual-2b")}}, NowUnixMs: 400})
	if err != nil {
		t.Fatal(err)
	}
	rejected, _, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: replacement.ID, Reject: true, RejectionFeedback: "Make part two more visual", NowUnixMs: 500})
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != VideoEditProposalStatusRejected || rejected.RejectionFeedback != "Make part two more visual" {
		t.Fatalf("rejection feedback missing: %+v", rejected)
	}
}

func TestVideoEditProposalsChainFromUnconfirmedWorkingRevision(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Video", InitialTimeline: proposalTestTimeline(), NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: "first", BaseRevisionID: base.ID, Operations: []VideoEditOperation{{ID: "volume", Type: VideoEditOperationUpdateClip, Clip: &VideoTimelineClip{ID: "clip_a", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 1000, DurationMs: 1000, Visible: true, Volume: .5}}}, NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: "second", BaseRevisionID: first.WorkingRevisionID, Operations: []VideoEditOperation{{ID: "remove", Type: VideoEditOperationRemoveClip, ClipID: "clip_b"}}, NowUnixMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	latest, _, _ := store.GetVideoProject("acc", "sess", project.ID)
	if latest.CurrentRevisionID != second.WorkingRevisionID || latest.ConfirmedRevisionID != base.ID || latest.RevisionCount != 3 {
		t.Fatalf("second proposal did not build on the unconfirmed working revision: %+v", latest)
	}
	latestRevision, ok, err := store.GetVideoProjectRevision("acc", "sess", project.ID, second.WorkingRevisionID)
	if err != nil || !ok || len(latestRevision.Timeline.Clips) != 1 || latestRevision.Timeline.Clips[0].Volume != .5 {
		t.Fatalf("chained working revision lost earlier unconfirmed work: revision=%+v ok=%v err=%v", latestRevision, ok, err)
	}
	if _, _, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: second.ID, Reject: true, RejectionFeedback: "restore this section", NowUnixMs: 400}); err != nil {
		t.Fatal(err)
	}
	restored, _, _ := store.GetVideoProject("acc", "sess", project.ID)
	if restored.CurrentRevisionID != first.WorkingRevisionID || restored.ConfirmedRevisionID != base.ID {
		t.Fatalf("reject did not restore the prior working revision: %+v", restored)
	}
}

func TestVisualVideoPlanRevisionAcceptsSelectedStablePart(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Video", InitialTimeline: &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, Clips: []VideoTimelineClip{}}, NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	initial := &VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{videoPlanTestPart("part-1", "Hook", "visual-1"), videoPlanTestPart("part-2", "Explain", "visual-2")}}
	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, BaseRevisionID: base.ID, Plan: initial, NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	_, acceptedBase, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: proposal.ID, NowUnixMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	acceptedWithOverlay, _, err := store.CreateVideoProjectRevision(CreateVideoProjectRevisionInput{
		AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID,
		Timeline: func() VideoProjectTimeline {
			timeline := acceptedBase.Timeline
			timeline.Clips = append(timeline.Clips, VideoTimelineClip{ID: "part-1-overlay", Track: 1, Sequence: 0, Layer: 1, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 500, DurationMs: 500, Visible: true})
			return timeline
		}(),
		NowUnixMs: 350,
	})
	if err != nil {
		t.Fatal(err)
	}
	revisionPlan := &VideoPlanProposal{Kind: VideoPlanKindRevision, Parts: []VideoPlanPart{videoPlanTestPart("part-1", "Hook", "visual-1b"), videoPlanTestPart("part-2", "Explain", "visual-2b")}}
	revisionProposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, BaseRevisionID: acceptedWithOverlay.ID, Plan: revisionPlan, NowUnixMs: 400})
	if err != nil {
		t.Fatal(err)
	}
	_, revised, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: revisionProposal.ID, SelectedOperationIDs: []string{"part-1"}, NowUnixMs: 500})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Timeline.Clips[0].ArtifactRef.VariantID != "visual-1b" || revised.Timeline.Clips[1].ArtifactRef.VariantID != "visual-2" {
		t.Fatalf("selected visual revision did not preserve unselected part: %+v", revised.Timeline.Clips)
	}
	if len(revised.Timeline.Clips) != 3 || revised.Timeline.Clips[2].ID != "part-1-overlay" || revised.Timeline.Clips[2].Track != 1 || revised.Timeline.Clips[2].TimelineEndMs != 500 {
		t.Fatalf("selected visual revision did not preserve auxiliary footage: %+v", revised.Timeline.Clips)
	}
}
