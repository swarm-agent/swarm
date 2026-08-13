package pebblestore

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"

	"swarm/packages/swarmd/internal/privacy"
)

const (
	SessionArtifactVersion                 = 1
	SessionArtifactMaxCollections          = 64
	SessionArtifactMaxVariantsPerCollection = 128
	SessionArtifactMaxList                 = 256
	SessionArtifactMaxMessageSelections    = 16

	SessionArtifactStatusStaging     = "staging"
	SessionArtifactStatusReady       = "ready"
	SessionArtifactStatusFailed      = "failed"
	SessionArtifactStatusUnavailable = "unavailable"
)

// SessionArtifactLineage records durable generation ancestry without granting
// ownership. Account, user, and session ownership always comes from the trusted
// V3 mutation envelope and is not model-authored artifact metadata.
type SessionArtifactLineage struct {
	// ParentSessionID is the trusted destination session that owns the
	// collection. SourceSessionID identifies the producing child when output is
	// routed into a parent-owned managed collection.
	ParentSessionID    string `json:"parent_session_id,omitempty"`
	SourceSessionID    string `json:"source_session_id,omitempty"`
	SourceCollectionID string `json:"source_collection_id,omitempty"`
	SourceVariantID    string `json:"source_variant_id,omitempty"`
	TaskCallID         string `json:"task_call_id,omitempty"`
	ProgramID          string `json:"program_id,omitempty"`
	ProgramJobID       string `json:"program_job_id,omitempty"`
	ChildSessionID     string `json:"child_session_id,omitempty"`
	IterationID        string `json:"iteration_id,omitempty"`
	IterationIndex     int    `json:"iteration_index,omitempty"`
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
	Lineage           SessionArtifactLineage      `json:"lineage,omitempty"`
	Presentation      SessionArtifactPresentation `json:"presentation,omitempty"`
	VariantCount      int                         `json:"variant_count"`
	StagingCount      int                         `json:"staging_count"`
	ReadyCount        int                         `json:"ready_count"`
	FailedCount       int                         `json:"failed_count"`
	UnavailableCount  int                         `json:"unavailable_count"`
	SelectedVariantID string                      `json:"selected_variant_id,omitempty"`
	CreatedAt         int64                       `json:"created_at"`
	UpdatedAt         int64                       `json:"updated_at"`
	EventSeq          uint64                      `json:"event_seq"`
}

// SessionArtifactSelectionReference is the portable, opaque reference placed in
// messages and handoffs. Label and description are bounded display metadata; the
// reference intentionally contains no bytes, digest, or storage path.
type SessionArtifactSelectionReference struct {
	SessionID    string `json:"session_id"`
	CollectionID string `json:"collection_id"`
	VariantID    string `json:"variant_id"`
	EventSeq     uint64 `json:"event_seq,omitempty"`
	Label        string `json:"label,omitempty"`
	Description  string `json:"description,omitempty"`
	Action       string `json:"action,omitempty"`
}

