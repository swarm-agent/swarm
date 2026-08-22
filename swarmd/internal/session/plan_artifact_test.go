package session

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestNormalizePlanDocumentPreservesPortableArtifacts(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-artifacts", "Artifacts", &pebblestore.SessionPlanDocument{
		Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "docs/summary.md", Role: " DELIVERABLE ", Description: " user deliverable ", MediaType: " text/markdown "}},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Deliver", Status: PlanCheckpointStatusPending, Order: 1,
			Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "tmp/evidence.json", Description: "selective evidence"}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("normalize plan artifacts: %v", err)
	}
	if got := doc.Artifacts[0]; got.Path != "docs/summary.md" || got.Role != "deliverable" || got.Description != "user deliverable" || got.MediaType != "text/markdown" {
		t.Fatalf("plan artifact = %#v", got)
	}
	if got := doc.Checkpoints[0].Artifacts[0].Path; got != "tmp/evidence.json" {
		t.Fatalf("checkpoint artifact path = %q", got)
	}
	clone := clonePlanDocument(doc)
	clone.Artifacts[0].Path = "changed.md"
	clone.Checkpoints[0].Artifacts[0].Path = "changed.json"
	if doc.Artifacts[0].Path != "docs/summary.md" || doc.Checkpoints[0].Artifacts[0].Path != "tmp/evidence.json" {
		t.Fatalf("artifact slices were not cloned: %#v", doc)
	}
}

func TestNormalizePlanDocumentRejectsNonPortableArtifacts(t *testing.T) {
	for _, artifactPath := range []string{"/tmp/result.md", "C:/tmp/result.md", `C:\\tmp\\result.md`, "../result.md", "docs/../result.md"} {
		t.Run(strings.ReplaceAll(artifactPath, "/", "_"), func(t *testing.T) {
			_, err := NormalizePlanDocumentForSave("plan-artifacts", "Artifacts", &pebblestore.SessionPlanDocument{
				Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: artifactPath}},
			}, nil)
			if err == nil || (!strings.Contains(err.Error(), "portable workspace-relative") && !strings.Contains(err.Error(), "within the workspace") && !strings.Contains(err.Error(), "clean portable")) {
				t.Fatalf("path %q error = %v", artifactPath, err)
			}
		})
	}
}

