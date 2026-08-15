package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// MetadataStore is the canonical V3 metadata boundary used by Authority.
// Implementations must commit event, projection, idempotency, and realtime
// outbox state atomically, as SessionStore.ApplyV3SessionMutation does.
type MetadataStore interface {
	GetSessionArtifactCollection(accountScopeID, sessionID, collectionID string) (pebblestore.SessionArtifactCollection, bool, error)
	GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error)
	GetSessionArtifactVariantByID(accountScopeID, sessionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error)
	ListSessionArtifactCollections(accountScopeID, sessionID, status string, limit int) ([]pebblestore.SessionArtifactCollection, error)
	ListSessionArtifactVariants(accountScopeID, sessionID, collectionID string, limit int) ([]pebblestore.SessionArtifactVariant, error)
	ApplySessionMutation(pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error)
}

// Principal is trusted run/session context. None of these ownership or lineage
// fields are accepted from artifact content or model-authored metadata.
type Principal struct {
	SessionID        string
	AccountScopeID   string
	UserID           string
	RunID            string
	PlanID           string
	CheckpointID     string
	AttemptID        string
	TaskCallID       string
	ProgramID        string
	ProgramJobID     string
	ChildSessionID   string
	IterationGroupID string
	IterationGroup   string
	IterationID      string
	IterationIndex   int
	IterationLabel   string
	IterationTheme   string
}

type CreateInput struct {
	RequestID             string
	CollectionID          string
	CollectionName        string
	CollectionDescription string
	VariantID             string
	Filename              string
	MediaType             string
	Presentation          pebblestore.SessionArtifactPresentation
	OutputRequirements    *pebblestore.SessionArtifactOutputRequirements
	SourceSessionID       string
	SourceCollectionID    string
	SourceVariantID       string
	SourceEventSeq        uint64
	Body                  []byte
}

type CreatePackageInput struct {
	CreateInput
	Entries []PackageEntry
}

type CreateFileInput struct {
	CreateInput
	SourcePath string
}

type Authority struct {
	registry *Registry
	metadata MetadataStore
	now      func() time.Time
}

func NewAuthority(registry *Registry, metadata MetadataStore) *Authority {
	return &Authority{registry: registry, metadata: metadata, now: time.Now}
}

func (a *Authority) Create(ctx context.Context, principal Principal, input CreateInput) (pebblestore.SessionArtifactVariant, error) {
	return a.create(ctx, principal, input, nil, "")
}

func (a *Authority) CreatePackage(ctx context.Context, principal Principal, input CreatePackageInput) (pebblestore.SessionArtifactVariant, error) {
	entries := make([]PackageEntry, len(input.Entries))
	copy(entries, input.Entries)
	return a.create(ctx, principal, input.CreateInput, entries, "")
}

func (a *Authority) CreateFromFile(ctx context.Context, principal Principal, input CreateFileInput) (pebblestore.SessionArtifactVariant, error) {
	return a.create(ctx, principal, input.CreateInput, nil, strings.TrimSpace(input.SourcePath))
}

