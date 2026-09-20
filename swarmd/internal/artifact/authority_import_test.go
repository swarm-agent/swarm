package artifact

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// testStore implements MetadataStore with map storage to support multi-variant,
// multi-collection, and cross-session test setups cleanly.
type testStore struct {
	collections     map[string]pebblestore.SessionArtifactCollection
	variants        map[string]pebblestore.SessionArtifactVariant
	partDefinitions map[string]pebblestore.SessionArtifactPartDefinition
	partRevisions   map[string]pebblestore.SessionArtifactPartRevision
	compositions    map[string]pebblestore.SessionArtifactComposition
	chains          map[string]pebblestore.SessionArtifactChain
}

func newTestStore() *testStore {
	return &testStore{
		collections:     make(map[string]pebblestore.SessionArtifactCollection),
		variants:        make(map[string]pebblestore.SessionArtifactVariant),
		partDefinitions: make(map[string]pebblestore.SessionArtifactPartDefinition),
		partRevisions:   make(map[string]pebblestore.SessionArtifactPartRevision),
		compositions:    make(map[string]pebblestore.SessionArtifactComposition),
		chains:          make(map[string]pebblestore.SessionArtifactChain),
	}
}

func (s *testStore) collKey(sessionID, id string) string {
	return sessionID + "\x00" + id
}

func (s *testStore) varKey(sessionID, collectionID, id string) string {
	return sessionID + "\x00" + collectionID + "\x00" + id
}

func (s *testStore) GetSessionArtifactCollection(_, sessionID, collectionID string) (pebblestore.SessionArtifactCollection, bool, error) {
	c, ok := s.collections[s.collKey(sessionID, collectionID)]
	return c, ok, nil
}

func (s *testStore) GetSessionArtifactVariant(_, sessionID, collectionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error) {
	v, ok := s.variants[s.varKey(sessionID, collectionID, variantID)]
	return v, ok, nil
}

func (s *testStore) GetSessionArtifactVariantByID(_, sessionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error) {
	for _, v := range s.variants {
		if v.SessionID == sessionID && v.ID == variantID {
			return v, true, nil
		}
	}
	return pebblestore.SessionArtifactVariant{}, false, nil
}

func (s *testStore) ListSessionArtifactCollections(_, sessionID, _ string, _ int) ([]pebblestore.SessionArtifactCollection, error) {
	var list []pebblestore.SessionArtifactCollection
	for _, c := range s.collections {
		if c.SessionID == sessionID {
			list = append(list, c)
		}
	}
	return list, nil
}

func (s *testStore) ListSessionArtifactVariants(_, sessionID, collectionID string, _ int) ([]pebblestore.SessionArtifactVariant, error) {
	var list []pebblestore.SessionArtifactVariant
	for _, v := range s.variants {
		if v.SessionID == sessionID && v.CollectionID == collectionID {
			list = append(list, v)
		}
	}
	return list, nil
}

func (s *testStore) SearchSessionArtifactCatalog(_, _ string, _ pebblestore.SessionArtifactCatalogOptions) (pebblestore.SessionArtifactCatalogPage, error) {
	return pebblestore.SessionArtifactCatalogPage{}, nil
}

func (s *testStore) GetSessionArtifactPartDefinition(_, _, owner, chain, part string) (pebblestore.SessionArtifactPartDefinition, bool, error) {
	d, ok := s.partDefinitions[owner+"\x00"+chain+"\x00"+part]
	return d, ok, nil
}

func (s *testStore) GetSessionArtifactPartRevision(_, _, owner, chain, part, revision string) (pebblestore.SessionArtifactPartRevision, bool, error) {
	r, ok := s.partRevisions[owner+"\x00"+chain+"\x00"+part+"\x00"+revision]
	return r, ok, nil
}

func (s *testStore) GetSessionArtifactComposition(_, _, owner, chain, composition string) (pebblestore.SessionArtifactComposition, bool, error) {
	c, ok := s.compositions[owner+"\x00"+chain+"\x00"+composition]
	return c, ok, nil
}

func (s *testStore) GetSessionArtifactChain(_, _, chainID string) (pebblestore.SessionArtifactChain, bool, error) {
	c, ok := s.chains[chainID]
	return c, ok, nil
}

