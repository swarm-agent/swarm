package tool

import (
	"context"
	"encoding/json"
	"strings"
	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: direct source handoffs preserve conventional non-index projects.
// Threat: assuming index.html loses the true entrypoint and binary assets.
// directArtifactV3ProjectManifest is the narrowest bounded read admission layer.
func TestNativeHandoffMultiFileManifest(t *testing.T) {
	manifest := pebblestore.ArtifactV3Manifest{Entrypoint: "pages/main.html", Parts: []pebblestore.ArtifactV3Part{{ID: "asset", Label: "Asset", Locator: pebblestore.ArtifactV3Locator{Kind: "file", Path: "assets/logo.bin"}}}}
	encoded, _ := json.Marshal(manifest)
	project := map[string][]byte{pebblestore.ArtifactV3ManifestFilename: encoded, "pages/main.html": []byte("<main>Fictional</main>"), "assets/logo.bin": {0, 255, 1}}
	got, err := directArtifactV3ProjectManifest(project)
	if err != nil || got.Entrypoint != manifest.Entrypoint || got.Parts[0].Locator.Kind != "file" {
		t.Fatalf("manifest=%+v err=%v", got, err)
	}
	if project["assets/logo.bin"][1] != 255 {
		t.Fatal("asset changed")
	}
	delete(project, manifest.Entrypoint)
	if _, err := directArtifactV3ProjectManifest(project); err == nil {
		t.Fatal("missing entrypoint accepted")
	}
}

// Requirement: all sibling content/identities are admitted before allocation.
// Threat: an invalid last sibling leaves earlier side effects. This parser test
// exercises the earliest admission gate; integration preflight owns Part checks.
func TestNativeHandoffRejectsInvalidLastSibling(t *testing.T) {
	for _, last := range []any{
		map[string]any{"candidate_index": 2, "content": strings.Repeat("x", manageArtifactMaxCreateBytes+1)},
		map[string]any{"candidate_index": 2, "content": string([]byte{255})},
		map[string]any{"candidate_index": 1, "content": "duplicate"},
	} {
		result, err := parseDirectArtifactV3Alternatives([]any{map[string]any{"candidate_index": 1, "content": "valid"}, last})
		if err == nil || result != nil {
			t.Fatalf("partial admission: %+v %v", result, err)
		}
	}
}

// Requirement: malformed final Part layout rejects the entire wave before any
// PrepareTurn. This direct adapter test observes allocation/submission counts,
// preventing a parser-only success from hiding partial durable work.
func TestNativeHandoffLastPartPreflightNoAllocation(t *testing.T) {
	repo := &directArtifactV3RepoFake{}
	manifest := pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "index.html"}
	encoded, _ := json.Marshal(manifest)
	html := `<main id="hero">Hero</main><section id="pricing">Pricing</section><footer id="footer">Footer</footer>`
	repo.submits = []ArtifactV3SubmitRequest{{Project: map[string][]byte{"index.html": []byte(html), pebblestore.ArtifactV3ManifestFilename: encoded}}}
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))
	principal := artifact.Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-1", RunID: "run-1"}
	args := map[string]any{"action": "revise_v3", "artifact_v3_reference": map[string]any{"session_id": "session-1", "artifact_id": "artifact-direct", "revision_ref": "revision-" + strings.Repeat("a", 40)}, "target_part_ids": []any{"pricing"}, "turn_key": "preflight", "alternatives": []any{map[string]any{"candidate_index": 1, "content": html}, map[string]any{"candidate_index": 2, "content": strings.Replace(html, `id="footer"`, `id="missing"`, 1)}}}
	if _, err := runtime.reviseDirectArtifactV3HTML(context.Background(), WorkspaceScope{SessionID: "session-1"}, principal, "call", args); err == nil {
		t.Fatal("invalid final Part accepted")
	}
	if len(repo.turns) != 0 || len(repo.submits) != 1 || len(repo.selected) != 0 {
		t.Fatal("preflight mutated state")
	}
}

type nativeHandoffRepo struct{ directArtifactV3RepoFake }

