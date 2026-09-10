package pebblestore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/cockroachdb/pebble"
)

// AutomationDeliveryReference names immutable persisted evidence, never a
// caller-provided notification body. Terminal occurrence revisions are the
// durable outbox source; recovery can enumerate them even before first delivery.
type AutomationDeliveryReference struct {
	Scope AutomationScope
	AutomationID string
	OccurrenceID string
	Revision uint64
}

type AutomationDelivery struct {
	Reference AutomationDeliveryReference
	Attempts int
	NextAt int64
	Acked bool
}

func (r AutomationDeliveryReference) NotificationID() string {
	b, _ := json.Marshal(r)
	return fmt.Sprintf("automation-%x", sha256.Sum256(b))
}

func (s *Store) ClaimAutomationDelivery(ref AutomationDeliveryReference, now int64) (AutomationRecord, AutomationDelivery, bool, error) {
	s.automationsMu.Lock()
	defer s.automationsMu.Unlock()
	var d AutomationDelivery
	if ref.Revision == 0 || now <= 0 { return AutomationRecord{}, d, false, ErrAutomationInvalid }
	r, found, err := s.GetAutomationRecord(ref.Scope, ref.AutomationID, "occurrence", ref.OccurrenceID, ref.Revision)
	if err != nil { return r, d, false, err }
	if !found || r.Occurrence == nil { return r, d, false, ErrAutomationInvalid }
	switch r.Occurrence.State { case "completed", "failed", "blocked", "cancelled", "skipped": default: return r, d, false, ErrAutomationInvalid }
	key := "automation-delivery:v1:" + ref.NotificationID()
	found, err = s.GetJSON(key, &d)
	if err != nil { return r, d, false, err }
	if found && d.Reference != ref { return r, d, false, ErrAutomationConflict }
	if d.Acked || d.Attempts >= 5 || now < d.NextAt { return r, d, false, nil }
	d.Reference = ref
	d.Attempts++
	// Durable bounded backoff also leases the attempt across concurrent workers.
	d.NextAt = now + int64(30000 << (d.Attempts-1))
	b, err := json.Marshal(d)
	if err != nil { return r, d, false, err }
	batch := s.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(key), b, nil); err != nil { return r, d, false, err }
	if err := batch.Commit(pebble.Sync); err != nil { return r, d, false, err }
	return r, d, true, nil
}

func (s *Store) AckAutomationDelivery(d AutomationDelivery) error {
	s.automationsMu.Lock()
	defer s.automationsMu.Unlock()
	key := "automation-delivery:v1:" + d.Reference.NotificationID()
	var current AutomationDelivery
	found, err := s.GetJSON(key, &current)
	if err != nil { return err }
	if !found || current.Reference != d.Reference || current.Attempts != d.Attempts { return ErrAutomationConflict }
	if current.Acked { return nil }
	current.Acked = true
	b, err := json.Marshal(current)
	if err != nil { return err }
	batch := s.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(key), b, nil); err != nil { return err }
	return batch.Commit(pebble.Sync)
}
