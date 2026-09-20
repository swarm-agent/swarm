package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"swarm/packages/swarmd/internal/artifactgit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ImportVariantInput specifies one authenticated cross-session legacy artifact import.
type ImportVariantInput struct {
	RequestID             string                                        `json:"request_id"`
	CollectionID          string                                        `json:"collection_id,omitempty"`
	CollectionName        string                                        `json:"collection_name,omitempty"`
	CollectionDescription string                                        `json:"collection_description,omitempty"`
	VariantID             string                                        `json:"variant_id,omitempty"`
	Source                pebblestore.SessionArtifactSelectionReference `json:"source,omitempty"`
	SourceSessionID       string                                        `json:"source_session_id,omitempty"`
	SourceCollectionID    string                                        `json:"source_collection_id,omitempty"`
	SourceVariantID       string                                        `json:"source_variant_id,omitempty"`
	SourceEventSeq        uint64                                        `json:"source_event_seq,omitempty"`
}

// SourceReference resolves the canonical selection reference from either embedded or flattened fields.
func (i ImportVariantInput) SourceReference() pebblestore.SessionArtifactSelectionReference {
	if i.Source.SessionID != "" || i.Source.CollectionID != "" || i.Source.VariantID != "" || i.Source.EventSeq != 0 {
		return pebblestore.SessionArtifactSelectionReference{
			SessionID:    strings.TrimSpace(i.Source.SessionID),
			CollectionID: strings.TrimSpace(i.Source.CollectionID),
			VariantID:    strings.TrimSpace(i.Source.VariantID),
			EventSeq:     i.Source.EventSeq,
		}
	}
	return pebblestore.SessionArtifactSelectionReference{
		SessionID:    strings.TrimSpace(i.SourceSessionID),
		CollectionID: strings.TrimSpace(i.SourceCollectionID),
		VariantID:    strings.TrimSpace(i.SourceVariantID),
		EventSeq:     i.SourceEventSeq,
	}
}

// Import imports an exact ready version (head, historical revision, or ready unselected candidate)
// from a retained source session into the authenticated destination session as a distinct,
// destination-owned finalized ready starting head. The source session and artifact remain
// completely immutable.
func (a *Authority) Import(ctx context.Context, principal Principal, input ImportVariantInput) (pebblestore.SessionArtifactVariant, error) {
	return a.importVariant(ctx, principal, input)
}

// ImportVariant is an alias for Import to provide consistent naming across callers.
func (a *Authority) ImportVariant(ctx context.Context, principal Principal, input ImportVariantInput) (pebblestore.SessionArtifactVariant, error) {
	return a.importVariant(ctx, principal, input)
}

// ImportReference imports an exact ready source reference directly into destination.
func (a *Authority) ImportReference(ctx context.Context, principal Principal, ref pebblestore.SessionArtifactSelectionReference, requestID, collectionID, variantID string) (pebblestore.SessionArtifactVariant, error) {
	return a.importVariant(ctx, principal, ImportVariantInput{
		RequestID:    requestID,
		CollectionID: collectionID,
		VariantID:    variantID,
		Source:       ref,
	})
}

type validatedSourcePart struct {
	definition pebblestore.SessionArtifactPartDefinition
	revision   pebblestore.SessionArtifactPartRevision
	bytes      []byte
	locked     bool
}

type validatedSource struct {
	reference   pebblestore.SessionArtifactSelectionReference
	collection  pebblestore.SessionArtifactCollection
	variant     pebblestore.SessionArtifactVariant
	body        []byte
	parts       []validatedSourcePart
	fingerprint string
}

// Source-only lookup: archived retained sessions remain readable without
// reopening them or relaxing the live destination guard in owned.
func (a *Authority) retainedImportOwner(id string, principal Principal) error {
	session, ok, err := a.registry.resolver.GetSession(id)
	if err != nil {
		return err
	}
	if !ok {
		tombstone, found, err := a.registry.resolver.GetSessionTombstone(id)
		if err != nil {
			return err
		}
		if !found || tombstone.Deleted || !tombstone.Archived || tombstone.Session.ID != id || tombstone.Session.AccountScopeID != tombstone.AccountScopeID || tombstone.Session.UserID != tombstone.UserID {
			return errors.New("retained artifact source session was not found")
		}
		session = tombstone.Session
	}
	if session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID {
		return errors.New("artifact source session ownership does not match")
	}
	return nil
}