func (s *testStore) ApplySessionMutation(input pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	projection := pebblestore.V3ArtifactProjection{Collection: input.Artifact.Collection, Variant: input.Artifact.Variant, Selection: input.Artifact.Selection}
	switch input.Kind {
	case pebblestore.V3SessionMutationCreateArtifact, pebblestore.V3SessionMutationUpdateArtifact:
		c := input.Artifact.Collection
		c.AccountScopeID, c.SessionID = input.AccountScopeID, input.SessionID
		if c.Status == "" {
			c.Status = pebblestore.SessionArtifactStatusStaging
		}
		s.collections[s.collKey(input.SessionID, c.ID)] = c

		v := *input.Artifact.Variant
		v.AccountScopeID, v.SessionID, v.CollectionID = input.AccountScopeID, input.SessionID, c.ID
		if v.Status == "" {
			v.Status = pebblestore.SessionArtifactStatusStaging
		}
		if v.EventSeq == 0 {
			v.EventSeq = 1
		}
		var defs []pebblestore.SessionArtifactPartDefinition
		for _, def := range input.Artifact.PartDefinitions {
			def.AccountScopeID, def.UserID, def.GraphState = input.AccountScopeID, input.UserID, pebblestore.SessionArtifactGraphAuthoritative
			s.partDefinitions[def.OwnerSessionID+"\x00"+def.ArtifactChainID+"\x00"+def.ID] = def
			defs = append(defs, def)
		}
		for _, rev := range input.Artifact.PartRevisions {
			rev.AccountScopeID, rev.UserID, rev.GraphState = input.AccountScopeID, input.UserID, pebblestore.SessionArtifactGraphAuthoritative
			rev.EventSeq = 1
			s.partRevisions[rev.OwnerSessionID+"\x00"+rev.ArtifactChainID+"\x00"+rev.PartID+"\x00"+rev.ID] = rev
		}
		if input.Artifact.Composition != nil {
			comp := *input.Artifact.Composition
			comp.AccountScopeID, comp.UserID, comp.GraphState = input.AccountScopeID, input.UserID, pebblestore.SessionArtifactGraphAuthoritative
			comp.EventSeq = 1
			s.compositions[comp.OwnerSessionID+"\x00"+comp.ArtifactChainID+"\x00"+comp.ID] = comp
			v.PartGraphState, v.Composition, v.PartDefinitions = pebblestore.SessionArtifactGraphAuthoritative, &comp, defs
			projection.Composition = &comp
		}
		s.variants[s.varKey(input.SessionID, c.ID, v.ID)] = v
		projection.Collection, projection.Variant, projection.PartDefinitions = c, &v, defs

	case pebblestore.V3SessionMutationFinalizeArtifact:
		v := *input.Artifact.Variant
		existingKey := s.varKey(input.SessionID, v.CollectionID, v.ID)
		if existing, ok := s.variants[existingKey]; ok {
			if v.Composition == nil && existing.Composition != nil {
				v.Composition, v.PartDefinitions, v.PartGraphState = existing.Composition, existing.PartDefinitions, existing.PartGraphState
			}
		}
		v.Status = pebblestore.SessionArtifactStatusReady
		if v.EventSeq == 0 {
			v.EventSeq = 2
		}
		s.variants[existingKey] = v

		c := s.collections[s.collKey(input.SessionID, v.CollectionID)]
		c.Status = pebblestore.SessionArtifactStatusReady
		if v.AutoAccept {
			c.SelectedVariantID = v.ID
			ref := pebblestore.SessionArtifactSelectionReference{
				SessionID:    v.SessionID,
				CollectionID: v.CollectionID,
				VariantID:    v.ID,
				EventSeq:     v.EventSeq,
			}
			s.chains[v.ArtifactChainID] = pebblestore.SessionArtifactChain{
				GraphState:        pebblestore.SessionArtifactGraphAuthoritative,
				ID:                v.ArtifactChainID,
				Head:              ref,
				Root:              ref,
				OfficialCommitOID: v.CommitOID,
			}
		}
		s.collections[s.collKey(input.SessionID, c.ID)] = c
		projection.Collection, projection.Variant = c, &v
	}
	return pebblestore.V3SessionMutationResult{Artifact: &projection}, nil
}

func setupImportTest(t *testing.T) (*Authority, *testStore, Principal, Principal) {
	t.Helper()
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := &registryResolver{sessions: []pebblestore.SessionSnapshot{
		{ID: "session-dest", AccountScopeID: "account-1", UserID: "user-1", WorkspacePath: workspace},
		{ID: "session-src", AccountScopeID: "account-1", UserID: "user-1", WorkspacePath: workspace},
		{ID: "session-foreign", AccountScopeID: "account-other", UserID: "user-other", WorkspacePath: workspace},
	}}
	store := newTestStore()
	authority := NewAuthority(NewRegistry(resolver, Limits{}), store)

	destPrincipal := Principal{
		SessionID:      "session-dest",
		AccountScopeID: "account-1",
		UserID:         "user-1",
		RunID:          "run-dest",
		PlanID:         "plan-dest",
		CheckpointID:   "cp-1",
		AttemptID:      "attempt-1",
	}
	srcPrincipal := Principal{
		SessionID:      "session-src",
		AccountScopeID: "account-1",
		UserID:         "user-1",
		RunID:          "run-src",
	}
	return authority, store, destPrincipal, srcPrincipal
}

