package pebblestore

import (
	"strings"
	"testing"
	"time"
)

// Written purpose / invariant:
// VideoProvenance serialization, validation, normalization, and ClientSafeCopy() preserve exact
// non-secret properties (AspectRatio, Resolution) while redacting secrets and handles.
// ProjectTaskDeliverable and SessionArtifactLineage serialize and preserve per-output Model,
// AspectRatio, Resolution, and DurationSeconds without data loss.
//
// Threat/regression: Media viewer lost track of original generation model/settings for individual
// deliverables because tasks were mutable and per-deliverable outputs did not store execution
// model or settings.
//
// Authority: video_provenance.go, project_store.go, session_artifact_store.go.

func TestVideoProvenance_AspectRatioAndResolutionSerialization(t *testing.T) {
	now := time.Now().UnixMilli()

	prov := &VideoProvenance{
		AccountScopeID:      "acc-123",
		CredentialID:        "cred-456",
		CredentialVersion:   "v1",
		Provider:            "google",
		Model:               "veo-3.1-generate-preview",
		Transport:           VideoTransportGooglePredictLongRunning,
		Operation:           VideoOperationCreate,
		InteractionID:       "handle-secret-789",
		ProviderResource:    "projects/123/operations/op-1",
		OutputDigestSHA256:  strings.Repeat("c", 64),
		CreatedAt:           now,
		ExpiresAt:           now + 3600*1000,
		ObservedDurationMs:  8000,
		ObservedWidth:       1280,
		ObservedHeight:      720,
		ExtensionCount:      0,
		ExtensionCountKnown: true,
		AspectRatio:         "16:9",
		Resolution:          "720p",
		DurationSeconds:     8,
	}

	if err := prov.Validate(); err != nil {
		t.Fatalf("prov.Validate() failed: %v", err)
	}

	prov.HasInteraction = true
	prov.HasProviderResource = true
	prov.Normalize()
	if prov.HasInteraction || prov.HasProviderResource {
		t.Errorf("Normalize must clear HasInteraction and HasProviderResource from stored record")
	}
	if prov.AspectRatio != "16:9" || prov.Resolution != "720p" || prov.DurationSeconds != 8 {
		t.Errorf("Normalize altered valid values: ar=%q, res=%q, dur=%d", prov.AspectRatio, prov.Resolution, prov.DurationSeconds)
	}

	// ClientSafeCopy must redact secrets/handles but preserve AspectRatio and Resolution
	safe := prov.ClientSafeCopy()
	if safe == nil {
		t.Fatalf("ClientSafeCopy returned nil")
	}
	if safe.CredentialID != "" || safe.InteractionID != "" || safe.ProviderResource != "" {
		t.Errorf("ClientSafeCopy leaked credentials or handles: cred=%q, interaction=%q, resource=%q",
			safe.CredentialID, safe.InteractionID, safe.ProviderResource)
	}
	if safe.Model != "veo-3.1-generate-preview" || safe.Provider != "google" {
		t.Errorf("ClientSafeCopy lost model/provider: %s/%s", safe.Provider, safe.Model)
	}
	if safe.AspectRatio != "16:9" || safe.Resolution != "720p" {
		t.Errorf("ClientSafeCopy lost aspect ratio or resolution: ar=%q, res=%q", safe.AspectRatio, safe.Resolution)
	}
	if safe.ObservedDurationMs != 8000 {
		t.Errorf("ClientSafeCopy lost observed duration: %d", safe.ObservedDurationMs)
	}
	if safe.DurationSeconds != 8 {
		t.Errorf("ClientSafeCopy lost duration seconds: %d", safe.DurationSeconds)
	}
	if !safe.HasInteraction {
		t.Errorf("ClientSafeCopy failed to project HasInteraction=true for non-empty InteractionID")
	}
	if !safe.HasProviderResource {
		t.Errorf("ClientSafeCopy failed to project HasProviderResource=true for non-empty ProviderResource")
	}

	// Verify ClientSafeCopy with empty handles produces false
	emptyHandlesProv := prov.Clone()
	emptyHandlesProv.InteractionID = ""
	emptyHandlesProv.ProviderResource = ""
	emptySafe := emptyHandlesProv.ClientSafeCopy()
	if emptySafe.HasInteraction || emptySafe.HasProviderResource {
		t.Errorf("ClientSafeCopy with empty handles must project false: has_interaction=%v, has_provider_resource=%v",
			emptySafe.HasInteraction, emptySafe.HasProviderResource)
	}

	// EqualVideoProvenance check
	clone := prov.Clone()
	if !EqualVideoProvenance(prov, clone) {
		t.Errorf("EqualVideoProvenance should be true for clone")
	}
	clone.AspectRatio = "9:16"
	if EqualVideoProvenance(prov, clone) {
		t.Errorf("EqualVideoProvenance should be false after modifying AspectRatio")
	}
}

