package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type directArtifactV3RepoFake struct {
	artifactV3AuthorRepoFake
	turns    []ArtifactV3PrepareTurnRequest
	selected []string
}

func (f *directArtifactV3RepoFake) PrepareArtifactV3Turn(_ context.Context, request ArtifactV3PrepareTurnRequest) (ArtifactV3AuthorGrant, error) {
	f.turns = append(f.turns, request)
	index := len(f.turns)
	candidateIndex := request.CandidateIndex
	if candidateIndex < 1 {
		candidateIndex = 1
	}
	return ArtifactV3AuthorGrant{
		ID: fmt.Sprintf("direct-grant-%d", index), ArtifactID: "artifact-direct", OwnerSessionID: request.OwnerSessionID,
		TurnID: "turn-" + request.TaskCallID, CandidateID: "candidate-" + request.TaskCallID + "-" + fmt.Sprint(candidateIndex), PolicyRevision: request.PolicyRevision,
		BaseCommitOID: request.BaseCommitOID, Initial: request.Initial, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
		AllowedActions: []string{artifactV3ActionInspect, artifactV3ActionList, artifactV3ActionRead, artifactV3ActionCreate, artifactV3ActionEdit, artifactV3ActionRename, artifactV3ActionDelete, artifactV3ActionDiff, artifactV3ActionBuild, artifactV3ActionFinish},
	}, nil
}

func (f *directArtifactV3RepoFake) FailArtifactV3Turn(context.Context, ArtifactV3TurnFailure) error {
	return nil
}