// Purpose: Verify requirement that importing an exact ready monolithic artifact (HTML, video, text)
// from a retained source session into a destination session creates a destination-owned ready starting
// head preserving exact bytes, filename, media type, presentation, output requirements, and animation profile.
// Threat prevented: Cross-session artifact data corruption, loss of fidelity, and missing starting head state.
// Symbols: Authority.Import, Authority.importMonolithicVariant, Authority.Read, Authority.Create.
// Layer: Internal artifact authority unit test with bare Git repository backend.
func TestAuthorityImportMonolithicFidelityAndEditability(t *testing.T) {
	authority, store, destPrincipal, srcPrincipal := setupImportTest(t)
	ctx := context.Background()

	// 1. Create a source artifact with rich animation profile and output requirements.
	htmlContent := []byte("<!DOCTYPE html><html><body><h1>Reusable Animation</h1></body></html>")
	srcInput := CreateInput{
		RequestID:             "req-src-1",
		CollectionID:          "coll-src",
		CollectionName:        "Source Designs",
		CollectionDescription: "Designs from earlier turn",
		VariantID:             "variant-html-1",
		Filename:              "index.html",
		MediaType:             "text/html",
		Presentation:          pebblestore.SessionArtifactPresentation{Kind: "html", Previewable: true},
		OutputRequirements:    &pebblestore.SessionArtifactOutputRequirements{Width: 1920, Height: 1080, AspectRatio: "16:9", Orientation: "landscape", RegistryVersion: "req-v1"},
		AnimationProfile:      &pebblestore.SessionArtifactAnimationProfile{ProfileID: "motion_ui", RuntimeKind: "native_css_waapi_svg", RegistryVersion: "anim-v1"},
		AutoAccept:            true,
		Body:                  htmlContent,
	}
	srcVariant, err := authority.Create(ctx, srcPrincipal, srcInput)
	if err != nil {
		t.Fatalf("create source variant: %v", err)
	}

	srcRef := pebblestore.SessionArtifactSelectionReference{
		SessionID:    srcVariant.SessionID,
		CollectionID: srcVariant.CollectionID,
		VariantID:    srcVariant.ID,
		EventSeq:     srcVariant.EventSeq,
	}

	// 2. Import into destination session.
	importInput := ImportVariantInput{
		RequestID:             "req-import-1",
		CollectionID:          "imported-designs",
		CollectionName:        "Imported Designs",
		CollectionDescription: "Finalized starting heads",
		VariantID:             "design-head-1",
		Source:                srcRef,
	}
	imported, err := authority.Import(ctx, destPrincipal, importInput)
	if err != nil {
		t.Fatalf("import variant: %v", err)
	}

	// 3. Assert fidelity and destination ownership.
	if imported.SessionID != destPrincipal.SessionID {
		t.Fatalf("imported session = %q, want %q", imported.SessionID, destPrincipal.SessionID)
	}
	if imported.AccountScopeID != destPrincipal.AccountScopeID {
		t.Fatalf("imported account = %q, want %q", imported.AccountScopeID, destPrincipal.AccountScopeID)
	}
	if imported.ID != "design-head-1" || imported.CollectionID != "imported-designs" {
		t.Fatalf("imported id/coll = %q/%q", imported.ID, imported.CollectionID)
	}
	if imported.Status != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("imported status = %q, want ready", imported.Status)
	}
	if imported.Filename != srcVariant.Filename || imported.MediaType != srcVariant.MediaType {
		t.Fatalf("metadata mismatch: %q %q vs %q %q", imported.Filename, imported.MediaType, srcVariant.Filename, srcVariant.MediaType)
	}
	if imported.DigestSHA256 != srcVariant.DigestSHA256 || imported.Size != srcVariant.Size {
		t.Fatalf("digest/size mismatch: %s/%d vs %s/%d", imported.DigestSHA256, imported.Size, srcVariant.DigestSHA256, srcVariant.Size)
	}
	if imported.AnimationProfile == nil || imported.AnimationProfile.ProfileID != "motion_ui" {
		t.Fatalf("animation profile not preserved: %+v", imported.AnimationProfile)
	}
	if imported.OutputRequirements == nil || imported.OutputRequirements.Width != 1920 {
		t.Fatalf("output requirements not preserved: %+v", imported.OutputRequirements)
	}
	if imported.Lineage.SourceSessionID != srcRef.SessionID || imported.Lineage.SourceVariantID != srcRef.VariantID || imported.Lineage.SourceEventSeq != srcRef.EventSeq {
		t.Fatalf("lineage not preserved: %+v", imported.Lineage)
	}
	if imported.Lineage.ParentSessionID != destPrincipal.SessionID {
		t.Fatalf("parent session id = %q, want %q", imported.Lineage.ParentSessionID, destPrincipal.SessionID)
	}

	// 4. Assert read returns exact byte fidelity from destination repo.
	readBytes, readVar, err := authority.Read(ctx, destPrincipal, imported.ID, 1024*1024)
	if err != nil {
		t.Fatalf("read imported variant: %v", err)
	}
	if !bytes.Equal(readBytes, htmlContent) {
		t.Fatalf("read bytes = %q, want %q", readBytes, htmlContent)
	}
	if readVar.ID != imported.ID {
		t.Fatalf("read variant ID = %q, want %q", readVar.ID, imported.ID)
	}

	// 5. Assert continued editing on destination: create revision 2 on top of imported head.
	rev2Content := []byte("<!DOCTYPE html><html><body><h1>Revision 2</h1></body></html>")
	rev2Input := CreateInput{
		RequestID:          "req-dest-rev2",
		CollectionID:       imported.CollectionID,
		VariantID:          "design-rev-2",
		Filename:           "index.html",
		MediaType:          "text/html",
		SourceSessionID:    imported.SessionID,
		SourceCollectionID: imported.CollectionID,
		SourceVariantID:    imported.ID,
		SourceEventSeq:     imported.EventSeq,
		AutoAccept:         true,
		Body:               rev2Content,
	}
	rev2, err := authority.Create(ctx, destPrincipal, rev2Input)
	if err != nil {
		t.Fatalf("create revision 2 on imported head: %v", err)
	}
	if rev2.Status != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("revision 2 status = %q", rev2.Status)
	}
	if rev2.RepositoryID != imported.RepositoryID {
		t.Fatalf("revision 2 repo = %q, want %q", rev2.RepositoryID, imported.RepositoryID)
	}
	if len(rev2.ParentCommitOIDs) == 0 || rev2.ParentCommitOIDs[0] != imported.CommitOID {
		t.Fatalf("revision 2 parent = %v, want %q", rev2.ParentCommitOIDs, imported.CommitOID)
	}

	// 6. Assert source session was not mutated.
	srcCollAfter, _, err := store.GetSessionArtifactCollection("account-1", "session-src", "coll-src")
	if err != nil {
		t.Fatalf("get source coll: %v", err)
	}
	if srcCollAfter.SelectedVariantID != srcVariant.ID {
		t.Fatalf("source selected variant changed: %q", srcCollAfter.SelectedVariantID)
	}
}

