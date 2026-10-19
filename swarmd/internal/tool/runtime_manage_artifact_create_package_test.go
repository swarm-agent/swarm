package tool

import (
	"context"
	"testing"

	"swarm/packages/swarmd/internal/identity"
)

// retiredCreatePackageAuthority embeds a nil ArtifactAuthority: any authority
// method call panics, so the test also proves no artifact write is attempted.
type retiredCreatePackageAuthority struct {
	ArtifactAuthority
}

// Requirement: manage_artifact create_package is retired and must return its
// exact retirement error for an authenticated, fully configured caller.
// Threat: the removed V1 package-write path is restored or the action falls
// through to another action and writes an artifact. Owner:
// Runtime.executeManageArtifact; calling it with a trusted run context, an
// authenticated principal and a configured (panicking) authority is the
// narrowest layer that reaches the create_package branch itself.
func TestManageArtifactCreatePackageIsRetired(t *testing.T) {
	r := NewRuntime(1)
	r.SetArtifactAuthority(retiredCreatePackageAuthority{})
	scope := WorkspaceScope{PrimaryPath: t.TempDir(), SessionID: "session-a", Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account", SessionID: "session-a"}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "session-a"})
	_, err := r.executeManageArtifact(ctx, scope, "call-1", map[string]any{
		"action":  "create_package",
		"entries": []any{map[string]any{"name": "index.html", "content": "<!doctype html><html><body>x</body></html>"}},
	})
	const want = "manage_artifact create_package is retired; managed project authoring uses Artifact V3"
	if err == nil || err.Error() != want {
		t.Fatalf("create_package err = %v, want %q", err, want)
	}
}
