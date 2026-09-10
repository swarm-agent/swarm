package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/identity"
)

// Requirement: the deterministic baseline service must commit only reviewed,
// explicitly selected bytes and preserve the real index, working files and
// existing history. Threats: silent secret inclusion, stale/cross-account consent,
// duplicate commits after response loss, and symlink/pathspec expansion. Real
// temporary Git repositories are the narrowest layer proving tree/index effects.
func TestRepositoryBaselineSelectionConsentAndRetry(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, unborn := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonrepo", true: "unborn"}[unborn], func(t *testing.T) {
			store, cleanup := newTestWorkspaceStore(t)
			defer cleanup()
			svc := NewService(store)
			path := t.TempDir()
			os.WriteFile(filepath.Join(path, "code.txt"), []byte("selected"), 0600)
			os.WriteFile(filepath.Join(path, ".env"), []byte("fixture-not-a-secret"), 0600)
			var before []byte
			if unborn {
				if _, e := runRepositoryGit(path, "init", "--initial-branch=main", "--template="); e != nil {
					t.Fatal(e)
				}
				if _, e := runRepositoryGit(path, "add", ".env"); e != nil {
					t.Fatal(e)
				}
				before, _ = os.ReadFile(filepath.Join(path, ".git", "index"))
			}
			review, e := svc.ReviewRepositoryForPrincipal(testPrincipal(), path)
			if e != nil {
				t.Fatal(e)
			}
			req := RepositoryBaselineRequest{Path: path, ExpectedResolvedPath: path, ReviewDigest: review.Digest, SelectedPaths: []string{"code.txt"}, ConfirmBaseline: true}
			if _, e = svc.PrepareRepositoryBaselineForPrincipal(testPrincipal(), req); e == nil {
				t.Fatal("omission accepted without consent")
			}
			req.ConfirmOmissions = true
			foreign := testPrincipal()
			foreign.AccountScopeID = "other-account"
			if _, e = svc.PrepareRepositoryBaselineForPrincipal(foreign, req); e == nil {
				t.Fatal("foreign review accepted")
			}
			var wg sync.WaitGroup
			results := make(chan RepositoryState, 2)
			errs := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					state, e := svc.PrepareRepositoryBaselineForPrincipal(testPrincipal(), req)
					results <- state
					errs <- e
				}()
			}
			wg.Wait()
			close(results)
			close(errs)
			for e := range errs {
				if e != nil {
					t.Fatal(e)
				}
			}
			head := ""
			for state := range results {
				if state.State != RepositoryStateReady || state.ContentReady {
					t.Fatalf("state=%+v", state)
				}
				if head != "" && head != state.HeadCommit {
					t.Fatal("duplicate commit")
				}
				head = state.HeadCommit
			}
			count, e := runRepositoryGit(path, "rev-list", "--count", "HEAD")
			if e != nil || count != "1" {
				t.Fatalf("count=%q %v", count, e)
			}
			tree, e := runRepositoryGit(path, "ls-tree", "-r", "--name-only", "HEAD")
			if e != nil || tree != "code.txt" {
				t.Fatalf("tree=%q %v", tree, e)
			}
			contents, e := runRepositoryGit(path, "show", "HEAD:code.txt")
			if e != nil || contents != "selected" {
				t.Fatalf("content=%q %v", contents, e)
			}
			after, e := os.ReadFile(filepath.Join(path, ".git", "index"))
			if unborn && (e != nil || string(before) != string(after)) {
				t.Fatal("index changed")
			}
			if !unborn && !os.IsNotExist(e) {
				t.Fatal("created user index")
			}
			data, e := os.ReadFile(filepath.Join(path, ".env"))
			if e != nil || string(data) != "fixture-not-a-secret" {
				t.Fatal("omitted file changed")
			}
			entries, e := svc.ListKnownForPrincipal(testPrincipal(), 10)
			if e != nil || len(entries) != 0 {
				t.Fatal("preparation saved catalog")
			}
			fresh, e := svc.ReviewRepositoryForPrincipal(testPrincipal(), path)
			if e != nil {
				t.Fatal(e)
			}
			req.ReviewDigest = fresh.Digest
			if _, e = svc.PrepareRepositoryBaselineForPrincipal(testPrincipal(), req); e == nil {
				t.Fatal("existing history modified")
			}
		})
	}
}

// Requirement: stale reviews, symlinks, unreviewed names, and absent principal
// fail before repository creation. Verify both the error and filesystem/catalog
// postconditions at the explicit baseline service mutation boundary.
func TestRepositoryBaselineRejectsUnsafeAndStaleReview(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := t.TempDir()
	os.WriteFile(filepath.Join(path, "file"), []byte("a"), 0600)
	os.Symlink(t.TempDir(), filepath.Join(path, "escape"))
	review, e := svc.ReviewRepositoryForPrincipal(testPrincipal(), path)
	if e != nil {
		t.Fatal(e)
	}
	base := RepositoryBaselineRequest{Path: path, ExpectedResolvedPath: path, ReviewDigest: review.Digest, SelectedPaths: []string{"file"}, ConfirmBaseline: true, ConfirmOmissions: true}
	for _, names := range [][]string{{"escape"}, {"../file"}, {"*"}, {"file", "file"}} {
		req := base
		req.SelectedPaths = names
		if _, e := svc.PrepareRepositoryBaselineForPrincipal(testPrincipal(), req); e == nil {
			t.Fatalf("accepted %v", names)
		}
	}
	if _, e := svc.PrepareRepositoryBaselineForPrincipal(identity.Principal{}, base); e == nil {
		t.Fatal("unauthenticated mutation")
	}
	os.WriteFile(filepath.Join(path, "file"), []byte("changed"), 0600)
	if _, e := svc.PrepareRepositoryBaselineForPrincipal(testPrincipal(), base); e == nil || !strings.Contains(e.Error(), "stale") {
		t.Fatalf("stale err=%v", e)
	}
	if _, e := os.Lstat(filepath.Join(path, ".git")); !os.IsNotExist(e) {
		t.Fatal("rejection wrote metadata")
	}
	entries, e := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if e != nil || len(entries) != 0 {
		t.Fatal("rejection saved catalog")
	}
}

