package artifact

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLibraryRoot(t *testing.T) {
	cacheRoot := t.TempDir()
	got, err := ResolveLibraryRoot("", cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cacheRoot, "swarm", "artifacts"); got != want {
		t.Fatalf("default library = %q, want %q", got, want)
	}
	custom := filepath.Join(cacheRoot, "Creative Library")
	if got, err := ResolveLibraryRoot(custom, ""); err != nil || got != custom {
		t.Fatalf("custom library = %q, err=%v", got, err)
	}
	if _, err := ResolveLibraryRoot("relative", cacheRoot); err == nil {
		t.Fatal("relative custom library was accepted")
	}
	if _, err := ResolveLibraryRoot("", ""); err == nil {
		t.Fatal("missing custom library and cache root was accepted")
	}
}

func TestEnsureLibraryRootRejectsSymlinkComponents(t *testing.T) {
	base := t.TempDir()
	realDirectory := filepath.Join(base, "real")
	if err := os.Mkdir(realDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDirectory, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLibraryRoot(filepath.Join(link, "Artifacts")); err == nil {
		t.Fatal("symlinked library path was accepted")
	}
	root := filepath.Join(base, "Swarm", "Artifacts")
	if err := EnsureLibraryRoot(root); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("library root was not created: info=%v err=%v", info, err)
	}
}
