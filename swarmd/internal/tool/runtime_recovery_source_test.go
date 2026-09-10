package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func recoverySourceFixture(t *testing.T) (recoveryFixture, RecoverySourceRequest) {
	t.Helper()
	f := newRecoveryFixture(t)
	parent, _, err := f.sessions.GetSession(f.scope.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{SessionID: parent.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, Kind: pebblestore.V3SessionMutationUpdateMetadata, Session: &parent, IdempotencyKey: "source-fixture", RequestHash: "source-fixture"}); err != nil {
		t.Fatal(err)
	}
	err = f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: "recovery-dirty", UserID: "user", AccountScopeID: "account", Phase: "failed", EndedAt: 10, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	return f, RecoverySourceRequest{TaskCallID: "mixed-wave", ChildSessionID: "recovery-dirty", Paths: []string{"change.txt"}}
}

// Purpose: Inspect/Retain/ReadRecoverySource must bind exact selected bytes to
// owned stopped-child lineage without mutating that child's index, HEAD, files
// or failed lifecycle. Real managed Git plus the session V3 store is the narrowest
// boundary proving durable publication and stale-source rejection together.
func TestRecoverySourceRetainsExactBytes(t *testing.T) {
	f, req := recoverySourceFixture(t)
	before := recoveryGit(t, f.dirtyPath, "status", "--porcelain")
	index := recoveryGit(t, f.dirtyPath, "ls-files", "--stage")
	childBefore, _, _ := f.sessions.GetLifecycle(req.ChildSessionID)
	source, err := f.runtime.InspectRecoverySource(f.scope, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ExpectedDigest = source.Digest()
	retained, err := f.runtime.RetainRecoverySource(f.scope, req)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Digest() != req.ExpectedDigest {
		t.Fatal("wrong retained identity")
	}
	foreign := f.scope
	foreign.Principal.AccountScopeID = "foreign"
	if _, err := f.runtime.ReadRecoverySource(foreign, req.ExpectedDigest); err == nil {
		t.Fatal("foreign retained read accepted")
	}
	if _, err := f.runtime.ReadRecoverySource(f.scope, strings.Repeat("0", 64)); err == nil {
		t.Fatal("unknown retained reference accepted")
	}
	// A new runtime reads persisted bytes, not an in-memory source cache.
	runtime := &Runtime{sessions: f.sessions, worktrees: f.runtime.worktrees, workspace: f.workspace}
	got, err := runtime.ReadRecoverySource(f.scope, req.ExpectedDigest)
	if err != nil || len(got.Files) != 1 || got.Files[0].Content != "recovery-dirty" {
		t.Fatalf("retained bytes: %+v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(f.dirtyPath, "change.txt"), []byte("later edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ReadRecoverySource(f.scope, req.ExpectedDigest); err == nil {
		t.Fatal("changed source read accepted")
	}
	parentBefore, _, _ := f.sessions.GetSession(f.scope.SessionID)
	if _, err := runtime.RetainRecoverySource(f.scope, req); err == nil {
		t.Fatal("accepted changed bytes under old digest")
	}
	parentAfter, _, _ := f.sessions.GetSession(f.scope.SessionID)
	a, _ := json.Marshal(parentBefore)
	b, _ := json.Marshal(parentAfter)
	if string(a) != string(b) {
		t.Fatal("stale publication mutated parent")
	}
	childAfter, _, _ := f.sessions.GetLifecycle(req.ChildSessionID)
	if childBefore != childAfter || recoveryGit(t, f.dirtyPath, "rev-parse", "HEAD") != f.base || recoveryGit(t, f.dirtyPath, "ls-files", "--stage") != index || recoveryGit(t, f.dirtyPath, "status", "--porcelain") != before {
		t.Fatal("source Git/lifecycle changed")
	}
	body, _ := os.ReadFile(filepath.Join(f.dirtyPath, "change.txt"))
	if string(body) != "later edit" {
		t.Fatal("recovery overwrote source")
	}
}

// Purpose: authentication, stale identity, traversal/symlink and byte bounds
// must reject before V3 publication. This table exercises the runtime authority
// against real durable lineage and checks both error and unchanged parent state.
func TestRecoverySourceRejectsUnauthorizedAndUnsafe(t *testing.T) {
	cases := []string{"account", "user", "session", "child", "call", "active", "head", "traversal", "absolute", "git", "symlink", "ancestor-symlink", "binary", "invalid-utf8", "large", "count", "duplicate", "missing-digest"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f, req := recoverySourceFixture(t)
			scope := f.scope
			baseline, err := f.runtime.InspectRecoverySource(scope, req)
			if err != nil {
				t.Fatal(err)
			}
			req.ExpectedDigest = baseline.Digest()
			switch name {
			case "account":
				scope.Principal.AccountScopeID = "other"
			case "user":
				scope.Principal.UserID = "other"
			case "session":
				scope.Principal.SessionID = "other"
			case "child":
				req.ChildSessionID = "unrelated"
			case "call":
				req.TaskCallID = "unrelated"
			case "active":
				if err := f.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: req.ChildSessionID, Active: true, Phase: "running", Generation: 2}); err != nil {
					t.Fatal(err)
				}
			case "head":
				recoveryGit(t, f.dirtyPath, "commit", "--allow-empty", "-m", "move")
			case "traversal":
				req.Paths = []string{"../outside"}
			case "absolute":
				req.Paths = []string{filepath.Join(f.dirtyPath, "change.txt")}
			case "git":
				req.Paths = []string{"missing/.git/config"}
			case "symlink", "ancestor-symlink":
				target := filepath.Join(f.dirtyPath, "change.txt")
				if name == "ancestor-symlink" {
					target = f.source
				}
				if err := os.Symlink(target, filepath.Join(f.dirtyPath, "link")); err != nil {
					t.Fatal(err)
				}
				req.Paths = []string{"link"}
				if name == "ancestor-symlink" {
					req.Paths = []string{"link/file"}
				}
			case "binary", "invalid-utf8", "large":
				content := []byte("a\x00b")
				if name == "invalid-utf8" {
					content = []byte{255}
				}
				if name == "large" {
					content = []byte(strings.Repeat("a", recoverySourceMaxBytes+1))
				}
				if err := os.WriteFile(filepath.Join(f.dirtyPath, "change.txt"), content, 0600); err != nil {
					t.Fatal(err)
				}
			case "count":
				req.Paths = make([]string, recoverySourceMaxFiles+1)
			case "duplicate":
				req.Paths = []string{"change.txt", "change.txt"}
			case "missing-digest":
				req.ExpectedDigest = ""
			}
			parent, _, _ := f.sessions.GetSession(f.scope.SessionID)
			before, _ := json.Marshal(parent)
			status := recoveryGit(t, f.dirtyPath, "status", "--porcelain")
			head := recoveryGit(t, f.dirtyPath, "rev-parse", "HEAD")
			if _, err := f.runtime.RetainRecoverySource(scope, req); err == nil {
				t.Fatal("unsafe source accepted")
			}
			parent, _, _ = f.sessions.GetSession(f.scope.SessionID)
			after, _ := json.Marshal(parent)
			if string(before) != string(after) || recoveryGit(t, f.dirtyPath, "status", "--porcelain") != status || recoveryGit(t, f.dirtyPath, "rev-parse", "HEAD") != head {
				t.Fatal("rejection mutated state")
			}
		})
	}
}

