package artifactv3video

import (
	"context"
	"errors"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: conversion must authenticate one exact V3 Git head and publish
// both digest-bound derivatives atomically. Threat: cross-owner/stale identity,
// renderer/storage failure, or digest substitution could create a partial or
// unauthenticated Video Studio proposal. This service-level test is the narrowest
// layer covering authority, renderer, and atomic-storage postconditions.
func TestConvertNativeArtifactV3Video(t *testing.T) {
	project := testProject()
	authority := &fakeAuthority{project: project}
	renderer := validFakeRenderer()
	storage := &fakeStore{data: map[string][]byte{}}
	service := New(authority, renderer, storage)
	selection := testSelection()

	conversion, err := service.Convert(context.Background(), "account", selection)
	if err != nil {
		t.Fatal(err)
	}
	if renderer.request.AnimationAdapter != animationAdapterVersion || renderer.request.DurationMs != DefaultDurationMs || renderer.request.FPS != DefaultFPS || renderer.request.PartID != selection.PartID || renderer.request.CaptureStateID != selection.CaptureStateID {
		t.Fatalf("trusted render request = %#v", renderer.request)
	}
	if conversion.Source.DerivativeID != "" || conversion.Fallback.MediaType != "image/png" || conversion.MP4.MediaType != "video/mp4" || conversion.Fallback.DerivativeID == conversion.MP4.DerivativeID || conversion.Fallback.DigestSHA256 != digestBytes(validPNG()) || conversion.MP4.DigestSHA256 != digestBytes(validMP4()) || len(storage.data) != 2 {
		t.Fatalf("conversion = %#v storage=%#v", conversion, storage.data)
	}
	if conversion.Source.CommitOID != selection.CommitOID || conversion.Source.TreeOID != selection.TreeOID || conversion.Source.RevisionID != selection.RevisionID || conversion.Source.PartID != selection.PartID || conversion.Source.CaptureStateID != selection.CaptureStateID {
		t.Fatalf("V3 identity drifted: %#v", conversion.Source)
	}
	if conversion.Plan.Parts[0].ArtifactV3Source == nil || conversion.Plan.Parts[0].ArtifactV3Still == nil || conversion.Plan.Parts[0].ArtifactV3Visual == nil || conversion.Plan.Parts[0].ArtifactV2Source != nil || conversion.Plan.Parts[0].ArtifactV2Still != nil || conversion.Plan.Parts[0].ArtifactV2Visual != nil || conversion.Plan.Parts[0].Visual != nil {
		t.Fatalf("plan did not stay V3-native: %#v", conversion.Plan)
	}
	payload, err := service.ReadVideoReference(context.Background(), "account", "user", conversion.MP4)
	if err != nil || string(payload) != string(validMP4()) {
		t.Fatalf("read=%q err=%v", payload, err)
	}
}

func TestConvertRejectsOwnershipStaleAndMissingEvidenceWithoutWrites(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*fakeAuthority, *Selection)
		wantErr string
	}{
		{"ownership", func(a *fakeAuthority, _ *Selection) { a.err = errors.New("owner mismatch") }, "owner mismatch"},
		{"stale head", func(a *fakeAuthority, _ *Selection) { a.project.CommitOID = strings.Repeat("d", 40) }, "stale"},
		{"missing build", func(a *fakeAuthority, _ *Selection) { a.project.BuildID = "" }, "build"},
		{"missing validation", func(a *fakeAuthority, _ *Selection) { a.project.ValidationID = "" }, "validation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authority := &fakeAuthority{project: testProject()}
			selection := testSelection()
			tc.mutate(authority, &selection)
			storage := &fakeStore{data: map[string][]byte{}}
			_, err := New(authority, validFakeRenderer(), storage).Convert(context.Background(), "account", selection)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v want %q", err, tc.wantErr)
			}
			if len(storage.data) != 0 {
				t.Fatalf("failed conversion wrote derivatives: %#v", storage.data)
			}
		})
	}
}

// Requirement: cancellation before authority/render work leaves no derivative.
// Threat: a stopped conversion could otherwise publish stale media after user intent ended.
func TestConvertCancelledContextLeavesNoWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	storage := &fakeStore{data: map[string][]byte{}}
	_, err := New(&fakeAuthority{project: testProject()}, validFakeRenderer(), storage).Convert(ctx, "account", testSelection())
	if !errors.Is(err, context.Canceled) || len(storage.data) != 0 {
		t.Fatalf("cancelled conversion err=%v storage=%#v", err, storage.data)
	}
}

// Requirement: cancellation after rendering but before publication is atomic.
// Threat: an expensive render could outlive cancellation and leak a partial durable set.
func TestConvertCancellationAfterRenderLeavesNoWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	storage := &fakeStore{data: map[string][]byte{}}
	renderer := validFakeRenderer()
	renderer.afterRender = cancel
	_, err := New(&fakeAuthority{project: testProject()}, renderer, storage).Convert(ctx, "account", testSelection())
	if !errors.Is(err, context.Canceled) || len(storage.data) != 0 {
		t.Fatalf("post-render cancellation err=%v storage=%#v", err, storage.data)
	}
}

