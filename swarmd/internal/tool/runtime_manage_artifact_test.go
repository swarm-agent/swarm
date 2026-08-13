package tool

import (
	"context"
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeArtifactAuthority struct {
	principal artifact.Principal
	created   artifact.CreateInput
	packaged  artifact.CreatePackageInput
	readBody  []byte
	variant   pebblestore.SessionArtifactVariant
	deleted   string
}

func (f *fakeArtifactAuthority) Create(_ context.Context, principal artifact.Principal, input artifact.CreateInput) (pebblestore.SessionArtifactVariant, error) {
	f.principal, f.created = principal, input
	f.variant = pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, Status: pebblestore.SessionArtifactStatusReady, Filename: input.Filename, MediaType: input.MediaType, Size: int64(len(input.Body)), Presentation: input.Presentation}
	return f.variant, nil
}
func (f *fakeArtifactAuthority) CreatePackage(_ context.Context, principal artifact.Principal, input artifact.CreatePackageInput) (pebblestore.SessionArtifactVariant, error) {
	f.principal, f.packaged = principal, input
	f.variant = pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, Status: pebblestore.SessionArtifactStatusReady, Filename: input.Filename, MediaType: "application/zip", Size: 1}
	return f.variant, nil
}
func (f *fakeArtifactAuthority) List(principal artifact.Principal, _ string, _ int) ([]pebblestore.SessionArtifactCollection, error) {
	f.principal = principal
	return []pebblestore.SessionArtifactCollection{{ID: "collection-1", Name: "Drafts"}}, nil
}
func (f *fakeArtifactAuthority) ListVariants(principal artifact.Principal, collectionID string, _ int) ([]pebblestore.SessionArtifactVariant, error) {
	f.principal = principal
	return []pebblestore.SessionArtifactVariant{{ID: "variant-1", CollectionID: collectionID}}, nil
}
func (f *fakeArtifactAuthority) Get(principal artifact.Principal, variantID string) (pebblestore.SessionArtifactVariant, error) {
	f.principal = principal
	if f.variant.ID == "" {
		f.variant = pebblestore.SessionArtifactVariant{ID: variantID, CollectionID: "collection-1", MediaType: "text/plain"}
	}
	return f.variant, nil
}
func (f *fakeArtifactAuthority) Read(_ context.Context, principal artifact.Principal, variantID string, _ int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	variant, err := f.Get(principal, variantID)
	return append([]byte(nil), f.readBody...), variant, err
}
func (f *fakeArtifactAuthority) Select(principal artifact.Principal, _, collectionID, variantID string) (pebblestore.SessionArtifactSelectionReference, error) {
	f.principal = principal
	return pebblestore.SessionArtifactSelectionReference{SessionID: principal.SessionID, CollectionID: collectionID, VariantID: variantID}, nil
}
func (f *fakeArtifactAuthority) DeleteVariant(principal artifact.Principal, _, collectionID, variantID string) error {
	f.principal, f.deleted = principal, collectionID+"/"+variantID
	return nil
}
func (f *fakeArtifactAuthority) DeleteCollection(principal artifact.Principal, _, collectionID string) error {
	f.principal, f.deleted = principal, collectionID
	return nil
}

func artifactToolContext() (context.Context, WorkspaceScope) {
	scope := WorkspaceScope{PrimaryPath: ".", SessionID: "session-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1", SessionID: "session-1"}}
	ctx := WithWorkspaceScope(context.Background(), scope)
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "session-1", RunID: "run-1", PlanID: "plan-1", CheckpointID: "cp-1", AttemptID: "attempt-1"})
	return ctx, scope
}

func TestManageArtifactCreateUsesTrustedOwnershipAndFinalAuthorityResult(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-1", Name: "manage_artifact", Arguments: `{"action":"create","collection_name":"Drafts","filename":"note.txt","media_type":"text/plain","content":"managed","presentation":{"kind":"text","label":"Note","previewable":true}}`})
	if err != nil {
		t.Fatal(err)
	}
	if authority.principal.SessionID != "session-1" || authority.principal.AccountScopeID != "account-1" || authority.principal.UserID != "user-1" || authority.principal.RunID != "run-1" {
		t.Fatalf("principal = %+v", authority.principal)
	}
	if authority.created.CollectionID == "" || authority.created.VariantID == "" || authority.created.RequestID == "" || string(authority.created.Body) != "managed" {
		t.Fatalf("create input = %+v", authority.created)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatal(err)
	}
	artifactPayload := payload["artifact"].(map[string]any)
	if artifactPayload["status"] != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("output = %s", output)
	}
	if _, exists := artifactPayload["session_id"]; exists {
		t.Fatalf("output exposed ownership: %s", output)
	}
}

func TestManageArtifactRejectsMissingOrMismatchedTrustedSession(t *testing.T) {
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(&fakeArtifactAuthority{})
	ctx, scope := artifactToolContext()
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{SessionID: "other-session", RunID: "run-1"})
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-2", Name: "manage_artifact", Arguments: `{"action":"list"}`})
	if err == nil {
		t.Fatal("expected trusted session mismatch")
	}
}

func TestManageArtifactListSelectAndDeleteUseOpaqueReferences(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-list", Name: "manage_artifact", Arguments: `{"action":"list","collection_id":"collection-1","limit":5}`})
	if err != nil {
		t.Fatal(err)
	}
	var listed map[string]any
	if err := json.Unmarshal([]byte(output), &listed); err != nil || listed["count"].(float64) != 1 {
		t.Fatalf("list output=%q err=%v", output, err)
	}
	output, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-select", Name: "manage_artifact", Arguments: `{"action":"select","collection_id":"collection-1","variant_id":"variant-1"}`})
	if err != nil {
		t.Fatal(err)
	}
	var selected map[string]any
	if err := json.Unmarshal([]byte(output), &selected); err != nil {
		t.Fatal(err)
	}
	reference := selected["reference"].(map[string]any)
	if reference["variant_id"] != "variant-1" {
		t.Fatalf("selection output = %s", output)
	}
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-delete", Name: "manage_artifact", Arguments: `{"action":"delete","collection_id":"collection-1","variant_id":"variant-1"}`}); err != nil {
		t.Fatal(err)
	}
	if authority.deleted != "collection-1/variant-1" {
		t.Fatalf("deleted = %q", authority.deleted)
	}
}

func TestManageArtifactPackageAndBoundedTextRead(t *testing.T) {
	authority := &fakeArtifactAuthority{readBody: []byte("hello"), variant: pebblestore.SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", MediaType: "text/plain"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-package", Name: "manage_artifact", Arguments: `{"action":"create_package","collection_name":"UI","filename":"ui.zip","entries":[{"name":"index.html","content":"<h1>Hi</h1>"}]}`}); err != nil {
		t.Fatal(err)
	}
	if len(authority.packaged.Entries) != 1 || string(authority.packaged.Entries[0].Data) != "<h1>Hi</h1>" {
		t.Fatalf("package input = %+v", authority.packaged)
	}
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-read", Name: "manage_artifact", Arguments: `{"action":"read","variant_id":"variant-1","max_bytes":100}`})
	if err != nil || !json.Valid([]byte(output)) {
		t.Fatalf("read output=%q err=%v", output, err)
	}
	authority.variant.MediaType = "application/octet-stream"
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-binary", Name: "manage_artifact", Arguments: `{"action":"read","variant_id":"variant-1"}`})
	if err == nil {
		t.Fatal("expected binary read rejection")
	}
	if err.Error() != "manage_artifact read returns only UTF-8 text artifacts" {
		t.Fatalf("unexpected binary read error: %v", err)
	}
}
