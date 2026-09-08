package artifactv2

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: Designers author only typed scene/behavior parts while the
// server compiler owns host manifests, binder, lifecycle, player bridge,
// scheduler, reduced-motion handling, and fixed viewport containment.
// Threat: freeform host bytes could restore prompt-only animation correctness
// or nondeterministic output. This compiler test is the narrowest byte proof.
func TestMotionCompilerOwnsDeterministicRuntime(t *testing.T) {
	input := motionCompileTestInput(t)
	compiler := MotionCompiler{}
	first, err := compiler.Compile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := compiler.Compile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes, second.Bytes) {
		t.Fatal("same exact revisions did not compile deterministically")
	}
	text := string(first.Bytes)
	for _, required := range []string{"swarm-animation-manifest", "swarm-iteration-manifest", "__SWARM_ANIMATION_BIND__", "requestAnimationFrame", "swarm-player/v1", "prefers-reduced-motion", "width:1920px;height:1080px", "ready:async", "seek:async", "stop:async"} {
		if !strings.Contains(text, required) {
			t.Fatalf("compiled runtime missing %q", required)
		}
	}
	for _, forbidden := range []string{"<script src=", "http://", "https://", "manage_artifact", "create_package"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("compiled runtime contains forbidden host dependency %q", forbidden)
		}
	}
	if first.CompilerVersion != MotionCompilerVersion || first.TemplateVersion != MotionTemplateVersion || len(first.RepresentativeTimestampsMS) < 3 {
		t.Fatalf("incomplete compiler binding: %+v", first)
	}
}

// Requirement: static failures target the exact repairable part and preserve
// safe structured diagnostics. Threat: invalid geometry could reach Chrome or
// expose source/browser detail in an opaque error.
func TestMotionCompilerRejectsViewportBeforeRendererWithPartDiagnostic(t *testing.T) {
	input := motionCompileTestInput(t)
	input.Parts[0].Body = []byte(`{"version":"artifact.motion.scene/v1","duration_ms":1000,"fps":30,"elements":[{"id":"hero","kind":"rect","x":1900,"y":0,"width":100,"height":100}],"sections":[{"id":"intro","label":"Intro","start_ms":0,"end_ms":1000}]}`)
	product, err := (MotionCompiler{}).Compile(context.Background(), input)
	if err == nil || len(product.Diagnostics) != 1 {
		t.Fatalf("expected structured static failure: product=%+v err=%v", product, err)
	}
	diagnostic := product.Diagnostics[0]
	if diagnostic.Code != "motion_viewport_overflow" || diagnostic.Phase != "viewport" || diagnostic.PartID != "scene-part" || diagnostic.AuthoredLocator != "elements[0]" || diagnostic.Bounds == "" {
		t.Fatalf("wrong diagnostic: %+v", diagnostic)
	}
	if strings.Contains(diagnostic.SafeMessage, "1900") || strings.Contains(diagnostic.SafeMessage, string(input.Parts[0].Body)) {
		t.Fatalf("diagnostic leaked authored bytes: %+v", diagnostic)
	}
}

// Requirement: Chrome diagnostics remain bounded and target the scene revision.
// Threat: renderer errors could leak browser output/private source or mutate a
// ready source. This validator unit test proves safe mapping and exact binding.
func TestMotionValidatorMapsTrustedRendererFailure(t *testing.T) {
	input := motionCompileTestInput(t)
	product, err := (MotionCompiler{}).Compile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	build := pebblestore.ArtifactV2BuildResult{ID: "build", Output: &pebblestore.ArtifactV2BlobReceipt{DigestSHA256: digest(product.Bytes)}}
	renderer := &fakeMotionRenderer{err: htmlcapture.NewError("animation_viewport_overflow", "private browser output must not survive")}
	validation, err := (MotionValidator{Renderer: renderer}).Validate(context.Background(), ValidationInput{Build: build, Product: product, Parts: input.Parts})
	if err != nil {
		t.Fatal(err)
	}
	if validation.Status != pebblestore.ArtifactV2ValidationInvalid || len(validation.Diagnostics) != 1 || validation.Diagnostics[0].Code != "animation_viewport_overflow" || validation.Diagnostics[0].PartID != "scene-part" {
		t.Fatalf("unexpected validation: %+v", validation)
	}
	if strings.Contains(validation.Diagnostics[0].SafeMessage, "private") {
		t.Fatalf("renderer detail leaked: %+v", validation.Diagnostics[0])
	}
}

