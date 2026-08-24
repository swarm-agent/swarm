package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type authorityMetadata struct {
	collection       pebblestore.SessionArtifactCollection
	sourceCollection pebblestore.SessionArtifactCollection
	variant          pebblestore.SessionArtifactVariant
	sourceVariant    pebblestore.SessionArtifactVariant
	readyCalls       int
	failReady        bool
	partDefinitions  map[string]pebblestore.SessionArtifactPartDefinition
	partRevisions    map[string]pebblestore.SessionArtifactPartRevision
	compositions     map[string]pebblestore.SessionArtifactComposition
}

func (m *authorityMetadata) GetSessionArtifactCollection(_, sessionID, id string) (pebblestore.SessionArtifactCollection, bool, error) {
	if m.sourceCollection.SessionID == sessionID && m.sourceCollection.ID == id {
		return m.sourceCollection, true, nil
	}
	return m.collection, m.collection.SessionID == sessionID && m.collection.ID == id, nil
}
func (m *authorityMetadata) GetSessionArtifactVariant(_, sessionID, collectionID, id string) (pebblestore.SessionArtifactVariant, bool, error) {
	if m.sourceVariant.SessionID == sessionID && m.sourceVariant.CollectionID == collectionID && m.sourceVariant.ID == id {
		return m.sourceVariant, true, nil
	}
	return m.variant, m.variant.SessionID == sessionID && m.variant.CollectionID == collectionID && m.variant.ID == id, nil
}
func (m *authorityMetadata) GetSessionArtifactVariantByID(_, _, id string) (pebblestore.SessionArtifactVariant, bool, error) {
	return m.variant, m.variant.ID == id, nil
}
func (m *authorityMetadata) ListSessionArtifactCollections(_, _, _ string, _ int) ([]pebblestore.SessionArtifactCollection, error) {
	if m.collection.ID == "" {
		return nil, nil
	}
	return []pebblestore.SessionArtifactCollection{m.collection}, nil
}
func (m *authorityMetadata) ListSessionArtifactVariants(_, _, collectionID string, _ int) ([]pebblestore.SessionArtifactVariant, error) {
	if m.variant.CollectionID != collectionID {
		return nil, nil
	}
	return []pebblestore.SessionArtifactVariant{m.variant}, nil
}
func (m *authorityMetadata) SearchSessionArtifactCatalog(_, _ string, _ pebblestore.SessionArtifactCatalogOptions) (pebblestore.SessionArtifactCatalogPage, error) {
	return pebblestore.SessionArtifactCatalogPage{}, nil
}
func (m *authorityMetadata) GetSessionArtifactPartDefinition(_, _, owner, chain, part string) (pebblestore.SessionArtifactPartDefinition, bool, error) {
	got, ok := m.partDefinitions[owner+"\x00"+chain+"\x00"+part]
	return got, ok, nil
}
func (m *authorityMetadata) GetSessionArtifactPartRevision(_, _, owner, chain, part, revision string) (pebblestore.SessionArtifactPartRevision, bool, error) {
	got, ok := m.partRevisions[owner+"\x00"+chain+"\x00"+part+"\x00"+revision]
	return got, ok, nil
}
func (m *authorityMetadata) GetSessionArtifactComposition(_, _, owner, chain, composition string) (pebblestore.SessionArtifactComposition, bool, error) {
	got, ok := m.compositions[owner+"\x00"+chain+"\x00"+composition]
	return got, ok, nil
}
func (m *authorityMetadata) ApplySessionMutation(input pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	projection := pebblestore.V3ArtifactProjection{Collection: input.Artifact.Collection, Variant: input.Artifact.Variant, Selection: input.Artifact.Selection}
	switch input.Kind {
	case pebblestore.V3SessionMutationCreateArtifact, pebblestore.V3SessionMutationUpdateArtifact:
		m.collection = input.Artifact.Collection
		m.collection.AccountScopeID, m.collection.SessionID, m.collection.Status = input.AccountScopeID, input.SessionID, pebblestore.SessionArtifactStatusStaging
		m.collection.VariantCount, m.collection.StagingCount = 1, 1
		m.variant = *input.Artifact.Variant
		m.variant.AccountScopeID, m.variant.SessionID, m.variant.CollectionID, m.variant.Status = input.AccountScopeID, input.SessionID, m.collection.ID, pebblestore.SessionArtifactStatusStaging
		if m.partDefinitions == nil {
			m.partDefinitions = map[string]pebblestore.SessionArtifactPartDefinition{}
		}
		if m.partRevisions == nil {
			m.partRevisions = map[string]pebblestore.SessionArtifactPartRevision{}
		}
		if m.compositions == nil {
			m.compositions = map[string]pebblestore.SessionArtifactComposition{}
		}
		for _, definition := range input.Artifact.PartDefinitions {
			definition.AccountScopeID, definition.UserID, definition.GraphState = input.AccountScopeID, input.UserID, pebblestore.SessionArtifactGraphAuthoritative
			m.partDefinitions[definition.OwnerSessionID+"\x00"+definition.ArtifactChainID+"\x00"+definition.ID] = definition
		}
		for _, revision := range input.Artifact.PartRevisions {
			revision.AccountScopeID, revision.UserID, revision.GraphState = input.AccountScopeID, input.UserID, pebblestore.SessionArtifactGraphAuthoritative
			m.partRevisions[revision.OwnerSessionID+"\x00"+revision.ArtifactChainID+"\x00"+revision.PartID+"\x00"+revision.ID] = revision
		}
		if input.Artifact.Composition != nil {
			composition := *input.Artifact.Composition
			composition.AccountScopeID, composition.UserID, composition.GraphState = input.AccountScopeID, input.UserID, pebblestore.SessionArtifactGraphAuthoritative
			m.compositions[composition.OwnerSessionID+"\x00"+composition.ArtifactChainID+"\x00"+composition.ID] = composition
			m.variant.PartGraphState, m.variant.Composition, m.variant.PartDefinitions = pebblestore.SessionArtifactGraphAuthoritative, &composition, input.Artifact.PartDefinitions
			projection.Variant = &m.variant
			projection.PartDefinitions, projection.PartRevisions, projection.Composition = input.Artifact.PartDefinitions, input.Artifact.PartRevisions, &composition
		}
	case pebblestore.V3SessionMutationFinalizeArtifact:
		m.readyCalls++
		if m.failReady {
			return pebblestore.V3SessionMutationResult{}, errors.New("metadata unavailable")
		}
		currentComposition, currentDefinitions, currentPartGraphState := m.variant.Composition, m.variant.PartDefinitions, m.variant.PartGraphState
		m.variant = *input.Artifact.Variant
		if m.variant.Composition == nil {
			m.variant.Composition, m.variant.PartDefinitions, m.variant.PartGraphState = currentComposition, currentDefinitions, currentPartGraphState
		}
		m.variant.Status = pebblestore.SessionArtifactStatusReady
		m.collection.Status = pebblestore.SessionArtifactStatusReady
		m.collection.StagingCount, m.collection.ReadyCount = 0, 1
		projection.Collection, projection.Variant, projection.Composition = m.collection, &m.variant, m.variant.Composition
	case pebblestore.V3SessionMutationFailArtifact:
		m.variant.Status, m.variant.FailureCode = pebblestore.SessionArtifactStatusFailed, input.Artifact.Variant.FailureCode
	case pebblestore.V3SessionMutationDeleteArtifactVariant:
		m.variant = pebblestore.SessionArtifactVariant{}
	case pebblestore.V3SessionMutationDeleteArtifactCollection:
		m.variant, m.collection = pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactCollection{}
	}
	return pebblestore.V3SessionMutationResult{Artifact: &projection}, nil
}

