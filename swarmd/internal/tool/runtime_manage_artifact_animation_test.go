package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func testAnimationFallbackPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, htmlcapture.Width, htmlcapture.Height))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var body bytes.Buffer
	if err := png.Encode(&body, img); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

type fakeHTMLAnimationRenderer struct {
	req          htmlcapture.AnimationRequest
	result       htmlcapture.AnimationResult
	err          error
	preflightErr error
	started      chan struct{}
	release      chan struct{}
}

func (f *fakeHTMLAnimationRenderer) PreflightAnimation(_ context.Context, req htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	f.req = req
	return f.result, f.preflightErr
}

func (f *fakeHTMLAnimationRenderer) RenderAnimation(ctx context.Context, req htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	f.req = req
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return htmlcapture.AnimationResult{}, ctx.Err()
		}
	}
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

func testAnimationInspectionFrames(t *testing.T, durationMS, fps int) []htmlcapture.AnimationInspectionFrame {
	t.Helper()
	frame := testAnimationFallbackPNG(t)
	frameCount := (durationMS*fps + 999) / 1000
	return []htmlcapture.AnimationInspectionFrame{
		{Slot: "start", TimestampMS: 0, PNG: frame},
		{Slot: "middle", TimestampMS: (frameCount / 2) * 1000 / fps, PNG: frame},
		{Slot: "exit", TimestampMS: (frameCount - 1) * 1000 / fps, PNG: frame},
	}
}