func (r *nativeHandoffRepo) ResolveArtifactV3SelectedSource(_ context.Context, account, user, session, id, commit string, seq uint64) (pebblestore.ArtifactV3SelectedSource, error) {
	if account != "account-1" || user != "user-1" || session != "session-1" {
		return pebblestore.ArtifactV3SelectedSource{}, pebblestore.ErrArtifactV3Unauthorized
	}
	return pebblestore.ArtifactV3SelectedSource{SessionID: session, ArtifactID: id, CommitOID: strings.Repeat("a", 40), RevisionRef: "revision-" + strings.Repeat("a", 40), ProjectionSeq: 7}, nil
}
func (r *nativeHandoffRepo) ListArtifactV3SelectedSources(ctx context.Context, account, user, session string, limit int) ([]pebblestore.ArtifactV3SelectedSource, error) {
	source, err := r.ResolveArtifactV3SelectedSource(ctx, account, user, session, "fictional", "", 0)
	return []pebblestore.ArtifactV3SelectedSource{source}, err
}

// Requirement: discovery returns both exact native identities without allocating
// or selecting. The adapter layer proves tuple copying and principal propagation;
// integrated store tests own actual account/CAS authorization evidence.
func TestNativeHandoffDiscoveryTuple(t *testing.T) {
	repo := &nativeHandoffRepo{}
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))
	principal := artifact.Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-1"}
	args := map[string]any{"action": "source_v3", "artifact_id": "fictional"}
	output, err := runtime.discoverDirectArtifactV3(context.Background(), WorkspaceScope{SessionID: "session-1"}, principal, args)
	if err != nil {
		t.Fatal(err)
	}
	source := output["artifact_v3_source"].(map[string]any)
	reference := output["reference"].(map[string]any)
	if source["projection_seq"] != uint64(7) || source["commit_oid"] != strings.Repeat("a", 40) || source["session_id"] != reference["session_id"] || source["artifact_id"] != reference["artifact_id"] || reference["revision_ref"] != "revision-"+source["commit_oid"].(string) {
		t.Fatalf("incomplete tuple: %+v", output)
	}
	principal.AccountScopeID = "foreign"
	if _, err := runtime.discoverDirectArtifactV3(context.Background(), WorkspaceScope{SessionID: "session-1"}, principal, args); err == nil {
		t.Fatal("foreign principal accepted")
	}
	if len(repo.turns) != 0 || len(repo.selected) != 0 {
		t.Fatal("discovery mutated state")
	}
}

func (r *nativeHandoffRepo) SelectArtifactV3Exact(_ context.Context, account, user, session, id, turn, candidate, head, request string, seq uint64) (ArtifactV3Revision, error) {
	if head != "revision-"+strings.Repeat("a", 40) || seq != 7 {
		return ArtifactV3Revision{}, pebblestore.ErrArtifactV3Conflict
	}
	r.selected = append(r.selected, candidate)
	return ArtifactV3Revision{CommitOID: strings.Repeat("b", 40)}, nil
}

// Requirement: selection forwards explicit caller CAS, never refreshed latest
// values. This adapter fake rejects stale CAS and observes zero selection effects;
// the canonical runtime SelectCandidate/store tests own persistence proof.
func TestNativeHandoffExplicitStaleSelection(t *testing.T) {
	repo := &nativeHandoffRepo{}
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))
	principal := artifact.Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-1"}
	args := map[string]any{"action": "select_v3", "artifact_id": "fictional", "turn_id": "turn", "candidate_id": "candidate", "expected_head": "revision-" + strings.Repeat("a", 40), "expected_turn_revision": float64(6)}
	scope := WorkspaceScope{SessionID: "session-1"}
	if _, err := runtime.selectDirectArtifactV3(context.Background(), scope, principal, "select", args); err == nil || len(repo.selected) != 0 {
		t.Fatal("stale selection mutated head")
	}
	args["expected_turn_revision"] = float64(7)
	if _, err := runtime.selectDirectArtifactV3(context.Background(), scope, principal, "select", args); err != nil || len(repo.selected) != 1 {
		t.Fatalf("explicit selection: %v", err)
	}
}