func (a *Authority) create(ctx context.Context, principal Principal, input CreateInput, packageEntries []PackageEntry, sourcePath string) (pebblestore.SessionArtifactVariant, error) {
	service, principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	input.SourceSessionID = strings.TrimSpace(input.SourceSessionID)
	input.SourceCollectionID = strings.TrimSpace(input.SourceCollectionID)
	input.SourceVariantID = strings.TrimSpace(input.SourceVariantID)
	if input.SourceSessionID != "" || input.SourceEventSeq != 0 {
		ref := pebblestore.SessionArtifactSelectionReference{SessionID: input.SourceSessionID, CollectionID: input.SourceCollectionID, VariantID: input.SourceVariantID, EventSeq: input.SourceEventSeq}
		if _, err := a.GetReference(principal, ref); err != nil {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("resolve source artifact: %w", err)
		}
	} else if input.SourceCollectionID != "" || input.SourceVariantID != "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("source artifact lineage requires source_session_id and source_event_seq")
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact request id is required")
	}
	lineage := a.lineage(principal, input)
	collectionLineage := lineage
	collectionLineage.SourceSessionID, collectionLineage.SourceCollectionID, collectionLineage.SourceVariantID = "", "", ""
	collectionLineage.ProgramJobID, collectionLineage.ChildSessionID = "", ""
	collectionLineage.IterationID, collectionLineage.IterationIndex, collectionLineage.IterationLabel, collectionLineage.IterationTheme = "", 0, "", ""
	if err := applyArtifactOutputRequirementsToPresentation(&input.Presentation, input.OutputRequirements); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	collection := pebblestore.SessionArtifactCollection{ID: strings.TrimSpace(input.CollectionID), Name: strings.TrimSpace(input.CollectionName), Description: strings.TrimSpace(input.CollectionDescription), Lineage: collectionLineage, Presentation: input.Presentation}
	variant := pebblestore.SessionArtifactVariant{ID: strings.TrimSpace(input.VariantID), CollectionID: collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Filename: strings.TrimSpace(input.Filename), MediaType: strings.TrimSpace(input.MediaType), Presentation: input.Presentation, OutputRequirements: cloneOutputRequirements(input.OutputRequirements), Lineage: lineage}
	existingStaging := false
	if existing, ok, getErr := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID); getErr != nil {
		return pebblestore.SessionArtifactVariant{}, getErr
	} else if ok {
		lineageCompatible := existing.Lineage == (pebblestore.SessionArtifactLineage{}) || artifactDestinationLineageCompatible(existing.Lineage, lineage)
		presentationCompatible := artifactPresentationRequirementsCompatible(existing.Presentation, variant.Presentation, existing.OutputRequirements)
		if existing.OutputRequirements != nil && variant.OutputRequirements == nil {
			if (variant.Presentation.Width != 0 && variant.Presentation.Width != existing.OutputRequirements.Width) || (variant.Presentation.Height != 0 && variant.Presentation.Height != existing.OutputRequirements.Height) {
				return pebblestore.SessionArtifactVariant{}, errors.New("artifact presentation dimensions conflict with output requirements")
			}
			variant.Presentation.Width, variant.Presentation.Height = existing.OutputRequirements.Width, existing.OutputRequirements.Height
			presentationCompatible = artifactPresentationRequirementsCompatible(existing.Presentation, variant.Presentation, existing.OutputRequirements)
		}
		if existing.Status == pebblestore.SessionArtifactStatusReady {
			readyRequirementsCompatible := equalOutputRequirements(existing.OutputRequirements, variant.OutputRequirements)
			if existing.OutputRequirements != nil && variant.OutputRequirements == nil {
				readyRequirementsCompatible = true
			}
			if !lineageCompatible || !readyRequirementsCompatible || !presentationCompatible {
				return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q already exists with incompatible metadata, lineage, requirements, or presentation", variant.ID)
			}
			return existing, nil
		}
		metadataCompatible := (existing.Filename == "" && existing.MediaType == "") || (existing.Filename == variant.Filename && existing.MediaType == variant.MediaType)
		requirementsCompatible := equalOutputRequirements(existing.OutputRequirements, variant.OutputRequirements)
		// Managed preallocation is the durable requirement authority. A caller may
		// omit the trusted snapshot only when finalizing that same staging row.
		if existing.OutputRequirements != nil && variant.OutputRequirements == nil {
			requirementsCompatible = true
		}
		if existing.Status != pebblestore.SessionArtifactStatusStaging || !metadataCompatible || !lineageCompatible || !requirementsCompatible || !presentationCompatible {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q already exists with incompatible status, metadata, or lineage, or with incompatible requirements or presentation", variant.ID)
		}
		storedCollection, collectionOK, collectionErr := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, collection.ID)
		if collectionErr != nil {
			return pebblestore.SessionArtifactVariant{}, collectionErr
		}
		if !collectionOK {
			return pebblestore.SessionArtifactVariant{}, errors.New("artifact staging collection metadata is missing")
		}
		collection, existingStaging = storedCollection, true
		// Keep the caller's filename/media/presentation for byte staging. The
		// preallocated canonical row contributes identity and immutable lineage,
		// then the terminal mutation merges the produced metadata into it.
		variant.Version, variant.AccountScopeID, variant.SessionID, variant.Status = existing.Version, existing.AccountScopeID, existing.SessionID, existing.Status
		variant.CreatedAt, variant.UpdatedAt, variant.EventSeq = existing.CreatedAt, existing.UpdatedAt, existing.EventSeq
		if existing.Lineage == (pebblestore.SessionArtifactLineage{}) {
			variant.Lineage = lineage
		} else {
			// The placeholder is allocated before the child provider run exists, so
			// preserve its stable trusted destination lineage instead of trying to
			// mutate it with run/plan/attempt metadata discovered during execution.
			variant.Lineage = existing.Lineage
		}
		if collection.Lineage == (pebblestore.SessionArtifactLineage{}) {
			collection.Lineage = collectionLineage
		}
		variant.OutputRequirements = cloneOutputRequirements(existing.OutputRequirements)
	}
	if !existingStaging {
		if _, err := a.mutate(principal, input.RequestID+":stage", pebblestore.V3SessionMutationCreateArtifact, collection, &variant, nil); err != nil {
			if replayed, ok, getErr := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID); getErr == nil && ok && replayed.Status == pebblestore.SessionArtifactStatusReady {
				return replayed, nil
			}
			return pebblestore.SessionArtifactVariant{}, err
		}
	}
	// Staging metadata is durable before any byte promotion.
	var staged Staged
	if packageEntries != nil {
		entries := make([]PackageEntry, 0, len(packageEntries))
		for _, entry := range packageEntries {
			entries = append(entries, PackageEntry{Name: entry.Name, Data: append([]byte(nil), entry.Data...)})
		}
		staged, err = service.StagePackage(ctx, variant, entries)
	} else if sourcePath != "" {
		staged, err = service.ImportFile(ctx, variant, sourcePath)
	} else {
		staged, err = service.Stage(ctx, variant, bytes.NewReader(input.Body))
	}
	if err != nil {
		failed, mutationErr := a.recordFailure(principal, input.RequestID, collection, variant, "stage_failed")
		if mutationErr != nil {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("%v; persist artifact failure: %w", err, mutationErr)
		}
		return failed, fmt.Errorf("stage artifact bytes: %w", err)
	}
	blob, err := service.Finalize(ctx, staged, staged.DigestSHA256, staged.Size)
	if err != nil {
		failed, mutationErr := a.recordFailure(principal, input.RequestID, collection, variant, "finalize_failed")
		if mutationErr != nil {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("%v; persist artifact failure: %w", err, mutationErr)
		}
		return failed, fmt.Errorf("finalize artifact bytes: %w", err)
	}
	variant.Filename, variant.MediaType, variant.DigestSHA256, variant.Size, variant.Presentation = blob.Filename, blob.MediaType, blob.DigestSHA256, blob.Size, blob.Presentation
	// Requirements are the trusted target contract. Byte staging/finalization does
	// not inspect binary pixel dimensions, so preserve the exact target metadata.
	variant.Presentation.Width, variant.Presentation.Height = 0, 0
	if err := applyArtifactOutputRequirementsToPresentation(&variant.Presentation, variant.OutputRequirements); err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("preserve finalized artifact output requirements: %w", err)
	}
	collection.Lineage = collectionLineage
	if _, err := a.mutate(principal, input.RequestID+":ready:"+blob.DigestSHA256, pebblestore.V3SessionMutationFinalizeArtifact, collection, &variant, nil); err != nil {
		// Finalized bytes may outlive a failed metadata write, but ready metadata is
		// never published. A retry with the same request and bytes safely converges.
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

func (a *Authority) List(principal Principal, status string, limit int) ([]pebblestore.SessionArtifactCollection, error) {
	_, principal, err := a.owned(principal)
	if err != nil {
		return nil, err
	}
	return a.metadata.ListSessionArtifactCollections(principal.AccountScopeID, principal.SessionID, status, limit)
}

func (a *Authority) ListVariants(principal Principal, collectionID string, limit int) ([]pebblestore.SessionArtifactVariant, error) {
	_, principal, err := a.owned(principal)
	if err != nil {
		return nil, err
	}
	return a.metadata.ListSessionArtifactVariants(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID), limit)
}

