package tool

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifactv2"
	"swarm/packages/swarmd/internal/identity"
)

// Requirement: the Designer executable schema omits destination, policy,
// renderer and legacy identities. Threat: an optional-looking field or legacy
// tool grant could restore redirection or V1 fallback before runtime checks.
func TestArtifactV2AuthorSchemaIsNarrowAndDesignerContractDisablesV1(t *testing.T) {
	definition := artifactV2AuthorDefinition()
	properties := definition.Parameters["properties"].(map[string]any)
	for _, allowed := range []string{"action", "parts", "part_id", "content", "content_base64", "media_type", "expected_base_revision_id", "expected_composition_head_revision"} {
		if _, ok := properties[allowed]; !ok {
			t.Errorf("missing author field %q", allowed)
		}
	}
	for _, forbidden := range []string{"destination", "collection_id", "variant_id", "output_requirements", "animation_profile", "policy", "renderer", "browser", "fps", "provider", "model", "failure_code", "status"} {
		if _, ok := properties[forbidden]; ok {
			t.Errorf("schema exposes forbidden field %q", forbidden)
		}
	}
	found := false
	for _, candidate := range NewRuntime(1).Definitions() {
		if candidate.Name == "artifact_v2_author" {
			found = true
		}
	}
	if !found {
		t.Fatal("artifact_v2_author is not registered")
	}
}

// Requirement: execution requires a trusted context-bound grant and rejects
// caller redirection without invoking the application service.
func TestArtifactV2AuthorRejectsMissingContextAndForbiddenField(t *testing.T) {
	runtime := NewRuntime(1)
	runtime.SetArtifactV2AuthorService(artifactv2.NewAuthorService(nil, nil, nil))
	scope := WorkspaceScope{SessionID: "child", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}}
	if _, err := runtime.executeArtifactV2Author(context.Background(), scope, "call", map[string]any{"action": "inspect_context"}); err == nil || !strings.Contains(err.Error(), "trusted context") {
		t.Fatalf("missing context error=%v", err)
	}
	ctx := WithArtifactV2AuthorRunContext(context.Background(), ArtifactV2AuthorRunContext{Grant: artifactv2.AuthorGrant{ID: "grant", OwnerSessionID: "owner", ProducerSessionID: "child", ProducerRunID: "run", ExpiresAt: 1 << 62, AllowedActions: []string{"inspect_context"}}})
	if err := requireOnlyAuthorFields(map[string]any{"action": "inspect_context", "destination": "legacy"}, "action"); err == nil {
		t.Fatal("forbidden destination field succeeded")
	}
	if _, err := runtime.executeArtifactV2Author(ctx, WorkspaceScope{SessionID: "other", Principal: scope.Principal}, "call", map[string]any{"action": "inspect_context"}); err == nil || !strings.Contains(err.Error(), "producer") {
		t.Fatalf("producer mismatch error=%v", err)
	}
}
