package pebblestore

import (
	"strings"
	"sync"
	"testing"
)

// Purpose: AppendOnce owns atomic signal/receipt persistence. Concurrent wrappers,
// account separation, retention and reopen must not duplicate a completed effect
// or suppress a different principal/key; this store test is the narrowest layer.
func TestPublishLandSignalOnce(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	sig := Signal{Account: "a", Kind: "job.landed", Summary: "landed", DedupKey: strings.Repeat("x", 250)}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := NewSignalStore(db).AppendOnce(sig); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	feed := NewSignalStore(db)
	page, err := feed.ListAfter(0, 100, SignalFilter{Account: "a"})
	if err != nil || len(page.Signals) != 1 || page.LatestSeq != 1 {
		t.Fatalf("duplicates: %+v %v", page, err)
	}
	original := page.Signals[0]
	// Feed retention must not erase the idempotency receipt.
	feed.keep = 1
	other := sig
	other.Account = "b"
	if _, err = feed.AppendOnce(other); err != nil {
		t.Fatal(err)
	}
	other = sig
	other.DedupKey += "different"
	if _, err = feed.AppendOnce(other); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	feed = NewSignalStore(db)
	replay, err := feed.AppendOnce(sig)
	if err != nil || replay.ID != original.ID || replay.Seq != original.Seq {
		t.Fatalf("lost receipt: %+v %v", replay, err)
	}
	page, err = feed.ListAfter(0, 100, SignalFilter{Account: "a"})
	if err != nil || page.LatestSeq != 3 {
		t.Fatalf("replay allocated sequence: %+v %v", page, err)
	}
	sig.DedupKey = ""
	if _, err = feed.AppendOnce(sig); err == nil {
		t.Fatal("accepted missing identity")
	}
	page, err = feed.ListAfter(0, 100, SignalFilter{Account: "a"})
	if err != nil || page.LatestSeq != 3 {
		t.Fatal("rejection changed feed")
	}
}
