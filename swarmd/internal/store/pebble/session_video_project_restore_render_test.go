package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Verify that forking a video project with an accepted visual plan atomically persists
// destination-scoped authority in Pebble, survives store restart/reopen, and resolves
// authoritative render plans with candidate selection.
// Invariant: Forked projects must persist destination-scoped proposal authority atomically
// with project and revision creation; closing and reopening the database must preserve
// complete authority without dangling proposal references.
// Authority: SessionStore.CreateVideoProject, ResolveAuthoritativeVideoPlan.
func TestCreateVideoProjectAtomicallyPersistsInitialProposalAndSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pebble.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	store := NewSessionStore(db)

	const accountID, userID = "acc-test", "user-test"
	const sourceSessionID, destSessionID = "sess-source", "sess-dest"

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
	plan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:              "clip_a",
			Title:           "Hook",
			DurationMs:      1000,
			Visual:          fallback,
			VisualMediaType: "image/png",
			AnimationCandidates: &VideoAnimationCandidateSet{
				Status: VideoAnimationCandidateStatusAwaitingExport,
				Candidates: []VideoAnimationCandidate{
					{ID: "cand-html", Source: htmlRef},
				},
				SelectedCandidateID: "cand-html",
				SelectedSource:      htmlRef,
			},
		}},
	}

	// 1. Create proposal and use real acceptance mutation to create accepted revision
	sourceProposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sourceSessionID,
		ProjectID:      sourceProj.ID,
		ProposalID:     "vprop-source-accepted",
		BaseRevisionID: sourceBaseRev.ID,
		Plan:           &plan,
		NowUnixMs:      150,
	})
	if err != nil {
		t.Fatalf("create source proposal failed: %v", err)
	}

	acceptedProposal, acceptedRev, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sourceSessionID,
		ProjectID:      sourceProj.ID,
		ProposalID:     sourceProposal.ID,
		NowUnixMs:      200,
	})
	if err != nil {
		t.Fatalf("accept source proposal failed: %v", err)
	}
	if acceptedProposal.Status != VideoEditProposalStatusAccepted || acceptedRev == nil {
		t.Fatalf("expected accepted proposal and revision: status=%s rev=%v", acceptedProposal.Status, acceptedRev)
	}

	// 2. Fork into destination session with InitialProposal
	destProposalInput := acceptedProposal
	destProj, destRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         destSessionID,
		ProjectID:         "vproj-dest",
		InitialRevisionID: "vrev-dest-initial",
		Title:             "Restored Cut",
		InitialTimeline:   &acceptedRev.Timeline,
		InitialProposal:   &destProposalInput,
		Metadata: map[string]any{
			"source_session_id":  sourceSessionID,
			"source_project_id":  sourceProj.ID,
			"source_revision_id": acceptedRev.ID,
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
	if err := db.Close(); err != nil {
		t.Fatalf("store close failed: %v", err)
	}

	reopenedDB, err := Open(dbPath)
	if err != nil {
		t.Fatalf("store reopen failed: %v", err)
	}
	defer func() { _ = reopenedDB.Close() }()
	reopenedStore := NewSessionStore(reopenedDB)

	// 4. Verify in reloaded store that destination proposal was durably persisted
	reloadedProp, ok, err := reopenedStore.GetVideoEditProposal(accountID, destSessionID, destProj.ID, acceptedProposal.ID)
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
	plan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:              "clip_a",
			Title:           "Orbit",
			DurationMs:      1000,
			Visual:          &SessionArtifactSelectionReference{SessionID: sourceSessionID, CollectionID: "col-still", VariantID: "var-still", EventSeq: 1},
			VisualMediaType: "image/png",
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

	sourceProposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sourceSessionID,
		ProjectID:      sourceProj.ID,
		ProposalID:     "vprop-lineage-accepted",
		BaseRevisionID: sourceBaseRev.ID,
		Plan:           &plan,
		NowUnixMs:      150,
	})
	if err != nil {
		t.Fatal(err)
	}

	acceptedProposal, acceptedRev, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sourceSessionID,
		ProjectID:      sourceProj.ID,
		ProposalID:     sourceProposal.ID,
		NowUnixMs:      200,
	})
	if err != nil || acceptedRev == nil {
		t.Fatalf("resolve source proposal failed: %v", err)
	}

	// Create dangling fork: NO InitialProposal passed (simulates prior behavior)
	destProj, destRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         destSessionID,
		ProjectID:         "vproj-lineage-dest",
		InitialRevisionID: "vrev-lineage-dest-initial",
		Title:             "Dangling Fork",
		InitialTimeline:   &acceptedRev.Timeline,
		Metadata: map[string]any{
			"source_session_id":  sourceSessionID,
			"source_project_id":  sourceProj.ID,
			"source_revision_id": acceptedRev.ID,
		},
		NowUnixMs: 300,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify proposal was NOT stored in destination (confirming this is a dangling fork)
	_, ok, _ := store.GetVideoEditProposal(accountID, destSessionID, destProj.ID, acceptedProposal.ID)
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

