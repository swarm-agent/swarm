package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathRejectsDanglingSymlink(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "dangling")
	if err := os.Symlink(filepath.Join(base, "missing-target"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := resolvePath(filepath.Join(link, "child")); err == nil {
		t.Fatal("resolvePath accepted a path beneath a dangling symlink")
	}
}

func TestResolvePathCanonicalizesExistingAncestorForMissingLeaf(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatalf("create real root: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := resolvePath(filepath.Join(link, "missing", "leaf"))
	if err != nil {
		t.Fatalf("resolve prospective path: %v", err)
	}
	want := filepath.Join(realRoot, "missing", "leaf")
	if got != want {
		t.Fatalf("resolved path = %q, want %q", got, want)
	}
}

func TestScopeForPathRevalidatesStoredDirectoryIdentity(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	principal := testPrincipal()
	base := t.TempDir()
	workspaceRoot := filepath.Join(base, "workspace")
	otherRoot := filepath.Join(base, "other")
	if err := os.Mkdir(workspaceRoot, 0o755); err != nil {
		t.Fatalf("create workspace root: %v", err)
	}
	if err := os.Mkdir(otherRoot, 0o755); err != nil {
		t.Fatalf("create other root: %v", err)
	}
	if _, err := svc.AddForPrincipal(principal, workspaceRoot, "Workspace", "", false); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	movedRoot := workspaceRoot + "-old"
	if err := os.Rename(workspaceRoot, movedRoot); err != nil {
		t.Fatalf("move workspace root: %v", err)
	}
	if err := os.Symlink(otherRoot, workspaceRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := svc.ScopeForPathForPrincipal(principal, workspaceRoot)
	if err == nil || !strings.Contains(err.Error(), "no longer resolves to its saved identity") {
		t.Fatalf("scope error = %v, want changed stored identity rejection", err)
	}
}

// Requirement: the account workspace pool authorizes only active saved roots
// that still exist at their exact canonical identity. The regression threat is
// either granting a symlink replacement or making one unavailable saved entry
// break unrelated valid workspace reads. This service test is the narrowest
// layer that proves the catalog-to-root security boundary and its omissions.
func TestAvailableSavedRootsForPrincipalOmitsUnavailableAndIdentityDrift(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	principal := testPrincipal()
	valid := t.TempDir()
	missing := t.TempDir()
	base := t.TempDir()
	drifted := filepath.Join(base, "drifted")
	other := filepath.Join(base, "other")
	if err := os.Mkdir(drifted, 0o755); err != nil {
		t.Fatalf("create drifted workspace: %v", err)
	}
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatalf("create replacement target: %v", err)
	}
	for _, path := range []string{valid, missing, drifted} {
		if _, err := svc.AddForPrincipal(principal, path, filepath.Base(path), "", false); err != nil {
			t.Fatalf("save workspace %q: %v", path, err)
		}
	}
	if err := os.Remove(missing); err != nil {
		t.Fatalf("remove saved workspace: %v", err)
	}
	moved := drifted + "-old"
	if err := os.Rename(drifted, moved); err != nil {
		t.Fatalf("move saved workspace: %v", err)
	}
	if err := os.Symlink(other, drifted); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	roots, err := svc.AvailableSavedRootsForPrincipal(principal)
	if err != nil {
		t.Fatalf("resolve available workspace roots: %v", err)
	}
	if len(roots) != 1 || roots[0] != valid {
		t.Fatalf("available roots = %v, want only %q", roots, valid)
	}
}

func TestCreateFolderForPrincipalUsesCanonicalOpenedParent(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	base := t.TempDir()
	realParent := filepath.Join(base, "real")
	if err := os.Mkdir(realParent, 0o755); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	linkedParent := filepath.Join(base, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := svc.CreateFolderForPrincipal(testPrincipal(), linkedParent, "child")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	want := filepath.Join(realParent, "child")
	if result.Path != want || result.ParentPath != realParent {
		t.Fatalf("result = %+v, want canonical path %q and parent %q", result, want, realParent)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("created folder info=%v err=%v", info, err)
	}
}