// Purpose: relative file selection preserves deletion and executable intent and
// rejects aggregate overflow; the confined reader is the narrowest byte layer.
func TestRecoverySourceFileBoundsAndModes(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(dir, "script"), []byte("hello"), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := readRecoverySourceFile(root, "script", 5)
	if err != nil || file.Content != "hello" || !file.Executable {
		t.Fatalf("file: %+v %v", file, err)
	}
	if _, err := readRecoverySourceFile(root, "script", 4); err == nil {
		t.Fatal("overflow accepted")
	}
	missing, err := readRecoverySourceFile(root, "absent", 0)
	if err != nil || !missing.Deleted {
		t.Fatalf("deletion: %+v %v", missing, err)
	}
}

// Purpose: a concurrent parent mutation must not be lost and an injected store
// failure must not publish partial source. The session-service seam is the
// narrowest layer for deterministically exercising RetainRecoverySource's CAS.
type recoverySourceMutationFailure struct {
	manageSessionService
	fixture  recoveryFixture
	conflict bool
}

func (s recoverySourceMutationFailure) GetLifecycle(id string) (pebblestore.SessionLifecycleSnapshot, bool, error) {
	return s.fixture.sessions.GetLifecycle(id)
}
func (s recoverySourceMutationFailure) TaskProgramRepositoryLanes(id string) ([]pebblestore.TaskProgramRepositoryLane, error) {
	return s.fixture.sessions.TaskProgramRepositoryLanes(id)
}
func (s recoverySourceMutationFailure) ApplySessionMutation(input pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	if !s.conflict {
		return pebblestore.V3SessionMutationResult{}, fmt.Errorf("injected publication failure")
	}
	parent, _, err := s.fixture.sessions.GetSession(input.SessionID)
	if err != nil {
		return pebblestore.V3SessionMutationResult{}, err
	}
	parent.Metadata["concurrent-marker"] = true
	_, err = s.fixture.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{SessionID: parent.ID, AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, Kind: pebblestore.V3SessionMutationUpdateMetadata, Session: &parent, IdempotencyKey: "concurrent", RequestHash: "concurrent"})
	if err != nil {
		return pebblestore.V3SessionMutationResult{}, err
	}
	return s.fixture.sessions.ApplySessionMutation(input)
}
func TestRecoverySourcePublicationFailure(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			f, req := recoverySourceFixture(t)
			source, err := f.runtime.InspectRecoverySource(f.scope, req)
			if err != nil {
				t.Fatal(err)
			}
			req.ExpectedDigest = source.Digest()
			f.runtime.sessions = recoverySourceMutationFailure{manageSessionService: f.sessions, fixture: f, conflict: conflict}
			if _, err := f.runtime.RetainRecoverySource(f.scope, req); err == nil {
				t.Fatal("publication failure accepted")
			}
			parent, _, err := f.sessions.GetSession(f.scope.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := parent.Metadata[recoverySourceMetadataKey]; ok {
				t.Fatal("partial source published")
			}
			if conflict && parent.Metadata["concurrent-marker"] != true {
				t.Fatal("concurrent metadata lost")
			}
			body, err := os.ReadFile(filepath.Join(f.dirtyPath, "change.txt"))
			if err != nil || string(body) != "recovery-dirty" || recoveryGit(t, f.dirtyPath, "rev-parse", "HEAD") != f.base {
				t.Fatal("publication failure mutated source")
			}
		})
	}
}
