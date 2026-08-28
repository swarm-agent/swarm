package videorender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

type fakeCommandRunner struct {
	lookPathErr error
	runHook     func(ctx context.Context, name string, args ...string) ([]byte, error)
	calls       [][]string
}

func (f *fakeCommandRunner) LookPath(file string) (string, error) {
	if f.lookPathErr != nil {
		return "", f.lookPathErr
	}
	return "/usr/bin/" + file, nil
}

func (f *fakeCommandRunner) RunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	full := append([]string{name}, args...)
	f.calls = append(f.calls, full)
	if f.runHook != nil {
		return f.runHook(ctx, name, args...)
	}
	// By default simulate ffmpeg creating output file if it's the last arg
	if len(args) > 0 {
		outPath := args[len(args)-1]
		if strings.HasSuffix(outPath, ".mp4") {
			_ = os.WriteFile(outPath, []byte("fake valid mp4 output"), 0o600)
		}
	}
	return []byte("ok"), nil
}

func reviewedHTMLAnimationVariant(sessionID, collectionID, variantID string, eventSeq uint64, durationMs int64) pebblestore.SessionArtifactVariant {
	profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
	requirements, _ := artifact.ResolveOutputRequirements(&artifact.OutputRequirementsInput{Preset: "landscape_video"})
	return pebblestore.SessionArtifactVariant{
		ID: variantID, CollectionID: collectionID, SessionID: sessionID, EventSeq: eventSeq,
		Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html",
		Presentation: pebblestore.SessionArtifactPresentation{Width: htmlcapture.Width, Height: htmlcapture.Height},
		OutputRequirements: requirements, AnimationProfile: profile,
		Parts: []pebblestore.SessionArtifactPart{{ID: "full", Kind: "temporal", StartMs: 0, EndMs: durationMs}},
	}
}

func TestApplySelectedHTMLAnimationSourcesUsesLockedSourceUntilDerivativeReady(t *testing.T) {
	htmlRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	fallbackRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "fallback", VariantID: "still", EventSeq: 6}
	timeline := pebblestore.VideoProjectTimeline{
		Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: fallbackRef, MediaType: "image/png", DurationMs: 1000, SourceEndMs: 1000}},
		Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{{
			ID: "intro", DurationMs: 1000, Visual: fallbackRef, VisualMediaType: "image/png", AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{
				Candidates: []pebblestore.VideoAnimationCandidate{{ID: "a", Source: htmlRef}}, Status: pebblestore.VideoAnimationCandidateStatusAwaitingExport, SelectedCandidateID: "a", SelectedSource: htmlRef,
			},
		}}}},
	}
	if err := applySelectedHTMLAnimationSources(&timeline); err != nil {
		t.Fatalf("apply selected HTML source: %v", err)
	}
	clip := timeline.Clips[0]
	if clip.ArtifactRef == nil || *clip.ArtifactRef != *htmlRef || clip.MediaType != "text/html" || clip.SourceStartMs != 0 || clip.SourceEndMs != clip.DurationMs {
		t.Fatalf("selected HTML was not applied to exact render timeline: %+v", clip)
	}
}

func TestApplySelectedHTMLAnimationSourcesIgnoresPlanPartRemovedFromTimeline(t *testing.T) {
	htmlRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	fallbackRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "fallback", VariantID: "still", EventSeq: 6}
	timeline := pebblestore.VideoProjectTimeline{
		Clips: []pebblestore.VideoTimelineClip{{ID: "kept", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: fallbackRef, MediaType: "image/png", DurationMs: 1000, SourceEndMs: 1000}},
		Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{
			{ID: "kept", DurationMs: 1000, Visual: fallbackRef, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{Candidates: []pebblestore.VideoAnimationCandidate{{ID: "kept-a", Source: htmlRef}}, Status: pebblestore.VideoAnimationCandidateStatusAwaitingExport, SelectedCandidateID: "kept-a", SelectedSource: htmlRef}},
			{ID: "removed", DurationMs: 1000, Visual: fallbackRef, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{Candidates: []pebblestore.VideoAnimationCandidate{{ID: "one", Source: htmlRef}, {ID: "two", Source: fallbackRef}}, Status: pebblestore.VideoAnimationCandidateStatusAwaitingSelection}},
		}}},
	}
	if err := applySelectedHTMLAnimationSources(&timeline); err != nil {
		t.Fatalf("apply selected HTML sources with removed historical part: %v", err)
	}
	if clip := timeline.Clips[0]; clip.ArtifactRef == nil || *clip.ArtifactRef != *htmlRef || clip.MediaType != "text/html" {
		t.Fatalf("kept selected HTML source was not applied: %+v", clip)
	}
}

func TestRenderHTMLAnimationClipProducesMaterializedMP4(t *testing.T) {
	html := []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`)
	mp4 := append([]byte{0, 0, 0, 12}, []byte("ftypisom")...)
	renderer := &fakeAnimationRenderer{result: htmlcapture.AnimationResult{MP4: mp4, DurationMS: 1000, FPS: 30, FrameCount: 30}}
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	authority := &fakeArtifactAuthority{body: html, readVariant: variant}
	svc := NewService(Config{}, newFakeSessionStore(), authority, renderer, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	output, err := svc.renderHTMLAnimationClip(context.Background(), artifact.Principal{SessionID: "session", AccountScopeID: "acc", UserID: "user"}, ref, variant, 1000, t.TempDir(), 0)
	if err != nil {
		t.Fatalf("render HTML animation clip: %v", err)
	}
	body, err := os.ReadFile(output)
	if err != nil || string(body) != string(mp4) || len(renderer.requests) != 1 {
		t.Fatalf("materialized HTML animation output=%q requests=%d err=%v", body, len(renderer.requests), err)
	}
	info, statErr := os.Stat(output)
	if statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("materialized HTML animation mode=%v err=%v", info, statErr)
	}
}

func TestRenderHTMLAnimationClipReportsProgress(t *testing.T) {
	html := []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`)
	mp4 := append([]byte{0, 0, 0, 12}, []byte("ftypisom")...)
	renderer := &fakeAnimationRenderer{result: htmlcapture.AnimationResult{MP4: mp4, DurationMS: 1000, FPS: 30, FrameCount: 30}}
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	svc := NewService(Config{}, newFakeSessionStore(), &fakeArtifactAuthority{body: html, readVariant: variant}, renderer, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	var observed htmlcapture.AnimationProgress
	if _, err := svc.renderHTMLAnimationClip(context.Background(), artifact.Principal{SessionID: "session"}, ref, variant, 1000, t.TempDir(), 0, func(progress htmlcapture.AnimationProgress) { observed = progress }); err != nil {
		t.Fatalf("render HTML animation clip: %v", err)
	}
	if renderer.requests[0].Progress == nil {
		t.Fatal("render progress callback was not forwarded")
	}
	renderer.requests[0].Progress(htmlcapture.AnimationProgress{Stage: "frame_capture", Completed: 15, Total: 30})
	if observed.Stage != "frame_capture" || observed.Completed != 15 || observed.Total != 30 {
		t.Fatalf("observed progress = %+v", observed)
	}
}

func TestMaterializeTimelineInputsRejectsHTMLReferenceWithoutExplicitSession(t *testing.T) {
	store := newFakeSessionStore()
	store.variants["acc/session/motion/html"] = reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, &fakeAnimationRenderer{}, nil, &fakeCommandRunner{})
	ref := &pebblestore.SessionArtifactSelectionReference{CollectionID: "motion", VariantID: "html", EventSeq: 7}
	timeline := pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: ref, DurationMs: 1000, Visible: true}}}
	if _, err := svc.materializeTimelineInputs(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}, pebblestore.SessionSnapshot{ID: "session"}, "", t.TempDir(), timeline); err == nil || !strings.Contains(err.Error(), "all four exact reference fields") {
		t.Fatalf("incomplete HTML reference error = %v", err)
	}
}

func TestMaterializeTimelineInputsRejectsHTMLWithoutReviewedAuthority(t *testing.T) {
	store := newFakeSessionStore()
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	variant.AnimationProfile = nil
	store.variants["acc/session/motion/html"] = variant
	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, &fakeAnimationRenderer{}, nil, &fakeCommandRunner{})
	ref := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	timeline := pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: ref, DurationMs: 1000, Visible: true}}}
	if _, err := svc.materializeTimelineInputs(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}, pebblestore.SessionSnapshot{ID: "session"}, "", t.TempDir(), timeline); err == nil || !strings.Contains(err.Error(), "reviewed animation profile") {
		t.Fatalf("unreviewed HTML authority error = %v", err)
	}
}

