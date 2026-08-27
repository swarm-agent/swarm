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

func TestResolveVideoPlanRenderAuthorityRecoversOnlyExactHistoricalSelection(t *testing.T) {
	orbit := &SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "motion", VariantID: "orbit", EventSeq: 7}
	pulse := &SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "motion", VariantID: "pulse", EventSeq: 8}
	unlocked := VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{{ID: "signal", AnimationCandidates: &VideoAnimationCandidateSet{Status: VideoAnimationCandidateStatusAwaitingSelection, Candidates: []VideoAnimationCandidate{{ID: "orbit", Source: orbit}, {ID: "pulse", Source: pulse}}}}}}
	locked := unlocked
	locked.Parts = append([]VideoPlanPart(nil), unlocked.Parts...)
	candidates := *unlocked.Parts[0].AnimationCandidates
	candidates.SelectedCandidateID = "orbit"
	candidates.SelectedSource = orbit
	candidates.Status = VideoAnimationCandidateStatusAwaitingExport
	locked.Parts[0].AnimationCandidates = &candidates
	revision := VideoProjectRevisionSnapshot{ID: "accepted", ProjectID: "project", SessionID: "sess", CreatedAt: 200, Timeline: VideoProjectTimeline{Metadata: map[string]any{"accepted_video_plan": unlocked, "accepted_video_plan_proposal_id": "proposal"}}}
	proposal := VideoEditProposalSnapshot{ID: "proposal", ProjectID: "project", SessionID: "sess", WorkingRevisionID: "working", UpdatedAt: 150, Plan: &locked}

	resolved, err := ResolveVideoPlanRenderAuthority(revision, &proposal)
	if err != nil || resolved == nil || resolved.Parts[0].AnimationCandidates.SelectedCandidateID != "orbit" || resolved.Parts[0].AnimationCandidates.SelectedSource == nil {
		t.Fatalf("legacy render authority was not recovered exactly: plan=%+v err=%v", resolved, err)
	}
	proposal.UpdatedAt = 250
	resolved, err = ResolveVideoPlanRenderAuthority(revision, &proposal)
	if err != nil || resolved == nil || resolved.Parts[0].AnimationCandidates.SelectedCandidateID != "" {
		t.Fatalf("newer mutable proposal state leaked into history: plan=%+v err=%v", resolved, err)
	}
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

func TestVideoPlanCompilesMP4RangesAndOnlyTypedPresentation(t *testing.T) {
	visual := &SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "motion", VariantID: "clip", EventSeq: 9}
	plan := VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{
		{ID: "part-1", Title: "Motion", DurationMs: 2000, OnScreenText: "descriptive only", TransitionIn: "descriptive only", Visual: visual, VisualMediaType: "video/mp4", SourceStartMs: 500, SourceEndMs: 2500},
		{ID: "part-2", Title: "Still", DurationMs: 1000, Visual: &SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "slides", VariantID: "still", EventSeq: 3}, VisualMediaType: "image/png", Caption: &VideoTextOverlay{ID: "caption-2", Text: "Explicit", Position: "bottom", StartMs: 100, EndMs: 900}, Transition: &VideoTimelineTransition{ID: "cut-1-2", Kind: VideoTransitionKindCut, FromClipID: "part-1", ToClipID: "part-2"}},
	}}
	if err := validateVideoPlanProposal(plan); err != nil {
		t.Fatal(err)
	}
	timeline := visualVideoPlanTimeline(VideoProjectTimeline{}, plan)
	if len(timeline.Clips) != 2 || timeline.Clips[0].SourceStartMs != 500 || timeline.Clips[0].SourceEndMs != 2500 || len(timeline.Clips[0].Captions) != 0 {
		t.Fatalf("MP4 plan compilation = %+v", timeline.Clips)
	}
	if len(timeline.Clips[1].Captions) != 1 || timeline.Clips[1].Captions[0].StartMs != 2100 || timeline.Clips[1].Captions[0].EndMs != 2900 {
		t.Fatalf("typed caption compilation = %+v", timeline.Clips[1].Captions)
	}
	if len(timeline.Transitions) != 1 || timeline.Transitions[0].ID != "cut-1-2" {
		t.Fatalf("typed transition compilation = %+v", timeline.Transitions)
	}
}