func (a *Authority) Get(principal Principal, variantID string) (pebblestore.SessionArtifactVariant, error) {
	_, principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	variant, ok, err := a.metadata.GetSessionArtifactVariantByID(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(variantID))
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q was not found", variantID)
	}
	return variant, nil
}

func (a *Authority) Read(ctx context.Context, principal Principal, variantID string, maxBytes int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	service, principal, err := a.owned(principal)
	if err != nil {
		return nil, pebblestore.SessionArtifactVariant{}, err
	}
	variant, err := a.Get(principal, variantID)
	if err != nil {
		return nil, pebblestore.SessionArtifactVariant{}, err
	}
	data, _, err := service.Read(ctx, variant, maxBytes)
	return data, variant, err
}

// GetReference resolves an attached opaque reference without changing the
// trusted current-run principal. The source session must belong to the same
// authenticated account and user, and the reference must still identify the
// exact ready variant event that was attached to the message.
func (a *Authority) GetReference(principal Principal, ref pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error) {
	if _, _, err := a.owned(principal); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.CollectionID = strings.TrimSpace(ref.CollectionID)
	ref.VariantID = strings.TrimSpace(ref.VariantID)
	if ref.SessionID == "" || ref.CollectionID == "" || ref.VariantID == "" || ref.EventSeq == 0 {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference requires session_id, collection_id, variant_id, and event_seq")
	}
	if _, _, err := a.registry.ServiceForOwnedSession(ref.SessionID, principal.AccountScopeID, principal.UserID); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, ref.SessionID, ref.CollectionID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference was not found")
	}
	if collection.AccountScopeID != principal.AccountScopeID || collection.SessionID != ref.SessionID || collection.ID != ref.CollectionID {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference ownership is inconsistent")
	}
	variant, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, ref.SessionID, ref.CollectionID, ref.VariantID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference was not found")
	}
	if variant.AccountScopeID != principal.AccountScopeID || variant.SessionID != ref.SessionID || variant.CollectionID != ref.CollectionID || variant.ID != ref.VariantID {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference ownership is inconsistent")
	}
	if variant.Status != pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference is not ready")
	}
	readySequence := variant.EventSeq == ref.EventSeq
	selectedSequence := collection.SelectedVariantID == variant.ID && collection.EventSeq == ref.EventSeq
	if !readySequence && !selectedSequence {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference is stale")
	}
	return variant, nil
}

