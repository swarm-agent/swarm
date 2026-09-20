package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeNativeImporter struct {
	importedInput pebblestore.ArtifactV3ImportInput
	importCalls   int
	importErr     error
	projection    pebblestore.ArtifactV3Projection
	returnExact   bool
}

func (f *fakeNativeImporter) ImportArtifactV3(_ context.Context, input pebblestore.ArtifactV3ImportInput) (pebblestore.ArtifactV3Projection, error) {
	f.importCalls++
	f.importedInput = input
	if f.importErr != nil {
		return pebblestore.ArtifactV3Projection{}, f.importErr
	}
	if f.returnExact {
		return f.projection, nil
	}
	return pebblestore.ArtifactV3Projection{
		Repository: &pebblestore.ArtifactV3RepositoryProjection{
			ArtifactID: input.DestinationArtifactID, OwnerSessionID: input.DestinationOwner.SessionID,
			AccountScopeID: input.DestinationOwner.AccountScopeID, UserID: input.DestinationOwner.UserID,
			HeadCommitOID: input.SourceCommitOID,
		},
		Revision: &pebblestore.ArtifactV3RevisionProjection{
			ArtifactID: input.DestinationArtifactID, OwnerSessionID: input.DestinationOwner.SessionID,
			CommitOID: input.SourceCommitOID,
			Build:     pebblestore.ArtifactV3EvidenceProjection{Status: "succeeded"},
			Preview:   pebblestore.ArtifactV3EvidenceProjection{Status: "succeeded"},
		},
	}, nil
}

type fakeRetainedReaderRepo struct {
	directArtifactV3RepoFake
	retainedReadCalls int
	retainedProject   map[string][]byte
	retainedParts     []pebblestore.ArtifactV3Part
	retainedErr       error
}

func (f *fakeRetainedReaderRepo) ReadArtifactV3RetainedRevision(_ context.Context, account, user, sourceSession, artifactID, revisionRef string) (map[string][]byte, []pebblestore.ArtifactV3Part, error) {
	f.retainedReadCalls++
	if f.retainedErr != nil {
		return nil, nil, f.retainedErr
	}
	return f.retainedProject, f.retainedParts, nil
}

type fakeNativeCatalogRepo struct {
	directArtifactV3RepoFake
	searchCalls int
	lastOptions pebblestore.ArtifactV3CatalogOptions
	page        pebblestore.ArtifactV3CatalogPage
	searchErr   error
}

func (f *fakeNativeCatalogRepo) SearchArtifactV3Catalog(_ context.Context, account, user string, options pebblestore.ArtifactV3CatalogOptions) (pebblestore.ArtifactV3CatalogPage, error) {
	f.searchCalls++
	f.lastOptions = options
	if f.searchErr != nil {
		return pebblestore.ArtifactV3CatalogPage{}, f.searchErr
	}
	return f.page, nil
}

// Requirement: the provider-facing Runtime.Definitions must advertise exact
// import references without exposing authority or destination identity inputs.
// Threat: a private helper-only schema leaves the actual provider unable to reuse
// retained artifacts. This registration-layer assertion is the narrowest proof.
func TestManageArtifactDefinitionExposesImportAndNestedReferences(t *testing.T) {
	var def Definition
	for _, candidate := range NewRuntime(1).Definitions() {
		if candidate.Name == "manage_artifact" {
			def = candidate
		}
	}
	if def.Name != "manage_artifact" {
		t.Fatalf("tool name = %q, want manage_artifact", def.Name)
	}
	params := def.Parameters
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("manage_artifact definition properties missing")
	}
	actionProp, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatal("manage_artifact action property missing")
	}
	actions, ok := actionProp["enum"].([]string)
	if !ok {
		t.Fatal("manage_artifact action enum missing")
	}
	hasImport := false
	for _, a := range actions {
		if a == "import" {
			hasImport = true
			break
		}
	}
	if !hasImport {
		t.Fatalf("manage_artifact action enum lacks 'import': %v", actions)
	}

	if _, ok := props["artifact_v3_reference"]; !ok {
		t.Fatal("manage_artifact schema missing artifact_v3_reference")
	}
	if _, ok := props["artifact_reference"]; !ok {
		t.Fatal("manage_artifact schema missing artifact_reference")
	}
	if _, ok := props["library"]; !ok {
		t.Fatal("manage_artifact schema missing library discriminator")
	}

	// Import authority stays internal; preserve the existing audio model field.
	if _, ok := props["provider"]; ok {
		t.Fatal("manage_artifact schema exposes provider")
	}
	for _, name := range []string{"artifact_reference", "artifact_v3_reference"} {
		ref := props[name].(map[string]any)
		if ref["additionalProperties"] != false {
			t.Fatalf("%s permits invented authority fields", name)
		}
	}
	for _, name := range []string{"account_scope_id", "user_id", "destination_artifact_id", "destination_collection_id", "destination_variant_id"} {
		if _, exists := props[name]; exists {
			t.Fatalf("schema exposes runtime-derived %s", name)
		}
	}
}

