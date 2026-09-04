package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
)

type directArtifactV3RepoFake struct {
	artifactV3AuthorRepoFake
}

func (f *directArtifactV3RepoFake) PrepareArtifactV3Turn(_ context.Context, request ArtifactV3PrepareTurnRequest) (ArtifactV3AuthorGrant, error) {
	return ArtifactV3AuthorGrant{
		ID: "direct-grant", ArtifactID: "artifact-direct", OwnerSessionID: request.OwnerSessionID,
		TurnID: "turn-direct", CandidateID: "candidate-direct", PolicyRevision: request.PolicyRevision,
		Initial: request.Initial, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
		AllowedActions: []string{artifactV3ActionInspect, artifactV3ActionList, artifactV3ActionRead, artifactV3ActionCreate, artifactV3ActionEdit, artifactV3ActionRename, artifactV3ActionDelete, artifactV3ActionDiff, artifactV3ActionBuild, artifactV3ActionFinish},
	}, nil
}

func (f *directArtifactV3RepoFake) FailArtifactV3Turn(context.Context, ArtifactV3TurnFailure) error {
	return nil
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
	arguments, err := json.Marshal(map[string]any{"action": "create", "filename": "index.html", "media_type": "text/html", "content": html, "collection_name": "Static product page"})
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
	if !strings.Contains(output, `"artifact_v3"`) || !strings.Contains(output, `"part_count":3`) || strings.Contains(output, "collection_id") || strings.Contains(output, "variant_id") {
		t.Fatalf("output=%s", output)
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
		"non-html":     `{"action":"create","filename":"note.txt","media_type":"text/plain","content":"text"}`,
		"missing-part": `{"action":"create","filename":"index.html","media_type":"text/html","content":"<!doctype html><html><body><div>no stable part</div></body></html>"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: name, Name: "manage_artifact", Arguments: arguments}); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