// Requirement: trust, access, malformed metadata and command failures are not
// absence. A locale-pinned fake Git deterministically exercises classification;
// rejecting setup must leave the selected empty folder and catalog untouched.
func TestRepositoryInspectionPreservesGitFailures(t *testing.T) {
	for _, tc := range []struct{ message, state string }{{"fatal: detected dubious ownership in repository", RepositoryStateTrustRequired}, {"fatal: Permission denied", RepositoryStateAccessDenied}, {"fatal: invalid gitfile format", RepositoryStateError}, {"", RepositoryStateError}} {
		t.Run(tc.state+tc.message, func(t *testing.T) {
			store, cleanup := newTestWorkspaceStore(t)
			defer cleanup()
			svc := NewService(store)
			path := t.TempDir()
			bin := t.TempDir()
			os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf '%s\\n' '"+tc.message+"' >&2\nexit 128\n"), 0700)
			t.Setenv("PATH", bin)
			state, e := svc.InspectRepositoryForPrincipal(testPrincipal(), path)
			if e != nil || state.State != tc.state || state.CanSetup {
				t.Fatalf("%+v %v", state, e)
			}
			if _, e := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path); e == nil {
				t.Fatal("setup accepted command failure")
			}
			entries, e := os.ReadDir(path)
			if e != nil || len(entries) != 0 {
				t.Fatal("setup wrote metadata")
			}
		})
	}
}

// Requirement: explicit setup may create a project outside daemon HOME, while
// exact canonical consent, effective-UID access and retry uniqueness remain
// mandatory. Real filesystem/Git assertions prove no implicit enrollment.
func TestRepositorySetupOutsideHomeAndResume(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := filepath.Join(t.TempDir(), "project")
	state, e := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path)
	if e != nil {
		t.Fatal(e)
	}
	retry, e := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path)
	if e != nil || state.HeadCommit != retry.HeadCommit {
		t.Fatalf("retry=%+v %v", retry, e)
	}
	if !state.RuntimeAccessible || !state.ContentReady {
		t.Fatalf("state=%+v", state)
	}
	entries, e := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if e != nil || len(entries) != 0 {
		t.Fatal("setup saved catalog")
	}
}

// Requirement: failed HEAD publication must leave content and index intact,
// permit exact review retry after service reconstruction, and never save catalog.
// A real Git HEAD lock models interruption/concurrency at the actual CAS boundary.
func TestRepositoryBaselinePublicationFailureResumes(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	path := t.TempDir()
	if _, err := runRepositoryGit(path, "init", "--initial-branch=main", "--template="); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(path, "file"), []byte("preserved"), 0600)
	review, err := svc.ReviewRepositoryForPrincipal(testPrincipal(), path)
	if err != nil {
		t.Fatal(err)
	}
	req := RepositoryBaselineRequest{Path: path, ExpectedResolvedPath: path, ReviewDigest: review.Digest, SelectedPaths: []string{"file"}, ConfirmBaseline: true}
	lock := filepath.Join(path, ".git", "HEAD.lock")
	if err := os.WriteFile(lock, []byte("fixture lock"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PrepareRepositoryBaselineForPrincipal(testPrincipal(), req); err == nil {
		t.Fatal("publication ignored lock")
	}
	if _, err := runRepositoryGit(path, "rev-parse", "--verify", "HEAD"); err == nil {
		t.Fatal("partial publication changed HEAD")
	}
	if _, err := os.Lstat(filepath.Join(path, ".git", "index")); !os.IsNotExist(err) {
		t.Fatal("failure changed user index")
	}
	data, err := os.ReadFile(filepath.Join(path, "file"))
	if err != nil || string(data) != "preserved" {
		t.Fatal("failure changed content")
	}
	entries, err := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if err != nil || len(entries) != 0 {
		t.Fatal("failure saved catalog")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	state, err := NewService(store).PrepareRepositoryBaselineForPrincipal(testPrincipal(), req)
	if err != nil || state.State != RepositoryStateReady {
		t.Fatalf("resume=%+v %v", state, err)
	}
	again, err := NewService(store).PrepareRepositoryBaselineForPrincipal(testPrincipal(), req)
	if err != nil || again.HeadCommit != state.HeadCommit {
		t.Fatalf("response-loss retry=%+v %v", again, err)
	}
}

// Requirement: effective-UID ACL-aware access, not terminal/root assumptions,
// governs setup. An inaccessible parent must reject before directory mutation.
func TestRepositoryRuntimeAccessRejectsUnwritableParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses DAC; non-root daemon fixture required")
	}
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	parent := t.TempDir()
	path := filepath.Join(parent, "project")
	if err := os.Chmod(parent, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0700)
	state, err := svc.SetupRepositoryForPrincipal(testPrincipal(), path, path)
	if err == nil || state.State != RepositoryStateAccessDenied {
		t.Fatalf("access=%+v %v", state, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("denied creation changed parent")
	}
	entries, err := svc.ListKnownForPrincipal(testPrincipal(), 10)
	if err != nil || len(entries) != 0 {
		t.Fatal("denied creation saved catalog")
	}
}