func TestMaterializeTimelineInputsReusesOnlySameExactHTMLSource(t *testing.T) {
	html := []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`)
	mp4 := append([]byte{0, 0, 0, 12}, []byte("ftypisom")...)
	renderer := &fakeAnimationRenderer{result: htmlcapture.AnimationResult{MP4: mp4, DurationMS: 1000, FPS: 30, FrameCount: 30}}
	store := newFakeSessionStore()
	for _, id := range []string{"same", "other"} {
		store.variants["acc/session/motion/"+id] = reviewedHTMLAnimationVariant("session", "motion", id, 7, 1000)
	}
	authority := &fakeArtifactAuthority{body: html, readVariants: map[string]pebblestore.SessionArtifactVariant{
		"session/motion/same": store.variants["acc/session/motion/same"],
		"session/motion/other": store.variants["acc/session/motion/other"],
	}}
	svc := NewService(Config{}, store, authority, renderer, nil, &fakeCommandRunner{})
	ref := func(id string) *pebblestore.SessionArtifactSelectionReference {
		return &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: id, EventSeq: 7}
	}
	timeline := pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{
		{ID: "part-a", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: ref("same"), MediaType: "text/html", DurationMs: 1000, Visible: true},
		{ID: "part-b", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: ref("same"), MediaType: "text/html", DurationMs: 1000, Visible: true},
		{ID: "part-c", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: ref("other"), MediaType: "text/html", DurationMs: 1000, Visible: true},
	}}
	inputs, err := svc.materializeTimelineInputs(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}, pebblestore.SessionSnapshot{ID: "session"}, "", t.TempDir(), timeline)
	if err != nil {
		t.Fatal(err)
	}
	if len(renderer.requests) != 2 {
		t.Fatalf("HTML conversion count = %d, want one for repeated exact source plus one isolated source", len(renderer.requests))
	}
	if len(authority.readRefs) != 2 || !sameExactArtifactReference(&authority.readRefs[0], ref("same")) || !sameExactArtifactReference(&authority.readRefs[1], ref("other")) {
		t.Fatalf("HTML authority reads = %+v", authority.readRefs)
	}
	if len(inputs) != 3 || inputs[0].FilePath != inputs[1].FilePath || inputs[2].FilePath == inputs[0].FilePath {
		t.Fatalf("materialized HTML reuse/isolation = %+v", inputs)
	}
}

func TestRenderHTMLAnimationClipRejectsExactVariantMismatch(t *testing.T) {
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	svc := NewService(Config{}, newFakeSessionStore(), &fakeArtifactAuthority{}, &fakeAnimationRenderer{}, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "other", EventSeq: 7}
	if _, err := svc.renderHTMLAnimationClip(context.Background(), artifact.Principal{SessionID: "session"}, ref, variant, 1000, t.TempDir(), 0); err == nil || !strings.Contains(err.Error(), "exact authenticated variant") {
		t.Fatalf("exact variant mismatch error = %v", err)
	}
}

func TestRenderHTMLAnimationClipRequiresTrustedRenderer(t *testing.T) {
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	svc := NewService(Config{}, newFakeSessionStore(), &fakeArtifactAuthority{}, nil, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	if _, err := svc.renderHTMLAnimationClip(context.Background(), artifact.Principal{SessionID: "session"}, ref, variant, 1000, t.TempDir(), 0); err == nil || !strings.Contains(err.Error(), "trusted HTML-to-MP4 renderer") {
		t.Fatalf("missing renderer error = %v", err)
	}
}

func TestRenderHTMLAnimationClipHonorsProjectRenderByteLimit(t *testing.T) {
	html := []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`)
	mp4 := append([]byte{0, 0, 0, 12}, []byte("ftypisom")...)
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	svc := NewService(Config{MaxRenderBytes: 11}, newFakeSessionStore(), &fakeArtifactAuthority{body: html, readVariant: variant}, &fakeAnimationRenderer{result: htmlcapture.AnimationResult{MP4: mp4, DurationMS: 1000, FPS: 30, FrameCount: 30}}, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	if _, err := svc.renderHTMLAnimationClip(context.Background(), artifact.Principal{SessionID: "session"}, ref, variant, 1000, t.TempDir(), 0); err == nil || !strings.Contains(err.Error(), "invalid bounded MP4") {
		t.Fatalf("render byte limit error = %v", err)
	}
}

func TestRenderHTMLAnimationClipPropagatesCancellationAfterCapture(t *testing.T) {
	html := []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`)
	mp4 := append([]byte{0, 0, 0, 12}, []byte("ftypisom")...)
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	renderer := &fakeAnimationRenderer{result: htmlcapture.AnimationResult{MP4: mp4, DurationMS: 1000, FPS: 30, FrameCount: 30}, afterRender: cancel}
	svc := NewService(Config{}, newFakeSessionStore(), &fakeArtifactAuthority{body: html, readVariant: variant}, renderer, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	if _, err := svc.renderHTMLAnimationClip(ctx, artifact.Principal{SessionID: "session"}, ref, variant, 1000, t.TempDir(), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture cancellation error = %v", err)
	}
}

func TestRenderHTMLAnimationClipRejectsTemporalAuthorityMismatch(t *testing.T) {
	html := []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`)
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 900)
	svc := NewService(Config{}, newFakeSessionStore(), &fakeArtifactAuthority{body: html, readVariant: variant}, &fakeAnimationRenderer{}, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	if _, err := svc.renderHTMLAnimationClip(context.Background(), artifact.Principal{SessionID: "session"}, ref, variant, 1000, t.TempDir(), 0); err == nil || !strings.Contains(err.Error(), "temporal authority duration") {
		t.Fatalf("temporal authority mismatch error = %v", err)
	}
}

func TestRenderHTMLAnimationClipRejectsDurationMismatch(t *testing.T) {
	html := []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":900,"fps":30}</script>`)
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	svc := NewService(Config{}, newFakeSessionStore(), &fakeArtifactAuthority{body: html, readVariant: variant}, &fakeAnimationRenderer{}, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	if _, err := svc.renderHTMLAnimationClip(context.Background(), artifact.Principal{SessionID: "session"}, ref, variant, 1000, t.TempDir(), 0); err == nil || !strings.Contains(err.Error(), "does not match clip duration") {
		t.Fatalf("duration mismatch error = %v", err)
	}
}

func TestValidateHTMLAnimationAuthorityRequiresReviewedProfileOutputAndTemporalContract(t *testing.T) {
	valid := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	if err := validateHTMLAnimationAuthority(valid); err != nil {
		t.Fatalf("valid authority rejected: %v", err)
	}
	cases := []struct {
		name string
		edit func(*pebblestore.SessionArtifactVariant)
		want string
	}{
		{name: "profile", edit: func(v *pebblestore.SessionArtifactVariant) { v.AnimationProfile = nil }, want: "reviewed animation profile"},
		{name: "network", edit: func(v *pebblestore.SessionArtifactVariant) { v.AnimationProfile.Budgets.NetworkAllowed = true }, want: "canonical non-network"},
		{name: "output", edit: func(v *pebblestore.SessionArtifactVariant) { v.OutputRequirements.Width = 1280 }, want: "1920x1080"},
		{name: "runtime budgets", edit: func(v *pebblestore.SessionArtifactVariant) { v.AnimationProfile.Budgets.StopWhenDocumentHidden = false }, want: "canonical non-network"},
		{name: "temporal", edit: func(v *pebblestore.SessionArtifactVariant) { v.Parts = nil }, want: "temporal authority"},
		{name: "invalid temporal", edit: func(v *pebblestore.SessionArtifactVariant) { v.Parts[0].StartMs = v.Parts[0].EndMs }, want: "temporal authority"},
		{name: "presentation", edit: func(v *pebblestore.SessionArtifactVariant) { v.Presentation.Width = 1280 }, want: "presentation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
			tc.edit(&variant)
			if err := validateHTMLAnimationAuthority(variant); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("authority error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMaterializeTimelineInputsRejectsHTMLVariantIdentityMismatch(t *testing.T) {
	store := newFakeSessionStore()
	variant := reviewedHTMLAnimationVariant("other-session", "motion", "html", 7, 1000)
	store.variants["acc/session/motion/html"] = variant
	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, &fakeAnimationRenderer{}, nil, &fakeCommandRunner{})
	ref := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	timeline := pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: ref, DurationMs: 1000, Visible: true}}}
	if _, err := svc.materializeTimelineInputs(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}, pebblestore.SessionSnapshot{ID: "session"}, "", t.TempDir(), timeline); err == nil || !strings.Contains(err.Error(), "does not match the exact authenticated reference") {
		t.Fatalf("HTML identity mismatch error = %v", err)
	}
}