// Purpose: Verify immutable selection wins: a proposal updated with candidate selections AFTER
// an older revision cut was created must NOT donate its later candidate choice to that revision.
// Invariant: Render authority resolution must strictly compare proposal UpdatedAt with revision CreatedAt.
// Authority: ResolveVideoPlanRenderAuthority, ResolveAuthoritativeVideoPlan.
func TestResolveAuthoritativeVideoPlanImmutableSelectionWinsOverLaterProposalUpdate(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	const accountID, userID = "acc-immut", "user-immut"
	const sessionID = "sess-immut"
	createTestSession(t, store, accountID, userID, sessionID)

	sourceTimeline := proposalTestTimeline()
	proj, baseRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          userID,
		SessionID:       sessionID,
		ProjectID:       "vproj-immut",
		Title:           "Immutable Selection Project",
		InitialTimeline: sourceTimeline,
		NowUnixMs:       100,
	})
	if err != nil {
		t.Fatal(err)
	}

	htmlRef := &SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: "col", VariantID: "html", EventSeq: 2}
	fallback := &SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: "col", VariantID: "still", EventSeq: 1}
	unlockedPlan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:              "clip_a",
			Title:           "Opening",
			DurationMs:      1000,
			Visual:          fallback,
			VisualMediaType: "image/png",
			AnimationCandidates: &VideoAnimationCandidateSet{
				Status: VideoAnimationCandidateStatusAwaitingSelection,
				Candidates: []VideoAnimationCandidate{
					{ID: "cand-1", Source: htmlRef},
				},
			},
		}},
	}

	// 1. Create proposal with unselected candidates at T=150
	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sessionID,
		ProjectID:      proj.ID,
		ProposalID:     "vprop-immut",
		BaseRevisionID: baseRev.ID,
		Plan:           &unlockedPlan,
		NowUnixMs:      150,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Accept proposal into Revision 2 at T=200
	_, rev2, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sessionID,
		ProjectID:      proj.ID,
		ProposalID:     proposal.ID,
		NowUnixMs:      200,
	})
	if err != nil || rev2 == nil {
		t.Fatalf("accept proposal failed: %v", err)
	}

	// 3. Later, at T=300, a candidate selection mutation updates the proposal
	_, _, err = store.SelectVideoAnimationCandidate(SelectVideoAnimationCandidateInput{
		AccountScopeID:      accountID,
		UserID:              userID,
		SessionID:           sessionID,
		ProjectID:           proj.ID,
		ProposalID:          proposal.ID,
		PartID:              "clip_a",
		SelectedCandidateID: "cand-1",
		SelectedSource:      htmlRef,
		NowUnixMs:           300,
	})
	if err != nil {
		t.Fatalf("select animation candidate failed: %v", err)
	}

	// 4. Resolve authority for Revision 2 (created at T=200):
	// Because proposal UpdatedAt (300) > rev2 CreatedAt (200), the later selection must NOT be applied to rev2!
	resolvedPlan, err := ResolveAuthoritativeVideoPlan(accountID, userID, *rev2, store)
	if err != nil {
		t.Fatalf("ResolveAuthoritativeVideoPlan failed: %v", err)
	}
	if resolvedPlan == nil || len(resolvedPlan.Parts) == 0 {
		t.Fatal("expected non-nil plan")
	}
	candSet := resolvedPlan.Parts[0].AnimationCandidates
	if candSet != nil && candSet.SelectedCandidateID != "" {
		t.Fatalf("later candidate selection must not be donated to older revision: %+v", candSet)
	}
}

