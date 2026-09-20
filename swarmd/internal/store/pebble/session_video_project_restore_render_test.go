package pebblestore

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Verify that forking a video project with an accepted visual plan atomically persists
// destination-scoped authority in Pebble, survives store restart/reopen, and resolves
// authoritative render plans with candidate selection.
// Invariant: Forked projects must persist destination-scoped proposal authority atomically
// with project, revision, and session attachment metadata/message; closing and reopening
// the database must preserve complete authority without dangling proposal references.
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
	htmlRef2 := &SessionArtifactSelectionReference{SessionID: sourceSessionID, CollectionID: "col-anim", VariantID: "var-html-2", EventSeq: 6}
	fallback := &SessionArtifactSelectionReference{SessionID: sourceSessionID, CollectionID: "col-still", VariantID: "var-still", EventSeq: 4}
	unselectedPlan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:              "clip_a",
			Title:           "Hook",
			DurationMs:      1000,
			Visual:          fallback,
			VisualMediaType: "image/png",
			AnimationCandidates: &VideoAnimationCandidateSet{
				Status: VideoAnimationCandidateStatusAwaitingSelection,
				Candidates: []VideoAnimationCandidate{
					{ID: "cand-html", Source: htmlRef},
					{ID: "cand-html-2", Source: htmlRef2},
				},
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
		Intent:         VideoEditProposalIntentHTMLIteration,
		Plan:           &unselectedPlan,
		NowUnixMs:      150,
	})
	if err != nil {
		t.Fatalf("create source proposal failed: %v", err)
	}

	// Legally select candidate via V3SessionMutationSelectVideoAnimationCandidate
	_, err = store.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:       sourceSessionID,
		UserID:          userID,
		AccountScopeID:  accountID,
		ClientRequestID: "select-cand-html",
		IdempotencyKey:  "select-cand-html",
		PayloadHash:     "select-cand-html-hash",
		Kind:            V3SessionMutationSelectVideoAnimationCandidate,
		NowUnixMs:       180,
		VideoProject: &V3VideoProjectMutation{
			EditProposal: &VideoEditProposalSnapshot{
				ID:             sourceProposal.ID,
				ProjectID:      sourceProj.ID,
				BaseRevisionID: sourceProposal.BaseRevisionID,
			},
			AnimationSelection: &VideoAnimationSelectionMutation{
				PartID:              "clip_a",
				SelectedCandidateID: "cand-html",
				SelectedSource:      htmlRef,
			},
		},
	})
	if err != nil {
		t.Fatalf("select candidate failed: %v", err)
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

	// 2. Fork into destination session with InitialProposal, session metadata, and attachment message
	destProposalInput := acceptedProposal
	attachmentMsg := &MessageSnapshot{
		ID:        "msg-attach-1",
		SessionID: destSessionID,
		Role:      "system",
		Content:   "Attached video project vproj-dest",
	}
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
		SessionMetadata: map[string]any{
			"active_video_project_id": "vproj-dest",
		},
		AttachmentMessage: attachmentMsg,
		NowUnixMs:         300,
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

	// 4. Verify in reloaded store that destination proposal, revision, session metadata, and message were durably persisted
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

	reloadedSession, ok, err := reopenedStore.GetSession(destSessionID)
	if err != nil || !ok {
		t.Fatalf("destination session not found after reload: %v", err)
	}
	if reloadedSession.Metadata["active_video_project_id"] != "vproj-dest" {
		t.Fatalf("session metadata not atomically persisted: %+v", reloadedSession.Metadata)
	}
	if reloadedSession.MessageCount != 1 {
		t.Fatalf("session message count = %d, want 1", reloadedSession.MessageCount)
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

// Purpose: Verify CreateVideoProject idempotency: retrying identical input with different NowUnixMs
// replays the same persisted project, revision, and attachment without recreating or duplicating state;
// altered input with the same client request ID is rejected with conflict.
// Invariant: Idempotent create requests must hash caller inputs rather than generated timestamps.
// Authority: SessionStore.CreateVideoProject, ApplyV3SessionMutation.
func TestCreateVideoProjectIdempotencyAndAlteredRejection(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	const accountID, userID = "acc-idem", "user-idem"
	const sessionID = "sess-idem"
	createTestSession(t, store, accountID, userID, sessionID)

	validPlan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:              "clip_a",
			Title:           "Idem Part",
			DurationMs:      1000,
			Visual:          &SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: "col", VariantID: "v1", EventSeq: 1},
			VisualMediaType: "image/png",
		}},
	}
	timeline := *proposalTestTimeline()
	timeline.Metadata = map[string]any{
		"accepted_video_plan":             validPlan,
		"accepted_video_plan_proposal_id": "vprop-idem",
	}
	acceptedProp := VideoEditProposalSnapshot{
		ID:     "vprop-idem",
		Status: VideoEditProposalStatusAccepted,
		Plan:   &validPlan,
	}

	// 1. Initial creation with explicit ClientRequestID
	const clientReqID = "client-req-create-123"
	proj1, rev1, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         sessionID,
		ProjectID:         "vproj-idem-1",
		InitialRevisionID: "vrev-idem-1",
		Title:             "Idempotent Project",
		InitialTimeline:   &timeline,
		InitialProposal:   &acceptedProp,
		ClientRequestID:   clientReqID,
		NowUnixMs:         1000,
	})
	if err != nil {
		t.Fatalf("initial CreateVideoProject failed: %v", err)
	}

	// 2. Identical retry with different NowUnixMs
	proj2, rev2, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         sessionID,
		ProjectID:         "vproj-idem-1",
		InitialRevisionID: "vrev-idem-1",
		Title:             "Idempotent Project",
		InitialTimeline:   &timeline,
		InitialProposal:   &acceptedProp,
		ClientRequestID:   clientReqID,
		NowUnixMs:         2000, // different timestamp
	})
	if err != nil {
		t.Fatalf("identical retry failed: %v", err)
	}
	if proj1.ID != proj2.ID || rev1.ID != rev2.ID {
		t.Fatalf("idempotent replay returned different identities: proj1=%s proj2=%s", proj1.ID, proj2.ID)
	}

	// 3. Altered plan with same ClientRequestID must be rejected as conflict
	alteredPlan := validPlan
	alteredPlan.Parts = append([]VideoPlanPart(nil), validPlan.Parts...)
	alteredPlan.Parts[0].DurationMs = 5000
	alteredTimeline := timeline
	alteredTimeline.Metadata = map[string]any{
		"accepted_video_plan":             alteredPlan,
		"accepted_video_plan_proposal_id": "vprop-idem",
	}
	alteredProp := acceptedProp
	alteredProp.Plan = &alteredPlan

	_, _, err = store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         sessionID,
		ProjectID:         "vproj-idem-1",
		InitialRevisionID: "vrev-idem-1",
		Title:             "Idempotent Project",
		InitialTimeline:   &alteredTimeline,
		InitialProposal:   &alteredProp,
		ClientRequestID:   clientReqID,
		NowUnixMs:         3000,
	})
	if err == nil || (!errors.Is(err, ErrV3IdempotencyConflict) && !strings.Contains(err.Error(), "conflict")) {
		t.Fatalf("expected idempotency conflict on altered payload, got: %v", err)
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
	htmlRef2 := &SessionArtifactSelectionReference{SessionID: sourceSessionID, CollectionID: "col-motion", VariantID: "var-orbit-2", EventSeq: 10}
	unselectedPlan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:              "clip_a",
			Title:           "Orbit",
			DurationMs:      1000,
			Visual:          &SessionArtifactSelectionReference{SessionID: sourceSessionID, CollectionID: "col-still", VariantID: "var-still", EventSeq: 1},
			VisualMediaType: "image/png",
			AnimationCandidates: &VideoAnimationCandidateSet{
				Status: VideoAnimationCandidateStatusAwaitingSelection,
				Candidates: []VideoAnimationCandidate{
					{ID: "orbit", Source: htmlRef},
					{ID: "pulse", Source: htmlRef2},
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
		Intent:         VideoEditProposalIntentHTMLIteration,
		Plan:           &unselectedPlan,
		NowUnixMs:      150,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:       sourceSessionID,
		UserID:          userID,
		AccountScopeID:  accountID,
		ClientRequestID: "select-orbit",
		IdempotencyKey:  "select-orbit",
		PayloadHash:     "select-orbit-hash",
		Kind:            V3SessionMutationSelectVideoAnimationCandidate,
		NowUnixMs:       180,
		VideoProject: &V3VideoProjectMutation{
			EditProposal: &VideoEditProposalSnapshot{
				ID:             sourceProposal.ID,
				ProjectID:      sourceProj.ID,
				BaseRevisionID: sourceProposal.BaseRevisionID,
			},
			AnimationSelection: &VideoAnimationSelectionMutation{
				PartID:              "clip_a",
				SelectedCandidateID: "orbit",
				SelectedSource:      htmlRef,
			},
		},
	})
	if err != nil {
		t.Fatalf("select orbit candidate failed: %v", err)
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
// an older revision cut was created must NOT donate its later candidate choice across local resolution,
// new-session fork persistence, or legacy lineage resolution.
// Invariant: Render authority resolution must strictly compare proposal UpdatedAt with revision CreatedAt.
// Authority: ResolveVideoPlanRenderAuthority, ResolveAuthoritativeVideoPlan, ForkRevision.
func TestResolveAuthoritativeVideoPlanImmutableSelectionWinsOverLaterProposalUpdate(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	const accountID, userID = "acc-immut", "user-immut"
	const sessionID = "sess-immut"
	const destForkSessionID = "sess-immut-fork"
	const destDangleSessionID = "sess-immut-dangle"
	createTestSession(t, store, accountID, userID, sessionID)
	createTestSession(t, store, accountID, userID, destForkSessionID)
	createTestSession(t, store, accountID, userID, destDangleSessionID)

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
	htmlRef2 := &SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: "col", VariantID: "html-2", EventSeq: 3}
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
					{ID: "cand-2", Source: htmlRef2},
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
		Intent:         VideoEditProposalIntentHTMLIteration,
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
	_, err = store.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:       sessionID,
		UserID:          userID,
		AccountScopeID:  accountID,
		ClientRequestID: "select-cand-1",
		IdempotencyKey:  "select-cand-1",
		PayloadHash:     "select-cand-1-hash",
		Kind:            V3SessionMutationSelectVideoAnimationCandidate,
		NowUnixMs:       300,
		VideoProject: &V3VideoProjectMutation{
			EditProposal: &VideoEditProposalSnapshot{
				ID:             proposal.ID,
				ProjectID:      proj.ID,
				BaseRevisionID: proposal.BaseRevisionID,
			},
			AnimationSelection: &VideoAnimationSelectionMutation{
				PartID:              "clip_a",
				SelectedCandidateID: "cand-1",
				SelectedSource:      htmlRef,
			},
		},
	})
	if err != nil {
		t.Fatalf("apply candidate selection mutation failed: %v", err)
	}

	// 4a. Local resolver: resolve authority for Revision 2 (created at T=200):
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
		t.Fatalf("local resolver: later candidate selection must not be donated to older revision: %+v", candSet)
	}

	// 4b. Fork persistence: forking rev2 must resolve exact historical authority and NOT donate the later candidate
	res, err := ResolveAuthoritativeVideoPlanDetails(accountID, userID, *rev2, store)
	if err != nil {
		t.Fatalf("ResolveAuthoritativeVideoPlanDetails failed: %v", err)
	}
	forkDestProp := res.SourceProposal
	forkDestProp.Plan = res.Plan
	forkDestProp.Status = VideoEditProposalStatusAccepted
	forkDestTL, err := CloneVideoTimeline(rev2.Timeline)
	if err != nil {
		t.Fatal(err)
	}
	forkDestTL.Metadata["accepted_video_plan"] = *res.Plan
	forkDestTL.Metadata["accepted_video_plan_proposal_id"] = proposal.ID

	_, forkDestRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         destForkSessionID,
		ProjectID:         "vproj-immut-fork",
		InitialRevisionID: "vrev-immut-fork",
		Title:             "Fork Cut",
		InitialTimeline:   &forkDestTL,
		InitialProposal:   &forkDestProp,
		Metadata: map[string]any{
			"source_session_id":  sessionID,
			"source_project_id":  proj.ID,
			"source_revision_id": rev2.ID,
		},
		NowUnixMs: 400,
	})
	if err != nil {
		t.Fatalf("create fork project failed: %v", err)
	}
	resolvedForkPlan, err := ResolveAuthoritativeVideoPlan(accountID, userID, *forkDestRev, store)
	if err != nil {
		t.Fatalf("resolve fork plan failed: %v", err)
	}
	if resolvedForkPlan.Parts[0].AnimationCandidates != nil && resolvedForkPlan.Parts[0].AnimationCandidates.SelectedCandidateID != "" {
		t.Fatalf("fork persistence: later candidate selection must not be donated to older cut: %+v", resolvedForkPlan.Parts[0].AnimationCandidates)
	}

	// 4c. Legacy lineage resolution: dangling fork resolving via lineage must NOT donate the later candidate
	_, dangleRev, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         destDangleSessionID,
		ProjectID:         "vproj-immut-dangle",
		InitialRevisionID: "vrev-immut-dangle",
		Title:             "Dangling Cut",
		InitialTimeline:   &rev2.Timeline,
		Metadata: map[string]any{
			"source_session_id":  sessionID,
			"source_project_id":  proj.ID,
			"source_revision_id": rev2.ID,
		},
		NowUnixMs: 500,
	})
	if err != nil {
		t.Fatalf("create dangling project failed: %v", err)
	}
	resolvedDanglePlan, err := ResolveAuthoritativeVideoPlan(accountID, userID, *dangleRev, store)
	if err != nil {
		t.Fatalf("resolve dangling plan failed: %v", err)
	}
	if resolvedDanglePlan.Parts[0].AnimationCandidates != nil && resolvedDanglePlan.Parts[0].AnimationCandidates.SelectedCandidateID != "" {
		t.Fatalf("legacy lineage: later candidate selection must not be donated to older cut: %+v", resolvedDanglePlan.Parts[0].AnimationCandidates)
	}
}