func authorityFixture(t *testing.T) (*Authority, *authorityMetadata, Principal) {
	t.Helper()
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := &registryResolver{sessions: []pebblestore.SessionSnapshot{
		{ID: "session-1", AccountScopeID: "account-1", UserID: "user-1", WorkspacePath: workspace},
		{ID: "source-session", AccountScopeID: "account-1", UserID: "user-1", WorkspacePath: workspace},
	}}
	metadata := &authorityMetadata{}
	return NewAuthority(NewRegistry(resolver, Limits{}), metadata), metadata, Principal{SessionID: "session-1", AccountScopeID: "account-1", UserID: "user-1", RunID: "run-1", PlanID: "plan-1", CheckpointID: "cp-1", AttemptID: "attempt-1"}
}

func TestAuthorityCreateFinalizesBytesBeforeReadyMetadata(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	created, err := authority.Create(context.Background(), principal, CreateInput{RequestID: "create-1", CollectionID: "collection-1", CollectionName: "Drafts", VariantID: "variant-1", Filename: "note.txt", MediaType: "text/plain", Presentation: pebblestore.SessionArtifactPresentation{Kind: "text", Previewable: true}, Body: []byte("managed")})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != pebblestore.SessionArtifactStatusReady || metadata.readyCalls != 1 || created.Lineage.RunID != "run-1" {
		t.Fatalf("created = %+v calls=%d", created, metadata.readyCalls)
	}
	body, stored, err := authority.Read(context.Background(), principal, "variant-1", 1024)
	if err != nil || string(body) != "managed" || stored.ID != "variant-1" {
		t.Fatalf("read body=%q stored=%+v err=%v", body, stored, err)
	}
}