func TestValidateExecutablePlanDocumentRejectsEscapingCheckpointArtifact(t *testing.T) {
	err := ValidateExecutablePlanDocument(&pebblestore.SessionPlanDocument{
		Title: "Artifact plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Deliver artifact"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Deliver", Status: PlanCheckpointStatusPending, Order: 1,
			Objective: "Deliver", AcceptanceCriteria: []string{"Delivered"},
			Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "../../secret"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "checkpoints[0].artifacts[0].path") {
		t.Fatalf("executable artifact validation error = %v", err)
	}
}

func TestNormalizePlanDocumentPreservesAndNormalizesManagedArtifacts(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-managed", "Managed Artifacts", &pebblestore.SessionPlanDocument{
		Artifacts: []pebblestore.SessionPlanArtifactReference{{
			SessionID:    " sess-123 ",
			CollectionID: " col-456 ",
			VariantID:    " var-789 ",
			EventSeq:     42,
			Label:        " Brainstorm Spec ",
			Description:  " Interactive concept document ",
			MediaType:    " text/html; charset=utf-8 ",
			Role:         " DELIVERABLE ",
		}},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID:     "cp-1",
			Title:  "Brainstorm",
			Status: PlanCheckpointStatusPending,
			Order:  1,
			Artifacts: []pebblestore.SessionPlanArtifactReference{{
				SessionID:    "sess-123",
				CollectionID: "col-456",
				VariantID:    "var-abc",
				EventSeq:     50,
				Label:        "Design Diagram",
				MediaType:    "image/png",
			}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("normalize plan with managed artifacts: %v", err)
	}
	ref := doc.Artifacts[0]
	if ref.SessionID != "sess-123" || ref.CollectionID != "col-456" || ref.VariantID != "var-789" || ref.EventSeq != 42 || ref.Label != "Brainstorm Spec" || ref.Description != "Interactive concept document" || ref.MediaType != "text/html; charset=utf-8" || ref.Role != "deliverable" || ref.Path != "" {
		t.Fatalf("normalized managed artifact = %#v", ref)
	}
	cpRef := doc.Checkpoints[0].Artifacts[0]
	if cpRef.SessionID != "sess-123" || cpRef.CollectionID != "col-456" || cpRef.VariantID != "var-abc" || cpRef.EventSeq != 50 || cpRef.Label != "Design Diagram" || cpRef.MediaType != "image/png" {
		t.Fatalf("checkpoint managed artifact = %#v", cpRef)
	}

	clone := clonePlanDocument(doc)
	clone.Artifacts[0].VariantID = "var-changed"
	clone.Checkpoints[0].Artifacts[0].VariantID = "var-changed-cp"
	if doc.Artifacts[0].VariantID != "var-789" || doc.Checkpoints[0].Artifacts[0].VariantID != "var-abc" {
		t.Fatalf("managed artifact slices were not cloned deeply: %#v", doc)
	}
}

func TestNormalizePlanDocumentRejectsInvalidManagedArtifacts(t *testing.T) {
	cases := []struct {
		name    string
		ref     pebblestore.SessionPlanArtifactReference
		wantErr string
	}{
		{
			name:    "missing session id",
			ref:     pebblestore.SessionPlanArtifactReference{CollectionID: "col-1", VariantID: "var-1", EventSeq: 1},
			wantErr: "session_id is required",
		},
		{
			name:    "invalid session id traversal",
			ref:     pebblestore.SessionPlanArtifactReference{SessionID: "../sess", CollectionID: "col-1", VariantID: "var-1", EventSeq: 1},
			wantErr: "session_id is invalid",
		},
		{
			name:    "missing collection id",
			ref:     pebblestore.SessionPlanArtifactReference{SessionID: "sess-1", VariantID: "var-1", EventSeq: 1},
			wantErr: "collection id is required",
		},
		{
			name:    "invalid collection id characters",
			ref:     pebblestore.SessionPlanArtifactReference{SessionID: "sess-1", CollectionID: "col$bad", VariantID: "var-1", EventSeq: 1},
			wantErr: "collection id contains unsupported characters",
		},
		{
			name:    "missing variant id",
			ref:     pebblestore.SessionPlanArtifactReference{SessionID: "sess-1", CollectionID: "col-1", EventSeq: 1},
			wantErr: "variant id is required",
		},
		{
			name:    "invalid variant id characters",
			ref:     pebblestore.SessionPlanArtifactReference{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var/bad", EventSeq: 1},
			wantErr: "variant id contains unsupported characters",
		},
		{
			name:    "missing event seq",
			ref:     pebblestore.SessionPlanArtifactReference{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var-1", EventSeq: 0},
			wantErr: "event_seq is required",
		},
		{
			name:    "forged path fallback rejected",
			ref:     pebblestore.SessionPlanArtifactReference{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var-1", EventSeq: 1, Path: "docs/forged.md"},
			wantErr: "must not declare a workspace path",
		},
		{
			name:    "oversized label",
			ref:     pebblestore.SessionPlanArtifactReference{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var-1", EventSeq: 1, Label: strings.Repeat("x", 257)},
			wantErr: "label exceeds bounds",
		},
		{
			name:    "oversized description",
			ref:     pebblestore.SessionPlanArtifactReference{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var-1", EventSeq: 1, Description: strings.Repeat("x", 2049)},
			wantErr: "description exceeds bounds",
		},
		{
			name:    "oversized media type",
			ref:     pebblestore.SessionPlanArtifactReference{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var-1", EventSeq: 1, MediaType: strings.Repeat("x", 129)},
			wantErr: "media_type exceeds bounds",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizePlanDocumentForSave("plan-managed", "Artifacts", &pebblestore.SessionPlanDocument{
				Artifacts: []pebblestore.SessionPlanArtifactReference{tc.ref},
			}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestNormalizePlanDocumentRejectsDuplicateArtifactReferences(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		Artifacts: []pebblestore.SessionPlanArtifactReference{
			{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var-1", EventSeq: 1, Label: "First"},
			{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var-1", EventSeq: 2, Label: "Duplicate"},
		},
	}
	_, err := NormalizePlanDocumentForSave("plan-managed", "Artifacts", doc, nil)
	if err == nil || !strings.Contains(err.Error(), "is duplicated") {
		t.Fatalf("error = %v, want duplicated error", err)
	}

	docWorkspace := &pebblestore.SessionPlanDocument{
		Artifacts: []pebblestore.SessionPlanArtifactReference{
			{Path: "docs/deliverable.md", Role: "deliverable"},
			{Path: "docs/deliverable.md", Role: "input"},
		},
	}
	_, err = NormalizePlanDocumentForSave("plan-workspace", "Artifacts", docWorkspace, nil)
	if err == nil || !strings.Contains(err.Error(), "is duplicated") {
		t.Fatalf("error = %v, want duplicated error", err)
	}
}

func TestMergePlanCheckpointArtifactsCoexistenceAndDeduplication(t *testing.T) {
	existing := []pebblestore.SessionPlanArtifactReference{
		{Path: "docs/spec.md", Role: "deliverable", Description: "Spec doc"},
		{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var-1", EventSeq: 10, Label: "Variant 1"},
	}
	added := []pebblestore.SessionPlanArtifactReference{
		{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var-1", EventSeq: 10, Label: "Variant 1 duplicate"},
		{SessionID: "sess-1", CollectionID: "col-1", VariantID: "var-2", EventSeq: 11, Label: "Variant 2"},
		{Path: "docs/spec.md", Role: "deliverable", Description: "Spec doc"},
		{Path: "out/report.html", Role: "deliverable", Description: "Report HTML"},
	}
	merged := mergePlanCheckpointArtifacts(existing, added)
	if len(merged) != 4 {
		t.Fatalf("merged artifacts count = %d, want 4; merged = %#v", len(merged), merged)
	}
	if merged[0].Path != "docs/spec.md" || merged[1].VariantID != "var-1" || merged[2].VariantID != "var-2" || merged[3].Path != "out/report.html" {
		t.Fatalf("merged artifacts order or content mismatch: %#v", merged)
	}
}

func TestSavePlanAuthenticatesManagedArtifacts(t *testing.T) {
	sessions := newArtifactCleanupTestService(t)
	created, _, err := sessions.CreateSessionWithOptions(artifactCleanupCreateOptions("session-auth", "/ws", "Artifact Test"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = sessions.store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: created.ID, UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "mut-create-col", PayloadHash: "hash-1", Kind: pebblestore.V3SessionMutationCreateArtifact, NowUnixMs: 1000,
		Artifact: &pebblestore.V3ArtifactMutation{
			Collection: pebblestore.SessionArtifactCollection{ID: "col-1", Name: "Concepts"},
			Variant: &pebblestore.SessionArtifactVariant{
				ID: "var-ready-1", CollectionID: "col-1", Filename: "concept.html", MediaType: "text/html",
				Presentation: pebblestore.SessionArtifactPresentation{Kind: "html", Label: "Concept Spec", Description: "Interactive Concept", Previewable: true},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sessions.store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: created.ID, UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "mut-finalize-1", PayloadHash: "hash-2", Kind: pebblestore.V3SessionMutationFinalizeArtifact, NowUnixMs: 2000,
		Artifact: &pebblestore.V3ArtifactMutation{
			Collection: pebblestore.SessionArtifactCollection{ID: "col-1"},
			Variant:    &pebblestore.SessionArtifactVariant{ID: "var-ready-1", CollectionID: "col-1", Filename: "concept.html", MediaType: "text/html", DigestSHA256: strings.Repeat("a", 64), Size: 100},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = sessions.store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: created.ID, UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "mut-create-2", PayloadHash: "hash-3", Kind: pebblestore.V3SessionMutationCreateArtifact, NowUnixMs: 3000,
		Artifact: &pebblestore.V3ArtifactMutation{
			Collection: pebblestore.SessionArtifactCollection{ID: "col-1"},
			Variant: &pebblestore.SessionArtifactVariant{
				ID: "var-ready-2", CollectionID: "col-1", Filename: "notes.md", MediaType: "text/markdown",
				Presentation: pebblestore.SessionArtifactPresentation{Kind: "markdown", Label: "Brainstorm Notes", Description: "Summary Notes", Previewable: true},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sessions.store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: created.ID, UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "mut-finalize-2", PayloadHash: "hash-4", Kind: pebblestore.V3SessionMutationFinalizeArtifact, NowUnixMs: 4000,
		Artifact: &pebblestore.V3ArtifactMutation{
			Collection: pebblestore.SessionArtifactCollection{ID: "col-1"},
			Variant:    &pebblestore.SessionArtifactVariant{ID: "var-ready-2", CollectionID: "col-1", Filename: "notes.md", MediaType: "text/markdown", DigestSHA256: strings.Repeat("b", 64), Size: 50},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = sessions.store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: created.ID, UserID: "user-1", AccountScopeID: "account-1",
		ClientRequestID: "mut-create-staging", PayloadHash: "hash-5", Kind: pebblestore.V3SessionMutationCreateArtifact, NowUnixMs: 5000,
		Artifact: &pebblestore.V3ArtifactMutation{
			Collection: pebblestore.SessionArtifactCollection{ID: "col-1"},
			Variant: &pebblestore.SessionArtifactVariant{
				ID: "var-staging", CollectionID: "col-1", Filename: "draft.html", MediaType: "text/html",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	v1, ok, err := sessions.store.GetSessionArtifactVariant("account-1", created.ID, "col-1", "var-ready-1")
	if err != nil || !ok {
		t.Fatalf("var-ready-1 fetch ok=%v err=%v", ok, err)
	}
	v2, ok, err := sessions.store.GetSessionArtifactVariant("account-1", created.ID, "col-1", "var-ready-2")
	if err != nil || !ok {
		t.Fatalf("var-ready-2 fetch ok=%v err=%v", ok, err)
	}
	vStaging, ok, err := sessions.store.GetSessionArtifactVariant("account-1", created.ID, "col-1", "var-staging")
	if err != nil || !ok {
		t.Fatalf("var-staging fetch ok=%v err=%v", ok, err)
	}

	validDoc := &pebblestore.SessionPlanDocument{
		Title: "Artifact Handoff Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Deliver artifacts"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Deliverable", Status: PlanCheckpointStatusCompleted, Order: 1,
			Objective: "Create concepts", AcceptanceCriteria: []string{"Ready"},
			Artifacts: []pebblestore.SessionPlanArtifactReference{
				{SessionID: created.ID, CollectionID: "col-1", VariantID: "var-ready-1", EventSeq: v1.EventSeq, Role: "deliverable"},
				{SessionID: created.ID, CollectionID: "col-1", VariantID: "var-ready-2", EventSeq: v2.EventSeq, Role: "deliverable"},
			},
			Handoff: &pebblestore.SessionPlanCheckpointHandoff{Overview: "Handoff ready with two artifacts"},
		}},
	}
	saved, _, err := sessions.SavePlanWithMetadata(created.ID, "plan-1", "Artifact Handoff Plan", "# Plan", "approved", "approved", true, PlanSaveMetadata{Document: validDoc})
	if err != nil {
		t.Fatalf("save plan with valid ready artifacts: %v", err)
	}
	if len(saved.Document.Checkpoints[0].Artifacts) != 2 {
		t.Fatalf("saved checkpoint artifacts = %#v", saved.Document.Checkpoints[0].Artifacts)
	}
	projected := ProjectPlanFinalHandoffArtifacts(saved.ID, "cp-1", saved.Document.Checkpoints[0].Artifacts)
	if len(projected) != 2 {
		t.Fatalf("projected final handoff artifacts count = %d, want 2", len(projected))
	}
	if projected[0].ID != "var-ready-1" || projected[0].Kind != "html" || !projected[0].Previewable {
		t.Fatalf("projected[0] = %#v", projected[0])
	}
	if projected[1].ID != "var-ready-2" || projected[1].Kind != "markdown" || !projected[1].Previewable {
		t.Fatalf("projected[1] = %#v", projected[1])
	}

	missingDoc := &pebblestore.SessionPlanDocument{
		Title: "Missing Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Fail"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Deliverable", Status: PlanCheckpointStatusCompleted, Order: 1,
			Objective: "Fail", AcceptanceCriteria: []string{"Fail"},
			Artifacts: []pebblestore.SessionPlanArtifactReference{
				{SessionID: created.ID, CollectionID: "col-1", VariantID: "var-nonexistent", EventSeq: 1, Role: "deliverable"},
			},
		}},
	}
	if _, _, err := sessions.SavePlanWithMetadata(created.ID, "plan-missing", "Missing", "# Plan", "approved", "approved", true, PlanSaveMetadata{Document: missingDoc}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}

	stagingDoc := &pebblestore.SessionPlanDocument{
		Title: "Staging Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Fail"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Deliverable", Status: PlanCheckpointStatusCompleted, Order: 1,
			Objective: "Fail", AcceptanceCriteria: []string{"Fail"},
			Artifacts: []pebblestore.SessionPlanArtifactReference{
				{SessionID: created.ID, CollectionID: "col-1", VariantID: "var-staging", EventSeq: vStaging.EventSeq, Role: "deliverable"},
			},
		}},
	}
	if _, _, err := sessions.SavePlanWithMetadata(created.ID, "plan-staging", "Staging", "# Plan", "approved", "approved", true, PlanSaveMetadata{Document: stagingDoc}); err == nil || !strings.Contains(err.Error(), "is not ready") {
		t.Fatalf("expected not ready error, got %v", err)
	}

	mismatchSeqDoc := &pebblestore.SessionPlanDocument{
		Title: "Mismatch Seq Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Fail"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Deliverable", Status: PlanCheckpointStatusCompleted, Order: 1,
			Objective: "Fail", AcceptanceCriteria: []string{"Fail"},
			Artifacts: []pebblestore.SessionPlanArtifactReference{
				{SessionID: created.ID, CollectionID: "col-1", VariantID: "var-ready-1", EventSeq: v1.EventSeq + 999, Role: "deliverable"},
			},
		}},
	}
	if _, _, err := sessions.SavePlanWithMetadata(created.ID, "plan-mismatch-seq", "Mismatch", "# Plan", "approved", "approved", true, PlanSaveMetadata{Document: mismatchSeqDoc}); err == nil || !strings.Contains(err.Error(), "event sequence") {
		t.Fatalf("expected event sequence mismatch error, got %v", err)
	}

	foreignDoc := &pebblestore.SessionPlanDocument{
		Title: "Foreign Session Plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Fail"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Deliverable", Status: PlanCheckpointStatusCompleted, Order: 1,
			Objective: "Fail", AcceptanceCriteria: []string{"Fail"},
			Artifacts: []pebblestore.SessionPlanArtifactReference{
				{SessionID: "other-foreign-session", CollectionID: "col-1", VariantID: "var-ready-1", EventSeq: v1.EventSeq, Role: "deliverable"},
			},
		}},
	}
	if _, _, err := sessions.SavePlanWithMetadata(created.ID, "plan-foreign", "Foreign", "# Plan", "approved", "approved", true, PlanSaveMetadata{Document: foreignDoc}); err == nil || (!strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "does not belong")) {
		t.Fatalf("expected foreign session error, got %v", err)
	}
}