// Purpose: Verify requirement that package zip artifacts are imported with complete byte and entry fidelity,
// allowing ReadPackageReference and MaterializeReference in destination without corruption.
// Threat prevented: Truncated or mangled multi-file package archives during cross-session reuse.
// Symbols: Authority.Import, Authority.ReadPackageReference, Authority.MaterializeReference.
// Layer: Internal artifact authority unit test.
func TestAuthorityImportPackageFidelityAndManifest(t *testing.T) {
	authority, _, destPrincipal, srcPrincipal := setupImportTest(t)
	ctx := context.Background()

	// 1. Create a package zip in source session.
	pkgInput := CreatePackageInput{
		CreateInput: CreateInput{
			RequestID:      "req-pkg-src",
			CollectionID:   "coll-pkg",
			VariantID:      "variant-pkg-1",
			Filename:       "widget.zip",
			MediaType:      "application/vnd.swarm.package+zip",
			Presentation:   pebblestore.SessionArtifactPresentation{Kind: "html", Previewable: true},
			AutoAccept:     true,
		},
		Entries: []PackageEntry{
			{Path: "index.html", Body: []byte("<h1>Hello Package</h1>")},
			{Path: "style.css", Body: []byte("h1 { color: red; }")},
		},
	}
	srcPkg, err := authority.CreatePackage(ctx, srcPrincipal, pkgInput)
	if err != nil {
		t.Fatalf("create package: %v", err)
	}

	srcRef := pebblestore.SessionArtifactSelectionReference{
		SessionID:    srcPkg.SessionID,
		CollectionID: srcPkg.CollectionID,
		VariantID:    srcPkg.ID,
		EventSeq:     srcPkg.EventSeq,
	}

	// 2. Import package into destination.
	importedPkg, err := authority.Import(ctx, destPrincipal, ImportVariantInput{
		RequestID:    "req-import-pkg",
		CollectionID: "dest-pkg-coll",
		VariantID:    "dest-pkg-1",
		Source:       srcRef,
	})
	if err != nil {
		t.Fatalf("import package: %v", err)
	}

	destRef := pebblestore.SessionArtifactSelectionReference{
		SessionID:    importedPkg.SessionID,
		CollectionID: importedPkg.CollectionID,
		VariantID:    importedPkg.ID,
		EventSeq:     importedPkg.EventSeq,
	}

	// 3. Read specific package entry from imported variant.
	manifest, fileBytes, _, err := authority.ReadPackageReference(ctx, destPrincipal, destRef, "style.css", 1024*1024)
	if err != nil {
		t.Fatalf("read package reference: %v", err)
	}
	if string(fileBytes) != "h1 { color: red; }" {
		t.Fatalf("package entry content mismatch: %q", string(fileBytes))
	}
	if len(manifest) != 2 {
		t.Fatalf("manifest count = %d, want 2", len(manifest))
	}

	// 4. Materialize imported package to destination workspace.
	wsRoot := filepath.Join(t.TempDir(), "dest-ws")
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	mat, err := authority.MaterializeReference(ctx, destPrincipal, destRef, wsRoot, "extracted", true)
	if err != nil {
		t.Fatalf("materialize reference: %v", err)
	}
	if mat.FileCount != 2 {
		t.Fatalf("materialized file count = %d, want 2", mat.FileCount)
	}
	readExtracted, err := os.ReadFile(filepath.Join(wsRoot, "extracted", "index.html"))
	if err != nil {
		t.Fatalf("read extracted index.html: %v", err)
	}
	if string(readExtracted) != "<h1>Hello Package</h1>" {
		t.Fatalf("extracted index.html = %q", string(readExtracted))
	}
}