func TestAuthorityMetadataFailureNeverPublishesReady(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	metadata.failReady = true
	_, err := authority.Create(context.Background(), principal, CreateInput{RequestID: "create-fail", CollectionID: "collection-1", CollectionName: "Drafts", VariantID: "variant-1", Filename: "note.txt", MediaType: "text/plain", Presentation: pebblestore.SessionArtifactPresentation{Kind: "text"}, Body: []byte("managed")})
	if err == nil {
		t.Fatal("expected metadata failure")
	}
	if metadata.variant.Status == pebblestore.SessionArtifactStatusReady {
		t.Fatalf("ready metadata published: %+v", metadata.variant)
	}
}

func TestAuthorityFinalizesPreallocatedManagedPlaceholder(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	principal.TaskCallID, principal.ChildSessionID = "call-1", "child-1"
	principal.IterationGroupID, principal.IterationID, principal.IterationIndex = "group-1", "iteration-1", 1
	principal.IterationLabel, principal.IterationTheme = "Compact", "compact"
	principal.IterationSectionID, principal.IterationSectionLabel, principal.IterationSectionStartMs, principal.IterationSectionEndMs = "part-2", "Part 2", 4000, 8000
	principal.PartID, principal.PartLabel, principal.PartKind = "part-2", "Part 2", "temporal"
	source := CreateInput{SourceSessionID: "source-session", SourceCollectionID: "source-collection", SourceVariantID: "source-variant", SourceEventSeq: 41}
	metadata.sourceCollection = pebblestore.SessionArtifactCollection{ID: source.SourceCollectionID, AccountScopeID: principal.AccountScopeID, SessionID: source.SourceSessionID, Status: pebblestore.SessionArtifactStatusReady, VariantCount: 1, ReadyCount: 1}
	metadata.sourceVariant = pebblestore.SessionArtifactVariant{ID: source.SourceVariantID, CollectionID: source.SourceCollectionID, AccountScopeID: principal.AccountScopeID, SessionID: source.SourceSessionID, Status: pebblestore.SessionArtifactStatusReady, EventSeq: source.SourceEventSeq, MediaType: "text/html"}
	lineage := authority.lineage(principal, source)
	collectionLineage := lineage
	collectionLineage.SourceSessionID, collectionLineage.SourceCollectionID, collectionLineage.SourceVariantID, collectionLineage.SourceEventSeq, collectionLineage.ChildSessionID = "", "", "", 0, ""
	collectionLineage.IterationID, collectionLineage.IterationIndex, collectionLineage.IterationLabel, collectionLineage.IterationTheme = "", 0, "", ""
	collectionLineage.IterationSectionID, collectionLineage.IterationSectionLabel, collectionLineage.IterationSectionStartMs, collectionLineage.IterationSectionEndMs = "", "", 0, 0
	collectionLineage.PartID, collectionLineage.PartLabel, collectionLineage.PartKind = "", "", ""
	metadata.collection = pebblestore.SessionArtifactCollection{ID: "collection-1", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Name: "Iterations", Status: pebblestore.SessionArtifactStatusStaging, Lineage: collectionLineage, VariantCount: 1, StagingCount: 1}
	metadata.variant = pebblestore.SessionArtifactVariant{ID: "variant-1", CollectionID: metadata.collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Status: pebblestore.SessionArtifactStatusStaging, Lineage: lineage, Presentation: pebblestore.SessionArtifactPresentation{Label: "Compact"}}
	// The parent reserves the placeholder before this child provider run exists.
	principal.RunID, principal.PlanID, principal.CheckpointID, principal.AttemptID = "child-run-1", "child-plan-1", "child-cp-1", "child-attempt-1"
	created, err := authority.Create(context.Background(), principal, CreateInput{RequestID: "managed-create", CollectionID: metadata.collection.ID, VariantID: metadata.variant.ID, Filename: "compact.html", MediaType: "text/html", Presentation: pebblestore.SessionArtifactPresentation{Kind: "html", Label: "Compact", Previewable: true}, SourceSessionID: source.SourceSessionID, SourceCollectionID: source.SourceCollectionID, SourceVariantID: source.SourceVariantID, SourceEventSeq: source.SourceEventSeq, Body: []byte("<h1>compact</h1>")})
	if err != nil {
		t.Fatalf("finalize placeholder: %v", err)
	}
	if created.Status != pebblestore.SessionArtifactStatusReady || created.Filename != "compact.html" || created.MediaType != "text/html" || created.Lineage != lineage || metadata.readyCalls != 1 {
		t.Fatalf("finalized placeholder = %#v calls=%d", created, metadata.readyCalls)
	}
}