// ValidateSessionArtifactMessageSelections resolves portable message references
// against authoritative artifact metadata. The returned values are normalized,
// bounded display metadata only; managed bytes and private storage paths never
// enter the message envelope.
func (s *SessionStore) ValidateSessionArtifactMessageSelections(accountScopeID, userID string, selections []SessionArtifactSelectionReference) ([]SessionArtifactSelectionReference, error) {
	if len(selections) == 0 {
		return nil, nil
	}
	if len(selections) > SessionArtifactMaxMessageSelections {
		return nil, errors.New("message artifact selection count limit exceeded")
	}
	accountScopeID, userID = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID)
	if accountScopeID == "" || userID == "" {
		return nil, errors.New("artifact selection ownership is required")
	}
	out := make([]SessionArtifactSelectionReference, 0, len(selections))
	seen := make(map[string]struct{}, len(selections))
	for index, incoming := range selections {
		ref := SessionArtifactSelectionReference{
			SessionID: strings.TrimSpace(incoming.SessionID), CollectionID: strings.TrimSpace(incoming.CollectionID),
			VariantID: strings.TrimSpace(incoming.VariantID), EventSeq: incoming.EventSeq,
			Label: strings.TrimSpace(incoming.Label), Description: strings.TrimSpace(incoming.Description),
			Action: strings.ToLower(strings.TrimSpace(incoming.Action)),
		}
		if len(ref.SessionID) > 256 || ref.SessionID == "" || ref.SessionID == "." || ref.SessionID == ".." || strings.ContainsAny(ref.SessionID, `/\\`) { return nil, fmt.Errorf("artifact selection %d session id is invalid", index) }
		if err := validateArtifactID("selection collection", ref.CollectionID); err != nil { return nil, fmt.Errorf("artifact selection %d: %w", index, err) }
		if err := validateArtifactID("selection variant", ref.VariantID); err != nil { return nil, fmt.Errorf("artifact selection %d: %w", index, err) }
		if ref.EventSeq == 0 { return nil, fmt.Errorf("artifact selection %d event sequence is required", index) }
		if len(ref.Label) > 256 || len(ref.Description) > 2048 { return nil, fmt.Errorf("artifact selection %d display metadata exceeds bounds", index) }
		ref.Label = strings.TrimSpace(privacy.SanitizeText(ref.Label))
		ref.Description = strings.TrimSpace(privacy.SanitizeText(ref.Description))
		if ref.Action == "" { ref.Action = "use" }
		if ref.Action != "select" && ref.Action != "use" { return nil, fmt.Errorf("artifact selection %d action must be select or use", index) }
		key := strings.Join([]string{ref.SessionID, ref.CollectionID, ref.VariantID}, "\x00")
		if _, duplicate := seen[key]; duplicate { return nil, fmt.Errorf("artifact selection %d is duplicated", index) }
		seen[key] = struct{}{}
		session, ok, err := s.GetSession(ref.SessionID)
		if err != nil { return nil, err }
		if !ok || session.AccountScopeID != accountScopeID || session.UserID != userID { return nil, fmt.Errorf("artifact selection %d source session is not owned by the principal", index) }
		collection, ok, err := s.GetSessionArtifactCollection(accountScopeID, ref.SessionID, ref.CollectionID)
		if err != nil { return nil, err }
		if !ok { return nil, fmt.Errorf("artifact selection %d collection was not found", index) }
		variant, ok, err := s.GetSessionArtifactVariant(accountScopeID, ref.SessionID, ref.CollectionID, ref.VariantID)
		if err != nil { return nil, err }
		if !ok { return nil, fmt.Errorf("artifact selection %d variant was not found", index) }
		if variant.Status != SessionArtifactStatusReady { return nil, fmt.Errorf("artifact selection %d variant is not ready", index) }
		readySequence := variant.EventSeq == ref.EventSeq
		selectedSequence := collection.SelectedVariantID == variant.ID && collection.EventSeq == ref.EventSeq
		if !readySequence && !selectedSequence { return nil, fmt.Errorf("artifact selection %d event sequence is stale", index) }
		if ref.Label == "" { ref.Label = firstNonEmptyArtifactString(variant.Presentation.Label, collection.Name, variant.Filename) }
		if ref.Description == "" { ref.Description = firstNonEmptyArtifactString(variant.Presentation.Description, collection.Description) }
		ref.Label = strings.TrimSpace(privacy.SanitizeText(ref.Label))
		ref.Description = strings.TrimSpace(privacy.SanitizeText(ref.Description))
		out = append(out, ref)
	}
	return out, nil
}

func firstNonEmptyArtifactString(values ...string) string {
	for _, value := range values { if value = strings.TrimSpace(value); value != "" { return value } }
	return ""
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
	DeletedVariants    []SessionArtifactVariant
	DeleteVariant      bool
	DeleteCollection   bool
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

func SessionArtifactCollectionStatusSessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/collections_by_status/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantPrefix(accountScopeID, sessionID, collectionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(collectionID))
}

func SessionArtifactVariantSessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionArtifactVariantStatus(accountScopeID, sessionID, status, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_status/%s/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(status), keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantStatusPrefix(accountScopeID, sessionID, status string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_status/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(status))
}

func SessionArtifactVariantStatusSessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_status/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionArtifactVariantDigest(accountScopeID, sessionID, digest, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_digest/%s/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(digest), keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantDigestPrefix(accountScopeID, sessionID, digest string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_digest/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(digest))
}

func SessionArtifactVariantDigestSessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_digest/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionArtifactVariantLineage(accountScopeID, sessionID, dimension, value, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_lineage/%s/%s/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(dimension), value, keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantLineagePrefix(accountScopeID, sessionID, dimension, value string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_lineage/%s/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(dimension), value)
}

func SessionArtifactVariantLineageSessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_lineage/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func artifactLineageIndexValue(value string) string {
	return hex.EncodeToString([]byte(strings.TrimSpace(value)))
}

func artifactVariantLineageIndexKeys(variant SessionArtifactVariant) []string {
	lineage := variant.Lineage
	values := []struct{ dimension, value string }{
		{"parent_session", lineage.ParentSessionID},
		{"task_call", lineage.TaskCallID},
		{"program", lineage.ProgramID},
		{"program_job", lineage.ProgramJobID},
		{"child_session", lineage.ChildSessionID},
		{"iteration", lineage.IterationID},
	}
	keys := make([]string, 0, len(values))
	for _, item := range values {
		if strings.TrimSpace(item.value) == "" { continue }
		keys = append(keys, KeySessionArtifactVariantLineage(variant.AccountScopeID, variant.SessionID, item.dimension, artifactLineageIndexValue(item.value), variant.CollectionID, variant.ID))
	}
	return keys
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
	if collection.Lineage.ParentSessionID != "" && collection.Lineage.ParentSessionID != sessionID {
		return SessionArtifactCollection{}, false, errors.New("artifact collection parent lineage is inconsistent")
	}
	if err := validateArtifactCollectionProgress(collection); err != nil { return SessionArtifactCollection{}, false, err }
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
	if variant.Lineage.ParentSessionID != "" && variant.Lineage.ParentSessionID != sessionID {
		return SessionArtifactVariant{}, false, errors.New("artifact variant parent lineage is inconsistent")
	}
	if variant.Lineage.ChildSessionID != "" && variant.Lineage.SourceSessionID != variant.Lineage.ChildSessionID && (variant.Lineage.SourceCollectionID == "" || variant.Lineage.SourceVariantID == "") {
		return SessionArtifactVariant{}, false, errors.New("artifact variant child lineage is inconsistent")
	}
	return variant, true, nil
}

// GetSessionArtifactVariantByID resolves an opaque session-scoped variant id
// without exposing or requiring any storage location.
func (s *SessionStore) GetSessionArtifactVariantByID(accountScopeID, sessionID, variantID string) (SessionArtifactVariant, bool, error) {
	if s == nil || s.store == nil { return SessionArtifactVariant{}, false, errors.New("session store is not configured") }
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	variantID = strings.TrimSpace(variantID)
	if variantID == "" { return SessionArtifactVariant{}, false, nil }
	collections, err := s.ListSessionArtifactCollections(accountScopeID, sessionID, "", SessionArtifactMaxCollections)
	if err != nil { return SessionArtifactVariant{}, false, err }
	var found SessionArtifactVariant
	matches := 0
	for _, collection := range collections {
		variant, ok, err := s.GetSessionArtifactVariant(accountScopeID, sessionID, collection.ID, variantID)
		if err != nil { return SessionArtifactVariant{}, false, err }
		if ok { found, matches = variant, matches+1 }
	}
	if matches > 1 { return SessionArtifactVariant{}, false, errors.New("artifact variant id is ambiguous in session") }
	return found, matches == 1, nil
}

// ListSessionArtifactCollections returns bounded metadata from one trusted
// account/session scope. An optional status uses the repaired status index.
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
		if collection.AccountScopeID != accountScopeID || collection.SessionID != sessionID { return errors.New("artifact collection ownership metadata is inconsistent") }
		if collection.Lineage.ParentSessionID != "" && collection.Lineage.ParentSessionID != sessionID { return errors.New("artifact collection parent lineage is inconsistent") }
		if err := validateArtifactCollectionProgress(collection); err != nil { return err }
		if status != "" && collection.Status != status { return errors.New("artifact collection status index is inconsistent") }
		out = append(out, collection)
		return nil
	})
	return out, err
}

