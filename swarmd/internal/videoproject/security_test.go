package videoproject

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestVideoprojectSecurityRejections(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)

	principalA := identity.Principal{
		AccountScopeID: "acc_alpha",
		UserID:         "usr_alpha",
	}
	principalB := identity.Principal{
		AccountScopeID: "acc_beta",
		UserID:         "usr_beta",
	}

	sessionA := "sess_alpha"
	store.sessions[sessionA] = pebblestore.SessionSnapshot{
		ID:             sessionA,
		AccountScopeID: principalA.AccountScopeID,
		UserID:         principalA.UserID,
	}

	ctx := context.Background()

	// 1. Principal B cannot create project in Principal A's session
	_, _, err := svc.CreateProject(ctx, principalB, CreateProjectInput{
		SessionID: sessionA,
		ProjectID: "vproj_hack",
		Title:     "Unauthorized Project",
	})
	if err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("expected ownership error for cross-account project create, got: %v", err)
	}

	// 2. Cannot reference staging (unready) artifact in timeline
	stagingVariantKey := fmt.Sprintf("%s/%s/col_raw/var_staging_1", principalA.AccountScopeID, sessionA)
	store.artifacts[stagingVariantKey] = pebblestore.SessionArtifactVariant{
		ID:           "var_staging_1",
		CollectionID: "col_raw",
		Status:       pebblestore.SessionArtifactStatusStaging, // not ready!
	}

	_, _, err = svc.CreateProject(ctx, principalA, CreateProjectInput{
		SessionID: sessionA,
		ProjectID: "vproj_with_unready_artifact",
		Title:     "Invalid Artifact Project",
		InitialTimeline: &pebblestore.VideoProjectTimeline{
			OutputPreset: pebblestore.VideoPresetLandscape1080p,
			Clips: []pebblestore.VideoTimelineClip{
				{
					ID:         "clip_1",
					Track:      0,
					Sequence:   0,
					SourceKind: pebblestore.VideoClipSourceKindManagedArtifact,
					ArtifactRef: &pebblestore.SessionArtifactSelectionReference{
						SessionID:    sessionA,
						CollectionID: "col_raw",
						VariantID:    "var_staging_1",
					},
					DurationMs:      1000,
					TimelineStartMs: 0,
					TimelineEndMs:   1000,
					Visible:         true,
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not in ready status") {
		t.Fatalf("expected error referencing unready artifact, got: %v", err)
	}

	// 3. Cannot reference nonexistent artifact
	_, _, err = svc.CreateProject(ctx, principalA, CreateProjectInput{
		SessionID: sessionA,
		ProjectID: "vproj_with_missing_artifact",
		Title:     "Missing Artifact Project",
		InitialTimeline: &pebblestore.VideoProjectTimeline{
			OutputPreset: pebblestore.VideoPresetLandscape1080p,
			Clips: []pebblestore.VideoTimelineClip{
				{
					ID:         "clip_1",
					Track:      0,
					Sequence:   0,
					SourceKind: pebblestore.VideoClipSourceKindManagedArtifact,
					ArtifactRef: &pebblestore.SessionArtifactSelectionReference{
						SessionID:    sessionA,
						CollectionID: "col_missing",
						VariantID:    "var_missing",
					},
					DurationMs:      1000,
					TimelineStartMs: 0,
					TimelineEndMs:   1000,
					Visible:         true,
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error referencing nonexistent artifact, got: %v", err)
	}

	// 4. Invalid principal rejected
	_, _, err = svc.CreateProject(ctx, identity.Principal{}, CreateProjectInput{
		SessionID: sessionA,
		ProjectID: "vproj_invalid_principal",
	})
	if err == nil || !strings.Contains(err.Error(), "authenticated principal is required") {
		t.Fatalf("expected error for invalid principal, got: %v", err)
	}
}