func TestProjectTaskDeliverable_PerOutputModelAndSettings(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	sessionStore := NewSessionStore(db)

	task := &ProjectTaskRecord{
		ID:        "task_test_1",
		ProjectID: "proj_1",
		AccountID: "acc_1",
		Title:     "Media Generation Task",
		Status:    "needs_review",
		Agent:     "video",
		Model:     "veo-3.1-generate-preview",
		Deliverables: []ProjectTaskDeliverable{
			{
				ID:              "deliv_image_1",
				Title:           "Variant 1 Image",
				Kind:            "artifact",
				Status:          "ready",
				MediaURL:        "data:image/png;base64,abc",
				Model:           "imagen-3.0",
				AspectRatio:     "1:1",
				Resolution:      "1K",
				DurationSeconds: 0,
			},
			{
				ID:              "deliv_video_2",
				Title:           "Variant 2 Video",
				Kind:            "video",
				Status:          "ready",
				MediaURL:        "data:video/mp4;base64,def",
				Model:           "veo-3.1-lite-generate-preview",
				AspectRatio:     "16:9",
				Resolution:      "720p",
				DurationSeconds: 8,
			},
		},
	}

	if err := task.Validate(); err != nil {
		t.Fatalf("task.Validate() failed: %v", err)
	}

	if err := sessionStore.PutProjectTask("acc_1", task); err != nil {
		t.Fatalf("PutProjectTask failed: %v", err)
	}

	loaded, ok, err := sessionStore.GetProjectTask("acc_1", "proj_1", "task_test_1")
	if err != nil || !ok {
		t.Fatalf("GetProjectTask failed: ok=%v, err=%v", ok, err)
	}

	if len(loaded.Deliverables) != 2 {
		t.Fatalf("expected 2 deliverables, got %d", len(loaded.Deliverables))
	}

	// Verify image deliverable retains its exact model and settings
	d0 := loaded.Deliverables[0]
	if d0.Model != "imagen-3.0" || d0.AspectRatio != "1:1" || d0.Resolution != "1K" {
		t.Errorf("deliverable 0 settings mismatch: model=%q, ar=%q, res=%q", d0.Model, d0.AspectRatio, d0.Resolution)
	}

	// Verify video deliverable retains its exact model and settings
	d1 := loaded.Deliverables[1]
	if d1.Model != "veo-3.1-lite-generate-preview" || d1.AspectRatio != "16:9" || d1.Resolution != "720p" || d1.DurationSeconds != 8 {
		t.Errorf("deliverable 1 settings mismatch: model=%q, ar=%q, res=%q, dur=%d",
			d1.Model, d1.AspectRatio, d1.Resolution, d1.DurationSeconds)
	}
}

func TestSessionArtifactLineage_ModelAndSettings(t *testing.T) {
	lineage := SessionArtifactLineage{
		Model:           "gemini-omni-1.1-flash",
		AspectRatio:     "16:9",
		Resolution:      "720p",
		DurationSeconds: 10,
	}

	if err := validateArtifactLineage(lineage); err != nil {
		t.Fatalf("validateArtifactLineage failed: %v", err)
	}

	normalizeArtifactLineage(&lineage)
	if lineage.Model != "gemini-omni-1.1-flash" || lineage.AspectRatio != "16:9" || lineage.Resolution != "720p" {
		t.Errorf("normalize altered valid values")
	}

	l2 := lineage
	if !equalArtifactLineage(lineage, l2) {
		t.Errorf("equalArtifactLineage should be true for identical lineage")
	}

	l2.Model = "other-model"
	if equalArtifactLineage(lineage, l2) {
		t.Errorf("equalArtifactLineage should be false after model change")
	}
}