// Requirement: import accepts one complete discriminated exact reference only.
// Threat: mixed identities, forged authority and partial input reach writes.
// Actual Runtime dispatch with recording adapters proves pre-authority rejection.
func TestManageArtifactImportValidationRejectsBeforeWrites(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)
	importer := &fakeNativeImporter{}
	runtime.SetArtifactV3NativeImporter(importer)

	ctx, scope := artifactToolContext()

	tests := []struct {
		name      string
		arguments map[string]any
		wantErr   string
	}{
		{
			name: "mixed native and legacy references",
			arguments: map[string]any{
				"action": "import",
				"artifact_v3_reference": map[string]any{
					"session_id":   "source-sess",
					"artifact_id":  "art-1",
					"revision_ref": "revision-" + strings.Repeat("a", 40),
				},
				"artifact_reference": map[string]any{
					"session_id":    "source-sess",
					"collection_id": "col-1",
					"variant_id":    "var-1",
					"event_seq":     1,
				},
			},
			wantErr: "accepts exactly one native or legacy reference, not both",
		},
		{
			name: "neither reference supplied",
			arguments: map[string]any{
				"action": "import",
			},
			wantErr: "requires exactly one native or legacy reference",
		},
		{
			name: "unknown argument supplied",
			arguments: map[string]any{
				"action": "import",
				"artifact_v3_reference": map[string]any{
					"session_id":   "source-sess",
					"artifact_id":  "art-1",
					"revision_ref": "revision-" + strings.Repeat("a", 40),
				},
				"unsupported_key": "malicious",
			},
			wantErr: "contains unsupported field \"unsupported_key\"",
		},
		{
			name: "native combined with legacy collection_id",
			arguments: map[string]any{
				"action": "import",
				"artifact_v3_reference": map[string]any{
					"session_id":   "source-sess",
					"artifact_id":  "art-1",
					"revision_ref": "revision-" + strings.Repeat("a", 40),
				},
				"collection_id": "forged",
			},
			wantErr: "contains unsupported field \"collection_id\"",
		},
		{
			name: "incomplete native reference missing revision_ref",
			arguments: map[string]any{
				"action": "import",
				"artifact_v3_reference": map[string]any{
					"session_id":  "source-sess",
					"artifact_id": "art-1",
				},
			},
			wantErr: "requires complete session_id, artifact_id, and exact revision_ref",
		},
		{
			name: "incomplete legacy reference missing event_seq",
			arguments: map[string]any{
				"action": "import",
				"artifact_reference": map[string]any{
					"session_id":    "source-sess",
					"collection_id": "col-1",
					"variant_id":    "var-1",
				},
			},
			wantErr: "requires complete session_id, collection_id, variant_id, and non-zero event_seq",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rawArgs, _ := json.Marshal(tc.arguments)
			call := Call{CallID: "test-import-val", Name: "manage_artifact", Arguments: string(rawArgs)}
			_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got err = %v, want substring %q", err, tc.wantErr)
			}
			if importer.importCalls != 0 || authority.importCalls != 0 {
				t.Fatalf("mutation performed on invalid input: importer calls=%d, authority calls=%d", importer.importCalls, authority.importCalls)
			}
		})
	}
}