// ReadReference reads bounded bytes through the source session's authenticated
// storage authority. It never exposes or accepts a filesystem path.
func (a *Authority) ReadReference(ctx context.Context, principal Principal, ref pebblestore.SessionArtifactSelectionReference, maxBytes int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	variant, err := a.GetReference(principal, ref)
	if err != nil {
		return nil, pebblestore.SessionArtifactVariant{}, err
	}
	service, _, err := a.registry.ServiceForOwnedSession(ref.SessionID, principal.AccountScopeID, principal.UserID)
	if err != nil {
		return nil, pebblestore.SessionArtifactVariant{}, err
	}
	data, _, err := service.Read(ctx, variant, maxBytes)
	return data, variant, err
}

func (a *Authority) ReadPackageReference(ctx context.Context, principal Principal, ref pebblestore.SessionArtifactSelectionReference, entryName string, maxBytes int64) ([]PackageManifestEntry, []byte, pebblestore.SessionArtifactVariant, error) {
	variant, err := a.GetReference(principal, ref)
	if err != nil {
		return nil, nil, pebblestore.SessionArtifactVariant{}, err
	}
	service, _, err := a.registry.ServiceForOwnedSession(ref.SessionID, principal.AccountScopeID, principal.UserID)
	if err != nil {
		return nil, nil, pebblestore.SessionArtifactVariant{}, err
	}
	manifest, data, _, err := service.ReadPackage(ctx, variant, entryName, maxBytes)
	return manifest, data, variant, err
}

// MaterializeReference verifies an exact authenticated ready reference before
// copying its managed bytes into the trusted current workspace root.
func (a *Authority) MaterializeReference(ctx context.Context, principal Principal, ref pebblestore.SessionArtifactSelectionReference, workspaceRoot, destination string, overwrite bool) (Materialized, error) {
	variant, err := a.GetReference(principal, ref)
	if err != nil {
		return Materialized{}, err
	}
	service, _, err := a.registry.ServiceForOwnedSession(ref.SessionID, principal.AccountScopeID, principal.UserID)
	if err != nil {
		return Materialized{}, err
	}
	return service.Materialize(ctx, variant, workspaceRoot, destination, overwrite)
}