func TestAuthorityRejectsPreallocatedManagedPlaceholderWithDifferentStableLineage(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	principal.TaskCallID, principal.ChildSessionID = "call-1", "child-1"
	principal.IterationGroupID, principal.IterationID, principal.IterationIndex = "group-1", "iteration-1", 1
	lineage := authority.lineage(principal, CreateInput{})
	collectionLineage := lineage
	collectionLineage.SourceSessionID, collectionLineage.ChildSessionID = "", ""
	collectionLineage.IterationID, collectionLineage.IterationIndex = "", 0
	metadata.collection = pebblestore.SessionArtifactCollection{ID: "collection-1", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Name: "Iterations", Status: pebblestore.SessionArtifactStatusStaging, Lineage: collectionLineage, VariantCount: 1, StagingCount: 1}
	lineage.TaskCallID = "other-call"
	metadata.variant = pebblestore.SessionArtifactVariant{ID: "variant-1", CollectionID: metadata.collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Status: pebblestore.SessionArtifactStatusStaging, Lineage: lineage}
	principal.RunID = "child-run-1"
	_, err := authority.Create(context.Background(), principal, CreateInput{RequestID: "managed-create", CollectionID: metadata.collection.ID, VariantID: metadata.variant.ID, Filename: "compact.html", MediaType: "text/html", Body: []byte("<h1>compact</h1>")})
	if err == nil || !strings.Contains(err.Error(), "incompatible status, metadata, or lineage") {
		t.Fatalf("stable lineage mismatch error = %v", err)
	}
}

