package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeHTMLAnimationRenderer struct {
	req    htmlcapture.AnimationRequest
	result htmlcapture.AnimationResult
	err    error
}

func (f *fakeHTMLAnimationRenderer) RenderAnimation(_ context.Context, req htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	f.req = req
	if f.err != nil {
		return htmlcapture.AnimationResult{}, f.err
	}
	if len(f.result.MP4) == 0 {
		f.result = htmlcapture.AnimationResult{MP4: testAnimationMP4(), DurationMS: req.DurationMS, FPS: req.FPS, FrameCount: (req.DurationMS*req.FPS + 999) / 1000}
	}
	return f.result, nil
}

func testAnimationMP4() []byte {
	return []byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0, 'i', 's', 'o', 'm'}
}

func reviewedMotionProfile(t *testing.T) *pebblestore.SessionArtifactAnimationProfile {
	t.Helper()
	profile, err := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestExportHTMLAnimationUsesManifestTimelineAndPublishesExactLineage(t *testing.T) {
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1200,"fps":24}</script><body></body>`
	authority := &fakeArtifactAuthority{readBody: []byte(html), variant: pebblestore.SessionArtifactVariant{ID: "source-variant", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 7, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html", AnimationProfile: reviewedMotionProfile(t)}}
	renderer := &fakeHTMLAnimationRenderer{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(renderer)
	ctx, scope := artifactToolContext()
	output, err := runtime.executeManageArtifact(ctx, scope, "animation-call", map[string]any{"action": "export_html_animation", "session_id": "source-session", "collection_id": "source-collection", "variant_id": "source-variant", "event_seq": 7})
	if err != nil {
		t.Fatalf("export_html_animation: %v", err)
	}
	if renderer.req.DurationMS != 1200 || renderer.req.FPS != 24 || renderer.req.Entry != "index.html" {
		t.Fatalf("renderer request = %+v", renderer.req)
	}
	if authority.created.SourceSessionID != "source-session" || authority.created.SourceCollectionID != "source-collection" || authority.created.SourceVariantID != "source-variant" || authority.created.SourceEventSeq != 7 {
		t.Fatalf("lineage = %+v", authority.created)
	}
	if authority.created.MediaType != "video/mp4" || authority.created.AnimationProfile == nil || authority.created.AnimationProfile.ProfileID != "final_render" || authority.created.OutputRequirements == nil || authority.created.OutputRequirements.Width != 1920 || authority.created.OutputRequirements.Height != 1080 {
		t.Fatalf("published contract = %+v", authority.created)
	}
	for _, required := range []string{`"action":"export_html_animation"`, `"media_type":"video/mp4"`, `"profile_id":"final_render"`, `"source_reference"`} {
		if !strings.Contains(output, required) {
			t.Fatalf("output lacks %s: %s", required, output)
		}
	}
}

func TestExportHTMLAnimationRejectsManifestBoundsAndUnreviewedProfile(t *testing.T) {
	valid := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`
	authority := &fakeArtifactAuthority{readBody: []byte(valid), variant: pebblestore.SessionArtifactVariant{ID: "source", CollectionID: "collection", SessionID: "source-session", EventSeq: 9, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html"}}
	renderer := &fakeHTMLAnimationRenderer{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(renderer)
	ctx, scope := artifactToolContext()
	args := map[string]any{"action": "export_html_animation", "session_id": "source-session", "collection_id": "collection", "variant_id": "source", "event_seq": 9}
	if _, err := runtime.executeManageArtifact(ctx, scope, "animation-profile", args); err == nil || !strings.Contains(err.Error(), "animation_profile_unsupported") {
		t.Fatalf("unreviewed profile error = %v", err)
	}
	if renderer.req.Entry != "" {
		t.Fatal("renderer called before profile validation")
	}
	authority.variant.AnimationProfile = reviewedMotionProfile(t)
	authority.readBody = []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":10001,"fps":30}</script>`)
	if _, err := runtime.executeManageArtifact(ctx, scope, "animation-bounds", args); err == nil || !strings.Contains(err.Error(), "animation_manifest_invalid") {
		t.Fatalf("manifest bounds error = %v", err)
	}
}

func TestValidateAnimationMP4(t *testing.T) {
	data := testAnimationMP4()
	if err := validateAnimationMP4(data); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) == "" {
		t.Fatal("empty digest")
	}
	if err := validateAnimationMP4([]byte("not-mp4")); err == nil {
		t.Fatal("expected invalid MP4 error")
	}
}
