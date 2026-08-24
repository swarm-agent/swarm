package pebblestore

import (
	"errors"
	"fmt"
	"strings"
)

// SessionArtifactPartLocator is optional review metadata for an already
// authoritative part definition. It never identifies bytes or proves that a
// part exists.
type SessionArtifactPartLocator struct {
	Kind     string  `json:"kind"`
	StartMs  int64   `json:"start_ms,omitempty"`
	EndMs    int64   `json:"end_ms,omitempty"`
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
	Width    float64 `json:"width,omitempty"`
	Height   float64 `json:"height,omitempty"`
	Page     int     `json:"page,omitempty"`
	StateID  string  `json:"state_id,omitempty"`
	Selector string  `json:"selector,omitempty"`
}

// SessionArtifactPartDefinition is the stable identity of one independently
// replaceable component in an artifact chain. Locator is display-only.
type SessionArtifactPartDefinition struct {
	Version         int                         `json:"version"`
	GraphState      string                      `json:"graph_state"`
	ArtifactChainID string                      `json:"artifact_chain_id"`
	ID              string                      `json:"id"`
	AccountScopeID  string                      `json:"account_scope_id"`
	UserID          string                      `json:"user_id"`
	OwnerSessionID  string                      `json:"owner_session_id"`
	Label           string                      `json:"label"`
	Description     string                      `json:"description,omitempty"`
	Locator         *SessionArtifactPartLocator `json:"locator,omitempty"`
	CreatedAt       int64                       `json:"created_at"`
	EventSeq        uint64                      `json:"event_seq"`
}

// SessionArtifactPartRevisionReference is an exact projection of a blob in the
// authoritative artifact Git repository. Pebble never resolves this reference
// to an application-managed file and never treats the repeated digest as the
// byte authority.
type SessionArtifactPartRevisionReference struct {
	ArtifactChainID string `json:"artifact_chain_id"`
	PartID          string `json:"part_id"`
	PartRevisionID  string `json:"part_revision_id"`
	OwnerSessionID  string `json:"owner_session_id"`
	RepositoryID    string `json:"repository_id"`
	CommitOID       string `json:"commit_oid"`
	BlobOID         string `json:"blob_oid"`
	DigestSHA256    string `json:"digest_sha256,omitempty"`
	Size            int64  `json:"size"`
	MediaType       string `json:"media_type"`
}

// SessionArtifactPartRevision is rebuildable metadata projected from an exact
// Git commit/tree/blob tuple. ParentCommitOIDs mirrors Git ancestry and may be
// empty, singular, or multi-parent; Pebble does not construct ancestry itself.
type SessionArtifactPartRevision struct {
	Version          int      `json:"version"`
	GraphState       string   `json:"graph_state"`
	ArtifactChainID  string   `json:"artifact_chain_id"`
	PartID           string   `json:"part_id"`
	ID               string   `json:"id"`
	AccountScopeID   string   `json:"account_scope_id"`
	UserID           string   `json:"user_id"`
	OwnerSessionID   string   `json:"owner_session_id"`
	RepositoryID     string   `json:"repository_id"`
	CommitOID        string   `json:"commit_oid"`
	ParentCommitOIDs []string `json:"parent_commit_oids,omitempty"`
	BlobOID          string   `json:"blob_oid"`
	DigestSHA256     string   `json:"digest_sha256,omitempty"`
	Size             int64    `json:"size"`
	MediaType        string   `json:"media_type"`
	// Parent decodes pre-Git projections only. New writes leave it nil.
	Parent           *SessionArtifactPartRevisionReference `json:"parent,omitempty"`
	IterationTurnID  string                                `json:"iteration_turn_id,omitempty"`
	IterationGroupID string                                `json:"iteration_group_id,omitempty"`
	CreatedAt        int64                                 `json:"created_at"`
	EventSeq         uint64                                `json:"event_seq"`
}

func (revision SessionArtifactPartRevision) Reference() SessionArtifactPartRevisionReference {
	return SessionArtifactPartRevisionReference{
		ArtifactChainID: revision.ArtifactChainID,
		PartID:          revision.PartID,
		PartRevisionID:  revision.ID,
		OwnerSessionID:  revision.OwnerSessionID,
		RepositoryID:    revision.RepositoryID,
		CommitOID:       revision.CommitOID,
		BlobOID:         revision.BlobOID,
		DigestSHA256:    revision.DigestSHA256,
		Size:            revision.Size,
		MediaType:       revision.MediaType,
	}
}

