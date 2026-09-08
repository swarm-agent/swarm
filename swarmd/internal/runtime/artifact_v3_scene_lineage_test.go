package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
	"time"
)

// Requirement: PrepareArtifactV3Turn accepts exact scene targets and rejects
// stale/foreign/unknown references without opening a turn. Real Git/Pebble plus
// a fake renderer is the narrowest durable authority test; it is not pixel proof.
func TestArtifactV3SceneLineage(t *testing.T) {
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
	_, _, err = sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "scene-owner", AccountScopeID: "account", UserID: "user", Title: "Scenes", WorkspacePath: root, WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	repoRoot, workRoot, evidenceRoot, err := artifactV3StorageRoots(filepath.Join(root, "data"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := pebblestore.NewArtifactV3Service(sessions.Store(), repoRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := newArtifactV3RuntimeAdapter(service, sessions.Store(), repoRoot, evidenceRoot, pebblestore.ArtifactV3Limits{}, artifactV3RuntimeRenderer{})
	adapter.publish = func(identity.Principal, api.ArtifactV3Artifact, string, string) error { return nil }
	author := tool.NewArtifactV3AuthorService(workRoot, adapter, adapter, adapter)
	profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
	c := &pebblestore.ArtifactV3SceneContract{DurationMS: 8000, Scenes: []pebblestore.ArtifactV3TemporalScene{{SceneID: "opening", StartMS: 0, EndMS: 4000}, {SceneID: "resolve", StartMS: 4000, EndMS: 8000}}}
	g, err := author.PrepareTurn(ctx, tool.ArtifactV3PrepareTurnRequest{AccountScopeID: "account", UserID: "user", OwnerSessionID: "scene-owner", TaskCallID: "initial", GenerationWaveID: "initial-wave", GenerationIndex: 1, GenerationCount: 1, Initial: true, PolicyRevision: "policy", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), AnimationProfile: profile, SceneContract: c})
	if err != nil {
		t.Fatal(err)
	}
	g.ProducerSessionID = "producer"
	g.ProducerRunID = "run"
	p := tool.ArtifactV3AuthorPrincipal{AccountScopeID: "account", UserID: "user", ProducerSessionID: "producer", ProducerRunID: "run"}
	if _, err := adapter.LoadAuthorDraft(tool.WithArtifactV3AuthorRunContext(ctx, tool.ArtifactV3AuthorRunContext{Grant: g}), p, g); err != nil {
		t.Fatal(err)
	}
	m := pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html", AnimationProfile: profile, SceneContract: c}
	for _, scene := range c.Scenes {
		scene := scene
		m.Parts = append(m.Parts, pebblestore.ArtifactV3Part{ID: scene.SceneID, Label: scene.SceneID, Temporal: &scene, Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#canvas"}})
	}
	body, _ := json.Marshal(m)
	for path, data := range map[string][]byte{pebblestore.ArtifactV3ManifestFilename: body, "index.html": []byte(`<!doctype html><html><head><title>Scenes</title></head><body><canvas id="canvas"></canvas>` + nativePreviewAnimationManifest + `</body></html>`)} {
		if err := author.Create(ctx, p, g, path, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := author.ReconcileParts(ctx, p, g, m.Parts); err != nil {
		t.Fatal("initial reconcile", err)
	}
	gate, err := author.BuildPreview(ctx, p, g)
	if err != nil || !gate.Ready {
		t.Fatalf("gate %+v %v", gate, err)
	}
	done, err := author.Finish(ctx, p, g)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := sessions.Store().GetArtifactV3Repository("account", "user", g.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := adapter.GetRevision(ctx, api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "scene-owner", g.ArtifactID, "revision-"+done.Revision.CommitOID)
	if err != nil || len(revision.Validation.Scenes) != 2 {
		t.Fatalf("scene evidence lost %+v %v", revision, err)
	}
	request := tool.ArtifactV3PrepareTurnRequest{AccountScopeID: "account", UserID: "user", OwnerSessionID: "scene-owner", TaskCallID: "reuse", GenerationWaveID: "reuse-wave", GenerationIndex: 1, GenerationCount: 1, ArtifactID: g.ArtifactID, BaseCommitOID: done.Revision.CommitOID, ProjectionSeq: original.EventSeq, PolicyRevision: "policy", RevisionIntent: pebblestore.ArtifactV3RevisionFocusedParts, TargetPartIDs: []string{"resolve"}, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	for _, mutate := range []func(*tool.ArtifactV3PrepareTurnRequest){func(r *tool.ArtifactV3PrepareTurnRequest) { r.UserID = "foreign" }, func(r *tool.ArtifactV3PrepareTurnRequest) { r.BaseCommitOID = strings.Repeat("f", 40) }, func(r *tool.ArtifactV3PrepareTurnRequest) { r.ProjectionSeq++ }, func(r *tool.ArtifactV3PrepareTurnRequest) { r.TargetPartIDs = []string{"missing"} }} {
		bad := request
		mutate(&bad)
		if _, err := adapter.PrepareArtifactV3Turn(ctx, bad); err == nil {
			t.Fatal("bad reference accepted")
		}
		after, _, _ := sessions.Store().GetArtifactV3Repository("account", "user", g.ArtifactID)
		if !reflect.DeepEqual(original, after) {
			t.Fatal("rejected reference mutated repository")
		}
	}
	follow, err := adapter.PrepareArtifactV3Turn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if follow.BaseCommitOID != done.Revision.CommitOID || !reflect.DeepEqual(follow.TargetPartIDs, []string{"resolve"}) || !reflect.DeepEqual(follow.SceneContract, c) {
		t.Fatal("source lineage lost")
	}
	after, _, _ := sessions.Store().GetArtifactV3Repository("account", "user", g.ArtifactID)
	hydrated, err := adapter.GetArtifact(ctx, api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}, "scene-owner", g.ArtifactID)
	if err != nil || len(hydrated.GenerationGroups) != 2 || len(hydrated.Generations) != 2 {
		t.Fatalf("generation hydration lost: %+v %v", hydrated, err)
	}
	if hydrated.GenerationGroups[0].Members[0].CommitOID != done.Revision.CommitOID {
		t.Fatal("initial member lost exact publication")
	}
	if hydrated.GenerationGroups[1].Members[0].BaseCommitOID != done.Revision.CommitOID || hydrated.GenerationGroups[1].Members[0].ArtifactID != g.ArtifactID {
		t.Fatal("generation broke source chain")
	}
	if after.HeadCommitOID != original.HeadCommitOID {
		t.Fatal("reference selected head")
	}
}
