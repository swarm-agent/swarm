package pebblestore

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Purpose: a failed cancel callback must durably fence recovered starts while
// preserving account isolation. WithAutomationExecutionFence owns this boundary;
// reopening a temporary Pebble store proves more than an in-memory host mock.
func TestAutomationExecutionFenceSurvivesFailedCancelAndRestart(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path); if err != nil { t.Fatal(err) }
	key := strings.Repeat("a", 64)
	failure := errors.New("injected lifecycle failure")
	if err := NewSessionStore(db).WithAutomationExecutionFence("account", key, true, func() error { return failure }); !errors.Is(err, failure) { t.Fatal(err) }
	if err := db.Close(); err != nil { t.Fatal(err) }
	db, err = Open(path); if err != nil { t.Fatal(err) }; defer db.Close()
	called := false
	err = NewSessionStore(db).WithAutomationExecutionFence("account", key, false, func() error { called = true; return nil })
	if !errors.Is(err, ErrAutomationExecutionCancelled) || called { t.Fatalf("cancel lost: err=%v called=%v", err, called) }
	if err := NewSessionStore(db).WithAutomationExecutionFence("other", key, false, func() error { called = true; return nil }); err != nil || !called { t.Fatalf("account isolation: %v", err) }
}

// Purpose: cancellation and admission using different host wrappers must share
// one store lock; once cancellation returns no racing or later start may enter.
func TestAutomationExecutionFenceCancelRace(t *testing.T) {
	db, err := Open(t.TempDir()); if err != nil { t.Fatal(err) }; defer db.Close()
	key := strings.Repeat("c", 64)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { done <- NewSessionStore(db).WithAutomationExecutionFence("account", key, true, func() error { close(entered); <-release; return nil }) }()
	select { case <-entered: case <-time.After(time.Second): t.Fatal("cancel did not enter") }
	start := make(chan error, 1)
	go func() { start <- NewSessionStore(db).WithAutomationExecutionFence("account", key, false, func() error { return errors.New("unauthorized callback") }) }()
	close(release)
	select { case err := <-done: if err != nil { t.Fatal(err) }; case <-time.After(time.Second): t.Fatal("cancel timeout") }
	select { case err := <-start: if !errors.Is(err, ErrAutomationExecutionCancelled) { t.Fatalf("race admitted: %v", err) }; case <-time.After(time.Second): t.Fatal("start timeout") }
}