// SessionArtifactCompositionPart is one ordered composition slot. The stable
// definition and exact byte revision may have different storage owners, but
// both remain in the same artifact chain and authenticated account/user scope.
type SessionArtifactCompositionPart struct {
	PartID                   string                               `json:"part_id"`
	DefinitionOwnerSessionID string                               `json:"definition_owner_session_id"`
	Revision                 SessionArtifactPartRevisionReference `json:"revision"`
	Locked                   bool                                 `json:"locked,omitempty"`
}

// SessionArtifactCompositionReference identifies one exact Git commit.
type SessionArtifactCompositionReference struct {
	ArtifactChainID string `json:"artifact_chain_id"`
	CompositionID   string `json:"composition_id"`
	OwnerSessionID  string `json:"owner_session_id"`
	RepositoryID    string `json:"repository_id"`
	CommitOID       string `json:"commit_oid"`
	EventSeq        uint64 `json:"event_seq"`
}

// SessionArtifactConstruction declares how exact part bytes become the complete
// artifact. No media-derived or implicit construction is permitted.
type SessionArtifactConstruction struct {
	Kind    string                             `json:"kind"`
	Entries []SessionArtifactConstructionEntry `json:"entries,omitempty"`
}

type SessionArtifactConstructionEntry struct {
	PartID string `json:"part_id"`
	Path   string `json:"path,omitempty"`
}

// SessionArtifactComposition is a rebuildable projection of one complete Git
// commit. Git owns the tree, ordered part blobs, locks, and merge ancestry.
type SessionArtifactComposition struct {
	Version          int      `json:"version"`
	GraphState       string   `json:"graph_state"`
	ID               string   `json:"id"`
	ArtifactChainID  string   `json:"artifact_chain_id"`
	AccountScopeID   string   `json:"account_scope_id"`
	UserID           string   `json:"user_id"`
	OwnerSessionID   string   `json:"owner_session_id"`
	RepositoryID     string   `json:"repository_id"`
	CommitOID        string   `json:"commit_oid"`
	TreeOID          string   `json:"tree_oid"`
	ParentCommitOIDs []string `json:"parent_commit_oids,omitempty"`
	// Parent decodes pre-Git projections only. ParentCommitOIDs is authoritative.
	Parent           *SessionArtifactCompositionReference `json:"parent,omitempty"`
	IterationTurnID  string                               `json:"iteration_turn_id,omitempty"`
	IterationGroupID string                               `json:"iteration_group_id,omitempty"`
	Construction     SessionArtifactConstruction          `json:"construction"`
	Parts            []SessionArtifactCompositionPart     `json:"parts"`
	CreatedAt        int64                                `json:"created_at"`
	EventSeq         uint64                               `json:"event_seq"`
}

