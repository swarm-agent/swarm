package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

type DeliverableActionContract struct {
	Action          string         `json:"action"` // e.g. "publish_x_post", "execute_webhook", "merge_pr", "custom"
	TargetURL       string         `json:"target_url,omitempty"`
	TargetSecretRef string         `json:"target_secret_ref,omitempty"` // e.g. GCP secret reference or local ENV name
	Parameters      map[string]any `json:"parameters,omitempty"`
}

type DeliverableRevisionFeedback struct {
	RequestedAt int64    `json:"requested_at"`
	RequestedBy string   `json:"requested_by,omitempty"`
	Notes       string   `json:"notes"`
	Tags        []string `json:"tags,omitempty"`
}

type DeliverableRecord struct {
	ID               string                         `json:"id"`
	AccountID        string                         `json:"account_id"`
	WorkspaceID      string                         `json:"workspace_id,omitempty"`
	WorkspacePath    string                         `json:"workspace_path,omitempty"`
	WorkerID         string                         `json:"worker_id,omitempty"`
	OccurrenceID     string                         `json:"occurrence_id,omitempty"`
	SessionID        string                         `json:"session_id,omitempty"`
	Title            string                         `json:"title"`
	Kind             string                         `json:"kind"`   // "social_post", "alert", "report", "pr_patch", "media_bundle", "custom"
	Status           string                         `json:"status"` // "pending_review", "approved", "rejected", "published", "dismissed", "needs_revision"
	Summary          string                         `json:"summary,omitempty"`
	Payload          map[string]any                 `json:"payload,omitempty"` // posts, tweets, report text, diffs, etc.
	MediaRefs        []SessionPlanArtifactReference `json:"media_refs,omitempty"`
	ActionContract   *DeliverableActionContract     `json:"action_contract,omitempty"`
	ActionResult     map[string]any                 `json:"action_result,omitempty"`
	RevisionFeedback *DeliverableRevisionFeedback   `json:"revision_feedback,omitempty"`
	RevisionHistory  []DeliverableRevisionFeedback  `json:"revision_history,omitempty"`
	CreatedAt        int64                          `json:"created_at"`
	UpdatedAt        int64                          `json:"updated_at"`
	ReviewedAt       int64                          `json:"reviewed_at,omitempty"`
	ReviewedBy       string                         `json:"reviewed_by,omitempty"`
}

type DeliverableFilter struct {
	Status      string `json:"status,omitempty"`       // e.g. "pending_review", "approved", "dismissed", or empty for all
	WorkerID    string `json:"worker_id,omitempty"`    // filter by worker/automation ID
	Kind        string `json:"kind,omitempty"`         // "social_post", "alert", "report", etc.
	WorkspaceID string `json:"workspace_id,omitempty"` // workspace scope filter
	Limit       int    `json:"limit,omitempty"`        // max items (default 100)
}

func (d *DeliverableRecord) Validate() error {
	d.Title = strings.TrimSpace(d.Title)
	if d.Title == "" {
		if d.Kind != "" {
			d.Title = strings.Title(strings.ReplaceAll(d.Kind, "_", " "))
		} else {
			return errors.New("deliverable title is required")
		}
	}
	if d.Kind == "" {
		d.Kind = "custom"
	}
	if d.Status == "" {
		d.Status = "pending_review"
	}
	switch d.Status {
	case "pending_review", "approved", "rejected", "published", "dismissed", "needs_revision":
		// valid
	default:
		return errors.New("invalid deliverable status; must be pending_review, approved, rejected, published, dismissed, or needs_revision")
	}
	return nil
}

func (s *SessionStore) PutDeliverable(accountScopeID string, deliv *DeliverableRecord) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	if deliv == nil {
		return errors.New("deliverable definition required")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return errors.New("account scope id is required")
	}
	deliv.AccountID = accountScopeID
	if err := deliv.Validate(); err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	if deliv.ID == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		deliv.ID = "deliv_" + hex.EncodeToString(b)
	}
	if deliv.CreatedAt <= 0 {
		deliv.CreatedAt = now
	}
	deliv.UpdatedAt = now

	// Check if old record exists to clean up old index keys if status/worker changed
	var oldRecord *DeliverableRecord
	mainKey := KeyDeliverable(accountScopeID, deliv.ID)
	if val, closer, err := s.store.db.Get([]byte(mainKey)); err == nil {
		var old DeliverableRecord
		if json.Unmarshal(val, &old) == nil {
			oldRecord = &old
		}
		closer.Close()
	}

	val, err := json.Marshal(deliv)
	if err != nil {
		return err
	}

	batch := s.store.db.NewBatch()
	defer batch.Close()

	if err := batch.Set([]byte(mainKey), val, nil); err != nil {
		return err
	}

	// Clean up stale index keys
	if oldRecord != nil {
		if oldRecord.Status != deliv.Status {
			oldStatusKey := KeyDeliverableStatusIndex(accountScopeID, oldRecord.Status, deliv.ID)
			_ = batch.Delete([]byte(oldStatusKey), nil)
		}
		if oldRecord.WorkerID != deliv.WorkerID && oldRecord.WorkerID != "" {
			oldWorkerKey := KeyDeliverableWorkerIndex(accountScopeID, oldRecord.WorkerID, deliv.ID)
			_ = batch.Delete([]byte(oldWorkerKey), nil)
		}
	}

	// Write index keys
	statusKey := KeyDeliverableStatusIndex(accountScopeID, deliv.Status, deliv.ID)
	if err := batch.Set([]byte(statusKey), []byte(deliv.ID), nil); err != nil {
		return err
	}
	if deliv.WorkerID != "" {
		workerKey := KeyDeliverableWorkerIndex(accountScopeID, deliv.WorkerID, deliv.ID)
		if err := batch.Set([]byte(workerKey), []byte(deliv.ID), nil); err != nil {
			return err
		}
	}

	return batch.Commit(pebble.Sync)
}

