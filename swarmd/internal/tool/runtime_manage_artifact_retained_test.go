package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeNativeImporter struct {
	importedInput pebblestore.ArtifactV3ImportInput
	importCalls   int
	importErr     error
	projection    pebblestore.ArtifactV3Projection
}

func (f *fakeNativeImporter) ImportArtifactV3(_ context.Context, input pebblestore.ArtifactV3ImportInput) (pebblestore.ArtifactV3Projection, error) {
	f.importCalls++
	f.importedInput = input
	if f.importErr != nil {
		return pebblestore.ArtifactV3Projection{}, f.importErr
	}
	proj := f.projection
	if proj.ArtifactID == "" {
		proj.ArtifactID = input.DestinationArtifactID
	}
	if proj.OwnerSessionID == "" {
		proj.OwnerSessionID = input.DestinationOwner.SessionID
	}
	if proj.HeadCommitOID == "" {
		proj.HeadCommitOID = input.SourceCommitOID
	}
	return proj, nil
}

type fakeRetainedReaderRepo struct {
	directArtifactV3RepoFake
	retainedReadCalls int
	retainedProject   map[string][]byte
	retainedParts     []pebblestore.ArtifactV3PartProjection
	retainedErr       error
}

func (f *fakeRetainedReaderRepo) ReadArtifactV3RetainedRevision(_ context.Context, account, user, sourceSession, artifactID, revisionRef string) (map[string][]byte, []pebblestore.ArtifactV3PartProjection, error) {
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

func TestManageArtifactDefinitionExposesImportAndNestedReferences(t *testing.T) {
	def := manageArtifactDefinition()
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

	// Invariant: provider and model are never exposed in public schema
	if _, ok := props["provider"]; ok {
		t.Fatal("manage_artifact schema exposes provider")
	}
	if _, ok := props["model"]; ok {
		t.Fatal("manage_artifact schema exposes model")
	}
}

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
			wantErr: "cannot be combined with legacy field",
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
		retainedParts:   []pebblestore.ArtifactV3PartProjection{{ID: "main", Label: "Main"}},
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
}

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
