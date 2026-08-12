package pebblestore

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	SessionArtifactVersion                 = 1
	SessionArtifactMaxCollections          = 64
	SessionArtifactMaxVariantsPerCollection = 128
	SessionArtifactMaxList                 = 256

	SessionArtifactStatusStaging     = "staging"
	SessionArtifactStatusReady       = "ready"
	SessionArtifactStatusFailed      = "failed"
	SessionArtifactStatusUnavailable = "unavailable"
)

// SessionArtifactLineage records durable generation ancestry without granting
// ownership. Account, user, and session ownership always comes from the trusted
// V3 mutation envelope and is not model-authored artifact metadata.
type SessionArtifactLineage struct {
	SourceSessionID    string `json:"source_session_id,omitempty"`
	SourceCollectionID string `json:"source_collection_id,omitempty"`
	SourceVariantID    string `json:"source_variant_id,omitempty"`
	RunID              string `json:"run_id,omitempty"`
	PlanID             string `json:"plan_id,omitempty"`
	CheckpointID       string `json:"checkpoint_id,omitempty"`
	AttemptID          string `json:"attempt_id,omitempty"`
}

// SessionArtifactPresentation contains bounded client display hints. It is
// metadata only and cannot name a filesystem location or embed artifact bytes.
type SessionArtifactPresentation struct {
	Kind        string `json:"kind,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Previewable bool   `json:"previewable,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
}

type SessionArtifactVariant struct {
	Version        int                         `json:"version"`
	ID             string                      `json:"id"`
	CollectionID   string                      `json:"collection_id"`
	AccountScopeID string                      `json:"account_scope_id"`
	SessionID      string                      `json:"session_id"`
	Status         string                      `json:"status"`
	Filename       string                      `json:"filename,omitempty"`
	MediaType      string                      `json:"media_type,omitempty"`
	DigestSHA256   string                      `json:"digest_sha256,omitempty"`
	Size           int64                       `json:"size,omitempty"`
	FailureCode    string                      `json:"failure_code,omitempty"`
	Lineage        SessionArtifactLineage      `json:"lineage,omitempty"`
	Presentation   SessionArtifactPresentation `json:"presentation,omitempty"`
	CreatedAt      int64                       `json:"created_at"`
	UpdatedAt      int64                       `json:"updated_at"`
	EventSeq       uint64                      `json:"event_seq"`
}

type SessionArtifactCollection struct {
	Version           int                         `json:"version"`
	ID                string                      `json:"id"`
	AccountScopeID    string                      `json:"account_scope_id"`
	SessionID         string                      `json:"session_id"`
	Status            string                      `json:"status"`
	Name              string                      `json:"name"`
	Description       string                      `json:"description,omitempty"`
	Presentation      SessionArtifactPresentation `json:"presentation,omitempty"`
	VariantCount      int                         `json:"variant_count"`
	SelectedVariantID string                      `json:"selected_variant_id,omitempty"`
	CreatedAt         int64                       `json:"created_at"`
	UpdatedAt         int64                       `json:"updated_at"`
	EventSeq          uint64                      `json:"event_seq"`
}

// SessionArtifactSelectionReference is the portable, opaque reference placed in
// messages and handoffs. It intentionally contains no bytes or storage path.
type SessionArtifactSelectionReference struct {
	SessionID   string `json:"session_id"`
	CollectionID string `json:"collection_id"`
	VariantID    string `json:"variant_id"`
	EventSeq     uint64 `json:"event_seq,omitempty"`
}

// V3ArtifactMutation is the metadata payload accepted only by artifact V3
// mutation kinds. Embedded ownership is ignored and replaced by the trusted
// mutation envelope before persistence.
type V3ArtifactMutation struct {
	Collection SessionArtifactCollection          `json:"collection"`
	Variant    *SessionArtifactVariant             `json:"variant,omitempty"`
	Selection  *SessionArtifactSelectionReference `json:"selection,omitempty"`
}

// V3ArtifactProjection is safe for session events and realtime delivery. It is
// a metadata snapshot with no body bytes, storage keys, or filesystem paths.
type V3ArtifactProjection struct {
	Collection SessionArtifactCollection          `json:"collection"`
	Variant    *SessionArtifactVariant             `json:"variant,omitempty"`
	Selection  *SessionArtifactSelectionReference `json:"selection,omitempty"`
}

type preparedV3ArtifactMutation struct {
	Projection         V3ArtifactProjection
	PreviousCollection *SessionArtifactCollection
	PreviousVariant    *SessionArtifactVariant
}

