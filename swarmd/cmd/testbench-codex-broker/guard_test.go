package main

// Purpose: guardedRequest prevents replay of rotating credentials after uncertain
// execution/persistence. The real filesystem guard is the narrowest crash marker
// boundary; injected request failure must retain the guard and prohibit retry.
import (
	"errors"
	"os"
	"path/filepath"
	"swarm/packages/swarmd/internal/provider/codex"
	"testing"
)

func TestGuardRetainsFailureAndRejectsReplay(t *testing.T) {
	root := t.TempDir()
	calls := 0
	execute := func() (codex.Response, error) { calls++; return codex.Response{}, errors.New("injected failure") }
	if _, err := guardedRequest(root, execute); err == nil {
		t.Fatal("failure swallowed")
	}
	if _, err := guardedRequest(root, execute); err == nil || calls != 1 {
		t.Fatal("uncertain request replayed")
	}
	info, err := os.Stat(filepath.Join(root, "request-in-flight"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private guard missing")
	}
}
func TestGuardSuccessRemovesMarker(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 2; i++ {
		if _, err := guardedRequest(root, func() (codex.Response, error) { return codex.Response{ID: "ok"}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "request-in-flight")); !os.IsNotExist(err) {
		t.Fatal("successful request left guard")
	}
}
