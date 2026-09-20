package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// These tests use actual Pebble mutations and bare Git, not mock chain state.
// Requirement: Import publishes an independent editable root in one canonical
// mutation. Threats: cross-principal reads, partial publication, forged reference
// replay, byte/part loss and accidentally accepting a source iteration.
// Authority.Import/prepareV3ArtifactMutation/attachGitProjection own the contract;
// this is the narrowest layer that proves durable projections and exact bytes.
type importTestResolver struct{ *pebblestore.SessionStore }

func (s importTestResolver) ListPendingSessionArtifactCleanups(limit int) ([]pebblestore.V3SessionTombstone, error) {
	return s.ListPendingV3SessionArtifactCleanups(limit)
}
func (s importTestResolver) MarkSessionArtifactCleanupComplete(id string) error {
	return s.MarkV3SessionArtifactCleanupComplete(id)
}
func (s importTestResolver) GetSessionTombstone(id string) (pebblestore.V3SessionTombstone, bool, error) {
	return s.GetV3SessionTombstone(id)
}

type importTestStore struct {
	*pebblestore.SessionStore
	last   pebblestore.V3SessionMutationResult
	mu     sync.Mutex
	db     *pebblestore.Store
	dbPath string
}

func (s *importTestStore) reopen(t *testing.T) *Authority {
	t.Helper()
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := pebblestore.Open(s.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s.db = db
	s.SessionStore = pebblestore.NewSessionStore(db)
	return NewAuthority(NewRegistry(importTestResolver{s.SessionStore}, Limits{}), s)
}

func (s *importTestStore) ApplySessionMutation(in pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	result, err := s.SessionStore.ApplyV3SessionMutation(in)
	if err == nil {
		s.mu.Lock()
		s.last = result
		s.mu.Unlock()
	}
	return result, err
}
func importFixture(t *testing.T) (*Authority, *importTestStore, Principal, Principal) {
	t.Helper()
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	dbPath := filepath.Join(t.TempDir(), "db")
	db, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	sessions := pebblestore.NewSessionStore(db)
	for _, owner := range []Principal{{SessionID: "source", AccountScopeID: "account", UserID: "user"}, {SessionID: "destination", AccountScopeID: "account", UserID: "user"}, {SessionID: "foreign-user", AccountScopeID: "account", UserID: "other"}, {SessionID: "foreign-account", AccountScopeID: "other", UserID: "user"}} {
		_, err := sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: owner.SessionID, AccountScopeID: owner.AccountScopeID, UserID: owner.UserID, Kind: pebblestore.V3SessionMutationCreateSession, IdempotencyKey: "create-" + owner.SessionID, RequestHash: "create-" + owner.SessionID, Session: &pebblestore.SessionSnapshot{ID: owner.SessionID, WorkspacePath: t.TempDir(), Title: owner.SessionID}, NowUnixMs: 1000})
		if err != nil {
			t.Fatal(err)
		}
	}
	store := &importTestStore{SessionStore: sessions, db: db, dbPath: dbPath}
	t.Cleanup(func() { store.db.Close() })
	return NewAuthority(NewRegistry(importTestResolver{sessions}, Limits{}), store), store, Principal{SessionID: "destination", AccountScopeID: "account", UserID: "user"}, Principal{SessionID: "source", AccountScopeID: "account", UserID: "user"}
}
func importRef(v pebblestore.SessionArtifactVariant) pebblestore.SessionArtifactSelectionReference {
	return pebblestore.SessionArtifactSelectionReference{SessionID: v.SessionID, CollectionID: v.CollectionID, VariantID: v.ID, EventSeq: v.EventSeq}
}
func importSource(t *testing.T, a *Authority, owner Principal) pebblestore.SessionArtifactVariant {
	t.Helper()
	v, err := a.Create(context.Background(), owner, CreateInput{RequestID: "source-create", CollectionID: "source-collection", CollectionName: "Source", VariantID: "source-version", Filename: "index.html", MediaType: "text/html", AnimationProfile: &pebblestore.SessionArtifactAnimationProfile{ProfileID: "motion_ui", RegistryVersion: "2026-08-16.v1", RuntimeKind: "native_css_waapi_svg", Budgets: pebblestore.SessionArtifactAnimationBudgets{MaxSimultaneousLivePreviews: 3, MaxDevicePixelRatio: 2, MaxCanvasPixels: 4194304, MaxDrawCallsPerFrame: 400, PauseWhenOffscreen: true, StopWhenDocumentHidden: true, ReducedMotionBehavior: "static_first_frame"}}, Body: []byte("<!doctype html><main id=scene>original</main>"), AutoAccept: true, Parts: []pebblestore.SessionArtifactPart{{ID: "scene", Label: "Scene", Kind: "selector", Selector: "#scene"}}})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func importEvents(t *testing.T, s *importTestStore, id string) []pebblestore.V3SessionEvent {
	t.Helper()
	events, err := s.ListV3SessionEvents(id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestAuthorityImportFidelityReplayAndEditing(t *testing.T) {
	a, s, dest, source := importFixture(t)
	ctx := context.Background()
	v := importSource(t, a, source)
	before := importEvents(t, s, source.SessionID)
	destBefore := importEvents(t, s, dest.SessionID)
	input := ImportVariantInput{RequestID: "import-one", Source: importRef(v)}
	got, err := a.Import(ctx, dest, input)
	if err != nil {
		t.Fatal(err)
	}
	if got.RepositoryID == v.RepositoryID || got.ArtifactChainID == v.ArtifactChainID || got.Status != "ready" || got.SessionID != dest.SessionID || got.RevisionNumber != 1 || !reflect.DeepEqual(got.Parts, v.Parts) || !reflect.DeepEqual(got.AnimationProfile, v.AnimationProfile) {
		t.Fatalf("not independent faithful root: %+v", got)
	}
	body, _, err := a.Read(ctx, dest, got.ID, 4096)
	if err != nil || !bytes.Equal(body, []byte("<!doctype html><main id=scene>original</main>")) {
		t.Fatalf("bytes: %q %v", body, err)
	}
	if !reflect.DeepEqual(before, importEvents(t, s, source.SessionID)) {
		t.Fatal("source events mutated")
	}
	if len(importEvents(t, s, dest.SessionID)) != len(destBefore)+1 || s.last.RealtimeOutbox == nil {
		t.Fatal("import was not one atomic event/outbox publication")
	}
	chain, ok, err := s.GetSessionArtifactChain(dest.AccountScopeID, dest.UserID, got.ArtifactChainID)
	if err != nil || !ok || chain.Head != importRef(got) {
		t.Fatalf("chain head: %+v %v", chain, err)
	}
	reopened := NewAuthority(NewRegistry(importTestResolver{s.SessionStore}, Limits{}), s)
	replay, err := reopened.Import(ctx, dest, input)
	if err != nil || replay.CommitOID != got.CommitOID || replay.EventSeq != got.EventSeq {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	changed := input
	changed.CollectionName = "changed"
	if _, err := reopened.Import(ctx, dest, changed); err == nil {
		t.Fatal("changed request replay accepted")
	}
	next, err := reopened.Create(ctx, dest, CreateInput{RequestID: "edit-import", CollectionID: got.CollectionID, VariantID: "edited", Filename: got.Filename, MediaType: got.MediaType, SourceSessionID: got.SessionID, SourceCollectionID: got.CollectionID, SourceVariantID: got.ID, SourceEventSeq: got.EventSeq, Body: []byte("edited"), AutoAccept: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.ParentCommitOIDs) != 1 || next.ParentCommitOIDs[0] != got.CommitOID {
		t.Fatal("edit did not continue imported head")
	}
	replay, err = reopened.Import(ctx, dest, input)
	if err != nil || replay.CommitOID != got.CommitOID {
		t.Fatalf("replay after edit: %v", err)
	}
}

func TestAuthorityImportPackage(t *testing.T) {
	a, s, dest, source := importFixture(t)
	_ = s
	ctx := context.Background()
	v, err := a.CreatePackage(ctx, source, CreatePackageInput{CreateInput: CreateInput{RequestID: "pkg", CollectionID: "pkg", CollectionName: "Package", VariantID: "package", Filename: "bundle.zip", MediaType: "application/zip", AutoAccept: true}, Entries: []PackageEntry{{Name: "index.html", Data: []byte("<main>package</main>")}, {Name: "assets/style.css", Data: []byte("main{color:red}")}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Import(ctx, dest, ImportVariantInput{RequestID: "package-import", Source: importRef(v)})
	if err != nil {
		t.Fatal(err)
	}
	original, err := a.ReadVariant(ctx, source, v, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := a.ReadVariant(ctx, dest, got, 1<<20)
	if err != nil || !bytes.Equal(original, imported) {
		t.Fatalf("archive changed %v", err)
	}
	_, body, _, err := a.ReadPackageReference(ctx, dest, importRef(got), "assets/style.css", 1<<20)
	if err != nil || string(body) != "main{color:red}" {
		t.Fatalf("asset %q %v", body, err)
	}
}

func TestAuthorityImportComposition(t *testing.T) {
	a, s, dest, source := importFixture(t)
	ctx := context.Background()
	v, err := a.CreateInitialComposition(ctx, source, CreateInitialCompositionInput{CreateInput: CreateInput{RequestID: "parts", CollectionID: "parts", CollectionName: "Parts", VariantID: "parts-root", Filename: "page.html", MediaType: "text/html", AutoAccept: true}, ArtifactChainID: pebblestore.RootSessionArtifactChainID(source.SessionID, "parts", "parts-root"), CompositionID: "composition", Parts: []InitialPartInput{{Definition: pebblestore.SessionArtifactPartDefinition{ID: "header", Label: "Header"}, RevisionID: "header-v1", MediaType: "text/html", Body: []byte("<header>one</header>")}, {Definition: pebblestore.SessionArtifactPartDefinition{ID: "body", Label: "Body"}, RevisionID: "body-v1", MediaType: "text/html", Body: []byte("<main>two</main>")}}})
	if err != nil {
		t.Fatal(err)
	}
	lockedPart := v.Composition.Parts[0]
	partRevision, ok, err := s.GetSessionArtifactPartRevision(source.AccountScopeID, source.UserID, lockedPart.Revision.OwnerSessionID, lockedPart.Revision.ArtifactChainID, lockedPart.Revision.PartID, lockedPart.Revision.PartRevisionID)
	if err != nil || !ok {
		t.Fatal("missing source part", err)
	}
	v, err = a.SelectPartRevisions(ctx, source, SelectPartRevisionsInput{RequestID: "lock-source", CollectionID: v.CollectionID, VariantID: "locked-source", ArtifactStepID: "locked-step", SourceArtifact: importRef(v), SourceComposition: *v.Composition, Choices: []PartRevisionChoiceInput{{PartID: lockedPart.PartID, Revision: lockedPart.Revision, RevisionEventSeq: partRevision.EventSeq, Locked: true}}})
	if err != nil {
		t.Fatal(err)
	}
	before := importEvents(t, s, source.SessionID)
	input := ImportVariantInput{RequestID: "parts-import", Source: importRef(v)}
	got, err := a.Import(ctx, dest, input)
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := a.Read(ctx, dest, got.ID, 1<<20)
	if err != nil || string(body) != "<header>one</header><main>two</main>" {
		t.Fatalf("composition %q %v", body, err)
	}
	if got.Composition.OwnerSessionID != dest.SessionID || got.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || !got.Composition.Parts[0].Locked || !reflect.DeepEqual(got.Composition.Construction, v.Composition.Construction) {
		t.Fatal("composition ownership, locks or construction changed")
	}
	if _, err := a.Import(ctx, dest, input); err != nil {
		t.Fatalf("composition replay: %v", err)
	}
	next, err := a.PublishPartReplacement(ctx, dest, PublishPartReplacementInput{RequestID: "replace", CallID: "replace", CollectionID: got.CollectionID, VariantID: "replacement", ArtifactStepID: "replace-step", CandidateIndex: 1, AutoAccept: true, SourceArtifact: importRef(got), SourceComposition: *got.Composition, PartDefinition: got.PartDefinitions[1], SourcePartRevision: got.Composition.Parts[1].Revision, MediaType: "text/html", Body: []byte("<main>new</main>")})
	if err != nil {
		t.Fatal(err)
	}
	body, _, err = a.Read(ctx, dest, next.ID, 1<<20)
	if err != nil || string(body) != "<header>one</header><main>new</main>" {
		t.Fatalf("edit %q %v", body, err)
	}
	if !reflect.DeepEqual(before, importEvents(t, s, source.SessionID)) {
		t.Fatal("source changed")
	}
}

func TestAuthorityImportUnselectedAndRejectionAtomicity(t *testing.T) {
	a, s, dest, source := importFixture(t)
	ctx := context.Background()
	v := importSource(t, a, source)
	candidate, err := a.Create(ctx, source, CreateInput{RequestID: "candidate", CollectionID: v.CollectionID, VariantID: "alternative", Filename: v.Filename, MediaType: v.MediaType, SourceSessionID: v.SessionID, SourceCollectionID: v.CollectionID, SourceVariantID: v.ID, SourceEventSeq: v.EventSeq, Body: []byte("alternative"), AutoAccept: false})
	if err != nil {
		t.Fatal(err)
	}
	before := importEvents(t, s, source.SessionID)
	got, err := a.Import(ctx, dest, ImportVariantInput{RequestID: "alternative-import", Source: importRef(candidate)})
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := a.Read(ctx, dest, got.ID, 4096)
	if err != nil || string(body) != "alternative" {
		t.Fatalf("alternative %q %v", body, err)
	}
	if !reflect.DeepEqual(before, importEvents(t, s, source.SessionID)) {
		t.Fatal("source selected by import")
	}
	selectedRef, err := a.Select(source, "select-alternative", v.CollectionID, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	selectedImport, err := a.Import(ctx, dest, ImportVariantInput{RequestID: "selected-import", Source: selectedRef})
	if err != nil || selectedImport.DigestSHA256 != candidate.DigestSHA256 {
		t.Fatalf("selected reference import: %v", err)
	}
	oldImport, err := a.Import(ctx, dest, ImportVariantInput{RequestID: "historical-import", Source: importRef(v)})
	if err != nil || oldImport.DigestSHA256 != v.DigestSHA256 {
		t.Fatalf("historical reference import: %v", err)
	}
	for _, tc := range []struct {
		name  string
		ref   pebblestore.SessionArtifactSelectionReference
		owner Principal
	}{
		{"stale", pebblestore.SessionArtifactSelectionReference{SessionID: v.SessionID, CollectionID: v.CollectionID, VariantID: v.ID, EventSeq: v.EventSeq + 100}, dest},
		{"missing", pebblestore.SessionArtifactSelectionReference{SessionID: "missing", CollectionID: v.CollectionID, VariantID: v.ID, EventSeq: v.EventSeq}, dest},
		{"foreign-user", importRef(v), Principal{SessionID: "foreign-user", AccountScopeID: "account", UserID: "other"}},
		{"foreign-account", importRef(v), Principal{SessionID: "foreign-account", AccountScopeID: "other", UserID: "user"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := importEvents(t, s, tc.owner.SessionID)
			_, err := a.Import(ctx, tc.owner, ImportVariantInput{RequestID: tc.name, Source: tc.ref})
			if err == nil {
				t.Fatal("invalid source accepted")
			}
			if !reflect.DeepEqual(before, importEvents(t, s, tc.owner.SessionID)) {
				t.Fatal("partial destination events")
			}
		})
	}
}

func TestAuthorityImportFailureRetry(t *testing.T) {
	a, s, dest, source := importFixture(t)
	ctx := context.Background()
	v := importSource(t, a, source)
	input := ImportVariantInput{RequestID: "retry", Source: importRef(v)}
	before := importEvents(t, s, dest.SessionID)
	outboxBefore, err := s.ListV3RealtimeOutboxForSessionAfterSeq(dest.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	restore := s.SetArtifactImportCommitHookForTest(func(string) error { return errors.New("injected durable publication failure") })
	if _, err := a.Import(ctx, dest, input); err == nil {
		t.Fatal("failure not injected")
	}
	if !reflect.DeepEqual(before, importEvents(t, s, dest.SessionID)) {
		t.Fatal("partial event")
	}
	outboxAfter, err := s.ListV3RealtimeOutboxForSessionAfterSeq(dest.SessionID, 0, 100)
	if err != nil || !reflect.DeepEqual(outboxBefore, outboxAfter) {
		t.Fatal("partial outbox")
	}
	collections, err := s.ListSessionArtifactCollections(dest.AccountScopeID, dest.SessionID, "", 100)
	if err != nil || len(collections) != 0 {
		t.Fatalf("partial collections %+v %v", collections, err)
	}
	restore()
	reopened := s.reopen(t)
	changed := input
	changed.CollectionName = "substitute"
	if _, err := reopened.Import(ctx, dest, changed); err == nil {
		t.Fatal("changed retry accepted")
	}
	got, err := reopened.Import(ctx, dest, input)
	if err != nil || got.Status != "ready" {
		t.Fatalf("retry %v", err)
	}
	if len(importEvents(t, s, dest.SessionID)) != len(before)+1 {
		t.Fatal("retry duplicate events")
	}
}

// Requirement: retained archived sources are read-only, deleted/failed/corrupt
// sources never publish, and source reads never recreate missing storage.
// Authority.Import, retainedImportOwner and OpenExisting are tested with actual
// tombstones and intentionally damaged test fixtures, not ambient repositories.
func TestAuthorityImportRetainedAndInvalidSources(t *testing.T) {
	for _, kind := range []string{"archived", "deleted", "failed", "corrupt", "missing-repository", "symlink-repository"} {
		t.Run(kind, func(t *testing.T) {
			a, s, dest, source := importFixture(t)
			ctx := context.Background()
			v := importSource(t, a, source)
			path := filepath.Join(a.registry.repositoryRoot, v.RepositoryID+".git")
			switch kind {
			case "archived":
				if err := s.ArchiveSession(source.SessionID); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := s.DeleteSession(source.SessionID); err != nil {
					t.Fatal(err)
				}
			case "failed", "corrupt":
				damaged := v
				if kind == "failed" {
					damaged.Status = pebblestore.SessionArtifactStatusFailed
				} else {
					damaged.DigestSHA256 = "wrong"
				}
				raw, err := json.Marshal(damaged)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.db.PutBytes(pebblestore.KeySessionArtifactVariant(source.AccountScopeID, source.SessionID, v.CollectionID, v.ID), raw); err != nil {
					t.Fatal(err)
				}
			case "missing-repository", "symlink-repository":
				if err := os.Rename(path, path+"-retained"); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink-repository" {
					if err := os.Symlink(path+"-retained", path); err != nil {
						t.Fatal(err)
					}
				}
			}
			a = NewAuthority(NewRegistry(importTestResolver{s.SessionStore}, Limits{}), s)
			before := importEvents(t, s, dest.SessionID)
			got, err := a.Import(ctx, dest, ImportVariantInput{RequestID: "invalid-import", Source: importRef(v)})
			if kind == "archived" {
				if err != nil || got.DigestSHA256 != v.DigestSHA256 {
					t.Fatalf("retained import: %v", err)
				}
				if _, live, err := s.GetSession(source.SessionID); err != nil || live {
					t.Fatal("source reactivated")
				}
				return
			}
			if err == nil {
				t.Fatal("invalid source accepted")
			}
			if !reflect.DeepEqual(before, importEvents(t, s, dest.SessionID)) {
				t.Fatal("partial publication")
			}
			collections, err := s.ListSessionArtifactCollections(dest.AccountScopeID, dest.SessionID, "", 100)
			if err != nil || len(collections) != 0 {
				t.Fatal("partial collections")
			}
			if kind == "missing-repository" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("source repository recreated")
				}
			}
		})
	}
}

// Requirement: concurrent identical requests converge (or report a retryable Git
// conflict) with a single event; a conflicting source must not overwrite it.
// Real Authority/Git/Pebble CAS is narrower than a provider-backed workflow.
func TestAuthorityImportConcurrentReplay(t *testing.T) {
	a, s, dest, source := importFixture(t)
	v := importSource(t, a, source)
	ctx := context.Background()
	input := ImportVariantInput{RequestID: "concurrent", Source: importRef(v)}
	before := importEvents(t, s, dest.SessionID)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; _, err := a.Import(ctx, dest, input); results <- err }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		<-results
	}
	got, err := a.Import(ctx, dest, input)
	if err != nil || got.Status != "ready" {
		t.Fatalf("retry: %v", err)
	}
	if len(importEvents(t, s, dest.SessionID)) != len(before)+1 {
		t.Fatal("duplicate publication")
	}
}