func KeySessionArtifactCollection(accountScopeID, sessionID, collectionID string) string {
	return fmt.Sprintf("v3/session_artifact/collections/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(collectionID))
}

func SessionArtifactCollectionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/collections/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionArtifactCollectionStatus(accountScopeID, sessionID, status, collectionID string) string {
	return fmt.Sprintf("v3/session_artifact/collections_by_status/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(status), keyPart(collectionID))
}

func SessionArtifactCollectionStatusPrefix(accountScopeID, sessionID, status string) string {
	return fmt.Sprintf("v3/session_artifact/collections_by_status/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(status))
}

func KeySessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantPrefix(accountScopeID, sessionID, collectionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(collectionID))
}

func KeySessionArtifactVariantStatus(accountScopeID, sessionID, status, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_status/%s/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(status), keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantStatusPrefix(accountScopeID, sessionID, status string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_status/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(status))
}

func KeySessionArtifactVariantDigest(accountScopeID, sessionID, digest, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_digest/%s/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(digest), keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantDigestPrefix(accountScopeID, sessionID, digest string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_digest/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(digest))
}

func (s *SessionStore) GetSessionArtifactCollection(accountScopeID, sessionID, collectionID string) (SessionArtifactCollection, bool, error) {
	if s == nil || s.store == nil { return SessionArtifactCollection{}, false, errors.New("session store is not configured") }
	var collection SessionArtifactCollection
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	collectionID = strings.TrimSpace(collectionID)
	ok, err := s.store.GetJSON(KeySessionArtifactCollection(accountScopeID, sessionID, collectionID), &collection)
	if err != nil || !ok {
		return SessionArtifactCollection{}, ok, err
	}
	if collection.AccountScopeID != accountScopeID || collection.SessionID != sessionID || collection.ID != collectionID {
		return SessionArtifactCollection{}, false, errors.New("artifact collection ownership metadata is inconsistent")
	}
	return collection, true, nil
}

func (s *SessionStore) GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) (SessionArtifactVariant, bool, error) {
	if s == nil || s.store == nil { return SessionArtifactVariant{}, false, errors.New("session store is not configured") }
	var variant SessionArtifactVariant
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	collectionID = strings.TrimSpace(collectionID)
	variantID = strings.TrimSpace(variantID)
	ok, err := s.store.GetJSON(KeySessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID), &variant)
	if err != nil || !ok {
		return SessionArtifactVariant{}, ok, err
	}
	if variant.AccountScopeID != accountScopeID || variant.SessionID != sessionID || variant.CollectionID != collectionID || variant.ID != variantID {
		return SessionArtifactVariant{}, false, errors.New("artifact variant ownership metadata is inconsistent")
	}
	return variant, true, nil
}

func (s *SessionStore) ListSessionArtifactCollections(accountScopeID, sessionID, status string, limit int) ([]SessionArtifactCollection, error) {
	if s == nil || s.store == nil { return nil, errors.New("session store is not configured") }
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	status = strings.TrimSpace(status)
	limit = boundedArtifactListLimit(limit, SessionArtifactMaxCollections)
	prefix := SessionArtifactCollectionPrefix(accountScopeID, sessionID)
	indexed := false
	if status != "" {
		prefix = SessionArtifactCollectionStatusPrefix(accountScopeID, sessionID, status)
		indexed = true
	}
	out := make([]SessionArtifactCollection, 0, limit)
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var collection SessionArtifactCollection
		if indexed {
			collectionID := string(value)
			stored, ok, err := s.GetSessionArtifactCollection(accountScopeID, sessionID, collectionID)
			if err != nil || !ok {
				if err == nil { err = errors.New("artifact collection status index is dangling") }
				return err
			}
			collection = stored
		} else if err := json.Unmarshal(value, &collection); err != nil {
			return err
		}
		out = append(out, collection)
		return nil
	})
	return out, err
}

func (s *SessionStore) ListSessionArtifactVariants(accountScopeID, sessionID, collectionID string, limit int) ([]SessionArtifactVariant, error) {
	if s == nil || s.store == nil { return nil, errors.New("session store is not configured") }
	limit = boundedArtifactListLimit(limit, SessionArtifactMaxVariantsPerCollection)
	out := make([]SessionArtifactVariant, 0, limit)
	err := s.store.IteratePrefix(SessionArtifactVariantPrefix(strings.TrimSpace(accountScopeID), strings.TrimSpace(sessionID), strings.TrimSpace(collectionID)), limit, func(_ string, value []byte) error {
		var variant SessionArtifactVariant
		if err := json.Unmarshal(value, &variant); err != nil { return err }
		out = append(out, variant)
		return nil
	})
	return out, err
}