func (s *SessionStore) GetDeliverable(accountScopeID, id string) (DeliverableRecord, bool, error) {
	var out DeliverableRecord
	if s == nil || s.store == nil || s.store.db == nil {
		return out, false, errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	id = strings.TrimSpace(id)
	if accountScopeID == "" || id == "" {
		return out, false, errors.New("account scope id and deliverable id are required")
	}

	key := KeyDeliverable(accountScopeID, id)
	val, closer, err := s.store.db.Get([]byte(key))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return out, false, nil
		}
		return out, false, err
	}
	defer closer.Close()

	if err := json.Unmarshal(val, &out); err != nil {
		return out, false, err
	}
	return out, true, nil
}

func (s *SessionStore) ListDeliverables(accountScopeID string, filter DeliverableFilter) ([]DeliverableRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}

	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	var records []DeliverableRecord

	// If filtered by status, use status index for quick matching
	if filter.Status != "" {
		prefix := DeliverableStatusIndexPrefix(accountScopeID, filter.Status)
		err := iteratePrefixFromReader(s.store.db, prefix, 0, func(key string, value []byte) error {
			id := string(value)
			rec, found, err := s.GetDeliverable(accountScopeID, id)
			if err != nil || !found {
				return nil
			}
			if filter.WorkerID != "" && rec.WorkerID != filter.WorkerID {
				return nil
			}
			if filter.Kind != "" && rec.Kind != filter.Kind {
				return nil
			}
			if filter.WorkspaceID != "" && rec.WorkspaceID != filter.WorkspaceID {
				return nil
			}
			records = append(records, rec)
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if filter.WorkerID != "" {
		// Filter by worker index
		prefix := DeliverableWorkerIndexPrefix(accountScopeID, filter.WorkerID)
		err := iteratePrefixFromReader(s.store.db, prefix, 0, func(key string, value []byte) error {
			id := string(value)
			rec, found, err := s.GetDeliverable(accountScopeID, id)
			if err != nil || !found {
				return nil
			}
			if filter.Kind != "" && rec.Kind != filter.Kind {
				return nil
			}
			if filter.WorkspaceID != "" && rec.WorkspaceID != filter.WorkspaceID {
				return nil
			}
			records = append(records, rec)
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		// Scan all deliverables for account
		prefix := DeliverablePrefix(accountScopeID)
		err := iteratePrefixFromReader(s.store.db, prefix, 0, func(key string, value []byte) error {
			var rec DeliverableRecord
			if err := json.Unmarshal(value, &rec); err != nil {
				return nil
			}
			if filter.Kind != "" && rec.Kind != filter.Kind {
				return nil
			}
			if filter.WorkspaceID != "" && rec.WorkspaceID != filter.WorkspaceID {
				return nil
			}
			records = append(records, rec)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// Sort newest first (CreatedAt descending)
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt > records[j].CreatedAt
	})

	if len(records) > limit {
		records = records[:limit]
	}

	return records, nil
}

func (s *SessionStore) UpdateDeliverableStatus(accountScopeID, id, status, reviewer string, actionResult map[string]any) (DeliverableRecord, error) {
	rec, found, err := s.GetDeliverable(accountScopeID, id)
	if err != nil {
		return rec, err
	}
	if !found {
		return rec, errors.New("deliverable not found")
	}

	rec.Status = status
	rec.ReviewedAt = time.Now().UnixMilli()
	if reviewer != "" {
		rec.ReviewedBy = reviewer
	}
	if actionResult != nil {
		rec.ActionResult = actionResult
	}

	if err := s.PutDeliverable(accountScopeID, &rec); err != nil {
		return rec, err
	}
	return rec, nil
}

func (s *SessionStore) RequestChangesDeliverable(accountScopeID, id, notes string, tags []string, requestedBy string) (DeliverableRecord, error) {
	rec, found, err := s.GetDeliverable(accountScopeID, id)
	if err != nil {
		return rec, err
	}
	if !found {
		return rec, errors.New("deliverable not found")
	}

	now := time.Now().UnixMilli()
	fb := DeliverableRevisionFeedback{
		RequestedAt: now,
		RequestedBy: requestedBy,
		Notes:       notes,
		Tags:        tags,
	}
	rec.Status = "needs_revision"
	rec.RevisionFeedback = &fb
	rec.RevisionHistory = append(rec.RevisionHistory, fb)
	rec.UpdatedAt = now

	if err := s.PutDeliverable(accountScopeID, &rec); err != nil {
		return rec, err
	}
	return rec, nil
}

func (s *SessionStore) DeleteDeliverable(accountScopeID, id string) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	id = strings.TrimSpace(id)
	if accountScopeID == "" || id == "" {
		return errors.New("account scope id and deliverable id are required")
	}

	rec, found, err := s.GetDeliverable(accountScopeID, id)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	batch := s.store.db.NewBatch()
	defer batch.Close()

	mainKey := KeyDeliverable(accountScopeID, id)
	_ = batch.Delete([]byte(mainKey), nil)

	statusKey := KeyDeliverableStatusIndex(accountScopeID, rec.Status, id)
	_ = batch.Delete([]byte(statusKey), nil)

	if rec.WorkerID != "" {
		workerKey := KeyDeliverableWorkerIndex(accountScopeID, rec.WorkerID, id)
		_ = batch.Delete([]byte(workerKey), nil)
	}

	return batch.Commit(pebble.Sync)
}