func TestReadHTMLAnimationSourceRejectsAuthorityByteMetadataMismatch(t *testing.T) {
	html := []byte("<!doctype html>")
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	authorityVariant := variant
	authorityVariant.Size = int64(len(html) + 1)
	svc := NewService(Config{}, newFakeSessionStore(), &fakeArtifactAuthority{body: html, readVariant: authorityVariant}, &fakeAnimationRenderer{}, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	if _, _, err := svc.readHTMLAnimationSource(context.Background(), artifact.Principal{SessionID: "session"}, ref, variant); err == nil || !strings.Contains(err.Error(), "inconsistent immutable metadata") {
		t.Fatalf("byte metadata mismatch error = %v", err)
	}
}

func TestReadHTMLAnimationSourceRejectsInvalidImmutableDigest(t *testing.T) {
	html := []byte("<!doctype html>")
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	variant.Size, variant.DigestSHA256 = int64(len(html)), strings.Repeat("a", 64)
	svc := NewService(Config{}, newFakeSessionStore(), &fakeArtifactAuthority{body: html, readVariant: variant}, &fakeAnimationRenderer{}, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	if _, _, err := svc.readHTMLAnimationSource(context.Background(), artifact.Principal{SessionID: "session"}, ref, variant); err == nil || !strings.Contains(err.Error(), "invalid immutable digest") {
		t.Fatalf("immutable digest error = %v", err)
	}
}

func TestReadHTMLAnimationSourceRejectsAuthorityIdentityMismatch(t *testing.T) {
	variant := reviewedHTMLAnimationVariant("session", "motion", "html", 7, 1000)
	authorityVariant := variant
	authorityVariant.EventSeq = 8
	svc := NewService(Config{}, newFakeSessionStore(), &fakeArtifactAuthority{body: []byte("<!doctype html>"), readVariant: authorityVariant}, &fakeAnimationRenderer{}, nil, &fakeCommandRunner{})
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	if _, _, err := svc.readHTMLAnimationSource(context.Background(), artifact.Principal{SessionID: "session"}, ref, variant); err == nil || !strings.Contains(err.Error(), "exact authenticated ready HTML revision") {
		t.Fatalf("identity mismatch error = %v", err)
	}
}

func TestApplySelectedHTMLAnimationSourcesBoundsFailureReason(t *testing.T) {
	htmlRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	timeline := pebblestore.VideoProjectTimeline{
		Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, DurationMs: 1000}},
		Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Parts: []pebblestore.VideoPlanPart{{ID: "intro", DurationMs: 1000, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{
			Candidates: []pebblestore.VideoAnimationCandidate{{ID: "chosen", Source: htmlRef}}, SelectedCandidateID: "chosen", SelectedSource: htmlRef, Status: pebblestore.VideoAnimationCandidateStatusFailed, FailureReason: strings.Repeat("x", 700),
		}}}}},
	}
	if err := applySelectedHTMLAnimationSources(&timeline); err == nil || len(err.Error()) > 600 {
		t.Fatalf("bounded failure reason error = %v", err)
	}
}

func TestApplySelectedHTMLAnimationSourcesRejectsUnsupportedStatus(t *testing.T) {
	htmlRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	timeline := pebblestore.VideoProjectTimeline{
		Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, DurationMs: 1000}},
		Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Parts: []pebblestore.VideoPlanPart{{ID: "intro", DurationMs: 1000, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{
			Candidates: []pebblestore.VideoAnimationCandidate{{ID: "chosen", Source: htmlRef}}, SelectedCandidateID: "chosen", SelectedSource: htmlRef, Status: "mystery",
		}}}}},
	}
	if err := applySelectedHTMLAnimationSources(&timeline); err == nil || !strings.Contains(err.Error(), "unsupported render status") {
		t.Fatalf("unsupported status error = %v", err)
	}
}

func TestApplySelectedHTMLAnimationSourcesRejectsPlanAndClipDurationMismatch(t *testing.T) {
	htmlRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	timeline := pebblestore.VideoProjectTimeline{
		Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, DurationMs: 900}},
		Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Parts: []pebblestore.VideoPlanPart{{ID: "intro", DurationMs: 1000, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{
			Candidates: []pebblestore.VideoAnimationCandidate{{ID: "chosen", Source: htmlRef}}, SelectedCandidateID: "chosen", SelectedSource: htmlRef, Status: pebblestore.VideoAnimationCandidateStatusAwaitingExport,
		}}}}},
	}
	if err := applySelectedHTMLAnimationSources(&timeline); err == nil || !strings.Contains(err.Error(), "does not match timeline clip duration") {
		t.Fatalf("plan/clip duration mismatch error = %v", err)
	}
}

func TestApplySelectedHTMLAnimationSourcesRejectsPendingMultiCandidateSelection(t *testing.T) {
	one := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "one", EventSeq: 7}
	two := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "two", EventSeq: 8}
	timeline := pebblestore.VideoProjectTimeline{
		Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, DurationMs: 1000}},
		Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Parts: []pebblestore.VideoPlanPart{{ID: "intro", DurationMs: 1000, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{
			Candidates: []pebblestore.VideoAnimationCandidate{{ID: "one", Source: one}, {ID: "two", Source: two}}, Status: pebblestore.VideoAnimationCandidateStatusAwaitingSelection,
		}}}}},
	}
	if err := applySelectedHTMLAnimationSources(&timeline); err == nil || !strings.Contains(err.Error(), "awaiting explicit candidate selection") {
		t.Fatalf("pending selection error = %v", err)
	}
}

func TestApplySelectedHTMLAnimationSourcesIgnoresReferenceDisplayMetadata(t *testing.T) {
	candidateRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "candidate", EventSeq: 7, Label: "Candidate"}
	selectedRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "candidate", EventSeq: 7, Label: "Selected display label"}
	timeline := pebblestore.VideoProjectTimeline{
		Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, DurationMs: 1000}},
		Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Parts: []pebblestore.VideoPlanPart{{ID: "intro", DurationMs: 1000, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{
			Candidates: []pebblestore.VideoAnimationCandidate{{ID: "chosen", Source: candidateRef}}, SelectedCandidateID: "chosen", SelectedSource: selectedRef, Status: pebblestore.VideoAnimationCandidateStatusAwaitingExport,
		}}}}},
	}
	if err := applySelectedHTMLAnimationSources(&timeline); err != nil {
		t.Fatalf("display metadata changed exact identity: %v", err)
	}
	if !sameExactArtifactReference(timeline.Clips[0].ArtifactRef, selectedRef) {
		t.Fatalf("selected exact source not applied: %+v", timeline.Clips[0].ArtifactRef)
	}
}

func TestApplySelectedHTMLAnimationSourcesRejectsMismatchedLockedSource(t *testing.T) {
	candidateRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "candidate", EventSeq: 7}
	otherRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "other", EventSeq: 8}
	timeline := pebblestore.VideoProjectTimeline{
		Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, DurationMs: 1000}},
		Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Parts: []pebblestore.VideoPlanPart{{ID: "intro", DurationMs: 1000, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{
			Candidates: []pebblestore.VideoAnimationCandidate{{ID: "chosen", Source: candidateRef}}, SelectedCandidateID: "chosen", SelectedSource: otherRef, Status: pebblestore.VideoAnimationCandidateStatusAwaitingExport,
		}}}}},
	}
	if err := applySelectedHTMLAnimationSources(&timeline); err == nil || !strings.Contains(err.Error(), "does not match its durably locked candidate") {
		t.Fatalf("locked source mismatch error = %v", err)
	}
}

func TestProbeInputHasAudioUsesFFprobeStreamResult(t *testing.T) {
	runner := &fakeCommandRunner{runHook: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ffprobe" {
			t.Fatalf("command = %q, want ffprobe", name)
		}
		return []byte("1\n"), nil
	}}
	hasAudio, err := probeInputHasAudio(context.Background(), runner, "clip.mkv")
	if err != nil {
		t.Fatalf("probeInputHasAudio() error = %v", err)
	}
	if !hasAudio {
		t.Fatal("probeInputHasAudio() = false, want true")
	}

	runner.runHook = func(_ context.Context, _ string, _ ...string) ([]byte, error) { return nil, nil }
	hasAudio, err = probeInputHasAudio(context.Background(), runner, "silent.mkv")
	if err != nil {
		t.Fatalf("probeInputHasAudio() silent error = %v", err)
	}
	if hasAudio {
		t.Fatal("probeInputHasAudio() = true for silent input")
	}
}

func TestProbeInputHasAudioRequiresFFprobe(t *testing.T) {
	_, err := probeInputHasAudio(context.Background(), &fakeCommandRunner{lookPathErr: errors.New("missing")}, "clip.mkv")
	if err == nil || !strings.Contains(err.Error(), "ffprobe is required") {
		t.Fatalf("probeInputHasAudio() error = %v, want ffprobe requirement", err)
	}
}

type fakeSessionStore struct {
	sessions      map[string]pebblestore.SessionSnapshot
	projects      map[string]pebblestore.VideoProjectSnapshot
	revisions     map[string]pebblestore.VideoProjectRevisionSnapshot
	jobs          map[string]pebblestore.VideoRenderJobSnapshot
	proposals     map[string]pebblestore.VideoEditProposalSnapshot
	sources       map[string]pebblestore.VideoSourceRecord
	audioSources  map[string]pebblestore.AudioSourceRecord
	variants      map[string]pebblestore.SessionArtifactVariant
	jobUpdates    []pebblestore.UpdateVideoRenderJobInput
	updateJobHook func(input pebblestore.UpdateVideoRenderJobInput)
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{
		sessions:     make(map[string]pebblestore.SessionSnapshot),
		projects:     make(map[string]pebblestore.VideoProjectSnapshot),
		revisions:    make(map[string]pebblestore.VideoProjectRevisionSnapshot),
		jobs:         make(map[string]pebblestore.VideoRenderJobSnapshot),
		proposals:    make(map[string]pebblestore.VideoEditProposalSnapshot),
		sources:      make(map[string]pebblestore.VideoSourceRecord),
		audioSources: make(map[string]pebblestore.AudioSourceRecord),
		variants:     make(map[string]pebblestore.SessionArtifactVariant),
	}
}

func (f *fakeSessionStore) GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error) {
	s, ok := f.sessions[sessionID]
	return s, ok, nil
}