func TestAuthorityRecordsManagedDesignerLineage(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	principal.TaskCallID, principal.ProgramID, principal.ProgramJobID = "call-1", "program-1", "job-1"
	principal.ChildSessionID, principal.IterationGroupID, principal.IterationGroup = "child-1", "group-1", "navigation"
	principal.IterationID, principal.IterationIndex, principal.IterationLabel, principal.IterationTheme = "iteration-1", 3, "Navigation Remix", "compact"
	created, err := authority.Create(context.Background(), principal, CreateInput{RequestID: "designer-1", CollectionID: "collection-1", CollectionName: "Alternatives", VariantID: "variant-1", Filename: "design.txt", MediaType: "text/plain", Body: []byte("managed")})
	if err != nil {
		t.Fatal(err)
	}
	lineage := created.Lineage
	if lineage.ParentSessionID != "session-1" || lineage.SourceSessionID != "child-1" || lineage.TaskCallID != "call-1" || lineage.ProgramID != "program-1" || lineage.ProgramJobID != "job-1" || lineage.ChildSessionID != "child-1" || lineage.IterationGroupID != "group-1" || lineage.IterationGroup != "navigation" || lineage.IterationID != "iteration-1" || lineage.IterationIndex != 3 || lineage.IterationLabel != "Navigation Remix" || lineage.IterationTheme != "compact" {
		t.Fatalf("designer lineage = %+v", lineage)
	}
	if metadata.collection.Lineage.ParentSessionID != lineage.ParentSessionID || metadata.collection.Lineage.TaskCallID != lineage.TaskCallID || metadata.collection.Lineage.ProgramID != lineage.ProgramID || metadata.collection.Lineage.ChildSessionID != "" || metadata.collection.Lineage.ProgramJobID != "" {
		t.Fatalf("collection lineage = %+v", metadata.collection.Lineage)
	}
	mismatch := principal
	mismatch.ChildSessionID = "child-2"
	if _, err := authority.Create(context.Background(), mismatch, CreateInput{RequestID: "designer-replay", CollectionID: "collection-1", CollectionName: "Alternatives", VariantID: "variant-1", Filename: "design.txt", MediaType: "text/plain", Body: []byte("managed")}); err == nil {
		t.Fatal("ready artifact replay accepted mismatched trusted lineage")
	}
}

func TestAuthorityRejectsMismatchedTrustedOwnership(t *testing.T) {
	authority, _, principal := authorityFixture(t)
	principal.AccountScopeID = "other-account"
	_, err := authority.List(principal, "", 10)
	if err == nil {
		t.Fatal("expected ownership error")
	}
}

func TestAuthorityReferenceRequiresOwnedReadyExactEvent(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	metadata.sourceCollection = pebblestore.SessionArtifactCollection{ID: "source-collection", AccountScopeID: "account-1", SessionID: "source-session", Status: pebblestore.SessionArtifactStatusReady, VariantCount: 1, ReadyCount: 1}
	metadata.sourceVariant = pebblestore.SessionArtifactVariant{ID: "source-variant", CollectionID: "source-collection", AccountScopeID: "account-1", SessionID: "source-session", Status: pebblestore.SessionArtifactStatusReady, EventSeq: 41, MediaType: "text/plain"}
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "source-session", CollectionID: "source-collection", VariantID: "source-variant", EventSeq: 41}
	if got, err := authority.GetReference(principal, ref); err != nil || got.ID != "source-variant" {
		t.Fatalf("get reference = %+v err=%v", got, err)
	}
	metadata.sourceCollection.SelectedVariantID = "source-variant"
	metadata.sourceCollection.EventSeq = 42
	selected := ref
	selected.EventSeq = 42
	if got, err := authority.GetReference(principal, selected); err != nil || got.ID != "source-variant" {
		t.Fatalf("get selected reference = %+v err=%v", got, err)
	}
	stale := ref
	stale.EventSeq = 40
	if _, err := authority.GetReference(principal, stale); err == nil || err.Error() != "artifact source reference is stale" {
		t.Fatalf("stale reference error = %v", err)
	}
	metadata.sourceVariant.Status = pebblestore.SessionArtifactStatusStaging
	if _, err := authority.GetReference(principal, ref); err == nil || err.Error() != "artifact source reference is not ready" {
		t.Fatalf("not-ready reference error = %v", err)
	}
	other := ref
	other.SessionID = "other-session"
	if _, err := authority.GetReference(principal, other); err == nil {
		t.Fatal("expected source session ownership rejection")
	}
	principal.AccountScopeID = "other-account"
	if _, err := authority.GetReference(principal, ref); err == nil {
		t.Fatal("expected source account ownership rejection")
	}
}