func testAnimationPreflightResult(t *testing.T, durationMS, fps int) htmlcapture.AnimationResult {
	return htmlcapture.AnimationResult{
		PreviewPNG: testAnimationFallbackPNG(t), InspectionFrames: testAnimationInspectionFrames(t, durationMS, fps),
		DurationMS: durationMS, FPS: fps, FrameCount: (durationMS*fps + 999) / 1000,
	}
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
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script><script id="swarm-iteration-manifest" type="application/json">{"version":"swarm.iteration/v1","duration_ms":1000,"sections":[{"id":"opening","label":"Opening","start_ms":0,"end_ms":500},{"id":"close","label":"Close","start_ms":500,"end_ms":1000}]}</script><body></body>`
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
	if renderer.req.DurationMS != 1000 || renderer.req.FPS != 30 || renderer.req.Entry != "index.html" {
		t.Fatalf("renderer request = %+v", renderer.req)
	}
	if authority.created.SourceSessionID != "source-session" || authority.created.SourceCollectionID != "source-collection" || authority.created.SourceVariantID != "source-variant" || authority.created.SourceEventSeq != 7 {
		t.Fatalf("lineage = %+v", authority.created)
	}
	if authority.created.MediaType != "video/mp4" || authority.created.AnimationProfile == nil || authority.created.AnimationProfile.ProfileID != "final_render" || authority.created.OutputRequirements == nil || authority.created.OutputRequirements.Width != 1920 || authority.created.OutputRequirements.Height != 1080 || len(authority.created.Parts) != 2 || authority.created.Parts[1].ID != "close" || authority.created.Parts[1].EndMs != 1000 {
		t.Fatalf("published contract = %+v", authority.created)
	}
	if !authority.created.AutoAccept {
		t.Fatalf("single animation export did not request auto-accept: %+v", authority.created)
	}
	for _, required := range []string{`"action":"export_html_animation"`, `"media_type":"video/mp4"`, `"profile_id":"final_render"`, `"source_reference"`} {
		if !strings.Contains(output, required) {
			t.Fatalf("output lacks %s: %s", required, output)
		}
	}
}

func TestExportHTMLAnimationQueuesLongTimelineWithoutHoldingToolCall(t *testing.T) {
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":74920,"fps":60}</script><script id="swarm-iteration-manifest" type="application/json">{"version":"swarm.iteration/v1","duration_ms":74920,"sections":[{"id":"opening","label":"Opening","start_ms":0,"end_ms":12000},{"id":"close","label":"Close","start_ms":60000,"end_ms":74920}]}</script><body></body>`
	authority := &fakeArtifactAuthority{readBody: []byte(html), variant: pebblestore.SessionArtifactVariant{ID: "source-variant", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 7, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html", AnimationProfile: reviewedMotionProfile(t)}}
	renderer := &fakeHTMLAnimationRenderer{started: make(chan struct{}, 1), release: make(chan struct{})}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(renderer)
	ctx, scope := artifactToolContext()
	startedAt := time.Now()
	output, err := runtime.executeManageArtifact(ctx, scope, "long-animation-call", map[string]any{"action": "export_html_animation", "session_id": "source-session", "collection_id": "source-collection", "variant_id": "source-variant", "event_seq": 7})
	if err != nil {
		t.Fatalf("queue long animation: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("long animation tool call blocked for %s", elapsed)
	}
	if !strings.Contains(output, `"status":"staging"`) || !strings.Contains(output, `"stage":"queued"`) || authority.created.SourceEventSeq != 7 || len(authority.created.Parts) != 2 || authority.created.Parts[1].EndMs != 74920 {
		t.Fatalf("queued output/lineage/parts = %s / %+v", output, authority.created)
	}
	select {
	case <-renderer.started:
	case <-time.After(time.Second):
		t.Fatal("background renderer did not start")
	}
	cancelArgs := map[string]any{"action": "cancel_html_animation_export", "session_id": authority.variant.SessionID, "collection_id": authority.variant.CollectionID, "variant_id": authority.variant.ID, "event_seq": authority.variant.EventSeq}
	cancelled, cancelErr := runtime.executeManageArtifact(ctx, scope, "cancel-long-animation", cancelArgs)
	if cancelErr != nil || !strings.Contains(cancelled, `"failure_code":"animation_cancelled"`) {
		t.Fatalf("cancel long animation: %v / %s", cancelErr, cancelled)
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
	authority.readBody = []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":600001,"fps":30}</script>`)
	if _, err := runtime.executeManageArtifact(ctx, scope, "animation-bounds", args); err == nil || !strings.Contains(err.Error(), "animation_manifest_invalid") {
		t.Fatalf("manifest bounds error = %v", err)
	}
}

func TestExportHTMLAnimationFallbackPublishesPreflightFrameWithExactLineage(t *testing.T) {
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":10000,"fps":30}</script>`
	authority := &fakeArtifactAuthority{readBody: []byte(html), variant: pebblestore.SessionArtifactVariant{ID: "source", CollectionID: "collection", SessionID: "source-session", EventSeq: 9, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html", AnimationProfile: reviewedMotionProfile(t)}}
	renderer := &fakeHTMLAnimationRenderer{result: htmlcapture.AnimationResult{PreviewPNG: testAnimationFallbackPNG(t)}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(renderer)
	ctx, scope := artifactToolContext()
	_, err := runtime.executeManageArtifact(ctx, scope, "fallback", map[string]any{"action": "export_html_animation_fallback", "session_id": "source-session", "collection_id": "collection", "variant_id": "source", "event_seq": 9})
	if err != nil {
		t.Fatal(err)
	}
	if authority.created.MediaType != "image/png" || authority.created.Role != pebblestore.SessionArtifactRoleRenderOnly || !bytes.Equal(authority.created.Body, renderer.result.PreviewPNG) || authority.created.SourceSessionID != "source-session" || authority.created.SourceCollectionID != "collection" || authority.created.SourceVariantID != "source" || authority.created.SourceEventSeq != 9 {
		t.Fatalf("animation fallback publication = %+v", authority.created)
	}
}

func TestExportHTMLAnimationPreflightsBeforeQueuingAndPreservesFailureCode(t *testing.T) {
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":12000,"fps":60}</script>`
	authority := &fakeArtifactAuthority{readBody: []byte(html), variant: pebblestore.SessionArtifactVariant{ID: "source", CollectionID: "collection", SessionID: "source-session", EventSeq: 9, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html", AnimationProfile: reviewedMotionProfile(t)}}
	renderer := &fakeHTMLAnimationRenderer{preflightErr: htmlcapture.NewError("animation_seek_failed", "animation runtime did not acknowledge the renderer-controlled timestamp")}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(renderer)
	ctx, scope := artifactToolContext()
	args := map[string]any{"action": "export_html_animation", "session_id": "source-session", "collection_id": "collection", "variant_id": "source", "event_seq": 9}
	if _, err := runtime.executeManageArtifact(ctx, scope, "preflight", args); err == nil || !strings.Contains(err.Error(), "animation_seek_failed") {
		t.Fatalf("preflight error = %v", err)
	}
	if authority.variant.Status == pebblestore.SessionArtifactStatusStaging {
		t.Fatalf("invalid animation was queued: %+v", authority.variant)
	}
	if got := animationFailureCode(animationError("animation_frame_unstable", "unstable")); got != "animation_frame_unstable" {
		t.Fatalf("failure code = %q", got)
	}
	renderer.preflightErr = nil
	renderer.err = htmlcapture.NewError("animation_frame_unstable", "animation changed after the renderer selected a deterministic timestamp")
	prepared := animationExportPrepared{Manifest: animationManifest{DurationMS: 12000, FPS: 60}, Input: artifact.CreateInput{RequestID: "render-request", CollectionID: "render-collection", VariantID: "render-variant"}}
	runtime.runHTMLAnimationExport(context.Background(), artifact.Principal{SessionID: "source-session"}, prepared, "render-variant")
	if authority.variant.Status != pebblestore.SessionArtifactStatusFailed || authority.variant.FailureCode != "animation_frame_unstable" {
		t.Fatalf("background failure = %+v", authority.variant)
	}
}

func TestManagedAnimationFailureCodeExtractionIsBoundedAndRejectsSuffixInjection(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{err: nil, want: "animation_renderer_failed"},
		{err: animationError("animation_seek_rejected", "safe"), want: "animation_seek_rejected"},
		{err: animationError("animation_seek_timeout", "safe"), want: "animation_seek_timeout"},
		{err: animationError("animation_seek_ack_mismatch", "safe"), want: "animation_seek_ack_mismatch"},
		{err: animationError("animation_seek_failed", "safe"), want: "animation_seek_failed"},
		{err: errors.New("manage_artifact HTML animation failed (code=animation_seek_failed private): unsafe"), want: "animation_renderer_failed"},
		{err: errors.New("manage_artifact HTML animation failed (code=animation_seek_failed/../../private): unsafe"), want: "animation_renderer_failed"},
		{err: errors.New("private animation_seek_failed text"), want: "animation_renderer_failed"},
	}
	for _, tc := range cases {
		if got := animationFailureCode(tc.err); got != tc.want {
			t.Fatalf("animationFailureCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestManagedAnimationCreatePreflightsBeforeReadyAndPersistsTerminalFailure(t *testing.T) {
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`
	authority := &fakeArtifactAuthority{}
	renderer := &fakeHTMLAnimationRenderer{preflightErr: htmlcapture.NewError("animation_viewport_overflow", "private/source/path: author exception secret")}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(renderer)
	ctx, scope := artifactToolContext()
	args := map[string]any{"action": "create", "filename": "intro.html", "media_type": "text/html", "content": html, "animation_profile": map[string]any{"profile": "motion_ui"}}
	if _, err := runtime.executeManageArtifact(ctx, scope, "managed-animation-failure", args); err == nil || !strings.Contains(err.Error(), "animation_viewport_overflow") || strings.Contains(err.Error(), "private/source/path") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("managed animation preflight error = %v", err)
	}
	if authority.reserveCalls != 1 || authority.createCalls != 0 || len(authority.created.Body) != 0 || authority.variant.Status != pebblestore.SessionArtifactStatusFailed || authority.variant.FailureCode != "animation_viewport_overflow" {
		t.Fatalf("managed animation failure state = %+v reserve=%d create=%d", authority.variant, authority.reserveCalls, authority.createCalls)
	}
	failedVariantID := authority.variant.ID
	if _, err := runtime.executeManageArtifact(ctx, scope, "managed-animation-failure", args); err == nil || !strings.Contains(err.Error(), "animation_publication_conflict") {
		t.Fatalf("failed reference replay error = %v", err)
	}
	if authority.reserveCalls != 2 || authority.createCalls != 0 {
		t.Fatalf("failed replay published bytes: reserve=%d create=%d", authority.reserveCalls, authority.createCalls)
	}
	renderer.preflightErr = nil
	renderer.result = testAnimationPreflightResult(t, 1000, 30)
	if _, err := runtime.executeManageArtifact(ctx, scope, "managed-animation-retry", args); err != nil {
		t.Fatalf("fresh tool-call retry: %v", err)
	}
	if authority.variant.ID == failedVariantID || authority.createCalls != 4 || len(authority.inspectionCreates) != 3 || authority.variant.Status != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("fresh retry did not use a new ready identity: failed=%s variant=%+v", failedVariantID, authority.variant)
	}
}

func TestManagedAnimationCreateAndPackageFinalizeOnlyAfterTrustedPreflight(t *testing.T) {
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`
	for _, tc := range []struct {
		name       string
		action     string
		args       map[string]any
		wantCreate int
		wantPack   int
	}{
		{name: "create", action: "create", args: map[string]any{"action": "create", "filename": "intro.html", "media_type": "text/html", "content": html, "animation_profile": map[string]any{"profile": "motion_ui"}}, wantCreate: 4},
		{name: "package", action: "create_package", args: map[string]any{"action": "create_package", "filename": "intro.zip", "entries": []any{map[string]any{"name": "index.html", "content": html}}, "animation_profile": map[string]any{"profile": "motion_ui"}}, wantCreate: 3, wantPack: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority := &fakeArtifactAuthority{}
			renderer := &fakeHTMLAnimationRenderer{result: testAnimationPreflightResult(t, 1000, 30)}
			runtime := NewRuntime(1)
			runtime.SetArtifactAuthority(authority)
			runtime.SetHTMLAnimationRenderer(renderer)
			ctx, scope := artifactToolContext()
			output, err := runtime.executeManageArtifact(ctx, scope, "managed-animation-"+tc.name, tc.args)
			if err != nil {
				t.Fatalf("%s: %v", tc.action, err)
			}
			if !strings.Contains(output, `"trusted_animation_preflight":true`) || !strings.Contains(output, `"animation_inspection_references"`) {
				t.Fatalf("%s output omitted trusted preflight evidence: %s", tc.action, output)
			}
			if authority.reserveCalls != 1 || authority.createCalls != tc.wantCreate || authority.packageCalls != tc.wantPack || authority.variant.Status != pebblestore.SessionArtifactStatusReady {
				t.Fatalf("publication order/result: reserve=%d create=%d package=%d variant=%+v", authority.reserveCalls, authority.createCalls, authority.packageCalls, authority.variant)
			}
		})
	}
}

func TestManagedDesignerProfiledAnimationUsesInjectedGate(t *testing.T) {
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(&fakeHTMLAnimationRenderer{result: testAnimationPreflightResult(t, 1000, 30)})
	scope := WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "parent-1", ChildSessionID: "child-1", TaskCallID: "task-1", CollectionID: "collection-1", VariantID: "variant-1", AnimationProfile: reviewedMotionProfile(t)})
	output, err := runtime.executeManageArtifact(ctx, scope, "managed-designer-animation", map[string]any{"action": "create", "filename": "intro.html", "media_type": "text/html", "content": html})
	if err != nil {
		t.Fatal(err)
	}
	if authority.created.CollectionID != "collection-1" || authority.created.VariantID != "variant-1" || authority.created.SourceSessionID != "" || authority.reserveCalls != 1 || authority.createCalls != 4 || len(authority.inspectionCreates) != 3 || !strings.Contains(output, `"trusted_animation_preflight":true`) || !strings.Contains(output, `"animation_inspection_references"`) {
		t.Fatalf("managed Designer animation gate: created=%+v reserve=%d create=%d output=%s", authority.created, authority.reserveCalls, authority.createCalls, output)
	}
}

func TestWorkspaceAnimationPublicationFailsTerminallyBeforeReady(t *testing.T) {
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(&fakeHTMLAnimationRenderer{preflightErr: htmlcapture.NewError("animation_frame_unstable", "representative frame changed")})
	ctx, scope := artifactToolContext()
	scope.PrimaryPath = t.TempDir()
	if err := os.WriteFile(filepath.Join(scope.PrimaryPath, "intro.html"), []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runtime.executeManageArtifact(ctx, scope, "workspace-animation-failure", map[string]any{"action": "publish_workspace", "source": "intro.html", "animation_profile": map[string]any{"profile": "motion_ui"}})
	if err == nil || !strings.Contains(err.Error(), "animation_frame_unstable") || authority.variant.Status != pebblestore.SessionArtifactStatusFailed || authority.publishCalls != 0 || authority.createCalls != 0 {
		t.Fatalf("workspace animation failure: err=%v variant=%+v publish=%d create=%d", err, authority.variant, authority.publishCalls, authority.createCalls)
	}
}

func TestWorkspaceAnimationPackagePublishesExactPreflightedEntries(t *testing.T) {
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script><script src="app.js"></script>`
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(&fakeHTMLAnimationRenderer{result: testAnimationPreflightResult(t, 1000, 30)})
	ctx, scope := artifactToolContext()
	scope.PrimaryPath = t.TempDir()
	packageRoot := filepath.Join(scope.PrimaryPath, "intro")
	if err := os.Mkdir(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "index.html"), []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "app.js"), []byte("const exact = true;"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runtime.executeManageArtifact(ctx, scope, "workspace-animation-package", map[string]any{"action": "publish_workspace", "source": "intro", "animation_profile": map[string]any{"profile": "motion_ui"}})
	if err != nil {
		t.Fatal(err)
	}
	if authority.publishCalls != 0 || authority.packageCalls != 1 || len(authority.packaged.Entries) != 2 || !strings.Contains(output, `"trusted_animation_preflight":true`) {
		t.Fatalf("workspace animation package result: publish=%d package=%d entries=%+v output=%s", authority.publishCalls, authority.packageCalls, authority.packaged.Entries, output)
	}
	entries := map[string]string{}
	for _, entry := range authority.packaged.Entries {
		entries[entry.Name] = string(entry.Data)
	}
	if entries["index.html"] != html || entries["app.js"] != "const exact = true;" {
		t.Fatalf("workspace animation package bytes changed: %+v", entries)
	}
}

func TestManagedAnimationRendererUnavailablePersistsTerminalFailure(t *testing.T) {
	html := `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	_, err := runtime.executeManageArtifact(ctx, scope, "managed-renderer-missing", map[string]any{"action": "create", "filename": "intro.html", "media_type": "text/html", "content": html, "animation_profile": map[string]any{"profile": "motion_ui"}})
	if err == nil || !strings.Contains(err.Error(), "animation_renderer_unavailable") || authority.variant.Status != pebblestore.SessionArtifactStatusFailed || authority.variant.FailureCode != "animation_renderer_unavailable" {
		t.Fatalf("renderer-unavailable publication: err=%v variant=%+v", err, authority.variant)
	}
}

func TestNonHTMLProfiledPublicationDoesNotRequireAnimationRenderer(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	output, err := runtime.executeManageArtifact(ctx, scope, "profiled-css", map[string]any{"action": "create", "filename": "motion.css", "media_type": "text/css", "content": "@keyframes fade{}", "animation_profile": map[string]any{"profile": "motion_ui"}})
	if err != nil {
		t.Fatal(err)
	}
	if authority.reserveCalls != 0 || authority.createCalls != 1 || strings.Contains(output, "trusted_animation_preflight") {
		t.Fatalf("non-HTML profiled publication was gated: reserve=%d create=%d output=%s", authority.reserveCalls, authority.createCalls, output)
	}
}

func TestManagedAnimationPublicationRequiresManifestAndCanonicalPreview(t *testing.T) {
	tests := []struct {
		name       string
		html       string
		result     htmlcapture.AnimationResult
		wantCode   string
		wantCalled bool
	}{
		{name: "manifest", html: `<!doctype html><body></body>`, wantCode: "animation_manifest_missing"},
		{name: "preview", html: `<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`, result: htmlcapture.AnimationResult{DurationMS: 1000, FPS: 30, FrameCount: 30, PreviewPNG: testPNGImage()}, wantCode: "animation_png_invalid", wantCalled: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			authority := &fakeArtifactAuthority{}
			renderer := &fakeHTMLAnimationRenderer{result: tc.result}
			runtime := NewRuntime(1)
			runtime.SetArtifactAuthority(authority)
			runtime.SetHTMLAnimationRenderer(renderer)
			ctx, scope := artifactToolContext()
			_, err := runtime.executeManageArtifact(ctx, scope, "managed-invalid-"+tc.name, map[string]any{"action": "create", "filename": "intro.html", "media_type": "text/html", "content": tc.html, "animation_profile": map[string]any{"profile": "motion_ui"}})
			if err == nil || !strings.Contains(err.Error(), tc.wantCode) || authority.variant.Status != pebblestore.SessionArtifactStatusFailed || authority.createCalls != 0 {
				t.Fatalf("invalid publication: err=%v variant=%+v create=%d", err, authority.variant, authority.createCalls)
			}
			if (renderer.req.Entry != "") != tc.wantCalled {
				t.Fatalf("renderer called=%v want=%v", renderer.req.Entry != "", tc.wantCalled)
			}
		})
	}
}

func TestAnimationExportActionsUseActionSpecificExactReferenceValidation(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLAnimationRenderer(&fakeHTMLAnimationRenderer{})
	ctx, scope := artifactToolContext()
	for _, action := range []string{"export_html_animation", "export_html_animation_fallback"} {
		_, err := runtime.executeManageArtifact(ctx, scope, "exact-"+action, map[string]any{"action": action, "variant_id": "source"})
		if err == nil || !strings.Contains(err.Error(), action+" requires a complete exact ready HTML reference") {
			t.Fatalf("%s exact-reference error = %v", action, err)
		}
	}
}

func TestAnimationOverallProgress(t *testing.T) {
	cases := []struct {
		progress htmlcapture.AnimationProgress
		want     float64
	}{
		{htmlcapture.AnimationProgress{Stage: "queue_wait", Completed: 0, Total: 1}, 0},
		{htmlcapture.AnimationProgress{Stage: "frame_capture", Completed: 50, Total: 100}, 50},
		{htmlcapture.AnimationProgress{Stage: "segment_encode", Completed: 5, Total: 10}, 50},
		{htmlcapture.AnimationProgress{Stage: "segment_concatenation", Completed: 1, Total: 1}, 99},
	}
	for _, tc := range cases {
		if got := animationOverallProgress(tc.progress); got != tc.want {
			t.Fatalf("%s progress = %v, want %v", tc.progress.Stage, got, tc.want)
		}
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