func KeySessionArtifactPartDefinition(accountScopeID, ownerSessionID, chainID, partID string) string {
	return fmt.Sprintf("v3/session_artifact/part_definitions/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(ownerSessionID), keyPart(chainID), keyPart(partID))
}

func SessionArtifactPartDefinitionSessionPrefix(accountScopeID, ownerSessionID string) string {
	return fmt.Sprintf("v3/session_artifact/part_definitions/%s/%s/", keyPart(accountScopeID), keyPart(ownerSessionID))
}

func KeySessionArtifactPartRevision(accountScopeID, ownerSessionID, chainID, partID, revisionID string) string {
	return fmt.Sprintf("v3/session_artifact/part_revisions/%s/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(ownerSessionID), keyPart(chainID), keyPart(partID), keyPart(revisionID))
}

func SessionArtifactPartRevisionSessionPrefix(accountScopeID, ownerSessionID string) string {
	return fmt.Sprintf("v3/session_artifact/part_revisions/%s/%s/", keyPart(accountScopeID), keyPart(ownerSessionID))
}

func KeySessionArtifactComposition(accountScopeID, ownerSessionID, chainID, compositionID string) string {
	return fmt.Sprintf("v3/session_artifact/compositions/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(ownerSessionID), keyPart(chainID), keyPart(compositionID))
}

func SessionArtifactCompositionSessionPrefix(accountScopeID, ownerSessionID string) string {
	return fmt.Sprintf("v3/session_artifact/compositions/%s/%s/", keyPart(accountScopeID), keyPart(ownerSessionID))
}

func (s *SessionStore) GetSessionArtifactPartDefinition(accountScopeID, userID, ownerSessionID, chainID, partID string) (SessionArtifactPartDefinition, bool, error) {
	if s == nil || s.store == nil {
		return SessionArtifactPartDefinition{}, false, errors.New("session store is not configured")
	}
	accountScopeID, userID, ownerSessionID, chainID, partID = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), strings.TrimSpace(ownerSessionID), strings.TrimSpace(chainID), strings.TrimSpace(partID)
	if accountScopeID == "" || userID == "" || ownerSessionID == "" || chainID == "" || partID == "" {
		return SessionArtifactPartDefinition{}, false, nil
	}
	var definition SessionArtifactPartDefinition
	ok, err := s.store.GetJSON(KeySessionArtifactPartDefinition(accountScopeID, ownerSessionID, chainID, partID), &definition)
	if err != nil || !ok {
		return SessionArtifactPartDefinition{}, ok, err
	}
	if definition.AccountScopeID != accountScopeID || definition.UserID != userID || definition.OwnerSessionID != ownerSessionID || definition.ArtifactChainID != chainID || definition.ID != partID {
		return SessionArtifactPartDefinition{}, false, errors.New("artifact part definition ownership metadata is inconsistent")
	}
	return definition, true, nil
}

func (s *SessionStore) GetSessionArtifactPartRevision(accountScopeID, userID, ownerSessionID, chainID, partID, revisionID string) (SessionArtifactPartRevision, bool, error) {
	if s == nil || s.store == nil {
		return SessionArtifactPartRevision{}, false, errors.New("session store is not configured")
	}
	accountScopeID, userID, ownerSessionID, chainID, partID, revisionID = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), strings.TrimSpace(ownerSessionID), strings.TrimSpace(chainID), strings.TrimSpace(partID), strings.TrimSpace(revisionID)
	if accountScopeID == "" || userID == "" || ownerSessionID == "" || chainID == "" || partID == "" || revisionID == "" {
		return SessionArtifactPartRevision{}, false, nil
	}
	var revision SessionArtifactPartRevision
	ok, err := s.store.GetJSON(KeySessionArtifactPartRevision(accountScopeID, ownerSessionID, chainID, partID, revisionID), &revision)
	if err != nil || !ok {
		return SessionArtifactPartRevision{}, ok, err
	}
	if revision.AccountScopeID != accountScopeID || revision.UserID != userID || revision.OwnerSessionID != ownerSessionID || revision.ArtifactChainID != chainID || revision.PartID != partID || revision.ID != revisionID {
		return SessionArtifactPartRevision{}, false, errors.New("artifact part revision ownership metadata is inconsistent")
	}
	return revision, true, nil
}

func (s *SessionStore) GetSessionArtifactComposition(accountScopeID, userID, ownerSessionID, chainID, compositionID string) (SessionArtifactComposition, bool, error) {
	if s == nil || s.store == nil {
		return SessionArtifactComposition{}, false, errors.New("session store is not configured")
	}
	accountScopeID, userID, ownerSessionID, chainID, compositionID = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), strings.TrimSpace(ownerSessionID), strings.TrimSpace(chainID), strings.TrimSpace(compositionID)
	if accountScopeID == "" || userID == "" || ownerSessionID == "" || chainID == "" || compositionID == "" {
		return SessionArtifactComposition{}, false, nil
	}
	var composition SessionArtifactComposition
	ok, err := s.store.GetJSON(KeySessionArtifactComposition(accountScopeID, ownerSessionID, chainID, compositionID), &composition)
	if err != nil || !ok {
		return SessionArtifactComposition{}, ok, err
	}
	if composition.AccountScopeID != accountScopeID || composition.UserID != userID || composition.OwnerSessionID != ownerSessionID || composition.ArtifactChainID != chainID || composition.ID != compositionID {
		return SessionArtifactComposition{}, false, errors.New("artifact composition ownership metadata is inconsistent")
	}
	return composition, true, nil
}