func boundedArtifactListLimit(limit, maximum int) int {
	if maximum <= 0 || maximum > SessionArtifactMaxList { maximum = SessionArtifactMaxList }
	if limit <= 0 || limit > maximum { return maximum }
	return limit
}

func isV3ArtifactMutationKind(kind string) bool {
	switch kind {
	case V3SessionMutationCreateArtifact, V3SessionMutationUpdateArtifact, V3SessionMutationFinalizeArtifact, V3SessionMutationFailArtifact, V3SessionMutationSelectArtifact:
		return true
	default:
		return false
	}
}

func normalizeV3ArtifactMutation(input *V3SessionMutationInput) {
	if input == nil || input.Artifact == nil { return }
	input.Artifact.Collection.ID = strings.TrimSpace(input.Artifact.Collection.ID)
	input.Artifact.Collection.Name = strings.TrimSpace(input.Artifact.Collection.Name)
	input.Artifact.Collection.Description = strings.TrimSpace(input.Artifact.Collection.Description)
	input.Artifact.Collection.Status = strings.ToLower(strings.TrimSpace(input.Artifact.Collection.Status))
	normalizeArtifactPresentation(&input.Artifact.Collection.Presentation)
	if input.Artifact.Variant != nil {
		variant := input.Artifact.Variant
		variant.ID = strings.TrimSpace(variant.ID)
		variant.CollectionID = strings.TrimSpace(variant.CollectionID)
		variant.Status = strings.ToLower(strings.TrimSpace(variant.Status))
		variant.Filename = strings.TrimSpace(variant.Filename)
		variant.MediaType = strings.ToLower(strings.TrimSpace(variant.MediaType))
		variant.DigestSHA256 = strings.ToLower(strings.TrimSpace(variant.DigestSHA256))
		variant.FailureCode = strings.ToLower(strings.TrimSpace(variant.FailureCode))
		normalizeArtifactLineage(&variant.Lineage)
		normalizeArtifactPresentation(&variant.Presentation)
	}
	if input.Artifact.Selection != nil {
		input.Artifact.Selection.SessionID = strings.TrimSpace(input.Artifact.Selection.SessionID)
		input.Artifact.Selection.CollectionID = strings.TrimSpace(input.Artifact.Selection.CollectionID)
		input.Artifact.Selection.VariantID = strings.TrimSpace(input.Artifact.Selection.VariantID)
	}
}

func normalizeArtifactLineage(lineage *SessionArtifactLineage) {
	lineage.SourceSessionID = strings.TrimSpace(lineage.SourceSessionID)
	lineage.SourceCollectionID = strings.TrimSpace(lineage.SourceCollectionID)
	lineage.SourceVariantID = strings.TrimSpace(lineage.SourceVariantID)
	lineage.RunID = strings.TrimSpace(lineage.RunID)
	lineage.PlanID = strings.TrimSpace(lineage.PlanID)
	lineage.CheckpointID = strings.TrimSpace(lineage.CheckpointID)
	lineage.AttemptID = strings.TrimSpace(lineage.AttemptID)
}

func normalizeArtifactPresentation(presentation *SessionArtifactPresentation) {
	presentation.Kind = strings.ToLower(strings.TrimSpace(presentation.Kind))
	presentation.Label = strings.TrimSpace(presentation.Label)
	presentation.Description = strings.TrimSpace(presentation.Description)
}

