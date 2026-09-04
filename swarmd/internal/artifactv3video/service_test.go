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
	renderer := &fakeRenderer{png: []byte("png"), mp4: []byte("mp4")}
	storage := &fakeStore{data: map[string][]byte{}}
	service := New(authority, renderer, storage)
	selection := testSelection()

	conversion, err := service.Convert(context.Background(), "account", selection)
	if err != nil {
		t.Fatal(err)
	}
	if renderer.request.AnimationAdapter != animationAdapterVersion || renderer.request.DurationMs != DefaultDurationMs || renderer.request.FPS != DefaultFPS {
		t.Fatalf("trusted render request = %#v", renderer.request)
	}
	if conversion.Source.DerivativeID != "" || conversion.Fallback.MediaType != "image/png" || conversion.MP4.MediaType != "video/mp4" || len(storage.data) != 2 {
		t.Fatalf("conversion = %#v storage=%#v", conversion, storage.data)
	}
	if conversion.Source.CommitOID != selection.CommitOID || conversion.Source.TreeOID != selection.TreeOID || conversion.Source.RevisionID != selection.RevisionID {
		t.Fatalf("V3 identity drifted: %#v", conversion.Source)
	}
	if conversion.Plan.Parts[0].ArtifactV3Source == nil || conversion.Plan.Parts[0].ArtifactV3Visual == nil || conversion.Plan.Parts[0].ArtifactV2Visual != nil || conversion.Plan.Parts[0].Visual != nil {
		t.Fatalf("plan did not stay V3-native: %#v", conversion.Plan)
	}
	payload, err := service.Read(context.Background(), "account", conversion.MP4)
	if err != nil || string(payload) != "mp4" {
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
		{"stale head", func(a *fakeAuthority, _ *Selection) { a.project.CommitOID = strings.Repeat("d", 64) }, "stale"},
		{"missing build", func(a *fakeAuthority, _ *Selection) { a.project.BuildID = "" }, "build"},
		{"missing validation", func(a *fakeAuthority, _ *Selection) { a.project.ValidationID = "" }, "validation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authority := &fakeAuthority{project: testProject()}
			selection := testSelection()
			tc.mutate(authority, &selection)
			storage := &fakeStore{data: map[string][]byte{}}
			_, err := New(authority, &fakeRenderer{png: []byte("png"), mp4: []byte("mp4")}, storage).Convert(context.Background(), "account", selection)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v want %q", err, tc.wantErr)
			}
			if len(storage.data) != 0 {
				t.Fatalf("failed conversion wrote derivatives: %#v", storage.data)
			}
		})
	}
}

func TestConvertRendererAndAtomicStoreFailuresLeaveNoPartialWrites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		renderer *fakeRenderer
		storeErr error
	}{
		{"preflight", &fakeRenderer{preflightErr: errors.New("browser rejected")}, nil},
		{"render", &fakeRenderer{renderErr: errors.New("ffmpeg failed")}, nil},
		{"atomic store", &fakeRenderer{png: []byte("png"), mp4: []byte("mp4")}, errors.New("commit failed")},
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

func TestReadRejectsDigestSubstitution(t *testing.T) {
	storage := &fakeStore{data: map[string][]byte{}}
	service := New(&fakeAuthority{project: testProject()}, &fakeRenderer{png: []byte("png"), mp4: []byte("mp4")}, storage)
	conversion, err := service.Convert(context.Background(), "account", testSelection())
	if err != nil {
		t.Fatal(err)
	}
	storage.data[conversion.MP4.DerivativeID] = []byte("substituted")
	if _, err := service.Read(context.Background(), "account", conversion.MP4); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("digest substitution err=%v", err)
	}
}

func TestVideoPlanValidationAcceptsOnlyCompleteNativeV3Conversion(t *testing.T) {
	storage := &fakeStore{data: map[string][]byte{}}
	conversion, err := New(&fakeAuthority{project: testProject()}, &fakeRenderer{png: []byte("png"), mp4: []byte("mp4")}, storage).Convert(context.Background(), "account", testSelection())
	if err != nil {
		t.Fatal(err)
	}
	if err := pebblestore.ValidateArtifactV3ConversionPlan(conversion.Plan); err != nil {
		t.Fatalf("native V3 plan invalid: %v", err)
	}
	bad := conversion.Plan
	bad.Parts = append([]pebblestore.VideoPlanPart(nil), conversion.Plan.Parts...)
	bad.Parts[0].ArtifactV3Visual = nil
	if err := pebblestore.ValidateArtifactV3ConversionPlan(bad); err == nil {
		t.Fatal("missing native V3 derivative must fail")
	}
}

func testSelection() Selection {
	return Selection{SessionID: "session", ArtifactID: "artifact", RevisionID: "revision", CommitOID: strings.Repeat("a", 64), TreeOID: strings.Repeat("b", 64), PartID: "hero", CaptureStateID: "state"}
}

func testProject() Project {
	sel := testSelection()
	return Project{SessionID: sel.SessionID, ArtifactID: sel.ArtifactID, RevisionID: sel.RevisionID, CommitOID: sel.CommitOID, TreeOID: sel.TreeOID, ManifestDigestSHA256: strings.Repeat("c", 64), BuildID: "build", ValidationID: "validation", EventSeq: 7, MediaType: "text/html", AnimationProfile: DefaultAnimationProfile, Files: map[string][]byte{"index.html": []byte("<html></html>")}}
}

type fakeAuthority struct { project Project; err error }
func (f *fakeAuthority) ReadSelectedHead(_ context.Context, account string, selection Selection) (Project, error) {
	if f.err != nil { return Project{}, f.err }
	if account != "account" || selection.AccountScopeID != account { return Project{}, errors.New("owner mismatch") }
	return cloneProject(f.project), nil
}

type fakeRenderer struct { request RenderRequest; preflightErr, renderErr error; png, mp4 []byte }
func (f *fakeRenderer) Preflight(_ context.Context, request RenderRequest) error { f.request = request; return f.preflightErr }
func (f *fakeRenderer) Render(_ context.Context, request RenderRequest) ([]byte, []byte, error) { f.request = request; return f.png, f.mp4, f.renderErr }

type fakeStore struct { data map[string][]byte; putErr error }
func (f *fakeStore) PutAtomic(_ context.Context, _, _ string, derivatives []Derivative) error {
	if f.putErr != nil { return f.putErr }
	staged := map[string][]byte{}
	for _, derivative := range derivatives { staged[derivative.ID] = append([]byte(nil), derivative.Bytes...) }
	for id, payload := range staged { f.data[id] = payload }
	return nil
}
func (f *fakeStore) Read(_ context.Context, _, _, derivativeID string) ([]byte, error) {
	payload, ok := f.data[derivativeID]; if !ok { return nil, errors.New("not found") }; return append([]byte(nil), payload...), nil
}