// Requirement: native import binds destination owner/IDs to trusted run/call context.
// Threat: model-chosen identities mutate an existing artifact; this dispatch test
// observes the exact canonical importer input, not real store persistence.
func TestManageArtifactNativeImportSuccess(t *testing.T) {
	runtime := NewRuntime(1)
	importer := &fakeNativeImporter{}
	runtime.SetArtifactV3NativeImporter(importer)

	ctx, scope := artifactToolContext()

	sourceOID := strings.Repeat("b", 40)
	arguments := map[string]any{
		"action": "import",
		"artifact_v3_reference": map[string]any{
			"session_id":   "retained-sess",
			"artifact_id":  "source-art",
			"revision_ref": "revision-" + sourceOID,
		},
		"title": "Imported concept",
	}
	rawArgs, _ := json.Marshal(arguments)
	call := Call{CallID: "call-native-import-1", Name: "manage_artifact", Arguments: string(rawArgs)}
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("native import failed: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	if resp["status"] != "ok" || resp["action"] != "import" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	artV3, ok := resp["artifact_v3"].(map[string]any)
	if !ok {
		t.Fatalf("missing artifact_v3 in response: %#v", resp)
	}
	if artV3["session_id"] != "session-1" {
		t.Fatalf("destination session = %v, want session-1", artV3["session_id"])
	}
	if artV3["head_commit"] != sourceOID {
		t.Fatalf("head_commit = %v, want %s", artV3["head_commit"], sourceOID)
	}

	ref, ok := resp["artifact_v3_reference"].(map[string]any)
	if !ok || ref["session_id"] != "session-1" || ref["revision_ref"] != "revision-"+sourceOID {
		t.Fatalf("invalid artifact_v3_reference: %#v", ref)
	}

	nextCalls, ok := resp["copyable_next_calls"].([]any)
	if !ok || len(nextCalls) != 2 {
		t.Fatalf("expected 2 copyable next calls, got %#v", resp["copyable_next_calls"])
	}

	// Verify importer received derived destination ownership
	if importer.importCalls != 1 {
		t.Fatalf("importer calls = %d, want 1", importer.importCalls)
	}
	in := importer.importedInput
	if in.DestinationOwner.SessionID != "session-1" || in.DestinationOwner.AccountScopeID != "account-1" {
		t.Fatalf("invalid destination owner: %+v", in.DestinationOwner)
	}
	if in.SourceSessionID != "retained-sess" || in.SourceArtifactID != "source-art" || in.SourceCommitOID != sourceOID {
		t.Fatalf("invalid source inputs: %+v", in)
	}
}

// Requirement: legacy exact references reach the legacy authority unchanged,
// with independently derived destination identities. Dispatch recording is the
// narrowest proof; runtime canonical-store tests cover durable copy fidelity.
func TestManageArtifactLegacyImportSuccess(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)

	ctx, scope := artifactToolContext()

	arguments := map[string]any{
		"action": "import",
		"artifact_reference": map[string]any{
			"session_id":    "retained-legacy-sess",
			"collection_id": "col-legacy",
			"variant_id":    "var-legacy",
			"event_seq":     5,
		},
		"collection_name": "Imported Designs",
	}
	rawArgs, _ := json.Marshal(arguments)
	call := Call{CallID: "call-legacy-import-1", Name: "manage_artifact", Arguments: string(rawArgs)}
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("legacy import failed: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	if resp["status"] != "ok" || resp["action"] != "import" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	ref, ok := resp["artifact_reference"].(map[string]any)
	if !ok || ref["session_id"] != "session-1" {
		t.Fatalf("invalid legacy reference: %#v", ref)
	}

	if authority.importCalls != 1 {
		t.Fatalf("authority import calls = %d, want 1", authority.importCalls)
	}
	if authority.imported.CollectionName != "Imported Designs" {
		t.Fatalf("collection name = %q, want 'Imported Designs'", authority.imported.CollectionName)
	}
	source := authority.imported.SourceReference()
	if source.SessionID != "retained-legacy-sess" || source.CollectionID != "col-legacy" || source.EventSeq != 5 {
		t.Fatalf("invalid imported source: %+v", source)
	}
}

// Requirement: cross-session reads use the retained reader and remain bounded.
// Threat: same-session policy accidentally blocks reuse or silently truncates.
// Recording reader tests dispatch only; runtime tests own source authentication.
func TestManageArtifactReadV3RetainedCrossSession(t *testing.T) {
	manifest := pebblestore.ArtifactV3Manifest{
		SchemaVersion: pebblestore.ArtifactV3ManifestVersion,
		Entrypoint:    "index.html",
	}
	encodedManifest, _ := json.Marshal(manifest)
	htmlContent := "<!doctype html><html><body><h1>Retained</h1></body></html>"
	project := map[string][]byte{
		pebblestore.ArtifactV3ManifestFilename: encodedManifest,
		"index.html":                           []byte(htmlContent),
	}

	repo := &fakeRetainedReaderRepo{
		retainedProject: project,
		retainedParts:   []pebblestore.ArtifactV3Part{{ID: "main", Label: "Main"}},
	}
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))

	ctx, scope := artifactToolContext()

	// Cross-session read
	sourceOID := strings.Repeat("c", 40)
	readArgs := map[string]any{
		"action": "read_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":   "retained-source-sess",
			"artifact_id":  "art-retained",
			"revision_ref": "revision-" + sourceOID,
		},
	}
	rawArgs, _ := json.Marshal(readArgs)
	call := Call{CallID: "call-read-v3-cross", Name: "manage_artifact", Arguments: string(rawArgs)}
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("cross-session read_v3 failed: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	resp = resp["artifact_v3"].(map[string]any)
	if resp["status"] != "ok" || resp["content"] != htmlContent {
		t.Fatalf("unexpected read_v3 response: %#v", resp)
	}

	// Verify copyable_next_calls includes import guidance for retained cross-session reads
	nextCalls, ok := resp["copyable_next_calls"].([]any)
	if !ok || len(nextCalls) == 0 {
		t.Fatalf("expected copyable next calls for retained read, got: %#v", resp["copyable_next_calls"])
	}
	firstCall := nextCalls[0].(map[string]any)
	if firstCall["action"] != "import" {
		t.Fatalf("expected import action in copyable next calls, got %v", firstCall["action"])
	}

	if repo.retainedReadCalls != 1 {
		t.Fatalf("retained read calls = %d, want 1", repo.retainedReadCalls)
	}

	// Requirement: read_v3 rejects unsupported max_bytes rather than advertise it
	readArgs["max_bytes"] = 1024
	rawArgsWithBytes, _ := json.Marshal(readArgs)
	callWithBytes := Call{CallID: "call-read-max-bytes", Name: "manage_artifact", Arguments: string(rawArgsWithBytes)}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, callWithBytes)
	if err == nil || !strings.Contains(err.Error(), "does not support max_bytes") {
		t.Fatalf("read_v3 with max_bytes must be rejected, got err: %v", err)
	}
}

