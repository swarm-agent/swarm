package videoproject

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: StartRenderJob must admit accepted exact native MP4s without a
// fabricated legacy reference, and reject unresolved or invalid authorities
// before CreateVideoRenderJob. Threat: the legacy-only guard strands accepted
// projects; merely removing it queues stale/foreign/HTML inputs. A fake-store
// service test is the narrowest proof of preflight and no mutation on rejection;
// no renderer, real project, or acceptance gesture is exercised here.
func TestStartRenderJobNativeSourcePreflight(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*pebblestore.VideoProjectTimeline, *fakeArtifactV3Authority)
		want   string
	}{
		{name: "accepted native MP4"},
		{name: "native without plan metadata", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) { tl.Metadata = nil }},
		{name: "unexported native without plan metadata", mutate: func(tl *pebblestore.VideoProjectTimeline, a *fakeArtifactV3Authority) {
			tl.Metadata = nil
			tl.Clips[0].MediaType = "text/html"
			tl.Clips[0].ArtifactV3Ref.MediaType = "text/html"
			a.allowed[*tl.Clips[0].ArtifactV3Ref] = true
		}, want: "exact MP4 derivative"},
		{name: "stale native without plan metadata", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Metadata = nil
			tl.Clips[0].ArtifactV3Ref.EventSeq++
		}, want: "stale"},
		{name: "missing native without plan metadata", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Metadata = nil
			tl.Clips[0].ArtifactV3Ref = nil
		}, want: "exactly one"},
		{name: "missing", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Clips[0].ArtifactV3Ref = nil
		}, want: "exact timeline derivative"},
		{name: "dual legacy", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Clips[0].ArtifactRef = &pebblestore.SessionArtifactSelectionReference{}
		}, want: "mixes"},
		{name: "dual V2", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Clips[0].ArtifactV2Ref = &pebblestore.ArtifactV2VideoReference{}
		}, want: "mixes"},
		{name: "stale", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Clips[0].ArtifactV3Ref.EventSeq++
		}, want: "exact timeline derivative"},
		{name: "foreign", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Clips[0].ArtifactV3Ref.SessionID = "foreign"
		}, want: "exact timeline derivative"},
		{name: "tampered", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Clips[0].ArtifactV3Ref.DigestSHA256 = strings.Repeat("0", 64)
		}, want: "exact timeline derivative"},
		{name: "wrong principal", mutate: func(_ *pebblestore.VideoProjectTimeline, a *fakeArtifactV3Authority) { a.userID = "other" }, want: "owner"},
		{name: "native HTML", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			p := tl.Metadata["accepted_video_plan"].(pebblestore.VideoPlanProposal)
			tl.Clips[0].ArtifactV3Ref = p.Parts[0].ArtifactV3Source
			tl.Clips[0].MediaType = "text/html"
		}, want: "exact timeline derivative"},
		{name: "missing derivative", mutate: func(tl *pebblestore.VideoProjectTimeline, a *fakeArtifactV3Authority) {
			tl.Clips[0].ArtifactV3Ref.DerivativeID = ""
			a.allowed[*tl.Clips[0].ArtifactV3Ref] = true
		}, want: "exact timeline derivative"},
		{name: "offset range", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Clips[0].SourceStartMs = 1
			tl.Clips[0].SourceEndMs++
		}, want: "timing"},
		{name: "invalid range", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) { tl.Clips[0].SourceEndMs = 0 }, want: "range"},
		{name: "media mismatch", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Clips[0].MediaType = "image/png"
		}, want: "media_type"},
		{name: "text placeholder", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			tl.Clips[0].SourceKind = pebblestore.VideoClipSourceKindText
		}, want: "unresolved"},
		{name: "pending production", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			p := tl.Metadata["accepted_video_plan"].(pebblestore.VideoPlanProposal)
			p.Parts[0].ProductionState = pebblestore.VideoProductionStatePending
		}, want: "pending"},
		{name: "missing plan derivative", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			p := tl.Metadata["accepted_video_plan"].(pebblestore.VideoPlanProposal)
			p.Parts[0].AnimationCandidates.V3Derivative = nil
		}, want: "derivative"},
		{name: "foreign selected source", mutate: func(tl *pebblestore.VideoProjectTimeline, _ *fakeArtifactV3Authority) {
			p := tl.Metadata["accepted_video_plan"].(pebblestore.VideoPlanProposal)
			p.Parts[0].ArtifactV3Source.SessionID = "foreign"
		}, want: "source identity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeSessionStore()
			principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
			plan, refs := testArtifactV3VideoPlan()
			authority := &fakeArtifactV3Authority{accountScopeID: "account", userID: "user", allowed: map[pebblestore.ArtifactV3VideoReference]bool{}}
			for _, ref := range refs {
				authority.allowed[ref] = true
			}
			visual := *plan.Parts[0].ArtifactV3Visual
			timeline := pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "motion", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactV3Ref: &visual, MediaType: "video/mp4", SourceEndMs: 2000, DurationMs: 2000, TimelineEndMs: 2000, Visible: true}}, Metadata: map[string]any{"accepted_video_plan": plan}}
			if tc.mutate != nil {
				tc.mutate(&timeline, authority)
			}
			store.sessions["studio"] = pebblestore.SessionSnapshot{ID: "studio", AccountScopeID: "account", UserID: "user"}
			project := pebblestore.VideoProjectSnapshot{ID: "project", SessionID: "studio", AccountScopeID: "account", UserID: "user", CurrentRevisionID: "accepted"}
			revision := pebblestore.VideoProjectRevisionSnapshot{ID: "accepted", ProjectID: "project", SessionID: "studio", AccountScopeID: "account", UserID: "user", Timeline: timeline}
			store.projects["project"] = project
			store.revisions["project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"accepted": revision}
			svc := NewService(store)
			svc.SetArtifactV3Authority(authority)
			job, err := svc.StartRenderJob(context.Background(), principal, StartRenderJobInput{SessionID: "studio", ProjectID: "project", JobID: "render"})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v, want %q", err, tc.want)
				}
				if len(store.jobs) != 0 {
					t.Fatalf("rejection created jobs: %+v", store.jobs)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(store.jobs) != 1 || job.RevisionID != "accepted" || job.Status != pebblestore.VideoRenderJobStatusQueued {
					t.Fatalf("wrong render job: %+v", job)
				}
				if len(authority.calls) == 0 || authority.calls[len(authority.calls)-1] != visual {
					t.Fatalf("exact native input not authenticated: %+v", authority.calls)
				}
			}
			if !reflect.DeepEqual(store.projects["project"], project) || !reflect.DeepEqual(store.revisions["project"]["accepted"], revision) || len(store.proposals) != 0 {
				t.Fatal("preflight changed accepted source or proposal state")
			}
		})
	}
}