func TestAuthorityMaterializeReferenceRequiresOwnedReadyExactEvent(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	metadata.sourceCollection = pebblestore.SessionArtifactCollection{ID: "source-collection", AccountScopeID: "account-1", SessionID: "source-session", Status: pebblestore.SessionArtifactStatusReady, VariantCount: 1, ReadyCount: 1}
	variant := testVariant("source-variant", "source.txt", "text/plain", "text")
	variant.SessionID, variant.CollectionID, variant.Status, variant.EventSeq = "source-session", "source-collection", pebblestore.SessionArtifactStatusStaging, 41
	service, _, err := authority.registry.ServiceForOwnedSession("source-session", "account-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	staged, err := service.Stage(context.Background(), variant, strings.NewReader("selected"))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := service.Finalize(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	variant.Status, variant.DigestSHA256, variant.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
	metadata.sourceVariant = variant
	workspace := t.TempDir()
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "source-session", CollectionID: "source-collection", VariantID: "source-variant", EventSeq: 41}
	if _, err := authority.MaterializeReference(context.Background(), principal, ref, workspace, "selected.txt", false); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "selected.txt")); err != nil || string(data) != "selected" {
		t.Fatalf("materialized reference = %q err=%v", data, err)
	}
	ref.EventSeq = 40
	if _, err := authority.MaterializeReference(context.Background(), principal, ref, workspace, "stale.txt", false); err == nil || err.Error() != "artifact source reference is stale" {
		t.Fatalf("stale materialize reference error = %v", err)
	}
}

