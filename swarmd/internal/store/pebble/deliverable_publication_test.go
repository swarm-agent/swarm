package pebblestore

import (
	"sync"
	"sync/atomic"
	"testing"
)

// Purpose: Claim/SaveDeliverablePublication must fence concurrent reviewers and
// retain receipts across reopen. Real temporary Pebble is the narrowest layer
// proving synced persistence, account isolation and mutation bypass rejection.
func TestDeliverablePublicationDurableClaim(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil { t.Fatal(err) }
	s := NewSessionStore(db)
	rec := DeliverableRecord{Title: "post", ActionContract: &DeliverableActionContract{Action: "publish_x_post"}}
	if err := s.PutDeliverable("owner", &rec); err != nil { t.Fatal(err) }
	if _, _, err := s.ClaimDeliverablePublication("other", rec.ID, "reviewer"); err == nil { t.Fatal("cross-account claim allowed") }
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, claimed, err := NewSessionStore(db).ClaimDeliverablePublication("owner", rec.ID, "reviewer"); if err == nil && claimed { winners.Add(1) } }()
	}
	wg.Wait()
	if winners.Load() != 1 { t.Fatalf("claim winners: %d", winners.Load()) }
	claimed, _, err := s.GetDeliverable("owner", rec.ID)
	if err != nil { t.Fatal(err) }
	receipt := map[string]any{"status": "reconciliation_required", "tweet_id": "123"}
	if _, err := s.SaveDeliverablePublication("other", rec.ID, claimed.PublicationClaim, "published", receipt); err == nil { t.Fatal("cross-account finalize allowed") }
	if _, err := s.SaveDeliverablePublication("owner", rec.ID, "stale", "published", receipt); err == nil { t.Fatal("stale claim allowed") }
	if _, err := s.SaveDeliverablePublication("owner", rec.ID, claimed.PublicationClaim, "publishing", receipt); err != nil { t.Fatal(err) }
	if err := s.PutDeliverable("owner", &rec); err == nil { t.Fatal("overwrite erased claim") }
	if err := s.DeleteDeliverable("owner", rec.ID); err == nil { t.Fatal("delete erased claim") }
	if _, err := s.UpdateDeliverableStatus("owner", rec.ID, "dismissed", "reviewer", nil); err == nil { t.Fatal("dismiss erased claim") }
	if _, err := s.RequestChangesDeliverable("owner", rec.ID, "retry", nil, "reviewer"); err == nil { t.Fatal("revision erased claim") }
	if err := db.Close(); err != nil { t.Fatal(err) }
	db, err = Open(path)
	if err != nil { t.Fatal(err) }
	defer db.Close()
	s = NewSessionStore(db)
	restored, claimedAgain, err := s.ClaimDeliverablePublication("owner", rec.ID, "reviewer")
	if err == nil || claimedAgain || restored.ActionResult["tweet_id"] != "123" || restored.Status != "publishing" { t.Fatalf("restart lost protection/receipt: %+v, %v", restored, err) }
	if _, err := s.SaveDeliverablePublication("owner", rec.ID, claimed.PublicationClaim, "published", map[string]any{"status": "published", "tweet_id": "123"}); err != nil { t.Fatal(err) }
	restored, claimedAgain, err = s.ClaimDeliverablePublication("owner", rec.ID, "reviewer")
	if err != nil || claimedAgain || restored.ActionResult["tweet_id"] != "123" { t.Fatalf("successful replay lost receipt: %+v %v", restored, err) }
}
