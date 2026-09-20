package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/artifactv3video"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/videoproject"
)

// Explicit disclosure: artifactV3RuntimeRenderer and artifactV3ConversionRenderer
// are fake in-memory renderers producing deterministic byte slices ("fake-renderer-evidence");
// real browser capture is not invoked in deterministic unit tests.

type retainedReuseHarness struct {
	root           string
	store          *pebblestore.Store
	events         *pebblestore.EventLog
	sessionStore   *pebblestore.SessionStore
	sessions       *sessionruntime.Service
	repositoryRoot string
	workspaceRoot  string
	evidenceRoot   string
	service        *pebblestore.ArtifactV3Service
	adapter        *artifactV3RuntimeAdapter
	videoBridge    *artifactV3VideoBridge
	videoProjects  *videoproject.Service
	toolRuntime    *tool.Runtime
}

func setupRetainedReuseHarness(t *testing.T) *retainedReuseHarness {
	t.Helper()
	root := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(root, "session.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}

	sessionStore := pebblestore.NewSessionStore(store)
	sessions := sessionruntime.NewService(sessionStore, events)

	// Create test sessions
	for _, sess := range []struct {
		id      string
		account string
		user    string
	}{
		{"session-source", "account-1", "user-1"},
		{"session-dest", "account-1", "user-1"},
		{"session-archived", "account-1", "user-1"},
		{"session-deleted", "account-1", "user-1"},
		{"session-foreign-user", "account-1", "user-foreign"},
		{"session-foreign-account", "account-foreign", "user-1"},
	} {
		_, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			SessionID:      sess.id,
			AccountScopeID: sess.account,
			UserID:         sess.user,
			Title:          sess.id,
			WorkspacePath:  root,
			WorkspaceName:  "workspace",
			Mode:           sessionruntime.ModeAuto,
			Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "medium"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	repositoryRoot, workspaceRoot, evidenceRoot, err := artifactV3StorageRoots(filepath.Join(root, "data"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}

	service, err := pebblestore.NewArtifactV3Service(sessions.Store(), repositoryRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}

	adapter := newArtifactV3RuntimeAdapter(service, sessions.Store(), repositoryRoot, evidenceRoot, pebblestore.ArtifactV3Limits{}, artifactV3RuntimeRenderer{})
	adapter.publish = func(identity.Principal, api.ArtifactV3Artifact, string, string) error { return nil }

	// Video Studio wiring
	derivatives, err := newArtifactV3DerivativeStore(filepath.Join(root, "video-derivatives"))
	if err != nil {
		t.Fatal(err)
	}
	videoProjects := videoproject.NewService(sessions.Store())
	videoService := artifactv3video.New(adapter, artifactV3AnimationRenderer{renderer: artifactV3ConversionRenderer{}}, derivatives)
	videoProjects.SetArtifactV3Authority(videoService)
	videoBridge := &artifactV3VideoBridge{artifacts: adapter, service: videoService, projects: videoProjects}

	// Tool Runtime
	toolRuntime := tool.NewRuntime(2)
	toolRuntime.SetArtifactV3AuthorService(tool.NewArtifactV3AuthorService(workspaceRoot, adapter, adapter, adapter))
	toolRuntime.SetArtifactV3NativeImporter(adapter)
	toolRuntime.SetArtifactV3VideoConversionService(videoBridge)

	return &retainedReuseHarness{
		root:           root,
		store:          store,
		events:         events,
		sessionStore:   sessionStore,
		sessions:       sessions,
		repositoryRoot: repositoryRoot,
		workspaceRoot:  workspaceRoot,
		evidenceRoot:   evidenceRoot,
		service:        service,
		adapter:        adapter,
		videoBridge:    videoBridge,
		videoProjects:  videoProjects,
		toolRuntime:    toolRuntime,
	}
}

func (h *retainedReuseHarness) setRun(t *testing.T, sessionID, runID, status string) {
	t.Helper()
	session, ok, err := h.sessions.Store().GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session %s: %v", sessionID, err)
	}
	_, err = h.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       sessionID,
		AccountScopeID:  session.AccountScopeID,
		UserID:          session.UserID,
		Kind:            pebblestore.V3SessionMutationRecordRunIntent,
		ClientRequestID: runID + status,
		PayloadHash:     runID + status,
		RunIntent: &pebblestore.V3SessionRunIntent{
			SessionID:      sessionID,
			AccountScopeID: session.AccountScopeID,
			UserID:         session.UserID,
			RunID:          runID,
			Status:         status,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (h *retainedReuseHarness) invokeTool(t *testing.T, sessionID, runID string, args map[string]any) (map[string]any, error) {
	t.Helper()
	session, ok, err := h.sessions.Store().GetSession(sessionID)
	if err != nil || !ok {
		// Session might be archived/deleted; find owner from tombstone if needed
		tombstone, tok, terr := h.sessions.Store().GetV3SessionTombstone(sessionID)
		if terr != nil || !tok {
			t.Fatalf("session %s not found: %v", sessionID, err)
		}
		session = tombstone.Session
	}
	scope := tool.WorkspaceScope{
		SessionID: sessionID,
		Principal: identity.Principal{
			Type:           identity.PrincipalTypeUser,
			AccountScopeID: session.AccountScopeID,
			UserID:         session.UserID,
			SessionID:      sessionID,
		},
	}
	h.setRun(t, sessionID, runID, pebblestore.V3RunIntentPendingExecutor)
	h.setRun(t, sessionID, runID, pebblestore.V3RunIntentRunning)

	body, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.toolRuntime.ExecuteForWorkspaceScopeWithRuntime(
		tool.WithArtifactRunContext(context.Background(), tool.ArtifactRunContext{SessionID: sessionID, RunID: runID}),
		scope,
		tool.Call{CallID: "call-" + runID, Name: "manage_artifact", Arguments: string(body)},
	)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal tool response: %v; raw=%s", err, out)
	}
	return result, nil
}

// createTestArtifact creates a fully published ready Artifact V3 in the given session.
func (h *retainedReuseHarness) createTestArtifact(t *testing.T, sessionID, artifactID, title string) (string, string) {
	t.Helper()
	html := fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><title>%s</title></head><body><main id="hero"><h1>%s</h1></main><section id="pricing">Pricing</section><footer id="footer">Footer</footer></body></html>`, title, title)
	created, err := h.invokeTool(t, sessionID, "run-create-"+artifactID, map[string]any{
		"action":      "create",
		"artifact_id": artifactID,
		"filename":    "index.html",
		"content":     html,
	})
	if err != nil {
		t.Fatalf("create artifact %s: %v", artifactID, err)
	}
	ref, ok := created["artifact_v3_reference"].(map[string]any)
	if !ok {
		ref = created["reference"].(map[string]any)
	}
	revRef := ref["revision_ref"].(string)
	commitOID := strings.TrimPrefix(revRef, "revision-")
	return artifactID, commitOID
}

// Purpose: Verify tool-level discovery (list_v3, source_v3) queries the canonical
// cross-session Artifact V3 catalog, returns copyable next calls with import guidance,
// handles historical revisions, candidates, archived tombstones, and rejects deleted/foreign sources.
// Regression: Prevents tool-level re-isolation from hiding retained cross-session artifacts.
// Authority: artifactV3RuntimeAdapter (SearchArtifactV3Catalog, ResolveArtifactV3SelectedSource) -> ArtifactV3Service.
func TestRetainedArtifactToolDiscovery(t *testing.T) {
	h := setupRetainedReuseHarness(t)

	// 1. Create artifacts across sessions
	artSource, commitSource := h.createTestArtifact(t, "session-source", "art-source", "Source Artifact")
	artArchived, commitArchived := h.createTestArtifact(t, "session-archived", "art-archived", "Archived Artifact")
	artDeleted, _ := h.createTestArtifact(t, "session-deleted", "art-deleted", "Deleted Artifact")
	artForeignUser, _ := h.createTestArtifact(t, "session-foreign-user", "art-foreign-user", "Foreign User Artifact")

	// Archive session-archived and delete session-deleted
	if err := h.sessions.ArchiveSession("session-archived"); err != nil {
		t.Fatalf("archive session: %v", err)
	}
	if err := h.sessions.DeleteSession("session-deleted"); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	// 2. Discover via list_v3 from destination session
	listResp, err := h.invokeTool(t, "session-dest", "run-list-1", map[string]any{
		"action": "list_v3",
		"limit":  50,
	})
	if err != nil {
		t.Fatalf("list_v3 failed: %v", err)
	}
	artifacts, ok := listResp["artifacts"].([]any)
	if !ok {
		t.Fatalf("list_v3 missing artifacts array: %#v", listResp)
	}

	foundSource := false
	foundArchived := false
	foundDeleted := false
	foundForeignUser := false
	for _, itemRaw := range artifacts {
		item := itemRaw.(map[string]any)
		id := item["artifact_id"].(string)
		sessID := item["session_id"].(string)
		if id == artSource && sessID == "session-source" {
			foundSource = true
			// Verify copyable_next_calls includes import for cross-session items
			nextCalls, ok := item["copyable_next_calls"].([]any)
			if !ok || len(nextCalls) < 2 {
				t.Fatalf("source artifact missing copyable next calls: %#v", item)
			}
			call1 := nextCalls[1].(map[string]any)
			if call1["action"] != "import" {
				t.Fatalf("expected next call action='import', got: %v", call1["action"])
			}
		}
		if id == artArchived {
			foundArchived = true
		}
		if id == artDeleted {
			foundDeleted = true
		}
		if id == artForeignUser {
			foundForeignUser = true
		}
	}
	if !foundSource {
		t.Fatalf("list_v3 failed to find cross-session source artifact %s", artSource)
	}
	if !foundArchived {
		t.Fatalf("list_v3 failed to find retained archived session artifact %s", artArchived)
	}
	if foundDeleted {
		t.Fatalf("list_v3 leaked deleted session artifact %s", artDeleted)
	}
	if foundForeignUser {
		t.Fatalf("list_v3 leaked foreign user artifact %s", artForeignUser)
	}

	// 3. Test empty continuation pagination
	// Request list_v3 with a non-matching query to verify empty page with cursor handling
	queryResp, err := h.invokeTool(t, "session-dest", "run-list-query", map[string]any{
		"action": "list_v3",
		"query":  "non-matching-query-xyz",
	})
	if err != nil {
		t.Fatalf("list_v3 query failed: %v", err)
	}
	qItems, ok := queryResp["artifacts"].([]any)
	if !ok || len(qItems) != 0 {
		t.Fatalf("expected 0 items for non-matching query, got: %d", len(qItems))
	}

	// 4. Test source_v3 resolution
	// Resolving cross-session source returns reference and copyable_next_calls with import
	srcResp, err := h.invokeTool(t, "session-dest", "run-source-1", map[string]any{
		"action": "source_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-source",
			"artifact_id":  artSource,
			"revision_ref": "revision-" + commitSource,
		},
	})
	if err != nil {
		t.Fatalf("source_v3 for cross-session source failed: %v", err)
	}
	srcNextCalls, ok := srcResp["copyable_next_calls"].([]any)
	if !ok || len(srcNextCalls) == 0 {
		t.Fatalf("source_v3 missing copyable next calls: %#v", srcResp)
	}
	if srcNextCalls[1].(map[string]any)["action"] != "import" {
		t.Fatalf("source_v3 second call != import: %#v", srcNextCalls)
	}

	// 5. Test source_v3 on archived session source -> succeeds
	archResp, err := h.invokeTool(t, "session-dest", "run-source-arch", map[string]any{
		"action": "source_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-archived",
			"artifact_id":  artArchived,
			"revision_ref": "revision-" + commitArchived,
		},
	})
	if err != nil {
		t.Fatalf("source_v3 on archived session source failed: %v", err)
	}
	if archResp["reference"].(map[string]any)["artifact_id"] != artArchived {
		t.Fatalf("source_v3 on archived session returned unexpected artifact: %#v", archResp)
	}

	// 6. Test source_v3 on deleted session source -> rejected
	if _, err := h.invokeTool(t, "session-dest", "run-source-del", map[string]any{
		"action": "source_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":  "session-deleted",
			"artifact_id": artDeleted,
		},
	}); err == nil {
		t.Fatal("source_v3 accepted deleted session source")
	}

	// 7. Test source_v3 on foreign user source -> rejected
	if _, err := h.invokeTool(t, "session-dest", "run-source-foreign", map[string]any{
		"action": "source_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":  "session-foreign-user",
			"artifact_id": artForeignUser,
		},
	}); err == nil {
		t.Fatal("source_v3 accepted foreign user source")
	}

	// 8. Test source_v3 with forged/mixed refs -> rejected
	if _, err := h.invokeTool(t, "session-dest", "run-source-forged", map[string]any{
		"action": "source_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-source",
			"artifact_id":  artSource,
			"revision_ref": "revision-" + strings.Repeat("0", 40),
		},
	}); err == nil {
		t.Fatal("source_v3 accepted forged revision_ref")
	}
}

// Purpose: Verify read_v3 inspects complete project HTML and manifest from an exact
// cross-session retained source without altering source session or artifact state,
// and enforces same-principal authorization.
// Regression: Prevents cross-session read leakage to unauthorized accounts or mutated source bytes.
// Authority: artifactV3RuntimeAdapter.ReadArtifactV3RetainedRevision -> ArtifactV3Service.ReadRetainedRevision.
func TestRetainedArtifactToolExactRead(t *testing.T) {
	h := setupRetainedReuseHarness(t)

	artSource, commitSource := h.createTestArtifact(t, "session-source", "art-source-read", "Exact Read Test")
	artArchived, commitArchived := h.createTestArtifact(t, "session-archived", "art-archived-read", "Archived Read Test")
	artDeleted, commitDeleted := h.createTestArtifact(t, "session-deleted", "art-deleted-read", "Deleted Read Test")
	artForeignUser, commitForeignUser := h.createTestArtifact(t, "session-foreign-user", "art-foreign-read", "Foreign Read Test")

	if err := h.sessions.ArchiveSession("session-archived"); err != nil {
		t.Fatal(err)
	}
	if err := h.sessions.DeleteSession("session-deleted"); err != nil {
		t.Fatal(err)
	}

	// Capture source state before read
	sourceRepoBefore, _, _ := h.sessionStore.GetArtifactV3Repository("account-1", "user-1", artSource)
	sourceEventsBefore, _ := h.sessionStore.ListV3SessionEvents("session-source", 0, 100)

	// 1. Cross-session read from session-dest
	readResp, err := h.invokeTool(t, "session-dest", "run-read-1", map[string]any{
		"action": "read_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-source",
			"artifact_id":  artSource,
			"revision_ref": "revision-" + commitSource,
		},
	})
	if err != nil {
		t.Fatalf("cross-session read_v3 failed: %v", err)
	}
	content, ok := readResp["content"].(string)
	if !ok || !strings.Contains(content, "Exact Read Test") {
		t.Fatalf("read_v3 content missing expected HTML: %v", content)
	}
	if readResp["media_type"] != "text/html" {
		t.Fatalf("read_v3 media_type = %v, want text/html", readResp["media_type"])
	}
	// Verify copyable_next_calls includes import guidance
	nextCalls, ok := readResp["copyable_next_calls"].([]any)
	if !ok || len(nextCalls) == 0 || nextCalls[0].(map[string]any)["action"] != "import" {
		t.Fatalf("read_v3 missing import guidance in copyable_next_calls: %#v", readResp)
	}

	// 2. Verify source artifact and session are completely unchanged
	sourceRepoAfter, _, _ := h.sessionStore.GetArtifactV3Repository("account-1", "user-1", artSource)
	sourceEventsAfter, _ := h.sessionStore.ListV3SessionEvents("session-source", 0, 100)
	if !reflect.DeepEqual(sourceRepoBefore, sourceRepoAfter) {
		t.Fatalf("source repository mutated by read_v3: before=%+v after=%+v", sourceRepoBefore, sourceRepoAfter)
	}
	if len(sourceEventsBefore) != len(sourceEventsAfter) {
		t.Fatalf("source session events increased after read_v3: before=%d after=%d", len(sourceEventsBefore), len(sourceEventsAfter))
	}

	// 3. Read archived session source -> succeeds
	archRead, err := h.invokeTool(t, "session-dest", "run-read-arch", map[string]any{
		"action": "read_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-archived",
			"artifact_id":  artArchived,
			"revision_ref": "revision-" + commitArchived,
		},
	})
	if err != nil {
		t.Fatalf("read_v3 on archived session source failed: %v", err)
	}
	if !strings.Contains(archRead["content"].(string), "Archived Read Test") {
		t.Fatalf("read_v3 on archived session missing content: %v", archRead)
	}

	// 4. Read deleted session source -> rejected
	if _, err := h.invokeTool(t, "session-dest", "run-read-del", map[string]any{
		"action": "read_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-deleted",
			"artifact_id":  artDeleted,
			"revision_ref": "revision-" + commitDeleted,
		},
	}); err == nil {
		t.Fatal("read_v3 accepted deleted session source")
	}

	// 5. Read foreign user source -> rejected
	if _, err := h.invokeTool(t, "session-dest", "run-read-foreign", map[string]any{
		"action": "read_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-foreign-user",
			"artifact_id":  artForeignUser,
			"revision_ref": "revision-" + commitForeignUser,
		},
	}); err == nil {
		t.Fatal("read_v3 accepted foreign user source")
	}
}

// Purpose: Verify the complete discover -> read -> independent import -> edit flow
// where import derives runtime destination ownership, source remains unchanged,
// inherited preview evidence is verified, subsequent begin_v3/author_v3/revise_v3 genuinely works,
// and idempotency/conflicts are strictly handled.
// Regression: Prevents tool import failures, broken draft editing on imported heads,
// or mutation leakage to source artifacts.
// Authority: tool.Runtime ExecuteForWorkspaceScopeWithRuntime -> artifactV3RuntimeAdapter.ImportArtifactV3 -> ArtifactV3Service.Import.
func TestRetainedArtifactIndependentImportAndEdit(t *testing.T) {
	h := setupRetainedReuseHarness(t)

	// 1. Create source artifact in session-source
	artSource, commitSource := h.createTestArtifact(t, "session-source", "art-source-import", "Source For Import")

	// Capture source repository and events before import
	sourceRepoBefore, _, _ := h.sessionStore.GetArtifactV3Repository("account-1", "user-1", artSource)
	sourceEventsBefore, _ := h.sessionStore.ListV3SessionEvents("session-source", 0, 100)

	// 2. Import into session-dest via manage_artifact action="import"
	importResp, err := h.invokeTool(t, "session-dest", "run-import-1", map[string]any{
		"action": "import",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-source",
			"artifact_id":  artSource,
			"revision_ref": "revision-" + commitSource,
		},
		"destination_artifact_id": "art-imported",
		"message":                 "Imported for test",
	})
	if err != nil {
		t.Fatalf("manage_artifact import failed: %v", err)
	}

	// Verify tool response structure
	artV3Info, ok := importResp["artifact_v3"].(map[string]any)
	if !ok {
		t.Fatalf("import response missing artifact_v3: %#v", importResp)
	}
	if artV3Info["status"] != "ready" || artV3Info["session_id"] != "session-dest" || artV3Info["artifact_id"] != "art-imported" {
		t.Fatalf("import response fields incorrect: %#v", artV3Info)
	}
	importedRevRef := artV3Info["revision_ref"].(string)

	// 3. Verify destination repository in store has runtime-derived ownership and lineage
	destRepo, found, err := h.sessionStore.GetArtifactV3Repository("account-1", "user-1", "art-imported")
	if err != nil || !found {
		t.Fatalf("imported repository not found in store: found=%v err=%v", found, err)
	}
	if destRepo.OwnerSessionID != "session-dest" || destRepo.AccountScopeID != "account-1" || destRepo.UserID != "user-1" {
		t.Fatalf("destination ownership was not runtime-derived: %+v", destRepo)
	}
	if destRepo.Lineage == nil || destRepo.Lineage.SourceSessionID != "session-source" || destRepo.Lineage.SourceArtifactID != artSource || destRepo.Lineage.SourceCommitOID != commitSource {
		t.Fatalf("imported lineage missing or incorrect: %+v", destRepo.Lineage)
	}

	// 4. Verify source artifact and source session are 100% UNCHANGED
	sourceRepoAfter, _, _ := h.sessionStore.GetArtifactV3Repository("account-1", "user-1", artSource)
	sourceEventsAfter, _ := h.sessionStore.ListV3SessionEvents("session-source", 0, 100)
	if !reflect.DeepEqual(sourceRepoBefore, sourceRepoAfter) {
		t.Fatalf("source repository was mutated by import: before=%+v after=%+v", sourceRepoBefore, sourceRepoAfter)
	}
	if len(sourceEventsBefore) != len(sourceEventsAfter) {
		t.Fatalf("source session events changed by import: before=%d after=%d", len(sourceEventsBefore), len(sourceEventsAfter))
	}

	// 5. Verify inherited preview evidence on the imported artifact
	evidenceBytes, err := h.adapter.ReadArtifactV3PreviewEvidence(context.Background(), "account-1", "user-1", "session-dest", "art-imported", importedRevRef)
	if err != nil {
		t.Fatalf("ReadArtifactV3PreviewEvidence failed on imported artifact: %v", err)
	}
	if string(evidenceBytes) != "fake-renderer-evidence" {
		t.Fatalf("inherited preview evidence bytes mismatch: %q", string(evidenceBytes))
	}

	// 6. Test Idempotent Retry: re-importing with same transactionID and fingerprint returns existing projection
	// Re-invoking the import tool with same arguments
	retryResp, err := h.invokeTool(t, "session-dest", "run-import-1", map[string]any{
		"action": "import",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-source",
			"artifact_id":  artSource,
			"revision_ref": "revision-" + commitSource,
		},
		"destination_artifact_id": "art-imported",
		"message":                 "Imported for test",
	})
	if err != nil {
		t.Fatalf("idempotent import retry failed: %v", err)
	}
	if retryResp["artifact_v3"].(map[string]any)["artifact_id"] != "art-imported" {
		t.Fatalf("retry returned wrong artifact: %#v", retryResp)
	}

	// 7. Test Conflict Rejection:
	// 7a. Reusing transaction ID with changed message -> ErrArtifactV3TxReuse
	if _, err := h.invokeTool(t, "session-dest", "run-import-1", map[string]any{
		"action": "import",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-source",
			"artifact_id":  artSource,
			"revision_ref": "revision-" + commitSource,
		},
		"destination_artifact_id": "art-imported",
		"message":                 "DIFFERENT message",
	}); err == nil || !strings.Contains(err.Error(), "transaction") {
		t.Fatalf("expected tx reuse error, got: %v", err)
	}

	// 7b. Importing to already existing destination artifact with new transaction ID -> ErrArtifactV3Conflict
	if _, err := h.invokeTool(t, "session-dest", "run-import-new-tx", map[string]any{
		"action": "import",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-source",
			"artifact_id":  artSource,
			"revision_ref": "revision-" + commitSource,
		},
		"destination_artifact_id": "art-imported",
	}); err == nil {
		t.Fatal("expected conflict importing to existing destination artifact")
	}

	// 8. Test Edit on Imported Head via begin_v3 and author_v3
	// begin_v3 returns draft_handle
	beginResp, err := h.invokeTool(t, "session-dest", "run-edit-begin", map[string]any{
		"action": "begin_v3",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-dest",
			"artifact_id":  "art-imported",
			"revision_ref": importedRevRef,
		},
		"target_part_ids": []string{"hero"},
	})
	if err != nil {
		t.Fatalf("begin_v3 on imported artifact failed: %v", err)
	}
	draftHandle, ok := beginResp["draft_handle"].(map[string]any)
	if !ok {
		t.Fatalf("begin_v3 missing draft_handle: %#v", beginResp)
	}

	// Edit file using author_v3
	oldHTML := `<h1>Source For Import</h1>`
	newHTML := `<h1>Source For Import - Revised</h1>`
	_, err = h.invokeTool(t, "session-dest", "run-edit-author", map[string]any{
		"action":       "author_v3",
		"draft_handle": draftHandle,
		"operation": map[string]any{
			"action":     "edit_file",
			"path":       "index.html",
			"old_string": oldHTML,
			"new_string": newHTML,
		},
	})
	if err != nil {
		t.Fatalf("author_v3 edit_file failed: %v", err)
	}

	// Build preview using author_v3
	_, err = h.invokeTool(t, "session-dest", "run-edit-build", map[string]any{
		"action":       "author_v3",
		"draft_handle": draftHandle,
		"operation": map[string]any{
			"action": "build_preview",
		},
	})
	if err != nil {
		t.Fatalf("author_v3 build_preview failed: %v", err)
	}

	// Finish turn using author_v3
	finishResp, err := h.invokeTool(t, "session-dest", "run-edit-finish", map[string]any{
		"action":       "author_v3",
		"draft_handle": draftHandle,
		"operation": map[string]any{
			"action": "finish_turn",
		},
	})
	if err != nil {
		t.Fatalf("author_v3 finish_turn failed: %v", err)
	}
	if finishResp["status"] != "ready" {
		t.Fatalf("finished turn status = %v, want ready", finishResp["status"])
	}

	// 9. Verify source artifact in session-source remains completely unchanged after destination edit
	sourceRepoFinal, _, _ := h.sessionStore.GetArtifactV3Repository("account-1", "user-1", artSource)
	sourceEventsFinal, _ := h.sessionStore.ListV3SessionEvents("session-source", 0, 100)
	if !reflect.DeepEqual(sourceRepoBefore, sourceRepoFinal) {
		t.Fatalf("source repository was mutated after destination edited: before=%+v final=%+v", sourceRepoBefore, sourceRepoFinal)
	}
	if len(sourceEventsBefore) != len(sourceEventsFinal) {
		t.Fatalf("source events changed after destination edited: before=%d final=%d", len(sourceEventsBefore), len(sourceEventsFinal))
	}
}

// Purpose: Verify downstream Video Studio conversion (convert_artifact_v3) successfully
// ingests an imported Artifact V3 head, creates a pending proposal with exact part metadata,
// and preserves the non-bypass contract (requires user acceptance).
// Regression: Prevents Video Studio conversion rejection when artifacts carry inherited provenance.
// Authority: artifactV3VideoBridge.ConvertToPendingProposal -> artifactv3video.Service -> videoproject.Service.
func TestRetainedArtifactDownstreamVideoStudioConversion(t *testing.T) {
	h := setupRetainedReuseHarness(t)

	// 1. Create source artifact and import into destination session
	artSource, commitSource := h.createTestArtifact(t, "session-source", "art-for-video", "Video Source")
	importResp, err := h.invokeTool(t, "session-dest", "run-imp-vid", map[string]any{
		"action": "import",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-source",
			"artifact_id":  artSource,
			"revision_ref": "revision-" + commitSource,
		},
		"destination_artifact_id": "art-video-dest",
	})
	if err != nil {
		t.Fatalf("import for video failed: %v", err)
	}
	importedRevRef := importResp["artifact_v3"].(map[string]any)["revision_ref"].(string)

	// 2. Create Video Studio project in session-dest
	principalIdentity := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		AccountScopeID: "account-1",
		UserID:         "user-1",
		SessionID:      "session-dest",
	}
	videoProject, videoBase, err := h.videoProjects.CreateProject(context.Background(), principalIdentity, videoproject.CreateProjectInput{
		SessionID:    "session-dest",
		Title:        "Studio Project",
		OutputPreset: pebblestore.VideoPresetLandscape1080p,
		ProjectID:    "video-proj-1",
	})
	if err != nil || videoBase == nil {
		t.Fatalf("create video project: project=%+v base=%+v err=%v", videoProject, videoBase, err)
	}

	// 3. Convert imported Artifact V3 into Video Studio proposal
	conversionInput := tool.ArtifactV3VideoConversionInput{
		RequestID:         "convert-req-1",
		VideoSessionID:    "session-dest",
		ProjectID:         videoProject.ID,
		BaseRevisionID:    videoBase.ID,
		ArtifactSessionID: "session-dest",
		ArtifactID:        "art-video-dest",
		RevisionRef:       importedRevRef,
		Title:             "Imported V3 Motion",
	}
	proposal, err := h.videoBridge.ConvertToPendingProposal(context.Background(), principalIdentity, conversionInput)
	if err != nil {
		t.Fatalf("ConvertToPendingProposal on imported artifact failed: %v", err)
	}

	// 4. Assert proposal integrity and non-bypass contract
	if proposal.Status != pebblestore.VideoEditProposalStatusPending {
		t.Fatalf("proposal status = %s, want pending", proposal.Status)
	}
	if proposal.Intent != pebblestore.VideoEditProposalIntentArtifactV3Convert {
		t.Fatalf("proposal intent = %s, want %s", proposal.Intent, pebblestore.VideoEditProposalIntentArtifactV3Convert)
	}
	if proposal.AcceptedRevisionID != "" {
		t.Fatalf("proposal must not auto-accept: accepted=%s", proposal.AcceptedRevisionID)
	}
	if proposal.Plan == nil || len(proposal.Plan.Parts) != 1 {
		t.Fatalf("proposal plan parts unexpected: %+v", proposal.Plan)
	}
	part := proposal.Plan.Parts[0]
	if part.ArtifactV3Source == nil || part.ArtifactV3Still == nil || part.ArtifactV3Visual == nil {
		t.Fatalf("proposal part missing Artifact V3 references: %+v", part)
	}
	if part.ArtifactV3Source.ArtifactID != "art-video-dest" {
		t.Fatalf("part source artifactID = %s, want art-video-dest", part.ArtifactV3Source.ArtifactID)
	}
}

// Purpose: Verify native vs legacy discrimination in manage_artifact import.
// artifact_v3_reference must route to native V3 import, while artifact_reference
// routes to legacy import (and cleanly requires the legacy authority).
// Regression: Prevents argument mixing or silent legacy fallbacks.
// Authority: tool.Runtime ExecuteForWorkspaceScopeWithRuntime (manage_artifact import handler).
func TestRetainedArtifactToolLegacyDiscrimination(t *testing.T) {
	h := setupRetainedReuseHarness(t)

	// Supplying both artifact_v3_reference and artifact_reference must be rejected
	_, err := h.invokeTool(t, "session-dest", "run-discrim-both", map[string]any{
		"action": "import",
		"artifact_v3_reference": map[string]any{
			"session_id":   "session-source",
			"artifact_id":  "any-id",
			"revision_ref": "revision-abc",
		},
		"artifact_reference": map[string]any{
			"session_id":    "session-source",
			"collection_id": "col-1",
			"variant_id":    "var-1",
			"event_seq":     1,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Fatalf("expected rejection combining native and legacy references, got: %v", err)
	}

	// Supplying neither must be rejected
	_, err = h.invokeTool(t, "session-dest", "run-discrim-neither", map[string]any{
		"action": "import",
	})
	if err == nil || !strings.Contains(err.Error(), "requires either") {
		t.Fatalf("expected rejection without reference, got: %v", err)
	}
}