func validateV3ArtifactMutation(input V3SessionMutationInput) error {
	if !isV3ArtifactMutationKind(input.Kind) {
		if input.Artifact != nil { return errors.New("artifact payload requires an artifact mutation kind") }
		return nil
	}
	if input.Artifact == nil { return errors.New("artifact mutation payload is required") }
	if len(input.EventPayload) != 0 || input.EventType != "" { return errors.New("artifact event type and payload are derived from canonical metadata") }
	collection := input.Artifact.Collection
	if err := validateArtifactID("collection", collection.ID); err != nil { return err }
	if len(collection.Name) > 256 || len(collection.Description) > 2048 { return errors.New("artifact collection metadata exceeds bounds") }
	if err := validateArtifactPresentation(collection.Presentation); err != nil { return err }
	if variant := input.Artifact.Variant; variant != nil {
		if err := validateArtifactID("variant", variant.ID); err != nil { return err }
		if variant.CollectionID != "" && variant.CollectionID != collection.ID { return errors.New("artifact variant collection id does not match collection") }
		if len(variant.Filename) > 255 || len(variant.MediaType) > 255 || len(variant.FailureCode) > 128 { return errors.New("artifact variant metadata exceeds bounds") }
		if variant.FailureCode != "" {
			if err := validateArtifactID("failure code", variant.FailureCode); err != nil { return err }
		}
		if variant.Filename != "" && (variant.Filename == "." || variant.Filename == ".." || strings.ContainsAny(variant.Filename, `/\\`)) { return errors.New("artifact filename must be a basename") }
		if variant.Size < 0 { return errors.New("artifact variant size must not be negative") }
		if err := validateArtifactLineage(variant.Lineage); err != nil { return err }
		if err := validateArtifactPresentation(variant.Presentation); err != nil { return err }
	}
	switch input.Kind {
	case V3SessionMutationCreateArtifact:
		if collection.Name == "" && input.Artifact.Variant == nil { return errors.New("artifact collection name is required") }
	case V3SessionMutationUpdateArtifact, V3SessionMutationFinalizeArtifact, V3SessionMutationFailArtifact:
		if input.Artifact.Variant == nil { return errors.New("artifact variant is required") }
	case V3SessionMutationSelectArtifact:
		if input.Artifact.Selection == nil || input.Artifact.Selection.CollectionID != collection.ID || input.Artifact.Selection.VariantID == "" {
			return errors.New("artifact selection must identify the collection and variant")
		}
		if input.Artifact.Selection.SessionID != "" && input.Artifact.Selection.SessionID != input.SessionID { return errors.New("artifact selection session does not match mutation session") }
	}
	return nil
}

func validateArtifactID(label, value string) error {
	if value == "" { return fmt.Errorf("artifact %s id is required", label) }
	if len(value) > 128 { return fmt.Errorf("artifact %s id exceeds bounds", label) }
	if value == "." || value == ".." { return fmt.Errorf("artifact %s id contains unsupported characters", label) }
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' { continue }
		return fmt.Errorf("artifact %s id contains unsupported characters", label)
	}
	return nil
}

func validateArtifactLineage(lineage SessionArtifactLineage) error {
	for _, value := range []string{lineage.SourceSessionID, lineage.SourceCollectionID, lineage.SourceVariantID, lineage.RunID, lineage.PlanID, lineage.CheckpointID, lineage.AttemptID} {
		if len(value) > 256 { return errors.New("artifact lineage metadata exceeds bounds") }
	}
	return nil
}

func validateArtifactPresentation(presentation SessionArtifactPresentation) error {
	if len(presentation.Kind) > 64 || len(presentation.Label) > 256 || len(presentation.Description) > 2048 { return errors.New("artifact presentation metadata exceeds bounds") }
	if presentation.Width < 0 || presentation.Height < 0 || presentation.Width > 100000 || presentation.Height > 100000 { return errors.New("artifact presentation dimensions are invalid") }
	return nil
}

