package runtime

import (
	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: native capture uses exact bounded output dimensions and reviewed
// immutable profiles. Threat: viewport drift, oversized captures and policy forgery.
// The capture request boundary is narrower than a browser for these assertions.
func TestArtifactV3ReviewedViewport(t *testing.T) {
	p, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
	m := pebblestore.ArtifactV3Manifest{Entrypoint: "index.html", AnimationProfile: p}
	files := map[string][]byte{"index.html": []byte(nativePreviewAnimationManifest)}
	r, err := artifactV3PreviewCaptureRequest(m, files)
	if err != nil || r.ViewportWidth != 1920 || r.ViewportHeight != 1080 {
		t.Fatalf("viewport=%+v err=%v", r, err)
	}
	m.OutputRequirements = &pebblestore.SessionArtifactOutputRequirements{Width: 1080, Height: 1920}
	r, err = artifactV3PreviewCaptureRequest(m, files)
	if err != nil || r.ViewportWidth != 1080 || r.ViewportHeight != 1920 {
		t.Fatalf("portrait=%+v err=%v", r, err)
	}
	m.OutputRequirements.Width = 1920
	if _, err = artifactV3PreviewCaptureRequest(m, files); err == nil {
		t.Fatal("oversized accepted")
	}
	m.OutputRequirements = nil
	p.RegistryVersion = "2026-08-16.v1"
	p.Budgets.MaxParticles = 0
	if _, err = artifactV3PreviewCaptureRequest(m, files); err != nil {
		t.Fatal(err)
	}
	p.Budgets.MaxParticles = 2000
	if _, err = artifactV3PreviewCaptureRequest(m, files); err == nil {
		t.Fatal("forged historical accepted")
	}
}