// Purpose: Verify CreateVideoProject validates initial edit proposal requirements:
// rejects pending or rejected proposals, mismatches with timeline, or invalid plans,
// and guarantees no partial destination project, revision, or proposal state is created.
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

	// 1. Pending initial proposal rejected without partial state
	pendingProp := VideoEditProposalSnapshot{
		ID:     "vprop-target",
		Status: VideoEditProposalStatusPending,
		Plan:   &validPlan,
	}
	_, _, err := store.CreateVideoProject(CreateVideoProjectInput{
		AccountScopeID:    accountID,
		UserID:            userID,
		SessionID:         sessionID,
		ProjectID:         "vproj-pending-rej",
		InitialRevisionID: "vrev-pending-rej",
		Title:             "Pending Rejection",
		InitialTimeline:   &timeline,
		InitialProposal:   &pendingProp,
		SessionMetadata:   map[string]any{"should_not_exist": true},
		AttachmentMessage: &MessageSnapshot{ID: "msg-pending-rej", SessionID: sessionID, Role: "system", Content: "nope"},
		NowUnixMs:         100,
	})
	if err == nil || !strings.Contains(err.Error(), "must be accepted") {
		t.Fatalf("expected pending proposal rejection, got: %v", err)
	}
	if _, ok, _ := store.GetVideoProject(accountID, sessionID, "vproj-pending-rej"); ok {
		t.Fatal("partial video project created after failed pending proposal validation")
	}
	if _, ok, _ := store.GetVideoProjectRevision(accountID, sessionID, "vproj-pending-rej", "vrev-pending-rej"); ok {
		t.Fatal("partial video revision created after failed pending proposal validation")
	}
	if _, ok, _ := store.GetVideoEditProposal(accountID, sessionID, "vproj-pending-rej", "vprop-target"); ok {
		t.Fatal("partial video edit proposal created after failed pending proposal validation")
	}
	sess, _, _ := store.GetSession(sessionID)
	if sess.Metadata["should_not_exist"] != nil || sess.MessageCount != 0 {
		t.Fatal("session state mutated after failed project creation")
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
	if _, ok, _ := store.GetVideoProject(accountID, sessionID, "vproj-rejected-rej"); ok {
		t.Fatal("partial video project created after rejected proposal validation failure")
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
	if _, ok, _ := store.GetVideoProject(accountID, sessionID, "vproj-mismatch-rej"); ok {
		t.Fatal("partial video project created after mismatched proposal id failure")
	}
}

// Purpose: Verify that raw V3 create-project mutations enforce proposal-revision-timeline consistency:
// missing initial revision, mismatched WorkingRevisionID/AcceptedRevisionID, and timeline/proposal plan divergence
// are rejected at the mutation boundary before any Pebble batch write.
// Threat/regression: Malformed mutations bypassing high-level service checks could write inconsistent authority state.
// Authority: validateV3VideoProjectMutationInput, ApplyV3SessionMutation.
func TestV3SessionMutationCreateVideoProjectRawBoundaryValidations(t *testing.T) {
	store, cleanup := newTestSessionStoreForVideoProject(t)
	defer cleanup()

	const accountID, userID = "acc-raw", "user-raw"
	const sessionID = "sess-raw"
	createTestSession(t, store, accountID, userID, sessionID)

	validPlan := VideoPlanProposal{
		Kind: VideoPlanKindInitial,
		Parts: []VideoPlanPart{{
			ID:              "clip_a",
			Title:           "Raw Part",
			DurationMs:      1000,
			Visual:          &SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: "col", VariantID: "v1", EventSeq: 1},
			VisualMediaType: "image/png",
		}},
	}
	timeline := *proposalTestTimeline()
	timeline.Metadata = map[string]any{
		"accepted_video_plan":             validPlan,
		"accepted_video_plan_proposal_id": "vprop-raw",
	}

	project := VideoProjectSnapshot{
		ID:             "vproj-raw",
		SessionID:      sessionID,
		AccountScopeID: accountID,
		UserID:         userID,
		Title:          "Raw Project",
	}
	revision := VideoProjectRevisionSnapshot{
		ID:             "vrev-raw-1",
		ProjectID:      project.ID,
		SessionID:      sessionID,
		AccountScopeID: accountID,
		UserID:         userID,
		RevisionNumber: 1,
		Timeline:       timeline,
	}

	// 1. EditProposal with missing Revision rejected
	_, err := store.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:       sessionID,
		UserID:          userID,
		AccountScopeID:  accountID,
		ClientRequestID: "raw-no-rev",
		IdempotencyKey:  "raw-no-rev",
		PayloadHash:     "raw-no-rev-hash",
		Kind:            V3SessionMutationCreateVideoProject,
		NowUnixMs:       100,
		VideoProject: &V3VideoProjectMutation{
			Project: &project,
			EditProposal: &VideoEditProposalSnapshot{
				ID:        "vprop-raw",
				ProjectID: project.ID,
				Status:    VideoEditProposalStatusAccepted,
				Plan:      &validPlan,
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires initial revision snapshot") {
		t.Fatalf("expected missing revision rejection, got: %v", err)
	}

	// 2. EditProposal working_revision_id mismatch rejected
	_, err = store.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:       sessionID,
		UserID:          userID,
		AccountScopeID:  accountID,
		ClientRequestID: "raw-working-mismatch",
		IdempotencyKey:  "raw-working-mismatch",
		PayloadHash:     "raw-working-mismatch-hash",
		Kind:            V3SessionMutationCreateVideoProject,
		NowUnixMs:       100,
		VideoProject: &V3VideoProjectMutation{
			Project:  &project,
			Revision: &revision,
			EditProposal: &VideoEditProposalSnapshot{
				ID:                "vprop-raw",
				ProjectID:         project.ID,
				Status:            VideoEditProposalStatusAccepted,
				WorkingRevisionID: "vrev-different",
				Plan:              &validPlan,
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "working_revision_id does not match initial revision") {
		t.Fatalf("expected working_revision_id mismatch rejection, got: %v", err)
	}

	// 3. Plan divergence between timeline accepted_video_plan and EditProposal.Plan rejected
	divergedPlan := validPlan
	divergedPlan.Parts = append([]VideoPlanPart(nil), validPlan.Parts...)
	divergedPlan.Parts[0].DurationMs = 8888

	_, err = store.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID:       sessionID,
		UserID:          userID,
		AccountScopeID:  accountID,
		ClientRequestID: "raw-plan-divergence",
		IdempotencyKey:  "raw-plan-divergence",
		PayloadHash:     "raw-plan-divergence-hash",
		Kind:            V3SessionMutationCreateVideoProject,
		NowUnixMs:       100,
		VideoProject: &V3VideoProjectMutation{
			Project:  &project,
			Revision: &revision,
			EditProposal: &VideoEditProposalSnapshot{
				ID:                 "vprop-raw",
				ProjectID:          project.ID,
				Status:             VideoEditProposalStatusAccepted,
				WorkingRevisionID:  revision.ID,
				AcceptedRevisionID: revision.ID,
				Plan:               &divergedPlan,
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match initial proposal plan") {
		t.Fatalf("expected plan divergence rejection, got: %v", err)
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
// rejected proposals, diverged timeline cuts, cross-user source project forks,
// and store proposal listing failures.
// Threat/regression: Arbitrary metadata tampering or unauthorized cross-principal access
// could allow executing unauthorized render jobs or recovering wrong cut authorities.
// Authority: ResolveAuthoritativeVideoPlan, ResolveAuthoritativeVideoPlanDetails.
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

	// 7. Source proposal listing failure fails closed without writes
	errReader := &failingProposalReader{
		SessionStore: store,
		listErr:      errors.New("simulated storage failure reading proposals"),
	}
	_, err = ResolveAuthoritativeVideoPlan(accountID, userID, rev, errReader)
	if err == nil || !strings.Contains(err.Error(), "simulated storage failure reading proposals") {
		t.Fatalf("expected listing failure propagation, got: %v", err)
	}
}

type failingProposalReader struct {
	*SessionStore
	listErr error
}

func (f *failingProposalReader) ListVideoEditProposals(accountScopeID, sessionID, projectID string, limit int) ([]VideoEditProposalSnapshot, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.SessionStore.ListVideoEditProposals(accountScopeID, sessionID, projectID, limit)
}