func (f *fakeSessionStore) GetVideoProject(accountScopeID, sessionID, projectID string) (pebblestore.VideoProjectSnapshot, bool, error) {
	p, ok := f.projects[projectID]
	if !ok || p.AccountScopeID != accountScopeID || p.SessionID != sessionID {
		return pebblestore.VideoProjectSnapshot{}, false, nil
	}
	return p, true, nil
}

func (f *fakeSessionStore) GetVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID string) (pebblestore.VideoProjectRevisionSnapshot, bool, error) {
	r, ok := f.revisions[revisionID]
	if !ok || r.AccountScopeID != accountScopeID || r.SessionID != sessionID || r.ProjectID != projectID {
		return pebblestore.VideoProjectRevisionSnapshot{}, false, nil
	}
	return r, true, nil
}

func (f *fakeSessionStore) GetVideoEditProposal(accountScopeID, sessionID, projectID, proposalID string) (pebblestore.VideoEditProposalSnapshot, bool, error) {
	proposal, ok := f.proposals[proposalID]
	if !ok || proposal.AccountScopeID != accountScopeID || proposal.SessionID != sessionID || proposal.ProjectID != projectID {
		return pebblestore.VideoEditProposalSnapshot{}, false, nil
	}
	return proposal, true, nil
}

func (f *fakeSessionStore) ListVideoEditProposals(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoEditProposalSnapshot, error) {
	proposals := make([]pebblestore.VideoEditProposalSnapshot, 0, len(f.proposals))
	for _, proposal := range f.proposals {
		if proposal.AccountScopeID == accountScopeID && proposal.SessionID == sessionID && proposal.ProjectID == projectID {
			proposals = append(proposals, proposal)
		}
	}
	if limit > 0 && len(proposals) > limit {
		proposals = proposals[:limit]
	}
	return proposals, nil
}

func (f *fakeSessionStore) GetVideoRenderJob(accountScopeID, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error) {
	j, ok := f.jobs[jobID]
	if !ok || j.AccountScopeID != accountScopeID || j.SessionID != sessionID {
		return pebblestore.VideoRenderJobSnapshot{}, false, nil
	}
	return j, true, nil
}

func (f *fakeSessionStore) UpdateVideoRenderJob(input pebblestore.UpdateVideoRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error) {
	f.jobUpdates = append(f.jobUpdates, input)
	if f.updateJobHook != nil {
		f.updateJobHook(input)
	}
	j, ok := f.jobs[input.JobID]
	if !ok {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("job not found")
	}
	if input.ExpectedStatus != "" && j.Status != input.ExpectedStatus {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("render job status conflict: expected %s, actual %s", input.ExpectedStatus, j.Status)
	}
	if input.Status != "" {
		j.Status = input.Status
	}
	if input.Progress > j.Progress {
		j.Progress = input.Progress
	}
	if input.ProgressStage != "" {
		j.ProgressStage = input.ProgressStage
	}
	if input.FailureCode != "" {
		j.FailureCode = input.FailureCode
	}
	if input.FailureReason != "" {
		j.FailureReason = input.FailureReason
	}
	if input.OutputPreset != "" {
		j.OutputPreset = input.OutputPreset
	}
	if input.OutputWidth > 0 {
		j.OutputWidth = input.OutputWidth
	}
	if input.OutputHeight > 0 {
		j.OutputHeight = input.OutputHeight
	}
	if input.OutputFPS > 0 {
		j.OutputFPS = input.OutputFPS
	}
	if input.OutputDurationMs > 0 {
		j.OutputDurationMs = input.OutputDurationMs
	}
	if input.OutputSizeBytes > 0 {
		j.OutputSizeBytes = input.OutputSizeBytes
	}
	if input.OutputDigestSHA256 != "" {
		j.OutputDigestSHA256 = input.OutputDigestSHA256
	}
	if input.OutputArtifact != nil {
		j.OutputArtifact = input.OutputArtifact
	}
	j.UpdatedAt = input.NowUnixMs
	f.jobs[input.JobID] = j
	return j, nil
}

func (f *fakeSessionStore) GetVideoSourceRecord(accountScopeID, workspaceID, ref string) (pebblestore.VideoSourceRecord, bool, error) {
	s, ok := f.sources[ref]
	if !ok || s.AccountScopeID != accountScopeID {
		return pebblestore.VideoSourceRecord{}, false, nil
	}
	return s, true, nil
}

func (f *fakeSessionStore) GetAudioSourceRecord(accountScopeID, workspaceID, ref string) (pebblestore.AudioSourceRecord, bool, error) {
	s, ok := f.audioSources[ref]
	if !ok || s.AccountScopeID != accountScopeID || s.WorkspaceID != workspaceID {
		return pebblestore.AudioSourceRecord{}, false, nil
	}
	return s, true, nil
}

func (f *fakeSessionStore) GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error) {
	key := fmt.Sprintf("%s/%s/%s/%s", accountScopeID, sessionID, collectionID, variantID)
	v, ok := f.variants[key]
	return v, ok, nil
}

func (f *fakeSessionStore) ListRecoverableVideoRenderJobs(limit int) ([]pebblestore.VideoRenderJobSnapshot, error) {
	var list []pebblestore.VideoRenderJobSnapshot
	for _, job := range f.jobs {
		if job.Status == pebblestore.VideoRenderJobStatusQueued || job.Status == pebblestore.VideoRenderJobStatusRendering {
			list = append(list, job)
			if len(list) == limit {
				break
			}
		}
	}
	return list, nil
}

func (f *fakeSessionStore) ListVideoRenderJobs(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoRenderJobSnapshot, error) {
	var list []pebblestore.VideoRenderJobSnapshot
	for _, j := range f.jobs {
		if j.AccountScopeID == accountScopeID && j.SessionID == sessionID && (projectID == "" || j.ProjectID == projectID) {
			list = append(list, j)
		}
	}
	return list, nil
}

type fakeArtifactAuthority struct {
	createdVariants []pebblestore.SessionArtifactVariant
	createInputs    []artifact.CreateFileInput
	createErr       error
	body            []byte
	readVariant     pebblestore.SessionArtifactVariant
	readVariants    map[string]pebblestore.SessionArtifactVariant
	readRefs        []pebblestore.SessionArtifactSelectionReference
}

type fakeAnimationRenderer struct {
	requests    []htmlcapture.AnimationRequest
	result      htmlcapture.AnimationResult
	err         error
	afterRender func()
}

func (f *fakeAnimationRenderer) PreflightAnimation(context.Context, htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	return f.result, f.err
}

func (f *fakeAnimationRenderer) RenderAnimation(_ context.Context, req htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	f.requests = append(f.requests, req)
	if req.Progress != nil && f.result.FrameCount > 0 {
		req.Progress(htmlcapture.AnimationProgress{Stage: "frame_capture", Completed: f.result.FrameCount, Total: f.result.FrameCount})
	}
	if f.afterRender != nil {
		f.afterRender()
	}
	return f.result, f.err
}

func (f *fakeArtifactAuthority) GetReference(principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error) {
	if exact, ok := f.readVariants[ref.SessionID+"/"+ref.CollectionID+"/"+ref.VariantID]; ok {
		return exact, nil
	}
	if f.readVariant.ID != "" {
		return f.readVariant, nil
	}
	return pebblestore.SessionArtifactVariant{
		ID:           ref.VariantID,
		CollectionID: ref.CollectionID,
		Status:       pebblestore.SessionArtifactStatusReady,
	}, nil
}

func (f *fakeArtifactAuthority) ReadReference(_ context.Context, _ artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, _ int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	f.readRefs = append(f.readRefs, ref)
	if f.body != nil {
		variant := f.readVariant
		if exact, ok := f.readVariants[ref.SessionID+"/"+ref.CollectionID+"/"+ref.VariantID]; ok {
			variant = exact
		}
		return append([]byte(nil), f.body...), variant, nil
	}
	return []byte("fake artifact content"), pebblestore.SessionArtifactVariant{}, nil
}

func (f *fakeArtifactAuthority) CreateFromFile(ctx context.Context, principal artifact.Principal, input artifact.CreateFileInput) (pebblestore.SessionArtifactVariant, error) {
	f.createInputs = append(f.createInputs, input)
	if f.createErr != nil {
		return pebblestore.SessionArtifactVariant{}, f.createErr
	}
	v := pebblestore.SessionArtifactVariant{
		ID:           input.VariantID,
		CollectionID: input.CollectionID,
		Filename:     input.Filename,
		MediaType:    input.MediaType,
		Status:       pebblestore.SessionArtifactStatusReady,
		Presentation: input.Presentation,
		EventSeq:     42,
		Size:         1024,
	}
	f.createdVariants = append(f.createdVariants, v)
	return v, nil
}

func testVideoFingerprint(root, relative string, size, modAt int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", root, relative, size, modAt)))
	return hex.EncodeToString(sum[:])
}

func testAudioSourceRecord(accountScopeID, workspaceID, root, relative string, size, modAt int64) pebblestore.AudioSourceRecord {
	fingerprint := testVideoFingerprint(root, relative, size, modAt)
	sum := sha256.Sum256([]byte(strings.Join([]string{accountScopeID, workspaceID, root, relative, fingerprint}, "\x00")))
	return pebblestore.AudioSourceRecord{
		Version: pebblestore.AudioSourceRecordVersion, Ref: "audiosrc_" + hex.EncodeToString(sum[:]),
		AccountScopeID: accountScopeID, WorkspaceID: workspaceID, RootPath: root, RelativePath: relative,
		DisplayName: filepath.Base(relative), MIMEType: "audio/wav", SizeBytes: size, ModifiedAt: modAt,
		SourceFingerprint: fingerprint, FingerprintVersion: pebblestore.AudioSourceFingerprintV1,
	}
}

