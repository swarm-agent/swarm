package devmode_test

import (
	"os"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/devmode"
)

func TestContainerImageFingerprintChangesWhenStagedBinaryChanges(t *testing.T) {
	root := t.TempDir()
	writeDevImageFixture(t, root)

	first, err := devmode.ContainerImageFingerprint(root)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".bin", "main", "swarmd"), []byte("binary-two"), 0o755); err != nil {
		t.Fatalf("os.WriteFile(swarmd) error = %v", err)
	}
	second, err := devmode.ContainerImageFingerprint(root)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() after change error = %v", err)
	}
	if first == second {
		t.Fatalf("fingerprint did not change after staged binary update: %q", first)
	}
}

func TestResolveRootRequiresCanonicalDevPaths(t *testing.T) {
	root := t.TempDir()
	if _, err := devmode.ResolveRoot(root); err == nil {
		t.Fatal("ResolveRoot() error = nil, want missing path error")
	}
	writeDevImageFixture(t, root)
	resolved, err := devmode.ResolveRoot(root)
	if err != nil {
		t.Fatalf("ResolveRoot() error = %v", err)
	}
	if resolved != filepath.Clean(root) {
		t.Fatalf("ResolveRoot() = %q, want %q", resolved, filepath.Clean(root))
	}
}

func writeDevImageFixture(t *testing.T, root string) {
	t.Helper()
	dirs := []string{
		filepath.Join(root, "scripts"),
		filepath.Join(root, "deploy", "container-mvp"),
		filepath.Join(root, ".bin", "main"),
		filepath.Join(root, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu"),
		filepath.Join(root, "web", "dist"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v", dir, err)
		}
	}
	files := map[string]string{
		filepath.Join(root, "scripts", "rebuild-container.sh"):                                    "#!/usr/bin/env bash\nexit 0\n",
		filepath.Join(root, "deploy", "container-mvp", "Containerfile"):                           "FROM ubuntu:24.04\n",
		filepath.Join(root, "deploy", "container-mvp", "entrypoint.sh"):                           "#!/usr/bin/env bash\n",
		filepath.Join(root, ".bin", "main", "swarmd"):                                             "binary-one",
		filepath.Join(root, ".bin", "main", "swarmctl"):                                           "ctl-one",
		filepath.Join(root, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu", "libfff_c.so"): "fff-one",
		filepath.Join(root, "web", "dist", "index.html"):                                          "<html>one</html>",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", path, err)
		}
	}
}