func (a *Authority) Select(principal Principal, requestID, collectionID, variantID string) (pebblestore.SessionArtifactSelectionReference, error) {
	_, principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactSelectionReference{}, err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID))
	if err != nil {
		return pebblestore.SessionArtifactSelectionReference{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactSelectionReference{}, fmt.Errorf("artifact collection %q was not found", collectionID)
	}
	selection := &pebblestore.SessionArtifactSelectionReference{SessionID: principal.SessionID, CollectionID: collection.ID, VariantID: strings.TrimSpace(variantID)}
	result, err := a.mutate(principal, requestID, pebblestore.V3SessionMutationSelectArtifact, collection, nil, selection)
	if err != nil {
		return pebblestore.SessionArtifactSelectionReference{}, err
	}
	if result.Artifact == nil || result.Artifact.Selection == nil {
		return pebblestore.SessionArtifactSelectionReference{}, errors.New("artifact selection was not persisted")
	}
	return *result.Artifact.Selection, nil
}

func (a *Authority) MarkFailed(principal Principal, requestID, collectionID, variantID, failureCode string) (pebblestore.SessionArtifactVariant, error) {
	service, principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID))
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact collection %q was not found", collectionID)
	}
	variant, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, strings.TrimSpace(variantID))
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q was not found", variantID)
	}
	if variant.Status == pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, errors.New("ready artifact variant is immutable")
	}
	if err := service.DeleteVariant(principal.SessionID, collection.ID, variant.ID); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	variant.FailureCode = strings.TrimSpace(failureCode)
	if variant.FailureCode == "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact failure code is required")
	}
	if _, err := a.mutate(principal, requestID, pebblestore.V3SessionMutationFailArtifact, collection, &variant, nil); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	stored, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact failure metadata was not persisted")
	}
	return stored, nil
}

func (a *Authority) DeleteVariant(principal Principal, requestID, collectionID, variantID string) error {
	service, principal, err := a.owned(principal)
	if err != nil {
		return err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	variant, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, strings.TrimSpace(variantID))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if _, err = a.mutate(principal, requestID, pebblestore.V3SessionMutationDeleteArtifactVariant, collection, &variant, nil); err != nil {
		return err
	}
	return service.DeleteVariant(principal.SessionID, collection.ID, variant.ID)
}

func (a *Authority) DeleteCollection(principal Principal, requestID, collectionID string) error {
	service, principal, err := a.owned(principal)
	if err != nil {
		return err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if _, err = a.mutate(principal, requestID, pebblestore.V3SessionMutationDeleteArtifactCollection, collection, nil, nil); err != nil {
		return err
	}
	return service.DeleteCollection(principal.SessionID, collection.ID)
}

func (a *Authority) recordFailure(principal Principal, requestID string, collection pebblestore.SessionArtifactCollection, variant pebblestore.SessionArtifactVariant, code string) (pebblestore.SessionArtifactVariant, error) {
	variant.FailureCode = code
	if _, err := a.mutate(principal, requestID+":failed:"+code, pebblestore.V3SessionMutationFailArtifact, collection, &variant, nil); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	stored, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact failure metadata was not persisted")
	}
	return stored, nil
}

func (a *Authority) owned(principal Principal) (*Service, Principal, error) {
	if a == nil || a.registry == nil || a.metadata == nil {
		return nil, Principal{}, errors.New("artifact authority is not configured")
	}
	principal.SessionID, principal.AccountScopeID, principal.UserID = strings.TrimSpace(principal.SessionID), strings.TrimSpace(principal.AccountScopeID), strings.TrimSpace(principal.UserID)
	if principal.SessionID == "" || principal.AccountScopeID == "" || principal.UserID == "" {
		return nil, Principal{}, errors.New("trusted artifact session ownership is required")
	}
	service, session, err := a.registry.ServiceForOwnedSession(principal.SessionID, principal.AccountScopeID, principal.UserID)
	if err != nil {
		return nil, Principal{}, err
	}
	principal.AccountScopeID, principal.UserID = session.AccountScopeID, session.UserID
	return service, principal, nil
}

