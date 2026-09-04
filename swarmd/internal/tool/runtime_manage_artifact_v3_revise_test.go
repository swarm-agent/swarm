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
	siblingHTML := strings.Replace(html, "Team $29", "ALTERNATE SIBLING — Team $29", 1)
	siblingArgs, _ := json.Marshal(map[string]any{
		"action": "revise_v3", "artifact_v3_reference": map[string]any{"session_id": "session-1", "artifact_id": "artifact-direct", "revision_ref": "revision-" + strings.Repeat("a", 40)},
		"target_part_ids": []string{"pricing"}, "turn_key": "pricing-alternatives", "candidate_index": 2, "content": siblingHTML,
	})
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "sibling", Name: "manage_artifact", Arguments: string(siblingArgs)}); err != nil {
		t.Fatal(err)
	}
	if len(repository.turns) < 3 || repository.turns[len(repository.turns)-1].TaskCallID != "direct-revise:pricing-alternatives" || repository.turns[len(repository.turns)-1].CandidateIndex != 2 || repository.turns[len(repository.turns)-1].BaseCommitOID != strings.Repeat("a", 40) || len(repository.selected) != 0 {
		t.Fatalf("sibling turn=%#v selected=%#v", repository.turns, repository.selected)
	}

	// Requirement: one provider tool call can durably produce every requested
	// exact-base sibling before it reports success. Threat: provider completion
	// after only one of multiple calls leaves a turn below requested cardinality.
	firstAlternative := strings.Replace(html, "Team $29", "ATOMIC OPTION ONE — Team $29", 1)
	secondAlternative := strings.Replace(html, "Team $29", "ATOMIC OPTION TWO — Team $29", 1)
	alternativesArgs, _ := json.Marshal(map[string]any{
		"action": "revise_v3", "artifact_v3_reference": map[string]any{"session_id": "session-1", "artifact_id": "artifact-direct", "revision_ref": "revision-" + strings.Repeat("a", 40)},
		"target_part_ids": []string{"pricing"}, "turn_key": "atomic-pricing-alternatives", "alternatives": []map[string]any{{"candidate_index": 1, "content": firstAlternative}, {"candidate_index": 2, "content": secondAlternative}},
	})
	atomicOutput, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "atomic-siblings", Name: "manage_artifact", Arguments: string(alternativesArgs)})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.turns) < 5 || repository.turns[len(repository.turns)-2].TaskCallID != "direct-revise:atomic-pricing-alternatives" || repository.turns[len(repository.turns)-2].CandidateIndex != 1 || repository.turns[len(repository.turns)-1].TaskCallID != "direct-revise:atomic-pricing-alternatives" || repository.turns[len(repository.turns)-1].CandidateIndex != 2 || len(repository.selected) != 0 {
		t.Fatalf("atomic turns=%#v selected=%#v", repository.turns, repository.selected)
	}
	for _, want := range []string{`"candidate_count":2`, `"candidate_index":1`, `"candidate_index":2`, `"turn_key":"atomic-pricing-alternatives"`, `"status":"awaiting_selection"`} {
		if !strings.Contains(atomicOutput, want) {
			t.Fatalf("atomic output lacks %s: %s", want, atomicOutput)
		}
	}
	invalidAlternatives := strings.Replace(string(alternativesArgs), `"candidate_index":2`, `"candidate_index":1`, 1)
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "duplicate-siblings", Name: "manage_artifact", Arguments: invalidAlternatives}); err == nil || !strings.Contains(err.Error(), "candidate_index") {
		t.Fatalf("duplicate alternatives error=%v", err)
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