// Requirement: native catalog pagination preserves an empty continuation page.
// Threat: losing opaque cursors silently hides later retained sources. This
// dispatch fixture exercises the empty-page contract independently of scan order.
func TestManageArtifactListV3NativeCatalogPaging(t *testing.T) {
	repo := &fakeNativeCatalogRepo{
		page: pebblestore.ArtifactV3CatalogPage{
			Items:      []pebblestore.ArtifactV3CatalogItem{},
			NextCursor: "opaque-cursor-cont",
			HasMore:    true,
		},
	}
	runtime := NewRuntime(1)
	runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))

	ctx, scope := artifactToolContext()

	// Requirement: list_v3 does not lose empty continuation pages
	args := map[string]any{"action": "list_v3", "cursor": "cursor-0"}
	rawArgs, _ := json.Marshal(args)
	call := Call{CallID: "call-list-v3", Name: "manage_artifact", Arguments: string(rawArgs)}
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("list_v3 failed: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	resp = resp["artifact_v3"].(map[string]any)
	if resp["next_cursor"] != "opaque-cursor-cont" {
		t.Fatalf("next_cursor lost on empty continuation page: %#v", resp)
	}
	if resp["has_more"] != true {
		t.Fatalf("has_more should be true when next_cursor is present: %#v", resp)
	}
	if resp["count"] != float64(0) {
		t.Fatalf("count = %v, want 0", resp["count"])
	}

	if repo.searchCalls != 1 || repo.lastOptions.Cursor != "cursor-0" {
		t.Fatalf("search catalog was not called with cursor: %+v", repo.lastOptions)
	}
	// The explicit library alias must not depend on a configured legacy authority.
	for _, library := range []string{"native", " Native "} {
		body, _ := json.Marshal(map[string]any{"action": "search", "library": library, "cursor": "opaque-cursor-cont"})
		if _, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "native-search", Name: "manage_artifact", Arguments: string(body)}); err != nil {
			t.Fatalf("native alias failed: %v", err)
		}
		if repo.lastOptions.Cursor != "opaque-cursor-cont" {
			t.Fatal("native alias lost continuation")
		}
	}
}

// Requirement: retained sources cannot be directly revised by another session.
// Threat: relaxed read authority accidentally grants source writes. The earliest
// dispatch rejection must explain import-first recovery without preparing a turn.
func TestManageArtifactReviseRejectsRetainedSessionWithImportGuidance(t *testing.T) {
	runtime := NewRuntime(1)
	repo := &directArtifactV3RepoFake{}
	runtime.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))

	ctx, scope := artifactToolContext()

	// Calling revise_v3 on a foreign retained session must fail with import-first guidance
	args := map[string]any{
		"action": "revise_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":   "retained-sess-99",
			"artifact_id":  "art-foreign",
			"revision_ref": "revision-" + strings.Repeat("d", 40),
		},
		"content": "<!doctype html><html><body>revised</body></html>",
	}
	rawArgs, _ := json.Marshal(args)
	call := Call{CallID: "call-revise-foreign", Name: "manage_artifact", Arguments: string(rawArgs)}
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err == nil {
		t.Fatal("revise_v3 on foreign session should have failed")
	}
	if !strings.Contains(err.Error(), "action='import'") || !strings.Contains(err.Error(), "retained artifacts are immutable") {
		t.Fatalf("error missing import-first guidance: %v", err)
	}
}

