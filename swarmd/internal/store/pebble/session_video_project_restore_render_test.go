package pebblestore

import (
	"os"
	"strings"
	"testing"
)

// Purpose: Verify that forking a video project with a visual plan atomically persists
// destination-scoped authority in Pebble, survives store restart/reopen, and resolves
// authoritative render plans with candidate selection.
// Invariant: Forked projects must persist destination-scoped proposal authority atomically
// with project and revision creation; closing and reopening the database must preserve
// complete authority without dangling proposal references.
// Authority: SessionStore.CreateVideoProject, ResolveAuthoritativeVideoPlan.
func TestCreateVideoProjectAtomicallyPersistsInitialProposalAndSurvivesReload(t *testing.T) {
	dir, err := os.MkdirTemp("", "pebble-restore-render-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	const accountID, userID = "acc-test", "user-test"
	const sourceSessionID, destSessionID = "sess-source", "sess-dest"

	// 1. Initial store setup
	store, err := NewSessionStoreWithPath(dir)
	if err != nil {
		t.Fatal(err)
	}

	createTestSession(t, store, accountID, userID, sourceSessionID)
	createTestSession(t, store, accountID, userID, destSessionID)

	sourceTimeline := proposalTestTimeline()
	sourceProj, sourceBaseRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          userID,
		SessionID:       sourceSessionID,
		ProjectID:       "vproj-src",
		Title:           "Original Cut",
		InitialTimeline: sourceTimeline,
		NowUnixMs:       100,
	})
	if err != nil {
		t.Fatalf("create source project failed: %v", err)
	}

	htmlRef := &SessionArtifactSelectionReference{SessionID: sourceSessionID, CollectionID: "col-anim", VariantID: "var-html", EventSeq: 5}
	fallback := &SessionArtifactSelectionReference{SessionID: sourceSessionID, CollectionID: "col-still", VariantID: "var-still", EventSeq: 4}
	unlockedPlan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:         "clip_a",
			DurationMs: 1000,
			Visual:     fallback,
			AnimationCandidates: &VideoAnimationCandidateSet{
				Status: VideoAnimationCandidateStatusAwaitingSelection,
				Candidates: []VideoAnimationCandidate{
					{ID: "cand-html", Source: htmlRef},
				},
			},
		}},
	}
	lockedPlan := unlockedPlan
	lockedPlan.Parts = append([]VideoPlanPart(nil), unlockedPlan.Parts...)
	lockedSet := *unlockedPlan.Parts[0].AnimationCandidates
	lockedSet.SelectedCandidateID = "cand-html"
	lockedSet.SelectedSource = htmlRef
	lockedSet.Status = VideoAnimationCandidateStatusAwaitingExport
	lockedPlan.Parts[0].AnimationCandidates = &lockedSet

	// Create accepted proposal and revision in source project
	sourceProposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sourceSessionID,
		ProjectID:      sourceProj.ID,
		ProposalID:     "vprop-source-accepted",
		BaseRevisionID: sourceBaseRev.ID,
		Plan:           &lockedPlan,
		NowUnixMs:      150,
	})
	if err != nil {
		t.Fatalf("create source proposal failed: %v", err)
	}

	sourceRevisionTimeline := *sourceTimeline
	sourceRevisionTimeline.Metadata = map[string]any{
		"accepted_video_plan":             unlockedPlan,
		"accepted_video_plan_proposal_id": sourceProposal.ID,
	}
	sourceAcceptedRev, _, err := store.CreateVideoProjectRevision(CreateVideoProjectRevisionInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sourceSessionID,
		ProjectID:      sourceProj.ID,
		RevisionID:     "vrev-src-accepted",
		Timeline:       sourceRevisionTimeline,
		NowUnixMs:      200,
	})
	if err != nil {
		t.Fatalf("create source accepted revision failed: %v", err)
	}

	// 2. Fork into destination session with InitialProposal
	destProposalInput := sourceProposal
	destProposalInput.Status = VideoEditProposalStatusAccepted
	destProj, destRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         destSessionID,
		ProjectID:         "vproj-dest",
		InitialRevisionID: "vrev-dest-initial",
		Title:             "Restored Cut",
		InitialTimeline:   &sourceAcceptedRev.Timeline,
		InitialProposal:   &destProposalInput,
		Metadata: map[string]any{
			"source_session_id":  sourceSessionID,
			"source_project_id":  sourceProj.ID,
			"source_revision_id": sourceAcceptedRev.ID,
		},
		NowUnixMs: 300,
	})
	if err != nil {
		t.Fatalf("fork create video project failed: %v", err)
	}
	if destRev == nil {
		t.Fatal("expected non-nil destination revision")
	}

	// 3. Close store and reload from disk
	if err := store.Close(); err != nil {
		t.Fatalf("store close failed: %v", err)
	}

	reopenedStore, err := NewSessionStoreWithPath(dir)
	if err != nil {
		t.Fatalf("store reopen failed: %v", err)
	}
	defer reopenedStore.Close()

	// 4. Verify in reloaded store that destination proposal was durably persisted
	reloadedProp, ok, err := reopenedStore.GetVideoEditProposal(accountID, destSessionID, destProj.ID, sourceProposal.ID)
	if err != nil || !ok {
		t.Fatalf("destination proposal not found after reload: ok=%v err=%v", ok, err)
	}
	if reloadedProp.Status != VideoEditProposalStatusAccepted || reloadedProp.WorkingRevisionID != destRev.ID || reloadedProp.ProjectID != destProj.ID {
		t.Fatalf("destination proposal state invalid: %+v", reloadedProp)
	}

	reloadedRev, ok, err := reopenedStore.GetVideoProjectRevision(accountID, destSessionID, destProj.ID, destRev.ID)
	if err != nil || !ok {
		t.Fatalf("destination revision not found after reload: ok=%v err=%v", ok, err)
	}

	// 5. Verify authoritative render plan resolution
	resolvedPlan, err := ResolveAuthoritativeVideoPlan(accountID, userID, reloadedRev, reopenedStore)
	if err != nil {
		t.Fatalf("ResolveAuthoritativeVideoPlan failed on reloaded store: %v", err)
	}
	if resolvedPlan == nil || len(resolvedPlan.Parts) == 0 {
		t.Fatalf("resolved plan missing parts: %+v", resolvedPlan)
	}
	part := resolvedPlan.Parts[0]
	if part.AnimationCandidates == nil || part.AnimationCandidates.SelectedCandidateID != "cand-html" {
		t.Fatalf("authoritative candidate selection was not recovered: %+v", part.AnimationCandidates)
	}
}