// Purpose: Verify CreateVideoProject validates initial edit proposal requirements:
// rejects pending or rejected proposals, mismatches with timeline, or invalid plans.
// Threat/regression: Unapproved or forged proposal states must never be persisted as accepted authority.
// Authority: SessionStore.CreateVideoProject.
func TestCreateVideoProjectRejectsNonAcceptedOrMismatchedInitialProposal(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	const accountID, userID = "acc-rej", "user-rej"
	const sessionID = "sess-rej"
	createTestSession(t, store, accountID, userID, sessionID)

	validPlan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:              "clip_a",
			Title:           "Valid Part",
			DurationMs:      1000,
			Visual:          &SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: "col", VariantID: "v1", EventSeq: 1},
			VisualMediaType: "image/png",
		}},
	}
	timeline := *proposalTestTimeline()
	timeline.Metadata = map[string]any{
		"accepted_video_plan":             validPlan,
		"accepted_video_plan_proposal_id": "vprop-target",
	}

	// 1. Pending initial proposal rejected
	pendingProp := VideoEditProposalSnapshot{
		ID:     "vprop-target",
		Status: VideoEditProposalStatusPending,
		Plan:   &validPlan,
	}
	_, _, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          userID,
		SessionID:       sessionID,
		ProjectID:       "vproj-pending-rej",
		InitialTimeline: &timeline,
		InitialProposal: &pendingProp,
		NowUnixMs:       100,
	})
	if err == nil || !strings.Contains(err.Error(), "must be accepted") {
		t.Fatalf("expected pending proposal rejection, got: %v", err)
	}
	// Verify no partial project created
	if _, ok, _ := store.GetVideoProject(accountID, sessionID, "vproj-pending-rej"); ok {
		t.Fatal("partial video project created after failed initial proposal validation")
	}

	// 2. Rejected initial proposal rejected
	rejectedProp := VideoEditProposalSnapshot{
		ID:     "vprop-target",
		Status: VideoEditProposalStatusRejected,
		Plan:   &validPlan,
	}
	_, _, err = store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          userID,
		SessionID:       sessionID,
		ProjectID:       "vproj-rejected-rej",
		InitialTimeline: &timeline,
		InitialProposal: &rejectedProp,
		NowUnixMs:       100,
	})
	if err == nil || !strings.Contains(err.Error(), "must be accepted") {
		t.Fatalf("expected rejected proposal rejection, got: %v", err)
	}

	// 3. ID mismatch with timeline rejected
	mismatchedProp := VideoEditProposalSnapshot{
		ID:     "vprop-mismatched",
		Status: VideoEditProposalStatusAccepted,
		Plan:   &validPlan,
	}
	_, _, err = store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          userID,
		SessionID:       sessionID,
		ProjectID:       "vproj-mismatch-rej",
		InitialTimeline: &timeline,
		InitialProposal: &mismatchedProp,
		NowUnixMs:       100,
	})
	if err == nil || !strings.Contains(err.Error(), "does not carry matching accepted_video_plan_proposal_id") {
		t.Fatalf("expected proposal id mismatch rejection, got: %v", err)
	}
}

