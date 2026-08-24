package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

// SessionArtifactRepairReport describes bounded idempotent maintenance of
// rebuildable catalog projections. Git remains the artifact authority.
type SessionArtifactRepairReport struct {
	CollectionsVisited  int
	CollectionsRepaired int
}

// RepairSessionArtifactCollections derives redundant collection progress from
// exact Git-backed variant projections. It never reconstructs ancestry or bytes.
func (s *SessionStore) RepairSessionArtifactCollections(sessionID string) (SessionArtifactRepairReport, error) {
	if s == nil || s.store == nil {
		return SessionArtifactRepairReport{}, errors.New("session store is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	storedSession, ok, err := s.GetSession(sessionID)
	if err != nil {
		return SessionArtifactRepairReport{}, err
	}
	if !ok {
		return SessionArtifactRepairReport{}, fmt.Errorf("session %q was not found", sessionID)
	}
	accountScopeID := strings.TrimSpace(storedSession.AccountScopeID)
	if accountScopeID == "" {
		return SessionArtifactRepairReport{}, errors.New("artifact repair requires session account ownership")
	}

	unlockSession := s.store.sessionMutations.lockSessions(sessionID)
	defer unlockSession()

	report := SessionArtifactRepairReport{}
	batch := s.store.NewBatch()
	defer batch.Close()
	const iterateAllCollections = int(^uint(0) >> 1)
	err = s.store.IteratePrefix(SessionArtifactCollectionPrefix(accountScopeID, sessionID), iterateAllCollections, func(_ string, value []byte) error {
		var collection SessionArtifactCollection
		if err := json.Unmarshal(value, &collection); err != nil {
			return fmt.Errorf("decode artifact collection for repair: %w", err)
		}
		if collection.AccountScopeID != accountScopeID || collection.SessionID != sessionID {
			return errors.New("artifact collection ownership metadata is inconsistent")
		}
		if collection.Lineage.ParentSessionID != "" && collection.Lineage.ParentSessionID != sessionID {
			return errors.New("artifact collection parent lineage is inconsistent")
		}
		report.CollectionsVisited++

		variants := make([]SessionArtifactVariant, 0, SessionArtifactMaxVariantsPerCollection)
		if err := s.store.IteratePrefix(SessionArtifactVariantPrefix(accountScopeID, sessionID, collection.ID), SessionArtifactMaxVariantsPerCollection+1, func(_ string, variantValue []byte) error {
			if len(variants) >= SessionArtifactMaxVariantsPerCollection {
				return errors.New("artifact collection variant limit exceeded")
			}
			var variant SessionArtifactVariant
			if err := json.Unmarshal(variantValue, &variant); err != nil {
				return fmt.Errorf("decode artifact variant for repair: %w", err)
			}
			if variant.AccountScopeID != accountScopeID || variant.SessionID != sessionID || variant.CollectionID != collection.ID {
				return errors.New("artifact variant ownership metadata is inconsistent")
			}
			if variant.GraphState != SessionArtifactGraphProjection || variant.RepositoryID == "" || !validGitOID(variant.CommitOID) {
				return errors.New("artifact repair refuses a variant without an exact Git projection")
			}
			if variant.Lineage.ParentSessionID != "" && variant.Lineage.ParentSessionID != sessionID {
				return errors.New("artifact variant parent lineage is inconsistent")
			}
			switch variant.Status {
			case SessionArtifactStatusStaging, SessionArtifactStatusReady, SessionArtifactStatusFailed, SessionArtifactStatusUnavailable:
			default:
				return errors.New("artifact variant status is inconsistent")
			}
			variants = append(variants, variant)
			return nil
		}); err != nil {
			return err
		}

		repaired := collection
		repaired.VariantCount = len(variants)
		repaired.StagingCount = 0
		repaired.ReadyCount = 0
		repaired.FailedCount = 0
		repaired.UnavailableCount = 0
		selectedReady := repaired.SelectedVariantID == ""
		for _, variant := range variants {
			switch variant.Status {
			case SessionArtifactStatusStaging:
				repaired.StagingCount++
			case SessionArtifactStatusReady:
				repaired.ReadyCount++
			case SessionArtifactStatusFailed:
				repaired.FailedCount++
			case SessionArtifactStatusUnavailable:
				repaired.UnavailableCount++
			}
			if variant.ID == repaired.SelectedVariantID && variant.Status == SessionArtifactStatusReady {
				selectedReady = true
			}
		}
		if !selectedReady {
			repaired.SelectedVariantID = ""
		}
		repaired.Status = artifactCollectionStatusFromCounts(repaired)
		if repaired == collection {
			return nil
		}
		if err := validateArtifactCollectionProgress(repaired); err != nil {
			return err
		}
		for _, status := range []string{SessionArtifactStatusStaging, SessionArtifactStatusReady, SessionArtifactStatusFailed, SessionArtifactStatusUnavailable} {
			if err := batch.Delete([]byte(KeySessionArtifactCollectionStatus(accountScopeID, sessionID, status, collection.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
				return err
			}
		}
		payload, err := json.Marshal(repaired)
		if err != nil {
			return fmt.Errorf("marshal repaired artifact collection: %w", err)
		}
		if err := batch.Set([]byte(KeySessionArtifactCollection(accountScopeID, sessionID, collection.ID)), payload, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeySessionArtifactCollectionStatus(accountScopeID, sessionID, repaired.Status, collection.ID)), []byte(collection.ID), nil); err != nil {
			return err
		}
		report.CollectionsRepaired++
		return nil
	})
	if err != nil {
		return SessionArtifactRepairReport{}, err
	}
	if report.CollectionsRepaired == 0 {
		return report, nil
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return SessionArtifactRepairReport{}, err
	}
	return report, nil
}