func TestStoryboardImportIntentRequiresCanonicalStoryboardFields(t *testing.T) {
	ref := &SessionArtifactSelectionReference{SessionID: "session", CollectionID: "collection", VariantID: "variant", EventSeq: 1}
	plan := VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{{ID: "intro", Title: "Intro", DurationMs: 1000, Visual: ref, VisualMediaType: "image/png", CaptureStateID: "intro-state", FilmingRequirements: []string{"Locked camera"}, ProductionState: "pending", StoryboardSource: ref, StoryboardStill: ref}}}
	if err := validateVideoPlanIntent(VideoEditProposalIntentStoryboardImport, plan); err != nil {
		t.Fatal(err)
	}
	missing := plan
	missing.Parts = append([]VideoPlanPart(nil), plan.Parts...)
	missing.Parts[0].StoryboardSource = nil
	if err := validateVideoPlanIntent(VideoEditProposalIntentStoryboardImport, missing); err == nil || !strings.Contains(err.Error(), "canonical storyboard fields") {
		t.Fatalf("missing storyboard fields error = %v", err)
	}
}

func TestStoryboardReplacementMaturesOnlySelectedPartAndPreservesProvenance(t *testing.T) {
	storyboard := &SessionArtifactSelectionReference{SessionID: "session", CollectionID: "storyboard", VariantID: "source", EventSeq: 1}
	openingStill := &SessionArtifactSelectionReference{SessionID: "session", CollectionID: "stills", VariantID: "opening", EventSeq: 2}
	proofStill := &SessionArtifactSelectionReference{SessionID: "session", CollectionID: "stills", VariantID: "proof", EventSeq: 3}
	accepted := &VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{
		{ID: "opening", Title: "Opening", DurationMs: 1000, CaptureStateID: "opening-state", FilmingRequirements: []string{"Locked camera"}, ProductionState: "pending", StoryboardSource: storyboard, StoryboardStill: openingStill, Visual: openingStill, VisualMediaType: "image/png"},
		{ID: "proof", Title: "Proof", DurationMs: 1000, CaptureStateID: "proof-state", FilmingRequirements: []string{"Macro shot"}, ProductionState: "pending", StoryboardSource: storyboard, StoryboardStill: proofStill, Visual: proofStill, VisualMediaType: "image/png"},
	}}
	finished := &SessionArtifactSelectionReference{SessionID: "session", CollectionID: "footage", VariantID: "proof-final", EventSeq: 4}
	merged, err := mergeAcceptedVideoPlan(accepted, VideoPlanProposal{Kind: VideoPlanKindRevision, Parts: []VideoPlanPart{{ID: "proof", Title: "Proof final", DurationMs: 1000, Visual: finished, VisualMediaType: "video/mp4", SourceEndMs: 1000}}}, []string{"proof"})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Parts[0].ProductionState != "pending" || merged.Parts[0].Visual != openingStill {
		t.Fatalf("unselected storyboard part changed: %+v", merged.Parts[0])
	}
	replaced := merged.Parts[1]
	if replaced.ProductionState != "ready" || replaced.Visual != finished || replaced.StoryboardSource != storyboard || replaced.StoryboardStill != proofStill || replaced.CaptureStateID != "proof-state" || len(replaced.FilmingRequirements) != 1 {
		t.Fatalf("storyboard replacement lost state or provenance: %+v", replaced)
	}
}