// Purpose: Verify that native Artifact V3 conversion intent and exact references
// survive project forking and are atomically preserved in destination proposal and timeline.
// Invariant: Forking must never silently drop Artifact V3 conversion intent or corrupt references.
// Authority: SessionStore.CreateVideoProject, ValidateVideoPlanForIntent.
func TestCreateVideoProjectPreservesNativeArtifactV3ConversionIntentAndReferences(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	const accountID, userID = "account", "user"
	const sourceSessionID, destSessionID = "studio-src", "studio-dest"
	createTestSession(t, store, accountID, userID, sourceSessionID)
	createTestSession(t, store, accountID, userID, destSessionID)

	source := testNativeV3VideoReference("text/html", "", "d")
	still := testNativeV3VideoReference("image/png", "still", "e")
	visual := testNativeV3VideoReference("video/mp4", "mp4", "f")

	part := VideoPlanPart{
		ID:                  "motion",
		Title:               "Motion",
		DurationMs:          2000,
		CaptureStateID:      "capture",
		FilmingRequirements: []string{"Capture motion"},
		ProductionState:     VideoProductionStateReady,
		ArtifactV3Source:    source,
		ArtifactV3Still:     still,
		ArtifactV3Visual:    visual,
		VisualMediaType:     "video/mp4",
		SourceEndMs:         2000,
		AnimationCandidates: &VideoAnimationCandidateSet{
			Status:              VideoAnimationCandidateStatusReady,
			SelectedCandidateID: "native",
			V3SelectedSource:    source,
			V3Derivative:        visual,
			Candidates:          []VideoAnimationCandidate{{ID: "native", V3Source: source}},
		},
	}
	v3Plan := VideoPlanProposal{
		Kind:  VideoPlanKindInitial,
		Parts: []VideoPlanPart{part},
	}

	proj, baseRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          userID,
		SessionID:       sourceSessionID,
		ProjectID:       "vproj-v3-src",
		Title:           "Native V3 Project",
		InitialTimeline: &VideoProjectTimeline{OutputPreset: VideoPresetLandscape1080p},
		NowUnixMs:       100,
	})
	if err != nil {
		t.Fatal(err)
	}

	proposal, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sourceSessionID,
		ProjectID:      proj.ID,
		ProposalID:     "vprop-convert-v3",
		BaseRevisionID: baseRev.ID,
		Intent:         VideoEditProposalIntentArtifactV3Convert,
		Plan:           &v3Plan,
		NowUnixMs:      200,
	})
	if err != nil {
		t.Fatal(err)
	}

	acceptedProp, acceptedRev, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sourceSessionID,
		ProjectID:      proj.ID,
		ProposalID:     proposal.ID,
		NowUnixMs:      300,
	})
	if err != nil || acceptedRev == nil {
		t.Fatalf("accept native V3 proposal failed: %v", err)
	}

	// Fork into destination session
	destProposalInput := acceptedProp
	destProj, destRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         destSessionID,
		ProjectID:         "vproj-v3-dest",
		InitialRevisionID: "vrev-v3-dest",
		Title:             "Forked Native V3",
		InitialTimeline:   &acceptedRev.Timeline,
		InitialProposal:   &destProposalInput,
		Metadata: map[string]any{
			"source_session_id":  sourceSessionID,
			"source_project_id":  proj.ID,
			"source_revision_id": acceptedRev.ID,
		},
		NowUnixMs: 400,
	})
	if err != nil {
		t.Fatalf("fork native V3 project failed: %v", err)
	}

	storedProp, ok, err := store.GetVideoEditProposal(accountID, destSessionID, destProj.ID, acceptedProp.ID)
	if err != nil || !ok {
		t.Fatalf("destination V3 proposal not found: ok=%v err=%v", ok, err)
	}
	if storedProp.Intent != VideoEditProposalIntentArtifactV3Convert {
		t.Fatalf("destination proposal intent lost: %s", storedProp.Intent)
	}
	if storedProp.Plan == nil || len(storedProp.Plan.Parts) == 0 || storedProp.Plan.Parts[0].ArtifactV3Source == nil {
		t.Fatalf("destination proposal native references not preserved: %+v", storedProp.Plan)
	}

	resolvedPlan, err := ResolveAuthoritativeVideoPlan(accountID, userID, *destRev, store)
	if err != nil {
		t.Fatalf("resolve authoritative plan failed on native V3 fork: %v", err)
	}
	if resolvedPlan.Parts[0].ArtifactV3Visual == nil || *resolvedPlan.Parts[0].ArtifactV3Visual != *visual {
		t.Fatalf("resolved plan native visual mismatch: %+v", resolvedPlan.Parts[0].ArtifactV3Visual)
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

	// 4. Pending working cut rejected: assert returned actual WorkingRevisionID exists and rejection occurs
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

	pendingProp, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
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
	if pendingProp.WorkingRevisionID == "" {
		t.Fatal("expected pending proposal to have a working revision id")
	}
	workingRev, ok, err := store.GetVideoProjectRevision(accountID, sessionID, proj.ID, pendingProp.WorkingRevisionID)
	if err != nil || !ok {
		t.Fatalf("working revision %q not found: %v", pendingProp.WorkingRevisionID, err)
	}
	_, err = ResolveAuthoritativeVideoPlan(accountID, userID, workingRev, store)
	if err == nil || !strings.Contains(err.Error(), "pending working cut") {
		t.Fatalf("expected pending working cut error, got: %v", err)
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

	plan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:              "clip_a",
			Title:           "Source Part",
			DurationMs:      1000,
			Visual:          &SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: "col", VariantID: "var1", EventSeq: 1},
			VisualMediaType: "image/png",
		}},
	}
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

	_, acceptedDivergeRev, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         userID,
		SessionID:      sessionID,
		ProjectID:      sourceProj.ID,
		ProposalID:     sourceProp.ID,
		NowUnixMs:      200,
	})
	if err != nil || acceptedDivergeRev == nil {
		t.Fatalf("resolve source proposal failed: %v", err)
	}

	// Destination project with lineage pointing to acceptedDivergeRev, but with a modified cut
	destDivergedTimeline := acceptedDivergeRev.Timeline
	destDivergedTimeline.Clips = append([]VideoTimelineClip(nil), acceptedDivergeRev.Timeline.Clips...)
	destDivergedTimeline.Clips[0].DurationMs = 9999
	destDivergedTimeline.Clips[0].TimelineEndMs = 9999

	_, destDivergedRev, err := store.CreateVideoProject(CreateVideoProjectInput{
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
			"source_revision_id": acceptedDivergeRev.ID,
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
	// Foreign project created in a separate foreign-owned session to honor session ownership
	const foreignSessionID = "sess-foreign-owner"
	createTestSession(t, store, accountID, "foreign-owner", foreignSessionID)

	foreignProj, foreignBaseRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:  accountID,
		UserID:          "foreign-owner",
		SessionID:       foreignSessionID,
		ProjectID:       "vproj-foreign-src",
		Title:           "Foreign Source",
		InitialTimeline: proposalTestTimeline(),
		NowUnixMs:       100,
	})
	if err != nil {
		t.Fatal(err)
	}

	foreignProp, err := store.CreateVideoEditProposal(CreateVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         "foreign-owner",
		SessionID:      foreignSessionID,
		ProjectID:      foreignProj.ID,
		ProposalID:     "vprop-foreign-accepted",
		BaseRevisionID: foreignBaseRev.ID,
		Plan:           &plan,
		NowUnixMs:      150,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, foreignAcceptedRev, _, err := store.ResolveVideoEditProposal(ResolveVideoEditProposalInput{
		AccountScopeID: accountID,
		UserID:         "foreign-owner",
		SessionID:      foreignSessionID,
		ProjectID:      foreignProj.ID,
		ProposalID:     foreignProp.ID,
		NowUnixMs:      200,
	})
	if err != nil || foreignAcceptedRev == nil {
		t.Fatalf("resolve foreign proposal failed: %v", err)
	}

	_, destForeignLinkRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         sessionID,
		ProjectID:         "vproj-foreign-link-dest",
		InitialRevisionID: "vrev-foreign-link-dest",
		Title:             "Foreign Link Dest",
		InitialTimeline:   &foreignAcceptedRev.Timeline,
		Metadata: map[string]any{
			"source_session_id":  foreignSessionID,
			"source_project_id":  foreignProj.ID,
			"source_revision_id": foreignAcceptedRev.ID,
		},
		NowUnixMs: 300,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ResolveAuthoritativeVideoPlan(accountID, userID, *destForeignLinkRev, store)
	if err == nil || !strings.Contains(err.Error(), "ownership does not match") {
		t.Fatalf("expected foreign source project rejection, got: %v", err)
	}
}
