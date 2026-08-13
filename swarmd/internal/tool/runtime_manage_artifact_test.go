package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeArtifactAuthority struct {
	principal artifact.Principal
	created   artifact.CreateInput
	packaged  artifact.CreatePackageInput
	readBody        []byte
	variant         pebblestore.SessionArtifactVariant
	reference       pebblestore.SessionArtifactSelectionReference
	referenceRead   bool
	deleted         string
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
func (f *fakeArtifactAuthority) GetReference(principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error) {
	f.principal, f.reference = principal, ref
	return f.variant, nil
}
func (f *fakeArtifactAuthority) Read(_ context.Context, principal artifact.Principal, variantID string, _ int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	variant, err := f.Get(principal, variantID)
	return append([]byte(nil), f.readBody...), variant, err
}
func (f *fakeArtifactAuthority) ReadReference(_ context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, _ int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	f.principal, f.reference, f.referenceRead = principal, ref, true
	return append([]byte(nil), f.readBody...), f.variant, nil
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

func TestManageArtifactCreatePinsTrustedManagedDestinationAndLineage(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx := WithWorkspaceScope(context.Background(), WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}})
	ctx = WithArtifactRunContext(ctx, ArtifactRunContext{
		SessionID: "parent-1", ChildSessionID: "child-1", TaskCallID: "call-1", ProgramID: "program-1", ProgramJobID: "job-1",
		IterationID: "iteration-1", IterationIndex: 1, CollectionID: "collection-trusted", VariantID: "variant-trusted",
	})
	_, err := runtime.executeManageArtifact(ctx, WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}, "artifact-call", map[string]any{
		"action": "create", "collection_name": "redirected", "collection_description": "model-authored", "filename": "variant.txt", "media_type": "text/plain", "content": "managed",
	})
	if err != nil {
		t.Fatalf("create trusted managed artifact: %v", err)
	}
	if authority.created.CollectionID != "collection-trusted" || authority.created.VariantID != "variant-trusted" {
		t.Fatalf("trusted destination not pinned: %#v", authority.created)
	}
	if authority.created.CollectionName != "" || authority.created.CollectionDescription != "" {
		t.Fatalf("managed create retained model-authored collection metadata: %#v", authority.created)
	}
	missingChild := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: "parent-1", TaskCallID: "call-1", CollectionID: "collection-trusted", VariantID: "variant-trusted"})
	if _, err := artifactPrincipal(missingChild, WorkspaceScope{SessionID: "parent-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}); err == nil || !strings.Contains(err.Error(), "requires trusted child session lineage") {
		t.Fatalf("missing managed child lineage error = %v", err)
	}
	if _, err := runtime.executeManageArtifact(ctx, WorkspaceScope{SessionID: "other-child", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}, "artifact-call-mismatch", map[string]any{
		"action": "create", "filename": "variant.txt", "media_type": "text/plain", "content": "managed",
	}); err == nil || !strings.Contains(err.Error(), "trusted session context is missing or inconsistent") {
		t.Fatalf("mismatched producer error = %v", err)
	}
	if _, err := runtime.executeManageArtifact(ctx, WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}}, "artifact-call-2", map[string]any{
		"action": "create", "collection_id": "redirected", "filename": "variant.txt", "media_type": "text/plain", "content": "managed",
	}); err == nil || !strings.Contains(err.Error(), "managed create must omit collection_id") {
		t.Fatalf("trusted destination redirect error = %v", err)
	}
	principal, err := artifactPrincipal(ctx, WorkspaceScope{SessionID: "child-1", Principal: identity.Principal{SessionID: "parent-1", AccountScopeID: "account-1", UserID: "user-1"}})
	if err != nil || principal.SessionID != "parent-1" || principal.ChildSessionID != "child-1" || principal.TaskCallID != "call-1" || principal.ProgramJobID != "job-1" || principal.IterationIndex != 1 {
		t.Fatalf("trusted managed principal = %#v err=%v", principal, err)
	}
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
	authority := &fakeArtifactAuthority{readBody: []byte("hello"), variant: pebblestore.SessionArtifactVariant{ID: "variant-1", CollectionID: "collection-1", SessionID: "session-1", EventSeq: 7, MediaType: "text/plain"}}
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

func TestManageArtifactReadsExplicitAttachedReference(t *testing.T) {
	authority := &fakeArtifactAuthority{readBody: []byte("selected design"), variant: pebblestore.SessionArtifactVariant{ID: "variant-source", CollectionID: "collection-source", SessionID: "source-session", EventSeq: 42, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/plain"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-read-source", Name: "manage_artifact", Arguments: `{"action":"read","session_id":"source-session","collection_id":"collection-source","variant_id":"variant-source","event_seq":42,"max_bytes":64}`})
	if err != nil {
		t.Fatal(err)
	}
	if !authority.referenceRead || authority.reference.SessionID != "source-session" || authority.reference.CollectionID != "collection-source" || authority.reference.VariantID != "variant-source" || authority.reference.EventSeq != 42 {
		t.Fatalf("source reference = %+v read=%t", authority.reference, authority.referenceRead)
	}
	if !strings.Contains(output, `"content":"selected design"`) || strings.Contains(output, "workspace") {
		t.Fatalf("read output = %s", output)
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-read-incomplete", Name: "manage_artifact", Arguments: `{"action":"read","session_id":"source-session","variant_id":"variant-source"}`})
	if err == nil || !strings.Contains(err.Error(), "requires session_id, collection_id, variant_id, and event_seq") {
		t.Fatalf("incomplete source reference error = %v", err)
	}
}

func TestManageArtifactCreateCarriesAuthenticatedSourceLineage(t *testing.T) {
	authority := &fakeArtifactAuthority{variant: pebblestore.SessionArtifactVariant{ID: "variant-source", CollectionID: "collection-source", SessionID: "source-session", EventSeq: 42, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/plain"}}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	ctx, scope := artifactToolContext()
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "call-derive", Name: "manage_artifact", Arguments: `{"action":"create","collection_name":"Derived","filename":"derived.txt","media_type":"text/plain","content":"revision","source_session_id":"source-session","source_collection_id":"collection-source","source_variant_id":"variant-source","source_event_seq":42}`})
	if err != nil {
		t.Fatal(err)
	}
	if authority.created.SourceSessionID != "source-session" || authority.created.SourceCollectionID != "collection-source" || authority.created.SourceVariantID != "variant-source" || authority.created.SourceEventSeq != 42 {
		t.Fatalf("derived input = %+v", authority.created)
	}
}