func TestVideoPlanHTMLIterationIntentRejectsEveryGenericOrDowngradedShape(t *testing.T) {
	imageOnly := VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{videoPlanTestPart("intro", "Intro", "fallback")}}
	if err := validateVideoPlanIntent(VideoEditProposalIntentHTMLIteration, imageOnly); err == nil || !strings.Contains(err.Error(), "image-only downgrade") {
		t.Fatalf("expected image-only downgrade rejection, got %v", err)
	}

	part := videoPlanTestPart("intro", "Intro", "fallback")
	part.AnimationCandidates = &VideoAnimationCandidateSet{Status: VideoAnimationCandidateStatusAwaitingSelection, Candidates: []VideoAnimationCandidate{
		{ID: "a", Source: &SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "html", VariantID: "a", EventSeq: 1}},
		{ID: "b", Source: &SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "html", VariantID: "b", EventSeq: 2}},
	}}
	htmlPlan := VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{part}}
	if err := validateVideoPlanIntent(VideoEditProposalIntentGeneral, htmlPlan); err == nil || !strings.Contains(err.Error(), "purpose-specific html_iteration") {
		t.Fatalf("expected generic route rejection, got %v", err)
	}
	if err := validateVideoPlanIntent(VideoEditProposalIntentHTMLIteration, htmlPlan); err != nil {
		t.Fatalf("canonical HTML iteration rejected: %v", err)
	}

	multiple := VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{part, part}}
	multiple.Parts[1].ID = "second"
	multiple.Parts[1].AnimationCandidates = &VideoAnimationCandidateSet{Status: VideoAnimationCandidateStatusAwaitingSelection, Candidates: append([]VideoAnimationCandidate(nil), part.AnimationCandidates.Candidates...)}
	if err := validateVideoPlanIntent(VideoEditProposalIntentHTMLIteration, multiple); err != nil {
		t.Fatalf("multi-part HTML iteration rejected: %v", err)
	}

	missingCandidates := multiple
	missingCandidates.Parts = append([]VideoPlanPart(nil), multiple.Parts...)
	missingCandidates.Parts[1].AnimationCandidates = nil
	if err := validateVideoPlanIntent(VideoEditProposalIntentHTMLIteration, missingCandidates); err == nil || !strings.Contains(err.Error(), `part "second" is an image-only downgrade`) {
		t.Fatalf("expected per-part image-only downgrade rejection, got %v", err)
	}

	premature := multiple
	premature.Parts = append([]VideoPlanPart(nil), multiple.Parts...)
	premature.Parts[1].VisualMediaType = "video/mp4"
	if err := validateVideoPlanIntent(VideoEditProposalIntentHTMLIteration, premature); err == nil || !strings.Contains(err.Error(), `part "second"`) || !strings.Contains(err.Error(), "premature MP4") {
		t.Fatalf("expected per-part premature export rejection, got %v", err)
	}
}

func TestVideoPlanRevisionRequiresReadyAnimationOnlyForSelectedParts(t *testing.T) {
	ready := videoPlanTestPart("ready", "Ready", "ready-fallback")
	ready.AnimationCandidates = &VideoAnimationCandidateSet{Status: VideoAnimationCandidateStatusReady}
	unready := videoPlanTestPart("unready", "Unready", "unready-fallback")
	unready.AnimationCandidates = &VideoAnimationCandidateSet{Status: VideoAnimationCandidateStatusAwaitingSelection}
	plan := &VideoPlanProposal{Kind: VideoPlanKindRevision, Parts: []VideoPlanPart{ready, unready}}

	if got := unresolvedSelectedVideoAnimationPart(plan, []string{"ready"}); got != "" {
		t.Fatalf("unselected animation part blocked acceptance: %q", got)
	}
	if got := unresolvedSelectedVideoAnimationPart(plan, []string{"ready", "unready"}); got != "unready" {
		t.Fatalf("selected unready animation part = %q, want unready", got)
	}
	plan.Kind = VideoPlanKindInitial
	if got := unresolvedSelectedVideoAnimationPart(plan, nil); got != "unready" {
		t.Fatalf("initial plan must remain atomic; got %q", got)
	}
}