func (s *SessionStore) prepareV3ArtifactMutation(input V3SessionMutationInput, seq uint64, now int64) (preparedV3ArtifactMutation, error) {
	if !isV3ArtifactMutationKind(input.Kind) { return preparedV3ArtifactMutation{}, nil }
	storedSession, ok, err := s.GetSession(input.SessionID)
	if err != nil { return preparedV3ArtifactMutation{}, err }
	if !ok || storedSession.AccountScopeID != input.AccountScopeID || storedSession.UserID != input.UserID { return preparedV3ArtifactMutation{}, errors.New("artifact mutation session ownership does not match") }
	incoming := *input.Artifact
	collection, collectionOK, err := s.GetSessionArtifactCollection(input.AccountScopeID, input.SessionID, incoming.Collection.ID)
	if err != nil { return preparedV3ArtifactMutation{}, err }
	prepared := preparedV3ArtifactMutation{}
	if collectionOK { copy := collection; prepared.PreviousCollection = &copy }

	if input.Kind == V3SessionMutationUpdateArtifact && !collectionOK {
		return preparedV3ArtifactMutation{}, fmt.Errorf("artifact collection %q was not found", incoming.Collection.ID)
	}
	if input.Kind == V3SessionMutationCreateArtifact {
		if collectionOK && incoming.Variant == nil { return preparedV3ArtifactMutation{}, fmt.Errorf("artifact collection %q already exists", incoming.Collection.ID) }
		if collectionOK && incoming.Collection.Name != "" && incoming.Collection.Name != collection.Name { return preparedV3ArtifactMutation{}, errors.New("existing artifact collection metadata cannot be replaced by variant creation") }
		if !collectionOK {
			if incoming.Collection.Name == "" { return preparedV3ArtifactMutation{}, errors.New("artifact collection name is required") }
			collections, err := s.ListSessionArtifactCollections(input.AccountScopeID, input.SessionID, "", SessionArtifactMaxCollections+1)
			if err != nil { return preparedV3ArtifactMutation{}, err }
			if len(collections) >= SessionArtifactMaxCollections { return preparedV3ArtifactMutation{}, errors.New("session artifact collection limit exceeded") }
			collection = incoming.Collection
			collection.Version = SessionArtifactVersion
			collection.AccountScopeID = input.AccountScopeID
			collection.SessionID = input.SessionID
			collection.Status = SessionArtifactStatusStaging
			collection.VariantCount = 0
			collection.SelectedVariantID = ""
			collection.CreatedAt = now
		}
	} else if !collectionOK {
		return preparedV3ArtifactMutation{}, fmt.Errorf("artifact collection %q was not found", incoming.Collection.ID)
	}
	collection.UpdatedAt = now
	collection.EventSeq = seq

	var variant *SessionArtifactVariant
	if incoming.Variant != nil {
		current, variantOK, err := s.GetSessionArtifactVariant(input.AccountScopeID, input.SessionID, collection.ID, incoming.Variant.ID)
		if err != nil { return preparedV3ArtifactMutation{}, err }
		if variantOK { copy := current; prepared.PreviousVariant = &copy }
		if variantOK && input.Kind == V3SessionMutationCreateArtifact { return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q already exists", incoming.Variant.ID) }
		next := *incoming.Variant
		if variantOK && (input.Kind == V3SessionMutationFinalizeArtifact || input.Kind == V3SessionMutationFailArtifact) {
			next = mergeTerminalArtifactVariant(current, next)
		}
		next.Version = SessionArtifactVersion
		next.AccountScopeID = input.AccountScopeID
		next.SessionID = input.SessionID
		next.CollectionID = collection.ID
		if variantOK { next.CreatedAt = current.CreatedAt } else { next.CreatedAt = now; collection.VariantCount++ }
		if !variantOK && collection.VariantCount > SessionArtifactMaxVariantsPerCollection { return preparedV3ArtifactMutation{}, errors.New("artifact collection variant limit exceeded") }
		switch input.Kind {
		case V3SessionMutationCreateArtifact, V3SessionMutationUpdateArtifact:
			if variantOK && current.Status == SessionArtifactStatusReady { return preparedV3ArtifactMutation{}, errors.New("finalized artifact variant is immutable") }
			next.Status = SessionArtifactStatusStaging
			next.DigestSHA256 = ""
			next.Size = 0
			next.FailureCode = ""
		case V3SessionMutationFinalizeArtifact:
			if !variantOK { return preparedV3ArtifactMutation{}, errors.New("artifact variant must be staged before finalization") }
			if current.Status == SessionArtifactStatusReady { return preparedV3ArtifactMutation{}, errors.New("finalized artifact variant is immutable") }
			if !validArtifactDigest(next.DigestSHA256) || next.Size <= 0 || next.MediaType == "" || next.Filename == "" { return preparedV3ArtifactMutation{}, errors.New("finalized artifact requires filename, media type, digest, and positive size") }
			next.Status = SessionArtifactStatusReady
			next.FailureCode = ""
			collection.Status = SessionArtifactStatusReady
		case V3SessionMutationFailArtifact:
			if !variantOK { return preparedV3ArtifactMutation{}, errors.New("artifact variant must be staged before failure") }
			if current.Status == SessionArtifactStatusReady { return preparedV3ArtifactMutation{}, errors.New("finalized artifact variant is immutable") }
			if next.FailureCode == "" { return preparedV3ArtifactMutation{}, errors.New("failed artifact variant requires a failure code") }
			next.Status = SessionArtifactStatusFailed
			next.DigestSHA256 = ""
			next.Size = 0
			collection.Status = SessionArtifactStatusFailed
		}
		next.UpdatedAt = now
		next.EventSeq = seq
		variant = &next
	}

	var selection *SessionArtifactSelectionReference
	if input.Kind == V3SessionMutationSelectArtifact {
		selected, ok, err := s.GetSessionArtifactVariant(input.AccountScopeID, input.SessionID, collection.ID, incoming.Selection.VariantID)
		if err != nil { return preparedV3ArtifactMutation{}, err }
		if !ok || selected.Status != SessionArtifactStatusReady { return preparedV3ArtifactMutation{}, errors.New("only a ready artifact variant can be selected") }
		collection.SelectedVariantID = selected.ID
		collection.Status = SessionArtifactStatusReady
		ref := SessionArtifactSelectionReference{SessionID: input.SessionID, CollectionID: collection.ID, VariantID: selected.ID, EventSeq: seq}
		selection = &ref
	}
	prepared.Projection = V3ArtifactProjection{Collection: collection, Variant: variant, Selection: selection}
	return prepared, nil
}

func mergeTerminalArtifactVariant(current, incoming SessionArtifactVariant) SessionArtifactVariant {
	next := current
	if incoming.Filename != "" { next.Filename = incoming.Filename }
	if incoming.MediaType != "" { next.MediaType = incoming.MediaType }
	if incoming.DigestSHA256 != "" { next.DigestSHA256 = incoming.DigestSHA256 }
	if incoming.Size != 0 { next.Size = incoming.Size }
	if incoming.FailureCode != "" { next.FailureCode = incoming.FailureCode }
	if incoming.Lineage != (SessionArtifactLineage{}) { next.Lineage = incoming.Lineage }
	if incoming.Presentation != (SessionArtifactPresentation{}) { next.Presentation = incoming.Presentation }
	return next
}

func validArtifactDigest(value string) bool {
	if len(value) != 64 { return false }
	_, err := hex.DecodeString(value)
	return err == nil
}

func setV3ArtifactMutationInBatch(batch *pebble.Batch, prepared preparedV3ArtifactMutation) error {
	collection := prepared.Projection.Collection
	if collection.ID == "" { return nil }
	if prepared.PreviousCollection != nil && prepared.PreviousCollection.Status != collection.Status {
		if err := batch.Delete([]byte(KeySessionArtifactCollectionStatus(collection.AccountScopeID, collection.SessionID, prepared.PreviousCollection.Status, collection.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) { return err }
	}
	collectionPayload, err := json.Marshal(collection)
	if err != nil { return fmt.Errorf("marshal artifact collection: %w", err) }
	if err := batch.Set([]byte(KeySessionArtifactCollection(collection.AccountScopeID, collection.SessionID, collection.ID)), collectionPayload, nil); err != nil { return err }
	if err := batch.Set([]byte(KeySessionArtifactCollectionStatus(collection.AccountScopeID, collection.SessionID, collection.Status, collection.ID)), []byte(collection.ID), nil); err != nil { return err }
	if variant := prepared.Projection.Variant; variant != nil {
		if previous := prepared.PreviousVariant; previous != nil {
			if previous.Status != variant.Status {
				if err := batch.Delete([]byte(KeySessionArtifactVariantStatus(variant.AccountScopeID, variant.SessionID, previous.Status, variant.CollectionID, variant.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) { return err }
			}
			if previous.DigestSHA256 != "" && previous.DigestSHA256 != variant.DigestSHA256 {
				if err := batch.Delete([]byte(KeySessionArtifactVariantDigest(variant.AccountScopeID, variant.SessionID, previous.DigestSHA256, variant.CollectionID, variant.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) { return err }
			}
		}
		variantPayload, err := json.Marshal(variant)
		if err != nil { return fmt.Errorf("marshal artifact variant: %w", err) }
		if err := batch.Set([]byte(KeySessionArtifactVariant(variant.AccountScopeID, variant.SessionID, variant.CollectionID, variant.ID)), variantPayload, nil); err != nil { return err }
		if err := batch.Set([]byte(KeySessionArtifactVariantStatus(variant.AccountScopeID, variant.SessionID, variant.Status, variant.CollectionID, variant.ID)), []byte(variant.ID), nil); err != nil { return err }
		if variant.DigestSHA256 != "" {
			if err := batch.Set([]byte(KeySessionArtifactVariantDigest(variant.AccountScopeID, variant.SessionID, variant.DigestSHA256, variant.CollectionID, variant.ID)), []byte(variant.ID), nil); err != nil { return err }
		}
	}
	return nil
}