type fakeWorkspaceAuthority struct {
	resolution workspaceruntime.Resolution
}

func (f fakeWorkspaceAuthority) ListSourceMediaDirectoriesForPrincipal(identity.Principal, string) (workspaceruntime.Resolution, error) {
	return f.resolution, nil
}

func TestMaterializeTimelineInputsResolvesExactAudioSource(t *testing.T) {
	root := t.TempDir()
	audioPath := filepath.Join(root, "soundtrack.wav")
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)
	if err := os.WriteFile(audioPath, wav, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	record := testAudioSourceRecord(principal.AccountScopeID, "ws", root, "soundtrack.wav", info.Size(), info.ModTime().UnixMilli())
	store := newFakeSessionStore()
	store.audioSources[record.Ref] = record
	svc := NewService(Config{}, store, nil, nil, fakeWorkspaceAuthority{resolution: workspaceruntime.Resolution{
		WorkspaceID: "ws", SourceMediaDirectories: []string{root},
	}}, &fakeCommandRunner{})
	timeline := pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{
		ID: "music", SourceKind: pebblestore.VideoClipSourceKindSourceAudio, AudioSource: &pebblestore.AudioSourceReference{
			Ref: record.Ref, Name: record.DisplayName, MIMEType: record.MIMEType, SizeBytes: record.SizeBytes,
			SourceFingerprint: record.SourceFingerprint, FingerprintVersion: record.FingerprintVersion,
		}, SourceStartMs: 0, SourceEndMs: 1000, TimelineStartMs: 500, TimelineEndMs: 1500, DurationMs: 1000,
	}}}
	inputs, err := svc.materializeTimelineInputs(context.Background(), principal, pebblestore.SessionSnapshot{ID: "session"}, "/workspace", t.TempDir(), timeline)
	if err != nil {
		t.Fatalf("materializeTimelineInputs() error = %v", err)
	}
	if len(inputs) != 1 || !inputs[0].IsAudio || inputs[0].IsVideo || !inputs[0].HasAudio {
		t.Fatalf("materialized input = %+v", inputs)
	}
	if data, err := os.ReadFile(inputs[0].FilePath); err != nil || string(data) != string(wav) {
		t.Fatalf("materialized audio mismatch: bytes=%d err=%v", len(data), err)
	}
}

func TestRenderJobSuccessfulFlow(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "swarm-render-test-")
	if err != nil {
		t.Fatalf("temp dir err: %v", err)
	}
	defer os.RemoveAll(tempDir)

	principal := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		AccountScopeID: "acc_1",
		UserID:         "usr_1",
	}
	sessionID := "sess_1"
	projectID := "vproj_1"
	revID := "vrev_1"
	jobID := "vjob_1"

	store := newFakeSessionStore()
	store.sessions[sessionID] = pebblestore.SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		Metadata: map[string]any{
			"workspace_id": "ws_1",
		},
	}
	store.projects[projectID] = pebblestore.VideoProjectSnapshot{
		ID:                projectID,
		AccountScopeID:    principal.AccountScopeID,
		UserID:            principal.UserID,
		SessionID:         sessionID,
		Title:             "Test Intro Video",
		CurrentRevisionID: revID,
	}

	// Create a dummy video file in tempDir
	srcFilePath := filepath.Join(tempDir, "source.mp4")
	_ = os.WriteFile(srcFilePath, []byte("ftypisomfakevideodata"), 0o600)
	srcStat, _ := os.Stat(srcFilePath)

	srcRef := "videosrc_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	store.sources[srcRef] = pebblestore.VideoSourceRecord{
		Ref:               srcRef,
		AccountScopeID:    principal.AccountScopeID,
		WorkspaceID:       "ws_1",
		RootPath:          tempDir,
		RelativePath:      "source.mp4",
		DisplayName:       "source.mp4",
		MIMEType:          "video/mp4",
		SizeBytes:         srcStat.Size(),
		ModifiedAt:        srcStat.ModTime().UnixMilli(),
		SourceFingerprint: testVideoFingerprint(tempDir, "source.mp4", srcStat.Size(), srcStat.ModTime().UnixMilli()),
	}

	// Managed artifact variant
	artVariantKey := fmt.Sprintf("%s/%s/col_intro/var_intro_1", principal.AccountScopeID, sessionID)
	store.variants[artVariantKey] = pebblestore.SessionArtifactVariant{
		ID:           "var_intro_1",
		CollectionID: "col_intro",
		Filename:     "intro_card.png",
		MediaType:    "image/png",
		Status:       pebblestore.SessionArtifactStatusReady,
		EventSeq:     7,
		Size:         500,
	}

	store.revisions[revID] = pebblestore.VideoProjectRevisionSnapshot{
		ID:             revID,
		ProjectID:      projectID,
		RevisionNumber: 1,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      sessionID,
		EventSeq:       11,
		Timeline: pebblestore.VideoProjectTimeline{
			OutputPreset: pebblestore.VideoPresetLandscape1080p,
			FPS:          30,
			Clips: []pebblestore.VideoTimelineClip{
				{
					ID:         "clip_art",
					Sequence:   0,
					SourceKind: pebblestore.VideoClipSourceKindManagedArtifact,
					ArtifactRef: &pebblestore.SessionArtifactSelectionReference{
						SessionID:    sessionID,
						CollectionID: "col_intro",
						VariantID:    "var_intro_1",
						EventSeq:     7,
					},
					DurationMs: 2000,
					Visible:    true,
				},
				{
					ID:         "clip_src",
					Sequence:   1,
					SourceKind: pebblestore.VideoClipSourceKindSourceVideo,
					SourceRef:  srcRef,
					DurationMs: 4000,
					Visible:    true,
					Captions: []pebblestore.VideoTextOverlay{
						{
							Text:     "First chapter",
							Position: "bottom",
						},
					},
				},
			},
		},
	}

	store.jobs[jobID] = pebblestore.VideoRenderJobSnapshot{
		ID:             jobID,
		ProjectID:      projectID,
		RevisionID:     revID,
		RevisionNumber: 1,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      sessionID,
		Status:         pebblestore.VideoRenderJobStatusQueued,
	}

	runner := &fakeCommandRunner{}
	artAuth := &fakeArtifactAuthority{}

	svc := NewService(Config{}, store, artAuth, nil, nil, runner)

	ctx := context.Background()
	result, err := svc.RenderJob(ctx, principal, RenderJobRequest{
		SessionID:  sessionID,
		ProjectID:  projectID,
		RevisionID: revID,
		JobID:      jobID,
	})
	if err != nil {
		t.Fatalf("render job failed: %v", err)
	}

	if result.Status != pebblestore.VideoRenderJobStatusReady {
		t.Fatalf("expected job status ready, got: %s", result.Status)
	}
	if result.Progress != 1.0 {
		t.Fatalf("expected progress 1.0, got: %f", result.Progress)
	}
	if result.OutputWidth != 1920 || result.OutputHeight != 1080 {
		t.Fatalf("expected 1920x1080 output dimensions, got %dx%d", result.OutputWidth, result.OutputHeight)
	}
	if result.OutputArtifact == nil || result.OutputArtifact.VariantID == "" {
		t.Fatalf("expected non-nil output artifact reference, got: %+v", result.OutputArtifact)
	}
	if len(artAuth.createInputs) != 1 || artAuth.createInputs[0].VideoProjectID != projectID || artAuth.createInputs[0].VideoRevisionID != revID || artAuth.createInputs[0].VideoRevisionEventSeq != store.revisions[revID].EventSeq {
		t.Fatalf("render artifact lineage = %+v", artAuth.createInputs)
	}
	var ffmpegCalls int
	for _, call := range runner.calls {
		if len(call) > 0 && call[0] == "ffmpeg" {
			ffmpegCalls++
		}
	}
	if ffmpegCalls != 1 {
		t.Fatalf("expected 1 ffmpeg call, got: %d (%v)", ffmpegCalls, runner.calls)
	}
}

func TestRenderJobRejectsPendingWorkingRevisionBeforeMaterialization(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	const sessionID, projectID, revisionID, jobID = "session", "project", "working", "job"
	store := newFakeSessionStore()
	store.sessions[sessionID] = pebblestore.SessionSnapshot{ID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
	store.projects[projectID] = pebblestore.VideoProjectSnapshot{ID: projectID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, CurrentRevisionID: revisionID}
	store.revisions[revisionID] = pebblestore.VideoProjectRevisionSnapshot{ID: revisionID, ProjectID: projectID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "visual", SourceKind: pebblestore.VideoClipSourceKindColor, DurationMs: 1000, TimelineEndMs: 1000, Visible: true}}}}
	store.proposals["pending"] = pebblestore.VideoEditProposalSnapshot{ID: "pending", ProjectID: projectID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, WorkingRevisionID: revisionID, Status: pebblestore.VideoEditProposalStatusPending}
	store.jobs[jobID] = pebblestore.VideoRenderJobSnapshot{ID: jobID, ProjectID: projectID, RevisionID: revisionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, Status: pebblestore.VideoRenderJobStatusQueued}
	runner := &fakeCommandRunner{}
	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, nil, nil, runner)

	_, err := svc.RenderJob(context.Background(), principal, RenderJobRequest{SessionID: sessionID, ProjectID: projectID, RevisionID: revisionID, JobID: jobID})
	if err == nil || !strings.Contains(err.Error(), "pending working cut") || !strings.Contains(err.Error(), "confirm or reject") {
		t.Fatalf("pending working render error = %v", err)
	}
	if len(runner.calls) != 0 || len(store.jobUpdates) != 0 {
		t.Fatalf("pending render performed work: calls=%v updates=%v", runner.calls, store.jobUpdates)
	}
}

