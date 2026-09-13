package tool

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: RetainRecoverySource must publish exact inspected bytes while the
// owning parent is executing a later run, without changing the stopped child's
// staged/unstaged split. Real V3 mutations and managed Git are the narrowest
// boundary proving resumed-run publication and persisted-source consumption.
func TestRecoverySourceResumedParent(t *testing.T) {
	f := newRecoveryFixture(t, true, true, true)
	req := RecoverySourceRequest{TaskCallID: "mixed-wave", ChildSessionID: "recovery-dirty", Paths: []string{"change.txt"}}
	if err := f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: req.ChildSessionID, Phase: "failed", EndedAt: 10, Generation: 1}); err != nil {
		t.Fatal(err)
	}
	recoveryGit(t, f.dirtyPath, "add", "change.txt")
	index := recoveryGit(t, f.dirtyPath, "show", ":change.txt")
	if err := os.WriteFile(filepath.Join(f.dirtyPath, "change.txt"), []byte("unstaged recovery"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ run, status string }{{"original-parent", pebblestore.V3RunIntentPendingExecutor}, {"original-parent", pebblestore.V3RunIntentCompleted}, {"resumed-parent", pebblestore.V3RunIntentPendingExecutor}} {
		_, err := f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{SessionID: f.scope.SessionID, UserID: "user", AccountScopeID: "account", Kind: pebblestore.V3SessionMutationRecordRunIntent, RunIntent: &pebblestore.V3SessionRunIntent{RunID: step.run, Status: step.status}, IdempotencyKey: step.run + step.status, RequestHash: step.run + step.status})
		if err != nil {
			t.Fatal(err)
		}
	}
	source, err := f.runtime.InspectRecoverySource(f.scope, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ExpectedDigest = source.Digest()
	for _, mode := range []string{"stale", "foreign", "busy"} {
		bad, scope := req, f.scope
		if mode == "stale" {
			bad.ExpectedDigest = strings.Repeat("0", 64)
		}
		if mode == "foreign" {
			scope.Principal.AccountScopeID = "foreign"
		}
		if mode == "busy" {
			if err := f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: req.ChildSessionID, Active: true, Phase: "running", Generation: 2}); err != nil {
				t.Fatal(err)
			}
		}
		before, _, _ := f.sessions.GetSession(f.scope.SessionID)
		events, _ := f.sessions.ListSessionEventsBefore(f.scope.SessionID, 0, 1)
		if _, err := f.runtime.RetainRecoverySource(scope, bad); err == nil {
			t.Fatalf("%s accepted", mode)
		}
		after, _, _ := f.sessions.GetSession(f.scope.SessionID)
		latest, _ := f.sessions.ListSessionEventsBefore(f.scope.SessionID, 0, 1)
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(events, latest) {
			t.Fatal("rejection published state")
		}
	}
	if err := f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: req.ChildSessionID, Phase: "failed", EndedAt: 20, Generation: 3}); err != nil {
		t.Fatal(err)
	}
	source, err = f.runtime.InspectRecoverySource(f.scope, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ExpectedDigest = source.Digest()
	if _, err := f.runtime.RetainRecoverySource(f.scope, req); err != nil {
		t.Fatal(err)
	}
	got, err := f.runtime.ReadRecoverySource(f.scope, req.ExpectedDigest)
	if err != nil || len(got.Files) != 1 || got.Files[0].Content != "unstaged recovery" {
		t.Fatalf("retained source: %+v %v", got, err)
	}
	destination := t.TempDir()
	if err := MaterializeRecoverySource(destination, got, []string{"change.txt"}); err != nil {
		t.Fatal(err)
	}
	materialized, err := os.ReadFile(filepath.Join(destination, "change.txt"))
	if err != nil || string(materialized) != "unstaged recovery" {
		t.Fatal("retained bytes not consumed")
	}
	if recoveryGit(t, f.dirtyPath, "show", ":change.txt") != index {
		t.Fatal("source index changed")
	}
	body, err := os.ReadFile(filepath.Join(f.dirtyPath, "change.txt"))
	if err != nil || string(body) != "unstaged recovery" {
		t.Fatal("source working bytes changed")
	}
}