type preflightV2Authority struct {
	ref pebblestore.ArtifactV2VideoReference
}

func (a preflightV2Authority) ValidateVideoReference(account, user string, ref pebblestore.ArtifactV2VideoReference) error {
	if account != "account" || user != "user" || ref != a.ref {
		return fmt.Errorf("invalid V2 exact authority")
	}
	return nil
}

// Requirement: the same StartRenderJob gate retains supported legacy and V2
// sources, while unexported V2 HTML and missing authority fail without jobs.
// Fake exact authorities isolate this service contract from media decoding.
func TestStartRenderJobLegacyAndV2SourcePreflight(t *testing.T) {
	for _, kind := range []string{"legacy", "V2", "V2 MP4", "V2 HTML", "V2 unavailable"} {
		t.Run(kind, func(t *testing.T) {
			store := newFakeSessionStore()
			svc := NewService(store)
			principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
			store.sessions["studio"] = pebblestore.SessionSnapshot{ID: "studio", AccountScopeID: "account", UserID: "user"}
			clip := pebblestore.VideoTimelineClip{ID: "still", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, MediaType: "image/png", DurationMs: 2000}
			if kind == "legacy" {
				clip.ArtifactRef = &pebblestore.SessionArtifactSelectionReference{SessionID: "studio", CollectionID: "collection", VariantID: "still", EventSeq: 1}
				store.artifacts["account/studio/collection/still"] = pebblestore.SessionArtifactVariant{ID: "still", Status: pebblestore.SessionArtifactStatusReady, EventSeq: 1, MediaType: "image/png"}
			} else {
				ref := pebblestore.ArtifactV2VideoReference{MediaType: "image/png"}
				if kind == "V2 MP4" {
					ref.MediaType = "video/mp4"
					ref.DurationMs = 2000
					clip.MediaType = ref.MediaType
					clip.SourceEndMs = 2000
				}
				if kind == "V2 HTML" {
					ref.MediaType = "text/html"
					clip.MediaType = ref.MediaType
				}
				clip.ArtifactV2Ref = &ref
				if kind != "V2 unavailable" {
					svc.SetArtifactV2Authority(preflightV2Authority{ref: ref})
				}
			}
			store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: "account", SessionID: "studio", UserID: "user", CurrentRevisionID: "accepted"}
			store.revisions["project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"accepted": {ID: "accepted", ProjectID: "project", SessionID: "studio", AccountScopeID: "account", UserID: "user", Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{clip}, Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial}}}}}
			_, err := svc.StartRenderJob(context.Background(), principal, StartRenderJobInput{SessionID: "studio", ProjectID: "project", JobID: "render"})
			if kind == "V2 HTML" || kind == "V2 unavailable" {
				if err == nil || len(store.jobs) != 0 {
					t.Fatalf("invalid V2 queued: %v %+v", err, store.jobs)
				}
			} else if err != nil || len(store.jobs) != 1 {
				t.Fatalf("supported source rejected: %v", err)
			}
		})
	}
}
