package pebblestore

import "testing"

func validStoredAnimationProfile() *SessionArtifactAnimationProfile {
	return &SessionArtifactAnimationProfile{
		ProfileID: "spatial_3d", RegistryVersion: "2026-08-16.v1", RuntimeKind: "three_webgl",
		RuntimePackage: "three", RuntimeVersion: "0.185.1", Heavy: true,
		Budgets: SessionArtifactAnimationBudgets{
			MaxSimultaneousLivePreviews: 1, MaxWebGLContexts: 1, MaxDevicePixelRatio: 1.5,
			MaxCanvasPixels: 2_073_600, MaxParticles: 2_000, MaxDrawCallsPerFrame: 200,
			PauseWhenOffscreen: true, StopWhenDocumentHidden: true,
			ReducedMotionBehavior: "static_first_frame", NetworkAllowed: false,
		},
	}
}

func TestValidateArtifactAnimationProfileRejectsUnsafeSnapshots(t *testing.T) {
	if err := validateArtifactAnimationProfile(validStoredAnimationProfile()); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	cases := []*SessionArtifactAnimationProfile{
		func() *SessionArtifactAnimationProfile {
			p := validStoredAnimationProfile()
			p.RuntimeVersion = "latest"
			return p
		}(),
		func() *SessionArtifactAnimationProfile {
			p := validStoredAnimationProfile()
			p.RuntimePackage = "https://cdn.example/three.js"
			return p
		}(),
		func() *SessionArtifactAnimationProfile {
			p := validStoredAnimationProfile()
			p.Budgets.NetworkAllowed = true
			return p
		}(),
		func() *SessionArtifactAnimationProfile {
			p := validStoredAnimationProfile()
			p.Budgets.MaxWebGLContexts = 2
			return p
		}(),
		func() *SessionArtifactAnimationProfile {
			p := validStoredAnimationProfile()
			p.ProfileID = "custom"
			return p
		}(),
	}
	for _, profile := range cases {
		if err := validateArtifactAnimationProfile(profile); err == nil {
			t.Fatalf("unsafe snapshot accepted: %+v", profile)
		}
	}
}

func TestCloneSessionArtifactAnimationProfile(t *testing.T) {
	original := validStoredAnimationProfile()
	cloned := cloneSessionArtifactAnimationProfile(original)
	if cloned == original || !equalArtifactAnimationProfile(original, cloned) {
		t.Fatalf("clone is not independent and equal: original=%p cloned=%p", original, cloned)
	}
	cloned.Budgets.MaxParticles = 1
	if original.Budgets.MaxParticles == cloned.Budgets.MaxParticles {
		t.Fatal("clone mutation changed original")
	}
}