// ListSessionArtifactVariantsByLineage resolves a bounded native catalog view
// without scanning transcripts or workspace folders.
func (s *SessionStore) ListSessionArtifactVariantsByLineage(accountScopeID, sessionID, dimension, value string, limit int) ([]SessionArtifactVariant, error) {
	if s == nil || s.store == nil { return nil, errors.New("session store is not configured") }
	accountScopeID, sessionID = strings.TrimSpace(accountScopeID), strings.TrimSpace(sessionID)
	dimension, value = strings.ToLower(strings.TrimSpace(dimension)), strings.TrimSpace(value)
	allowed := dimension == "parent_session" || dimension == "task_call" || dimension == "program" || dimension == "program_job" || dimension == "child_session" || dimension == "iteration"
	if !allowed || value == "" { return nil, errors.New("artifact lineage filter is invalid") }
	limit = boundedArtifactListLimit(limit, SessionArtifactMaxList)
	out := make([]SessionArtifactVariant, 0, limit)
	err := s.store.IteratePrefix(SessionArtifactVariantLineagePrefix(accountScopeID, sessionID, dimension, artifactLineageIndexValue(value)), limit, func(_ string, indexed []byte) error {
		parts := strings.SplitN(string(indexed), "\x00", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" { return errors.New("artifact lineage index is malformed") }
		variant, ok, err := s.GetSessionArtifactVariant(accountScopeID, sessionID, parts[0], parts[1])
		if err != nil || !ok {
			if err == nil { err = errors.New("artifact lineage index is dangling") }
			return err
		}
		out = append(out, variant)
		return nil
	})
	return out, err
}

// ListSessionArtifactVariants returns bounded metadata for one collection.
func (s *SessionStore) ListSessionArtifactVariants(accountScopeID, sessionID, collectionID string, limit int) ([]SessionArtifactVariant, error) {
	if s == nil || s.store == nil { return nil, errors.New("session store is not configured") }
	limit = boundedArtifactListLimit(limit, SessionArtifactMaxVariantsPerCollection)
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	collectionID = strings.TrimSpace(collectionID)
	out := make([]SessionArtifactVariant, 0, limit)
	err := s.store.IteratePrefix(SessionArtifactVariantPrefix(accountScopeID, sessionID, collectionID), limit, func(_ string, value []byte) error {
		var variant SessionArtifactVariant
		if err := json.Unmarshal(value, &variant); err != nil { return err }
		if variant.AccountScopeID != accountScopeID || variant.SessionID != sessionID || variant.CollectionID != collectionID { return errors.New("artifact variant ownership metadata is inconsistent") }
		if variant.Lineage.ParentSessionID != "" && variant.Lineage.ParentSessionID != sessionID { return errors.New("artifact variant parent lineage is inconsistent") }
		if variant.Lineage.ChildSessionID != "" && variant.Lineage.SourceSessionID != variant.Lineage.ChildSessionID && (variant.Lineage.SourceCollectionID == "" || variant.Lineage.SourceVariantID == "") { return errors.New("artifact variant child lineage is inconsistent") }
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
	case V3SessionMutationCreateArtifact, V3SessionMutationUpdateArtifact, V3SessionMutationFinalizeArtifact, V3SessionMutationFailArtifact, V3SessionMutationUnavailableArtifact, V3SessionMutationSelectArtifact, V3SessionMutationDeleteArtifactVariant, V3SessionMutationDeleteArtifactCollection:
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
	normalizeArtifactLineage(&input.Artifact.Collection.Lineage)
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
		input.Artifact.Selection.Label = strings.TrimSpace(input.Artifact.Selection.Label)
		input.Artifact.Selection.Description = strings.TrimSpace(input.Artifact.Selection.Description)
		input.Artifact.Selection.Action = strings.ToLower(strings.TrimSpace(input.Artifact.Selection.Action))
	}
}

func normalizeArtifactLineage(lineage *SessionArtifactLineage) {
	lineage.ParentSessionID = strings.TrimSpace(lineage.ParentSessionID)
	lineage.SourceSessionID = strings.TrimSpace(lineage.SourceSessionID)
	lineage.SourceCollectionID = strings.TrimSpace(lineage.SourceCollectionID)
	lineage.SourceVariantID = strings.TrimSpace(lineage.SourceVariantID)
	lineage.TaskCallID = strings.TrimSpace(lineage.TaskCallID)
	lineage.ProgramID = strings.TrimSpace(lineage.ProgramID)
	lineage.ProgramJobID = strings.TrimSpace(lineage.ProgramJobID)
	lineage.ChildSessionID = strings.TrimSpace(lineage.ChildSessionID)
	lineage.IterationID = strings.TrimSpace(lineage.IterationID)
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
	if err := validateArtifactLineage(collection.Lineage); err != nil { return err }
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
	case V3SessionMutationUpdateArtifact, V3SessionMutationFinalizeArtifact, V3SessionMutationFailArtifact, V3SessionMutationUnavailableArtifact, V3SessionMutationDeleteArtifactVariant:
		if input.Artifact.Variant == nil { return errors.New("artifact variant is required") }
	case V3SessionMutationSelectArtifact:
		if input.Artifact.Selection == nil || input.Artifact.Selection.CollectionID != collection.ID || input.Artifact.Selection.VariantID == "" {
			return errors.New("artifact selection must identify the collection and variant")
		}
		if input.Artifact.Selection.SessionID != "" && input.Artifact.Selection.SessionID != input.SessionID { return errors.New("artifact selection session does not match mutation session") }
		if input.Artifact.Selection.Action != "" && input.Artifact.Selection.Action != "select" && input.Artifact.Selection.Action != "use" { return errors.New("artifact selection action must be select or use") }
		if len(input.Artifact.Selection.Label) > 256 || len(input.Artifact.Selection.Description) > 2048 { return errors.New("artifact selection display metadata exceeds bounds") }
	case V3SessionMutationDeleteArtifactCollection:
		if input.Artifact.Variant != nil || input.Artifact.Selection != nil { return errors.New("artifact collection deletion accepts only a collection id") }
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
	for _, value := range []string{lineage.ParentSessionID, lineage.SourceSessionID, lineage.SourceCollectionID, lineage.SourceVariantID, lineage.TaskCallID, lineage.ProgramID, lineage.ProgramJobID, lineage.ChildSessionID, lineage.IterationID, lineage.RunID, lineage.PlanID, lineage.CheckpointID, lineage.AttemptID} {
		if len(value) > 256 { return errors.New("artifact lineage metadata exceeds bounds") }
	}
	if lineage.IterationIndex < 0 || lineage.IterationIndex > 1_000_000 {
		return errors.New("artifact iteration index is invalid")
	}
	if lineage.ChildSessionID != "" && lineage.SourceSessionID != lineage.ChildSessionID && (lineage.SourceCollectionID == "" || lineage.SourceVariantID == "") {
		return errors.New("artifact child lineage requires the child as source session unless authenticated source artifact lineage is present")
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
	if input.Kind == V3SessionMutationDeleteArtifactCollection {
		if !collectionOK { return preparedV3ArtifactMutation{}, fmt.Errorf("artifact collection %q was not found", incoming.Collection.ID) }
		variants, err := s.ListSessionArtifactVariants(input.AccountScopeID, input.SessionID, collection.ID, SessionArtifactMaxVariantsPerCollection)
		if err != nil { return preparedV3ArtifactMutation{}, err }
		if len(variants) != collection.VariantCount { return preparedV3ArtifactMutation{}, errors.New("artifact collection variant count is inconsistent") }
		deletedCollection := collection
		deletedCollection.UpdatedAt = now
		deletedCollection.EventSeq = seq
		prepared.Projection = V3ArtifactProjection{Collection: deletedCollection}
		prepared.DeletedVariants = variants
		prepared.DeleteCollection = true
		return prepared, nil
	}
	if input.Kind == V3SessionMutationCreateArtifact {
		if collectionOK && incoming.Variant == nil { return preparedV3ArtifactMutation{}, fmt.Errorf("artifact collection %q already exists", incoming.Collection.ID) }
		if collectionOK && incoming.Collection.Name != "" && incoming.Collection.Name != collection.Name { return preparedV3ArtifactMutation{}, errors.New("existing artifact collection metadata cannot be replaced by variant creation") }
		if collectionOK && incoming.Collection.Lineage != (SessionArtifactLineage{}) && collection.Lineage != (SessionArtifactLineage{}) && !artifactCollectionLineageCompatible(collection.Lineage, incoming.Collection.Lineage) { return preparedV3ArtifactMutation{}, errors.New("existing artifact collection lineage cannot be replaced by variant creation") }
		if collectionOK && collection.Lineage == (SessionArtifactLineage{}) && incoming.Collection.Lineage != (SessionArtifactLineage{}) {
			collection.Lineage = incoming.Collection.Lineage
		}
		if collectionOK && incoming.Variant != nil {
			if existing, duplicate, err := s.GetSessionArtifactVariantByID(input.AccountScopeID, input.SessionID, incoming.Variant.ID); err != nil { return preparedV3ArtifactMutation{}, err } else if duplicate && existing.CollectionID != collection.ID { return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q already exists in session", incoming.Variant.ID) }
		}
		if !collectionOK {
			if incoming.Collection.Name == "" { return preparedV3ArtifactMutation{}, errors.New("artifact collection name is required") }
			if incoming.Variant != nil {
				if _, duplicate, err := s.GetSessionArtifactVariantByID(input.AccountScopeID, input.SessionID, incoming.Variant.ID); err != nil { return preparedV3ArtifactMutation{}, err } else if duplicate { return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q already exists in session", incoming.Variant.ID) }
			}
			collections, err := s.ListSessionArtifactCollections(input.AccountScopeID, input.SessionID, "", SessionArtifactMaxCollections+1)
			if err != nil { return preparedV3ArtifactMutation{}, err }
			if len(collections) >= SessionArtifactMaxCollections { return preparedV3ArtifactMutation{}, errors.New("session artifact collection limit exceeded") }
			collection = incoming.Collection
			collection.Version = SessionArtifactVersion
			collection.AccountScopeID = input.AccountScopeID
			collection.SessionID = input.SessionID
			collection.Status = SessionArtifactStatusStaging
			collection.VariantCount = 0
			collection.StagingCount = 0
			collection.ReadyCount = 0
			collection.FailedCount = 0
			collection.UnavailableCount = 0
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
		if variantOK && incoming.Variant.Lineage != (SessionArtifactLineage{}) && current.Lineage != (SessionArtifactLineage{}) && incoming.Variant.Lineage != current.Lineage {
			return preparedV3ArtifactMutation{}, errors.New("artifact variant lineage is immutable")
		}
		if variantOK && current.Lineage == (SessionArtifactLineage{}) && incoming.Variant.Lineage != (SessionArtifactLineage{}) {
			current.Lineage = incoming.Variant.Lineage
		}
		if input.Kind == V3SessionMutationDeleteArtifactVariant {
			if !variantOK { return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q was not found", incoming.Variant.ID) }
			if collection.VariantCount <= 0 { return preparedV3ArtifactMutation{}, errors.New("artifact collection variant count is inconsistent") }
			if collection.SelectedVariantID != "" && collection.SelectedVariantID != current.ID {
				selected, ok, err := s.GetSessionArtifactVariant(input.AccountScopeID, input.SessionID, collection.ID, collection.SelectedVariantID)
				if err != nil { return preparedV3ArtifactMutation{}, err }
				if !ok || selected.Status != SessionArtifactStatusReady { return preparedV3ArtifactMutation{}, errors.New("artifact collection selection metadata is inconsistent") }
			}
			collection.VariantCount--
			if err := adjustArtifactCollectionStatusCount(&collection, current.Status, -1); err != nil { return preparedV3ArtifactMutation{}, err }
			if collection.SelectedVariantID == current.ID { collection.SelectedVariantID = "" }
			collection.Status = artifactCollectionStatusFromCounts(collection)
			collection.UpdatedAt = now
			collection.EventSeq = seq
			deleted := current
			if err := validateArtifactCollectionProgress(collection); err != nil { return preparedV3ArtifactMutation{}, err }
			prepared.Projection = V3ArtifactProjection{Collection: collection, Variant: &deleted}
			prepared.DeletedVariants = []SessionArtifactVariant{current}
			prepared.DeleteVariant = true
			return prepared, nil
		}
		if variantOK && input.Kind == V3SessionMutationCreateArtifact { return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q already exists", incoming.Variant.ID) }
		if !variantOK && input.Kind != V3SessionMutationCreateArtifact { return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q was not found", incoming.Variant.ID) }
		next := *incoming.Variant
		if variantOK && (input.Kind == V3SessionMutationFinalizeArtifact || input.Kind == V3SessionMutationFailArtifact || input.Kind == V3SessionMutationUnavailableArtifact) {
			next = mergeTerminalArtifactVariant(current, next)
		}
		next.Version = SessionArtifactVersion
		next.AccountScopeID = input.AccountScopeID
		next.SessionID = input.SessionID
		next.CollectionID = collection.ID
		if variantOK {
			next.CreatedAt = current.CreatedAt
		} else {
			next.CreatedAt = now
			collection.VariantCount++
			collection.StagingCount++
		}
		if !variantOK && collection.VariantCount > SessionArtifactMaxVariantsPerCollection { return preparedV3ArtifactMutation{}, errors.New("artifact collection variant limit exceeded") }
		switch input.Kind {
		case V3SessionMutationCreateArtifact, V3SessionMutationUpdateArtifact:
			if variantOK && current.Status == SessionArtifactStatusReady { return preparedV3ArtifactMutation{}, errors.New("finalized artifact variant is immutable") }
			next.Status = SessionArtifactStatusStaging
			if variantOK && current.Status != next.Status {
				if err := adjustArtifactCollectionStatusCount(&collection, current.Status, -1); err != nil { return preparedV3ArtifactMutation{}, err }
				if err := adjustArtifactCollectionStatusCount(&collection, next.Status, 1); err != nil { return preparedV3ArtifactMutation{}, err }
				collection.Status = artifactCollectionStatusFromCounts(collection)
			}
			next.DigestSHA256 = ""
			next.Size = 0
			next.FailureCode = ""
		case V3SessionMutationFinalizeArtifact:
			if current.Status == SessionArtifactStatusReady { return preparedV3ArtifactMutation{}, errors.New("finalized artifact variant is immutable") }
			if !validArtifactDigest(next.DigestSHA256) || next.Size <= 0 || next.MediaType == "" || next.Filename == "" { return preparedV3ArtifactMutation{}, errors.New("finalized artifact requires filename, media type, digest, and positive size") }
			next.Status = SessionArtifactStatusReady
			next.FailureCode = ""
			if err := adjustArtifactCollectionStatusCount(&collection, current.Status, -1); err != nil { return preparedV3ArtifactMutation{}, err }
			if err := adjustArtifactCollectionStatusCount(&collection, next.Status, 1); err != nil { return preparedV3ArtifactMutation{}, err }
			collection.Status = artifactCollectionStatusFromCounts(collection)
		case V3SessionMutationFailArtifact, V3SessionMutationUnavailableArtifact:
			if current.Status == SessionArtifactStatusReady { return preparedV3ArtifactMutation{}, errors.New("finalized artifact variant is immutable") }
			if next.FailureCode == "" { return preparedV3ArtifactMutation{}, errors.New("failed artifact variant requires a failure code") }
			if input.Kind == V3SessionMutationUnavailableArtifact {
				next.Status = SessionArtifactStatusUnavailable
			} else {
				next.Status = SessionArtifactStatusFailed
			}
			if err := adjustArtifactCollectionStatusCount(&collection, current.Status, -1); err != nil { return preparedV3ArtifactMutation{}, err }
			if err := adjustArtifactCollectionStatusCount(&collection, next.Status, 1); err != nil { return preparedV3ArtifactMutation{}, err }
			collection.Status = artifactCollectionStatusFromCounts(collection)
			next.DigestSHA256 = ""
			next.Size = 0
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
		if incoming.Selection.EventSeq != 0 && incoming.Selection.EventSeq != selected.EventSeq { return preparedV3ArtifactMutation{}, errors.New("artifact selection event sequence is stale") }
		collection.SelectedVariantID = selected.ID
		collection.Status = artifactCollectionStatusFromCounts(collection)
		collection.UpdatedAt = now
		collection.EventSeq = seq
		action := incoming.Selection.Action
		if action == "" { action = "select" }
		ref := SessionArtifactSelectionReference{SessionID: input.SessionID, CollectionID: collection.ID, VariantID: selected.ID, EventSeq: seq, Label: incoming.Selection.Label, Description: incoming.Selection.Description, Action: action}
		selection = &ref
	}
	if err := validateArtifactCollectionProgress(collection); err != nil { return preparedV3ArtifactMutation{}, err }
	prepared.Projection = V3ArtifactProjection{Collection: collection, Variant: variant, Selection: selection}
	return prepared, nil
}

func artifactCollectionLineageCompatible(existing, incoming SessionArtifactLineage) bool {
	return existing.ParentSessionID == incoming.ParentSessionID && existing.TaskCallID == incoming.TaskCallID && existing.ProgramID == incoming.ProgramID && existing.RunID == incoming.RunID && existing.PlanID == incoming.PlanID && existing.CheckpointID == incoming.CheckpointID && existing.AttemptID == incoming.AttemptID
}

func artifactCollectionProgressTotal(collection SessionArtifactCollection) int {
	return collection.StagingCount + collection.ReadyCount + collection.FailedCount + collection.UnavailableCount
}

func validateArtifactCollectionProgress(collection SessionArtifactCollection) error {
	if collection.VariantCount < 0 || collection.StagingCount < 0 || collection.ReadyCount < 0 || collection.FailedCount < 0 || collection.UnavailableCount < 0 || artifactCollectionProgressTotal(collection) != collection.VariantCount {
		return errors.New("artifact collection progress is inconsistent")
	}
	return nil
}

func adjustArtifactCollectionStatusCount(collection *SessionArtifactCollection, status string, delta int) error {
	if collection == nil { return errors.New("artifact collection is required") }
	if delta == 0 { return nil }
	counter := (*int)(nil)
	switch status {
	case SessionArtifactStatusStaging:
		counter = &collection.StagingCount
	case SessionArtifactStatusReady:
		counter = &collection.ReadyCount
	case SessionArtifactStatusFailed:
		counter = &collection.FailedCount
	case SessionArtifactStatusUnavailable:
		counter = &collection.UnavailableCount
	}
	if counter == nil { return fmt.Errorf("artifact variant status %q is invalid", status) }
	if delta < 0 && *counter < -delta { return errors.New("artifact collection status count would underflow") }
	*counter += delta
	return nil
}

func artifactCollectionStatusFromCounts(collection SessionArtifactCollection) string {
	switch {
	case collection.ReadyCount > 0:
		return SessionArtifactStatusReady
	case collection.StagingCount > 0:
		return SessionArtifactStatusStaging
	case collection.UnavailableCount > 0:
		return SessionArtifactStatusUnavailable
	case collection.FailedCount > 0:
		return SessionArtifactStatusFailed
	default:
		return SessionArtifactStatusStaging
	}
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
	if prepared.DeleteCollection || len(prepared.DeletedVariants) != 0 {
		for _, variant := range prepared.DeletedVariants {
			keys := []string{
				KeySessionArtifactVariant(variant.AccountScopeID, variant.SessionID, variant.CollectionID, variant.ID),
				KeySessionArtifactVariantStatus(variant.AccountScopeID, variant.SessionID, variant.Status, variant.CollectionID, variant.ID),
			}
			keys = append(keys, artifactVariantLineageIndexKeys(variant)...)
			for _, key := range keys {
				if err := batch.Delete([]byte(key), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) { return err }
			}
			if variant.DigestSHA256 != "" {
				if err := batch.Delete([]byte(KeySessionArtifactVariantDigest(variant.AccountScopeID, variant.SessionID, variant.DigestSHA256, variant.CollectionID, variant.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) { return err }
			}
		}
		if prepared.DeleteCollection {
			if prepared.PreviousCollection == nil { return errors.New("artifact collection deletion is missing previous metadata") }
			if prepared.PreviousCollection.VariantCount != len(prepared.DeletedVariants) { return errors.New("artifact collection variant count is inconsistent") }
			for _, key := range []string{
				KeySessionArtifactCollection(collection.AccountScopeID, collection.SessionID, collection.ID),
				KeySessionArtifactCollectionStatus(collection.AccountScopeID, collection.SessionID, prepared.PreviousCollection.Status, collection.ID),
			} {
				if err := batch.Delete([]byte(key), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) { return err }
			}
			return nil
		}
	}
	if prepared.PreviousCollection != nil && prepared.PreviousCollection.Status != collection.Status {
		if err := batch.Delete([]byte(KeySessionArtifactCollectionStatus(collection.AccountScopeID, collection.SessionID, prepared.PreviousCollection.Status, collection.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) { return err }
	}
	collectionPayload, err := json.Marshal(collection)
	if err != nil { return fmt.Errorf("marshal artifact collection: %w", err) }
	if err := batch.Set([]byte(KeySessionArtifactCollection(collection.AccountScopeID, collection.SessionID, collection.ID)), collectionPayload, nil); err != nil { return err }
	if err := batch.Set([]byte(KeySessionArtifactCollectionStatus(collection.AccountScopeID, collection.SessionID, collection.Status, collection.ID)), []byte(collection.ID), nil); err != nil { return err }
	if variant := prepared.Projection.Variant; variant != nil && !prepared.DeleteVariant {
		if previous := prepared.PreviousVariant; previous != nil {
			for _, key := range artifactVariantLineageIndexKeys(*previous) {
				if err := batch.Delete([]byte(key), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) { return err }
			}
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
		for _, key := range artifactVariantLineageIndexKeys(*variant) {
			if err := batch.Set([]byte(key), []byte(variant.CollectionID+"\x00"+variant.ID), nil); err != nil { return err }
		}
	}
	return nil
}