func (a *Authority) retainedImportRepository(ctx context.Context, id string) (*artifactgit.Repository, error) {
	if a.registry.repositoryErr != nil {
		return nil, a.registry.repositoryErr
	}
	return artifactgit.OpenExisting(ctx, a.registry.repositoryRoot, id, artifactgit.Limits{MaxBlobBytes: a.registry.limits.MaxVideoArtifactBytes, MaxCompositionBytes: a.registry.limits.MaxSessionBytes, MaxParts: pebblestore.SessionArtifactMaxParts})
}

func (a *Authority) validateAndFetchSource(ctx context.Context, ownedPrincipal Principal, ref pebblestore.SessionArtifactSelectionReference) (validatedSource, error) {
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.CollectionID = strings.TrimSpace(ref.CollectionID)
	ref.VariantID = strings.TrimSpace(ref.VariantID)
	if ref.SessionID == "" || ref.CollectionID == "" || ref.VariantID == "" || ref.EventSeq == 0 {
		return validatedSource{}, errors.New("artifact source reference requires session_id, collection_id, variant_id, and event_seq")
	}

	// Authenticate source session ownership: must belong to the same account scope and user.
	if err := a.retainedImportOwner(ref.SessionID, ownedPrincipal); err != nil {
		return validatedSource{}, fmt.Errorf("resolve source artifact session: %w", err)
	}

	// Verify source collection.
	sourceCollection, ok, err := a.metadata.GetSessionArtifactCollection(ownedPrincipal.AccountScopeID, ref.SessionID, ref.CollectionID)
	if err != nil {
		return validatedSource{}, err
	}
	if !ok {
		return validatedSource{}, errors.New("artifact source collection was not found")
	}
	if sourceCollection.AccountScopeID != ownedPrincipal.AccountScopeID || sourceCollection.SessionID != ref.SessionID || sourceCollection.ID != ref.CollectionID {
		return validatedSource{}, errors.New("artifact source collection ownership is inconsistent")
	}

	// Verify source variant.
	sourceVariant, ok, err := a.metadata.GetSessionArtifactVariant(ownedPrincipal.AccountScopeID, ref.SessionID, ref.CollectionID, ref.VariantID)
	if err != nil {
		return validatedSource{}, err
	}
	if !ok {
		return validatedSource{}, errors.New("artifact source variant was not found")
	}
	if sourceVariant.AccountScopeID != ownedPrincipal.AccountScopeID || sourceVariant.SessionID != ref.SessionID || sourceVariant.CollectionID != ref.CollectionID || sourceVariant.ID != ref.VariantID {
		return validatedSource{}, errors.New("artifact source variant ownership is inconsistent")
	}

	// Variant must be ready (reject staging, failed, unavailable).
	if sourceVariant.Status != pebblestore.SessionArtifactStatusReady {
		return validatedSource{}, errors.New("artifact source reference is not ready")
	}

	// Allow ready sequence (including unselected turn/swarm candidate) or collection's selected sequence.
	readySequence := sourceVariant.EventSeq == ref.EventSeq
	selectedSequence := sourceCollection.SelectedVariantID == sourceVariant.ID && sourceCollection.EventSeq == ref.EventSeq
	if !readySequence && !selectedSequence {
		return validatedSource{}, errors.New("artifact source reference is stale")
	}

	res := validatedSource{
		reference:  ref,
		collection: sourceCollection,
		variant:    sourceVariant,
	}

	// Verify content and Git integrity before any destination mutation.
	repo, err := a.retainedImportRepository(ctx, sourceVariant.RepositoryID)
	if err != nil {
		return validatedSource{}, err
	}
	commit, err := repo.ReadCommit(ctx, sourceVariant.CommitOID)
	if err != nil {
		return validatedSource{}, err
	}
	if commit.Tree != sourceVariant.TreeOID || commit.Manifest.MediaType != sourceVariant.MediaType {
		return validatedSource{}, errors.New("source Git projection is inconsistent")
	}
	if sourceVariant.PartGraphState == pebblestore.SessionArtifactGraphAuthoritative && sourceVariant.Composition != nil {
		comp := sourceVariant.Composition
		if strings.TrimSpace(comp.ArtifactChainID) == "" {
			return validatedSource{}, errors.New("source composition is missing artifact chain identity")
		}
		if len(comp.Parts) == 0 || len(comp.Parts) > pebblestore.SessionArtifactMaxParts {
			return validatedSource{}, errors.New("source composition has no parts")
		}

		if err := pebblestore.ValidateArtifactConstruction(*comp); err != nil {
			return validatedSource{}, err
		}
		if comp.OwnerSessionID != ref.SessionID || comp.RepositoryID != sourceVariant.RepositoryID || comp.CommitOID != sourceVariant.CommitOID || comp.TreeOID != sourceVariant.TreeOID {
			return validatedSource{}, errors.New("source composition Git identity is inconsistent")
		}
		if len(commit.Manifest.Parts) != len(comp.Parts) || !reflect.DeepEqual(commit.Manifest.Construction, gitConstruction(comp.Construction)) {
			return validatedSource{}, errors.New("source construction does not match Git")
		}
		var total int64
		parts := make([]validatedSourcePart, 0, len(comp.Parts))
		for _, slot := range comp.Parts {
			partID := strings.TrimSpace(slot.PartID)
			if partID == "" {
				return validatedSource{}, errors.New("source composition part has empty part id")
			}
			for _, owner := range []string{slot.DefinitionOwnerSessionID, slot.Revision.OwnerSessionID} {
				if err := a.retainedImportOwner(owner, ownedPrincipal); err != nil {
					return validatedSource{}, err
				}
			}
			def, defOK, defErr := a.metadata.GetSessionArtifactPartDefinition(ownedPrincipal.AccountScopeID, ownedPrincipal.UserID, slot.DefinitionOwnerSessionID, comp.ArtifactChainID, partID)
			if defErr != nil {
				return validatedSource{}, defErr
			}
			if !defOK || def.GraphState != pebblestore.SessionArtifactGraphAuthoritative {
				return validatedSource{}, errors.New("selected part definition is unavailable or non-Git")
			}

			rev, revOK, revErr := a.metadata.GetSessionArtifactPartRevision(ownedPrincipal.AccountScopeID, ownedPrincipal.UserID, slot.Revision.OwnerSessionID, slot.Revision.ArtifactChainID, slot.Revision.PartID, slot.Revision.PartRevisionID)
			if revErr != nil {
				return validatedSource{}, revErr
			}
			if !revOK || rev.GraphState != pebblestore.SessionArtifactGraphAuthoritative || rev.Reference() != slot.Revision {
				return validatedSource{}, errors.New("selected source part revision is unavailable, mismatched, or stale")
			}

			if rev.Size <= 0 || rev.Size > a.registry.limits.MaxVideoArtifactBytes || rev.Size > a.registry.limits.MaxSessionBytes-total {
				return validatedSource{}, ErrQuotaExceeded
			}
			total += rev.Size
			matched := false
			for _, gitPart := range commit.Manifest.Parts {
				if gitPart.ID == partID && gitPart.Blob == rev.BlobOID && gitPart.Size == rev.Size && gitPart.MediaType == rev.MediaType && gitPart.Locked == slot.Locked {
					matched = true
				}
			}
			if !matched {
				return validatedSource{}, errors.New("source part does not match composition Git tree")
			}
			partRepo, readErr := a.retainedImportRepository(ctx, rev.RepositoryID)
			if readErr != nil {
				return validatedSource{}, readErr
			}
			actualBlob, readErr := partRepo.ReadBlobOID(ctx, rev.CommitOID, rev.PartID)
			if readErr != nil || actualBlob != rev.BlobOID {
				return validatedSource{}, artifactgit.ErrIntegrity
			}
			partBytes, readErr := partRepo.ReadBlob(ctx, rev.CommitOID, rev.PartID)
			if readErr != nil {
				return validatedSource{}, fmt.Errorf("read source artifact part %q: %w", partID, readErr)
			}
			digest := sha256.Sum256(partBytes)
			if hex.EncodeToString(digest[:]) != rev.DigestSHA256 || int64(len(partBytes)) != rev.Size {
				return validatedSource{}, fmt.Errorf("artifact source part %q content is corrupt", partID)
			}

			parts = append(parts, validatedSourcePart{
				definition: def,
				revision:   rev,
				bytes:      partBytes,
				locked:     slot.Locked,
			})
		}
		res.parts = parts
	} else {
		if sourceVariant.RepositoryID == "" || sourceVariant.CommitOID == "" {
			return validatedSource{}, errors.New("source artifact is missing exact Git identity")
		}
		if sourceVariant.Size <= 0 || sourceVariant.Size > a.registry.limits.MaxVideoArtifactBytes {
			return validatedSource{}, ErrQuotaExceeded
		}
		body, readErr := repo.ReadBlob(ctx, sourceVariant.CommitOID, "content")
		if readErr != nil {
			return validatedSource{}, fmt.Errorf("read source artifact: %w", readErr)
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != sourceVariant.DigestSHA256 || int64(len(body)) != sourceVariant.Size {
			return validatedSource{}, errors.New("artifact source content is corrupt")
		}
		res.body = body
	}

	return res, nil
}

func (a *Authority) importVariant(ctx context.Context, principal Principal, input ImportVariantInput) (pebblestore.SessionArtifactVariant, error) {
	ownedPrincipal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}

	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact request id is required")
	}

	ref := input.SourceReference()
	if input.Source.ArtifactID != "" || input.Source.RevisionRef != "" || input.Source.CommitOID != "" || input.Source.ProjectionSeq != 0 || input.Source.PartID != "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("legacy import requires a complete legacy artifact reference")
	}
	if (input.SourceSessionID != "" && input.SourceSessionID != ref.SessionID) || (input.SourceCollectionID != "" && input.SourceCollectionID != ref.CollectionID) || (input.SourceVariantID != "" && input.SourceVariantID != ref.VariantID) || (input.SourceEventSeq != 0 && input.SourceEventSeq != ref.EventSeq) {
		return pebblestore.SessionArtifactVariant{}, errors.New("conflicting source references")
	}

	// Authenticate and fetch source before any destination mutation.
	source, err := a.validateAndFetchSource(ctx, ownedPrincipal, ref)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}

	// Resolve destination collection ID and variant ID.
	destCollectionID := strings.TrimSpace(input.CollectionID)
	if destCollectionID == "" {
		destCollectionID = artifactGitID("import", ownedPrincipal.SessionID, requestID)
	}

	destVariantID := strings.TrimSpace(input.VariantID)
	if destVariantID == "" {
		destVariantID = artifactGitID("imported", ownedPrincipal.SessionID, requestID)
	}
	fingerprintBytes, err := json.Marshal([]any{requestID, ref, destCollectionID, destVariantID, input.CollectionName, input.CollectionDescription})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	fingerprintDigest := sha256.Sum256(fingerprintBytes)
	fingerprint := hex.EncodeToString(fingerprintDigest[:])

	// Check idempotency and destination conflict.
	if existing, exists, getErr := a.metadata.GetSessionArtifactVariant(ownedPrincipal.AccountScopeID, ownedPrincipal.SessionID, destCollectionID, destVariantID); getErr != nil {
		return pebblestore.SessionArtifactVariant{}, getErr
	} else if exists {
		if existing.Status == pebblestore.SessionArtifactStatusReady {
			sameSource := existing.Lineage.SourceSessionID == ref.SessionID &&
				existing.Lineage.SourceCollectionID == ref.CollectionID &&
				existing.Lineage.SourceVariantID == ref.VariantID &&
				existing.Lineage.SourceEventSeq == ref.EventSeq
			if sameSource && existing.ImportFingerprint == fingerprint && existing.ImportRequestID == requestID {
				return existing, nil
			}
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q already exists and conflicts with import request", destVariantID)
		}
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q already exists with non-ready status %q", destVariantID, existing.Status)
	}

	// Prepare destination collection and lineage.
	lineageInput := CreateInput{
		SourceSessionID:    ref.SessionID,
		SourceCollectionID: ref.CollectionID,
		SourceVariantID:    ref.VariantID,
		SourceEventSeq:     ref.EventSeq,
	}
	lineage := a.lineage(ownedPrincipal, lineageInput)

	collectionLineage := lineage
	collectionLineage.SourceSessionID, collectionLineage.SourceCollectionID, collectionLineage.SourceVariantID, collectionLineage.SourceEventSeq = "", "", "", 0
	collectionLineage.ProgramJobID, collectionLineage.ChildSessionID = "", ""
	collectionLineage.IterationID, collectionLineage.IterationIndex, collectionLineage.IterationLabel, collectionLineage.IterationTheme = "", 0, "", ""
	collectionLineage.IterationSectionID, collectionLineage.IterationSectionLabel, collectionLineage.IterationSectionStartMs, collectionLineage.IterationSectionEndMs = "", "", 0, 0
	collectionLineage.PartID, collectionLineage.PartLabel, collectionLineage.PartKind = "", "", ""
	collectionLineage.SelectedReviewTargetIDs = ""
	collectionLineage.VideoProjectID, collectionLineage.VideoRevisionID, collectionLineage.VideoRevisionEventSeq = "", "", 0

	collectionName := strings.TrimSpace(input.CollectionName)
	if collectionName == "" {
		collectionName = source.collection.Name
		if collectionName == "" {
			collectionName = "Imported Artifacts"
		}
	}
	collectionDesc := strings.TrimSpace(input.CollectionDescription)
	if collectionDesc == "" {
		collectionDesc = source.collection.Description
	}

	destCollection := pebblestore.SessionArtifactCollection{
		ID:           destCollectionID,
		Name:         collectionName,
		Description:  collectionDesc,
		Presentation: source.variant.Presentation,
		Lineage:      collectionLineage,
	}

	if storedColl, collOK, collErr := a.metadata.GetSessionArtifactCollection(ownedPrincipal.AccountScopeID, ownedPrincipal.SessionID, destCollectionID); collErr != nil {
		return pebblestore.SessionArtifactVariant{}, collErr
	} else if collOK {
		_ = storedColl
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact import requires an independent destination collection")
	}

	stepDigest := sha256.Sum256([]byte("artifact-step-import-v1\x00" + ownedPrincipal.SessionID + "\x00" + requestID))
	artifactStepID := "artifact-step-" + hex.EncodeToString(stepDigest[:12])

	destChainID := pebblestore.RootSessionArtifactChainID(ownedPrincipal.SessionID, destCollection.ID, destVariantID)
	source.fingerprint = fingerprint

	if source.variant.PartGraphState == pebblestore.SessionArtifactGraphAuthoritative && source.variant.Composition != nil {
		return a.importCompositionVariant(ctx, ownedPrincipal, requestID, destCollection, destVariantID, destChainID, artifactStepID, lineage, source)
	}

	return a.importMonolithicVariant(ctx, ownedPrincipal, requestID, destCollection, destVariantID, destChainID, artifactStepID, lineage, source)
}