func TestConvertRendererAndAtomicStoreFailuresLeaveNoPartialWrites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		renderer *fakeRenderer
		storeErr error
	}{
		{"preflight", &fakeRenderer{preflightErr: errors.New("browser rejected")}, nil},
		{"render", &fakeRenderer{renderErr: errors.New("ffmpeg failed"), durationMs: DefaultDurationMs, fps: DefaultFPS}, nil},
		{"atomic store", validFakeRenderer(), errors.New("commit failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &fakeStore{data: map[string][]byte{}, putErr: tc.storeErr}
			_, err := New(&fakeAuthority{project: testProject()}, tc.renderer, storage).Convert(context.Background(), "account", testSelection())
			if err == nil {
				t.Fatal("expected failure")
			}
			if len(storage.data) != 0 {
				t.Fatalf("partial derivatives persisted: %#v", storage.data)
			}
		})
	}
}

// Requirement: readiness requires actual PNG and MP4 container signatures.
// Threat: arbitrary non-empty renderer bytes could become trusted media authority.
func TestConvertRejectsInvalidDerivativeContainersWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		renderer *fakeRenderer
		want     string
	}{
		{"invalid png", &fakeRenderer{png: []byte("not-png"), mp4: validMP4(), durationMs: DefaultDurationMs, fps: DefaultFPS}, "PNG"},
		{"invalid mp4", &fakeRenderer{png: validPNG(), mp4: []byte("not-mp4"), durationMs: DefaultDurationMs, fps: DefaultFPS}, "MP4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &fakeStore{data: map[string][]byte{}}
			_, err := New(&fakeAuthority{project: testProject()}, tc.renderer, storage).Convert(context.Background(), "account", testSelection())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
			if len(storage.data) != 0 {
				t.Fatalf("invalid containers persisted: %#v", storage.data)
			}
		})
	}
}

// Requirement: renderer-observed duration/FPS must equal the requested contract.
// Threat: valid media for a different timing could corrupt proposal and final-cut ranges.
func TestConvertRejectsRendererTimingMismatchWithoutWrites(t *testing.T) {
	storage := &fakeStore{data: map[string][]byte{}}
	renderer := validFakeRenderer()
	renderer.durationMs--
	_, err := New(&fakeAuthority{project: testProject()}, renderer, storage).Convert(context.Background(), "account", testSelection())
	if err == nil || !strings.Contains(err.Error(), "timing") {
		t.Fatalf("timing mismatch err=%v", err)
	}
	if len(storage.data) != 0 {
		t.Fatalf("timing mismatch persisted derivatives: %#v", storage.data)
	}
}

func TestReadCancelledContextLeavesDerivativeUnread(t *testing.T) {
	storage := &fakeStore{data: map[string][]byte{}}
	service := New(&fakeAuthority{project: testProject()}, validFakeRenderer(), storage)
	conversion, err := service.Convert(context.Background(), "account", testSelection())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ReadVideoReference(ctx, "account", "user", conversion.MP4); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled derivative read err=%v", err)
	}
}

// Requirement: a durable reference is unusable when its derivative is missing.
// Threat: final rendering could silently substitute another authority or empty bytes.
func TestReadRejectsMissingDerivative(t *testing.T) {
	storage := &fakeStore{data: map[string][]byte{}}
	service := New(&fakeAuthority{project: testProject()}, validFakeRenderer(), storage)
	conversion, err := service.Convert(context.Background(), "account", testSelection())
	if err != nil {
		t.Fatal(err)
	}
	delete(storage.data, conversion.MP4.DerivativeID)
	if _, err := service.ReadVideoReference(context.Background(), "account", "user", conversion.MP4); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing derivative err=%v", err)
	}
}

func TestReadRejectsDigestSubstitution(t *testing.T) {
	storage := &fakeStore{data: map[string][]byte{}}
	service := New(&fakeAuthority{project: testProject()}, validFakeRenderer(), storage)
	conversion, err := service.Convert(context.Background(), "account", testSelection())
	if err != nil {
		t.Fatal(err)
	}
	storage.data[conversion.MP4.DerivativeID] = []byte("substituted")
	if _, err := service.ReadVideoReference(context.Background(), "account", "user", conversion.MP4); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("digest substitution err=%v", err)
	}
}

// Requirement: every Video Studio consumer fails closed when native V3
// reference authority is absent; nil wiring must never panic or downgrade.
func TestVideoReferenceAuthorityFailsClosedWhenUnconfigured(t *testing.T) {
	if err := (*Service)(nil).ValidateVideoReference("account", "user", pebblestore.ArtifactV3VideoReference{}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil authority validation err=%v", err)
	}
	if _, err := (*Service)(nil).ReadVideoReference(context.Background(), "account", "user", pebblestore.ArtifactV3VideoReference{DerivativeID: "missing"}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil authority read err=%v", err)
	}
	if _, err := (*Service)(nil).Read(context.Background(), "account", pebblestore.ArtifactV3VideoReference{DerivativeID: "missing"}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil compatibility read err=%v", err)
	}
}