func (f *directArtifactV3RepoFake) MaterializeBase(_ context.Context, _, _, destination string) error {
	if len(f.submits) == 0 {
		return errors.New("missing direct Artifact V3 base")
	}
	for path, body := range f.submits[0].Project {
		target := filepath.Join(destination, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, body, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (f *directArtifactV3RepoFake) ReadArtifactV3DirectRevision(_ context.Context, _, _, _, _, _ string) (map[string][]byte, []pebblestore.ArtifactV3Part, error) {
	if len(f.submits) == 0 {
		return nil, nil, errors.New("missing direct Artifact V3 revision")
	}
	return artifactV3Clone(f.submits[len(f.submits)-1].Project), []pebblestore.ArtifactV3Part{{ID: "hero", Label: "Hero", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#hero"}}, {ID: "pricing", Label: "Pricing", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#pricing"}}, {ID: "footer", Label: "Footer", Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#footer"}}}, nil
}

func (f *directArtifactV3RepoFake) SelectArtifactV3DirectHead(_ context.Context, _, _, _, _, turnID, candidateID string) (ArtifactV3Revision, error) {
	f.selected = append(f.selected, turnID+":"+candidateID)
	return ArtifactV3Revision{CommitOID: "selected-repair-commit", TreeOID: "selected-repair-tree", ManifestBlobOID: "selected-repair-manifest"}, nil
}

// Requirement: ordinary primary Swarm can publish one complete HTML Artifact V3
// without Designer delegation or any V1/V2 artifact authority. The narrow tool
// layer proves schema exposure, stable Part derivation, the real author
// build/preview/finish sequence, and native exact revision output.
// Threat: removing create from the schema made providers repeatedly substitute
// generate_image, while restoring the retired V1/V2 writer would create a second
// current managed-artifact authority.
func TestManageArtifactCreateUsesDirectArtifactV3HTMLPath(t *testing.T) {
	repository := &directArtifactV3RepoFake{artifactV3AuthorRepoFake: artifactV3AuthorRepoFake{}}
	author := NewArtifactV3AuthorService(t.TempDir(), repository, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(author)
	scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "session-1", RunID: "run-1"})
	html := `<!doctype html><html><body><main id="hero">Product hero</main><section id="pricing">Team $29</section><footer id="footer">Get started</footer></body></html>`
	requestedParts := []map[string]any{{"id": "hero", "label": "Hero", "kind": "semantic"}, {"id": "pricing", "label": "Pricing", "kind": "semantic"}, {"id": "footer", "label": "Footer", "kind": "semantic"}}
	arguments, err := json.Marshal(map[string]any{"action": "create", "filename": "index.html", "media_type": "text/html", "content": html, "collection_name": "Static product page", "parts": requestedParts})
	if err != nil {
		t.Fatal(err)
	}
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "direct-html", Name: "manage_artifact", Arguments: string(arguments)})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.submits) != 1 || !repository.submits[0].Initial || len(repository.submits[0].Project) != 2 {
		t.Fatalf("submits=%#v", repository.submits)
	}
	manifest := string(repository.submits[0].Project["swarm-artifact.json"])
	for _, expected := range []string{`"schema_version":"swarm.artifact/v3"`, `"id":"hero"`, `"id":"pricing"`, `"id":"footer"`} {
		if !strings.Contains(manifest, expected) {
			t.Fatalf("manifest missing %s: %s", expected, manifest)
		}
	}
	if !strings.Contains(output, `"artifact_v3"`) || !strings.Contains(output, `"part_count":3`) || !strings.Contains(output, `"revision_kind":"initial"`) || strings.Contains(output, "collection_id") || strings.Contains(output, "variant_id") {
		t.Fatalf("output=%s", output)
	}
	secondArguments, err := json.Marshal(map[string]any{"action": "create", "filename": "index.html", "media_type": "text/html", "content": strings.ReplaceAll(html, "Product hero", "Revised hero"), "collection_name": "Static product page", "parts": requestedParts})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "second-direct-html", Name: "manage_artifact", Arguments: string(secondArguments)})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.submits) != 2 || repository.submits[1].Initial || repository.submits[1].BaseCommitOID == "" || len(repository.selected) != 1 || !strings.Contains(second, `"commit_oid":"selected-repair-commit"`) || !strings.Contains(second, `"revision_kind":"visual_repair"`) || !strings.Contains(second, `"media_inspect_reference":{"artifact_id":"artifact-direct","revision_ref":"revision-selected-repair-commit"`) {
		t.Fatalf("changed same-run HTML did not create and select one native repair: submits=%#v selected=%#v output=%s", repository.submits, repository.selected, second)
	}
	third, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "third-direct-html", Name: "manage_artifact", Arguments: string(secondArguments)})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.submits) != 2 || len(repository.selected) != 1 || !strings.Contains(third, `"idempotent_replay":true`) || !strings.Contains(third, `do not recreate it`) {
		t.Fatalf("exact same-run replay was not explicit and idempotent: submits=%d selected=%d output=%s", len(repository.submits), len(repository.selected), third)
	}
	changedPartArguments, err := json.Marshal(map[string]any{"action": "create", "filename": "index.html", "media_type": "text/html", "content": strings.ReplaceAll(strings.ReplaceAll(html, "Product hero", "Another hero"), `id="hero"`, `id="hero-renamed"`), "collection_name": "Static product page", "parts": []map[string]any{{"id": "hero-renamed", "label": "Hero", "kind": "semantic"}, {"id": "pricing", "label": "Pricing", "kind": "semantic"}, {"id": "footer", "label": "Footer", "kind": "semantic"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "renamed-part", Name: "manage_artifact", Arguments: string(changedPartArguments)}); err == nil || !strings.Contains(err.Error(), "preserve the prior Artifact V3 stable Part IDs") {
		t.Fatalf("same-run repair changed stable Part identity: %v", err)
	}

	definition := manageArtifactDefinition()
	properties := definition.Parameters["properties"].(map[string]any)
	actions := properties["action"].(map[string]any)["enum"].([]string)
	found := false
	for _, action := range actions {
		if action == "create" {
			found = true
		}
	}
	if !found {
		t.Fatal("manage_artifact schema does not expose direct Artifact V3 create")
	}
	if !strings.Contains(definition.Description, "Do not substitute generate_image") || !strings.Contains(definition.Description, "native Artifact V3") {
		t.Fatalf("manage_artifact description does not distinguish HTML V3 creation from image generation: %s", definition.Description)
	}
}

func TestManageArtifactCreateV3RejectsNonHTMLAndMissingStableParts(t *testing.T) {
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), &directArtifactV3RepoFake{}, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))
	scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "session-1", RunID: "run-1"})
	for name, arguments := range map[string]string{
		"non-html":               `{"action":"create","filename":"note.txt","media_type":"text/plain","content":"text"}`,
		"missing-part":           `{"action":"create","filename":"index.html","media_type":"text/html","content":"<!doctype html><html><body><div>no stable part</div></body></html>"}`,
		"requested-part-missing": `{"action":"create","filename":"index.html","media_type":"text/html","content":"<!doctype html><html><body><main id=\"hero\">Hero</main></body></html>","parts":[{"id":"pricing","label":"Pricing","kind":"semantic"}]}`,
		"ambiguous-extra-parts":  `{"action":"create","filename":"index.html","media_type":"text/html","content":"<!doctype html><html><body><main id=\"page\"><section id=\"hero\">Hero</section><section id=\"pricing\">Team $29</section><footer id=\"footer\">Footer</footer></main></body></html>"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: name, Name: "manage_artifact", Arguments: arguments}); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