func normalizeArtifactPartRevisionReference(reference *SessionArtifactPartRevisionReference) {
	if reference == nil {
		return
	}
	reference.ArtifactChainID = strings.TrimSpace(reference.ArtifactChainID)
	reference.PartID = strings.TrimSpace(reference.PartID)
	reference.PartRevisionID = strings.TrimSpace(reference.PartRevisionID)
	reference.OwnerSessionID = strings.TrimSpace(reference.OwnerSessionID)
	reference.RepositoryID = strings.TrimSpace(reference.RepositoryID)
	reference.CommitOID = strings.ToLower(strings.TrimSpace(reference.CommitOID))
	reference.BlobOID = strings.ToLower(strings.TrimSpace(reference.BlobOID))
	reference.DigestSHA256 = strings.ToLower(strings.TrimSpace(reference.DigestSHA256))
	reference.MediaType = strings.ToLower(strings.TrimSpace(reference.MediaType))
}

func normalizeArtifactPartLocator(locator *SessionArtifactPartLocator) {
	if locator == nil {
		return
	}
	locator.Kind = strings.ToLower(strings.TrimSpace(locator.Kind))
	locator.StateID = strings.TrimSpace(locator.StateID)
	locator.Selector = strings.TrimSpace(locator.Selector)
}

func validateArtifactPartLocator(locator *SessionArtifactPartLocator) error {
	if locator == nil {
		return nil
	}
	part := SessionArtifactPart{ID: "locator", Label: "locator", Kind: locator.Kind, StartMs: locator.StartMs, EndMs: locator.EndMs, X: locator.X, Y: locator.Y, Width: locator.Width, Height: locator.Height, Page: locator.Page, StateID: locator.StateID, Selector: locator.Selector}
	return validateArtifactReviewPart(part)
}

func validateArtifactReviewPart(part SessionArtifactPart) error {
	if err := validateArtifactID("part", part.ID); err != nil {
		return err
	}
	if part.Label == "" || len(part.Label) > 256 || len(part.Description) > 2048 || len(part.Kind) > 32 || len(part.StateID) > 128 || len(part.Selector) > 512 {
		return errors.New("artifact part metadata is incomplete or exceeds bounds")
	}
	switch part.Kind {
	case "temporal":
		if part.StartMs < 0 || part.EndMs <= part.StartMs {
			return errors.New("temporal artifact part requires a valid time range")
		}
	case "spatial":
		if part.X < 0 || part.Y < 0 || part.Width <= 0 || part.Height <= 0 || part.X+part.Width > 1 || part.Y+part.Height > 1 {
			return errors.New("spatial artifact part requires normalized bounds")
		}
	case "page":
		if part.Page < 1 {
			return errors.New("page artifact part requires a positive page")
		}
	case "state":
		if part.StateID == "" {
			return errors.New("state artifact part requires a state id")
		}
	case "selector":
		if part.Selector == "" {
			return errors.New("selector artifact part requires a selector")
		}
	case "semantic":
	default:
		return errors.New("artifact part kind is invalid")
	}
	return nil
}

func validateArtifactPartRevisionReference(reference SessionArtifactPartRevisionReference) error {
	if err := validateArtifactID("part", reference.PartID); err != nil {
		return err
	}
	if err := validateArtifactID("part revision", reference.PartRevisionID); err != nil {
		return err
	}
	if reference.ArtifactChainID == "" || reference.OwnerSessionID == "" || len(reference.ArtifactChainID) > 128 || len(reference.OwnerSessionID) > 128 || len(reference.MediaType) > 255 || reference.Size <= 0 {
		return errors.New("artifact part revision reference is incomplete")
	}
	if err := validateArtifactRepositoryID(reference.RepositoryID); err != nil {
		return err
	}
	if !validGitOID(reference.CommitOID) || !validGitOID(reference.BlobOID) {
		return errors.New("artifact part revision requires exact Git commit and blob oids")
	}
	if reference.DigestSHA256 != "" && !validArtifactDigest(reference.DigestSHA256) {
		return errors.New("artifact part revision digest is invalid")
	}
	return nil
}

func exactPartRevisionReference(reference SessionArtifactPartRevisionReference, revision SessionArtifactPartRevision) bool {
	return reference == revision.Reference()
}