// Purpose: Verify requirement that multipart compositions are imported with full graph integrity,
// preserving part definitions, revisions, construction, and lock states, while allowing continued
// refinement via PublishPartReplacement on destination.
// Threat prevented: Dropped part definitions, broken Git trees, or non-editable composed artifacts.
// Symbols: Authority.Import, Authority.importCompositionVariant, Authority.PublishPartReplacement.
// Layer: Internal artifact authority unit test.
func TestAuthorityImportCompositionFidelityAndEditability(t *testing.T) {
	authority, _, destPrincipal, srcPrincipal := setupImportTest(t)
	ctx := context.Background()

	// 1. Create multipart composition in source session.
	chainID := pebblestore.RootSessionArtifactChainID(srcPrincipal.SessionID, "comp-coll", "comp-src-1")
	srcComp, err := authority.CreateInitialComposition(ctx, srcPrincipal, CreateInitialCompositionInput{
		CreateInput: CreateInput{
			RequestID:    "req-src-comp",
			CollectionID: "comp-coll",
			VariantID:    "comp-src-1",
			Filename:     "page.html",
			MediaType:    "text/html",
			Presentation: pebblestore.SessionArtifactPresentation{Kind: "html", Previewable: true},
			AutoAccept:   true,
		},
		ArtifactChainID: chainID,
		CompositionID:   "comp-def-1",
		Construction: pebblestore.SessionArtifactConstruction{
			Kind: "concat-v1",
			Entries: []pebblestore.SessionArtifactConstructionEntry{
				{PartID: "header"},
				{PartID: "body"},
			},
		},
		Parts: []InitialPartInput{
			{
				Definition: pebblestore.SessionArtifactPartDefinition{ID: "header", Label: "Page Header"},
				RevisionID: "rev-header-1",
				MediaType:  "text/html",
				Body:       []byte("<header>Welcome</header>"),
			},
			{
				Definition: pebblestore.SessionArtifactPartDefinition{ID: "body", Label: "Page Body"},
				RevisionID: "rev-body-1",
				MediaType:  "text/html",
				Body:       []byte("<main>Content</main>"),
			},
		},
	})
	if err != nil {
		t.Fatalf("create source composition: %v", err)
	}

	srcRef := pebblestore.SessionArtifactSelectionReference{
		SessionID:    srcComp.SessionID,
		CollectionID: srcComp.CollectionID,
		VariantID:    srcComp.ID,
		EventSeq:     srcComp.EventSeq,
	}

	// 2. Import composition into destination session.
	importedComp, err := authority.Import(ctx, destPrincipal, ImportVariantInput{
		RequestID:    "req-import-comp",
		CollectionID: "dest-comp-coll",
		VariantID:    "dest-comp-head",
		Source:       srcRef,
	})
	if err != nil {
		t.Fatalf("import composition: %v", err)
	}

	if importedComp.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative {
		t.Fatalf("imported part graph state = %q", importedComp.PartGraphState)
	}
	if importedComp.Composition == nil {
		t.Fatal("imported composition is nil")
	}
	if len(importedComp.Composition.Parts) != 2 {
		t.Fatalf("imported parts count = %d, want 2", len(importedComp.Composition.Parts))
	}
	if importedComp.Composition.OwnerSessionID != destPrincipal.SessionID {
		t.Fatalf("composition owner session = %q, want %q", importedComp.Composition.OwnerSessionID, destPrincipal.SessionID)
	}

	// Verify constructed reading.
	assembled, _, err := authority.Read(ctx, destPrincipal, importedComp.ID, 1024*1024)
	if err != nil {
		t.Fatalf("read assembled imported composition: %v", err)
	}
	expectedAssembled := "<header>Welcome</header><main>Content</main>"
	if string(assembled) != expectedAssembled {
		t.Fatalf("assembled bytes = %q, want %q", string(assembled), expectedAssembled)
	}

	// 3. Continue editing: replace "body" part on top of imported composition in destination session.
	destRef := pebblestore.SessionArtifactSelectionReference{
		SessionID:    importedComp.SessionID,
		CollectionID: importedComp.CollectionID,
		VariantID:    importedComp.ID,
		EventSeq:     importedComp.EventSeq,
	}

	replaced, err := authority.PublishPartReplacement(ctx, destPrincipal, PublishPartReplacementInput{
		RequestID:          "req-replace-body",
		CallID:             "call-replace",
		CollectionID:       importedComp.CollectionID,
		VariantID:          "dest-comp-rev2",
		ArtifactStepID:     "step-replace",
		CandidateIndex:     1,
		AutoAccept:         true,
		SourceArtifact:     destRef,
		SourceComposition:  *importedComp.Composition,
		PartDefinition:     importedComp.PartDefinitions[1],
		SourcePartRevision: importedComp.Composition.Parts[1].Revision,
		MediaType:          "text/html",
		Body:               []byte("<main>Updated Content</main>"),
	})
	if err != nil {
		t.Fatalf("publish part replacement on imported composition: %v", err)
	}
	if replaced.Status != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("replaced variant status = %q", replaced.Status)
	}

	// 4. Verify new assembled content contains updated body and untouched header.
	newAssembled, _, err := authority.Read(ctx, destPrincipal, replaced.ID, 1024*1024)
	if err != nil {
		t.Fatalf("read new assembled composition: %v", err)
	}
	expectedNew := "<header>Welcome</header><main>Updated Content</main>"
	if string(newAssembled) != expectedNew {
		t.Fatalf("new assembled bytes = %q, want %q", string(newAssembled), expectedNew)
	}
}