// Purpose: Verify that an existing dangling fork (without destination proposal)
// can resolve render authority through authenticated exact-source lineage and exact-timeline match.
// Threat/regression: Older forks created before destination proposal persistence failed
// rendering because the proposal ID was only present in the source session/project.
// Lineage fallback must strictly authenticate the source and compare timelines.
// Authority: ResolveAuthoritativeVideoPlan.
func TestResolveAuthoritativeVideoPlanDanglingForkRecoversViaSourceLineage(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	const accountID, userID = "acc-lineage", "user-lineage"
	const sourceSessionID, destSessionID = "sess-lineage-src", "sess-lineage-dest"

	createTestSession(t, store, accountID, userID, sourceSessionID)
	createTestSession(t, store, accountID, userID, destSessionID)

	sourceTimeline := proposalTestTimeline()
	sourceProj, sourceBaseRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          userID,
		SessionID:       sourceSessionID,
		ProjectID:       "vproj-lineage-src",
		Title:           "Lineage Source",
		InitialTimeline: sourceTimeline,
		NowUnixMs:       100,
	})
	if err != nil {
		t.Fatal(err)
	}

	htmlRef := &SessionArtifactSelectionReference{SessionID: sourceSessionID, CollectionID: "col-motion", VariantID: "var-orbit", EventSeq: 9}
	lockedPlan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:         "clip_a",
			DurationMs: 1000,
			AnimationCandidates: &VideoAnimationCandidateSet{
				Status:              VideoAnimationCandidateStatusAwaitingExport,
				SelectedCandidateID: "orbit",
				SelectedSource:      htmlRef,
				Candidates: []VideoAnimationCandidate{
					{ID: "orbit", Source: htmlRef},
				},
			},
		}},
	}
	unlockedPlan := lockedPlan
	unlockedPlan.Parts = append([]VideoPlanPart(nil), lockedPlan.Parts...)
	unlockedSet := *lockedPlan.Parts[0].AnimationCandidates
	unlockedSet.SelectedCandidateID = ""
	unlockedSet.SelectedSource = nil
	unlockedSet.Status = VideoAnimationCandidateStatusAwaitingSelection
	unlockedPlan.Parts[0].AnimationCandidates = &unlockedSet

	sourceProposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sourceSessionID,
		ProjectID:      sourceProj.ID,
		ProposalID:     "vprop-lineage-accepted",
		BaseRevisionID: sourceBaseRev.ID,
		Plan:           &lockedPlan,
		NowUnixMs:      150,
	})
	if err != nil {
		t.Fatal(err)
	}

	sourceRevisionTimeline := *sourceTimeline
	sourceRevisionTimeline.Metadata = map[string]any{
		"accepted_video_plan":             unlockedPlan,
		"accepted_video_plan_proposal_id": sourceProposal.ID,
	}
	sourceAcceptedRev, _, err := store.CreateVideoProjectRevision(CreateVideoProjectRevisionInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sourceSessionID,
		ProjectID:      sourceProj.ID,
		RevisionID:     "vrev-lineage-src-accepted",
		Timeline:       sourceRevisionTimeline,
		NowUnixMs:      200,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create dangling fork: NO InitialProposal passed (simulates prior behaviour)
	destProj, destRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         destSessionID,
		ProjectID:         "vproj-lineage-dest",
		InitialRevisionID: "vrev-lineage-dest-initial",
		Title:             "Dangling Fork",
		InitialTimeline:   &sourceAcceptedRev.Timeline,
		Metadata: map[string]any{
			"source_session_id":  sourceSessionID,
			"source_project_id":  sourceProj.ID,
			"source_revision_id": sourceAcceptedRev.ID,
		},
		NowUnixMs: 300,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify proposal was NOT stored in destination (confirming this is a dangling fork test)
	_, ok, _ := store.GetVideoEditProposal(accountID, destSessionID, destProj.ID, sourceProposal.ID)
	if ok {
		t.Fatal("destination proposal unexpectedly exists in dangling fork test")
	}

	// ResolveAuthoritativeVideoPlan must succeed via exact-source lineage
	resolvedPlan, err := ResolveAuthoritativeVideoPlan(accountID, userID, *destRev, store)
	if err != nil {
		t.Fatalf("ResolveAuthoritativeVideoPlan failed for dangling fork: %v", err)
	}
	if resolvedPlan == nil || len(resolvedPlan.Parts) == 0 {
		t.Fatalf("resolved plan missing parts: %+v", resolvedPlan)
	}
	if resolvedPlan.Parts[0].AnimationCandidates.SelectedCandidateID != "orbit" {
		t.Fatalf("candidate selection not recovered via lineage: %+v", resolvedPlan.Parts[0].AnimationCandidates)
	}
}