func TestVideoPlanRejectsInvalidMP4Range(t *testing.T) {
	part := videoPlanTestPart("part", "Motion", "motion")
	part.VisualMediaType = "video/mp4"
	part.SourceStartMs, part.SourceEndMs = 100, 500
	if err := validateVideoPlanProposal(VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{part}}); err == nil || !strings.Contains(err.Error(), "duration_ms must match") {
		t.Fatalf("expected MP4 range rejection, got %v", err)
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

func TestVisualVideoPlanRevisionPreservesAuxiliarySourceVideo(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")
	sourceVideo := VideoTimelineClip{ID: "source-video", Track: 0, Sequence: 2, SourceKind: VideoClipSourceKindSourceVideo, SourceRef: "videosrc_trusted", SourceStartMs: 12000, SourceEndMs: 16000, TimelineStartMs: 12000, TimelineEndMs: 16000, DurationMs: 4000, Visible: true}
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Mixed video", InitialTimeline: &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, TotalDurationMs: 16000, Clips: []VideoTimelineClip{sourceVideo}}, NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	first, second := videoPlanTestPart("html-1", "First HTML", "html-1-a"), videoPlanTestPart("html-2", "Second HTML", "html-2-a")
	first.DurationMs, second.DurationMs = 6000, 6000
	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, BaseRevisionID: base.ID, Plan: &VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{first, second}}, NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	working, ok, err := store.GetVideoProjectRevision("acc", "sess", project.ID, proposal.WorkingRevisionID)
	if err != nil || !ok || len(working.Timeline.Clips) != 3 {
		t.Fatalf("mixed working revision = %+v ok=%v err=%v", working, ok, err)
	}
	preserved := working.Timeline.Clips[2]
	if preserved.ID != sourceVideo.ID || preserved.SourceKind != sourceVideo.SourceKind || preserved.SourceRef != sourceVideo.SourceRef || preserved.SourceStartMs != sourceVideo.SourceStartMs || preserved.SourceEndMs != sourceVideo.SourceEndMs || preserved.TimelineStartMs != sourceVideo.TimelineStartMs || preserved.TimelineEndMs != sourceVideo.TimelineEndMs || preserved.DurationMs != sourceVideo.DurationMs || preserved.Track != sourceVideo.Track || preserved.Sequence != sourceVideo.Sequence {
		t.Fatalf("source video changed while compiling HTML plan: got %+v want %+v", preserved, sourceVideo)
	}
}