// Purpose: Verify requirement that an unselected swarm/turn candidate can be imported by its exact ready
// reference without selecting it in the source session, while the source selection remains unchanged.
// Threat prevented: Accidental mutation of source selection or inability to import non-head iterations.
// Symbols: Authority.Import, Authority.validateAndFetchSource.
// Layer: Internal artifact authority unit test.
func TestAuthorityImportUnselectedSwarmCandidate(t *testing.T) {
	authority, store, destPrincipal, srcPrincipal := setupImportTest(t)
	ctx := context.Background()

	// Create candidate-1 and select it in collection.
	cand1, err := authority.Create(ctx, srcPrincipal, CreateInput{
		RequestID:    "req-cand-1",
		CollectionID: "swarm-coll",
		VariantID:    "candidate-1",
		Filename:     "variant1.txt",
		MediaType:    "text/plain",
		Body:         []byte("candidate 1 content"),
		AutoAccept:   true,
	})
	if err != nil {
		t.Fatalf("create candidate 1: %v", err)
	}

	// Create candidate-2 without auto-accept (unselected iteration candidate).
	cand2, err := authority.Create(ctx, srcPrincipal, CreateInput{
		RequestID:      "req-cand-2",
		CollectionID:   "swarm-coll",
		VariantID:      "candidate-2",
		Filename:       "variant2.txt",
		MediaType:      "text/plain",
		CandidateIndex: 2,
		AutoAccept:     false,
		Body:           []byte("candidate 2 alternative content"),
	})
	if err != nil {
		t.Fatalf("create candidate 2: %v", err)
	}

	// Verify source collection selected variant is candidate-1, not candidate-2.
	srcColl, _, err := store.GetSessionArtifactCollection("account-1", "session-src", "swarm-coll")
	if err != nil {
		t.Fatalf("get source coll: %v", err)
	}
	if srcColl.SelectedVariantID != "candidate-1" {
		t.Fatalf("source selected = %q, want candidate-1", srcColl.SelectedVariantID)
	}

	// Reference candidate-2 by its own ready EventSeq.
	cand2Ref := pebblestore.SessionArtifactSelectionReference{
		SessionID:    cand2.SessionID,
		CollectionID: cand2.CollectionID,
		VariantID:    cand2.ID,
		EventSeq:     cand2.EventSeq,
	}

	// Import unselected candidate-2 into destination session.
	imported, err := authority.Import(ctx, destPrincipal, ImportVariantInput{
		RequestID:    "req-import-cand2",
		CollectionID: "finalized-coll",
		VariantID:    "finalized-head",
		Source:       cand2Ref,
	})
	if err != nil {
		t.Fatalf("import unselected candidate 2: %v", err)
	}

	if imported.ID != "finalized-head" || imported.Status != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("imported candidate status = %q", imported.Status)
	}

	// Destination collection head is now finalized-head.
	destColl, _, err := store.GetSessionArtifactCollection("account-1", "session-dest", "finalized-coll")
	if err != nil {
		t.Fatalf("get dest coll: %v", err)
	}
	if destColl.SelectedVariantID != "finalized-head" {
		t.Fatalf("dest coll selected = %q, want finalized-head", destColl.SelectedVariantID)
	}

	// Source collection selected variant remains candidate-1.
	srcCollAfter, _, err := store.GetSessionArtifactCollection("account-1", "session-src", "swarm-coll")
	if err != nil {
		t.Fatalf("get source coll after: %v", err)
	}
	if srcCollAfter.SelectedVariantID != "candidate-1" {
		t.Fatalf("source selected variant mutated: %q, want candidate-1", srcCollAfter.SelectedVariantID)
	}

	// Verify imported body matches candidate 2 bytes.
	body, _, err := authority.Read(ctx, destPrincipal, imported.ID, 1024)
	if err != nil {
		t.Fatalf("read imported candidate: %v", err)
	}
	if string(body) != "candidate 2 alternative content" {
		t.Fatalf("imported body = %q", string(body))
	}
}

