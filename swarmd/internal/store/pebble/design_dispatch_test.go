package pebblestore

import (
	"fmt"
	"path/filepath"
	"testing"
)

// Purpose: daemon startup must discover pending requests across owners without
// returning context/history bytes or depending on a transient wake. Real Pebble
// reopen and pagination are the narrow durable-index contract, not an AI benchmark.
func TestDesignDispatchDiscoveryReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		owner := DesignPrincipal{AccountID: fmt.Sprintf("account-%03d", i), PrincipalID: "owner"}
		if _, err = db.SubmitDesignRequest(owner, designTestSubmit("request", DesignHTML)); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cursor := ""
	count := 0
	pages := 0
	for {
		rows, next, err := db.ScanDesignPending(cursor)
		if err != nil {
			t.Fatal(err)
		}
		count += len(rows)
		pages++
		for _, row := range rows {
			if row.State != DesignQueued {
				t.Fatal("lost queued work")
			}
		}
		if next == "" {
			break
		}
		if next == cursor || pages > 10 {
			t.Fatal("unbounded/repeated cursor")
		}
		cursor = next
	}
	if count != 100 || pages < 2 {
		t.Fatalf("discovery count=%d pages=%d", count, pages)
	}
	if _, _, err = db.ScanDesignPending("outside-prefix"); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	wake := db.DesignWake()
	db.WakeDesign()
	db.WakeDesign()
	select {
	case <-wake:
	default:
		t.Fatal("missing wake")
	}
	select {
	case <-wake:
		t.Fatal("wake not coalesced")
	default:
	}
}

// Purpose: history growth must not force discovery to walk content keys, and
// terminal pre-allocation failures must remove pending work without attempts.
// Real Pebble seeks and owner checks are the narrow persistence boundary.
func TestDesignDispatchDiscoverySkipsHistoryAndFailureAuthority(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := DesignPrincipal{AccountID: "account", PrincipalID: "owner"}
	r, err := db.SubmitDesignRequest(p, designTestSubmit("request", DesignHTML))
	if err != nil {
		t.Fatal(err)
	}
	b := db.db.NewBatch()
	defer b.Close()
	for i := 0; i < 600; i++ {
		if err := b.Set([]byte(designKey(p, "context", fmt.Sprintf("old-%04d", i))), []byte("not decoded"), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err = b.Commit(nil); err != nil {
		t.Fatal(err)
	}
	rows, next, err := db.ScanDesignPending("")
	if err != nil || len(rows) != 1 || next != "" {
		t.Fatalf("walked historical content: %d %q %v", len(rows), next, err)
	}
	foreign := p
	foreign.PrincipalID = "foreign"
	if _, err = db.FailQueuedDesign(foreign, r.ID, r.Revision, 0, "model_unavailable"); err == nil {
		t.Fatal("foreign failure accepted")
	}
	if _, err = db.FailQueuedDesign(p, r.ID, r.Revision+1, 0, "model_unavailable"); err == nil {
		t.Fatal("stale failure accepted")
	}
	unchanged, _ := db.GetDesignRequest(p, r.ID)
	if unchanged.Revision != r.Revision || unchanged.State != DesignQueued {
		t.Fatal("rejection mutated request")
	}
	got, err := db.FailQueuedDesign(p, r.ID, r.Revision, 0, "model_unavailable")
	if err != nil || got.State != DesignFailed || len(got.Candidates[0].Attempts) != 0 || got.Candidates[0].RouterAlert == "" {
		t.Fatalf("failure: %+v %v", got, err)
	}
	rows, _, err = db.ScanDesignPending("")
	if err != nil || len(rows) != 0 {
		t.Fatal("terminal request remains pending", err)
	}
}
