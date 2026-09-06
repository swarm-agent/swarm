package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
)

// This fake implements the durable adapter contract, including trusted first
// producer binding. Tool tests prove dispatch and payloads, not Pebble recovery.
type primaryDraftRepo struct {
	directArtifactV3RepoFake
	grants map[string]ArtifactV3AuthorGrant
	drafts map[string]ArtifactV3AuthorDraft
}

func (f *primaryDraftRepo) LoadAuthorDraft(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant) (ArtifactV3AuthorDraft, error) {
	trusted, ok := ArtifactV3GrantFromContext(ctx)
	if !ok || trusted.ID != g.ID || trusted.ProducerRunID != p.ProducerRunID {
		return ArtifactV3AuthorDraft{}, ErrArtifactV3AuthorUnauthorized
	}
	if f.grants == nil {
		f.grants = map[string]ArtifactV3AuthorGrant{}
		f.drafts = map[string]ArtifactV3AuthorDraft{}
	}
	g.AccountScopeID, g.UserID = p.AccountScopeID, p.UserID
	f.grants[g.ID] = g
	return f.drafts[g.ID], nil
}
func (f *primaryDraftRepo) SaveAuthorDraft(ctx context.Context, p ArtifactV3AuthorPrincipal, g ArtifactV3AuthorGrant, d ArtifactV3AuthorDraft) (ArtifactV3AuthorDraft, error) {
	if _, err := f.LoadAuthorDraft(ctx, p, g); err != nil {
		return d, err
	}
	if d.Sequence != f.drafts[g.ID].Sequence {
		return d, ErrArtifactV3AuthorConflict
	}
	d.Sequence++
	f.drafts[g.ID] = d
	return d, nil
}
func (f *primaryDraftRepo) ResolveArtifactV3DirectDraft(_ context.Context, p ArtifactV3AuthorPrincipal, h ArtifactV3DraftHandle) (ArtifactV3AuthorGrant, error) {
	g, ok := f.grants[h.GrantID]
	if !ok || directArtifactV3Handle(g) != h || g.AccountScopeID != p.AccountScopeID || g.UserID != p.UserID || g.ProducerRunID != p.ProducerRunID {
		return ArtifactV3AuthorGrant{}, ErrArtifactV3AuthorUnauthorized
	}
	return g, nil
}