// Purpose: Verify requirement that invalid, foreign, forged, stale, deleted, missing, or corrupt sources
// are rejected before any visible destination state is mutated (no-partial-state invariant).
// Threat prevented: Destination pollution from invalid import requests, cross-tenant leaks, or corrupt sources.
// Symbols: Authority.Import, Authority.validateAndFetchSource.
// Layer: Internal artifact authority negative unit tests.
func TestAuthorityImportRejectionNoPartialState(t *testing.T) {
	authority, store, destPrincipal, srcPrincipal := setupImportTest(t)
	ctx := context.Background()

	// Set up valid source variant.
	validSrc, err := authority.Create(ctx, srcPrincipal, CreateInput{
		RequestID:    "req-valid-src",
		CollectionID: "coll-valid",
		VariantID:    "var-valid",
		Filename:     "file.txt",
		MediaType:    "text/plain",
		Body:         []byte("hello safe world"),
		AutoAccept:   true,
	})
	if err != nil {
		t.Fatalf("create valid src: %v", err)
	}

	testCases := []struct {
		name      string
		input     ImportVariantInput
		principal Principal
		wantErr   string
	}{
		{
			name: "missing request id",
			input: ImportVariantInput{
				RequestID:    "",
				CollectionID: "dest-coll",
				VariantID:    "v-1",
				Source: pebblestore.SessionArtifactSelectionReference{
					SessionID: validSrc.SessionID, CollectionID: validSrc.CollectionID, VariantID: validSrc.ID, EventSeq: validSrc.EventSeq,
				},
			},
			principal: destPrincipal,
			wantErr:   "request id is required",
		},
		{
			name: "forged / stale event seq",
			input: ImportVariantInput{
				RequestID:    "req-stale",
				CollectionID: "dest-coll",
				VariantID:    "v-stale",
				Source: pebblestore.SessionArtifactSelectionReference{
					SessionID: validSrc.SessionID, CollectionID: validSrc.CollectionID, VariantID: validSrc.ID, EventSeq: 99999,
				},
			},
			principal: destPrincipal,
			wantErr:   "stale",
		},
		{
			name: "foreign cross-account session",
			input: ImportVariantInput{
				RequestID:    "req-foreign",
				CollectionID: "dest-coll",
				VariantID:    "v-foreign",
				Source: pebblestore.SessionArtifactSelectionReference{
					SessionID: "session-foreign", CollectionID: "coll-valid", VariantID: "var-valid", EventSeq: 1,
				},
			},
			principal: destPrincipal,
			wantErr:   "ownership does not match",
		},
		{
			name: "deleted / missing source session",
			input: ImportVariantInput{
				RequestID:    "req-missing-session",
				CollectionID: "dest-coll",
				VariantID:    "v-miss",
				Source: pebblestore.SessionArtifactSelectionReference{
					SessionID: "session-nonexistent", CollectionID: "coll-valid", VariantID: "var-valid", EventSeq: 1,
				},
			},
			principal: destPrincipal,
			wantErr:   "was not found",
		},
		{
			name: "missing source collection",
			input: ImportVariantInput{
				RequestID:    "req-missing-coll",
				CollectionID: "dest-coll",
				VariantID:    "v-miss-coll",
				Source: pebblestore.SessionArtifactSelectionReference{
					SessionID: validSrc.SessionID, CollectionID: "coll-nonexistent", VariantID: validSrc.ID, EventSeq: validSrc.EventSeq,
				},
			},
			principal: destPrincipal,
			wantErr:   "was not found",
		},
		{
			name: "missing source variant",
			input: ImportVariantInput{
				RequestID:    "req-missing-var",
				CollectionID: "dest-coll",
				VariantID:    "v-miss-var",
				Source: pebblestore.SessionArtifactSelectionReference{
					SessionID: validSrc.SessionID, CollectionID: validSrc.CollectionID, VariantID: "var-nonexistent", EventSeq: 1,
				},
			},
			principal: destPrincipal,
			wantErr:   "was not found",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := authority.Import(ctx, tc.principal, tc.input)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}

			// Invariant assertion: No destination variant was created.
			if tc.input.VariantID != "" {
				_, exists, _ := store.GetSessionArtifactVariant("account-1", "session-dest", tc.input.CollectionID, tc.input.VariantID)
				if exists {
					t.Fatalf("partial state leak: destination variant %q exists after failed import", tc.input.VariantID)
				}
			}
		})
	}
}