func (a *Authority) importMonolithicVariant(ctx context.Context, principal Principal, requestID string, collection pebblestore.SessionArtifactCollection, variantID, chainID, stepID string, lineage pebblestore.SessionArtifactLineage, source validatedSource) (pebblestore.SessionArtifactVariant, error) {
	repo, err := a.repository(ctx, chainID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if err := repo.BindImportRequest(ctx, source.fingerprint); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}

	commitOID, err := repo.Genesis(ctx, artifactgit.Genesis{
		MediaType: source.variant.MediaType,
		Content:   &artifactgit.BlobInput{MediaType: source.variant.MediaType, Bytes: source.body},
	})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("publish imported artifact Git commit: %w", err)
	}

	commit, err := repo.ReadCommit(ctx, commitOID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}

	variant := pebblestore.SessionArtifactVariant{
		ImportRequestID:    requestID,
		ImportFingerprint:  source.fingerprint,
		ID:                 variantID,
		CollectionID:       collection.ID,
		AccountScopeID:     principal.AccountScopeID,
		SessionID:          principal.SessionID,
		Filename:           source.variant.Filename,
		MediaType:          source.variant.MediaType,
		Role:               source.variant.Role,
		Presentation:       source.variant.Presentation,
		OutputRequirements: cloneOutputRequirements(source.variant.OutputRequirements),
		AnimationProfile:   cloneAnimationProfile(source.variant.AnimationProfile),
		Parts:              append([]pebblestore.SessionArtifactPart(nil), source.variant.Parts...),
		Lineage:            lineage,
		ArtifactChainID:    chainID,
		ArtifactStepID:     stepID,
		RevisionRoundID:    stepID,
		RevisionNumber:     1,
		CandidateIndex:     1,
		AutoAccept:         true,
		DigestSHA256:       source.variant.DigestSHA256,
		Size:               source.variant.Size,
		RepositoryID:       chainID,
		CommitOID:          commitOID,
		TreeOID:            commit.Tree,
		GraphState:         pebblestore.SessionArtifactGraphAuthoritative,
	}

	variant.Status = pebblestore.SessionArtifactStatusReady
	if _, err := a.mutate(principal, requestID, pebblestore.V3SessionMutationImportArtifact, collection, &variant, nil); err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("persist finalized artifact metadata: %w", err)
	}

	stored, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok || stored.Status != pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact ready metadata was not persisted")
	}
	return stored, nil
}