func TestVisualVideoPlanRevisionCanAppendSelectedStablePart(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "acc", "usr", "sess")
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: "project", Title: "Video", InitialTimeline: &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, Clips: []VideoTimelineClip{}}, NowUnixMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	initial := &VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{videoPlanTestPart("part-1", "Hook", "visual-1")}}
	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, BaseRevisionID: base.ID, Plan: initial, NowUnixMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	_, acceptedBase, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: proposal.ID, NowUnixMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	appendedPart := videoPlanTestPart("part-2", "Particle explosion", "visual-2")
	revisionProposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, BaseRevisionID: acceptedBase.ID, Plan: &VideoPlanProposal{Kind: VideoPlanKindRevision, Parts: []VideoPlanPart{appendedPart}}, NowUnixMs: 400})
	if err != nil {
		t.Fatal(err)
	}
	workingRevision, ok, err := store.GetVideoProjectRevision("acc", "sess", project.ID, revisionProposal.WorkingRevisionID)
	if err != nil || !ok || len(workingRevision.Timeline.Clips) != 2 || workingRevision.Timeline.Clips[1].ID != "part-2" {
		t.Fatalf("working revision did not append the new stable part: revision=%+v ok=%v err=%v", workingRevision, ok, err)
	}
	_, acceptedRevision, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{AccountScopeID: "acc", UserID: "usr", SessionID: "sess", ProjectID: project.ID, ProposalID: revisionProposal.ID, SelectedOperationIDs: []string{"part-2"}, NowUnixMs: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(acceptedRevision.Timeline.Clips) != 2 || acceptedRevision.Timeline.Clips[0].ID != "part-1" || acceptedRevision.Timeline.Clips[1].ID != "part-2" {
		t.Fatalf("accepted revision did not preserve existing parts and append selected part: %+v", acceptedRevision.Timeline.Clips)
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

func TestVideoHTMLIterationMultiPartAuthoritySurvivesAppendAndTargetedReplacement(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()
	createTestSession(t, store, "account", "user", "studio")

	artifactRef := func(collection, variant string, seq uint64) *SessionArtifactSelectionReference {
		return &SessionArtifactSelectionReference{SessionID: "studio", CollectionID: collection, VariantID: variant, EventSeq: seq}
	}
	animatedPart := func(id string, startSeq uint64) VideoPlanPart {
		fallback := artifactRef("fallback", id+"-still", startSeq)
		return VideoPlanPart{
			ID: id, Title: id, DurationMs: 1000, Visual: fallback, VisualMediaType: "image/png",
			AnimationCandidates: &VideoAnimationCandidateSet{Status: VideoAnimationCandidateStatusAwaitingSelection, Candidates: []VideoAnimationCandidate{
				{ID: id + "-a", Source: artifactRef("html", id+"-a", startSeq+1)},
				{ID: id + "-b", Source: artifactRef("html", id+"-b", startSeq+2)},
			}},
		}
	}
	project, base, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: "project", Title: "Iteration regression",
		InitialTimeline: &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p, Clips: []VideoTimelineClip{{
			ID: "retained-video", Track: 0, Sequence: 3, SourceKind: VideoClipSourceKindSourceVideo, SourceRef: "videosrc-retained",
			MediaType: "video/mp4", TimelineStartMs: 2000, TimelineEndMs: 3000, DurationMs: 1000, SourceStartMs: 0, SourceEndMs: 1000, Visible: true,
		}}}, NowUnixMs: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: "initial",
		BaseRevisionID: base.ID, Intent: VideoEditProposalIntentHTMLIteration,
		Plan: &VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{animatedPart("opening", 10), animatedPart("closing", 20)}}, NowUnixMs: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: "stale",
		BaseRevisionID: base.ID, Operations: []VideoEditOperation{{ID: "noop", Type: VideoEditOperationRemoveClip, ClipID: "missing"}}, NowUnixMs: 210,
	}); err == nil || !strings.Contains(err.Error(), "base revision") {
		t.Fatalf("stale base proposal error = %v, want base revision rejection", err)
	}

	mutateSelection := func(kind, requestID, partID, candidateID string, source, derivative *SessionArtifactSelectionReference, now int64) {
		t.Helper()
		_, err := store.ApplyV3SessionMutation(V3SessionMutationInput{
			SessionID: "studio", UserID: "user", AccountScopeID: "account", ClientRequestID: requestID, IdempotencyKey: requestID,
			PayloadHash: requestID + "-hash", Kind: kind, NowUnixMs: now,
			VideoProject: &V3VideoProjectMutation{EditProposal: &VideoEditProposalSnapshot{ID: initial.ID, ProjectID: project.ID, BaseRevisionID: initial.BaseRevisionID}, AnimationSelection: &VideoAnimationSelectionMutation{
				PartID: partID, SelectedCandidateID: candidateID, SelectedSource: source, Derivative: derivative,
			}},
		})
		if err != nil {
			t.Fatalf("%s %s: %v", kind, partID, err)
		}
	}
	openingSource := artifactRef("html", "opening-a", 11)
	openingMP4 := artifactRef("derivative", "opening-a-mp4", 31)
	closingSource := artifactRef("html", "closing-b", 22)
	closingMP4 := artifactRef("derivative", "closing-b-mp4", 32)
	mutateSelection(V3SessionMutationSelectVideoAnimationCandidate, "select-opening", "opening", "opening-a", openingSource, nil, 220)
	mutateSelection(V3SessionMutationPromoteVideoAnimationDerivative, "promote-opening", "opening", "opening-a", openingSource, openingMP4, 230)
	mutateSelection(V3SessionMutationSelectVideoAnimationCandidate, "select-closing", "closing", "closing-b", closingSource, nil, 240)
	mutateSelection(V3SessionMutationPromoteVideoAnimationDerivative, "promote-closing", "closing", "closing-b", closingSource, closingMP4, 250)

	locked, ok, err := store.GetVideoEditProposal("account", "studio", project.ID, initial.ID)
	if err != nil || !ok {
		t.Fatalf("read locked proposal: ok=%v err=%v", ok, err)
	}
	for index, want := range []struct {
		id, candidate      string
		source, derivative *SessionArtifactSelectionReference
	}{{"opening", "opening-a", openingSource, openingMP4}, {"closing", "closing-b", closingSource, closingMP4}} {
		part := locked.Plan.Parts[index]
		if part.ID != want.id || part.VisualMediaType != "video/mp4" || part.AnimationCandidates.Status != VideoAnimationCandidateStatusReady ||
			part.AnimationCandidates.SelectedCandidateID != want.candidate || part.AnimationCandidates.SelectedSource == nil || *part.AnimationCandidates.SelectedSource != *want.source ||
			part.AnimationCandidates.Derivative == nil || *part.AnimationCandidates.Derivative != *want.derivative || part.Visual == nil || *part.Visual != *want.derivative {
			t.Fatalf("locked part %d authority mismatch: %+v", index, part)
		}
	}
	revisionPart := animatedPart("closing", 40)
	revisionProposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: "replace-closing",
		BaseRevisionID: initial.WorkingRevisionID, Intent: VideoEditProposalIntentHTMLIteration,
		Plan: &VideoPlanProposal{Kind: VideoPlanKindRevision, Parts: []VideoPlanPart{revisionPart}}, NowUnixMs: 260,
	})
	if err != nil {
		t.Fatal(err)
	}
	revisionSource := artifactRef("html", "closing-a", 41)
	revisionMP4 := artifactRef("derivative", "closing-a-mp4", 51)
	mutateRevisionSelection := func(kind, requestID string, derivative *SessionArtifactSelectionReference, now int64) {
		t.Helper()
		_, mutationErr := store.ApplyV3SessionMutation(V3SessionMutationInput{
			SessionID: "studio", UserID: "user", AccountScopeID: "account", ClientRequestID: requestID, IdempotencyKey: requestID,
			PayloadHash: requestID + "-hash", Kind: kind, NowUnixMs: now,
			VideoProject: &V3VideoProjectMutation{EditProposal: &VideoEditProposalSnapshot{ID: revisionProposal.ID, ProjectID: project.ID, BaseRevisionID: revisionProposal.BaseRevisionID}, AnimationSelection: &VideoAnimationSelectionMutation{
				PartID: "closing", SelectedCandidateID: "closing-a", SelectedSource: revisionSource, Derivative: derivative,
			}},
		})
		if mutationErr != nil {
			t.Fatalf("%s revision closing: %v", kind, mutationErr)
		}
	}
	mutateRevisionSelection(V3SessionMutationSelectVideoAnimationCandidate, "select-revision-closing", nil, 270)
	mutateRevisionSelection(V3SessionMutationPromoteVideoAnimationDerivative, "promote-revision-closing", revisionMP4, 280)
	revisionWorking, ok, err := store.GetVideoProjectRevision("account", "studio", project.ID, revisionProposal.WorkingRevisionID)
	if err != nil || !ok {
		t.Fatalf("read revision working timeline: ok=%v err=%v", ok, err)
	}
	if len(revisionWorking.Timeline.Clips) != 3 || revisionWorking.Timeline.Clips[0].ID != "opening" || revisionWorking.Timeline.Clips[0].ArtifactRef == nil || *revisionWorking.Timeline.Clips[0].ArtifactRef != *openingMP4 {
		t.Fatalf("targeted working promotion dropped or changed preserved opening: %+v", revisionWorking.Timeline.Clips)
	}
	if revisionWorking.Timeline.Clips[1].ID != "closing" || revisionWorking.Timeline.Clips[1].ArtifactRef == nil || *revisionWorking.Timeline.Clips[1].ArtifactRef != *revisionMP4 {
		t.Fatalf("targeted working promotion did not replace closing: %+v", revisionWorking.Timeline.Clips)
	}
	if retained := revisionWorking.Timeline.Clips[2]; retained.ID != "retained-video" || retained.SourceRef != "videosrc-retained" || retained.TimelineStartMs != 2000 || retained.TimelineEndMs != 3000 {
		t.Fatalf("targeted working promotion changed retained video: %+v", retained)
	}

	_, accepted, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{
		AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: revisionProposal.ID, SelectedOperationIDs: []string{"closing"}, RevisionID: "accepted-initial", NowUnixMs: 300,
	})
	if err != nil {
		t.Fatal(err)
	}

	appended := videoPlanTestPart("credits", "Credits", "credits-still")
	appendProposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: "append", BaseRevisionID: accepted.ID,
		Plan: &VideoPlanProposal{Kind: VideoPlanKindRevision, Parts: []VideoPlanPart{appended}}, NowUnixMs: 400,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, appendedRevision, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{
		AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: appendProposal.ID,
		SelectedOperationIDs: []string{"credits"}, RevisionID: "accepted-append", NowUnixMs: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(appendedRevision.Timeline.Clips) != 4 {
		t.Fatalf("appended timeline clips = %+v, want four", appendedRevision.Timeline.Clips)
	}
	for index, want := range []*SessionArtifactSelectionReference{openingMP4, revisionMP4} {
		clip := appendedRevision.Timeline.Clips[index]
		if clip.ArtifactRef == nil || *clip.ArtifactRef != *want || clip.MediaType != "video/mp4" || clip.ID != []string{"opening", "closing"}[index] {
			t.Fatalf("append changed established clip %d: %+v", index, clip)
		}
	}
	if retained := appendedRevision.Timeline.Clips[3]; retained.ID != "retained-video" || retained.SourceRef != "videosrc-retained" || retained.TimelineStartMs != 3000 || retained.TimelineEndMs != 4000 {
		t.Fatalf("append did not move retained video after the new HTML part: %+v", retained)
	}

	replacement := videoPlanTestPart("credits", "Credits revised", "credits-revised")
	replaceProposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: "replace-credits", BaseRevisionID: appendedRevision.ID,
		Plan: &VideoPlanProposal{Kind: VideoPlanKindRevision, Parts: []VideoPlanPart{replacement}}, NowUnixMs: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, replaced, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{
		AccountScopeID: "account", UserID: "user", SessionID: "studio", ProjectID: project.ID, ProposalID: replaceProposal.ID,
		SelectedOperationIDs: []string{"credits"}, RevisionID: "accepted-replacement", NowUnixMs: 700,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced.Timeline.Clips) != 4 || replaced.Timeline.Clips[2].ArtifactRef == nil || replaced.Timeline.Clips[2].ArtifactRef.VariantID != "credits-revised" {
		t.Fatalf("targeted replacement did not update only appended part: %+v", replaced.Timeline.Clips)
	}
	for index, want := range []*SessionArtifactSelectionReference{openingMP4, revisionMP4} {
		clip := replaced.Timeline.Clips[index]
		if clip.ArtifactRef == nil || *clip.ArtifactRef != *want || clip.MediaType != "video/mp4" {
			t.Fatalf("targeted replacement changed non-target authority %d: %+v", index, clip)
		}
	}
	if retained := replaced.Timeline.Clips[3]; retained.ID != "retained-video" || retained.SourceRef != "videosrc-retained" || retained.TimelineStartMs != 3000 || retained.TimelineEndMs != 4000 {
		t.Fatalf("targeted replacement changed retained video timing or authority: %+v", retained)
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
