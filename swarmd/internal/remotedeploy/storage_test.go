package remotedeploy

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteDeployCacheRootUsesSystemCacheRoot(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	cacheRoot := filepath.Join(t.TempDir(), "swarmd-cache")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CACHE_DIRECTORY", cacheRoot)

	got, err := remoteDeployCacheRoot()
	if err != nil {
		t.Fatalf("remoteDeployCacheRoot: %v", err)
	}
	want := filepath.Join(cacheRoot, "remote-deploy")
	if got != want {
		t.Fatalf("remoteDeployCacheRoot = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, home) {
		t.Fatalf("remote deploy cache root %q is under HOME", got)
	}
}