func (a *Authority) importCompositionVariant(ctx context.Context, principal Principal, requestID string, collection pebblestore.SessionArtifactCollection, variantID, chainID, stepID string, lineage pebblestore.SessionArtifactLineage, source validatedSource) (pebblestore.SessionArtifactVariant, error) {
	repo, err := a.repository(ctx, chainID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if err := repo.BindImportRequest(ctx, source.fingerprint); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}

	compSeed := sha256.Sum256([]byte("artifact-composition-import-v1\x00" + principal.SessionID + "\x00" + variantID + "\x00" + requestID))
	destCompID := "composition-" + hex.EncodeToString(compSeed[:12])

	destDefs := make([]pebblestore.SessionArtifactPartDefinition, 0, len(source.parts))
	destRevs := make([]pebblestore.SessionArtifactPartRevision, 0, len(source.parts))
	destCompParts := make([]pebblestore.SessionArtifactCompositionPart, 0, len(source.parts))
	gitParts := make(map[string]artifactgit.BlobInput, len(source.parts))
	lockedParts := make(map[string]bool, len(source.parts))

	for idx, part := range source.parts {
		destDef := part.definition
		destDef.ArtifactChainID = chainID
		destDef.OwnerSessionID = principal.SessionID
		destDef.AccountScopeID = principal.AccountScopeID
		destDef.UserID = principal.UserID

		revSeed := sha256.Sum256([]byte(fmt.Sprintf("artifact-part-import-v1\x00%s\x00%s\x00%s\x00%d", principal.SessionID, requestID, part.definition.ID, idx)))
		destRevID := "part-revision-" + hex.EncodeToString(revSeed[:12])

		destRev := pebblestore.SessionArtifactPartRevision{
			ArtifactChainID: chainID,
			PartID:          part.definition.ID,
			ID:              destRevID,
			OwnerSessionID:  principal.SessionID,
			MediaType:       part.revision.MediaType,
			DigestSHA256:    part.revision.DigestSHA256,
			Size:            part.revision.Size,
		}

		destDefs = append(destDefs, destDef)
		destRevs = append(destRevs, destRev)
		destCompParts = append(destCompParts, pebblestore.SessionArtifactCompositionPart{
			PartID:                   part.definition.ID,
			DefinitionOwnerSessionID: principal.SessionID,
			Locked:                   part.locked,
		})
		lockedParts[part.definition.ID] = part.locked
		gitParts[part.definition.ID] = artifactgit.BlobInput{
			MediaType: part.revision.MediaType,
			Bytes:     part.bytes,
		}
	}

	destConstruction := source.variant.Composition.Construction

	commitOID, err := repo.Genesis(ctx, artifactgit.Genesis{
		MediaType:    source.variant.MediaType,
		Parts:        gitParts,
		LockedParts:  lockedParts,
		Construction: gitConstruction(destConstruction),
	})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("publish imported artifact Git composition: %w", err)
	}

	commit, err := repo.ReadCommit(ctx, commitOID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}

	for index := range destRevs {
		blobOID, blobErr := repo.ReadBlobOID(ctx, commitOID, destRevs[index].PartID)
		if blobErr != nil {
			return pebblestore.SessionArtifactVariant{}, blobErr
		}
		destRevs[index].RepositoryID = chainID
		destRevs[index].CommitOID = commitOID
		destRevs[index].BlobOID = blobOID
		destRevs[index].ParentCommitOIDs = append([]string(nil), commit.Parents...)
		for slotIndex := range destCompParts {
			if destCompParts[slotIndex].PartID == destRevs[index].PartID {
				destCompParts[slotIndex].Revision = destRevs[index].Reference()
			}
		}
	}

	destComposition := pebblestore.SessionArtifactComposition{
		ID:               destCompID,
		ArtifactChainID:  chainID,
		OwnerSessionID:   principal.SessionID,
		RepositoryID:     chainID,
		CommitOID:        commitOID,
		TreeOID:          commit.Tree,
		ParentCommitOIDs: append([]string(nil), commit.Parents...),
		Construction:     destConstruction,
		Parts:            destCompParts,
	}

	compBytes, err := json.Marshal(destComposition)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("encode imported artifact composition projection: %w", err)
	}
	compDigest := sha256.Sum256(compBytes)

	variant := pebblestore.SessionArtifactVariant{
		ImportRequestID:    requestID,
		ImportFingerprint:  source.fingerprint,
		ID:                 variantID,
		CollectionID:       collection.ID,
		AccountScopeID:     principal.AccountScopeID,
		SessionID:          principal.SessionID,
		Filename:           source.variant.Filename,
		MediaType:          source.variant.MediaType,
		Role:               source.variant.Role,
		Presentation:       source.variant.Presentation,
		OutputRequirements: cloneOutputRequirements(source.variant.OutputRequirements),
		AnimationProfile:   cloneAnimationProfile(source.variant.AnimationProfile),
		Parts:              append([]pebblestore.SessionArtifactPart(nil), source.variant.Parts...),
		Lineage:            lineage,
		ArtifactChainID:    chainID,
		ArtifactStepID:     stepID,
		RevisionRoundID:    stepID,
		RevisionNumber:     1,
		CandidateIndex:     1,
		AutoAccept:         true,
		DigestSHA256:       hex.EncodeToString(compDigest[:]),
		Size:               int64(len(compBytes)),
		RepositoryID:       chainID,
		CommitOID:          commitOID,
		TreeOID:            commit.Tree,
		GraphState:         pebblestore.SessionArtifactGraphAuthoritative,
		PartDefinitions:    destDefs,
		Composition:        &destComposition,
	}

	variant.Status = pebblestore.SessionArtifactStatusReady
	result, err := a.mutateArtifact(principal, requestID, pebblestore.V3SessionMutationImportArtifact, collection, &variant, nil, destDefs, destRevs, &destComposition)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("persist imported artifact composition: %w", err)
	}
	if result.Artifact == nil || result.Artifact.Variant == nil || result.Artifact.Composition == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("imported artifact composition was not persisted")
	}

	finalized := result
	if finalized.Artifact == nil || finalized.Artifact.Variant == nil || finalized.Artifact.Variant.Status != pebblestore.SessionArtifactStatusReady || finalized.Artifact.Variant.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || finalized.Artifact.Variant.Composition == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("imported artifact composition did not become an authoritative ready variant")
	}

	return *finalized.Artifact.Variant, nil
}