// Purpose: Verify requirement that repeating an identical import request is an idempotent replay returning
// the existing variant, while a duplicate request targeting an existing variant with conflicting content is rejected.
// Threat prevented: Duplicate genesis commits or corrupting already imported variants.
// Symbols: Authority.Import, Authority.importVariant.
// Layer: Internal artifact authority unit test.
func TestAuthorityImportIdempotencyReplayAndConflict(t *testing.T) {
	authority, _, destPrincipal, srcPrincipal := setupImportTest(t)
	ctx := context.Background()

	// 1. Create source variant A and source variant B.
	varA, err := authority.Create(ctx, srcPrincipal, CreateInput{
		RequestID:    "req-src-a",
		CollectionID: "coll-a",
		VariantID:    "var-a",
		Filename:     "file-a.txt",
		MediaType:    "text/plain",
		Body:         []byte("content a"),
		AutoAccept:   true,
	})
	if err != nil {
		t.Fatalf("create var A: %v", err)
	}

	varB, err := authority.Create(ctx, srcPrincipal, CreateInput{
		RequestID:    "req-src-b",
		CollectionID: "coll-b",
		VariantID:    "var-b",
		Filename:     "file-b.txt",
		MediaType:    "text/plain",
		Body:         []byte("content b"),
		AutoAccept:   true,
	})
	if err != nil {
		t.Fatalf("create var B: %v", err)
	}

	srcRefA := pebblestore.SessionArtifactSelectionReference{
		SessionID:    varA.SessionID,
		CollectionID: varA.CollectionID,
		VariantID:    varA.ID,
		EventSeq:     varA.EventSeq,
	}
	srcRefB := pebblestore.SessionArtifactSelectionReference{
		SessionID:    varB.SessionID,
		CollectionID: varB.CollectionID,
		VariantID:    varB.ID,
		EventSeq:     varB.EventSeq,
	}

	// 2. Initial import of A.
	importAInput := ImportVariantInput{
		RequestID:    "req-import-idempotent",
		CollectionID: "dest-coll",
		VariantID:    "imported-target",
		Source:       srcRefA,
	}
	imported1, err := authority.Import(ctx, destPrincipal, importAInput)
	if err != nil {
		t.Fatalf("initial import: %v", err)
	}

	// 3. Exact replay of identical import of A -> must succeed and return same variant.
	imported2, err := authority.Import(ctx, destPrincipal, importAInput)
	if err != nil {
		t.Fatalf("idempotent replay failed: %v", err)
	}
	if imported2.ID != imported1.ID || imported2.CommitOID != imported1.CommitOID || imported2.DigestSHA256 != imported1.DigestSHA256 {
		t.Fatalf("idempotent replay returned different variant: %+v vs %+v", imported2, imported1)
	}

	// 4. Conflicting import: importing variant B into the same destination variant ID -> must fail.
	importBConflict := ImportVariantInput{
		RequestID:    "req-import-conflict",
		CollectionID: "dest-coll",
		VariantID:    "imported-target",
		Source:       srcRefB,
	}
	_, err = authority.Import(ctx, destPrincipal, importBConflict)
	if err == nil {
		t.Fatal("expected conflict error when importing different source to same variant ID, got nil")
	}
	if !strings.Contains(err.Error(), "conflicts with import request") {
		t.Fatalf("expected conflict error, got: %v", err)
	}
}
