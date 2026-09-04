package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
)

// Requirement: ordinary primary Swarm can turn one exact native V3 head and a
// targeted Part request into one complete candidate without moving the head.
// Threat: routing follow-ups through Designer/V1/V2 or auto-selecting before
// explicit user choice would break the no-Designer turn and selection contract.
func TestManageArtifactReviseV3CreatesExactBaseCandidateWithoutSelecting(t *testing.T) {
	repository := &directArtifactV3RepoFake{}
	author := NewArtifactV3AuthorService(t.TempDir(), repository, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(author)
	scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "session-1", RunID: "run-1"})
	html := `<!doctype html><html><body><main id="hero">Product hero</main><section id="pricing">Team $29</section><footer id="footer">Get started</footer></body></html>`
	createArgs, _ := json.Marshal(map[string]any{"action": "create", "filename": "index.html", "media_type": "text/html", "content": html, "parts": []map[string]any{{"id": "hero", "label": "Hero", "kind": "semantic"}, {"id": "pricing", "label": "Pricing", "kind": "semantic"}, {"id": "footer", "label": "Footer", "kind": "semantic"}}})
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "create", Name: "manage_artifact", Arguments: string(createArgs)}); err != nil {
		t.Fatal(err)
	}
	readArgs, _ := json.Marshal(map[string]any{"action": "read_v3", "artifact_v3_reference": map[string]any{"session_id": "session-1", "artifact_id": "artifact-direct", "revision_ref": "revision-" + strings.Repeat("a", 40)}})
	readOutput, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "read-v3", Name: "manage_artifact", Arguments: string(readArgs)})
	if err != nil || !strings.Contains(readOutput, `"content":"\u003c!doctype html`) || !strings.Contains(readOutput, `"id":"pricing"`) || strings.Contains(readOutput, "collection_id") {
		t.Fatalf("read output=%s err=%v", readOutput, err)
	}
	revisedHTML := strings.Replace(html, "Team $29", "TARGETED PRICING TURN — Team $29", 1)
	reviseArgs, _ := json.Marshal(map[string]any{
		"action": "revise_v3", "artifact_v3_reference": map[string]any{"session_id": "session-1", "artifact_id": "artifact-direct", "revision_ref": "revision-" + strings.Repeat("a", 40)},
		"target_part_ids": []string{"pricing"}, "content": revisedHTML,
	})
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "revise", Name: "manage_artifact", Arguments: string(reviseArgs)})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.submits) != 2 || repository.submits[1].Initial || repository.submits[1].BaseCommitOID != strings.Repeat("a", 40) || len(repository.selected) != 0 {
		t.Fatalf("revise submits=%#v selected=%#v", repository.submits, repository.selected)
	}
	if !strings.Contains(output, `"status":"awaiting_selection"`) || !strings.Contains(output, `"target_part_ids":["pricing"]`) || !strings.Contains(output, `"base_revision_ref":"revision-`+strings.Repeat("a", 40)+`"`) || strings.Contains(output, "collection_id") || strings.Contains(output, "variant_id") {
		t.Fatalf("output=%s", output)
	}

	wrongTarget := strings.Replace(string(reviseArgs), `"pricing"`, `"missing"`, 1)
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "missing", Name: "manage_artifact", Arguments: wrongTarget}); err == nil || !strings.Contains(err.Error(), "absent from the exact base") {
		t.Fatalf("missing target error=%v", err)
	}
	wrongSession := strings.Replace(string(reviseArgs), `"session-1"`, `"foreign"`, 1)
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "foreign", Name: "manage_artifact", Arguments: wrongSession}); err == nil || !strings.Contains(err.Error(), "current authenticated session") {
		t.Fatalf("foreign session error=%v", err)
	}
	missingPartHTML := strings.Replace(revisedHTML, `id="footer"`, `id="removed"`, 1)
	missingPartPayload := map[string]any{
		"action": "revise_v3", "artifact_v3_reference": map[string]any{"session_id": "session-1", "artifact_id": "artifact-direct", "revision_ref": "revision-" + strings.Repeat("a", 40)},
		"target_part_ids": []string{"pricing"}, "content": missingPartHTML,
	}
	missingPartArgs, _ := json.Marshal(missingPartPayload)
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "part-drift", Name: "manage_artifact", Arguments: string(missingPartArgs)}); err == nil || !strings.Contains(err.Error(), `preserve stable Part "footer"`) {
		t.Fatalf("part drift error=%v", err)
	}
}
