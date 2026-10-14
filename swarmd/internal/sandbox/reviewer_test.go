package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Reviewer scopes must protect all shared Git state and sibling/source
// checkouts, while preserving writable build outputs. Manager.scopeRunArgs is
// the container mount authority; argv inspection is the narrow hermetic layer,
// not proof of Docker/kernel read-only enforcement.
func TestReviewerMounts(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	bucket := t.TempDir()
	checkout := filepath.Join(bucket, "review")
	metadata := filepath.Join(common, "worktrees", "review")
	for _, dir := range []string{metadata, checkout} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: "+metadata+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: Config{UID: 1000, GID: 1000, Image: DefaultImage}, reviews: map[string]string{checkout: root}}
	image := EnvironmentImage{Image: DefaultImage, Path: sandboxPath}
	scope := Scope{Root: root, Mounts: []string{root, bucket}, ReviewCheckout: checkout}
	args := m.scopeRunArgs(scopeContainerName(scope), root, "spec", scope.Mounts, SecretSetup{}, image, checkout)
	joined := strings.Join(args, "\n")
	for _, path := range []string{root, bucket, common, filepath.Join(checkout, ".git")} {
		if !strings.Contains(joined, "type=bind,src="+path+",dst="+path+",readonly") {
			t.Fatalf("missing readonly mount %s: %v", path, args)
		}
	}
	if !strings.Contains(joined, "type=bind,src="+checkout+",dst="+checkout+"\n") {
		t.Fatal("checkout not writable")
	}
	if !strings.Contains(joined, "GIT_OPTIONAL_LOCKS=0") {
		t.Fatal("optional locks enabled")
	}
	coder := m.scopeRunArgs(ContainerName(root), root, "spec", scope.Mounts, SecretSetup{}, image, "")
	if strings.Contains(strings.Join(coder, "\n"), "dst="+root+",readonly") {
		t.Fatal("coder changed")
	}
	if scopeContainerName(scope) == ContainerName(root) {
		t.Fatal("reviewer shares coder container")
	}
	if m.reviewScope(Scope{Root: root}, filepath.Join(checkout, "src")).ReviewCheckout != checkout {
		t.Fatal("review routing lost")
	}
	if m.reviewScope(Scope{Root: root}, root).ReviewCheckout != "" {
		t.Fatal("coder routing changed")
	}
	if _, err := reviewGitMounts(root, root); err == nil {
		t.Fatal("source accepted as disposable checkout")
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: "+t.TempDir()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewGitMounts(checkout, root); err == nil {
		t.Fatal("foreign git dir accepted")
	}
}