// Requirement: runtime-owned import identity and evidence cannot be overridden,
// even with null discriminators or near-valid malformed exact references.
// Threat: coercion/mixed fields produce a write under an unintended identity.
// Actual dispatch recording asserts rejection AND zero authority side effects.
func TestManageArtifactImportRejectsForgedIdentity(t *testing.T) {
	r := NewRuntime(1)
	native := &fakeNativeImporter{}
	legacy := &fakeArtifactAuthority{}
	r.SetArtifactV3NativeImporter(native)
	r.SetArtifactAuthority(legacy)
	ctx, scope := artifactToolContext()
	for _, nativeRef := range []bool{true, false} {
		base := func() map[string]any {
			if nativeRef {
				return map[string]any{"action": "import", "artifact_v3_reference": map[string]any{"session_id": "source", "artifact_id": "source-art", "revision_ref": "revision-" + strings.Repeat("a", 40)}}
			}
			return map[string]any{"action": "import", "artifact_reference": map[string]any{"session_id": "source", "collection_id": "source-col", "variant_id": "source-var", "event_seq": 1}}
		}
		for _, key := range []string{"account_scope_id", "user_id", "session_id", "collection_id", "variant_id", "event_seq", "destination_artifact_id", "destination_collection_id", "destination_variant_id", "request_id", "transaction_id", "lineage", "build", "preview", "provider", "model"} {
			args := base()
			args[key] = "forged"
			body, _ := json.Marshal(args)
			if _, err := r.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "forged", Name: "manage_artifact", Arguments: string(body)}); err == nil {
				t.Fatalf("accepted forged %s (native=%v)", key, nativeRef)
			}
		}
	}
	for _, args := range []map[string]any{
		{"action": "import", "artifact_reference": nil},
		{"action": "import", "artifact_v3_reference": nil},
		{"action": "import", "artifact_reference": nil, "artifact_v3_reference": nil},
		{"action": "import", "artifact_reference": map[string]any{"session_id": "source", "collection_id": "c", "variant_id": "v", "event_seq": 1.5}},
		{"action": "import", "artifact_v3_reference": map[string]any{"session_id": "source", "artifact_id": "a", "revision_ref": "revision-" + strings.Repeat("z", 40)}},
		{"action": "import", "artifact_v3_reference": map[string]any{"session_id": "source", "artifact_id": "a", "revision_ref": "revision-" + strings.Repeat("a", 40), "preview": "forged"}},
	} {
		body, _ := json.Marshal(args)
		if _, err := r.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "malformed", Name: "manage_artifact", Arguments: string(body)}); err == nil {
			t.Fatalf("accepted malformed reference: %s", body)
		}
	}
	if native.importCalls != 0 || legacy.importCalls != 0 {
		t.Fatalf("invalid import caused writes: native=%d legacy=%d", native.importCalls, legacy.importCalls)
	}
}

// Requirement: an incomplete importer result cannot be advertised as ready.
// Threat: wiring regressions fabricate a reusable reference from metadata only.
// Actual dispatch with an injected malformed projection is the narrowest layer;
// the canonical store, not this response check, owns atomic publication.
func TestManageArtifactImportRejectsIncompleteProjection(t *testing.T) {
	ctx, scope := artifactToolContext()
	for _, projection := range []pebblestore.ArtifactV3Projection{
		{},
		{Repository: &pebblestore.ArtifactV3RepositoryProjection{ArtifactID: "foreign", OwnerSessionID: "other"}},
		{Revision: &pebblestore.ArtifactV3RevisionProjection{CommitOID: strings.Repeat("a", 40)}},
	} {
		r := NewRuntime(1)
		importer := &fakeNativeImporter{projection: projection, returnExact: true}
		r.SetArtifactV3NativeImporter(importer)
		args, _ := json.Marshal(map[string]any{"action": "import", "artifact_v3_reference": map[string]any{"session_id": "source", "artifact_id": "original", "revision_ref": "revision-" + strings.Repeat("a", 40)}})
		out, err := r.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "incomplete", Name: "manage_artifact", Arguments: string(args)})
		if err == nil || !strings.Contains(err.Error(), "destination-owned ready revision") || out != "" || importer.importCalls != 1 {
			t.Fatalf("incomplete result advertised: output=%q err=%v calls=%d", out, err, importer.importCalls)
		}
	}
}