// Requirement: primary create failures retain exact source, then authenticated
// file operations repair and publish the same candidate. Threats: discarded
// drafts, forged handles, missing trusted context, stale gates and implicit head
// selection. Runtime dispatch plus a durable-adapter fake is the narrow layer
// proving wire serialization and AuthorService integration without a browser.
func TestPrimaryArtifactDraftRepairDispatch(t *testing.T) {
	repo := &primaryDraftRepo{}
	builder := &artifactV3BuilderFake{failFirst: true}
	r := NewRuntime(1)
	r.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repo, builder, &artifactV3PreviewerFake{}))
	scope := WorkspaceScope{SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: scope.SessionID, RunID: "run-1"})
	invoke := func(args map[string]any) (map[string]any, error) {
		t.Helper()
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		out, err := r.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "draft-call", Name: "manage_artifact", Arguments: string(b)})
		if err != nil {
			return nil, err
		}
		var result map[string]any
		if err = json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		return result["artifact_v3"].(map[string]any), nil
	}
	html := `<html><body><main id="hero">Original</main><section id="pricing">Unchanged</section><footer id="footer">Footer</footer></body></html>`
	created, err := invoke(map[string]any{"action": "create", "filename": "index.html", "content": html})
	if err != nil || created["status"] != "fixing" || len(repo.submits) != 0 {
		t.Fatalf("create=%v err=%v", created, err)
	}
	handle := created["draft_handle"].(map[string]any)
	op := func(operation map[string]any) (map[string]any, error) {
		return invoke(map[string]any{"action": "author_v3", "draft_handle": handle, "operation": operation})
	}
	if _, err = op(map[string]any{"action": "build_preview"}); err == nil || !strings.Contains(err.Error(), ErrArtifactV3AuthorNotReady.Error()) {
		t.Fatalf("unchanged retry: %v", err)
	}
	if builder.calls != 1 {
		t.Fatal("unchanged retry executed builder")
	}
	read, err := op(map[string]any{"action": "read_file", "path": "index.html"})
	if err != nil || read["result"].(map[string]any)["Content"] != html {
		t.Fatalf("retained read=%v err=%v", read, err)
	}
	for _, field := range []string{"grant_id", "candidate_id", "session_id"} {
		original := handle[field]
		handle[field] = "foreign"
		if _, err = op(map[string]any{"action": "read_file", "path": "index.html"}); err == nil {
			t.Fatalf("accepted foreign %s", field)
		}
		handle[field] = original
	}
	grantID := handle["grant_id"].(string)
	grant := repo.grants[grantID]
	expired := grant
	expired.ExpiresAt = 1
	repo.grants[grantID] = expired
	if _, err = op(map[string]any{"action": "read_file", "path": "index.html"}); err == nil || err.Error() != ErrArtifactV3AuthorExpired.Error() {
		t.Fatalf("expiry: %v", err)
	}
	repo.grants[grantID] = grant
	if _, err = op(map[string]any{"action": "delete_file", "path": "swarm-artifact.json"}); err == nil || err.Error() != ErrArtifactV3AuthorLocked.Error() {
		t.Fatalf("manifest lock: %v", err)
	}
	if len(repo.submits) != 0 || string(repo.drafts[grantID].Project["index.html"]) != html {
		t.Fatal("rejection changed source or published")
	}
	if _, err = op(map[string]any{"action": "edit_file", "path": "index.html", "old_string": "Original", "new_string": "Corrected"}); err != nil {
		t.Fatal(err)
	}
	if _, err = op(map[string]any{"action": "finish_turn"}); err == nil || !strings.Contains(err.Error(), ErrArtifactV3AuthorNotReady.Error()) {
		t.Fatalf("stale finish: %v", err)
	}
	if _, err = op(map[string]any{"action": "build_preview"}); err != nil {
		t.Fatal(err)
	}
	finished, err := op(map[string]any{"action": "finish_turn"})
	if err != nil || finished["status"] != "ready" || len(repo.submits) != 1 {
		t.Fatalf("finish=%v err=%v", finished, err)
	}
	if string(repo.submits[0].Project["index.html"]) != strings.Replace(html, "Original", "Corrected", 1) {
		t.Fatal("unrelated source changed")
	}
	manifest := string(repo.submits[0].Project["swarm-artifact.json"])
	for _, id := range []string{"hero", "pricing", "footer"} {
		if !strings.Contains(manifest, `"id":"`+id+`"`) {
			t.Fatal("stable part lost")
		}
	}
	if len(repo.selected) != 0 {
		t.Fatal("unexpected selection")
	}
	second, err := invoke(map[string]any{"action": "begin_v3", "artifact_v3_reference": finished["media_inspect_reference"], "target_part_ids": []string{"hero"}})
	if err != nil || second["status"] != "editing" {
		t.Fatalf("second edit=%v err=%v", second, err)
	}
	handle = second["draft_handle"].(map[string]any)
	if _, err = op(map[string]any{"action": "edit_file", "path": "index.html", "old_string": "Corrected", "new_string": "Second"}); err != nil {
		t.Fatal(err)
	}
	if _, err = op(map[string]any{"action": "build_preview"}); err != nil {
		t.Fatal(err)
	}
	finished, err = op(map[string]any{"action": "finish_turn"})
	if err != nil || finished["status"] != "awaiting_selection" || len(repo.submits) != 2 || len(repo.selected) != 0 {
		t.Fatalf("second finish=%v err=%v", finished, err)
	}
}