func TestAuthorityReadPackageReferenceRequiresOwnedReadyExactEvent(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	metadata.sourceCollection = pebblestore.SessionArtifactCollection{ID: "source-collection", AccountScopeID: "account-1", SessionID: "source-session", Status: pebblestore.SessionArtifactStatusReady, VariantCount: 1, ReadyCount: 1}
	service, _, err := authority.registry.ServiceForOwnedSession("source-session", "account-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	variant := testVariant("source-variant", "design.zip", "application/zip", "package")
	variant.SessionID, variant.CollectionID, variant.Status, variant.EventSeq = "source-session", "source-collection", pebblestore.SessionArtifactStatusStaging, 41
	staged, err := service.StagePackage(context.Background(), variant, []PackageEntry{{Name: "index.html", Data: []byte("<main>selected</main>")}})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := service.Finalize(context.Background(), staged, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	variant.Status, variant.DigestSHA256, variant.Size = pebblestore.SessionArtifactStatusReady, blob.DigestSHA256, blob.Size
	metadata.sourceVariant = variant
	ref := pebblestore.SessionArtifactSelectionReference{SessionID: "source-session", CollectionID: "source-collection", VariantID: "source-variant", EventSeq: 41}
	if manifest, body, _, err := authority.ReadPackageReference(context.Background(), principal, ref, "index.html", 64); err != nil || manifest != nil || string(body) != "<main>selected</main>" {
		t.Fatalf("package reference manifest=%#v body=%q err=%v", manifest, body, err)
	}
	ref.EventSeq = 40
	if _, _, _, err := authority.ReadPackageReference(context.Background(), principal, ref, "index.html", 64); err == nil || err.Error() != "artifact source reference is stale" {
		t.Fatalf("stale package reference error = %v", err)
	}
	principal.AccountScopeID = "other-account"
	ref.EventSeq = 41
	if _, _, _, err := authority.ReadPackageReference(context.Background(), principal, ref, "index.html", 64); err == nil {
		t.Fatal("foreign package reference was accepted")
	}
}

func TestAuthorityDerivedArtifactRecordsAttachedSourceSession(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	metadata.sourceCollection = pebblestore.SessionArtifactCollection{ID: "source-collection", AccountScopeID: "account-1", SessionID: "source-session", Status: pebblestore.SessionArtifactStatusReady, VariantCount: 1, ReadyCount: 1}
	metadata.sourceVariant = pebblestore.SessionArtifactVariant{ID: "source-variant", CollectionID: "source-collection", AccountScopeID: "account-1", SessionID: "source-session", Status: pebblestore.SessionArtifactStatusReady, EventSeq: 41, MediaType: "text/plain"}
	created, err := authority.Create(context.Background(), principal, CreateInput{RequestID: "derived-1", CollectionID: "derived-collection", CollectionName: "Derived", VariantID: "derived-variant", Filename: "derived.txt", MediaType: "text/plain", SourceSessionID: "source-session", SourceCollectionID: "source-collection", SourceVariantID: "source-variant", SourceEventSeq: 41, Body: []byte("revision")})
	if err != nil {
		t.Fatal(err)
	}
	if created.Lineage.SourceSessionID != "source-session" || created.Lineage.SourceCollectionID != "source-collection" || created.Lineage.SourceVariantID != "source-variant" || created.Lineage.SourceEventSeq != 41 {
		t.Fatalf("derived lineage = %+v", created.Lineage)
	}
}

func TestAuthorityCreateFromWorkspaceDirectoryCreatesPackageWithSourceLineage(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	metadata.sourceCollection = pebblestore.SessionArtifactCollection{ID: "collection-source", AccountScopeID: "account-1", SessionID: "session-1", Status: pebblestore.SessionArtifactStatusReady, VariantCount: 1, ReadyCount: 1}
	metadata.sourceVariant = pebblestore.SessionArtifactVariant{ID: "variant-source", CollectionID: "collection-source", AccountScopeID: "account-1", SessionID: "session-1", Status: pebblestore.SessionArtifactStatusReady, EventSeq: 9, MediaType: "text/plain"}
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("<main>revision</main>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "assets", "site.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := authority.CreateFromFile(context.Background(), principal, CreateFileInput{
		CreateInput: CreateInput{
			RequestID: "workspace-package", CollectionID: "collection-workspace", CollectionName: "Workspace", VariantID: "variant-workspace",
			Filename: "workspace.zip", MediaType: "application/zip", Presentation: pebblestore.SessionArtifactPresentation{Kind: "package"},
			SourceSessionID: "session-1", SourceCollectionID: "collection-source", SourceVariantID: "variant-source", SourceEventSeq: 9,
		},
		SourcePath: source,
		Package:    true,
	})
	if err != nil {
		t.Fatalf("CreateFromFile package: %v", err)
	}
	if created.Status != pebblestore.SessionArtifactStatusReady || created.MediaType != "application/zip" || created.Lineage.SourceCollectionID != "collection-source" || created.Lineage.SourceEventSeq != 9 {
		t.Fatalf("workspace package = %#v", created)
	}
}

func TestAuthorityPublishWorkspaceRejectsExistingTrustedVariantID(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	metadata.collection = pebblestore.SessionArtifactCollection{ID: "collection-existing", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID}
	metadata.variant = pebblestore.SessionArtifactVariant{ID: "variant-existing", CollectionID: metadata.collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Status: pebblestore.SessionArtifactStatusReady}
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := authority.PublishWorkspace(context.Background(), principal, CreateFileInput{CreateInput: CreateInput{RequestID: "publish", CollectionID: metadata.collection.ID, VariantID: metadata.variant.ID, Filename: "source.txt", MediaType: "text/plain"}, SourcePath: source})
	if err == nil || !strings.Contains(err.Error(), "variant already exists") {
		t.Fatalf("existing publication error = %v", err)
	}
}

func TestAuthorityCreateFromFileVideo(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	mp4Header := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00isommp42")
	mp4Content := append(mp4Header, []byte("videodata")...)
	sourceFile := filepath.Join(t.TempDir(), "render.mp4")
	if err := os.WriteFile(sourceFile, mp4Content, 0o600); err != nil {
		t.Fatal(err)
	}

	created, err := authority.CreateFromFile(context.Background(), principal, CreateFileInput{
		CreateInput: CreateInput{
			RequestID:      "video-create-1",
			CollectionID:   "video-collection",
			CollectionName: "Rendered Videos",
			VariantID:      "video-variant",
			Filename:       "render.mp4",
			MediaType:      "video/mp4",
			Presentation:   pebblestore.SessionArtifactPresentation{Kind: "video", Label: "Final Render"},
		},
		SourcePath: sourceFile,
	})
	if err != nil {
		t.Fatalf("CreateFromFile: %v", err)
	}
	if created.Status != pebblestore.SessionArtifactStatusReady || created.Filename != "render.mp4" || created.MediaType != "video/mp4" {
		t.Fatalf("created variant = %+v", created)
	}
	if metadata.readyCalls != 1 {
		t.Fatalf("readyCalls = %d, want 1", metadata.readyCalls)
	}
}
