package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// ClaimDeliverablePublication synchronously commits intent BEFORE external I/O.
// Claims never expire: a crash may have happened after the remote side effect.
// Such records require operator reconciliation, never automatic retry or deletion.
func (s *SessionStore) ClaimDeliverablePublication(account, id, reviewer string) (DeliverableRecord, bool, error) {
	if s == nil || s.store == nil {
		return DeliverableRecord{}, false, errors.New("database not available")
	}
	s.store.deliverablesMu.Lock()
	defer s.store.deliverablesMu.Unlock()
	rec, found, err := s.GetDeliverable(account, id)
	if err != nil {
		return rec, false, err
	}
	if !found {
		return rec, false, errors.New("deliverable not found")
	}
	if rec.Status == "published" {
		if rec.PublicationClaim == "" || rec.ActionResult["status"] != "published" {
			return rec, false, errors.New("legacy publication receipt requires reconciliation")
		}
		return rec, false, nil
	}
	if rec.PublicationClaim != "" {
		return rec, false, errors.New("publication already attempted; reconciliation required")
	}
	if reviewer == "" || rec.Status != "pending_review" {
		return rec, false, errors.New("pending user review required")
	}
	if rec.ActionContract == nil || (rec.ActionContract.Action != "publish_x_post" && rec.ActionContract.Action != "execute_webhook") {
		return rec, false, errors.New("publication action required")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return rec, false, err
	}
	rec.PublicationClaim = hex.EncodeToString(token[:])
	rec.Status = "publishing"
	rec.ReviewedBy = reviewer
	rec.ReviewedAt = time.Now().UnixMilli()
	rec.ActionResult = map[string]any{"status": "reconciliation_required"}
	if err := s.putDeliverableLocked(account, &rec); err != nil {
		return rec, false, err
	}
	return rec, true, nil
}

// SaveDeliverablePublication persists each receipt before the next remote call.
// The claim is a fencing token; only its owner can finalize a publishing record.
func (s *SessionStore) SaveDeliverablePublication(account, id, claim, status string, result map[string]any) (DeliverableRecord, error) {
	if s == nil || s.store == nil {
		return DeliverableRecord{}, errors.New("database not available")
	}
	s.store.deliverablesMu.Lock()
	defer s.store.deliverablesMu.Unlock()
	rec, found, err := s.GetDeliverable(account, id)
	if err != nil {
		return rec, err
	}
	if !found || claim == "" || rec.PublicationClaim != claim || rec.Status != "publishing" {
		return rec, errors.New("publication claim conflict")
	}
	switch status {
	case "publishing", "published", "publication_failed", "reconciliation_required":
	default:
		return rec, errors.New("invalid publication outcome")
	}
	rec.Status = status
	rec.ActionResult = result
	if err := s.putDeliverableLocked(account, &rec); err != nil {
		return rec, err
	}
	return rec, nil
}