func TestValidateVideoReferenceRejectsForeignUser(t *testing.T) {
	service := New(&fakeAuthority{project: testProject()}, validFakeRenderer(), &fakeStore{data: map[string][]byte{}})
	conversion, err := service.Convert(context.Background(), "account", testSelection())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateVideoReference("account", "foreign", conversion.Source); err == nil || !strings.Contains(err.Error(), "owner mismatch") {
		t.Fatalf("foreign source validation err=%v", err)
	}
}

func TestVideoPlanValidationAcceptsOnlyCompleteNativeV3Conversion(t *testing.T) {
	storage := &fakeStore{data: map[string][]byte{}}
	conversion, err := New(&fakeAuthority{project: testProject()}, validFakeRenderer(), storage).Convert(context.Background(), "account", testSelection())
	if err != nil {
		t.Fatal(err)
	}
	if err := pebblestore.ValidateVideoPlanForIntent(pebblestore.VideoEditProposalIntentArtifactV3Convert, conversion.Plan); err != nil {
		t.Fatalf("native V3 plan invalid: %v", err)
	}
	bad := conversion.Plan
	bad.Parts = append([]pebblestore.VideoPlanPart(nil), conversion.Plan.Parts...)
	bad.Parts[0].ArtifactV3Visual = nil
	if err := pebblestore.ValidateVideoPlanForIntent(pebblestore.VideoEditProposalIntentArtifactV3Convert, bad); err == nil {
		t.Fatal("missing native V3 derivative must fail")
	}
}

func validPNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("payload")...)
}

func validMP4() []byte {
	return append([]byte{0, 0, 0, 12}, []byte("ftypisom")...)
}

func validFakeRenderer() *fakeRenderer {
	return &fakeRenderer{png: validPNG(), mp4: validMP4(), durationMs: DefaultDurationMs, fps: DefaultFPS}
}

func testSelection() Selection {
	return Selection{UserID: "user", SessionID: "session", ArtifactID: "artifact", RevisionID: "revision", CommitOID: strings.Repeat("a", 40), TreeOID: strings.Repeat("b", 40)}
}

func testProject() Project {
	sel := testSelection()
	return Project{SessionID: sel.SessionID, ArtifactID: sel.ArtifactID, RevisionID: sel.RevisionID, CommitOID: sel.CommitOID, TreeOID: sel.TreeOID, ManifestDigestSHA256: strings.Repeat("c", 64), BuildID: "build", ValidationID: "validation", EventSeq: 7, MediaType: "text/html", AnimationProfile: DefaultAnimationProfile, Files: map[string][]byte{"index.html": []byte("<html></html>")}}
}

type fakeAuthority struct {
	project Project
	err     error
}

func (f *fakeAuthority) ReadImmutableRevision(ctx context.Context, account string, selection Selection) (Project, error) {
	return f.ReadSelectedHead(ctx, account, selection)
}

func (f *fakeAuthority) ReadSelectedHead(_ context.Context, account string, selection Selection) (Project, error) {
	if f.err != nil {
		return Project{}, f.err
	}
	if account != "account" || selection.AccountScopeID != account || selection.UserID != "user" {
		return Project{}, errors.New("owner mismatch")
	}
	return cloneProject(f.project), nil
}

type fakeRenderer struct {
	request                 RenderRequest
	preflightErr, renderErr error
	afterRender             func()
	png, mp4                []byte
	durationMs              int64
	fps                     float64
}

func (f *fakeRenderer) Preflight(_ context.Context, request RenderRequest) error {
	f.request = request
	return f.preflightErr
}
func (f *fakeRenderer) Render(_ context.Context, request RenderRequest) (RenderResult, error) {
	f.request = request
	if f.afterRender != nil {
		f.afterRender()
	}
	return RenderResult{FallbackPNG: f.png, SilentMP4: f.mp4, DurationMs: f.durationMs, FPS: f.fps}, f.renderErr
}

type fakeStore struct {
	data     map[string][]byte
	receipts map[pebblestore.ArtifactV3VideoReference]bool
	putErr   error
}

func (f *fakeStore) PutAtomic(_ context.Context, _, _ string, derivatives []Derivative) error {
	if f.putErr != nil {
		return f.putErr
	}
	staged := map[string][]byte{}
	if f.receipts == nil {
		f.receipts = map[pebblestore.ArtifactV3VideoReference]bool{}
	}
	for _, derivative := range derivatives {
		f.receipts[derivative.Reference] = true
		staged[derivative.ID] = append([]byte(nil), derivative.Bytes...)
	}
	for id, payload := range staged {
		f.data[id] = payload
	}
	return nil
}
func (f *fakeStore) Read(_ context.Context, _, _ string, ref pebblestore.ArtifactV3VideoReference) ([]byte, error) {
	if !f.receipts[ref] {
		return nil, errors.New("exact receipt missing")
	}
	derivativeID := ref.DerivativeID
	payload, ok := f.data[derivativeID]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), payload...), nil
}
