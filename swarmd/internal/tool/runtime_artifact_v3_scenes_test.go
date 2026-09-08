package tool

import (
	"context"
	"encoding/json"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: a producer cannot downgrade parent-required scene policy at the
// BuildPreview boundary, even if a builder/previewer reports success. This
// service test checks gate and submission postconditions, not browser semantics.
func TestArtifactV3ScenePolicyCannotDowngrade(t *testing.T) {
	c, err := ParseArtifactV3SceneContract(map[string]any{"duration_ms": 8000, "scenes": []any{map[string]any{"scene_id": "one", "start_ms": 0, "end_ms": 4000}, map[string]any{"scene_id": "two", "start_ms": 4000, "end_ms": 8000}}})
	if err != nil {
		t.Fatal(err)
	}
	m := pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html"}
	b, _ := json.Marshal(m)
	repo := &artifactV3AuthorRepoFake{base: map[string][]byte{pebblestore.ArtifactV3ManifestFilename: b, "index.html": []byte("<main id='main'></main>")}}
	s := NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
	g := fullArtifactV3Grant()
	g.SceneContract = c
	gate, err := s.BuildPreview(context.Background(), artifactV3Principal(), g)
	if err != nil {
		t.Fatal(err)
	}
	if gate.Ready {
		t.Fatal("downgraded scene policy marked ready")
	}
	if _, err := s.Finish(context.Background(), artifactV3Principal(), g); err == nil || len(repo.submits) != 0 {
		t.Fatal("downgraded policy published")
	}
}

// Requirement: the shared parser rejects malformed policy before allocation;
// initial reconciliation is available but focused revisions cannot change IDs.
func TestArtifactV3SceneParserAndInitialCapability(t *testing.T) {
	for _, raw := range []any{nil, map[string]any{"duration_ms": 100, "scenes": []any{}}, map[string]any{"duration_ms": 100, "scenes": []any{map[string]any{"scene_id": "a", "start_ms": 1, "end_ms": 100}}}, map[string]any{"duration_ms": 100, "scenes": []any{map[string]any{"scene_id": "a", "start_ms": 0, "end_ms": 100, "foreign": true}}}} {
		if _, err := ParseArtifactV3SceneContract(raw); err == nil {
			t.Fatal("accepted malformed policy")
		}
	}
	g := fullArtifactV3Grant()
	g.Initial = true
	if !g.Allows(artifactV3ActionReconcile) {
		t.Fatal("initial reconciliation unavailable")
	}
	g.Initial = false
	g.RevisionIntent = pebblestore.ArtifactV3RevisionFocusedParts
	if g.Allows(artifactV3ActionReconcile) {
		t.Fatal("focused structural mutation authorized")
	}
}

// Requirement: direct native create hands required policy and profile to trusted
// allocation together, and accepts multiple temporal IDs on one real selector.
func TestArtifactV3SceneDirectCreateHandoff(t *testing.T) {
	repo := &directArtifactV3RepoFake{}
	r := NewRuntime(1)
	r.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))
	scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "session-1", RunID: "run-1"})
	args := map[string]any{"action": "create", "media_type": "text/html", "content": `<html><body><main id="canvas">Output</main></body></html>` + nativeAnimationTestManifest, "animation_profile": map[string]any{"profile": "motion_ui"}, "scene_contract": map[string]any{"duration_ms": 4000, "scenes": []any{map[string]any{"scene_id": "one", "start_ms": 0, "end_ms": 2000}, map[string]any{"scene_id": "two", "start_ms": 2000, "end_ms": 4000}}}}
	parts := []pebblestore.ArtifactV3Part{}
	for _, scene := range []pebblestore.ArtifactV3TemporalScene{{SceneID: "one", StartMS: 0, EndMS: 2000}, {SceneID: "two", StartMS: 2000, EndMS: 4000}} {
		scene := scene
		parts = append(parts, pebblestore.ArtifactV3Part{ID: scene.SceneID, Label: scene.SceneID, Temporal: &scene, Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#canvas"}})
	}
	args["native_parts"] = parts
	b, _ := json.Marshal(args)
	if _, err := r.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "scene", Name: "manage_artifact", Arguments: string(b)}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 || repo.turns[0].AnimationProfile == nil || repo.turns[0].SceneContract == nil || len(repo.turns[0].SceneContract.Scenes) != 2 {
		t.Fatal("allocation policy lost")
	}
	var m pebblestore.ArtifactV3Manifest
	if len(repo.submits) != 1 || json.Unmarshal(repo.submits[0].Project[pebblestore.ArtifactV3ManifestFilename], &m) != nil || len(m.Parts) != 2 || m.Parts[1].Temporal == nil {
		t.Fatal("scene parts lost")
	}
}