func TestRenderJobCapturesAcceptedHTMLAndComposesRegisteredSoundtrackInOneJob(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	const sessionID, projectID, revisionID, jobID = "session", "project", "revision", "job"
	tempDir := t.TempDir()
	audioPath := filepath.Join(tempDir, "soundtrack.wav")
	if err := os.WriteFile(audioPath, append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...), 0o600); err != nil {
		t.Fatal(err)
	}
	audioInfo, err := os.Stat(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	audio := testAudioSourceRecord(principal.AccountScopeID, "ws", tempDir, "soundtrack.wav", audioInfo.Size(), audioInfo.ModTime().UnixMilli())
	htmlRef := &pebblestore.SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: "motion", VariantID: "html", EventSeq: 7}
	fallbackRef := &pebblestore.SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: "fallback", VariantID: "png", EventSeq: 6}
	htmlVariant := reviewedHTMLAnimationVariant(sessionID, "motion", "html", 7, 1000)
	htmlVariant.AccountScopeID = principal.AccountScopeID
	store := newFakeSessionStore()
	store.sessions[sessionID] = pebblestore.SessionSnapshot{ID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Metadata: map[string]any{"workspace_id": "ws"}}
	store.projects[projectID] = pebblestore.VideoProjectSnapshot{ID: projectID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, CurrentRevisionID: revisionID, Title: "HTML soundtrack"}
	store.audioSources[audio.Ref] = audio
	store.variants["acc/session/motion/html"] = htmlVariant
	store.revisions[revisionID] = pebblestore.VideoProjectRevisionSnapshot{
		ID: revisionID, ProjectID: projectID, RevisionNumber: 1, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, EventSeq: 12,
		Timeline: pebblestore.VideoProjectTimeline{Width: 1920, Height: 1080, FPS: 30, TotalDurationMs: 1000,
			Clips: []pebblestore.VideoTimelineClip{
				{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: fallbackRef, MediaType: "image/png", DurationMs: 1000, TimelineEndMs: 1000, Visible: true},
				{ID: "soundtrack", SourceKind: pebblestore.VideoClipSourceKindSourceAudio, AudioSource: &pebblestore.AudioSourceReference{Ref: audio.Ref, Name: audio.DisplayName, MIMEType: audio.MIMEType, SizeBytes: audio.SizeBytes, SourceFingerprint: audio.SourceFingerprint, FingerprintVersion: audio.FingerprintVersion}, SourceEndMs: 1000, DurationMs: 1000, Track: 1, TimelineEndMs: 1000},
			},
			Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Parts: []pebblestore.VideoPlanPart{{ID: "intro", DurationMs: 1000, Visual: fallbackRef, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{
				Candidates: []pebblestore.VideoAnimationCandidate{{ID: "chosen", Source: htmlRef}}, SelectedCandidateID: "chosen", SelectedSource: htmlRef, Status: pebblestore.VideoAnimationCandidateStatusAwaitingExport,
			}}}}},
		},
	}
	store.jobs[jobID] = pebblestore.VideoRenderJobSnapshot{ID: jobID, ProjectID: projectID, RevisionID: revisionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, Status: pebblestore.VideoRenderJobStatusQueued}
	html := []byte(`<!doctype html><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":1000,"fps":30}</script>`)
	mp4 := append([]byte{0, 0, 0, 12}, []byte("ftypisom")...)
	renderer := &fakeAnimationRenderer{result: htmlcapture.AnimationResult{MP4: mp4, DurationMS: 1000, FPS: 30, FrameCount: 30}}
	authority := &fakeArtifactAuthority{body: html, readVariant: htmlVariant}
	runner := &fakeCommandRunner{}
	svc := NewService(Config{WorkDir: tempDir}, store, authority, renderer, nil, runner)
	result, err := svc.RenderJob(context.Background(), principal, RenderJobRequest{SessionID: sessionID, ProjectID: projectID, RevisionID: revisionID, JobID: jobID})
	if err != nil {
		t.Fatalf("RenderJob() error = %v", err)
	}
	if got := store.revisions[revisionID].Timeline.Clips[0].ArtifactRef; got == nil || !sameExactArtifactReference(got, fallbackRef) {
		t.Fatalf("render mutated durable fallback clip authority: %+v", got)
	}
	if len(authority.readRefs) != 1 || !sameExactArtifactReference(&authority.readRefs[0], htmlRef) {
		t.Fatalf("exact selected HTML reads = %+v", authority.readRefs)
	}
	if result.Status != pebblestore.VideoRenderJobStatusReady || len(renderer.requests) != 1 || len(authority.createInputs) != 1 {
		t.Fatalf("one-shot render result=%+v captures=%d publications=%d", result, len(renderer.requests), len(authority.createInputs))
	}
	if len(authority.createdVariants) != 1 || len(authority.createdVariants) != len(authority.createInputs) {
		t.Fatalf("unexpected prerequisite artifact publication: %+v", authority.createdVariants)
	}
	var stages []string
	for _, update := range store.jobUpdates {
		stages = append(stages, update.ProgressStage)
	}
	if !strings.Contains(strings.Join(stages, "|"), "Capturing HTML animation frames") || !strings.Contains(strings.Join(stages, "|"), "Composing project timeline") {
		t.Fatalf("one-shot render progress stages = %v", stages)
	}
	var ffmpegCalls int
	for _, call := range runner.calls {
		if len(call) > 0 && call[0] == "ffmpeg" {
			ffmpegCalls++
			joined := strings.Join(call, " ")
			if !strings.Contains(joined, "input_0_html-animation.mp4") || !strings.Contains(joined, "input_1_soundtrack.wav") || !strings.Contains(joined, "amix=inputs=2") {
				t.Fatalf("final project command does not compose HTML capture and soundtrack: %s", joined)
			}
		}
	}
	if ffmpegCalls != 1 {
		t.Fatalf("final project ffmpeg calls = %d, want exactly one composition", ffmpegCalls)
	}
	published := authority.createInputs[0]
	if published.MediaType != "video/mp4" || published.VideoProjectID != projectID || published.VideoRevisionID != revisionID || published.VideoRevisionEventSeq != 12 || published.OutputRequirements == nil || published.OutputRequirements.Width != 1920 || published.OutputRequirements.Height != 1080 {
		t.Fatalf("final artifact lineage/output authority = %+v", published)
	}
}

func TestRenderJobSecurityAndRejections(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_alpha", UserID: "usr_alpha"}
	otherPrincipal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_beta", UserID: "usr_beta"}

	store := newFakeSessionStore()
	store.sessions["sess_alpha"] = pebblestore.SessionSnapshot{
		ID:             "sess_alpha",
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
	}
	store.jobs["job_1"] = pebblestore.VideoRenderJobSnapshot{
		ID:             "job_1",
		AccountScopeID: principal.AccountScopeID,
		SessionID:      "sess_alpha",
		ProjectID:      "proj_1",
		Status:         pebblestore.VideoRenderJobStatusQueued,
	}

	runner := &fakeCommandRunner{}
	artAuth := &fakeArtifactAuthority{}
	svc := NewService(Config{}, store, artAuth, nil, nil, runner)
	ctx := context.Background()

	// 1. Cross-account rejected
	_, err := svc.RenderJob(ctx, otherPrincipal, RenderJobRequest{
		SessionID: "sess_alpha",
		ProjectID: "proj_1",
		JobID:     "job_1",
	})
	if err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("expected ownership rejection for other principal, got: %v", err)
	}

	// 2. Missing principal
	_, err = svc.RenderJob(ctx, identity.Principal{}, RenderJobRequest{
		SessionID: "sess_alpha",
		ProjectID: "proj_1",
		JobID:     "job_1",
	})
	if err == nil || !strings.Contains(err.Error(), "authenticated principal") {
		t.Fatalf("expected authenticated principal required, got: %v", err)
	}

	// 3. Nonexistent job
	_, err = svc.RenderJob(ctx, principal, RenderJobRequest{
		SessionID: "sess_alpha",
		ProjectID: "proj_1",
		JobID:     "nonexistent_job",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found for nonexistent job, got: %v", err)
	}
}

