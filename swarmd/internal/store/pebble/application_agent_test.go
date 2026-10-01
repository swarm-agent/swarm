package pebblestore

import (
	"errors"
	"sync"
	"testing"
)

func TestApplicationAgentRevisionIsolationAndRestart(t *testing.T) {
	// Purpose: Put/GetApplicationAgent must retain immutable context, reject stale
	// writes and isolate principals. Real Pebble reopen is the narrowest durability
	// boundary; retries and concurrent writers must not corrupt the latest pointer.
	path := t.TempDir()
	db, err := Open(path)
	if err != nil { t.Fatal(err) }
	store := NewSessionStore(db)
	input := ApplicationAgent{ID:"editor", Name:"Editor", Instructions:"Draft only", Context:"Original context"}
	first, err := store.PutApplicationAgent("account", "user", input, 0)
	if err != nil || first.Revision != 1 { t.Fatalf("create: %+v %v",first,err) }
	retry, err := store.PutApplicationAgent("account", "user", input, 0)
	if err != nil || retry != first { t.Fatalf("retry: %+v %v",retry,err) }
	input.Context = "Updated context"
	var wg sync.WaitGroup
	results := make(chan error,2)
	for _, name := range []string{"One", "Two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			candidate := input
			candidate.Name = name
			_, err := store.PutApplicationAgent("account","user",candidate,1)
			results <- err
		}(name)
	}
	wg.Wait(); close(results)
	success, conflicts := 0,0
	for err := range results {
		if err == nil { success++ } else if errors.Is(err,ErrApplicationAgentConflict) { conflicts++ } else { t.Fatal(err) }
	}
	if success != 1 || conflicts != 1 { t.Fatalf("concurrency: %d success %d conflicts",success,conflicts) }
	if _, err := store.PutApplicationAgent("account","user",input,0); !errors.Is(err,ErrApplicationAgentConflict) { t.Fatalf("stale write: %v",err) }
	if err := db.Close(); err != nil { t.Fatal(err) }
	db, err = Open(path)
	if err != nil { t.Fatal(err) }
	defer db.Close()
	store = NewSessionStore(db)
	old, found, err := store.GetApplicationAgent("account","user","editor",1)
	if err != nil || !found || old != first { t.Fatalf("immutable revision: %+v %v",old,err) }
	latest, found, err := store.GetApplicationAgent("account","user","editor",0)
	if err != nil || !found || latest.Revision != 2 || latest.Context != "Updated context" { t.Fatalf("latest: %+v %v",latest,err) }
	for _, principal := range [][2]string{{"other","user"},{"account","other"}} {
		if _, found, err := store.GetApplicationAgent(principal[0],principal[1],"editor",1); err != nil || found { t.Fatalf("cross principal: %v %v",found,err) }
	}
}
