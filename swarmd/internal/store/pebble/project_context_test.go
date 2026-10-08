package pebblestore

import (
	"errors"
	"sync"
	"testing"
)

// Purpose: CreateProjectWithContext/ClaimProjectContext/FinishProjectContext own
// atomic creation, single launch and attempt-fenced publication. This store test
// proves concurrency, account isolation, restart and late-result rejection without
// provider timing; it is not evidence of live AI execution.
func TestProjectContextLifecycle(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	p, created, err := s.CreateProjectWithContext("account", "request", "hash", ProjectRecord{Name: "Project", ProjectContext: "untrusted supplied success"})
	if err != nil || !created || p.ProjectContext != "" || p.ContextGeneration.Status != "pending" {
		t.Fatalf("create: %+v %v", p, err)
	}
	var wg sync.WaitGroup
	wins := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ClaimProjectContext("account", p.ID, 0, false); err == nil {
				wins <- 1
			} else if !errors.Is(err, ErrProjectContextStale) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(wins)
	if len(wins) != 1 {
		t.Fatalf("claims=%d", len(wins))
	}
	replay, newRecord, err := s.CreateProjectWithContext("account", "request", "hash", ProjectRecord{Name: "Project"})
	if err != nil || newRecord || replay.ID != p.ID || replay.ContextGeneration.Attempt != 1 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, _, err := s.CreateProjectWithContext("account", "request", "changed", ProjectRecord{Name: "Overwrite"}); !errors.Is(err, ErrProjectCreationConflict) {
		t.Fatal(err)
	}
	if _, err := s.FinishProjectContext("foreign", p.ID, 1, "stolen", "", ""); err == nil {
		t.Fatal("cross-account finish allowed")
	}
	if _, err := s.FinishProjectContext("account", p.ID, 1, "", "provider failed", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	recovered, found, err := s.GetProject("account", p.ID)
	if err != nil || !found || recovered.ContextGeneration.Status != "failed" || recovered.ProjectContext != "" {
		t.Fatalf("restart: %+v %v", recovered, err)
	}
	if _, err := s.ClaimProjectContext("account", p.ID, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishProjectContext("account", p.ID, 1, "late", "", ""); !errors.Is(err, ErrProjectContextStale) {
		t.Fatal(err)
	}
	// Emulate a crashed job's expired lease, then prove a late result cannot win.
	if _, err := s.UpdateProject("account", p.ID, func(p *ProjectRecord) error { p.ContextGeneration.LeaseUntil = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimProjectContext("account", p.ID, 2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishProjectContext("account", p.ID, 2, "late", "", ""); !errors.Is(err, ErrProjectContextStale) {
		t.Fatal(err)
	}
	final, err := s.FinishProjectContext("account", p.ID, 3, "# Generated document", "", "fallback warning")
	if err != nil || final.ProjectContext != "# Generated document" || final.ContextGeneration.Status != "ready" || final.ContextGeneration.RouterAlert != "fallback warning" {
		t.Fatalf("finish: %+v %v", final, err)
	}
	if _, err := s.ClaimProjectContext("account", p.ID, 3, true); !errors.Is(err, ErrProjectContextStale) {
		t.Fatal(err)
	}
	all, err := s.ListProjects("account", 100)
	if err != nil || len(all) != 1 || all[0].Name != "Project" {
		t.Fatalf("postconditions: %+v %v", all, err)
	}
}