func TestRenderJobRejectsRevisionDifferentFromPinnedJob(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	store := newFakeSessionStore()
	store.sessions["sess_1"] = pebblestore.SessionSnapshot{ID: "sess_1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
	store.projects["proj_1"] = pebblestore.VideoProjectSnapshot{ID: "proj_1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "sess_1", CurrentRevisionID: "rev_new"}
	store.jobs["job_1"] = pebblestore.VideoRenderJobSnapshot{ID: "job_1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_pinned", Status: pebblestore.VideoRenderJobStatusQueued}

	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, nil, nil, &fakeCommandRunner{})
	_, err := svc.RenderJob(context.Background(), principal, RenderJobRequest{
		SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_new", JobID: "job_1",
	})
	if err == nil || !strings.Contains(err.Error(), `pinned to revision "rev_pinned"`) {
		t.Fatalf("RenderJob() error = %v, want pinned revision rejection", err)
	}
	if got := store.jobs["job_1"].Status; got != pebblestore.VideoRenderJobStatusQueued {
		t.Fatalf("job status = %s, want queued after rejected revision override", got)
	}
}

func TestRenderJobProgressIsMonotonicAndNamesStages(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store := newFakeSessionStore()
	store.jobs["job"] = pebblestore.VideoRenderJobSnapshot{ID: "job", AccountScopeID: "acc", UserID: "user", SessionID: "session", Status: pebblestore.VideoRenderJobStatusRendering, Progress: 0.20}
	svc := NewService(Config{}, store, nil, nil, nil, nil)
	svc.updateProgress(principal, "session", "job", 0.15, "old")
	if store.jobs["job"].Progress != 0.20 {
		t.Fatalf("progress regressed to %v", store.jobs["job"].Progress)
	}
	svc.updateProgress(principal, "session", "job", 0.25, "Capturing HTML animation frames")
	if store.jobs["job"].Progress != 0.25 || store.jobs["job"].ProgressStage != "Capturing HTML animation frames" {
		t.Fatalf("progress update = %+v", store.jobs["job"])
	}
}

func TestApplySelectedHTMLAnimationSourcesUsesExactReadyDerivativeWithoutCapture(t *testing.T) {
	htmlRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion", VariantID: "html", EventSeq: 7}
	derivativeRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "motion-render", VariantID: "mp4", EventSeq: 9}
	fallbackRef := &pebblestore.SessionArtifactSelectionReference{SessionID: "session", CollectionID: "fallback", VariantID: "still", EventSeq: 6}
	timeline := pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactRef: fallbackRef, MediaType: "image/png", DurationMs: 1000}}, Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Parts: []pebblestore.VideoPlanPart{{ID: "intro", DurationMs: 1000, AnimationCandidates: &pebblestore.VideoAnimationCandidateSet{Candidates: []pebblestore.VideoAnimationCandidate{{ID: "a", Source: htmlRef}}, Status: pebblestore.VideoAnimationCandidateStatusReady, SelectedCandidateID: "a", SelectedSource: htmlRef, Derivative: derivativeRef}}}}}}
	if err := applySelectedHTMLAnimationSources(&timeline); err != nil {
		t.Fatal(err)
	}
	if timeline.Clips[0].ArtifactRef == nil || *timeline.Clips[0].ArtifactRef != *derivativeRef || timeline.Clips[0].MediaType != "video/mp4" {
		t.Fatalf("ready derivative not reused: %+v", timeline.Clips[0])
	}
}

func TestRenderFailureCodesPreserveSafeSpecificAnimationAndRevisionReasons(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{htmlcapture.NewError("animation_encode_failed", "trusted MP4 encoder failed"), "animation_encode_failed"},
		{errors.New("selected HTML animation requires the trusted HTML-to-MP4 renderer, but it is unavailable"), "animation_renderer_unavailable"},
		{errors.New(`render job "job" is pinned to revision "old"`), "stale_pinned_revision"},
		{errors.New(`video project revision "missing" not found`), "invalid_pinned_revision"},
	}
	for _, tc := range cases {
		if got := renderFailureCode(tc.err, "fallback"); got != tc.want {
			t.Fatalf("renderFailureCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	long := errors.New(strings.Repeat("x", 700))
	if reason := renderFailureReason(long); len(reason) != 512 {
		t.Fatalf("safe reason length = %d", len(reason))
	}
}

func TestRenderJobTimeoutBecomesNamedFailure(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store := newFakeSessionStore()
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: "acc", UserID: "user", SessionID: "session", CurrentRevisionID: "revision"}
	store.revisions["revision"] = pebblestore.VideoProjectRevisionSnapshot{ID: "revision", ProjectID: "project", AccountScopeID: "acc", UserID: "user", SessionID: "session", Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "color", SourceKind: pebblestore.VideoClipSourceKindColor, DurationMs: 1000, Visible: true}}}}
	store.jobs["job"] = pebblestore.VideoRenderJobSnapshot{ID: "job", ProjectID: "project", RevisionID: "revision", AccountScopeID: "acc", UserID: "user", SessionID: "session", Status: pebblestore.VideoRenderJobStatusQueued}
	runner := &fakeCommandRunner{runHook: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, nil, nil, runner)
	if _, err := svc.RenderJob(context.Background(), principal, RenderJobRequest{SessionID: "session", ProjectID: "project", RevisionID: "revision", JobID: "job", Timeout: time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RenderJob timeout error = %v", err)
	}
	job := store.jobs["job"]
	if job.Status != pebblestore.VideoRenderJobStatusFailed || job.FailureCode != "render_timeout" || job.ProgressStage != "Render failed" {
		t.Fatalf("timeout terminal job = %+v", job)
	}
}

func TestRenderJobFailureHandling(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	sessionID := "sess_fail"
	projectID := "proj_fail"
	revID := "rev_fail"
	jobID := "job_fail"

	store := newFakeSessionStore()
	store.sessions[sessionID] = pebblestore.SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
	}
	store.projects[projectID] = pebblestore.VideoProjectSnapshot{
		ID:                projectID,
		AccountScopeID:    principal.AccountScopeID,
		SessionID:         sessionID,
		CurrentRevisionID: revID,
	}
	store.revisions[revID] = pebblestore.VideoProjectRevisionSnapshot{
		ID:             revID,
		ProjectID:      projectID,
		AccountScopeID: principal.AccountScopeID,
		SessionID:      sessionID,
		Timeline: pebblestore.VideoProjectTimeline{
			Clips: []pebblestore.VideoTimelineClip{
				{
					ID:         "c1",
					SourceKind: pebblestore.VideoClipSourceKindColor,
					DurationMs: 1000,
					Visible:    true,
				},
			},
		},
	}
	store.jobs[jobID] = pebblestore.VideoRenderJobSnapshot{
		ID:             jobID,
		ProjectID:      projectID,
		RevisionID:     revID,
		AccountScopeID: principal.AccountScopeID,
		SessionID:      sessionID,
		Status:         pebblestore.VideoRenderJobStatusQueued,
	}

	// Command runner fails
	runner := &fakeCommandRunner{
		runHook: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("simulated ffmpeg error: invalid codec")
		},
	}
	artAuth := &fakeArtifactAuthority{}
	svc := NewService(Config{}, store, artAuth, nil, nil, runner)

	_, err := svc.RenderJob(context.Background(), principal, RenderJobRequest{
		SessionID:  sessionID,
		ProjectID:  projectID,
		RevisionID: revID,
		JobID:      jobID,
	})
	if err == nil {
		t.Fatalf("expected error from failed ffmpeg run")
	}

	updatedJob, _, _ := store.GetVideoRenderJob(principal.AccountScopeID, sessionID, jobID)
	if updatedJob.Status != pebblestore.VideoRenderJobStatusFailed {
		t.Fatalf("expected job status failed, got: %s", updatedJob.Status)
	}
	if updatedJob.FailureCode != "ffmpeg_execution_error" {
		t.Fatalf("expected ffmpeg_execution_error failure code, got: %s", updatedJob.FailureCode)
	}
}

func TestRenderJobDaemonInterruptionRequeuesPinnedJob(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	store := newFakeSessionStore()
	store.sessions["sess_1"] = pebblestore.SessionSnapshot{ID: "sess_1", AccountScopeID: "acc_1", UserID: "usr_1"}
	store.projects["proj_1"] = pebblestore.VideoProjectSnapshot{ID: "proj_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", CurrentRevisionID: "rev_1"}
	store.revisions["rev_1"] = pebblestore.VideoProjectRevisionSnapshot{
		ID: "rev_1", ProjectID: "proj_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1",
		Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{
			ID: "clip", SourceKind: pebblestore.VideoClipSourceKindColor,
			DurationMs: 1000, TimelineEndMs: 1000, Visible: true,
		}}},
	}
	store.jobs["job_1"] = pebblestore.VideoRenderJobSnapshot{ID: "job_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_1", Status: pebblestore.VideoRenderJobStatusQueued}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &fakeCommandRunner{runHook: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, nil, nil, runner)
	if _, err := svc.RenderJob(ctx, principal, RenderJobRequest{SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_1", JobID: "job_1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderJob() error = %v, want context canceled", err)
	}
	job := store.jobs["job_1"]
	if job.Status != pebblestore.VideoRenderJobStatusQueued || job.FailureCode != "" {
		t.Fatalf("interrupted job = status %s failure %s, want durable queued retry", job.Status, job.FailureCode)
	}
}