// Requirement: derivative failures are durable but never demote or advance the
// source composition head. Threat: failed MP4/fallback work could corrupt the
// validated source authority or trigger a legacy fallback.
func TestMotionDerivativeFailurePreservesValidatedHead(t *testing.T) {
	store, sessions, author := newAuthorTestService(t)
	defer store.Close()
	principal := Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "owner", RunID: "designer-run", ActorClass: "designer"}
	working, err := author.AllocateWorking(context.Background(), principal, "motion-allocate", "animation", "motion", PolicySnapshot{AnimationProfile: "motion_ui"})
	if err != nil {
		t.Fatal(err)
	}
	grant := AuthorGrant{ID: "motion-grant", ArtifactID: working.ID, OwnerSessionID: "owner", ProducerSessionID: "child", ProducerRunID: principal.RunID, AllowedActions: []string{"inspect_context", "declare_parts", "write_part", "request_build", "submit_candidate"}, AllowPartDeclaration: true, ExpiresAt: author.now().Add(60_000_000_000).UnixMilli(), Policy: normalizedPolicy(PolicySnapshot{AnimationProfile: "motion_ui"})}
	ctx, err := author.DeclareParts(context.Background(), principal, grant, "motion-declare", []AuthorPartDeclaration{{Key: "scene", Label: "Scene", MediaClass: "motion", Order: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := author.WritePart(context.Background(), principal, grant, "motion-write", AuthorPartWrite{PartID: ctx.Parts[0].ID, MediaType: MotionSceneMediaType, Body: motionCompileTestInput(t).Parts[0].Body}); err != nil {
		t.Fatal(err)
	}
	motionAuthor := NewAuthorService(author.core, MotionCompiler{}, MotionValidator{Renderer: &fakeMotionRenderer{result: MotionRenderResult{PreviewPNG: []byte("png"), Frames: []MotionFrame{{Slot: "opening", PNG: []byte("png")}}}}})
	candidate, err := motionAuthor.RequestBuild(context.Background(), principal, grant, "motion-build")
	if err != nil || candidate.State != pebblestore.ArtifactV2StateReady {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
	before, _, _ := sessions.GetArtifactV2Working("account-1", working.ID)
	derivative, err := author.core.CreateDerivative(context.Background(), principal, CreateDerivativeInput{RequestID: "motion-mp4", ArtifactID: working.ID, ExpectedWorkingRevision: before.Revision, Kind: "mp4", Renderer: &fakeMotionRenderer{err: htmlcapture.NewError("animation_encode_failed", "private encoder output")}})
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ := sessions.GetArtifactV2Working("account-1", working.ID)
	if derivative.Status != "failed" || derivative.Output != nil || after.CompositionHead == nil || before.CompositionHead == nil || *after.CompositionHead != *before.CompositionHead || after.PublishedHead != nil {
		t.Fatalf("failed derivative mutated source: derivative=%+v before=%+v after=%+v", derivative, before, after)
	}
	if len(derivative.Diagnostics) != 1 || strings.Contains(derivative.Diagnostics[0].SafeMessage, "private") {
		t.Fatalf("unsafe derivative diagnostic: %+v", derivative.Diagnostics)
	}
}

type fakeMotionRenderer struct {
	result MotionRenderResult
	err    error
}

func (f *fakeMotionRenderer) Preflight(context.Context, []byte, int, int) (MotionRenderResult, error) {
	return f.result, f.err
}
func (f *fakeMotionRenderer) Render(context.Context, []byte, int, int) (MotionRenderResult, error) {
	return f.result, f.err
}

func motionCompileTestInput(t *testing.T) CompileInput {
	t.Helper()
	scene := []byte(`{"version":"artifact.motion.scene/v1","duration_ms":2000,"fps":30,"background":"#101820","elements":[{"id":"hero","kind":"text","text":"Hello","x":100,"y":100,"width":800,"height":200,"color":"#ffffff","font_size":72}],"sections":[{"id":"intro","label":"Intro","start_ms":0,"end_ms":1000},{"id":"finish","label":"Finish","start_ms":1000,"end_ms":2000}]}`)
	behavior := []byte(`{"version":"artifact.motion.behavior/v1","tracks":[{"target_id":"hero","property":"opacity","from":0,"to":1,"start_ms":0,"end_ms":800,"easing":"ease_in_out"}]}`)
	return CompileInput{Parts: []CompilePart{
		{Definition: pebblestore.ArtifactV2Part{ID: "scene-part", Order: 1}, Revision: pebblestore.ArtifactV2PartRevision{Blob: pebblestore.ArtifactV2BlobReceipt{MediaType: MotionSceneMediaType, DigestSHA256: digest(scene)}}, Body: scene},
		{Definition: pebblestore.ArtifactV2Part{ID: "behavior-part", Order: 2}, Revision: pebblestore.ArtifactV2PartRevision{Blob: pebblestore.ArtifactV2BlobReceipt{MediaType: MotionBehaviorMediaType, DigestSHA256: digest(behavior)}}, Body: behavior},
	}}
}

var _ = errors.New
