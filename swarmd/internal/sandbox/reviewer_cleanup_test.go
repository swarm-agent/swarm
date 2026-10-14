package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Purpose: CleanupReviewValidated must validate before shutdown, block every
// acquisition during removal, retain restrictive state on failure, and remove
// container/home resources on retry. A recording engine proves ordering and
// absence of recreation; Docker proof additionally verifies actual resources.
func TestReviewerCleanupNoReacquisition(t *testing.T) {
	runner := readyRunner()
	runner.out["volume ls"] = "review-home\n"
	m := newTestManager(t, ModeRequired, runner, Config{})
	checkout, source := t.TempDir(), t.TempDir()
	scope := Scope{Root: source, ReviewCheckout: checkout}
	name := scopeContainerName(scope)
	m.reviews = map[string]string{checkout: source}
	m.boxes[name] = &box{name: name, root: source}
	validated := 0
	validate := func() error {
		validated++
		if runner.ran("rm -f " + name) {
			t.Fatal("validation after shutdown")
		}
		return nil
	}
	failure := errors.New("removal failed")
	remove := func() error {
		if !runner.ran("rm -f " + name) {
			t.Fatal("removal before shutdown")
		}
		if _, err := m.acquire(context.Background(), scope, "git"); err == nil || !strings.Contains(err.Error(), "closing") {
			t.Fatal("acquisition not closed", err)
		}
		return failure
	}
	if err := m.CleanupReviewValidated(context.Background(), checkout, validate, remove); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if m.reviews[checkout] != source || runner.ran("volume rm") {
		t.Fatal("failure lost restrictive state")
	}
	if err := m.CleanupReviewValidated(context.Background(), checkout, validate, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if validated != 1 || m.reviews[checkout] != "" || m.boxes[name] != nil || !runner.ran("volume rm "+name+"-home") {
		t.Fatal("incomplete lifecycle", validated, runner.calls)
	}
	if runner.ran("run ") {
		t.Fatal("container recreated", runner.calls)
	}
	if _, err := m.acquire(context.Background(), scope, "git"); err == nil {
		t.Fatal("terminal acquisition accepted")
	}
}