func TestRecoverJobsResumesPinnedRevisionAndWorkspace(t *testing.T) {
	store := newFakeSessionStore()
	store.sessions["sess_1"] = pebblestore.SessionSnapshot{ID: "sess_1", AccountScopeID: "acc_1", UserID: "usr_1", WorkspacePath: "/trusted/workspace"}
	store.projects["proj_1"] = pebblestore.VideoProjectSnapshot{ID: "proj_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", CurrentRevisionID: "rev_new"}
	store.revisions["rev_old"] = pebblestore.VideoProjectRevisionSnapshot{ID: "rev_old", ProjectID: "proj_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "clip", SourceKind: pebblestore.VideoClipSourceKindColor, DurationMs: 1000, Visible: true}}}}
	store.jobs["job_1"] = pebblestore.VideoRenderJobSnapshot{ID: "job_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_old", Status: pebblestore.VideoRenderJobStatusRendering}
	svc := NewService(Config{}, store, &fakeArtifactAuthority{}, nil, nil, &fakeCommandRunner{})
	count, err := svc.RecoverJobs(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("RecoverJobs() = %d, %v", count, err)
	}
	job := store.jobs["job_1"]
	if job.Status != pebblestore.VideoRenderJobStatusReady {
		t.Fatalf("status = %s, want ready", job.Status)
	}
}

func TestRecoverJobsPreservesExistingReadyArtifact(t *testing.T) {
	store := newFakeSessionStore()
	job := pebblestore.VideoRenderJobSnapshot{ID: "job_1", AccountScopeID: "acc_1", UserID: "usr_1", SessionID: "sess_1", ProjectID: "proj_1", RevisionID: "rev_1", Status: pebblestore.VideoRenderJobStatusRendering}
	store.jobs[job.ID] = job
	store.variants["acc_1/sess_1/vproj_proj_1/vrender_job_1"] = pebblestore.SessionArtifactVariant{ID: "vrender_job_1", CollectionID: "vproj_proj_1", SessionID: "sess_1", Status: pebblestore.SessionArtifactStatusReady, EventSeq: 9, Size: 44}
	runner := &fakeCommandRunner{}
	svc := NewService(Config{}, store, nil, nil, nil, runner)
	if count, err := svc.RecoverJobs(context.Background()); err != nil || count != 1 {
		t.Fatalf("RecoverJobs() = %d, %v", count, err)
	}
	if store.jobs[job.ID].Status != pebblestore.VideoRenderJobStatusReady {
		t.Fatal("artifact recovery did not complete")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("ffmpeg calls = %d, want 0", len(runner.calls))
	}
	if store.jobs[job.ID].OutputArtifact == nil {
		t.Fatal("ready artifact reference was not restored")
	}
}

func TestRenderJobProcessAdmissionRejectsDuplicate(t *testing.T) {
	svc := NewService(Config{}, newFakeSessionStore(), nil, nil, nil, nil)
	if !svc.admit("job") {
		t.Fatal("first admission failed")
	}
	if svc.admit("job") {
		t.Fatal("duplicate admission succeeded")
	}
	svc.release("job")
	if !svc.admit("job") {
		t.Fatal("admission after release failed")
	}
	svc.release("job")
}

func TestReconcileInterruptedJobs(t *testing.T) {
	store := newFakeSessionStore()
	store.jobs["job_rendering_1"] = pebblestore.VideoRenderJobSnapshot{
		ID:             "job_rendering_1",
		AccountScopeID: "acc_1",
		SessionID:      "sess_1",
		ProjectID:      "proj_1",
		Status:         pebblestore.VideoRenderJobStatusRendering,
	}
	store.jobs["job_ready_1"] = pebblestore.VideoRenderJobSnapshot{
		ID:             "job_ready_1",
		AccountScopeID: "acc_1",
		SessionID:      "sess_1",
		ProjectID:      "proj_1",
		Status:         pebblestore.VideoRenderJobStatusReady,
	}

	svc := NewService(Config{}, store, nil, nil, nil, nil)
	reconciled, err := svc.ReconcileInterruptedJobs(context.Background(), "acc_1", "sess_1", "proj_1")
	if err != nil {
		t.Fatalf("reconcile err: %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("expected 1 reconciled job, got %d", reconciled)
	}

	job1, _, _ := store.GetVideoRenderJob("acc_1", "sess_1", "job_rendering_1")
	if job1.Status != pebblestore.VideoRenderJobStatusFailed || job1.FailureCode != "recovery_metadata_invalid" {
		t.Fatalf("expected invalid legacy job to fail recovery, got status=%s code=%s", job1.Status, job1.FailureCode)
	}

	jobReady, _, _ := store.GetVideoRenderJob("acc_1", "sess_1", "job_ready_1")
	if jobReady.Status != pebblestore.VideoRenderJobStatusReady {
		t.Fatalf("expected job_ready_1 to remain ready, got: %s", jobReady.Status)
	}
}

func TestStartRenderJobQueueGraceAllowsImmediateCancellation(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	store := newFakeSessionStore()
	store.jobs["job_cancel"] = pebblestore.VideoRenderJobSnapshot{
		ID:             "job_cancel",
		AccountScopeID: principal.AccountScopeID,
		SessionID:      "sess_1",
		ProjectID:      "proj_1",
		Status:         pebblestore.VideoRenderJobStatusQueued,
	}
	runner := &fakeCommandRunner{}
	svc := NewService(Config{}, store, nil, nil, nil, runner)

	svc.StartRenderJob(principal, RenderJobRequest{
		SessionID:  "sess_1",
		ProjectID:  "proj_1",
		JobID:      "job_cancel",
		QueueGrace: time.Second,
	})
	job, err := svc.CancelRenderJob(context.Background(), principal, "sess_1", "job_cancel")
	if err != nil {
		t.Fatalf("CancelRenderJob() error = %v", err)
	}
	if job.Status != pebblestore.VideoRenderJobStatusCancelled {
		t.Fatalf("CancelRenderJob() status = %s, want cancelled", job.Status)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := svc.WaitForIdle(waitCtx); err != nil {
		t.Fatalf("WaitForIdle() error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("render command calls = %d, want 0", len(runner.calls))
	}
	if got := store.jobs["job_cancel"].Status; got != pebblestore.VideoRenderJobStatusCancelled {
		t.Fatalf("durable status = %s, want cancelled", got)
	}
}

func TestStartRenderJobMarksQueuedPreflightFailureTerminal(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	store := newFakeSessionStore()
	store.jobs["job_preflight"] = pebblestore.VideoRenderJobSnapshot{ID: "job_preflight", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: "sess_1", ProjectID: "proj_missing", RevisionID: "rev_missing", Status: pebblestore.VideoRenderJobStatusQueued}
	store.sessions["sess_1"] = pebblestore.SessionSnapshot{ID: "sess_1", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
	svc := NewService(Config{}, store, nil, nil, nil, &fakeCommandRunner{})

	svc.StartRenderJob(principal, RenderJobRequest{SessionID: "sess_1", ProjectID: "proj_missing", RevisionID: "rev_missing", JobID: "job_preflight"})
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := svc.WaitForIdle(waitCtx); err != nil {
		t.Fatalf("WaitForIdle() error = %v", err)
	}
	job := store.jobs["job_preflight"]
	if job.Status != pebblestore.VideoRenderJobStatusFailed || job.FailureCode != "render_preflight_error" || !strings.Contains(job.FailureReason, "video project") {
		t.Fatalf("preflight failure job = %+v", job)
	}
}

func TestCancelRenderJob(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	store := newFakeSessionStore()
	store.jobs["job_cancel"] = pebblestore.VideoRenderJobSnapshot{
		ID:             "job_cancel",
		AccountScopeID: principal.AccountScopeID,
		SessionID:      "sess_1",
		ProjectID:      "proj_1",
		Status:         pebblestore.VideoRenderJobStatusRendering,
	}

	svc := NewService(Config{}, store, nil, nil, nil, nil)
	job, err := svc.CancelRenderJob(context.Background(), principal, "sess_1", "job_cancel")
	if err != nil {
		t.Fatalf("cancel err: %v", err)
	}
	if job.Status != pebblestore.VideoRenderJobStatusCancelled {
		t.Fatalf("expected cancelled status, got: %s", job.Status)
	}
}

func TestCancelRenderJobRejectsTerminalStates(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc_1", UserID: "usr_1"}
	for _, status := range []string{
		pebblestore.VideoRenderJobStatusReady,
		pebblestore.VideoRenderJobStatusFailed,
	} {
		t.Run(status, func(t *testing.T) {
			store := newFakeSessionStore()
			store.jobs["job_terminal"] = pebblestore.VideoRenderJobSnapshot{
				ID:             "job_terminal",
				AccountScopeID: principal.AccountScopeID,
				SessionID:      "sess_1",
				ProjectID:      "proj_1",
				Status:         status,
			}
			svc := NewService(Config{}, store, nil, nil, nil, nil)

			if _, err := svc.CancelRenderJob(context.Background(), principal, "sess_1", "job_terminal"); err == nil || !strings.Contains(err.Error(), "cannot cancel terminal render job") {
				t.Fatalf("CancelRenderJob() error = %v, want terminal-state rejection", err)
			}
			if got := store.jobs["job_terminal"].Status; got != status {
				t.Fatalf("durable status = %s, want %s", got, status)
			}
		})
	}
}
