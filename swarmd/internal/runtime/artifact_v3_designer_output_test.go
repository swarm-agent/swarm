package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: PrepareArtifactV3Turn must admit two exact-base spatial_3d
// siblings when landscape_video merely asserts the native default viewport.
// Real Git/Pebble is the narrowest layer proving IDs, durable lineage and source
// immutability; synthetic evidence intentionally does not prove browser/provider
// execution. Foreign/stale/policy-changing requests must allocate nothing.
func TestArtifactV3DesignerDefaultOutputSiblings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	_, _, err = sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "source", AccountScopeID: "account", UserID: "user", Title: "Source", WorkspacePath: root, Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	gitRoot := filepath.Join(root, "git")
	service, err := pebblestore.NewArtifactV3Service(sessions.Store(), gitRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := newArtifactV3RuntimeAdapter(service, sessions.Store(), gitRoot, filepath.Join(root, "evidence"), pebblestore.ArtifactV3Limits{}, artifactV3RuntimeRenderer{})
	profile, err := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "spatial_3d"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := artifact.ResolveOutputRequirements(&artifact.OutputRequirementsInput{Preset: "landscape_video"})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html", AnimationProfile: profile, Parts: []pebblestore.ArtifactV3Part{{ID: "scene", Label: "Scene", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#scene"}}}})
	if err != nil {
		t.Fatal(err)
	}
	html := []byte(`<html><head><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":8000,"fps":30}</script></head><body><main id="scene">Three.js experiment</main><script type="module">import * as THREE from 'three'; const scene=new THREE.Scene(); globalThis.__SWARM_ANIMATION_V1__={version:'swarm.animation/v1',ready:()=>({duration_ms:8000,fps:30}),seek:ms=>({time_ms:ms})};</script></body></html>`)
	owner := pebblestore.ArtifactV3Owner{AccountScopeID: "account", UserID: "user", SessionID: "source"}
	evidence := pebblestore.ArtifactV3EvidenceProjection{Status: "succeeded", DigestSHA256: "fixture", Reference: "fixture"}
	created, err := service.Create(ctx, pebblestore.ArtifactV3CreateInput{Owner: owner, ArtifactID: "artifact-source", TransactionID: "genesis", Project: pebblestore.ArtifactV3Project{Files: map[string][]byte{pebblestore.ArtifactV3ManifestFilename: manifest, "index.html": html}}, Build: evidence, Preview: evidence})
	if err != nil {
		t.Fatal(err)
	}
	request := tool.ArtifactV3PrepareTurnRequest{AccountScopeID: owner.AccountScopeID, UserID: owner.UserID, OwnerSessionID: owner.SessionID, TaskCallID: "swarm:two", ArtifactID: "artifact-source", BaseCommitOID: created.Repository.HeadCommitOID, ProjectionSeq: created.Repository.EventSeq, PolicyRevision: "policy", CandidateIndex: 1, RevisionIntent: pebblestore.ArtifactV3RevisionWholeProject, AnimationProfile: profile, OutputRequirements: output, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	snapshot := func() pebblestore.ArtifactV3RepositoryProjection {
		r, ok, e := sessions.Store().GetArtifactV3Repository("account", "user", "artifact-source")
		if e != nil || !ok {
			t.Fatalf("repository: %v", e)
		}
		return r
	}
	before := snapshot()
	for _, kind := range []string{"portrait", "forged-output", "profile", "owner", "stale"} {
		bad := request
		switch kind {
		case "portrait":
			bad.OutputRequirements, _ = artifact.ResolveOutputRequirements(&artifact.OutputRequirementsInput{Preset: "portrait_video"})
		case "forged-output":
			copied := *output
			copied.RegistryVersion = "forged"
			bad.OutputRequirements = &copied
		case "profile":
			bad.AnimationProfile, _ = artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
		case "owner":
			bad.OwnerSessionID = "foreign"
		case "stale":
			bad.ProjectionSeq++
		}
		if _, e := adapter.PrepareArtifactV3Turn(ctx, bad); e == nil {
			t.Fatalf("accepted %s", kind)
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatalf("%s mutated repository", kind)
		}
	}
	first, err := adapter.PrepareArtifactV3Turn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.CandidateIndex = 2
	second, err := adapter.PrepareArtifactV3Turn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []tool.ArtifactV3AuthorGrant{first, second} {
		if g.ArtifactID != request.ArtifactID || g.BaseCommitOID != request.BaseCommitOID || g.SourceProjectionSeq != request.ProjectionSeq || g.OwnerSessionID != owner.SessionID || g.OutputRequirements != nil || !reflect.DeepEqual(g.AnimationProfile, profile) || g.RevisionIntent != request.RevisionIntent {
			t.Fatalf("binding drift: %+v", g)
		}
	}
	if first.TurnID != second.TurnID || first.CandidateID == second.CandidateID || first.ID == second.ID {
		t.Fatal("sibling identities collided")
	}
	after := snapshot()
	if after.HeadCommitOID != before.HeadCommitOID || len(after.Drafts) != 2 {
		t.Fatalf("source/sibling drift: %+v", after)
	}
	request.TaskCallID = "unrelated"
	if _, e := adapter.PrepareArtifactV3Turn(ctx, request); !errors.Is(e, pebblestore.ErrArtifactV3Conflict) {
		t.Fatalf("stale unrelated turn: %v", e)
	}
	if !reflect.DeepEqual(after, snapshot()) {
		t.Fatal("stale turn changed siblings")
	}
	revision, ok, err := sessions.Store().GetArtifactV3Revision("account", "user", request.ArtifactID, request.BaseCommitOID)
	if err != nil || !ok || revision.CommitOID != request.BaseCommitOID {
		t.Fatal("selected revision lost")
	}
	repo, err := pebblestore.OpenArtifactV3Repository(ctx, gitRoot, request.ArtifactID, owner, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := repo.ReadFile(ctx, request.BaseCommitOID, pebblestore.ArtifactV3ManifestFilename)
	if err != nil || string(body) != string(manifest) {
		t.Fatalf("manifest changed: %v", err)
	}
}