func cloneOutputRequirements(input *pebblestore.SessionArtifactOutputRequirements) *pebblestore.SessionArtifactOutputRequirements {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func equalOutputRequirements(left, right *pebblestore.SessionArtifactOutputRequirements) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func applyArtifactOutputRequirementsToPresentation(presentation *pebblestore.SessionArtifactPresentation, requirements *pebblestore.SessionArtifactOutputRequirements) error {
	if requirements == nil {
		return nil
	}
	if presentation == nil {
		return errors.New("artifact presentation is required")
	}
	if (presentation.Width != 0 && presentation.Width != requirements.Width) || (presentation.Height != 0 && presentation.Height != requirements.Height) {
		return errors.New("artifact presentation dimensions conflict with output requirements")
	}
	presentation.Width, presentation.Height = requirements.Width, requirements.Height
	return nil
}

func artifactPresentationRequirementsCompatible(existing, incoming pebblestore.SessionArtifactPresentation, requirements *pebblestore.SessionArtifactOutputRequirements) bool {
	if requirements == nil {
		return true
	}
	return existing.Width == requirements.Width && existing.Height == requirements.Height && incoming.Width == requirements.Width && incoming.Height == requirements.Height
}

func artifactDestinationLineageCompatible(existing, incoming pebblestore.SessionArtifactLineage) bool {
	// A managed destination is reserved before the child provider run is
	// allocated. Execution-attempt identifiers are therefore unavailable on the
	// placeholder and are not part of destination identity. Every parent/task/
	// program/child/iteration/source field remains immutable and must match.
	existing.RunID, existing.PlanID, existing.CheckpointID, existing.AttemptID = "", "", "", ""
	incoming.RunID, incoming.PlanID, incoming.CheckpointID, incoming.AttemptID = "", "", "", ""
	return existing == incoming
}

func (a *Authority) lineage(principal Principal, input CreateInput) pebblestore.SessionArtifactLineage {
	childSessionID := strings.TrimSpace(principal.ChildSessionID)
	sourceSessionID := strings.TrimSpace(input.SourceSessionID)
	if sourceSessionID == "" {
		sourceSessionID = principal.SessionID
		if childSessionID != "" {
			sourceSessionID = childSessionID
		}
	}
	return pebblestore.SessionArtifactLineage{
		ParentSessionID: principal.SessionID, SourceSessionID: sourceSessionID,
		SourceCollectionID: strings.TrimSpace(input.SourceCollectionID), SourceVariantID: strings.TrimSpace(input.SourceVariantID),
		TaskCallID: strings.TrimSpace(principal.TaskCallID), ProgramID: strings.TrimSpace(principal.ProgramID), ProgramJobID: strings.TrimSpace(principal.ProgramJobID),
		ChildSessionID: childSessionID, IterationGroupID: strings.TrimSpace(principal.IterationGroupID), IterationGroup: strings.TrimSpace(principal.IterationGroup),
		IterationID: strings.TrimSpace(principal.IterationID), IterationIndex: principal.IterationIndex, IterationLabel: strings.TrimSpace(principal.IterationLabel), IterationTheme: strings.TrimSpace(principal.IterationTheme),
		RunID: strings.TrimSpace(principal.RunID), PlanID: strings.TrimSpace(principal.PlanID), CheckpointID: strings.TrimSpace(principal.CheckpointID), AttemptID: strings.TrimSpace(principal.AttemptID),
	}
}

func (a *Authority) mutate(principal Principal, requestID, kind string, collection pebblestore.SessionArtifactCollection, variant *pebblestore.SessionArtifactVariant, selection *pebblestore.SessionArtifactSelectionReference) (pebblestore.V3SessionMutationResult, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return pebblestore.V3SessionMutationResult{}, errors.New("artifact request id is required")
	}
	payload := struct {
		Kind       string                                         `json:"kind"`
		Collection pebblestore.SessionArtifactCollection          `json:"collection"`
		Variant    *pebblestore.SessionArtifactVariant            `json:"variant,omitempty"`
		Selection  *pebblestore.SessionArtifactSelectionReference `json:"selection,omitempty"`
	}{kind, collection, variant, selection}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return pebblestore.V3SessionMutationResult{}, err
	}
	hash := sha256.Sum256(encoded)
	payloadHash := hex.EncodeToString(hash[:])
	keyHash := sha256.Sum256([]byte(strings.Join([]string{"managed-artifact", principal.SessionID, requestID, kind}, "\x00")))
	key := "managed-artifact-" + hex.EncodeToString(keyHash[:18])
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	return a.metadata.ApplySessionMutation(pebblestore.V3SessionMutationInput{SessionID: principal.SessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: kind, Artifact: &pebblestore.V3ArtifactMutation{Collection: collection, Variant: variant, Selection: selection}, NowUnixMs: now.UnixMilli()})
}