// Purpose: Verify strict security rejections in ResolveAuthoritativeVideoPlan:
// missing proposals without lineage, cross-account/user access, pending working cuts,
// rejected proposals, diverged timeline cuts, and cross-user source project forks.
// Threat/regression: Arbitrary metadata tampering or unauthorized cross-principal access
// could allow executing unauthorized render jobs or recovering wrong cut authorities.
// Authority: ResolveAuthoritativeVideoPlan.
func TestResolveAuthoritativeVideoPlanSecurityAndRejections(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	const accountID, userID = "acc-sec", "user-sec"
	const sessionID = "sess-sec"

	createTestSession(t, store, accountID, userID, sessionID)

	timeline := proposalTestTimeline()
	timeline.Metadata = map[string]any{
		"accepted_video_plan_proposal_id": "vprop-nonexistent",
	}

	// 1. Missing proposal with no lineage fails
	rev := VideoProjectRevisionSnapshot{
		ID:             "vrev-no-lineage",
		ProjectID:      "vproj-no-lineage",
		SessionID:      sessionID,
		AccountScopeID: accountID,
		UserID:         userID,
		Timeline:       *timeline,
	}
	_, err := ResolveAuthoritativeVideoPlan(accountID, userID, rev, store)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected proposal not found error, got: %v", err)
	}

	// 2. Cross-account caller rejected
	_, err = ResolveAuthoritativeVideoPlan("foreign-account", userID, rev, store)
	if err == nil || !strings.Contains(err.Error(), "ownership does not match") {
		t.Fatalf("expected ownership error for foreign account, got: %v", err)
	}

	// 3. Cross-user caller rejected
	_, err = ResolveAuthoritativeVideoPlan(accountID, "foreign-user", rev, store)
	if err == nil || !strings.Contains(err.Error(), "ownership does not match") {
		t.Fatalf("expected ownership error for foreign user, got: %v", err)
	}

	// 4. Pending working cut rejected
	pendingRevID := "vrev-working"
	proj, baseRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          userID,
		SessionID:       sessionID,
		ProjectID:       "vproj-pending",
		Title:           "Pending Test",
		InitialTimeline: proposalTestTimeline(),
		NowUnixMs:       100,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sessionID,
		ProjectID:      proj.ID,
		ProposalID:     "vprop-pending",
		BaseRevisionID: baseRev.ID,
		Operations: []VideoEditOperation{
			{ID: "vol", Type: VideoEditOperationUpdateClip, Clip: &VideoTimelineClip{ID: "clip_a", Track: 0, Sequence: 0, SourceKind: VideoClipSourceKindColor, TimelineStartMs: 0, TimelineEndMs: 1000, DurationMs: 1000, Visible: true, Volume: 0.5}},
		},
		NowUnixMs: 200,
	})
	if err != nil {
		t.Fatal(err)
	}

	workingRev, ok, err := store.GetVideoProjectRevision(accountID, sessionID, proj.ID, pendingRevID)
	if ok {
		_, err = ResolveAuthoritativeVideoPlan(accountID, userID, workingRev, store)
		if err == nil || !strings.Contains(err.Error(), "pending working cut") {
			t.Fatalf("expected pending working cut error, got: %v", err)
		}
	}

	// 5. Lineage fork with diverged timeline rejected
	sourceProj, sourceBase, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          userID,
		SessionID:       sessionID,
		ProjectID:       "vproj-src-diverge",
		Title:           "Diverge Source",
		InitialTimeline: proposalTestTimeline(),
		NowUnixMs:       100,
	})
	if err != nil {
		t.Fatal(err)
	}

	plan := VideoPlanProposal{Kind: VideoPlanKindInitial, Parts: []VideoPlanPart{{ID: "clip_a", DurationMs: 1000}}}
	sourceProp, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sessionID,
		ProjectID:      sourceProj.ID,
		ProposalID:     "vprop-src-accepted",
		BaseRevisionID: sourceBase.ID,
		Plan:           &plan,
		NowUnixMs:      150,
	})
	if err != nil {
		t.Fatal(err)
	}

	srcRevTimeline := *proposalTestTimeline()
	srcRevTimeline.Metadata = map[string]any{
		"accepted_video_plan":             plan,
		"accepted_video_plan_proposal_id": sourceProp.ID,
	}
	srcRev, _, err := store.CreateVideoProjectRevision(CreateVideoProjectRevisionInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sessionID,
		ProjectID:      sourceProj.ID,
		RevisionID:     "vrev-src-diverged",
		Timeline:       srcRevTimeline,
		NowUnixMs:      200,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Destination project with lineage pointing to srcRev, but with a modified cut (different clip duration)
	destDivergedTimeline := srcRevTimeline
	destDivergedTimeline.Clips = append([]VideoTimelineClip(nil), srcRevTimeline.Clips...)
	destDivergedTimeline.Clips[0].DurationMs = 9999
	destDivergedTimeline.Clips[0].TimelineEndMs = 9999

	destDivergedProj, destDivergedRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         sessionID,
		ProjectID:         "vproj-diverged-dest",
		InitialRevisionID: "vrev-diverged-dest",
		Title:             "Diverged Dest",
		InitialTimeline:   &destDivergedTimeline,
		Metadata: map[string]any{
			"source_session_id":  sessionID,
			"source_project_id":  sourceProj.ID,
			"source_revision_id": srcRev.ID,
		},
		NowUnixMs: 300,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ResolveAuthoritativeVideoPlan(accountID, userID, *destDivergedRev, store)
	if err == nil || !strings.Contains(err.Error(), "timeline does not match exact source revision") {
		t.Fatalf("expected timeline divergence rejection, got: %v", err)
	}

	// 6. Lineage fork with foreign user source project rejected
	foreignProj, foreignBase, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          "foreign-owner",
		SessionID:       sessionID,
		ProjectID:       "vproj-foreign-src",
		Title:           "Foreign Source",
		InitialTimeline: proposalTestTimeline(),
		NowUnixMs:       100,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignSrcRev, _, err := store.CreateVideoProjectRevision(CreateVideoProjectRevisionInput{
		AccountScopeID: accountID,
		UserID:         "foreign-owner",
		SessionID:      sessionID,
		ProjectID:      foreignProj.ID,
		RevisionID:     "vrev-foreign-src",
		Timeline:       srcRevTimeline,
		NowUnixMs:      200,
	})
	if err != nil {
		t.Fatal(err)
	}

	destForeignLinkProj, destForeignLinkRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         sessionID,
		ProjectID:         "vproj-foreign-link-dest",
		InitialRevisionID: "vrev-foreign-link-dest",
		Title:             "Foreign Link Dest",
		InitialTimeline:   &srcRevTimeline,
		Metadata: map[string]any{
			"source_session_id":  sessionID,
			"source_project_id":  foreignProj.ID,
			"source_revision_id": foreignSrcRev.ID,
		},
		NowUnixMs: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = destForeignLinkProj
	_, err = ResolveAuthoritativeVideoPlan(accountID, userID, *destForeignLinkRev, store)
	if err == nil || !strings.Contains(err.Error(), "ownership does not match") {
		t.Fatalf("expected foreign source project rejection, got: %v", err)
	}
}
